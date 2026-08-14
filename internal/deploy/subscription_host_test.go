package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// The subscription is published under whichever name the hub already exposes to
// its operator: the monitor's once there is one, and the install domain when
// there is not.
func TestSubscriptionHost(t *testing.T) {
	base := Config{Domain: "example.com", DeployMonitor: true}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "a monitor of its own lends its name",
			mutate: func(c *Config) { c.MonitorDomain = "monitor.example.com" },
			want:   "monitor.example.com",
		},
		{
			name:   "a monitor left on the install domain changes nothing",
			mutate: func(*Config) {},
			want:   "example.com",
		},
		{
			name:   "no monitor means no name to borrow",
			mutate: func(c *Config) { c.MonitorDomain, c.DeployMonitor = "monitor.example.com", false },
			want:   "example.com",
		},
		{
			name:   "a spoke publishes no subscription of its own",
			mutate: func(c *Config) { c.MonitorDomain, c.SpokeMode = "monitor.example.com", true },
			want:   "example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if got := cfg.SubscriptionHost(); got != tc.want {
				t.Fatalf("SubscriptionHost = %q, want %q", got, tc.want)
			}
		})
	}
}

// The link is only usable if the port it names answers to that name with a
// certificate for it, so the subscription block follows the monitor over.
func TestWriteManagedNginxConfigServesSubscriptionUnderTheMonitorName(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:                "example.com",
		MonitorDomain:         "monitor.example.com",
		SubscribePort:         DefaultSubscribePort,
		MonitorPublicPort:     DefaultMonitorPublicPort,
		MonitorPort:           DefaultMonitorPort,
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
	}
	if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	monitorCert, monitorKey := CertificatePaths(layout, cfg.MonitorDomain)
	block := subscriptionBlock(t, string(conf), DefaultSubscribePort)
	for _, want := range []string{
		"server_name monitor.example.com;",
		"ssl_certificate " + monitorCert + ";",
		"ssl_certificate_key " + monitorKey + ";",
		"location /s/",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("subscription block missing %q:\n%s", want, block)
		}
	}
	siteCert, _ := CertificatePaths(layout, cfg.Domain)
	if strings.Contains(block, siteCert) || strings.Contains(block, "server_name example.com;") {
		t.Fatalf("the subscription block must not carry the masquerade site's name or certificate:\n%s", block)
	}
}

// Sharing the monitor's name and its port, the subscription has no block of its
// own to claim: two blocks with one server_name on one port is a conflict Nginx
// resolves by ignoring the second.
func TestWriteManagedNginxConfigFoldsSubscriptionIntoTheMonitorBlock(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:                "example.com",
		MonitorDomain:         "monitor.example.com",
		SubscribePort:         DefaultMonitorPublicPort,
		MonitorPublicPort:     DefaultMonitorPublicPort,
		MonitorPort:           DefaultMonitorPort,
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
	}
	if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	listen := fmt.Sprintf("listen %d ssl;", DefaultMonitorPublicPort)
	if got := strings.Count(string(conf), listen); got != 1 {
		t.Fatalf("expected one block on the shared port, got %d:\n%s", got, conf)
	}
	block := subscriptionBlock(t, string(conf), DefaultMonitorPublicPort)
	for _, want := range []string{"server_name monitor.example.com;", "location /s/", "/monitor/api/"} {
		if !strings.Contains(block, want) {
			t.Fatalf("shared block missing %q:\n%s", want, block)
		}
	}
}

// Without a monitor there is no second name in play, so a monitor domain left
// over in state moves nothing: the subscription stays on the install domain and
// its certificate.
func TestWriteManagedNginxConfigKeepsSubscriptionOnTheInstallDomainWithoutMonitor(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:            "example.com",
		MonitorDomain:     "monitor.example.com",
		SubscribePort:     DefaultSubscribePort,
		MonitorPublicPort: DefaultMonitorPublicPort,
		MonitorPort:       DefaultMonitorPort,
	}
	if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	siteCert, siteKey := CertificatePaths(layout, cfg.Domain)
	block := subscriptionBlock(t, string(conf), DefaultSubscribePort)
	for _, want := range []string{
		"server_name example.com;",
		"ssl_certificate " + siteCert + ";",
		"ssl_certificate_key " + siteKey + ";",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("subscription block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(string(conf), "monitor.example.com") {
		t.Fatalf("a disabled monitor should publish nothing under its name:\n%s", conf)
	}
}

// On 443 the camouflage site is the default server, so a subscription answering
// to the monitor's name needs a block of its own beside it rather than the
// shared location that would hand out the site's certificate.
func TestWriteManagedNginxConfigSubscribesOn443UnderTheMonitorName(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:                "example.com",
		MonitorDomain:         "monitor.example.com",
		SubscribePort:         443,
		MonitorPublicPort:     DefaultMonitorPublicPort,
		MonitorPort:           DefaultMonitorPort,
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
	}
	if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	siteBlock := string(conf)[strings.Index(string(conf), "listen 443 ssl default_server;"):]
	siteBlock = siteBlock[:strings.Index(siteBlock, "\n}")]
	if strings.Contains(siteBlock, "location /s/") {
		t.Fatalf("the camouflage block serves the site's certificate; /s/ must not sit in it:\n%s", siteBlock)
	}
	block := subscriptionBlock(t, string(conf), 443)
	monitorCert, _ := CertificatePaths(layout, cfg.MonitorDomain)
	for _, want := range []string{"server_name monitor.example.com;", "ssl_certificate " + monitorCert + ";", "location /s/"} {
		if !strings.Contains(block, want) {
			t.Fatalf("subscription block missing %q:\n%s", want, block)
		}
	}
}

