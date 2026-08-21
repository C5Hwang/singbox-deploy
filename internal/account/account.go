package account

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// UpdateOptions describes a single-user account metadata update. Account
// management owns only display_name; protocol credentials and ports belong to
// Protocol Management.
type UpdateOptions struct {
	Layout      paths.Layout
	Runner      system.Runner
	DisplayName string
	Fetch       deploy.SubscriptionFetcher
	Progress    func(deploy.Event)
}

// Update updates the single account display name and regenerates all
// dependent config/subscription output.
func Update(ctx context.Context, opts UpdateOptions) (deploy.Config, error) {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.Fetch == nil {
		opts.Fetch = deploy.DefaultSubscriptionFetch
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
	remotes, err := deploy.LoadRemoteSubscriptions(opts.Layout)
	if err != nil {
		return deploy.Config{}, err
	}

	if err := deploy.RunSteps(ctx, opts.Progress, updateSteps(opts, cfg, remotes)); err != nil {
		return deploy.Config{}, err
	}
	return cfg, nil
}

func updateSteps(opts UpdateOptions, cfg deploy.Config, remotes []deploy.RemoteSubscription) []deploy.Step {
	return []deploy.Step{
		{Label: "Config", Detail: "render candidate config.json", Run: func(context.Context) error {
			return deploy.WriteProtocolConfigCandidate(opts.Layout, cfg)
		}},
		{Label: "Validate", Detail: "validate candidate config with sing-box", Run: func(context.Context) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", deploy.ProtocolConfigCandidate(opts.Layout)}})
		}},
		{Label: "Activate config", Detail: "replace config.json after validation", Run: func(context.Context) error {
			return os.Rename(deploy.ProtocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		{Label: "Subscriptions", Detail: "regenerate subscription files", Run: func(ctx context.Context) error {
			return deploy.WriteSubscriptionsWithRemotes(ctx, opts.Layout, cfg, remotes, opts.Fetch, deploy.LoadLocalSubscriptionPosition(opts.Layout))
		}},
		{Label: "State", Detail: "persist account display name", Run: func(context.Context) error {
			return deploy.WriteInstallState(opts.Layout.StateDir, cfg)
		}},
		{Label: "Restart", Detail: "restart sing-box.service", Run: func(context.Context) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	}
}
