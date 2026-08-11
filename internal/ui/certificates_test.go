package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
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
	for _, want := range []string{"Add certificate", "Renew certificate", "Delete certificate", "Manage DNS zones"} {
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
	if err := certmgr.Register(m.layout, domain); err != nil {
		t.Fatalf("register managed certificate: %v", err)
	}
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: "example.com", Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS zone: %v", err)
	}
	m.reload()

	issued := false
	m.issueCertificate = func(context.Context, string, io.Writer) error {
		issued = true
		return nil
	}
	m.beginAddForDomain(domain)
	m.completeForm()

	if m.phase != certPhaseList {
		t.Fatalf("duplicate add phase = %d, want certificate list", m.phase)
	}
	for _, want := range []string{domain, "already managed", "Renew certificate"} {
		if !strings.Contains(m.notice.text, want) {
			t.Fatalf("duplicate-add result missing %q: %q", want, m.notice.text)
		}
	}
	if issued || m.startCmd != nil {
		t.Fatal("duplicate add started an ACME issuance")
	}
}

func TestAddCertificateIssuesAndDistributesWithDistinctResult(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "new.example.com"
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: "example.com", Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS credential: %v", err)
	}
	m.reload()

	var calls []string
	m.issueCertificate = func(_ context.Context, gotDomain string, _ io.Writer) error {
		calls = append(calls, "issue:"+gotDomain)
		return nil
	}
	m.distributeCertificate = func(_ context.Context, gotDomain string, _ io.Writer, _ func(deploy.Event)) error {
		calls = append(calls, "distribute:"+gotDomain)
		return nil
	}

	m.beginAddForDomain(domain)
	m.completeForm()
	if m.phase != certPhaseRunning || m.startCmd == nil {
		t.Fatalf("new certificate did not start: phase=%d cmd=%v", m.phase, m.startCmd != nil)
	}
	if view := m.View(); !strings.Contains(view, "Adding certificate") {
		t.Fatalf("add running title is not distinct:\n%s", view)
	}
	drainCertificateRun(t, m, m.startCmd)

	wantCalls := []string{"issue:" + domain, "distribute:" + domain}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("add calls = %v, want %v", calls, wantCalls)
	}
	if m.notice.text != "added certificate "+domain {
		t.Fatalf("add result = %q", m.notice.text)
	}
	if view := m.View(); !strings.Contains(view, "Certificate added") {
		t.Fatalf("add completion title is not distinct:\n%s", view)
	}
}

