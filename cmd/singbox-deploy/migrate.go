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

// seedSubscriptionGroups gives an installation upgraded from the single-salt
// layout its first subscription group before anything reads the registry, so
// the status page and the subscription screens never have to render a hub that
// publishes a subscription belonging to no group.
func seedSubscriptionGroups(layout paths.Layout, expectedVersion string) error {
	_, err := (&hubctl.Controller{Layout: layout, ExpectedVersion: expectedVersion}).EnsureSubscriptionGroups()
	return err
}

// Candidate inspection and Agent export run a not-yet-installed Hub binary.
// They must remain read-only so a later update failure can still roll back the
// whole transaction without having published target-version output early.
func shouldMigrateHubSubscriptions(args []string) bool {
	if len(args) < 2 {
		return true
	}
	switch args[1] {
	// "relay" runs from a boot-time unit and must reinstall the forwarding
	// rules immediately; the subscription migration reaches the network and
	// would hold that up for as long as its timeout.
	case "--version", "agent", "relay":
		return false
	default:
		return true
	}
}
