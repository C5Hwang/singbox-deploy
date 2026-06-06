package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestUpdateMonitorSettingsUsageAndRemoteSources(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.MonitorAlias = "US-local"
	cfg.ResetHour = 0
	cfg.MonitorIntervalSeconds = DefaultMonitorIntervalSeconds
	if err := writeInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("writeInstallState: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.MonitorDB), 0o755); err != nil {
		t.Fatalf("mkdir monitor db dir: %v", err)
	}
	store, err := monitor.OpenStore(layout.MonitorDB)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC).Unix()
	if err := store.InsertSample(base, "eth0", 100, 50, 100, 50); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	store.Close()

	remote := RemoteSubscription{Domain: "remote.example.com", Port: 9443, Alias: "JP-remote", Salt: "abc", Monitor: true, MonitorPublicPort: 9444}
	fetches := map[string][]byte{
		remote.monitorURL(): []byte(`{"inUsedBytes":10,"outUsedBytes":20,"totalUsedBytes":30,"inRemainingBytes":90,"outRemainingBytes":80,"totalRemainingBytes":70,"inLimitBytes":100,"outLimitBytes":100,"totalLimitBytes":100,"resetTime":"2026-06-15T05:00:00Z","trend":[]}`),
	}
	runner := &recordingRunner{}
	now := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	var checked []system.Port
	updated, err := UpdateMonitor(context.Background(), MonitorUpdateOptions{
		Layout:            layout,
		Runner:            runner,
		SetLocal:          true,
		SetMonitor:        true,
		DeployMonitor:     true,
		MonitorAlias:      "JP-local",
		MonitorPublicPort: 24447,
		MonitorPort:       19091,
		Interface:         "ens3",
		IntervalSeconds:   60,
		InLimitBytes:      200 << 30,
		OutLimitBytes:     300 << 30,
		TotalLimitBytes:   400 << 30,
		ResetDay:          15,
		ResetHour:         5,
		SetCurrentTotals:  true,
		CurrentInBytes:    2 << 30,
		CurrentOutBytes:   3 << 30,
		SetRemotes:        true,
		Remotes:           []RemoteSubscription{remote},
		Firewall:          system.FirewallUFW,
		NginxConfPath:     filepath.Join(root, "nginx", "singbox-deploy.conf"),
		SystemdDir:        filepath.Join(root, "systemd"),
		DeployBin:         "/usr/bin/singbox-deploy",
		Now:               func(context.Context) (time.Time, error) { return now, nil },
		CheckPorts: func(_ context.Context, _ Config, ports []system.Port) error {
			checked = append(checked, ports...)
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
		t.Fatalf("UpdateMonitor error: %v", err)
	}
	if updated.MonitorAlias != "JP-local" || updated.MonitorPublicPort != 24447 || updated.MonitorPort != 19091 || updated.ResetHour != 5 || updated.MonitorIntervalSeconds != 60 {
		t.Fatalf("updated monitor config = %#v", updated)
	}
	if len(checked) != 2 || checked[0].Number != 24447 || !checked[0].Public || checked[1].Number != 19091 || checked[1].Public {
		t.Fatalf("checked ports = %#v", checked)
	}

	unit, err := os.ReadFile(filepath.Join(root, "systemd", system.MonitorService))
	if err != nil {
		t.Fatalf("read monitor unit: %v", err)
	}
	unitText := string(unit)
	for _, want := range []string{"--reset-hour 5", "--alias \"JP-local\"", "--interval-seconds 60", "--remote-monitor " + remoteMonitorPath(layout)} {
		if !strings.Contains(unitText, want) {
			t.Fatalf("monitor unit missing %q:\n%s", want, unitText)
		}
	}
	nginx, err := os.ReadFile(filepath.Join(root, "nginx", "singbox-deploy.conf"))
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	if !strings.Contains(string(nginx), "listen 24447 ssl;") || !strings.Contains(string(nginx), "proxy_pass http://127.0.0.1:19091/") {
		t.Fatalf("nginx config not updated:\n%s", nginx)
	}

	totals, err := CurrentTrafficTotals(layout, updated, now)
	if err != nil {
		t.Fatalf("CurrentTrafficTotals: %v", err)
	}
	if totals.InBytes != 2<<30 || totals.OutBytes != 3<<30 {
		t.Fatalf("totals = %#v", totals)
	}
	remoteBody, err := os.ReadFile(remoteMonitorPath(layout))
	if err != nil {
		t.Fatalf("read remote monitor: %v", err)
	}
	var sources []monitor.SourceSummary
	if err := json.Unmarshal(remoteBody, &sources); err != nil {
		t.Fatalf("decode remote monitor: %v", err)
	}
	if len(sources) != 1 || sources[0].Name != subscription.AddNodePrefixFlag(remote.Alias) || sources[0].TotalUsedBytes != 30 {
		t.Fatalf("remote sources = %#v", sources)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{"ufw allow 24447/tcp", "nginx -t", "systemctl restart nginx", "systemctl restart singbox-deploy-monitor.service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}