func TestRenewCertificateRequiresExplicitYThenIssuesAndDistributes(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "managed.example.com"
	if err := certmgr.Register(m.layout, domain); err != nil {
		t.Fatalf("register managed certificate: %v", err)
	}
	m.reload()

	var calls []string
	m.issueCertificate = func(_ context.Context, gotDomain string, _ io.Writer) error {
		calls = append(calls, "issue:"+gotDomain)
		return nil
	}
	m.distributeCertificate = func(_ context.Context, gotDomain string, _ io.Writer, _ func(deploy.Event)) error {
		calls = append(calls, "distribute:"+gotDomain)
		return nil
	}

	selectCertAction(t, m, actionRenewCertificate)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseRenewPick {
		t.Fatalf("renew action phase = %d, want picker", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseRenewConfirm {
		t.Fatalf("renew picker phase = %d, want confirmation", m.phase)
	}
	confirm := m.View()
	for _, want := range []string{domain, "a new ACME DNS-01 order", "rate limits", "Press y to force renew"} {
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

	wantCalls := []string{"issue:" + domain, "distribute:" + domain}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("renew calls = %v, want %v", calls, wantCalls)
	}
	if m.notice.text != "renewed certificate "+domain {
		t.Fatalf("renew result = %q", m.notice.text)
	}
	if view := m.View(); !strings.Contains(view, "Certificate renewed") {
		t.Fatalf("renew completion title is not distinct:\n%s", view)
	}
}

func TestRenewCertificateFailureHasRenewalTitleAndSkipsDistribution(t *testing.T) {
	m := newCertificateManagerForTest(t)
	m.distributeCertificate = func(context.Context, string, io.Writer, func(deploy.Event)) error {
		t.Fatal("distribution ran after failed renewal issuance")
		return nil
	}
	m.issueCertificate = func(context.Context, string, io.Writer) error {
		return fmt.Errorf("ACME order failed")
	}

	m.startCertificateRun(certOperationRenew, "managed.example.com")
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

func TestCertificateFormsDoNotCollectAnACMEEmail(t *testing.T) {
	m := newCertificateManagerForTest(t)
	forms := map[string]func(){
		"certificate": func() { m.beginHostForm("example.com", "") },
		"DNS zone":    func() { m.beginZoneForm("") },
	}
	for name, begin := range forms {
		begin()
		for _, f := range m.form.fields {
			if f.key == "email" {
				t.Fatalf("%s form still collects an ACME email", name)
			}
		}
		if view := m.View(); strings.Contains(strings.ToLower(view), "acme account email") {
			t.Fatalf("%s form still prompts for an ACME email:\n%s", name, view)
		}
	}
}

func TestCertificateRunAdvancesProgressBarPerDistributionTarget(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "new.example.com"
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: "example.com", Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS credential: %v", err)
	}
	m.reload()

	// A hub reload plus one spoke, so distribution owns two of the three steps.
	m.countDistributionTargets = func(string) int { return 2 }
	m.issueCertificate = func(context.Context, string, io.Writer) error { return nil }
	m.distributeCertificate = func(_ context.Context, _ string, _ io.Writer, progress func(deploy.Event)) error {
		for i, label := range []string{"Reload hub services", "Deliver to tokyo"} {
			for _, status := range []string{"running", "ok"} {
				progress(deploy.Event{Index: i + 1, Total: 2, Label: label, Status: status})
			}
		}
		return nil
	}

	m.beginAddForDomain(domain)
	m.completeForm()
	cmd := m.startCmd
	if cmd == nil {
		t.Fatal("certificate run did not start")
	}

	// Collect the percentages the bar actually renders, collapsing the repeats
	// that the running/ok pair of each step produces.
	var progression []float64
	for steps := 0; cmd != nil && steps < 20; steps++ {
		var done bool
		cmd, done = m.Update(cmd())
		if done {
			t.Fatal("certificate run unexpectedly closed its parent flow")
		}
		if percent := m.run.percent(); len(progression) == 0 || progression[len(progression)-1] != percent {
			progression = append(progression, percent)
		}
	}
	if m.phase != certPhaseDone {
		t.Fatalf("certificate run did not finish: phase=%d", m.phase)
	}

	// Without progress events every one of these would be 0: the ACME order is
	// step 1 of 3 and the fan-out carries the bar the rest of the way to 100%.
	want := []float64{0, 1.0 / 3, 2.0 / 3, 1}
	if len(progression) != len(want) {
		t.Fatalf("progress = %v, want %v", progression, want)
	}
	for i := range want {
		if progression[i] != want[i] {
			t.Fatalf("progress = %v, want %v", progression, want)
		}
	}
}

func TestAddCertificateWithNoConsumersHoldsTheBarUntilIssuanceFinishes(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "new.example.com"
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: "example.com", Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS credential: %v", err)
	}
	m.reload()

	// The common Add case: nothing uses the domain yet, so the ACME order is
	// the whole run. The bar must stay empty until it actually completes.
	m.countDistributionTargets = func(string) int { return 0 }
	// Sentinel: a run that never reports an in-flight issuance step fails the
	// assertion below rather than passing by default.
	duringIssuance := -1.0
	m.issueCertificate = func(context.Context, string, io.Writer) error { return nil }
	m.distributeCertificate = func(context.Context, string, io.Writer, func(deploy.Event)) error { return nil }

	m.beginAddForDomain(domain)
	m.completeForm()
	cmd := m.startCmd
	if cmd == nil {
		t.Fatal("certificate run did not start")
	}
	for steps := 0; cmd != nil && steps < 20; steps++ {
		var done bool
		cmd, done = m.Update(cmd())
		if done {
			t.Fatal("certificate run unexpectedly closed its parent flow")
		}
		// Sample once the issuance step has been reported but before it is
		// acknowledged as finished.
		if len(m.run.events) == 1 && m.run.events[0].Status == "running" {
			duringIssuance = m.run.percent()
		}
	}
	if m.phase != certPhaseDone {
		t.Fatalf("certificate run did not finish: phase=%d", m.phase)
	}
	if duringIssuance != 0 {
		t.Fatalf("bar during ACME order = %v, want 0", duringIssuance)
	}
	if got := m.run.percent(); got != 1 {
		t.Fatalf("bar after a completed run = %v, want 1", got)
	}
}

func TestUncoveredDomainSeedsTheZoneFormWithTheRegistrableParent(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "us1.vpn.example.co.uk"

	m.issueCertificate = func(context.Context, string, io.Writer) error {
		t.Fatal("issuance started before a covering zone existed")
		return nil
	}
	m.beginAddForDomain(domain)

	if m.phase != certPhaseZoneForm {
		t.Fatalf("uncovered domain phase = %d, want the DNS zone form", m.phase)
	}
	// Seeding the certificate domain itself would scope the zone to one host and
	// force the same API token again for every sibling.
	if got := m.form.values["domain"]; got != "example.co.uk" {
		t.Fatalf("seeded DNS zone = %q, want the registrable parent example.co.uk", got)
	}
	if view := m.View(); !strings.Contains(view, "example.co.uk") {
		t.Fatalf("zone form does not show the proposed zone:\n%s", view)
	}
}

func typeRunes(t *testing.T, m *certManager, text string) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	for _, r := range text {
		cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return cmd
}

// selectCertAction parks the cursor on an action by name, so the tests keep
// working when the menu is reordered and fail loudly if one is renamed.
func selectCertAction(t *testing.T, m *certManager, action string) {
	t.Helper()
	for i, candidate := range certActions {
		if candidate == action {
			m.actionCursor = i
			return
		}
	}
	t.Fatalf("certificate menu has no %q action: %v", action, certActions)
}

func addZoneForTest(t *testing.T, m *certManager, zone string) {
	t.Helper()
	if err := certmgr.UpsertCredential(m.layout, certmgr.DNSCredential{
		Domain: zone, Provider: certmgr.ProviderCloudflare, Credential: "token",
	}); err != nil {
		t.Fatalf("save DNS zone %s: %v", zone, err)
	}
	m.reload()
}

func TestCertificateDomainForHostComposesUnderTheZone(t *testing.T) {
	cases := []struct{ zone, host, want string }{
		{"example.com", "us1", "us1.example.com"},
		{"example.com", "a.b", "a.b.example.com"},
		// An empty hostname is the zone apex, not an error.
		{"example.com", "", "example.com"},
		{"example.com", "  ", "example.com"},
		// A pasted fully-qualified name keeps its suffix instead of doubling it.
		{"example.com", "us1.example.com", "us1.example.com"},
		{"example.com", "example.com", "example.com"},
		{"example.com", "US1.EXAMPLE.COM", "us1.example.com"},
		{"example.com", "us1.", "us1.example.com"},
	}
	for _, tc := range cases {
		if got := certificateDomainForHost(tc.zone, tc.host); got != tc.want {
			t.Fatalf("certificateDomainForHost(%q, %q) = %q, want %q", tc.zone, tc.host, got, tc.want)
		}
	}
	if got := hostWithinZone("example.com", "us1.example.com"); got != "us1" {
		t.Fatalf("hostWithinZone = %q, want us1", got)
	}
	if got := hostWithinZone("example.com", "example.com"); got != "" {
		t.Fatalf("hostWithinZone at the apex = %q, want empty", got)
	}
}

func TestAddCertificateAsksForTheZoneBeforeTheHostname(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")

	var issued []string
	m.issueCertificate = func(_ context.Context, domain string, _ io.Writer) error {
		issued = append(issued, domain)
		return nil
	}
	m.distributeCertificate = func(context.Context, string, io.Writer, func(deploy.Event)) error { return nil }

	// "Add certificate" opens the zone picker, not a free-form domain field.
	selectCertAction(t, m, actionAddCertificate)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseZonePick {
		t.Fatalf("add phase = %d, want the zone picker", m.phase)
	}
	view := m.View()
	for _, want := range []string{"Which DNS zone issues this certificate?", "example.com", addZoneRow} {
		if !strings.Contains(view, want) {
			t.Fatalf("zone picker missing %q:\n%s", want, view)
		}
	}

	// Picking the zone narrows the question to the name below it.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseHostForm || m.pendingZone != "example.com" {
		t.Fatalf("zone selection phase = %d zone = %q", m.phase, m.pendingZone)
	}
	view = m.View()
	for _, want := range []string{"Add certificate · example.com", "Hostname"} {
		if !strings.Contains(view, want) {
			t.Fatalf("hostname step missing %q:\n%s", want, view)
		}
	}

	// The badge previews the composed domain while it is still being typed.
	typeRunes(t, m, "us1")
	if view := m.View(); !strings.Contains(view, "will issue: us1.example.com") {
		t.Fatalf("hostname step does not preview the composed domain:\n%s", view)
	}

	cmd, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.phase != certPhaseRunning {
		t.Fatalf("hostname did not start issuance: phase=%d cmd=%v", m.phase, cmd != nil)
	}
	drainCertificateRun(t, m, cmd)
	if len(issued) != 1 || issued[0] != "us1.example.com" {
		t.Fatalf("issued = %v, want [us1.example.com]", issued)
	}
}

