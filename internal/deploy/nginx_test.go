package deploy

import (
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
	for _, want := range []string{
		"server_name monitor.example.com;",
		"ssl_certificate " + monitorCert + ";",
		"ssl_certificate_key " + monitorKey + ";",
		"listen 2097 ssl default_server;",
		"ssl_reject_handshake on;",
	} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("nginx config missing %q:\n%s", want, conf)
		}
	}
	siteCert, _ := CertificatePaths(layout, cfg.Domain)
	monitorBlock := string(conf)[strings.Index(string(conf), "listen 2097 ssl;"):]
	if strings.Contains(monitorBlock, siteCert) || strings.Contains(monitorBlock, "server_name example.com;") {
		t.Fatalf("the monitor block must not carry the masquerade site's name or certificate:\n%s", monitorBlock)
	}
}

// Left on the install domain, the monitor keeps sharing its certificate and no
// reject block is emitted for the subscription port it shares nothing with.
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
	if strings.Contains(string(conf), "ssl_reject_handshake on;") {
		t.Fatalf("443 already has a default server; no reject block should be emitted:\n%s", conf)
	}
}
