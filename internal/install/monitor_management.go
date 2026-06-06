package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// MonitorUpdateOptions describes updates to the local monitor settings, current
// cycle usage counters, and selected remote monitor sources.
type MonitorUpdateOptions struct {
	Layout paths.Layout
	Runner system.Runner

	SetLocal          bool
	SetMonitor        bool
	DeployMonitor     bool
	MonitorAlias      string
	MonitorPublicPort int
	MonitorPort       int
	Interface         string
	IntervalSeconds   int
	InLimitBytes      uint64
	OutLimitBytes     uint64
	TotalLimitBytes   uint64
	ResetDay          int
	ResetHour         int

	SetCurrentTotals bool
	CurrentInBytes   uint64
	CurrentOutBytes  uint64

	SetRemotes bool
	Remotes    []RemoteSubscription

	Firewall      system.Firewall
	CheckPorts    func(context.Context, Config, []system.Port) error
	Fetch         SubscriptionFetcher
	Now           func(context.Context) (time.Time, error)
	Progress      func(Event)
	NginxConfPath string
	SystemdDir    string
	DeployBin     string
}

// UpdateMonitor applies monitor settings to an existing installation.
func UpdateMonitor(ctx context.Context, opts MonitorUpdateOptions) (Config, error) {
	opts = defaultMonitorOptions(opts)
	cfg, err := LoadProtocolConfig(opts.Layout)
	if err != nil {
		return Config{}, err
	}
	old := cfg
	if opts.SetLocal {
		applyMonitorOptions(&cfg, opts)
	}
	if cfg.DeployMonitor && strings.TrimSpace(cfg.MonitorInterface) == "" {
		iface, err := monitor.DefaultInterface()
		if err != nil {
			return Config{}, err
		}
		cfg.MonitorInterface = iface
	}
	if err := validateMonitorConfig(cfg); err != nil {
		return Config{}, err
	}

	remotes := opts.Remotes
	if !opts.SetRemotes {
		remotes, err = LoadRemoteSubscriptions(opts.Layout)
		if err != nil {
			return Config{}, err
		}
	}
	if err := validateRemoteSubscriptions(remotes); err != nil {
		return Config{}, err
	}

	steps := monitorUpdateSteps(opts, old, cfg, remotes)
	for i, s := range steps {
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

type monitorUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, Config) error
}

func defaultMonitorOptions(opts MonitorUpdateOptions) MonitorUpdateOptions {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.Fetch == nil {
		opts.Fetch = defaultSubscriptionFetch
	}
	if opts.Now == nil {
		opts.Now = func(ctx context.Context) (time.Time, error) { return monitor.NetworkGMTNow(ctx) }
	}
	if opts.CheckPorts == nil {
		opts.CheckPorts = func(ctx context.Context, cfg Config, ports []system.Port) error {
			if len(ports) == 0 {
				return nil
			}
			return system.CheckPorts(ctx, cfg.Domain, ports)
		}
	}
	if opts.NginxConfPath == "" {
		opts.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	if opts.SystemdDir == "" {
		opts.SystemdDir = "/etc/systemd/system"
	}
	if opts.DeployBin == "" {
		opts.DeployBin = "/usr/bin/singbox-deploy"
	}
	return opts
}

func applyMonitorOptions(cfg *Config, opts MonitorUpdateOptions) {
	if opts.SetMonitor {
		cfg.DeployMonitor = opts.DeployMonitor
	}
	if strings.TrimSpace(opts.MonitorAlias) != "" {
		cfg.MonitorAlias = strings.TrimSpace(opts.MonitorAlias)
	}
	if cfg.MonitorAlias == "" {
		cfg.MonitorAlias = DefaultMonitorAlias
	}
	if opts.MonitorPublicPort > 0 {
		cfg.MonitorPublicPort = opts.MonitorPublicPort
	}
	if opts.MonitorPort > 0 {
		cfg.MonitorPort = opts.MonitorPort
	}
	if strings.TrimSpace(opts.Interface) != "" {
		cfg.MonitorInterface = strings.TrimSpace(opts.Interface)
	}
	if opts.IntervalSeconds > 0 {
		cfg.MonitorIntervalSeconds = opts.IntervalSeconds
	}
	cfg.TrafficInLimitBytes = opts.InLimitBytes
	cfg.TrafficOutLimitBytes = opts.OutLimitBytes
	cfg.TrafficTotalLimitBytes = opts.TotalLimitBytes
	if opts.ResetDay > 0 {
		cfg.ResetDay = opts.ResetDay
	}
	cfg.ResetHour = opts.ResetHour
}

func validateMonitorConfig(cfg Config) error {
	if !cfg.DeployMonitor {
		return nil
	}
	if strings.TrimSpace(cfg.MonitorAlias) == "" {
		return fmt.Errorf("monitor alias is required")
	}
	if cfg.MonitorPublicPort <= 0 || cfg.MonitorPublicPort > 65535 {
		return fmt.Errorf("monitor public port must be between 1 and 65535")
	}
	if cfg.MonitorPort <= 0 || cfg.MonitorPort > 65535 {
		return fmt.Errorf("monitor service port must be between 1 and 65535")
	}
	if cfg.ResetDay < 1 || cfg.ResetDay > 28 {
		return fmt.Errorf("reset day must be between 1 and 28")
	}
	if cfg.ResetHour < 0 || cfg.ResetHour > 23 {
		return fmt.Errorf("reset hour must be between 0 and 23")
	}
	if cfg.MonitorIntervalSeconds < 10 {
		return fmt.Errorf("sampling interval must be at least 10 seconds")
	}
	return nil
}

func monitorUpdateSteps(opts MonitorUpdateOptions, old, cfg Config, remotes []RemoteSubscription) []monitorUpdateStep {
	var steps []monitorUpdateStep
	changedPorts := monitorChangedPortChecks(old, cfg)
	if opts.SetLocal && len(changedPorts) > 0 {
		steps = append(steps, monitorUpdateStep{label: "Port check", detail: "check changed monitor ports", run: func(ctx context.Context, cfg Config) error {
			return opts.CheckPorts(ctx, cfg, changedPorts)
		}})
		if opts.Firewall != system.FirewallNone && cfg.DeployMonitor && monitorPublicPortChanged(old, cfg) {
			steps = append(steps, monitorUpdateStep{label: "Firewall", detail: "open monitor HTTPS port", run: func(_ context.Context, cfg Config) error {
				cmds := system.FirewallCommands(opts.Firewall, []system.Port{{Number: cfg.MonitorPublicPort, Proto: "tcp", Label: "monitor/Nginx"}})
				if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
					cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
				}
				return runProtocolCommands(opts.Runner, cmds...)
			}})
		}
	}
	if opts.SetLocal && monitorNginxChanged(old, cfg) {
		steps = append(steps, monitorUpdateStep{label: "Nginx", detail: "rewrite monitor reverse proxy", run: func(_ context.Context, cfg Config) error {
			if err := writeManagedNginxConfig(opts.Layout, cfg, opts.NginxConfPath); err != nil {
				return err
			}
			return runProtocolCommands(opts.Runner,
				system.Command{Name: "nginx", Args: []string{"-t"}},
				system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
			)
		}})
	}
	if opts.SetLocal {
		steps = append(steps, monitorUpdateStep{label: "Monitor service", detail: "rewrite and restart monitor", run: func(_ context.Context, cfg Config) error {
			return applyMonitorService(opts, cfg)
		}})
	}
	if opts.SetCurrentTotals {
		steps = append(steps, monitorUpdateStep{label: "Current usage", detail: "adjust current traffic totals", run: func(ctx context.Context, cfg Config) error {
			return setCurrentTrafficTotals(ctx, opts, cfg)
		}})
	}
	if opts.SetRemotes {
		steps = append(steps, monitorUpdateStep{label: "Remote monitor", detail: "refresh selected remote monitors", run: func(ctx context.Context, _ Config) error {
			return refreshRemoteMonitor(ctx, opts.Layout, remotes, opts.Fetch)
		}})
	}
	steps = append(steps, monitorUpdateStep{label: "State", detail: "persist monitor settings", run: func(_ context.Context, cfg Config) error {
		if opts.SetLocal || opts.SetCurrentTotals {
			if err := writeInstallState(opts.Layout.StateDir, cfg); err != nil {
				return err
			}
		}
		if opts.SetRemotes {
			return SaveRemoteSubscriptions(opts.Layout, remotes)
		}
		return nil
	}})
	return steps
}

