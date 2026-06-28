package subscription_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestUpdateAggregatesRemoteAndMonitor(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}
	oldToken := subscription.TokenFromSalt(cfg.Salt)
	if err := deploy.WriteFile(filepath.Join(layout.SubscribeDir, "default", oldToken), []byte("old subscription"), 0o644); err != nil {
		t.Fatalf("write old subscription: %v", err)
	}

	remote := subscription.Remote{Domain: "remote.example.com", Port: 9443, Alias: "US-edge", Salt: "abc"}
	remoteEntry := subscription.RemoteEntry{Domain: "remote.example.com", Port: 9443, Alias: "US-edge", Salt: "abc"}
	fetches := map[string][]byte{
		remoteEntry.DefaultURL():         []byte(subscription.EncodeBase64("hysteria2://pass@remote.example.com:443#JP-01")),
		remoteEntry.ClashURL():           []byte("proxies:\n  - name: \"JP-01\"\n    type: hysteria2\n"),
		remoteEntry.SingBoxProfilesURL(): []byte(`{"outbounds":[{"type":"selector","tag":"PROXY"},{"type":"hysteria2","tag":"JP-01"},{"type":"direct","tag":"direct"}]}`),
		remoteEntry.SurgeURL():           []byte("JP-01 = hysteria2, remote.example.com, 443, password=pass\n"),
	}
	runner := &recordingRunner{}
	var checkedPort int
	updated, err := subscription.Update(context.Background(), subscription.UpdateOptions{
		Layout:        layout,
		Runner:        runner,
		Salt:          "newsalt",
		SubscribePort: 24443,
		Remotes:       []subscription.Remote{remote},
		SetRemotes:    true,
		Firewall:      system.FirewallUFW,
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CheckPorts: func(_ context.Context, _ string, port int) error {
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
		LoadConfig: func(l paths.Layout) (subscription.Config, error) {
			return subscription.Config{
				Domain:        cfg.Domain,
				Salt:          cfg.Salt,
				SubscribePort: cfg.SubscribePort,
			}, nil
		},
		LoadRemotes:     func(l paths.Layout) ([]subscription.Remote, error) { return nil, nil },
		ValidateRemotes: func(r []subscription.Remote) error { return nil },
		WriteState:      func(stateDir string, c subscription.Config) error { return nil },
		SaveRemotes: func(l paths.Layout, r []subscription.Remote) error {
			return deploy.SaveRemoteSubscriptions(l, toDeployRemotes(r))
		},
		WriteNginxConfig: func(l paths.Layout, c subscription.Config, confPath string) error { return nil },
		WriteWithRemotes: func(ctx context.Context, l paths.Layout, c subscription.Config, remotes []subscription.Remote, fetch subscription.Fetcher) error {
			dcfg := deploy.Config{Domain: c.Domain, Salt: c.Salt, SubscribePort: c.SubscribePort, Creds: cfg.Creds, DisplayName: cfg.DisplayName}
			return deploy.WriteSubscriptionsWithRemotes(ctx, l, dcfg, toDeployRemotes(remotes), deploy.SubscriptionFetcher(fetch), 0)
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error { return deploy.RunCommands(r, cmds...) },
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
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
	surgeBody, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "surge", token))
	if err != nil {
		t.Fatalf("read surge subscription: %v", err)
	}
	if !strings.Contains(string(surgeBody), "US-edge-01") {
		t.Fatalf("surge subscription missing renamed remote node name:\n%s", surgeBody)
	}

	dRemotes, err := deploy.LoadRemoteSubscriptions(layout)
	if err != nil {
		t.Fatalf("LoadRemoteSubscriptions error: %v", err)
	}
	if len(dRemotes) != 1 || dRemotes[0].Domain != remote.Domain || dRemotes[0].Alias != remote.Alias {
		t.Fatalf("remote state = %#v", dRemotes)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{"ufw allow 24443/tcp", "nginx -t", "systemctl restart nginx"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}

func toDeployRemotes(remotes []subscription.Remote) []deploy.RemoteSubscription {
	out := make([]deploy.RemoteSubscription, len(remotes))
	for i, r := range remotes {
		out[i] = deploy.RemoteSubscription{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt}
	}
	return out
}

func testConfig(t *testing.T) deploy.Config {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	return deploy.Config{
		Domain:        "example.com",
		DisplayName:   "US-vps1",
		Salt:          "testsalt",
		SubscribePort: deploy.DefaultSubscribePort,
		Creds:         creds,
	}
}
