package account

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// UpdateOptions describes a single-user account metadata update. Account
// management currently owns only display_name; protocol credentials and ports
// remain in Protocol Management per product choice.
type UpdateOptions struct {
	Layout      paths.Layout
	Runner      system.Runner
	DisplayName string
	Progress    func(deploy.Event)
}

// Update updates the single account display name and regenerates all
// dependent config/subscription output (including aggregated entries for
// every registered cluster node).
func Update(ctx context.Context, opts UpdateOptions) (deploy.Config, error) {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		return deploy.Config{}, fmt.Errorf("display name is required")
	}
	cfg, err := deploy.LoadProtocolConfig(opts.Layout)
	if err != nil {
		return deploy.Config{}, err
	}
	cfg.DisplayName = displayName
	registry := cluster.NewRegistry(opts.Layout)

	steps := updateSteps(opts, registry)
	for i, s := range steps {
		deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return deploy.Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

type updateStep struct {
	label  string
	detail string
	run    func(context.Context, deploy.Config) error
}

func updateSteps(opts UpdateOptions, registry cluster.Registry) []updateStep {
	return []updateStep{
		{label: "Config", detail: "render candidate config.json", run: func(_ context.Context, cfg deploy.Config) error {
			return deploy.WriteProtocolConfigCandidate(opts.Layout, cfg)
		}},
		{label: "Validate", detail: "validate candidate config with sing-box", run: func(_ context.Context, _ deploy.Config) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", deploy.ProtocolConfigCandidate(opts.Layout)}})
		}},
		{label: "Activate config", detail: "replace config.json after validation", run: func(_ context.Context, _ deploy.Config) error {
			return os.Rename(deploy.ProtocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		{label: "Subscriptions", detail: "regenerate subscription files for fleet", run: func(_ context.Context, cfg deploy.Config) error {
			return registry.WriteFleetSubscriptions(opts.Layout, cfg)
		}},
		{label: "State", detail: "persist account display name", run: func(_ context.Context, cfg deploy.Config) error {
			return deploy.WriteInstallState(opts.Layout.StateDir, cfg)
		}},
		{label: "Restart", detail: "restart sing-box.service", run: func(_ context.Context, _ deploy.Config) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	}
}
