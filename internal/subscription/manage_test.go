package subscription_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
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

// TestUpdateRegeneratesAndReloads exercises the subscription update flow
// after the move to cluster-based aggregation: a salt+port rotation should
// regenerate subscription files, clean up the stale token, persist state,
// open the new firewall port, and reload Nginx.
func TestUpdateRegeneratesAndReloads(t *testing.T) {
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

	registry := cluster.NewRegistry(layout)
	runner := &recordingRunner{}
	var checkedPort int
	updated, err := subscription.Update(context.Background(), subscription.UpdateOptions{
		Layout:        layout,
		Runner:        runner,
		Salt:          "newsalt",
		SubscribePort: 24443,
		Firewall:      system.FirewallUFW,
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CheckPorts: func(_ context.Context, _ string, port int) error {
			checkedPort = port
			return nil
		},
		LoadConfig: func(l paths.Layout) (subscription.Config, error) {
			return subscription.Config{
				Domain:        cfg.Domain,
				Salt:          cfg.Salt,
				SubscribePort: cfg.SubscribePort,
			}, nil
		},
		WriteState:       func(stateDir string, c subscription.Config) error { return nil },
		WriteNginxConfig: func(l paths.Layout, c subscription.Config, confPath string) error { return nil },
		WriteSubscriptions: func(_ context.Context, l paths.Layout, c subscription.Config) error {
			full := deploy.Config{
				Domain:        c.Domain,
				Salt:          c.Salt,
				SubscribePort: c.SubscribePort,
				DisplayName:   cfg.DisplayName,
				Creds:         cfg.Creds,
			}
			return registry.WriteFleetSubscriptions(l, full)
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

	if _, err := os.Stat(filepath.Join(layout.SubscribeDir, "default", oldToken)); err == nil {
		t.Fatalf("old subscription token file should be removed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat old subscription token: %v", err)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{"ufw allow 24443/tcp", "nginx -t", "systemctl restart nginx"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
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
