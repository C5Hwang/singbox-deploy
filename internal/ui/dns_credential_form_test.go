package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestGuessRootDomain(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"example.com":            "example.com",
		"sub.example.com":        "example.com",
		"a.b.c.example.com":      "example.com",
		"example.com.":           "example.com",
		"  example.com  ":        "example.com",
		"single":                 "single",
		"tokyo.sub.example.com.": "example.com",
	}
	for in, want := range cases {
		if got := guessRootDomain(in); got != want {
			t.Errorf("guessRootDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeDNSSaver records the credentials it was asked to save.
type fakeDNSSaver struct {
	last cluster.DNSCredentials
	err  error
}

func (s *fakeDNSSaver) Save(c cluster.DNSCredentials) error {
	if s.err != nil {
		return s.err
	}
	s.last = c
	return nil
}

func TestDNSCredentialFormPrefillsAndSaves(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := newDNSCredentialForm("tokyo.example.com", saver)
	if got := f.fields[0].def; got != "example.com" {
		t.Fatalf("root_domain pre-fill default = %q, want example.com", got)
	}
	// Walk through the four fields: root_domain (default), provider
	// (cloudflare default), api_token (typed), api_secret (skipped/empty).
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default provider=cloudflare
	f.input.SetValue("tok")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit api_token
	f.input.SetValue("")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit empty api_secret (cloudflare doesn't need it)

	saved, cancelled, creds := f.State()
	if !saved || cancelled {
		t.Fatalf("expected saved=true cancelled=false, got saved=%v cancelled=%v", saved, cancelled)
	}
	if creds.RootDomain != "example.com" || creds.Provider != "cloudflare" || creds.APIToken != "tok" {
		t.Fatalf("saved credentials wrong: %#v", creds)
	}
	if saver.last.APIToken != "tok" {
		t.Fatalf("saver did not receive token: %#v", saver.last)
	}
}

func TestInstallFlowTransitionsToMissingDNSCreds(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	flow := &installFlow{
		phase:    phaseForm,
		form:     installFormForTest(),
		dnsStore: cluster.NewRegistry(layout).DNS(),
	}
	flow.form.startForm()
	flow.form.input.SetValue("install.example.com")
	// Simulate the install_flow handleKey path for committing the domain
	// field: capture prevKey, advance via the parameterForm, then run the
	// post-handle hook the real code uses.
	prevKey := flow.form.currentFieldKey()
	if prevKey != "domain" {
		t.Fatalf("expected starting field domain, got %q", prevKey)
	}
	flow.form.commitField()
	// Mirror the production logic at install_flow.go handleKey:
	if flow.form.currentFieldKey() != "domain" && flow.form.fieldErr == "" {
		domain := flow.form.values["domain"]
		if _, err := flow.dnsStore.FindForDomain(domain); err != nil {
			flow.enterMissingDNSCreds(domain, "")
		}
	}
	if flow.phase != phaseMissingDNSCreds {
		t.Fatalf("expected phaseMissingDNSCreds, got %v", flow.phase)
	}
	if flow.subForm == nil {
		t.Fatalf("expected sub-form to be set")
	}
}

func TestInstallFlowResumesAfterSubFormSave(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	flow := &installFlow{
		phase:    phaseMissingDNSCreds,
		form:     installFormWithValuesForTest(map[string]string{"domain": "install.example.com"}),
		dnsStore: cluster.NewRegistry(layout).DNS(),
	}
	// Save credentials that cover the install domain, then mark the sub-form
	// as saved and check the parent resumes the install flow.
	if err := flow.dnsStore.Save(cluster.DNSCredentials{
		RootDomain: "example.com",
		Provider:   "cloudflare",
		APIToken:   "tok",
	}); err != nil {
		t.Fatalf("seed DNS: %v", err)
	}
	flow.subForm = newDNSCredentialForm("install.example.com", flow.dnsStore)
	flow.subForm.saved = true
	flow.subForm.done = true
	flow.advanceSubFormState()
	if flow.phase != phaseForm {
		t.Fatalf("expected phaseForm after save, got %v", flow.phase)
	}
	if flow.subForm != nil {
		t.Fatalf("subForm should be cleared after consumed save")
	}
}

func TestInstallFlowSubFormSaveRetriesOnNonCoveringRoot(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	flow := &installFlow{
		phase:    phaseMissingDNSCreds,
		form:     installFormWithValuesForTest(map[string]string{"domain": "install.example.com"}),
		dnsStore: cluster.NewRegistry(layout).DNS(),
	}
	// Save credentials for an unrelated root — the lookup should still miss.
	if err := flow.dnsStore.Save(cluster.DNSCredentials{
		RootDomain: "unrelated.io",
		Provider:   "cloudflare",
		APIToken:   "tok",
	}); err != nil {
		t.Fatalf("seed DNS: %v", err)
	}
	flow.subForm = newDNSCredentialForm("install.example.com", flow.dnsStore)
	flow.subForm.saved = true
	flow.subForm.done = true
	flow.advanceSubFormState()
	if flow.phase != phaseMissingDNSCreds {
		t.Fatalf("expected to re-enter phaseMissingDNSCreds, got %v", flow.phase)
	}
	if flow.subForm == nil || flow.subForm.headerErr == "" {
		t.Fatalf("expected new sub-form with header error")
	}
}

func TestInstallFlowSubFormCancelReturnsToDomain(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	flow := &installFlow{
		phase:    phaseMissingDNSCreds,
		form:     installFormForTest(),
		dnsStore: cluster.NewRegistry(layout).DNS(),
	}
	flow.form.startForm()
	flow.form.values["domain"] = "install.example.com"
	flow.subForm = newDNSCredentialForm("install.example.com", flow.dnsStore)
	flow.subForm.cancelled = true
	flow.subForm.done = true
	flow.advanceSubFormState()
	if flow.phase != phaseForm {
		t.Fatalf("expected phaseForm after cancel, got %v", flow.phase)
	}
	if flow.statusErr == "" {
		t.Fatalf("expected status banner to be set after cancel")
	}
	if flow.form.currentFieldKey() != "domain" {
		t.Fatalf("expected cursor on domain field, got %q", flow.form.currentFieldKey())
	}
}
