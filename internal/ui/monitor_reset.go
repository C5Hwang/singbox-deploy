package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// resetTargetKey is the form field the node picker writes to. The two clearing
// actions share it because they ask the same question.
const resetTargetKey = "reset_target"

// resetAllNodesOption ticks every node the hub can reach in one keystroke. It
// stays alongside the individual entries now that the picker takes several,
// because clearing the whole fleet is the common case: the dashboard
// aggregates it, so a figure it shows is only really gone once every node has
// dropped it.
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
	// probe narrows the clear to one probe series on that node, for a scope
	// that records several. It is empty for a scope that records one, which
	// clears the whole scope.
	probe string
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
		multi:   true,
		note:    note,
	}}
}

// validateResetTarget refuses an empty tick list. A multi picker starts with
// nothing chosen, so Enter on an untouched screen would otherwise report a
// clear that ran against no node at all.
func validateResetTarget(val string) error {
	if strings.TrimSpace(val) == "" {
		return errors.New("select at least one node to clear")
	}
	return nil
}

// resetTargetOptions lists the fleet as the picker offers it: everything at
// once, the hub, then each installed spoke. A spoke that is not installed has
// no Agent to ask and is left out rather than offered and refused.
func resetTargetOptions(spokes []nodes.Node) []string {
	options := make([]string, 0, len(spokes)+2)
	options = append(options, resetAllNodesOption, resetHubOption)
	return append(options, spokeLabels(spokes)...)
}

// resetTargets expands the ticked options into the nodes to clear.
func (tm *monitorManager) resetTargets() []resetTarget {
	return expandResetTargets(tm.values[resetTargetKey], tm.trafficSpokes())
}

// expandResetTargets expands the ticked options into the nodes to clear, in the
// order the picker lists them. A node named twice — by itself and by the
// fleet-wide entry — is cleared once, so ticking both does not ask the same
// Agent for the same deletion twice.
func expandResetTargets(picked string, spokes []nodes.Node) []resetTarget {
	var targets []resetTarget
	seen := map[string]bool{}
	add := func(key string, target resetTarget) {
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, target)
	}
	addHub := func() { add("hub", resetTarget{label: resetHubOption, hub: true}) }
	addSpoke := func(node nodes.Node) {
		add("spoke:"+node.ID, resetTarget{label: spokeOptionLabel(node), node: node})
	}
	for _, option := range strings.Split(picked, ",") {
		switch option = strings.TrimSpace(option); option {
		case "":
		case resetHubOption:
			addHub()
		case resetAllNodesOption:
			addHub()
			for _, node := range spokes {
				addSpoke(node)
			}
		default:
			if node, ok := spokeNodeForLabel(spokes, option); ok {
				addSpoke(node)
			}
		}
	}
	return targets
}

// resetMonitorHistoryRun clears one scope on every target, reporting each as a
// step of its own. A node that cannot be reached does not stop the rest: the
// operator asked for a fleet-wide clear, and clearing three of four nodes and
// saying which one is left is more use than clearing none.
func resetMonitorHistoryRun(
	ctx context.Context,
	layout paths.Layout,
	targets []resetTarget,
	scope monitor.ResetScope,
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
		err := clearOneTarget(ctx, layout, target, scope)
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

func clearOneTarget(ctx context.Context, layout paths.Layout, target resetTarget, scope monitor.ResetScope) error {
	if target.hub {
		return resetHubMonitorHistory(layout.MonitorDB, scope, target.probe)
	}
	return resetSpokeMonitorHistory(ctx, target.node, nodeapi.MonitorResetRequest{
		Scope:  nodeapi.MonitorResetScope(scope),
		Target: target.probe,
	})
}
