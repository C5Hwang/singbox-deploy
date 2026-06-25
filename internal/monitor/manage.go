package monitor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// ManageEvent mirrors deploy.Event for step-based progress reporting.
type ManageEvent struct {
	Index  int
	Total  int
	Label  string
	Detail string
	Status string
	Err    error
}

// ManageConfig holds the subset of install state relevant to monitor management.
// It is a local type to avoid importing deploy (which already imports monitor).
type ManageConfig struct {
	Domain                 string
	DeployMonitor          bool
	MonitorAlias           string
	MonitorPublicPort      int
	MonitorPort            int
	MonitorInterface       string
	MonitorIntervalSeconds int
	TrafficInLimitBytes    uint64
	TrafficOutLimitBytes   uint64
	TrafficTotalLimitBytes uint64
	ResetDay               int
	ResetHour              int
	SubscribePort          int
}

// UpdateOptions describes updates to the local monitor settings and current
// cycle usage counters. Remote monitor aggregation is driven by the cluster
// registry on the master side; it is no longer a per-update concern here.
type UpdateOptions struct {
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

	Firewall      system.Firewall
	CheckPorts    func(context.Context, ManageConfig, []system.Port) error
	Now           func(context.Context) (time.Time, error)
	Progress      func(ManageEvent)
	NginxConfPath string
	SystemdDir    string
	MonitorBin    string

	// Deploy callbacks — wired by the caller to concrete deploy functions.
	LoadConfig              func(paths.Layout) (ManageConfig, error)
	WriteState              func(stateDir string, cfg ManageConfig) error
	WriteManagedNginxConfig func(layout paths.Layout, cfg ManageConfig, confPath string) error
	RenderMonitorUnit       func(layout paths.Layout, monitorBin string, cfg ManageConfig) (string, error)
	RunCommands             func(runner system.Runner, cmds ...system.Command) error
}

