package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func newCertificateManagerForTest(t *testing.T) *certManager {
	t.Helper()
	m := &certManager{
		run:    newCommandRun(),
		form:   newParameterForm(nil),
		layout: paths.LayoutForRoot(t.TempDir()),
		phase:  certPhaseList,
	}
	m.reload()
	if m.loadErr != nil {
		t.Fatalf("load certificate manager: %v", m.loadErr)
	}
	return m
}

func drainCertificateRun(t *testing.T, m *certManager, cmd tea.Cmd) {
	t.Helper()
	for steps := 0; cmd != nil && steps < 20; steps++ {
		msg := cmd()
		var done bool
		cmd, done = m.Update(msg)
		if done {
			t.Fatal("certificate run unexpectedly closed its parent flow")
		}
	}
	if m.phase != certPhaseDone {
		t.Fatalf("certificate run did not finish: phase=%d", m.phase)
	}
}

func TestCertificateActionsSeparateAddAndRenew(t *testing.T) {
	m := newCertificateManagerForTest(t)
	view := m.View()
	for _, want := range []string{"Add certificate", "Renew certificate", "Delete certificate", "Manage DNS credentials"} {
		if !strings.Contains(view, want) {
			t.Fatalf("certificate menu missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Add / force renew") {
		t.Fatalf("certificate menu still combines add and renew:\n%s", view)
	}
}

func TestAddCertificateRejectsManagedDomainAndPointsToRenew(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "vpn.example.com"
	if err := certmgr.Register(m.layout, domain, "admin@example.com"); err != nil {
		t.Fatalf("register managed certificate: %v", err)
	}
	m.reload()

	issued := false
	m.issueCertificate = func(context.Context, string, string, io.Writer) error {
		issued = true
		return nil
	}
	m.beginCertFormWithSeed(domain, "new@example.com")
	m.completeForm()

	if m.phase != certPhaseList {
		t.Fatalf("duplicate add phase = %d, want certificate list", m.phase)
	}
	for _, want := range []string{domain, "already managed", "Renew certificate"} {
		if !strings.Contains(m.result, want) {
			t.Fatalf("duplicate-add result missing %q: %q", want, m.result)
		}
	}
	if issued || m.startCmd != nil {
		t.Fatal("duplicate add started an ACME issuance")
	}
}

func TestAddCertificateIssuesAndDistributesWithDistinctResult(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const (
		domain = "new.example.com"
		email  = "admin@example.com"
	)
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: "example.com", Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS credential: %v", err)
	}
	m.reload()

	var calls []string
	m.issueCertificate = func(_ context.Context, gotDomain, gotEmail string, _ io.Writer) error {
		calls = append(calls, "issue:"+gotDomain+":"+gotEmail)
		return nil
	}
	m.distributeCertificate = func(_ context.Context, gotDomain string, _ io.Writer) error {
		calls = append(calls, "distribute:"+gotDomain)
		return nil
	}

	m.beginCertFormWithSeed(domain, email)
	m.completeForm()
	if m.phase != certPhaseRunning || m.startCmd == nil {
		t.Fatalf("new certificate did not start: phase=%d cmd=%v", m.phase, m.startCmd != nil)
	}
	if view := m.View(); !strings.Contains(view, "Adding certificate") {
		t.Fatalf("add running title is not distinct:\n%s", view)
	}
	drainCertificateRun(t, m, m.startCmd)

	wantCalls := []string{"issue:" + domain + ":" + email, "distribute:" + domain}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("add calls = %v, want %v", calls, wantCalls)
	}
	if m.result != "certificate added" {
		t.Fatalf("add result = %q", m.result)
	}
	if view := m.View(); !strings.Contains(view, "Certificate added") {
		t.Fatalf("add completion title is not distinct:\n%s", view)
	}
}

func TestRenewCertificateRequiresExplicitYThenIssuesAndDistributes(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const (
		domain = "managed.example.com"
		email  = "renew@example.com"
	)
	if err := certmgr.Register(m.layout, domain, email); err != nil {
		t.Fatalf("register managed certificate: %v", err)
	}
	m.reload()

	var calls []string
	m.issueCertificate = func(_ context.Context, gotDomain, gotEmail string, _ io.Writer) error {
		calls = append(calls, "issue:"+gotDomain+":"+gotEmail)
		return nil
	}
	m.distributeCertificate = func(_ context.Context, gotDomain string, _ io.Writer) error {
		calls = append(calls, "distribute:"+gotDomain)
		return nil
	}

	m.actionCursor = 1
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseRenewPick {
		t.Fatalf("renew action phase = %d, want picker", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseRenewConfirm {
		t.Fatalf("renew picker phase = %d, want confirmation", m.phase)
	}
	confirm := m.View()
	for _, want := range []string{domain, "forces a new ACME DNS-01 order", "rate limits", "Press y to force renew"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("renew confirmation missing %q:\n%s", want, confirm)
		}
	}

	// Enter is deliberately insufficient for a force renewal.
	cmd, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.phase != certPhaseRenewConfirm || len(calls) != 0 {
		t.Fatalf("Enter bypassed explicit renewal confirmation: phase=%d calls=%v", m.phase, calls)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.phase != certPhaseList || len(calls) != 0 {
		t.Fatalf("n did not cancel renewal: phase=%d calls=%v", m.phase, calls)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseRenewConfirm {
		t.Fatalf("renew confirmation did not reopen: phase=%d", m.phase)
	}

	cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || m.phase != certPhaseRunning {
		t.Fatalf("y did not start renewal: phase=%d cmd=%v", m.phase, cmd != nil)
	}
	if view := m.View(); !strings.Contains(view, "Renewing certificate") {
		t.Fatalf("renew running title is not distinct:\n%s", view)
	}
	drainCertificateRun(t, m, cmd)

	wantCalls := []string{"issue:" + domain + ":" + email, "distribute:" + domain}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("renew calls = %v, want %v", calls, wantCalls)
	}
	if m.result != "certificate renewed" {
		t.Fatalf("renew result = %q", m.result)
	}
	if view := m.View(); !strings.Contains(view, "Certificate renewed") {
		t.Fatalf("renew completion title is not distinct:\n%s", view)
	}
}

func TestRenewCertificateFailureHasRenewalTitleAndSkipsDistribution(t *testing.T) {
	m := newCertificateManagerForTest(t)
	m.distributeCertificate = func(context.Context, string, io.Writer) error {
		t.Fatal("distribution ran after failed renewal issuance")
		return nil
	}
	m.issueCertificate = func(context.Context, string, string, io.Writer) error {
		return fmt.Errorf("ACME order failed")
	}

	m.startCertificateRun(certOperationRenew, "managed.example.com", "renew@example.com")
	drainCertificateRun(t, m, m.startCmd)
	view := m.View()
	for _, want := range []string{"Certificate renewal failed", "ACME order failed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("renew failure view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Certificate addition failed") {
		t.Fatalf("renew failure used the add title:\n%s", view)
	}
}
