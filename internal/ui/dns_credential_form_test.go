package ui

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestInstallFlowTransitionsToMissingDNSCreds(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	flow := &installFlow{
		phase:    phaseForm,
		form:     installFormForTest(),
		dnsStore: cluster.NewRegistry(layout).DNS(),
	}
	flow.form.startForm()
	flow.form.Input.SetValue("install.example.com")
	// Simulate the install_flow handleKey path for committing the domain
	// field: capture prevKey, advance via the parameterForm, then run the
	// post-handle hook the real code uses.
	prevKey := flow.form.CurrentFieldKey()
	if prevKey != "domain" {
		t.Fatalf("expected starting field domain, got %q", prevKey)
	}
	flow.form.commitField()
	// Mirror the production logic at install_flow.go handleKey:
	if flow.form.CurrentFieldKey() != "domain" && flow.form.FieldErr == "" {
		domain := flow.form.Values["domain"]
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
	flow.subForm.Saved = true
	flow.subForm.Done = true
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
	flow.subForm.Saved = true
	flow.subForm.Done = true
	flow.advanceSubFormState()
	if flow.phase != phaseMissingDNSCreds {
		t.Fatalf("expected to re-enter phaseMissingDNSCreds, got %v", flow.phase)
	}
	if flow.subForm == nil || flow.subForm.HeaderErr == "" {
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
	flow.form.Values["domain"] = "install.example.com"
	flow.subForm = newDNSCredentialForm("install.example.com", flow.dnsStore)
	flow.subForm.Cancelled = true
	flow.subForm.Done = true
	flow.advanceSubFormState()
	if flow.phase != phaseForm {
		t.Fatalf("expected phaseForm after cancel, got %v", flow.phase)
	}
	if flow.statusErr == "" {
		t.Fatalf("expected status banner to be set after cancel")
	}
	if flow.form.CurrentFieldKey() != "domain" {
		t.Fatalf("expected cursor on domain field, got %q", flow.form.CurrentFieldKey())
	}
}
