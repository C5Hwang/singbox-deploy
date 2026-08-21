package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// hubReset and spokeReset record what each half of a clear was asked for.
type hubReset struct {
	dbPath string
	scope  monitor.ResetScope
	target string
}

type spokeReset struct {
	nodeID  string
	request nodeapi.MonitorResetRequest
}

func withMonitorResetUIDeps(t *testing.T) (paths.Layout, *[]hubReset, *[]spokeReset) {
	t.Helper()
	layout := protocolManagerState(t, "hysteria2", "")
	if err := nodes.Save(layout, testSpokeTrafficUsageNodes()); err != nil {
		t.Fatal(err)
	}
	oldLayout, oldDetect, oldService := monitorUILayout, detectMonitorHost, monitorServiceSnapshot
	oldHub, oldSpoke := resetHubMonitorHistory, resetSpokeMonitorHistory
	t.Cleanup(func() {
		monitorUILayout, detectMonitorHost, monitorServiceSnapshot = oldLayout, oldDetect, oldService
		resetHubMonitorHistory, resetSpokeMonitorHistory = oldHub, oldSpoke
	})
	monitorUILayout = func() paths.Layout { return layout }
	detectMonitorHost = func() (system.Host, error) { return supportedTestHost(), nil }
	monitorServiceSnapshot = func() string { return "running" }

	hub := &[]hubReset{}
	spoke := &[]spokeReset{}
	resetHubMonitorHistory = func(dbPath string, scope monitor.ResetScope, target string) error {
		*hub = append(*hub, hubReset{dbPath: dbPath, scope: scope, target: target})
		return nil
	}
	resetSpokeMonitorHistory = func(_ context.Context, node nodes.Node, req nodeapi.MonitorResetRequest) error {
		*spoke = append(*spoke, spokeReset{nodeID: node.ID, request: req})
		return nil
	}
	return layout, hub, spoke
}

// pickResetTarget walks the menu the way an operator does: choose the action,
// then choose the node from the picker it opens.
func pickResetTarget(t *testing.T, tm *monitorManager, action monitorAction, option int) {
	t.Helper()
	setMonitorActionForTest(t, tm, action)
	if cmd := tm.activateAction(); cmd != nil {
		t.Fatal("the reset target picker unexpectedly returned a command")
	}
	if tm.phase != monitorPhaseForm || tm.fields[tm.fieldIx].key != resetTargetKey {
		t.Fatalf("picker phase=%d field=%q", tm.phase, tm.fields[tm.fieldIx].key)
	}
	tm.optionIx = option
	if _, done := tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); done {
		t.Fatal("choosing a reset target closed the screen")
	}
	if tm.phase != monitorPhaseConfirm {
		t.Fatalf("phase after choosing a target = %d, want the confirmation", tm.phase)
	}
}

// The picker offers the whole fleet at once, the hub, and every installed
// spoke — and nothing else: a spoke with no Agent installed has nothing to ask.
func TestMonitorResetPickerOffersTheFleetAndEveryInstalledSpoke(t *testing.T) {
	withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	setMonitorActionForTest(t, tm, monitorActionResetClients)
	tm.activateAction()
	options := tm.fields[tm.fieldIx].options
	if len(options) != 4 || options[0] != resetAllNodesOption || options[1] != resetHubOption {
		t.Fatalf("options = %v", options)
	}
	for _, option := range options[2:] {
		if strings.Contains(option, "Pending") {
			t.Fatalf("options = %v, want the uninstalled spoke left out", options)
		}
	}
}

// Clearing clients on the whole fleet reaches the hub's own store and every
// spoke's Agent, each under the scope the dashboard page it belongs to.
func TestMonitorResetClientsClearsEveryNodeWhenAllIsChosen(t *testing.T) {
	layout, hub, spoke := withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	pickResetTarget(t, tm, monitorActionResetClients, 0)

	if view := tm.View(); !strings.Contains(view, "Client traffic history") || !strings.Contains(view, "cannot be undone") {
		t.Fatalf("confirmation does not say what it clears:\n%s", view)
	}
	runMonitorResetForTest(t, tm)

	if len(*hub) != 1 || (*hub)[0].dbPath != layout.MonitorDB || (*hub)[0].scope != monitor.ResetScopeClients {
		t.Fatalf("hub resets = %#v", *hub)
	}
	if len(*spoke) != 2 {
		t.Fatalf("spoke resets = %#v, want one per installed spoke", *spoke)
	}
	for _, got := range *spoke {
		if got.request.Scope != nodeapi.MonitorResetClients || got.request.Target != "" {
			t.Fatalf("spoke request = %#v", got.request)
		}
	}
}

// Choosing one spoke leaves every other node — the hub included — untouched.
func TestMonitorResetLatencyClearsOnlyTheChosenSpoke(t *testing.T) {
	_, hub, spoke := withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	// Index 2 is the first installed spoke, after "All nodes" and "Hub".
	pickResetTarget(t, tm, monitorActionResetLatency, 2)
	runMonitorResetForTest(t, tm)

	if len(*hub) != 0 {
		t.Fatalf("hub resets = %#v, want none", *hub)
	}
	if len(*spoke) != 1 || (*spoke)[0].request.Scope != nodeapi.MonitorResetLatency {
		t.Fatalf("spoke resets = %#v", *spoke)
	}
}

// One unreachable node must not swallow the rest of a fleet-wide clear, and the
// node that failed has to be named.
func TestMonitorResetReportsAFailedNodeAndClearsTheRest(t *testing.T) {
	_, _, spoke := withMonitorResetUIDeps(t)
	failing := testSpokeTrafficUsageNodes()[0].ID
	inner := resetSpokeMonitorHistory
	resetSpokeMonitorHistory = func(ctx context.Context, node nodes.Node, req nodeapi.MonitorResetRequest) error {
		if node.ID == failing {
			return errors.New("node did not answer")
		}
		return inner(ctx, node, req)
	}
	targets := expandResetTargets(resetAllNodesOption, testSpokeTrafficUsageNodes()[:2])
	err := resetMonitorHistoryRun(
		context.Background(), targets, monitor.ResetScopeClients, "",
		&logWriter{ch: make(chan runMsg, 16)}, func(deploy.Event) {},
	)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("error = %v, want the failing node named", err)
	}
	if len(*spoke) != 1 {
		t.Fatalf("spoke resets = %#v, want the reachable spoke still cleared", *spoke)
	}
}

func runMonitorResetForTest(t *testing.T, tm *monitorManager) {
	t.Helper()
	cmd := tm.startRun()
	if cmd == nil || tm.phase != monitorPhaseRunning {
		t.Fatalf("startRun: cmd=%v phase=%d", cmd != nil, tm.phase)
	}
	for msg := range tm.ch {
		if msg.done {
			if msg.err != nil {
				t.Fatalf("reset run: %v", msg.err)
			}
			return
		}
	}
}
