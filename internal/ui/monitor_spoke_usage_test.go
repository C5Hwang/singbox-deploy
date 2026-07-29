package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func testSpokeTrafficUsageNodes() []nodes.Node {
	return []nodes.Node{
		{
			ID: "11111111111111111111111111111111", Alias: "Tokyo", Domain: "jp.example.com",
			WGIP: "10.90.0.2", Token: "jp-token", Installed: true, Monitor: true,
		},
		{
			ID: "22222222222222222222222222222222", Alias: "London", Domain: "uk.example.com",
			WGIP: "10.90.0.3", Token: "uk-token", Installed: true, Monitor: true,
		},
		{
			ID: "33333333333333333333333333333333", Alias: "Pending", Domain: "pending.example.com",
			WGIP: "10.90.0.4", Token: "pending-token", Installed: false,
		},
	}
}

func testSpokeTrafficUsageLayout(t *testing.T) (paths.Layout, []nodes.Node) {
	t.Helper()
	layout := protocolManagerState(t, "hysteria2", "")
	list := testSpokeTrafficUsageNodes()
	if err := nodes.Save(layout, list); err != nil {
		t.Fatal(err)
	}
	return layout, list
}

func withSpokeTrafficUsageUIDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := monitorUILayout
	oldDetect := detectMonitorHost
	oldService := monitorServiceSnapshot
	oldFetch := fetchSpokeTrafficUsage
	oldSet := setSpokeTrafficUsage
	oldRefresh := refreshSpokeMonitorSnapshot
	oldApply := applySpokeTrafficUsageRun
	t.Cleanup(func() {
		monitorUILayout = oldLayout
		detectMonitorHost = oldDetect
		monitorServiceSnapshot = oldService
		fetchSpokeTrafficUsage = oldFetch
		setSpokeTrafficUsage = oldSet
		refreshSpokeMonitorSnapshot = oldRefresh
		applySpokeTrafficUsageRun = oldApply
	})
	monitorUILayout = func() paths.Layout { return layout }
	detectMonitorHost = func() (system.Host, error) { return supportedTestHost(), nil }
	monitorServiceSnapshot = func() string { return "running" }
}

func setMonitorActionForTest(t *testing.T, tm *monitorManager, action monitorAction) {
	t.Helper()
	for i, item := range tm.actions() {
		if !item.separator && item.action == action {
			tm.cursor = i
			return
		}
	}
	t.Fatalf("monitor action %d not found", action)
}

func beginSpokeTrafficUsageLoad(t *testing.T, tm *monitorManager, option int) tea.Cmd {
	t.Helper()
	setMonitorActionForTest(t, tm, monitorActionSpokeUsage)
	if cmd := tm.activateAction(); cmd != nil {
		t.Fatal("spoke traffic selector unexpectedly returned a command")
	}
	if tm.phase != monitorPhaseForm || tm.fields[tm.fieldIx].key != "adjust_spoke_traffic_select" {
		t.Fatalf("selector phase=%d field=%q", tm.phase, tm.fields[tm.fieldIx].key)
	}
	if len(tm.fields[tm.fieldIx].options) != 2 {
		t.Fatalf("selector options=%v; uninstalled spokes must be excluded", tm.fields[tm.fieldIx].options)
	}
	tm.optionIx = option
	cmd, done := tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || cmd == nil || tm.phase != monitorPhaseSpokeUsageLoading {
		t.Fatalf("start usage load: done=%v cmd=%v phase=%d", done, cmd != nil, tm.phase)
	}
	return cmd
}

