package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// Setup collects the monitor name explicitly, but offers the install domain so
// an operator who does not want to split the two just presses enter.
func TestInstallFormPrefillsMonitorDomainFromInstallDomain(t *testing.T) {
	w := installFormForTest()
	w.width = 80
	w.startForm()
	w.input.SetValue("vpn.example.com")
	w.commitField()
	w.setField(fieldIndex(t, w.fields, "monitor_domain"))

	if !strings.Contains(w.View(), "default: vpn.example.com") {
		t.Fatalf("monitor domain field should offer the install domain:\n%s", w.View())
	}
	w.input.SetValue("")
	w.commitField()
	if w.values["monitor_domain"] != "vpn.example.com" {
		t.Fatalf("monitor domain = %q, want the install domain", w.values["monitor_domain"])
	}
}

// The monitor name is only checked for issuability: it is served by this host
// but may point at something in front of it, so it is not matched against this
// server's public address the way the install domain is.
func TestInstallFormMonitorDomainSkipsTheResolutionCheck(t *testing.T) {
	w := installFormForTest()
	w.validateDomain = func(_ context.Context, domain string) (string, error) {
		if domain != "vpn.example.com" {
			t.Fatalf("only the install domain is matched against this server, got %q", domain)
		}
		return "203.0.113.10", nil
	}
	var covered []string
	w.validateDomainCovered = func(domain string) error {
		covered = append(covered, domain)
		return nil
	}
	w.startForm()
	w.input.SetValue("vpn.example.com")
	w.commitField()
	w.setField(fieldIndex(t, w.fields, "monitor_domain"))
	w.input.SetValue("monitor.example.com")
	w.commitField()

	if w.values["monitor_domain"] != "monitor.example.com" {
		t.Fatalf("monitor domain = %q", w.values["monitor_domain"])
	}
	if strings.Join(covered, ",") != "vpn.example.com,monitor.example.com" {
		t.Fatalf("credential coverage checked for %v", covered)
	}
}

// A monitor name that certificate management cannot issue for suspends setup
// and hands the operator to Certificate management, exactly as the install
// domain does.
func TestInstallFlowRedirectsUnmanagedMonitorDomainToCertificates(t *testing.T) {
	flow := &installFlow{phase: phaseForm, form: installFormForTest(), run: newCommandRun()}
	flow.form.validateDomainCovered = func(domain string) error {
		if domain == "monitor.example.com" {
			return &certmgr.UnmanagedDomainError{Domain: domain}
		}
		return nil
	}
	flow.form.startForm()
	flow.form.input.SetValue("vpn.example.com")
	flow.form.commitField()
	flow.form.setField(fieldIndex(t, flow.form.fields, "monitor_domain"))
	flow.form.input.SetValue("monitor.example.com")
	flow.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if flow.certificateDomainRequest != "monitor.example.com" {
		t.Fatalf("certificateDomainRequest = %q", flow.certificateDomainRequest)
	}
	if flow.form.values["monitor_domain"] != "" {
		t.Fatalf("an unmanaged monitor domain must not commit: %#v", flow.form.values)
	}
}

func TestBuildConfigCarriesMonitorDomain(t *testing.T) {
	values := map[string]string{
		"domain":         "vpn.example.com",
		"protocols":      defaultProtocolValue(),
		"reality_sni":    "www.microsoft.com",
		"display_name":   "Node",
		"monitor":        "yes",
		"monitor_alias":  "US-local",
		"monitor_domain": "monitor.example.com",
	}
	w := &installFlow{form: installFormWithValuesForTest(values), host: supportedTestHost()}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if cfg.MonitorDomain != "monitor.example.com" || cfg.MonitorHost() != "monitor.example.com" {
		t.Fatalf("monitor domain = %q", cfg.MonitorDomain)
	}
	certDomain, err := cfg.MonitorCertificateDomain()
	if err != nil {
		t.Fatalf("MonitorCertificateDomain error: %v", err)
	}
	if certDomain != "monitor.example.com" {
		t.Fatalf("monitor certificate domain = %q", certDomain)
	}
}

// With the monitor turned off there is no monitor endpoint to name, so no
// second certificate is registered for a value the form left behind.
func TestBuildConfigDropsMonitorDomainWhenMonitorDisabled(t *testing.T) {
	values := map[string]string{
		"domain":         "vpn.example.com",
		"protocols":      defaultProtocolValue(),
		"reality_sni":    "www.microsoft.com",
		"display_name":   "Node",
		"monitor":        "no",
		"monitor_domain": "monitor.example.com",
	}
	w := &installFlow{form: installFormWithValuesForTest(values), host: supportedTestHost()}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if cfg.MonitorDomain != "" || cfg.MonitorHost() != "vpn.example.com" {
		t.Fatalf("monitor domain = %q, host = %q", cfg.MonitorDomain, cfg.MonitorHost())
	}
}