// UpdateSettings applies monitor settings to an existing installation.
func UpdateSettings(ctx context.Context, opts UpdateOptions) (ManageConfig, error) {
	opts = defaultUpdateOptions(opts)
	cfg, err := opts.LoadConfig(opts.Layout)
	if err != nil {
		return ManageConfig{}, err
	}
	old := cfg
	if opts.SetLocal {
		applyUpdateOptions(&cfg, opts)
	}
	if cfg.DeployMonitor && strings.TrimSpace(cfg.MonitorInterface) == "" {
		iface, err := DefaultInterface()
		if err != nil {
			return ManageConfig{}, err
		}
		cfg.MonitorInterface = iface
	}
	if err := validateManageConfig(cfg); err != nil {
		return ManageConfig{}, err
	}

	steps := manageUpdateSteps(opts, old, cfg)
	for i, s := range steps {
		emitManageProgress(opts.Progress, ManageEvent{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			emitManageProgress(opts.Progress, ManageEvent{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return ManageConfig{}, fmt.Errorf("%s: %w", s.label, err)
		}
		emitManageProgress(opts.Progress, ManageEvent{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

func emitManageProgress(progress func(ManageEvent), e ManageEvent) {
	if progress != nil {
		progress(e)
	}
}

type manageUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, ManageConfig) error
}

func defaultUpdateOptions(opts UpdateOptions) UpdateOptions {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.Now == nil {
		opts.Now = func(ctx context.Context) (time.Time, error) { return NetworkGMTNow(ctx) }
	}
	if opts.CheckPorts == nil {
		opts.CheckPorts = func(ctx context.Context, cfg ManageConfig, ports []system.Port) error {
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
	if opts.MonitorBin == "" {
		opts.MonitorBin = "/usr/bin/singbox-monitor"
	}
	return opts
}

func applyUpdateOptions(cfg *ManageConfig, opts UpdateOptions) {
	if opts.SetMonitor {
		cfg.DeployMonitor = opts.DeployMonitor
	}
	if strings.TrimSpace(opts.MonitorAlias) != "" {
		cfg.MonitorAlias = strings.TrimSpace(opts.MonitorAlias)
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

func validateManageConfig(cfg ManageConfig) error {
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

func manageUpdateSteps(opts UpdateOptions, old, cfg ManageConfig) []manageUpdateStep {
	var steps []manageUpdateStep
	changedPorts := manageChangedPortChecks(old, cfg)
	if opts.SetLocal && len(changedPorts) > 0 {
		steps = append(steps, manageUpdateStep{label: "Port check", detail: "check changed monitor ports", run: func(ctx context.Context, cfg ManageConfig) error {
			return opts.CheckPorts(ctx, cfg, changedPorts)
		}})
		if opts.Firewall != system.FirewallNone && cfg.DeployMonitor && managePublicPortChanged(old, cfg) {
			steps = append(steps, manageUpdateStep{label: "Firewall", detail: "open monitor HTTPS port", run: func(_ context.Context, cfg ManageConfig) error {
				cmds := system.FirewallCommands(opts.Firewall, []system.Port{{Number: cfg.MonitorPublicPort, Proto: "tcp", Label: "monitor/Nginx"}})
				if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
					cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
				}
				return opts.RunCommands(opts.Runner, cmds...)
			}})
		}
	}
	if opts.SetLocal && manageNginxChanged(old, cfg) {
		steps = append(steps, manageUpdateStep{label: "Nginx", detail: "rewrite monitor reverse proxy", run: func(_ context.Context, cfg ManageConfig) error {
			if err := opts.WriteManagedNginxConfig(opts.Layout, cfg, opts.NginxConfPath); err != nil {
				return err
			}
			return opts.RunCommands(opts.Runner,
				system.Command{Name: "nginx", Args: []string{"-t"}},
				system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
			)
		}})
	}
	if opts.SetLocal {
		steps = append(steps, manageUpdateStep{label: "Monitor service", detail: "rewrite and restart monitor", run: func(_ context.Context, cfg ManageConfig) error {
			return applyManageMonitorService(opts, cfg)
		}})
	}
	if opts.SetCurrentTotals {
		steps = append(steps, manageUpdateStep{label: "Current usage", detail: "adjust current traffic totals", run: func(ctx context.Context, cfg ManageConfig) error {
			return setManageCurrentTrafficTotals(ctx, opts, cfg)
		}})
	}
	steps = append(steps, manageUpdateStep{label: "State", detail: "persist monitor settings", run: func(_ context.Context, cfg ManageConfig) error {
		if opts.SetLocal || opts.SetCurrentTotals {
			if err := opts.WriteState(opts.Layout.StateDir, cfg); err != nil {
				return err
			}
		}
		return nil
	}})
	return steps
}

func manageChangedPortChecks(old, cfg ManageConfig) []system.Port {
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

func managePublicPortChanged(old, cfg ManageConfig) bool {
	return !old.DeployMonitor || old.MonitorPublicPort != cfg.MonitorPublicPort
}

func manageNginxChanged(old, cfg ManageConfig) bool {
	return old.DeployMonitor != cfg.DeployMonitor || old.MonitorPublicPort != cfg.MonitorPublicPort || old.MonitorPort != cfg.MonitorPort
}

func applyManageMonitorService(opts UpdateOptions, cfg ManageConfig) error {
	unitPath := filepath.Join(opts.SystemdDir, system.MonitorService)
	if !cfg.DeployMonitor {
		if err := opts.RunCommands(opts.Runner,
			system.Command{Name: "systemctl", Args: []string{"disable", "--now", system.MonitorService}},
		); err != nil {
			return err
		}
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := opts.RunCommands(opts.Runner,
			system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		); err != nil {
			return err
		}
		if opts.MonitorBin != "" {
			if err := os.Remove(opts.MonitorBin); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	unit, err := opts.RenderMonitorUnit(opts.Layout, opts.MonitorBin, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.Layout.MonitorDB), 0o755); err != nil {
		return err
	}
	if err := writeManageFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	return opts.RunCommands(opts.Runner,
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.MonitorService}},
		system.Command{Name: "systemctl", Args: []string{"restart", system.MonitorService}},
	)
}

func writeManageFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func setManageCurrentTrafficTotals(ctx context.Context, opts UpdateOptions, cfg ManageConfig) error {
	now, err := opts.Now(ctx)
	if err != nil {
		return err
	}
	store, err := OpenStore(opts.Layout.MonitorDB)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.SetTotalsSince(CycleStart(now, cfg.ResetDay, cfg.ResetHour).Unix(), now.UTC().Unix(), TrafficTotals{
		InBytes:  opts.CurrentInBytes,
		OutBytes: opts.CurrentOutBytes,
	})
}

// CurrentTrafficTotals reads the current GMT quota-cycle usage for display in
// the monitor management UI. Missing databases are created with empty totals.
func CurrentTrafficTotals(layout paths.Layout, resetDay, resetHour int, now time.Time) (TrafficTotals, error) {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	store, err := OpenStore(layout.MonitorDB)
	if err != nil {
		return TrafficTotals{}, err
	}
	defer store.Close()
	return store.TotalsSince(CycleStart(now, resetDay, resetHour).Unix())
}
