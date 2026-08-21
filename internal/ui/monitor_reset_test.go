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

// openResetPicker chooses the action and stops on the picker it opens.
func openResetPicker(t *testing.T, tm *monitorManager, action monitorAction) {
	t.Helper()
	setMonitorActionForTest(t, tm, action)
	if cmd := tm.activateAction(); cmd != nil {
		t.Fatal("the reset target picker unexpectedly returned a command")
	}
	if tm.phase != monitorPhaseForm || tm.fields[tm.fieldIx].key != resetTargetKey {
		t.Fatalf("picker phase=%d field=%q", tm.phase, tm.fields[tm.fieldIx].key)
	}
}

// pickResetTarget walks the menu the way an operator does: choose the action,
// tick the nodes in the picker it opens, then confirm.
func pickResetTarget(t *testing.T, tm *monitorManager, action monitorAction, options ...int) {
	t.Helper()
	openResetPicker(t, tm, action)
	for _, option := range options {
		tm.optionIx = option
		tm.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	}
	if _, done := tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); done {
		t.Fatal("choosing a reset target closed the screen")
	}
	if tm.phase != monitorPhaseConfirm {
		t.Fatalf("phase after choosing a target = %d, want the confirmation (%s)", tm.phase, tm.parameterForm.fieldErr)
	}
}

// The picker offers the hub and every installed spoke — and nothing else: a
// spoke with no Agent installed has nothing to ask, and no entry stands for the
// fleet now that the list is ticked rather than chosen from.
func TestMonitorResetPickerOffersTheHubAndEveryInstalledSpoke(t *testing.T) {
	withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	setMonitorActionForTest(t, tm, monitorActionResetClients)
	tm.activateAction()
	options := tm.fields[tm.fieldIx].options
	if len(options) != 3 || options[0] != resetHubOption {
		t.Fatalf("options = %v", options)
	}
	if !tm.fields[tm.fieldIx].multi {
		t.Fatal("the node picker must take more than one node, like the protocol picker")
	}
	for _, option := range options[1:] {
		if strings.Contains(option, "Pending") {
			t.Fatalf("options = %v, want the uninstalled spoke left out", options)
		}
	}
}

// Ticking the whole list reaches the hub's own store and every spoke's Agent,
// each under the scope of the dashboard page it belongs to.
func TestMonitorResetClientsClearsEveryNodeWhenAllAreTicked(t *testing.T) {
	layout, hub, spoke := withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	pickResetTarget(t, tm, monitorActionResetClients, 0, 1, 2)

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
	// Index 1 is the first installed spoke, after "Hub".
	pickResetTarget(t, tm, monitorActionResetLatency, 1)
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
	spokes := testSpokeTrafficUsageNodes()[:2]
	targets := expandResetTargets(strings.Join(append([]string{resetHubOption}, spokeLabels(spokes)...), ","), spokes)
	err := resetMonitorHistoryRun(
		context.Background(), monitorUILayout(), targets, monitor.ResetScopeClients,
		&logWriter{ch: make(chan runMsg, 16)}, func(deploy.Event) {},
	)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("error = %v, want the failing node named", err)
	}
	if len(*spoke) != 1 {
		t.Fatalf("spoke resets = %#v, want the reachable spoke still cleared", *spoke)
	}
}

// Ticking two nodes clears both and nothing else, so a clear no longer has to
// be repeated once per node.
func TestMonitorResetClearsEveryTickedNode(t *testing.T) {
	layout, hub, spoke := withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	// The hub and the first installed spoke, leaving the second alone.
	pickResetTarget(t, tm, monitorActionResetClients, 0, 1)

	if view := tm.View(); !strings.Contains(view, "Nodes") || !strings.Contains(view, resetHubOption) {
		t.Fatalf("confirmation does not name the nodes it clears:\n%s", view)
	}
	runMonitorResetForTest(t, tm)

	if len(*hub) != 1 || (*hub)[0].dbPath != layout.MonitorDB {
		t.Fatalf("hub resets = %#v", *hub)
	}
	if len(*spoke) != 1 || (*spoke)[0].nodeID != testSpokeTrafficUsageNodes()[0].ID {
		t.Fatalf("spoke resets = %#v, want only the ticked spoke", *spoke)
	}
}

// Enter on an untouched picker must not report a clear that ran against no
// node at all.
func TestMonitorResetRefusesAnEmptySelection(t *testing.T) {
	_, hub, spoke := withMonitorResetUIDeps(t)
	tm := newMonitorManager()
	openResetPicker(t, tm, monitorActionResetClients)
	tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if tm.phase != monitorPhaseForm {
		t.Fatalf("phase = %d, want the picker to hold with an error", tm.phase)
	}
	if !strings.Contains(tm.parameterForm.fieldErr, "at least one node") {
		t.Fatalf("fieldErr = %q", tm.parameterForm.fieldErr)
	}
	if len(*hub) != 0 || len(*spoke) != 0 {
		t.Fatalf("an empty selection cleared something: hub=%#v spoke=%#v", *hub, *spoke)
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
