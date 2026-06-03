package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestUpdateProtocolsRegeneratesConfigSubscriptionsAndState(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolRealityVision}
	cfg.Ports.Hysteria2 = 0
	if err := writeInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}

	runner := &recordingRunner{}
	var checked []config.Protocol
	updated, err := UpdateProtocols(context.Background(), ProtocolUpdateOptions{
		Layout:   layout,
		Runner:   runner,
		Firewall: system.FirewallUFW,
		Selected: []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		CheckPorts: func(_ context.Context, _ Config, added []config.Protocol) error {
			checked = append([]config.Protocol(nil), added...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("UpdateProtocols error: %v", err)
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
	if strings.TrimSpace(string(stateBody)) != "reality-vision,hysteria2" {
		t.Fatalf("enabled_protocols = %q", stateBody)
	}

	token := subscriptionToken(cfg.Salt)
	if _, err := os.Stat(filepath.Join(layout.SubscribeDir, "sing-box", token)); err != nil {
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

func TestUpdateProtocolsKeepsPreviousConfigOnValidationFailure(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.Enabled = []config.Protocol{config.ProtocolRealityVision}
	if err := writeInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}
	if err := writeFile(layout.ConfigJSON, []byte("previous config"), 0o644); err != nil {
		t.Fatalf("write previous config: %v", err)
	}

	runner := &failOnCandidateCheckRunner{}
	_, err := UpdateProtocols(context.Background(), ProtocolUpdateOptions{
		Layout:     layout,
		Runner:     runner,
		Selected:   []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		CheckPorts: func(context.Context, Config, []config.Protocol) error { return nil },
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
