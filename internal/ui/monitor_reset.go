package ui

import (
	"context"
	"errors"
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
)

// resetTargetKey is the form field the node picker writes to. The two clearing
// actions share it because they ask the same question.
const resetTargetKey = "reset_target"

// resetAllNodesOption clears every node the hub can reach in one pass. It reads
// as one entry rather than as a repeated operation because that is how an
// operator thinks about it: the dashboard aggregates the fleet, so a figure it
// shows is only really gone once every node has dropped it.
const resetAllNodesOption = "All nodes"

// resetHubOption names the hub's own recorded history.
const resetHubOption = "Hub"

var (
	// resetHubMonitorHistory clears a scope from the hub's own store. The hub's
	// monitor service holds that store open, so this writes to it alongside the
	// sampler rather than through it — see monitor.ResetHistory.
	resetHubMonitorHistory = monitor.ResetHistory
	// resetSpokeMonitorHistory asks one spoke's Agent to clear the same scope
	// over the overlay.
	resetSpokeMonitorHistory = func(ctx context.Context, node nodes.Node, req nodeapi.MonitorResetRequest) error {
		ctrl := &hubctl.Controller{Layout: monitorUILayout(), ExpectedVersion: toolVersion}
		return ctrl.ResetMonitorHistory(ctx, node, req)
	}
)

// resetTarget is one node a clear runs against. A zero node is the hub, which
// is cleared in this process rather than over the overlay.
type resetTarget struct {
	label string
	node  nodes.Node
	hub   bool
}

// resetScope maps the current action onto the history it clears, and reports
// false for an action that clears nothing.
func (tm *monitorManager) resetScope() (monitor.ResetScope, bool) {
	switch tm.action {
	case monitorActionResetClients:
		return monitor.ResetScopeClients, true
	case monitorActionResetLatency:
		return monitor.ResetScopeLatency, true
	default:
		return "", false
	}
}

func (tm *monitorManager) resetTargetField() []field {
	note := "Clears the recorded per-address traffic on the chosen nodes: what they served directly and what they relayed."
	if tm.action == monitorActionResetLatency {
		note = "Clears the recorded carrier probe history on the chosen nodes. Relay link probes are cleared under Relay."
	}
	return []field{{
		key:     resetTargetKey,
		label:   "Nodes to clear",
		options: resetTargetOptions(tm.trafficSpokes()),
		note:    note,
	}}
}

// resetTargetOptions lists the fleet as the picker offers it: everything at
// once, the hub, then each installed spoke. A spoke that is not installed has
// no Agent to ask and is left out rather than offered and refused.
func resetTargetOptions(spokes []nodes.Node) []string {
	options := make([]string, 0, len(spokes)+2)
	options = append(options, resetAllNodesOption, resetHubOption)
	return append(options, spokeLabels(spokes)...)
}

// resetTargets expands the picked option into the nodes to clear.
func (tm *monitorManager) resetTargets() []resetTarget {
	return expandResetTargets(tm.values[resetTargetKey], tm.trafficSpokes())
}

func expandResetTargets(picked string, spokes []nodes.Node) []resetTarget {
	switch picked {
	case resetHubOption:
		return []resetTarget{{label: resetHubOption, hub: true}}
	case resetAllNodesOption:
		targets := make([]resetTarget, 0, len(spokes)+1)
		targets = append(targets, resetTarget{label: resetHubOption, hub: true})
		for _, node := range spokes {
			targets = append(targets, resetTarget{label: spokeOptionLabel(node), node: node})
		}
		return targets
	default:
		if node, ok := spokeNodeForLabel(spokes, picked); ok {
			return []resetTarget{{label: spokeOptionLabel(node), node: node}}
		}
		return nil
	}
}

// resetMonitorHistoryRun clears one scope on every target, reporting each as a
// step of its own. A node that cannot be reached does not stop the rest: the
// operator asked for a fleet-wide clear, and clearing three of four nodes and
// saying which one is left is more use than clearing none.
func resetMonitorHistoryRun(
	ctx context.Context,
	targets []resetTarget,
	scope monitor.ResetScope,
	probeTarget string,
	logs *logWriter,
	progress func(deploy.Event),
) error {
	if len(targets) == 0 {
		return fmt.Errorf("no node was selected to clear")
	}
	total := len(targets)
	var failures []error
	for i, target := range targets {
		event := deploy.Event{
			Index: i + 1, Total: total,
			Label: "Clear " + string(scope), Detail: target.label, Status: "running",
		}
		deploy.EmitProgress(progress, event)
		err := clearOneTarget(ctx, target, scope, probeTarget)
		if err != nil {
			event.Status = "fail"
			event.Err = err
			deploy.EmitProgress(progress, event)
			failures = append(failures, fmt.Errorf("clear %s history on %s: %w", scope, target.label, err))
			continue
		}
		event.Status = "ok"
		deploy.EmitProgress(progress, event)
		fmt.Fprintf(logs, "cleared %s history on %s\n", scope, target.label)
	}
	return errors.Join(failures...)
}

func clearOneTarget(ctx context.Context, target resetTarget, scope monitor.ResetScope, probeTarget string) error {
	if target.hub {
		return resetHubMonitorHistory(monitorUILayout().MonitorDB, scope, probeTarget)
	}
	return resetSpokeMonitorHistory(ctx, target.node, nodeapi.MonitorResetRequest{
		Scope:  nodeapi.MonitorResetScope(scope),
		Target: probeTarget,
	})
}
