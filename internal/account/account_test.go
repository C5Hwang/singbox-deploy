package account_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/account"
	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
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
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}

	runner := &recordingRunner{}
	updated, err := account.Update(context.Background(), account.UpdateOptions{
		Layout:      layout,
		Runner:      runner,
		DisplayName: "NewNode",
	})
	if err != nil {
		t.Fatalf("account.Update error: %v", err)
	}
	if updated.DisplayName != "NewNode" {
		t.Fatalf("DisplayName = %q", updated.DisplayName)
	}

	stateName, err := os.ReadFile(filepath.Join(layout.StateDir, "display_name"))
	if err != nil {
		t.Fatalf("read display_name state: %v", err)
	}
	if strings.TrimSpace(string(stateName)) != "NewNode" {
		t.Fatalf("display_name state = %q", stateName)
	}
	configBody, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configBody), "NewNode-Reality-Vision") {
		t.Fatalf("config did not include new display name:\n%s", configBody)
	}
	token := subscription.TokenFromSalt(cfg.Salt)
	defaultBody, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read default subscription: %v", err)
	}
	decoded, err := subscription.DecodeBase64(string(defaultBody))
	if err != nil {
		t.Fatalf("decode default subscription: %v", err)
	}
	if !strings.Contains(decoded, "NewNode-Reality-Vision") {
		t.Fatalf("subscription did not include new display name:\n%s", decoded)
	}
	if !strings.Contains(strings.Join(runner.commands, "\n"), "systemctl restart sing-box.service") {
		t.Fatalf("missing sing-box restart command: %#v", runner.commands)
	}
}
