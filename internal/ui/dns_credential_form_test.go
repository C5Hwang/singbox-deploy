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

func TestDNSCredentialFormCloudflarePath(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := newDNSCredentialForm("tokyo.example.com", saver)
	if got := f.fields[0].def; got != "example.com" {
		t.Fatalf("root_domain pre-fill default = %q, want example.com", got)
	}
	// Cloudflare path is three steps: root_domain (default), provider
	// (cloudflare default), cf_token (typed). The aliyun fields are skipped.
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default provider=cloudflare
	f.input.SetValue("tok")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit cf_token → form completes

	saved, cancelled, creds := f.State()
	if !saved || cancelled {
		t.Fatalf("expected saved=true cancelled=false, got saved=%v cancelled=%v", saved, cancelled)
	}
	if creds.RootDomain != "example.com" || creds.Provider != "cloudflare" || creds.APIToken != "tok" || creds.APISecret != "" {
		t.Fatalf("saved credentials wrong: %#v", creds)
	}
	if saver.last.APIToken != "tok" || saver.last.APISecret != "" {
		t.Fatalf("saver did not receive cloudflare token only: %#v", saver.last)
	}
}

func TestDNSCredentialFormAliyunPath(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := newDNSCredentialForm("tokyo.example.com", saver)
	// Switch the provider selection to aliyun before committing it.
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit default root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyDown})  // move provider option to aliyun
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit provider=aliyun
	f.input.SetValue("LTAI-key")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit aliyun_access_key_id
	f.input.SetValue("LTAI-secret")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit aliyun_access_key_secret → form completes

	saved, cancelled, creds := f.State()
	if !saved || cancelled {
		t.Fatalf("expected saved=true cancelled=false, got saved=%v cancelled=%v", saved, cancelled)
	}
	if creds.Provider != "aliyun" || creds.APIToken != "LTAI-key" || creds.APISecret != "LTAI-secret" {
		t.Fatalf("saved aliyun credentials wrong: %#v", creds)
	}
	if saver.last.APIToken != "LTAI-key" || saver.last.APISecret != "LTAI-secret" {
		t.Fatalf("saver did not receive aliyun access key + secret: %#v", saver.last)
	}
}

func TestDNSCredentialFormAliyunRequiresSecret(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := newDNSCredentialForm("example.com", saver)
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyDown})  // provider → aliyun
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit provider
	f.input.SetValue("LTAI-key")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit access key id
	// Leave access key secret blank — validation should reject and form must
	// remain on the aliyun_access_key_secret field, not saved.
	f.input.SetValue("")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	saved, cancelled, _ := f.State()
	if saved || cancelled {
		t.Fatalf("blank aliyun secret should not save: saved=%v cancelled=%v", saved, cancelled)
	}
	if got := f.parameterForm.currentFieldKey(); got != "aliyun_access_key_secret" {
		t.Fatalf("cursor should remain on aliyun_access_key_secret, got %q", got)
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