func TestSpokeTrafficUsageLoadsFreshAgentCountersByStableNodeID(t *testing.T) {
	layout, list := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	cycleStart := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC).Unix()
	var fetchedID string
	fetchSpokeTrafficUsage = func(_ context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
		fetchedID = node.ID
		return nodeapi.TrafficUsage{
			InBytes: 3 << 30, OutBytes: 4 << 30, CycleStart: cycleStart,
		}, nil
	}

	tm := newMonitorManager()
	cmd := beginSpokeTrafficUsageLoad(t, tm, 1)
	if tm.editNodeID != list[1].ID || fetchedID != "" {
		t.Fatalf("selected ID=%q fetched before command=%q", tm.editNodeID, fetchedID)
	}
	if view := tm.View(); !strings.Contains(view, "London") || !strings.Contains(view, "Reading fresh quota-cycle usage") {
		t.Fatalf("loading view is incomplete:\n%s", view)
	}
	hints := hintText(tm.footerHints()...)
	if !strings.Contains(hints, keyBack+": Back") ||
		!strings.Contains(hints, keyCancel+": Cancel") ||
		strings.Contains(hints, "Enter") {
		t.Fatalf("loading footer advertises unsupported keys: %q", hints)
	}

	// Reordering the in-memory list after selection must not redirect the
	// response to a different spoke.
	tm.nodes[0], tm.nodes[1] = tm.nodes[1], tm.nodes[0]
	_, _ = tm.Update(cmd())
	if fetchedID != list[1].ID || tm.editNodeID != list[1].ID {
		t.Fatalf("stable selection changed: fetched=%q selected=%q", fetchedID, tm.editNodeID)
	}
	if tm.phase != monitorPhaseForm || !tm.haveSpokeUsage {
		t.Fatalf("loaded phase=%d haveUsage=%v err=%q", tm.phase, tm.haveSpokeUsage, tm.parameterForm.fieldErr)
	}
	if got := tm.fields[fieldIndex(t, tm.fields, "current_in_traffic")].def; got != "3GB" {
		t.Fatalf("inbound default=%q, want fresh Agent value", got)
	}
	if got := tm.fields[fieldIndex(t, tm.fields, "current_out_traffic")].def; got != "4GB" {
		t.Fatalf("outbound default=%q, want fresh Agent value", got)
	}

	tm.values["current_in_traffic"] = "5GB"
	tm.values["current_out_traffic"] = "6GB"
	tm.phase = monitorPhaseConfirm
	view := tm.View()
	for _, want := range []string{
		"London", "2026-07-01 00:00 GMT", "3 GB", "5GB", "4 GB", "6GB",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, view)
		}
	}
}

func TestSpokeTrafficUsageLoadCancellationIgnoresStaleResponse(t *testing.T) {
	layout, _ := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	fetchSpokeTrafficUsage = func(context.Context, nodes.Node) (nodeapi.TrafficUsage, error) {
		return nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: 100}, nil
	}

	tm := newMonitorManager()
	cmd := beginSpokeTrafficUsageLoad(t, tm, 0)
	loadID := tm.spokeUsageLoad
	_, done := tm.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if done || tm.phase != monitorPhaseForm || tm.editNodeID != "" || tm.spokeUsageStop != nil {
		t.Fatalf("back from load: done=%v phase=%d node=%q cancel=%v",
			done, tm.phase, tm.editNodeID, tm.spokeUsageStop != nil)
	}
	if tm.spokeUsageLoad == loadID {
		t.Fatal("cancelling the load did not advance its generation")
	}
	_, _ = tm.Update(cmd())
	if tm.haveSpokeUsage || tm.phase != monitorPhaseForm ||
		tm.fields[tm.fieldIx].key != "adjust_spoke_traffic_select" {
		t.Fatalf("stale response changed selector: usage=%v phase=%d field=%q",
			tm.haveSpokeUsage, tm.phase, tm.fields[tm.fieldIx].key)
	}
}

func TestSpokeTrafficUsageFreshLoadRequiresAnApplicableHost(t *testing.T) {
	layout, _ := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	tm := newMonitorManager()
	tm.host.IsRoot = false
	setMonitorActionForTest(t, tm, monitorActionSpokeUsage)
	if cmd := tm.activateAction(); cmd != nil {
		t.Fatal("blocked action unexpectedly returned a command")
	}
	if tm.phase != monitorPhaseAction || !strings.Contains(tm.fieldErr, "must be run as root") {
		t.Fatalf("non-root fresh load was not blocked: phase=%d err=%q", tm.phase, tm.fieldErr)
	}
}