// The setup summary names the monitor's own URL, not the masquerade site's.
func TestInstalledSummaryReportsMonitorDomainURL(t *testing.T) {
	w := &installFlow{cfg: deploy.Config{
		Domain:                "vpn.example.com",
		MonitorDomain:         "monitor.example.com",
		MonitorPublicPort:     2097,
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
	}}
	summary := w.doneSummary()
	if !strings.Contains(summary, "https://monitor.example.com:2097/monitor/") {
		t.Fatalf("summary should report the monitor domain URL:\n%s", summary)
	}
	if strings.Contains(summary, "https://vpn.example.com:2097") {
		t.Fatalf("summary should not report the monitor under the site domain:\n%s", summary)
	}
}

// The monitor management form is seeded from the persisted monitor domain and
// carries an edit through to the update options.
func TestMonitorLocalFormEditsTheMonitorDomain(t *testing.T) {
	tm := &monitorManager{cfg: deploy.Config{
		Domain:                 "vpn.example.com",
		MonitorDomain:          "monitor.example.com",
		DeployMonitor:          true,
		DeployMonitorFrontend:  true,
		MonitorAlias:           "US-local",
		MonitorPublicPort:      2097,
		MonitorPort:            19090,
		MonitorInterface:       "eth0",
		MonitorIntervalSeconds: 60,
	}}
	fields := tm.localFields()
	field := fields[fieldIndex(t, fields, "monitor_domain")]
	if field.def != "monitor.example.com" {
		t.Fatalf("monitor domain default = %q", field.def)
	}
	if field.label != uiparams.LabelMonitorDomain {
		t.Fatalf("monitor domain label = %q", field.label)
	}

	tm.values = map[string]string{
		"monitor":                  "yes",
		"monitor_frontend":         "yes",
		"monitor_alias":            "US-local",
		"monitor_domain":           "stats.example.com",
		"monitor_public_port":      "2097",
		"monitor_port":             "19090",
		"monitor_interface":        "eth0",
		"monitor_interval_seconds": "60",
		"reset_day":                "1",
		"reset_hour":               "0",
	}
	if got := tm.localUpdateOptions().MonitorDomain; got != "stats.example.com" {
		t.Fatalf("update options monitor domain = %q", got)
	}
}

// Editing the monitor domain runs through the same certificate-management gate
// as setup.
func TestValidateMonitorFieldChecksMonitorDomainCoverage(t *testing.T) {
	old := validateMonitorDomain
	t.Cleanup(func() { validateMonitorDomain = old })
	validateMonitorDomain = func(domain string) error {
		return &certmgr.NoCredentialError{Domain: domain}
	}
	err := validateMonitorField(field{key: "monitor_domain"}, "stats.example.com", nil)
	if certificateRedirectDomain(err) != "stats.example.com" {
		t.Fatalf("expected a certificate-management redirect, got %v", err)
	}
	if err := validateMonitorField(field{key: "monitor_alias"}, "US-local", nil); err != nil {
		t.Fatalf("unrelated field should not be gated: %v", err)
	}
}

// The status panel points at the monitor's own name and reports its separate
// certificate.
func TestLoadStatusReportsTheMonitorDomain(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	writeStatusState(t, layout.StateDir, "domain", "vpn.example.com")
	writeStatusState(t, layout.StateDir, "public_ip", "203.0.113.10")
	writeStatusState(t, layout.StateDir, "monitor_domain", "monitor.example.com")
	writeStatusState(t, layout.StateDir, "monitor_public_port", "2097")
	writeStatusState(t, layout.StateDir, "monitor", "yes")

	oldLayout, oldOutput := defaultStatusLayout, statusCommandOutput
	t.Cleanup(func() { defaultStatusLayout, statusCommandOutput = oldLayout, oldOutput })
	defaultStatusLayout = func() paths.Layout { return layout }
	statusCommandOutput = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("unexpected command: %s %v", name, args)
	}

	status := loadStatus()
	if status.MonitorUI != "https://monitor.example.com:2097/monitor/" {
		t.Fatalf("MonitorUI = %q", status.MonitorUI)
	}
}
