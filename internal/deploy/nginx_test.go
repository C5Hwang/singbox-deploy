package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestAptNginxInstallCommandIsNoninteractive(t *testing.T) {
	cmds := NginxInstallCommands(system.OSRelease{PackageManager: "apt"})
	if len(cmds) != 1 {
		t.Fatalf("commands = %#v", cmds)
	}
	script := strings.Join(cmds[0].Args, " ")
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"NEEDRESTART_MODE=a",
		"Dpkg::Options::=--force-confdef",
		"gpg --batch --yes --no-tty --dearmor",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("nginx apt script missing %q:\n%s", want, script)
		}
	}
}

// The monitor's own name selects its own server block and its own certificate,
// and every other name offered on that port is dropped.
func TestWriteManagedNginxConfigSeparatesTheMonitorDomain(t *testing.T) {
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
	monitorListen := fmt.Sprintf("listen %d ssl;", DefaultMonitorPublicPort)
	for _, want := range []string{
		"server_name monitor.example.com;",
		"ssl_certificate " + monitorCert + ";",
		"ssl_certificate_key " + monitorKey + ";",
		fmt.Sprintf("listen %d ssl default_server;", DefaultMonitorPublicPort),
		"ssl_reject_handshake on;",
	} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("nginx config missing %q:\n%s", want, conf)
		}
	}
	siteCert, _ := CertificatePaths(layout, cfg.Domain)
	monitorBlock := string(conf)[strings.Index(string(conf), monitorListen):]
	if strings.Contains(monitorBlock, siteCert) || strings.Contains(monitorBlock, "server_name example.com;") {
		t.Fatalf("the monitor block must not carry the masquerade site's name or certificate:\n%s", monitorBlock)
	}
}

// Left on the install domain, the monitor keeps sharing its certificate, and on
// 443 it adds no catch-all: the camouflage site is already that port's default
// server. The only one emitted is the subscription's, on its own port.
func TestWriteManagedNginxConfigKeepsSharedMonitorDomain(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:                "example.com",
		SubscribePort:         DefaultSubscribePort,
		MonitorPublicPort:     443,
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
	if strings.Count(string(conf), "listen 443") != 1 {
		t.Fatalf("monitor on 443 under the install domain should fold into the camouflage block:\n%s", conf)
	}
	if got := strings.Count(string(conf), "ssl_reject_handshake on;"); got != 1 {
		t.Fatalf("expected only the subscription port's catch-all, got %d:\n%s", got, conf)
	}
	if !strings.Contains(string(conf), fmt.Sprintf("listen %d ssl default_server;", DefaultSubscribePort)) {
		t.Fatalf("the catch-all should sit on the subscription port:\n%s", conf)
	}
}

// Nginx matches server_name against the name the client offers, which is always
// the normalized one. A monitor domain typed with a trailing dot, in capitals,
// or as an IDN must still produce a block that can actually be selected — and
// the same spelling must not be mistaken for a second name.
func TestWriteManagedNginxConfigNormalizesServerNames(t *testing.T) {
	cases := []struct {
		name          string
		domain        string
		monitorDomain string
		wantServer    string
	}{
		{name: "trailing dot", domain: "example.com", monitorDomain: "monitor.example.com.", wantServer: "monitor.example.com"},
		{name: "capitals", domain: "example.com", monitorDomain: "Monitor.Example.COM", wantServer: "monitor.example.com"},
		{name: "idn", domain: "example.com", monitorDomain: "监控.example.com", wantServer: "xn--izun04b.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			layout := paths.LayoutForRoot(root)
			confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
			cfg := Config{
				Domain:                tc.domain,
				MonitorDomain:         tc.monitorDomain,
				SubscribePort:         DefaultSubscribePort,
				MonitorPublicPort:     DefaultMonitorPublicPort,
				MonitorPort:           DefaultMonitorPort,
				DeployMonitor:         true,
				DeployMonitorFrontend: true,
			}
			certDomain, err := cfg.MonitorCertificateDomain()
			if err != nil {
				t.Fatalf("MonitorCertificateDomain: %v", err)
			}
			if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
				t.Fatalf("WriteManagedNginxConfig: %v", err)
			}
			conf, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatalf("read nginx config: %v", err)
			}
			if !strings.Contains(string(conf), "server_name "+tc.wantServer+";") {
				t.Fatalf("nginx config missing server_name %q:\n%s", tc.wantServer, conf)
			}
			// The certificate manager writes the pair under the normalized name,
			// so that is the only path Nginx may reference.
			issuedCert, issuedKey := CertificatePaths(layout, certDomain)
			for _, want := range []string{"ssl_certificate " + issuedCert + ";", "ssl_certificate_key " + issuedKey + ";"} {
				if !strings.Contains(string(conf), want) {
					t.Fatalf("nginx config missing %q:\n%s", want, conf)
				}
			}
		})
	}
}

// The install domain spelled differently on the monitor field is still the same
// name: it shares the install certificate and must not gain a second block.
func TestWriteManagedNginxConfigFoldsTheSameNameSpelledDifferently(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{
		Domain:                "example.com",
		MonitorDomain:         "EXAMPLE.com.",
		SubscribePort:         DefaultSubscribePort,
		MonitorPublicPort:     443,
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
	if strings.Count(string(conf), "listen 443") != 1 {
		t.Fatalf("the monitor should fold into the camouflage block on 443:\n%s", conf)
	}
}