func TestApplySpokeTrafficUsageUsesAgentCycleAndDoesNotPersistUsageInRegistry(t *testing.T) {
	layout, list := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	before, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	cycleStart := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC).Unix()
	var (
		gotNode nodes.Node
		gotReq  nodeapi.TrafficUsageRequest
		order   []string
		events  []deploy.Event
	)
	setSpokeTrafficUsage = func(_ context.Context, node nodes.Node, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
		gotNode, gotReq = node, req
		order = append(order, "set")
		return nodeapi.TrafficUsageUpdate{
			Previous: nodeapi.TrafficUsage{InBytes: 1 << 30, OutBytes: 2 << 30, CycleStart: req.ExpectedCycleStart},
			Applied: nodeapi.TrafficUsage{
				InBytes: req.InBytes, OutBytes: req.OutBytes, CycleStart: req.ExpectedCycleStart,
			},
		}, nil
	}
	refreshSpokeMonitorSnapshot = func(context.Context) error {
		order = append(order, "refresh")
		return nil
	}
	tm := &monitorManager{
		action:         monitorActionSpokeUsage,
		editNodeID:     list[1].ID,
		spokeUsage:     nodeapi.TrafficUsage{InBytes: 1 << 30, OutBytes: 2 << 30, CycleStart: cycleStart},
		haveSpokeUsage: true,
		parameterForm:  newParameterForm(nil),
	}
	tm.values["current_in_traffic"] = "7GB"
	tm.values["current_out_traffic"] = "8GB"
	logCh := make(chan runMsg, 8)
	err = tm.applySpokeTrafficUsage(
		context.Background(),
		&logWriter{ch: logCh},
		func(event deploy.Event) { events = append(events, event) },
	)
	if err != nil {
		t.Fatalf("applySpokeTrafficUsage: %v", err)
	}
	if gotNode.ID != list[1].ID ||
		gotReq.InBytes != 7<<30 || gotReq.OutBytes != 8<<30 ||
		gotReq.ExpectedCycleStart != cycleStart {
		t.Fatalf("Agent request: node=%q req=%+v", gotNode.ID, gotReq)
	}
	if strings.Join(order, ",") != "set,refresh" {
		t.Fatalf("operation order=%v", order)
	}
	if tm.spokeUsageUpdate.Previous.InBytes != 1<<30 ||
		tm.spokeUsageUpdate.Applied.InBytes != 7<<30 {
		t.Fatalf("saved traffic update=%+v", tm.spokeUsageUpdate)
	}
	if len(events) != 4 ||
		events[1].Label != "Spoke traffic counters" || events[1].Status != "ok" ||
		events[3].Label != "Monitor snapshot" || events[3].Status != "ok" {
		t.Fatalf("progress events=%+v", events)
	}
	after, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("traffic adjustment mutated registry:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestApplySpokeTrafficUsageReportsCommittedQuotaWarningWithoutInvitingRetry(t *testing.T) {
	layout, list := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	setSpokeTrafficUsage = func(_ context.Context, _ nodes.Node, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
		return nodeapi.TrafficUsageUpdate{
			Previous: nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: req.ExpectedCycleStart},
			Applied: nodeapi.TrafficUsage{
				InBytes: req.InBytes, OutBytes: req.OutBytes, CycleStart: req.ExpectedCycleStart,
			},
			Warning: "restart sing-box: unit unavailable",
		}, nil
	}
	refreshSpokeMonitorSnapshot = func(context.Context) error { return nil }
	tm := &monitorManager{
		action:         monitorActionSpokeUsage,
		editNodeID:     list[0].ID,
		spokeUsage:     nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: 100},
		haveSpokeUsage: true,
		parameterForm:  newParameterForm(nil),
	}
	tm.values["current_in_traffic"] = "10GB"
	tm.values["current_out_traffic"] = "11GB"
	var events []deploy.Event
	logCh := make(chan runMsg, 8)
	err := tm.applySpokeTrafficUsage(
		context.Background(),
		&logWriter{ch: logCh},
		func(event deploy.Event) { events = append(events, event) },
	)
	if err != nil {
		t.Fatalf("a committed counter update with a quota warning must not invite a blind retry: %v", err)
	}
	if len(events) != 4 || events[1].Status != "warn" ||
		events[1].Label != "Spoke traffic counters" ||
		events[1].Err == nil {
		t.Fatalf("quota warning events=%+v", events)
	}
	var logs []string
	for len(logCh) > 0 {
		msg := <-logCh
		if msg.logLine != "" {
			logs = append(logs, msg.logLine)
		}
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "counters were updated") ||
		!strings.Contains(joined, "inspect the Agent service state before retrying") {
		t.Fatalf("quota warning log is ambiguous:\n%s", joined)
	}
	tm.events = events
	if done := tm.doneSummary(); !strings.Contains(done, "restart sing-box: unit unavailable") ||
		!strings.Contains(done, "counters committed") ||
		!strings.Contains(done, "inspect Agent service state") {
		t.Fatalf("done summary hides committed quota warning:\n%s", done)
	}
}