// The provider URL the generated Clash and Surge profiles fetch from is a
// subscription link like any other, so it is spelled with the same host.
func TestFillProfilesUsesTheSubscriptionHost(t *testing.T) {
	outbounds := []map[string]any{{"type": "vless", "tag": "🇺🇸 US-vps1-VLESS"}}
	cfg := Config{
		Domain:        "example.com",
		MonitorDomain: "monitor.example.com",
		DeployMonitor: true,
		SubscribePort: DefaultSubscribePort,
		Salt:          "testsalt",
	}
	token := SubscriptionToken(cfg.Salt)
	var out subscriptionOutputs
	if err := fillProfiles(&out, cfg, outbounds); err != nil {
		t.Fatalf("fillProfiles error: %v", err)
	}
	wantClash := fmt.Sprintf("https://monitor.example.com:%d/s/clashMeta/%s", DefaultSubscribePort, token)
	wantSurge := fmt.Sprintf("https://monitor.example.com:%d/s/surge/%s", DefaultSubscribePort, token)
	if !strings.Contains(out.ClashProfile, wantClash) {
		t.Fatalf("clash profile missing %q:\n%s", wantClash, out.ClashProfile)
	}
	if !strings.Contains(out.SurgeProfile, wantSurge) {
		t.Fatalf("surge profile missing %q:\n%s", wantSurge, out.SurgeProfile)
	}

	cfg.DeployMonitor = false
	out = subscriptionOutputs{}
	if err := fillProfiles(&out, cfg, outbounds); err != nil {
		t.Fatalf("fillProfiles error: %v", err)
	}
	if !strings.Contains(out.ClashProfile, "https://example.com:") {
		t.Fatalf("without a monitor the provider URL stays on the install domain:\n%s", out.ClashProfile)
	}
}

// A port that publishes one name and nothing else drops every other name during
// the handshake, so a bare-address probe learns neither a certificate nor that
// anything is there. The subscription port earns that the same way the monitor's
// does — except where the port already has a default server, which cannot have a
// second.
func TestWriteManagedNginxConfigRejectsUnknownNamesOnTheSubscriptionPort(t *testing.T) {
	cases := []struct {
		name          string
		subscribePort int
		monitorPort   int
		monitor       bool
		wantRejects   []int
	}{
		{
			name:          "its own port takes a catch-all",
			subscribePort: DefaultSubscribePort,
			monitorPort:   DefaultMonitorPublicPort,
			monitor:       true,
			wantRejects:   []int{DefaultSubscribePort, DefaultMonitorPublicPort},
		},
		{
			name:          "a port serving both endpoints takes only one",
			subscribePort: DefaultMonitorPublicPort,
			monitorPort:   DefaultMonitorPublicPort,
			monitor:       true,
			wantRejects:   []int{DefaultMonitorPublicPort},
		},
		{
			name:          "no monitor is no reason to leave the port open",
			subscribePort: DefaultSubscribePort,
			monitorPort:   DefaultMonitorPublicPort,
			wantRejects:   []int{DefaultSubscribePort},
		},
		{
			name:          "443 already has the camouflage site as its default",
			subscribePort: 443,
			monitorPort:   443,
			monitor:       true,
			wantRejects:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			layout := paths.LayoutForRoot(root)
			confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
			cfg := Config{
				Domain:                "example.com",
				MonitorDomain:         "monitor.example.com",
				SubscribePort:         tc.subscribePort,
				MonitorPublicPort:     tc.monitorPort,
				MonitorPort:           DefaultMonitorPort,
				DeployMonitor:         tc.monitor,
				DeployMonitorFrontend: tc.monitor,
			}
			if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
				t.Fatalf("WriteManagedNginxConfig: %v", err)
			}
			conf, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatalf("read nginx config: %v", err)
			}
			if got := strings.Count(string(conf), "ssl_reject_handshake on;"); got != len(tc.wantRejects) {
				t.Fatalf("catch-all blocks = %d, want %d:\n%s", got, len(tc.wantRejects), conf)
			}
			for _, port := range tc.wantRejects {
				listen := fmt.Sprintf("listen %d ssl default_server;", port)
				if !strings.Contains(string(conf), listen) {
					t.Fatalf("no catch-all on %d:\n%s", port, conf)
				}
			}
			// Nginx refuses to start with two default servers on one port, so the
			// pair the config emits must never land on the same one.
			for _, port := range []int{tc.subscribePort, tc.monitorPort, 443} {
				listen := fmt.Sprintf("listen %d ssl default_server;", port)
				if got := strings.Count(string(conf), listen); got > 1 {
					t.Fatalf("port %d has %d default servers:\n%s", port, got, conf)
				}
			}
		})
	}
}

// subscriptionBlock returns the server block listening on port, from its listen
// directive to the closing brace.
func subscriptionBlock(t *testing.T, conf string, port int) string {
	t.Helper()
	start := strings.Index(conf, fmt.Sprintf("listen %d ssl;", port))
	if start < 0 {
		t.Fatalf("no server block listens on %d:\n%s", port, conf)
	}
	end := strings.Index(conf[start:], "\n}")
	if end < 0 {
		t.Fatalf("server block on %d is unterminated:\n%s", port, conf[start:])
	}
	return conf[start : start+end]
}
