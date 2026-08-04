package main

import (
	"context"

	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/submigration"
)

func migrateHubSubscriptions(ctx context.Context, layout paths.Layout, expectedVersion string) (bool, error) {
	return submigration.EnsureCurrent(ctx, layout, func(ctx context.Context) error {
		return (&hubctl.Controller{Layout: layout, ExpectedVersion: expectedVersion}).RefreshSubscriptions(ctx)
	})
}

// Candidate inspection and Agent export run a not-yet-installed Hub binary.
// They must remain read-only so a later update failure can still roll back the
// whole transaction without having published target-version output early.
func shouldMigrateHubSubscriptions(args []string) bool {
	return len(args) < 2 || (args[1] != "--version" && args[1] != "agent")
}
