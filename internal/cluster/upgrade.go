package cluster

import (
	"context"
	"fmt"
)

// UpgradeOutcome reports the result of pushing an upgrade to one node.
type UpgradeOutcome struct {
	Node Node
	Err  error // nil on success
}

// Succeeded reports whether the outcome represents a successful upgrade.
func (o UpgradeOutcome) Succeeded() bool { return o.Err == nil }

// BroadcastUpgrade sends the same UpgradeRequest to every selected node and
// returns one outcome per node (in registry order). The caller decides which
// nodes to target — passing the whole registry list applies the upgrade
// fleet-wide; a single node updates just that node.
//
// Failures are returned via UpgradeOutcome.Err rather than aborting the
// loop so one unreachable node does not block the rest of the fleet from
// being updated.
func BroadcastUpgrade(ctx context.Context, targets []Node, req UpgradeRequest) []UpgradeOutcome {
	out := make([]UpgradeOutcome, 0, len(targets))
	for _, node := range targets {
		client := NewAgentClient(node)
		err := client.Upgrade(ctx, req)
		if err != nil {
			err = fmt.Errorf("upgrade %s (%s): %w", node.Alias, node.WGIP, err)
		}
		out = append(out, UpgradeOutcome{Node: node, Err: err})
	}
	return out
}

// FilterReachable returns just the subset of nodes whose agent /api/status
// endpoint replies successfully. Useful for pre-flighting an upgrade so the
// TUI can warn about unreachable nodes before pushing version changes.
func FilterReachable(ctx context.Context, nodes []Node) (reachable, unreachable []Node) {
	for _, node := range nodes {
		client := NewAgentClient(node)
		if _, err := client.Status(ctx); err != nil {
			unreachable = append(unreachable, node)
			continue
		}
		reachable = append(reachable, node)
	}
	return reachable, unreachable
}
