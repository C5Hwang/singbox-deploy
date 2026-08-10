package monitor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
		MonitorDomain:          cfg.MonitorHost(),
		DeployMonitor:          cfg.DeployMonitor,
		MonitorAlias:           cfg.MonitorAlias,
		MonitorToken:           cfg.MonitorToken,
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

func toManageMonitorSources(sources []deploy.MonitorSource) []monitor.ManageMonitorSource {
	out := make([]monitor.ManageMonitorSource, len(sources))
	for i, s := range sources {
		out[i] = monitor.ManageMonitorSource{Domain: s.Domain, Alias: s.Alias, MonitorPublicPort: s.MonitorPublicPort}
	}
	return out
}

func fromManageMonitorSources(sources []monitor.ManageMonitorSource) []deploy.MonitorSource {
	out := make([]deploy.MonitorSource, len(sources))
	for i, s := range sources {
		out[i] = deploy.MonitorSource{Domain: s.Domain, Alias: s.Alias, MonitorPublicPort: s.MonitorPublicPort}
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

	monitorSrc := deploy.MonitorSource{Domain: "remote.example.com", Alias: "JP-remote", MonitorPublicPort: 9444}
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
		SetMonitorSources: true,
		MonitorSources:    toManageMonitorSources([]deploy.MonitorSource{monitorSrc}),
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
		LoadMonitorSources: func(l paths.Layout) ([]monitor.ManageMonitorSource, error) {
			srcs, err := deploy.LoadMonitorSources(l)
			if err != nil {
				return nil, err
			}
			return toManageMonitorSources(srcs), nil
		},
		ValidateMonitorSources: func(sources []monitor.ManageMonitorSource) error {
			return deploy.ValidateMonitorSources(fromManageMonitorSources(sources))
		},
		SaveMonitorSources: func(l paths.Layout, sources []monitor.ManageMonitorSource) error {
			return deploy.SaveMonitorSources(l, fromManageMonitorSources(sources))
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
		RefreshRemoteMonitor: func(ctx context.Context, l paths.Layout, sources []monitor.ManageMonitorSource, fetch func(context.Context, string) ([]byte, error)) error {
			return deploy.RefreshRemoteMonitor(ctx, l, fromManageMonitorSources(sources), deploy.SubscriptionFetcher(fetch))
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
	for _, want := range []string{"--reset-hour 5", "--alias \"JP-local\"", "--interval-seconds 60", "--remote-monitor " + strconv.Quote(deploy.RemoteMonitorPath(layout))} {
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
	if len(sources) != 1 || sources[0].Name != subscription.AddNodePrefixFlag(monitorSrc.Alias) || sources[0].TotalUsedBytes != 30 {
		t.Fatalf("remote sources = %#v", sources)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{"ufw allow 24447/tcp", "nginx -t", "systemctl restart nginx", "systemctl restart singbox-deploy-monitor.service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}

// monitorDomainUpdateOptions is a minimal local-settings update: every callback
// is a stub except the ones the monitor-domain path depends on.
func monitorDomainUpdateOptions(t *testing.T, root string, layout paths.Layout, record *[]string) monitor.UpdateOptions {
	t.Helper()
	return monitor.UpdateOptions{
		Layout:        layout,
		Runner:        &recordingRunner{},
		SetLocal:      true,
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
		SystemdDir:    filepath.Join(root, "systemd"),
		DeployBin:     "/usr/bin/singbox-deploy",
		CheckPorts:    func(context.Context, monitor.ManageConfig, []system.Port) error { return nil },
		LoadConfig: func(l paths.Layout) (monitor.ManageConfig, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return monitor.ManageConfig{}, err
			}
			return toManageConfig(dcfg), nil
		},
		LoadMonitorSources:     func(paths.Layout) ([]monitor.ManageMonitorSource, error) { return nil, nil },
		ValidateMonitorSources: func([]monitor.ManageMonitorSource) error { return nil },
		SaveMonitorSources:     func(paths.Layout, []monitor.ManageMonitorSource) error { return nil },
		Progress: func(e monitor.ManageEvent) {
			if e.Status == "running" {
				*record = append(*record, "step:"+e.Label)
			}
		},
		WriteManagedNginxConfig: func(l paths.Layout, mcfg monitor.ManageConfig, confPath string) error {
			*record = append(*record, "nginx:"+mcfg.MonitorDomain)
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.MonitorDomain = mcfg.MonitorDomain
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			return deploy.WriteManagedNginxConfig(l, dcfg, confPath)
		},
		RenderMonitorUnit: func(l paths.Layout, deployBin string, mcfg monitor.ManageConfig) (string, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return "", err
			}
			return deploy.RenderMonitorUnit(l, deployBin, dcfg)
		},
		WriteState: func(stateDir string, mcfg monitor.ManageConfig) error {
			dcfg, err := deploy.LoadProtocolConfig(layout)
			if err != nil {
				return err
			}
			dcfg.MonitorDomain = mcfg.MonitorDomain
			return deploy.WriteInstallState(stateDir, dcfg)
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}

// Moving the monitor to a new name rewrites Nginx onto that name's certificate
// and persists it. Issuance is the caller's precondition, not a step here, so
// the run must not carry one.
func TestUpdateSettingsRewritesNginxOnAMonitorDomainMove(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	var record []string
	opts := monitorDomainUpdateOptions(t, root, layout, &record)
	opts.MonitorDomain = "monitor.example.com"

	updated, err := monitor.UpdateSettings(context.Background(), opts)
	if err != nil {
		t.Fatalf("UpdateSettings error: %v", err)
	}
	if updated.MonitorDomain != "monitor.example.com" {
		t.Fatalf("updated monitor domain = %q", updated.MonitorDomain)
	}
	want := []string{"step:Nginx", "nginx:monitor.example.com", "step:Monitor service", "step:State"}
	if strings.Join(record, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", record, want)
	}
	stored, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		t.Fatalf("LoadProtocolConfig: %v", err)
	}
	if stored.MonitorHost() != "monitor.example.com" {
		t.Fatalf("persisted monitor domain = %q", stored.MonitorHost())
	}
	nginx, err := os.ReadFile(opts.NginxConfPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	for _, wantLine := range []string{
		"server_name monitor.example.com;",
		filepath.Join(layout.TLSDir, "monitor.example.com.crt"),
		"ssl_reject_handshake on;",
	} {
		if !strings.Contains(string(nginx), wantLine) {
			t.Fatalf("nginx config missing %q:\n%s", wantLine, nginx)
		}
	}
}

// Editing any other monitor setting rewrites Nginx too, and likewise reaches no
// certificate step.
func TestUpdateSettingsRunsNoCertificateStepForAnUnchangedMonitorDomain(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.MonitorDomain = "monitor.example.com"
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	var record []string
	opts := monitorDomainUpdateOptions(t, root, layout, &record)
	opts.MonitorDomain = "monitor.example.com"
	opts.MonitorPort = 19099

	if _, err := monitor.UpdateSettings(context.Background(), opts); err != nil {
		t.Fatalf("UpdateSettings error: %v", err)
	}
	for _, step := range record {
		if step == "step:Certificate" {
			t.Fatalf("the monitor update must not issue certificates: %v", record)
		}
	}
	if !slices.Contains(record, "step:Nginx") {
		t.Fatalf("a changed monitor port should still rewrite Nginx: %v", record)
	}
}

// The monitor reads its token from state when it starts, and the state step
// runs last, so the token has to be on disk before the service is restarted.
func TestUpdateSettingsWritesAccessTokenBeforeRestartingTheService(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.MonitorToken = "old-monitor-token"
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	var record []string
	opts := monitorDomainUpdateOptions(t, root, layout, &record)
	opts.SetMonitor = true
	opts.DeployMonitor = true
	opts.MonitorToken = "new-monitor-token"
	// Capture what the token file holds at the moment the unit is rendered,
	// which is immediately before the restart commands run.
	var tokenAtRender string
	render := opts.RenderMonitorUnit
	opts.RenderMonitorUnit = func(l paths.Layout, deployBin string, mcfg monitor.ManageConfig) (string, error) {
		tokenAtRender = monitor.ReadAccessToken(l.StateDir)
		return render(l, deployBin, mcfg)
	}

	updated, err := monitor.UpdateSettings(context.Background(), opts)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if updated.MonitorToken != "new-monitor-token" {
		t.Fatalf("MonitorToken = %q, want %q", updated.MonitorToken, "new-monitor-token")
	}
	if tokenAtRender != "new-monitor-token" {
		t.Fatalf("token seen by the restarting monitor = %q, want %q", tokenAtRender, "new-monitor-token")
	}
}

// An empty token is the meaningful "publish without a gate" answer, so it must
// survive the round trip rather than being treated as "leave unchanged".
func TestUpdateSettingsClearsAccessToken(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	cfg.MonitorToken = "old-monitor-token"
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	var record []string
	opts := monitorDomainUpdateOptions(t, root, layout, &record)
	opts.SetMonitor = true
	opts.DeployMonitor = true
	opts.MonitorToken = ""

	updated, err := monitor.UpdateSettings(context.Background(), opts)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if updated.MonitorToken != "" {
		t.Fatalf("MonitorToken = %q, want empty", updated.MonitorToken)
	}
	if got := monitor.ReadAccessToken(layout.StateDir); got != "" {
		t.Fatalf("stored token = %q, want empty", got)
	}
}
