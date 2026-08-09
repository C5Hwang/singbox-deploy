package ui

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// writeStatusCertificate drops a self-signed pair under the name the status
// panel looks certificates up by, so its certificate rows have something to
// report.
func writeStatusCertificate(t *testing.T, layout paths.Layout, domain string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	if err := os.MkdirAll(layout.TLSDir, 0o700); err != nil {
		t.Fatalf("create tls dir: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(layout.TLSDir, domain+".crt"), pemBytes, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
}

// The status panel decides whether the monitor carries a second certificate by
// the name Nginx serves it under, so the install domain spelled differently on
// the monitor field is still one certificate — and the URL it reports is the
// one that selects the monitor's server block.
func TestLoadStatusComparesMonitorAndInstallDomainsAsServed(t *testing.T) {
	cases := []struct {
		name          string
		domain        string
		monitorDomain string
		wantURL       string
		wantCertRow   bool
	}{
		{
			name:          "the same name spelled differently is one certificate",
			domain:        "vpn.example.com",
			monitorDomain: "VPN.example.com.",
			wantURL:       "https://vpn.example.com:2097/monitor/",
		},
		{
			name:          "a genuinely separate name carries its own",
			domain:        "vpn.example.com",
			monitorDomain: "Monitor.Example.COM",
			wantURL:       "https://monitor.example.com:2097/monitor/",
			wantCertRow:   true,
		},
		{
			name:          "an idn is reported as it is served",
			domain:        "vpn.example.com",
			monitorDomain: "监控.example.com",
			wantURL:       "https://xn--izun04b.example.com:2097/monitor/",
			wantCertRow:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			writeStatusState(t, layout.StateDir, "domain", tc.domain)
			writeStatusState(t, layout.StateDir, "public_ip", "203.0.113.10")
			writeStatusState(t, layout.StateDir, "monitor_domain", tc.monitorDomain)
			writeStatusState(t, layout.StateDir, "monitor_public_port", "2097")
			writeStatusState(t, layout.StateDir, "monitor", "yes")
			// The pairs exist under the names they were issued for — the served
			// ones — so a row that stays empty did so by folding the two names,
			// not by looking up a spelling nothing was ever written under.
			writeStatusCertificate(t, layout, deploy.ServerName(tc.domain))
			writeStatusCertificate(t, layout, deploy.ServerName(tc.monitorDomain))

			oldLayout, oldOutput := defaultStatusLayout, statusCommandOutput
			t.Cleanup(func() { defaultStatusLayout, statusCommandOutput = oldLayout, oldOutput })
			defaultStatusLayout = func() paths.Layout { return layout }
			statusCommandOutput = func(name string, args ...string) (string, error) {
				return "", fmt.Errorf("unexpected command: %s %v", name, args)
			}

			status := loadStatus()
			if status.MonitorUI != tc.wantURL {
				t.Fatalf("MonitorUI = %q, want %q", status.MonitorUI, tc.wantURL)
			}
			if got := status.MonitorCertState != ""; got != tc.wantCertRow {
				t.Fatalf("monitor certificate row = %v (%q), want %v", got, status.MonitorCertState, tc.wantCertRow)
			}
		})
	}
}
