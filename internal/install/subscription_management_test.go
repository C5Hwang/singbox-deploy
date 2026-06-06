package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestUpdateAccountRegeneratesConfigSubscriptionsAndState(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	if err := writeInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}

	runner := &recordingRunner{}
	updated, err := UpdateAccount(context.Background(), AccountUpdateOptions{
		Layout:      layout,
		Runner:      runner,
		DisplayName: "NewNode",
	})
	if err != nil {
		t.Fatalf("UpdateAccount error: %v", err)
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

func TestUpdateSubscriptionsAggregatesRemoteAndMonitor(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	if err := writeInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}
	oldToken := subscription.TokenFromSalt(cfg.Salt)
	if err := writeFile(filepath.Join(layout.SubscribeDir, "default", oldToken), []byte("old subscription"), 0o644); err != nil {
		t.Fatalf("write old subscription: %v", err)
	}

	remote := RemoteSubscription{Domain: "remote.example.com", Port: 9443, Alias: "US-edge", Salt: "abc", Monitor: true, MonitorPublicPort: 9444}
	remoteEntry := remote.entry()
	fetches := map[string][]byte{
		remoteEntry.DefaultURL(): []byte(subscription.EncodeBase64("hysteria2://pass@remote.example.com:443#JP-01")),
		remoteEntry.ClashURL():   []byte("proxies:\n  - name: \"JP-01\"\n    type: hysteria2\n"),
		remoteEntry.SingBoxURL(): []byte(`{"outbounds":[{"type":"selector","tag":"PROXY"},{"type":"hysteria2","tag":"JP-01"},{"type":"direct","tag":"direct"}]}`),
		remote.monitorURL():      []byte(`{"inUsedBytes":100,"outUsedBytes":200,"totalUsedBytes":300,"inRemainingBytes":900,"outRemainingBytes":800,"totalRemainingBytes":700,"inLimitBytes":1000,"outLimitBytes":1000,"totalLimitBytes":1000,"resetTime":"2026-06-01T00:00:00Z","trend":[]}`),
	}
	runner := &recordingRunner{}
	var checkedPort int
	updated, err := UpdateSubscriptions(context.Background(), SubscriptionUpdateOptions{
		Layout:        layout,
		Runner:        runner,
		Salt:          "newsalt",
		SubscribePort: 24443,
		Remotes:       []RemoteSubscription{remote},
		SetRemotes:    true,
		Firewall:      system.FirewallUFW,
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CheckPorts: func(_ context.Context, _ Config, port int) error {
			checkedPort = port
			return nil
		},
		Fetch: func(_ context.Context, url string) ([]byte, error) {
			body, ok := fetches[url]
			if !ok {
				return nil, fmt.Errorf("unexpected fetch %s", url)
			}
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("UpdateSubscriptions error: %v", err)
	}
	if updated.Salt != "newsalt" || updated.SubscribePort != 24443 {
		t.Fatalf("updated subscription settings = salt %q port %d", updated.Salt, updated.SubscribePort)
	}
	if checkedPort != 24443 {
		t.Fatalf("checked port = %d", checkedPort)
	}

	token := subscription.TokenFromSalt("newsalt")
	defaultBody, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read default subscription: %v", err)
	}
	decoded, err := subscription.DecodeBase64(string(defaultBody))
	if err != nil {
		t.Fatalf("decode default subscription: %v", err)
	}
	if !strings.Contains(decoded, "US-edge-01") {
		t.Fatalf("default subscription missing renamed remote node name:\n%s", decoded)
	}
	if _, err := os.Stat(filepath.Join(layout.SubscribeDir, "default", oldToken)); err == nil {
		t.Fatalf("old subscription token file should be removed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat old subscription token: %v", err)
	}
	clashBody, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "clashMeta", token))
	if err != nil {
		t.Fatalf("read clash subscription: %v", err)
	}
	if !strings.Contains(string(clashBody), "US-edge-01") {
		t.Fatalf("clash subscription missing renamed remote node name:\n%s", clashBody)
	}
	if strings.Contains(string(clashBody), "proxies:\n- name:") || !strings.HasPrefix(string(clashBody), "proxies:\n  - name:") {
		t.Fatalf("clash subscription first proxy should stay indented:\n%s", clashBody)
	}
	singBoxOutbounds, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "sing-boxProfiles", token))
	if err != nil {
		t.Fatalf("read sing-box outbounds: %v", err)
	}
	if !strings.Contains(string(singBoxOutbounds), "US-edge-01") || strings.Contains(string(singBoxOutbounds), "PROXY") {
		t.Fatalf("sing-box outbounds should include renamed remote node only:\n%s", singBoxOutbounds)
	}

	remotes, err := LoadRemoteSubscriptions(layout)
	if err != nil {
		t.Fatalf("LoadRemoteSubscriptions error: %v", err)
	}
	if len(remotes) != 1 || remotes[0].Domain != remote.Domain || remotes[0].Alias != remote.Alias || !remotes[0].Monitor {
		t.Fatalf("remote state = %#v", remotes)
	}
	trafficBody, err := os.ReadFile(remoteMonitorPath(layout))
	if err != nil {
		t.Fatalf("read remote monitor snapshot: %v", err)
	}
	var sources []monitor.SourceSummary
	if err := json.Unmarshal(trafficBody, &sources); err != nil {
		t.Fatalf("decode remote monitor: %v", err)
	}
	if len(sources) != 1 || sources[0].Name != subscription.AddNodePrefixFlag(remote.Alias) || sources[0].TotalUsedBytes != 300 {
		t.Fatalf("remote monitor sources = %#v", sources)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{"ufw allow 24443/tcp", "nginx -t", "systemctl restart nginx"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}
