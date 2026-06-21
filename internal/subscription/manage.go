package subscription

import (
	"context"
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Event mirrors deploy.Event for step-based progress reporting.
type Event struct {
	Index  int
	Total  int
	Label  string
	Detail string
	Status string
	Err    error
}

// Config holds the subset of install state relevant to subscription updates.
// The struct is filled by LoadConfig and returned by Update so the caller can
// feed it back to deploy helpers that persist state.
type Config struct {
	Domain        string
	Salt          string
	SubscribePort int
}

// UpdateOptions describes a subscription settings update. Remote/fleet
// aggregation now lives in the cluster package — every regenerate triggers
// a fleet-wide refresh implicitly via WriteSubscriptions.
type UpdateOptions struct {
	Layout paths.Layout
	Runner system.Runner

	Salt          string
	SubscribePort int

	Firewall      system.Firewall
	CheckPorts    func(ctx context.Context, domain string, port int) error
	Progress      func(Event)
	NginxConfPath string

	// Deploy callbacks — wired by the caller to concrete deploy functions.
	LoadConfig         func(paths.Layout) (Config, error)
	WriteState         func(stateDir string, cfg Config) error
	WriteNginxConfig   func(layout paths.Layout, cfg Config, confPath string) error
	WriteSubscriptions func(ctx context.Context, layout paths.Layout, cfg Config) error
	RunCommands        func(runner system.Runner, cmds ...system.Command) error
}

// Update updates local subscription settings, rewrites generated subscription
// files (including aggregated entries for every cluster node), and reloads
// Nginx when the public subscription port changes.
func Update(ctx context.Context, opts UpdateOptions) (Config, error) {
	opts = defaultOptions(opts)
	cfg, err := opts.LoadConfig(opts.Layout)
	if err != nil {
		return Config{}, err
	}
	oldPort := cfg.SubscribePort
	if strings.TrimSpace(opts.Salt) != "" {
		cfg.Salt = strings.TrimSpace(opts.Salt)
	}
	if opts.SubscribePort > 0 {
		cfg.SubscribePort = opts.SubscribePort
	}
	if cfg.SubscribePort <= 0 || cfg.SubscribePort > 65535 {
		return Config{}, fmt.Errorf("subscription port must be between 1 and 65535")
	}

	steps := updateSteps(opts, oldPort, cfg.SubscribePort)
	for i, s := range steps {
		emitProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			emitProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		emitProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

func emitProgress(progress func(Event), e Event) {
	if progress != nil {
		progress(e)
	}
}

type updateStep struct {
	label  string
	detail string
	run    func(context.Context, Config) error
}

func updateSteps(opts UpdateOptions, oldPort, newPort int) []updateStep {
	portChanged := oldPort != newPort
	var steps []updateStep
	if portChanged {
		steps = append(steps, updateStep{label: "Port check", detail: "check new subscription HTTPS port", run: func(ctx context.Context, cfg Config) error {
			return opts.CheckPorts(ctx, cfg.Domain, newPort)
		}})
		if opts.Firewall != system.FirewallNone {
			steps = append(steps, updateStep{label: "Firewall", detail: "open new subscription HTTPS port", run: func(_ context.Context, _ Config) error {
				cmds := system.FirewallCommands(opts.Firewall, []system.Port{{Number: newPort, Proto: "tcp", Label: "subscription/Nginx"}})
				if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
					cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
				}
				return opts.RunCommands(opts.Runner, cmds...)
			}})
		}
	}
	steps = append(steps,
		updateStep{label: "Subscriptions", detail: "regenerate subscription outputs for fleet", run: func(ctx context.Context, cfg Config) error {
			return opts.WriteSubscriptions(ctx, opts.Layout, cfg)
		}},
		updateStep{label: "State", detail: "persist subscription settings", run: func(_ context.Context, cfg Config) error {
			return opts.WriteState(opts.Layout.StateDir, cfg)
		}},
	)
	if portChanged {
		steps = append(steps, updateStep{label: "Nginx", detail: "rewrite managed Nginx config and restart", run: func(_ context.Context, cfg Config) error {
			if err := opts.WriteNginxConfig(opts.Layout, cfg, opts.NginxConfPath); err != nil {
				return err
			}
			return opts.RunCommands(opts.Runner,
				system.Command{Name: "nginx", Args: []string{"-t"}},
				system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
			)
		}})
	}
	return steps
}

func defaultOptions(opts UpdateOptions) UpdateOptions {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.NginxConfPath == "" {
		opts.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	return opts
}