func monitorChangedPortChecks(old, cfg Config) []system.Port {
	if !cfg.DeployMonitor {
		return nil
	}
	var ports []system.Port
	if !old.DeployMonitor || old.MonitorPublicPort != cfg.MonitorPublicPort {
		ports = append(ports, system.Port{Number: cfg.MonitorPublicPort, Proto: "tcp", Label: "monitor/Nginx", Public: true})
	}
	if !old.DeployMonitor || old.MonitorPort != cfg.MonitorPort {
		ports = append(ports, system.Port{Number: cfg.MonitorPort, Proto: "tcp", Label: "monitor service", Public: false})
	}
	return ports
}

func monitorPublicPortChanged(old, cfg Config) bool {
	return !old.DeployMonitor || old.MonitorPublicPort != cfg.MonitorPublicPort
}

func monitorNginxChanged(old, cfg Config) bool {
	return old.DeployMonitor != cfg.DeployMonitor || old.MonitorPublicPort != cfg.MonitorPublicPort || old.MonitorPort != cfg.MonitorPort
}

func applyMonitorService(opts MonitorUpdateOptions, cfg Config) error {
	unitPath := filepath.Join(opts.SystemdDir, system.MonitorService)
	if !cfg.DeployMonitor {
		return runProtocolCommands(opts.Runner,
			system.Command{Name: "systemctl", Args: []string{"disable", "--now", system.MonitorService}},
			system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		)
	}
	unit, err := renderMonitorUnit(opts.Layout, opts.DeployBin, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.Layout.MonitorDB), 0o755); err != nil {
		return err
	}
	if err := writeFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	return runProtocolCommands(opts.Runner,
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.MonitorService}},
		system.Command{Name: "systemctl", Args: []string{"restart", system.MonitorService}},
	)
}

func setCurrentTrafficTotals(ctx context.Context, opts MonitorUpdateOptions, cfg Config) error {
	now, err := opts.Now(ctx)
	if err != nil {
		return err
	}
	store, err := monitor.OpenStore(opts.Layout.MonitorDB)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.SetTotalsSince(monitor.CycleStart(now, cfg.ResetDay, cfg.ResetHour).Unix(), now.UTC().Unix(), monitor.TrafficTotals{
		InBytes:  opts.CurrentInBytes,
		OutBytes: opts.CurrentOutBytes,
	})
}

// CurrentTrafficTotals reads the current GMT quota-cycle usage for display in
// the monitor management UI. Missing databases are created with empty totals.
func CurrentTrafficTotals(layout paths.Layout, cfg Config, now time.Time) (monitor.TrafficTotals, error) {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	store, err := monitor.OpenStore(layout.MonitorDB)
	if err != nil {
		return monitor.TrafficTotals{}, err
	}
	defer store.Close()
	return store.TotalsSince(monitor.CycleStart(now, cfg.ResetDay, cfg.ResetHour).Unix())
}
