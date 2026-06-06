package monitor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
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
		DisplayName:            "US-vps1",
		Salt:                   "testsalt",
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
		MonitorIntervalSeconds: deploy.DefaultMonitorIntervalSeconds,
		Creds:                  creds,
	}
}

func toManageConfig(cfg deploy.Config) monitor.ManageConfig {
	return monitor.ManageConfig{
		Domain:                 cfg.Domain,
		DeployMonitor:          cfg.DeployMonitor,
		MonitorAlias:           cfg.MonitorAlias,
		MonitorPublicPort:      cfg.MonitorPublicPort,
		MonitorPort:            cfg.MonitorPort,
		MonitorInterface:       cfg.MonitorInterface,
		MonitorIntervalSeconds: cfg.MonitorIntervalSeconds,
		TrafficInLimitBytes:    cfg.TrafficInLimitBytes,
		TrafficOutLimitBytes:   cfg.TrafficOutLimitBytes,
		TrafficTotalLimitBytes: cfg.TrafficTotalLimitBytes,
		ResetDay:               cfg.ResetDay,
		ResetHour:              cfg.ResetHour,
		SubscribePort:          cfg.SubscribePort,
	}
}

func toManageRemotes(remotes []deploy.RemoteSubscription) []monitor.ManageRemote {
	out := make([]monitor.ManageRemote, len(remotes))
	for i, r := range remotes {
		out[i] = monitor.ManageRemote{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt, Monitor: r.Monitor, MonitorPublicPort: r.MonitorPublicPort}
	}
	return out
}

func toDeployRemotes(remotes []monitor.ManageRemote) []deploy.RemoteSubscription {
	out := make([]deploy.RemoteSubscription, len(remotes))
	for i, r := range remotes {
		out[i] = deploy.RemoteSubscription{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt, Monitor: r.Monitor, MonitorPublicPort: r.MonitorPublicPort}
	}
	return out
}

func TestUpdateSettingsUsageAndRemoteSources(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.MonitorAlias = "US-local"
	cfg.ResetHour = 0
	cfg.MonitorIntervalSeconds = deploy.DefaultMonitorIntervalSeconds
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
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

	remote := deploy.RemoteSubscription{Domain: "remote.example.com", Port: 9443, Alias: "JP-remote", Salt: "abc", Monitor: true, MonitorPublicPort: 9444}
	monitorURL := fmt.Sprintf("https://remote.example.com:9444/monitor/api/summary")
	fetches := map[string][]byte{
		monitorURL: []byte(`{"inUsedBytes":10,"outUsedBytes":20,"totalUsedBytes":30,"inRemainingBytes":90,"outRemainingBytes":80,"totalRemainingBytes":70,"inLimitBytes":100,"outLimitBytes":100,"totalLimitBytes":100,"resetTime":"2026-06-15T05:00:00Z","trend":[]}`),
	}
	runner := &recordingRunner{}
	now := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	var checked []system.Port
	updated, err := monitor.UpdateSettings(context.Background(), monitor.UpdateOptions{
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
		Remotes:           toManageRemotes([]deploy.RemoteSubscription{remote}),
		Firewall:          system.FirewallUFW,
		NginxConfPath:     filepath.Join(root, "nginx", "singbox-deploy.conf"),
		SystemdDir:        filepath.Join(root, "systemd"),
		DeployBin:         "/usr/bin/singbox-deploy",
		Now:               func(context.Context) (time.Time, error) { return now, nil },
		CheckPorts: func(_ context.Context, _ monitor.ManageConfig, ports []system.Port) error {
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
		LoadConfig: func(l paths.Layout) (monitor.ManageConfig, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return monitor.ManageConfig{}, err
			}
			return toManageConfig(dcfg), nil
		},
		LoadRemotes: func(l paths.Layout) ([]monitor.ManageRemote, error) {
			dr, err := deploy.LoadRemoteSubscriptions(l)
			if err != nil {
				return nil, err
			}
			return toManageRemotes(dr), nil
		},
		ValidateRemotes: func(remotes []monitor.ManageRemote) error {
			return deploy.ValidateRemoteSubscriptions(toDeployRemotes(remotes))
		},
		SaveRemotes: func(l paths.Layout, remotes []monitor.ManageRemote) error {
			return deploy.SaveRemoteSubscriptions(l, toDeployRemotes(remotes))
		},
		WriteState: func(stateDir string, mcfg monitor.ManageConfig) error {
			dcfg, err := deploy.LoadProtocolConfig(layout)
			if err != nil {
				return err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.MonitorAlias = mcfg.MonitorAlias
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.MonitorInterface = mcfg.MonitorInterface
			dcfg.MonitorIntervalSeconds = mcfg.MonitorIntervalSeconds
			dcfg.TrafficInLimitBytes = mcfg.TrafficInLimitBytes
			dcfg.TrafficOutLimitBytes = mcfg.TrafficOutLimitBytes
			dcfg.TrafficTotalLimitBytes = mcfg.TrafficTotalLimitBytes
			dcfg.ResetDay = mcfg.ResetDay
			dcfg.ResetHour = mcfg.ResetHour
			return deploy.WriteInstallState(stateDir, dcfg)
		},
		WriteManagedNginxConfig: func(l paths.Layout, mcfg monitor.ManageConfig, confPath string) error {
			dcfg, _ := deploy.LoadProtocolConfig(l)
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.SubscribePort = mcfg.SubscribePort
			return deploy.WriteManagedNginxConfig(l, dcfg, confPath)
		},
		RenderMonitorUnit: func(l paths.Layout, deployBin string, mcfg monitor.ManageConfig) (string, error) {
			dcfg, _ := deploy.LoadProtocolConfig(l)
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.MonitorAlias = mcfg.MonitorAlias
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.MonitorInterface = mcfg.MonitorInterface
			dcfg.MonitorIntervalSeconds = mcfg.MonitorIntervalSeconds
			dcfg.TrafficInLimitBytes = mcfg.TrafficInLimitBytes
			dcfg.TrafficOutLimitBytes = mcfg.TrafficOutLimitBytes
			dcfg.TrafficTotalLimitBytes = mcfg.TrafficTotalLimitBytes
			dcfg.ResetDay = mcfg.ResetDay
			dcfg.ResetHour = mcfg.ResetHour
			return deploy.RenderMonitorUnit(l, deployBin, dcfg)
		},
		RefreshRemoteMonitor: func(ctx context.Context, l paths.Layout, remotes []monitor.ManageRemote, fetch func(context.Context, string) ([]byte, error)) error {
			return deploy.RefreshRemoteMonitor(ctx, l, toDeployRemotes(remotes), deploy.SubscriptionFetcher(fetch))
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	})
	if err != nil {
		t.Fatalf("UpdateSettings error: %v", err)
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
	for _, want := range []string{"--reset-hour 5", "--alias \"JP-local\"", "--interval-seconds 60", "--remote-monitor " + deploy.RemoteMonitorPath(layout)} {
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

	totals, err := monitor.CurrentTrafficTotals(layout, updated.ResetDay, updated.ResetHour, now)
	if err != nil {
		t.Fatalf("CurrentTrafficTotals: %v", err)
	}
	if totals.InBytes != 2<<30 || totals.OutBytes != 3<<30 {
		t.Fatalf("totals = %#v", totals)
	}
	remoteBody, err := os.ReadFile(deploy.RemoteMonitorPath(layout))
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