func TestApplySpokeTrafficUsageKeepsSuccessWhenOnlySnapshotRefreshFails(t *testing.T) {
	layout, list := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	setSpokeTrafficUsage = func(_ context.Context, _ nodes.Node, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
		return nodeapi.TrafficUsageUpdate{
			Previous: nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: req.ExpectedCycleStart},
			Applied: nodeapi.TrafficUsage{
				InBytes: req.InBytes, OutBytes: req.OutBytes, CycleStart: req.ExpectedCycleStart,
			},
		}, nil
	}
	refreshSpokeMonitorSnapshot = func(context.Context) error {
		return errors.New("snapshot unavailable")
	}
	tm := &monitorManager{
		action:         monitorActionSpokeUsage,
		editNodeID:     list[0].ID,
		spokeUsage:     nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: 100},
		haveSpokeUsage: true,
		parameterForm:  newParameterForm(nil),
	}
	tm.values["current_in_traffic"] = "10GB"
	tm.values["current_out_traffic"] = "11GB"
	var events []deploy.Event
	logCh := make(chan runMsg, 8)
	err := tm.applySpokeTrafficUsage(
		context.Background(),
		&logWriter{ch: logCh},
		func(event deploy.Event) { events = append(events, event) },
	)
	if err != nil {
		t.Fatalf("a snapshot-only failure must not misreport the committed Agent update: %v", err)
	}
	if len(events) != 4 || events[3].Status != "warn" ||
		events[3].Label != "Monitor snapshot" ||
		events[3].Err == nil {
		t.Fatalf("snapshot warning events=%+v", events)
	}
	var logs []string
	for len(logCh) > 0 {
		msg := <-logCh
		if msg.logLine != "" {
			logs = append(logs, msg.logLine)
		}
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "traffic counters were updated") ||
		!strings.Contains(joined, "periodic refresh will retry") {
		t.Fatalf("warning log is ambiguous:\n%s", joined)
	}

	tm.events = events
	done := tm.doneSummary()
	if !strings.Contains(done, "refresh warning") || !strings.Contains(done, "periodic refresh will retry") {
		t.Fatalf("done summary hides snapshot warning:\n%s", done)
	}
}

func TestSpokeTrafficUsageRunForwardsProgressEvents(t *testing.T) {
	layout, _ := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	applySpokeTrafficUsageRun = func(_ *monitorManager, _ context.Context, _ *logWriter, progress func(deploy.Event)) error {
		deploy.EmitProgress(progress, deploy.Event{
			Index: 1, Total: 2, Label: "Spoke traffic counters", Status: "running",
		})
		return nil
	}
	tm := &monitorManager{
		phase:      monitorPhaseConfirm,
		action:     monitorActionSpokeUsage,
		host:       supportedTestHost(),
		commandRun: newCommandRun(),
	}
	wait := tm.startRun()
	if wait == nil {
		t.Fatal("spoke traffic usage run did not start")
	}
	msg, ok := wait().(runMsg)
	if !ok || msg.event == nil || msg.event.Label != "Spoke traffic counters" {
		t.Fatalf("first run message=%#v, want forwarded traffic event", msg)
	}
}
