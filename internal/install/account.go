package install

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// AccountUpdateOptions describes a single-user account metadata update. Account
// management currently owns only display_name; protocol credentials and ports
// remain in Protocol Management per product choice.
type AccountUpdateOptions struct {
	Layout      paths.Layout
	Runner      system.Runner
	DisplayName string
	Fetch       SubscriptionFetcher
	Progress    func(Event)
}

// UpdateAccount updates the single account display name and regenerates all
// dependent config/subscription output.
func UpdateAccount(ctx context.Context, opts AccountUpdateOptions) (Config, error) {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.Fetch == nil {
		opts.Fetch = defaultSubscriptionFetch
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		return Config{}, fmt.Errorf("display name is required")
	}
	cfg, err := LoadProtocolConfig(opts.Layout)
	if err != nil {
		return Config{}, err
	}
	cfg.DisplayName = displayName
	remotes, err := LoadRemoteSubscriptions(opts.Layout)
	if err != nil {
		return Config{}, err
	}

	steps := accountUpdateSteps(opts, remotes)
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

type accountUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, Config) error
}

func accountUpdateSteps(opts AccountUpdateOptions, remotes []RemoteSubscription) []accountUpdateStep {
	return []accountUpdateStep{
		{label: "Config", detail: "render candidate config.json", run: func(_ context.Context, cfg Config) error {
			return writeProtocolConfigCandidate(opts.Layout, cfg)
		}},
		{label: "Validate", detail: "validate candidate config with sing-box", run: func(_ context.Context, _ Config) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", protocolConfigCandidate(opts.Layout)}})
		}},
		{label: "Activate config", detail: "replace config.json after validation", run: func(_ context.Context, _ Config) error {
			return os.Rename(protocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		{label: "Subscriptions", detail: "regenerate subscription files", run: func(ctx context.Context, cfg Config) error {
			return writeSubscriptionsWithRemotes(ctx, opts.Layout, cfg, remotes, opts.Fetch)
		}},
		{label: "State", detail: "persist account display name", run: func(_ context.Context, cfg Config) error {
			return writeInstallState(opts.Layout.StateDir, cfg)
		}},
		{label: "Restart", detail: "restart sing-box.service", run: func(_ context.Context, _ Config) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	}
}
