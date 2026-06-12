package protocol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

func testConfig(t *testing.T) deploy.Config {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	return deploy.Config{
		Domain:                 "example.com",
		Email:                  "admin@example.com",
		Challenge:              acme.ChallengeHTTP01,
		Ports:                  config.Ports{RealityVision: 443, RealityGRPC: 8443, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		DisplayName:            "US-vps1",
		Salt:                   "testsalt",
		SiteTemplate:           "massively",
		RealityServerName:      "www.microsoft.com",
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		SubscribePort:          deploy.DefaultSubscribePort,
		MonitorPublicPort:      deploy.DefaultMonitorPublicPort,
		MonitorPort:            deploy.DefaultMonitorPort,
		DeployMonitor:          true,
		MonitorAlias:           "US-local",
		TrafficInLimitBytes:    40 << 30,
		TrafficOutLimitBytes:   50 << 30,
		TrafficTotalLimitBytes: 100 << 30,
		ResetDay:               deploy.DefaultResetDay,
		ResetHour:              deploy.DefaultResetHour,
		MonitorInterface:       "eth0",
		MonitorIntervalSeconds: deploy.DefaultMonitorIntervalSeconds,
		OS:                     system.OSRelease{Family: system.FamilyDebian, PackageManager: "apt"},
		Firewall:               system.FirewallUFW,
		Creds:                  creds,
	}
}

func TestUpdateRegeneratesConfigSubscriptionsAndState(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolRealityVision}
	cfg.Ports.Hysteria2 = 0
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}

	runner := &recordingRunner{}
	var checked []config.Protocol
	updated, err := Update(context.Background(), UpdateOptions{
		Layout:   layout,
		Runner:   runner,
		Firewall: system.FirewallUFW,
		Selected: []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		CheckPorts: func(_ context.Context, _ deploy.Config, added []config.Protocol) error {
			checked = append([]config.Protocol(nil), added...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !sameProtocols(updated.Enabled, []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2}) {
		t.Fatalf("Enabled = %#v", updated.Enabled)
	}
	if updated.Ports.Hysteria2 == 0 {
		t.Fatalf("expected generated Hysteria2 port")
	}
	if !sameProtocols(checked, []config.Protocol{config.ProtocolHysteria2}) {
		t.Fatalf("checked added protocols = %#v", checked)
	}

	configBody, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configBody), `"tag": "hysteria2"`) {
		t.Fatalf("config missing Hysteria2 inbound:\n%s", configBody)
	}

	stateBody, err := os.ReadFile(filepath.Join(layout.StateDir, "enabled_protocols"))
	if err != nil {
		t.Fatalf("read enabled_protocols: %v", err)
	}
	if strings.TrimSpace(string(stateBody)) != "vless-reality-vision,hysteria2" {
		t.Fatalf("enabled_protocols = %q", stateBody)
	}

	token := deploy.SubscriptionToken(cfg.Salt)
	if _, err := os.Stat(filepath.Join(layout.SubscribeDir, "singboxProfiles", token)); err != nil {
		t.Fatalf("subscription not refreshed: %v", err)
	}

	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		fmt.Sprintf("ufw allow %d/udp", updated.Ports.Hysteria2),
		"check -c " + layout.ConfigJSON + ".candidate",
		"systemctl restart sing-box.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}

func TestUpdateKeepsPreviousConfigOnValidationFailure(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolRealityVision}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}
	if err := deploy.WriteFile(layout.ConfigJSON, []byte("previous config"), 0o644); err != nil {
		t.Fatalf("write previous config: %v", err)
	}

	runner := &failOnCandidateCheckRunner{}
	_, err := Update(context.Background(), UpdateOptions{
		Layout:     layout,
		Runner:     runner,
		Selected:   []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		CheckPorts: func(context.Context, deploy.Config, []config.Protocol) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "Validate") {
		t.Fatalf("expected validation failure, got %v", err)
	}
	body, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(body) != "previous config" {
		t.Fatalf("previous config was replaced: %q", body)
	}
}

func TestUpdateAppliesCredentialAndPortOverrides(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolHysteria2}
	cfg.Ports.Hysteria2 = 9443
	cfg.Creds.HysteriaPassword = "oldpass"
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}

	runner := &recordingRunner{}
	var checked []config.Protocol
	updated, err := Update(context.Background(), UpdateOptions{
		Layout:            layout,
		Runner:            runner,
		Firewall:          system.FirewallUFW,
		Selected:          []config.Protocol{config.ProtocolHysteria2},
		Ports:             config.Ports{Hysteria2: 18443},
		Creds:             deploy.Credentials{HysteriaPassword: "newpass"},
		CheckPorts: func(_ context.Context, _ deploy.Config, changed []config.Protocol) error {
			checked = append([]config.Protocol(nil), changed...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Ports.Hysteria2 != 18443 || updated.Creds.HysteriaPassword != "newpass" {
		t.Fatalf("override not applied: port=%d password=%q", updated.Ports.Hysteria2, updated.Creds.HysteriaPassword)
	}
	if !sameProtocols(checked, []config.Protocol{config.ProtocolHysteria2}) {
		t.Fatalf("changed port protocols = %#v", checked)
	}

	configBody, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configBody), `"listen_port": 18443`) || !strings.Contains(string(configBody), `"password": "newpass"`) {
		t.Fatalf("config did not include overrides:\n%s", configBody)
	}
	statePassword, err := os.ReadFile(filepath.Join(layout.StateDir, "hysteria2_password"))
	if err != nil {
		t.Fatalf("read password state: %v", err)
	}
	if strings.TrimSpace(string(statePassword)) != "newpass" {
		t.Fatalf("password state = %q", statePassword)
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, "ufw allow 18443/udp") {
		t.Fatalf("missing firewall command for changed port:\n%s", joined)
	}
}

func TestUpdateAppliesRealitySNIOverride(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolRealityVision}
	cfg.RealityServerName = "www.microsoft.com"
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}

	runner := &recordingRunner{}
	updated, err := Update(context.Background(), UpdateOptions{
		Layout:            layout,
		Runner:            runner,
		Selected:          []config.Protocol{config.ProtocolRealityVision},
		RealityServerName: "www.cloudflare.com",
		CheckPorts:        func(context.Context, deploy.Config, []config.Protocol) error { return nil },
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.RealityServerName != "www.cloudflare.com" {
		t.Fatalf("RealityServerName = %q", updated.RealityServerName)
	}
	stateSNI, err := os.ReadFile(filepath.Join(layout.StateDir, "reality_server_name"))
	if err != nil {
		t.Fatalf("read reality_server_name: %v", err)
	}
	if strings.TrimSpace(string(stateSNI)) != "www.cloudflare.com" {
		t.Fatalf("reality_server_name state = %q", stateSNI)
	}
	configBody, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configBody), `"server_name": "www.cloudflare.com"`) {
		t.Fatalf("config missing updated SNI:\n%s", configBody)
	}
}

type failOnCandidateCheckRunner struct{}

func (r *failOnCandidateCheckRunner) Run(c system.Command) error {
	if strings.Contains(c.String(), ".candidate") {
		return fmt.Errorf("candidate invalid")
	}
	return nil
}

func sameProtocols(a, b []config.Protocol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