func TestEmptyHostnameIssuesForTheZoneApex(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")

	var issued []string
	m.issueCertificate = func(_ context.Context, domain string, _ io.Writer) error {
		issued = append(issued, domain)
		return nil
	}
	m.distributeCertificate = func(context.Context, string, io.Writer, func(deploy.Event)) error { return nil }

	m.beginHostForm("example.com", "")
	if view := m.View(); !strings.Contains(view, "will issue: example.com") {
		t.Fatalf("empty hostname does not preview the apex:\n%s", view)
	}
	cmd, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("empty hostname did not start issuance for the zone apex")
	}
	drainCertificateRun(t, m, cmd)
	if len(issued) != 1 || issued[0] != "example.com" {
		t.Fatalf("issued = %v, want [example.com]", issued)
	}
}

func TestAddingAZoneFromThePickerContinuesToTheHostname(t *testing.T) {
	m := newCertificateManagerForTest(t)

	// With no zones stored the picker's only row is the one that adds the first,
	// so the prerequisite is walked through rather than reported as a failure.
	m.beginAddCertificate()
	view := m.View()
	if !strings.Contains(view, addZoneRow) || !strings.Contains(view, "No DNS zones yet") {
		t.Fatalf("empty zone picker does not offer the first zone:\n%s", view)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseZoneForm || !m.pickHostAfterZone {
		t.Fatalf("add-zone row phase = %d continue = %v", m.phase, m.pickHostAfterZone)
	}

	// Fill the zone form: zone, provider (default), token.
	typeRunes(t, m, "example.com")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	typeRunes(t, m, "cf-token")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.phase != certPhaseHostForm || m.pendingZone != "example.com" {
		t.Fatalf("saving a zone from the picker did not continue to the hostname: phase=%d zone=%q", m.phase, m.pendingZone)
	}
	if m.pickHostAfterZone {
		t.Fatal("hostname continuation was not consumed")
	}
	zones, err := certmgr.LoadCredentials(m.layout)
	if err != nil || len(zones) != 1 || zones[0].Domain != "example.com" {
		t.Fatalf("zone was not saved: zones=%+v err=%v", zones, err)
	}
}

func TestRedirectedDomainSkipsThePickerWhenItsZoneExists(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")

	// A domain another screen rejected arrives ready to confirm: the covering
	// zone is already chosen and only its hostname is filled in.
	m.beginAddForDomain("spoke.example.com")
	if m.phase != certPhaseHostForm || m.pendingZone != "example.com" {
		t.Fatalf("redirect phase = %d zone = %q, want the hostname step under example.com", m.phase, m.pendingZone)
	}
	if got := m.form.values["host"]; got != "spoke" {
		t.Fatalf("redirect host = %q, want spoke", got)
	}
	if view := m.View(); !strings.Contains(view, "will issue: spoke.example.com") {
		t.Fatalf("redirect does not preview the requested domain:\n%s", view)
	}
}

func TestCancellingTheHostnameStepReturnsToTheZonePicker(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")

	m.beginAddCertificate()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseHostForm {
		t.Fatalf("phase = %d, want the hostname step", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != certPhaseZonePick {
		t.Fatalf("cancelled hostname phase = %d, want the zone picker", m.phase)
	}
	// Backing out of the zone form reached from the picker returns there too,
	// rather than dropping into the unrelated zone-management list.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseZoneForm {
		t.Fatalf("phase = %d, want the zone form", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != certPhaseZonePick || m.pickHostAfterZone {
		t.Fatalf("cancelled zone form phase = %d continue = %v", m.phase, m.pickHostAfterZone)
	}
}

func TestDomainFieldsNameTheZonesTheyWillAccept(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	for _, zone := range []string{"example.com", "foo.net"} {
		if err := certmgr.UpsertCredential(layout, certmgr.DNSCredential{
			Domain: zone, Provider: certmgr.ProviderCloudflare, Credential: "token",
		}); err != nil {
			t.Fatalf("save DNS zone %s: %v", zone, err)
		}
	}

	fields := withCoveredZones(layout, []field{
		{key: "domain", note: "The spoke's proxy domain. " + noteDNSZone},
		{key: "alias", note: "Names this node."},
	})
	for _, want := range []string{noteDNSZone, "example.com", "foo.net", "and any subdomain"} {
		if !strings.Contains(fields[0].note, want) {
			t.Fatalf("domain note missing %q: %q", want, fields[0].note)
		}
	}
	// Fields that do not carry the precondition are left exactly as they were.
	if fields[1].note != "Names this node." {
		t.Fatalf("unrelated field note was rewritten: %q", fields[1].note)
	}

	// With nothing configured the note says so rather than listing an empty set.
	empty := withCoveredZones(paths.LayoutForRoot(t.TempDir()), []field{{key: "domain", note: noteDNSZone}})
	if !strings.Contains(empty[0].note, "No DNS zones are configured yet") {
		t.Fatalf("empty-zone note = %q", empty[0].note)
	}
}

func TestCertificateListLeadsWithTheZoneItsIssuanceDependsOn(t *testing.T) {
	m := newCertificateManagerForTest(t)
	m.setSize(96, 30)

	// Zone management is listed directly under the action that depends on it,
	// ahead of the maintenance actions for certificates that already exist.
	want := []string{actionAddCertificate, actionManageZones, actionRenewCertificate, actionDeleteCertificate}
	if strings.Join(certActions, "|") != strings.Join(want, "|") {
		t.Fatalf("certificate actions = %v, want %v", certActions, want)
	}

	// With nothing configured the page states the order instead of leaving it to
	// be discovered by a rejection.
	view := m.View()
	for _, wantText := range []string{"No DNS zones yet", "issued through the zone that contains its domain"} {
		if !strings.Contains(view, wantText) {
			t.Fatalf("empty certificate page missing %q:\n%s", wantText, view)
		}
	}

	// Once zones exist they are named on the page, not just counted.
	addZoneForTest(t, m, "example.com")
	addZoneForTest(t, m, "foo.net")
	view = m.View()
	for _, wantText := range []string{"DNS zones", "example.com (cloudflare)", "foo.net (cloudflare)"} {
		if !strings.Contains(view, wantText) {
			t.Fatalf("certificate page does not name the zones, missing %q:\n%s", wantText, view)
		}
	}
	if strings.Contains(view, "DNS zones: 2") {
		t.Fatalf("certificate page still reports a bare zone count:\n%s", view)
	}
}

// A certificate a node still serves cannot be deleted, and the refusal has to
// be visible on the screen the operator is left on.
func TestDeleteCertificateRefusalIsVisibleOnThePicker(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "in-use.example.com"
	if err := certmgr.Register(m.layout, domain); err != nil {
		t.Fatalf("register certificate: %v", err)
	}
	m.reload()
	m.phase = certPhaseCertPick
	m.pickCursor = 0
	m.notice.setError("cannot delete " + domain + ": certificate is used by hub")

	view := m.View()
	for _, want := range []string{"cannot delete", domain} {
		if !strings.Contains(view, want) {
			t.Fatalf("delete picker hides the refusal %q:\n%s", want, view)
		}
	}
}

// Deleting a certificate is destructive and irreversible without a fresh ACME
// order, so the picker's Enter opens a confirmation rather than deleting.
func TestDeleteCertificateRequiresExplicitY(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const domain = "doomed.example.com"
	if err := certmgr.Register(m.layout, domain); err != nil {
		t.Fatalf("register certificate: %v", err)
	}
	m.reload()

	selectCertAction(t, m, actionDeleteCertificate)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseCertPick {
		t.Fatalf("delete action phase = %d, want picker", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseCertDeleteConfirm {
		t.Fatalf("delete picker phase = %d, want confirmation", m.phase)
	}
	confirm := m.View()
	for _, want := range []string{domain, "Press y to delete"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("delete confirmation missing %q:\n%s", want, confirm)
		}
	}

	// Enter is deliberately insufficient, and cancelling returns to the picker
	// with the certificate still on disk.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseCertDeleteConfirm {
		t.Fatalf("Enter bypassed the delete confirmation: phase=%d", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != certPhaseCertPick {
		t.Fatalf("cancelled delete phase = %d, want the picker", m.phase)
	}
	m.reload()
	if len(m.inventory) != 1 {
		t.Fatalf("cancelling deleted the certificate: %#v", m.inventory)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.phase != certPhaseList {
		t.Fatalf("confirmed delete phase = %d, want the certificate page", m.phase)
	}
	if len(m.inventory) != 0 {
		t.Fatalf("confirmed delete left the certificate behind: %#v", m.inventory)
	}
}

// A zone is the DNS-01 path for every certificate under it, so its confirmation
// names them: deleting the zone is what silently breaks their renewal.
func TestDeleteDNSZoneConfirmationNamesTheCertificatesItIssues(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")
	addZoneForTest(t, m, "other.example.net")
	for _, domain := range []string{"a.example.com", "b.example.com"} {
		if err := certmgr.Register(m.layout, domain); err != nil {
			t.Fatalf("register certificate: %v", err)
		}
	}
	m.reload()

	selectCertAction(t, m, actionManageZones)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.zoneActionCursor = 1
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseZoneDeletePick {
		t.Fatalf("zone delete phase = %d, want picker", m.phase)
	}
	for i, zone := range m.zones {
		if zone.Domain == "example.com" {
			m.pickCursor = i
		}
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != certPhaseZoneDeleteConfirm {
		t.Fatalf("zone picker phase = %d, want confirmation", m.phase)
	}
	confirm := m.View()
	for _, want := range []string{"example.com", "a.example.com", "b.example.com", "Press y to delete"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("zone delete confirmation missing %q:\n%s", want, confirm)
		}
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != certPhaseZoneDeletePick || len(m.zones) != 2 {
		t.Fatalf("cancelling deleted the zone: phase=%d zones=%#v", m.phase, m.zones)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(m.zones) != 1 || m.zones[0].Domain != "other.example.net" {
		t.Fatalf("confirmed zone delete = %#v", m.zones)
	}
}

// Every result notice on this page names the object it acted on, so a run that
// finished and a zone that was removed read the same way as the rest.
func TestCertificateResultNoticesNameTheirSubject(t *testing.T) {
	m := newCertificateManagerForTest(t)
	addZoneForTest(t, m, "example.com")
	if err := certmgr.Register(m.layout, "kept.example.com"); err != nil {
		t.Fatalf("register certificate: %v", err)
	}
	m.reload()

	m.phase, m.operation, m.operationDomain = certPhaseRunning, certOperationAdd, "new.example.com"
	if _, _ = m.Update(runMsg{done: true}); !strings.Contains(m.notice.text, "added certificate new.example.com") {
		t.Fatalf("add notice = %q", m.notice.text)
	}
	m.phase, m.operation, m.operationDomain = certPhaseRunning, certOperationRenew, "kept.example.com"
	if _, _ = m.Update(runMsg{done: true}); !strings.Contains(m.notice.text, "renewed certificate kept.example.com") {
		t.Fatalf("renew notice = %q", m.notice.text)
	}

	m.phase = certPhaseZoneDeleteConfirm
	m.pendingDeleteZone = m.zones[0]
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !strings.Contains(m.notice.text, "deleted DNS zone example.com") {
		t.Fatalf("zone delete notice = %q", m.notice.text)
	}
}
