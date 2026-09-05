package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type hubGrant struct {
	resetDay, resetHour int
	delta               monitor.TrafficPackage
}

type spokeGrant struct {
	nodeID string
	grant  nodeapi.TrafficPackageGrant
}

// testPackageNodes is the reset fixture with limits: Tokyo can take an
// inbound or total package, London only a total one, Pending is not
// installed and so is not offered.
func testPackageNodes() []nodes.Node {
	list := testSpokeTrafficUsageNodes()
	list[0].TrafficInLimitBytes, list[0].TrafficTotalLimitBytes = 200<<30, 1<<40
	list[1].TrafficTotalLimitBytes = 500 << 30
	return list
}

func withMonitorPackageUIDeps(t *testing.T) (paths.Layout, *[]hubGrant, *[]spokeGrant, *int) {
	t.Helper()
	layout := protocolManagerState(t, "hysteria2", "")
	writeStatusState(t, layout.StateDir, "monitor", "yes")
	writeStatusState(t, layout.StateDir, "traffic_total_limit_bytes", "1099511627776")
	writeStatusState(t, layout.StateDir, "reset_day", "7")
	writeStatusState(t, layout.StateDir, "reset_hour", "4")
	if err := nodes.Save(layout, testPackageNodes()); err != nil {
		t.Fatal(err)
	}
	oldLayout, oldDetect, oldService := monitorUILayout, detectMonitorHost, monitorServiceSnapshot
	oldHub, oldSpoke, oldCycle := grantHubTrafficPackage, grantSpokeTrafficPackage, readSpokeTrafficCycle
	oldRefresh, oldNow := refreshSpokeMonitorSnapshot, packageGrantNow
	t.Cleanup(func() {
		monitorUILayout, detectMonitorHost, monitorServiceSnapshot = oldLayout, oldDetect, oldService
		grantHubTrafficPackage, grantSpokeTrafficPackage, readSpokeTrafficCycle = oldHub, oldSpoke, oldCycle
		refreshSpokeMonitorSnapshot, packageGrantNow = oldRefresh, oldNow
	})
	monitorUILayout = func() paths.Layout { return layout }
	detectMonitorHost = func() (system.Host, error) { return supportedTestHost(), nil }
	monitorServiceSnapshot = func() string { return "running" }
	packageGrantNow = func(context.Context, *logWriter) time.Time {
		return time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	}

	hub := &[]hubGrant{}
	spoke := &[]spokeGrant{}
	refreshes := new(int)
	grantHubTrafficPackage = func(_ paths.Layout, resetDay, resetHour int, _ time.Time, delta monitor.TrafficPackage) (monitor.TrafficPackage, error) {
		*hub = append(*hub, hubGrant{resetDay: resetDay, resetHour: resetHour, delta: delta})
		return delta.Add(monitor.TrafficPackage{TotalBytes: 1 << 30}), nil
	}
	readSpokeTrafficCycle = func(_ context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
		return nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: 1_782_864_000}, nil
	}
	grantSpokeTrafficPackage = func(_ context.Context, node nodes.Node, grant nodeapi.TrafficPackageGrant) (nodeapi.TrafficUsageUpdate, error) {
		*spoke = append(*spoke, spokeGrant{nodeID: node.ID, grant: grant})
		return nodeapi.TrafficUsageUpdate{
			Previous: nodeapi.TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: grant.ExpectedCycleStart},
			Applied: nodeapi.TrafficUsage{
				InBytes: 1, OutBytes: 2, CycleStart: grant.ExpectedCycleStart, Package: grant.Package(),
			},
		}, nil
	}
	refreshSpokeMonitorSnapshot = func(context.Context) error {
		*refreshes++
		return nil
	}
	return layout, hub, spoke, refreshes
}

// openPackagePicker chooses the grant action and stops on its node picker.
func openPackagePicker(t *testing.T, tm *monitorManager) {
	t.Helper()
	setMonitorActionForTest(t, tm, monitorActionAddPackage)
	if cmd := tm.activateAction(); cmd != nil {
		t.Fatal("the package picker unexpectedly returned a command")
	}
	if tm.phase != monitorPhaseForm || tm.fields[tm.fieldIx].key != packageTargetKey {
		t.Fatalf("picker phase=%d field=%q err=%q", tm.phase, tm.fields[tm.fieldIx].key, tm.parameterForm.fieldErr)
	}
}

// tickPackageNodes ticks the given picker rows and confirms the picker.
func tickPackageNodes(t *testing.T, tm *monitorManager, options ...int) {
	t.Helper()
	for _, option := range options {
		tm.optionIx = option
		tm.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	}
	if _, done := tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); done {
		t.Fatal("choosing package targets closed the screen")
	}
}

// typeSize enters one size field and moves on, returning the validation error
// the form showed, if any.
func typeSize(t *testing.T, tm *monitorManager, key, value string) string {
	t.Helper()
	if tm.phase != monitorPhaseForm || tm.fields[tm.fieldIx].key != key {
		t.Fatalf("expected the %s field, at phase=%d field=%q", key, tm.phase, tm.fields[tm.fieldIx].key)
	}
	tm.input.SetValue(value)
	tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	return tm.parameterForm.fieldErr
}

func runMonitorPackageForTest(t *testing.T, tm *monitorManager) error {
	t.Helper()
	cmd := tm.startRun()
	if cmd == nil || tm.phase != monitorPhaseRunning {
		t.Fatalf("startRun: cmd=%v phase=%d", cmd != nil, tm.phase)
	}
	// Feed every message through the screen's own handler so the progress
	// events land where the done screen reads them.
	for msg := range tm.ch {
		tm.handleRun(msg)
		if msg.done {
			return msg.err
		}
	}
	return nil
}

func TestMonitorPackagePickerOffersTheHubAndEveryInstalledSpoke(t *testing.T) {
	withMonitorPackageUIDeps(t)
	tm := newMonitorManager()
	openPackagePicker(t, tm)
	picker := tm.fields[tm.fieldIx]
	if len(picker.options) != 3 || picker.options[0] != resetHubOption || !picker.multi {
		t.Fatalf("picker = %+v", picker.options)
	}
	for _, option := range picker.options {
		if strings.Contains(option, "Pending") {
			t.Fatalf("an uninstalled spoke was offered: %v", picker.options)
		}
	}
	if view := tm.View(); !strings.Contains(view, "lapses at the next reset") {
		t.Fatalf("picker does not say the package is temporary:\n%s", view)
	}
}

// The whole flow: tick the hub and Tokyo, add a total package, confirm, and
// each half gets its grant — the hub in its own store against its own reset
// boundary, the spoke through its Agent against the cycle the Agent reported.
func TestMonitorPackageGrantReachesTheHubStoreAndTheSpokeAgent(t *testing.T) {
	_, hub, spoke, refreshes := withMonitorPackageUIDeps(t)
	tm := newMonitorManager()
	openPackagePicker(t, tm)
	tickPackageNodes(t, tm, 0, 1)
	if err := typeSize(t, tm, uiparams.KeyPackageGrantIn, "0"); err != "" {
		t.Fatalf("inbound 0: %s", err)
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantOut, "0"); err != "" {
		t.Fatalf("outbound 0: %s", err)
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantTotal, "100GB"); err != "" {
		t.Fatalf("total 100GB: %s", err)
	}
	if tm.phase != monitorPhaseConfirm {
		t.Fatalf("phase after the form = %d (%s)", tm.phase, tm.parameterForm.fieldErr)
	}
	confirm := tm.View()
	for _, want := range []string{"total 100 GB", "Nodes:", "Hub", "Tokyo", "current cycle only", "lapses at the next reset"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, confirm)
		}
	}

	if err := runMonitorPackageForTest(t, tm); err != nil {
		t.Fatalf("grant run: %v", err)
	}
	if len(*hub) != 1 || (*hub)[0].resetDay != 7 || (*hub)[0].resetHour != 4 ||
		(*hub)[0].delta != (monitor.TrafficPackage{TotalBytes: 100 << 30}) {
		t.Fatalf("hub grants = %+v", *hub)
	}
	if len(*spoke) != 1 || (*spoke)[0].nodeID != testPackageNodes()[0].ID ||
		(*spoke)[0].grant != (nodeapi.TrafficPackageGrant{TotalBytes: 100 << 30, ExpectedCycleStart: 1_782_864_000}) {
		t.Fatalf("spoke grants = %+v", *spoke)
	}
	if *refreshes != 1 {
		t.Fatalf("snapshot refreshes = %d, want 1", *refreshes)
	}
	labels := make([]string, 0, len(tm.events))
	for _, event := range tm.events {
		if event.Status == "ok" {
			labels = append(labels, event.Label+":"+event.Detail)
		}
	}
	if strings.Join(labels, ",") != "Traffic package:Hub,Traffic package:"+spokeOptionLabel(testPackageNodes()[0])+",Monitor snapshot:refresh the hub dashboard from the spokes" {
		t.Fatalf("progress = %v", labels)
	}
	tm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	done := tm.View()
	for _, want := range []string{"Traffic package granted", "total 100 GB", "Hub: package now", "total 101 GB", "Tokyo"} {
		if !strings.Contains(done, want) {
			t.Fatalf("done screen missing %q:\n%s", want, done)
		}
	}
}

// A package on a direction that has no limit would never be spent, so the
// form refuses it and names the node — before anything is sent anywhere.
func TestMonitorPackageFormRefusesADirectionWithoutALimit(t *testing.T) {
	_, hub, spoke, _ := withMonitorPackageUIDeps(t)
	tm := newMonitorManager()
	openPackagePicker(t, tm)
	// Hub (total only) + Tokyo (in, total) + London (total only).
	tickPackageNodes(t, tm, 0, 1, 2)
	err := typeSize(t, tm, uiparams.KeyPackageGrantIn, "100GB")
	if !strings.Contains(err, "Hub has no inbound traffic limit") {
		t.Fatalf("inbound package for the unlimited hub was accepted: %q", err)
	}
	if tm.fields[tm.fieldIx].key != uiparams.KeyPackageGrantIn {
		t.Fatal("the refused field must stay open for correction")
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantIn, "0"); err != "" {
		t.Fatalf("inbound 0: %s", err)
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantOut, "1GB"); !strings.Contains(err, "no outbound traffic limit") {
		t.Fatalf("outbound package on nodes without an outbound limit was accepted: %q", err)
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantOut, "0"); err != "" {
		t.Fatalf("outbound 0: %s", err)
	}
	if err := typeSize(t, tm, uiparams.KeyPackageGrantTotal, "0"); err != "" {
		t.Fatalf("total 0: %s", err)
	}
	// An all-zero package is refused at the run, not silently granted.
	if err := runMonitorPackageForTest(t, tm); err == nil || !strings.Contains(err.Error(), "adds nothing") {
		t.Fatalf("empty package run error = %v", err)
	}
	if len(*hub) != 0 || len(*spoke) != 0 {
		t.Fatalf("an empty package was granted: hub=%+v spoke=%+v", *hub, *spoke)
	}
}

// A node that cannot be reached does not stop the rest, and the done screen
// says which one failed.
func TestMonitorPackageGrantKeepsGoingPastAFailedSpoke(t *testing.T) {
	_, hub, _, refreshes := withMonitorPackageUIDeps(t)
	grantSpokeTrafficPackage = func(_ context.Context, node nodes.Node, _ nodeapi.TrafficPackageGrant) (nodeapi.TrafficUsageUpdate, error) {
		return nodeapi.TrafficUsageUpdate{}, errors.New("tunnel down")
	}
	tm := newMonitorManager()
	openPackagePicker(t, tm)
	tickPackageNodes(t, tm, 1, 0)
	typeSize(t, tm, uiparams.KeyPackageGrantIn, "0")
	typeSize(t, tm, uiparams.KeyPackageGrantOut, "0")
	typeSize(t, tm, uiparams.KeyPackageGrantTotal, "10GB")
	err := runMonitorPackageForTest(t, tm)
	if err == nil || !strings.Contains(err.Error(), "tunnel down") || !strings.Contains(err.Error(), "Tokyo") {
		t.Fatalf("run error = %v", err)
	}
	if len(*hub) != 1 || *refreshes != 1 {
		t.Fatalf("the hub grant and the refresh must still run: hub=%+v refreshes=%d", *hub, *refreshes)
	}
	if len(tm.packageGrants) != 2 || tm.packageGrants[0].err != nil || tm.packageGrants[1].err == nil {
		t.Fatalf("results = %+v", tm.packageGrants)
	}
}

// The counter form shows the package the Agent reported and sends the edited
// one back beside the usage.
func TestSpokeTrafficUsageFormCarriesThePackage(t *testing.T) {
	layout, list := testSpokeTrafficUsageLayout(t)
	withSpokeTrafficUsageUIDeps(t, layout)
	refreshSpokeMonitorSnapshot = func(context.Context) error { return nil }
	cycleStart := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC).Unix()
	fetchSpokeTrafficUsage = func(_ context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
		return nodeapi.TrafficUsage{
			InBytes: 1 << 30, OutBytes: 2 << 30, CycleStart: cycleStart,
			Package: nodeapi.TrafficPackage{InBytes: 3 << 30, TotalBytes: 4 << 30},
		}, nil
	}
	tm := newMonitorManager()
	cmd := beginSpokeTrafficUsageLoad(t, tm, 1)
	tm.Update(cmd())
	if tm.phase != monitorPhaseForm {
		t.Fatalf("phase after load = %d (%s)", tm.phase, tm.parameterForm.fieldErr)
	}
	defaults := map[string]string{}
	for _, f := range tm.fields {
		defaults[f.key] = f.def
	}
	if defaults[uiparams.KeyPackageIn] != "3GB" || defaults[uiparams.KeyPackageOut] != "0" || defaults[uiparams.KeyPackageTotal] != "4GB" {
		t.Fatalf("package defaults = %v", defaults)
	}

	var gotReq nodeapi.TrafficUsageRequest
	setSpokeTrafficUsage = func(_ context.Context, node nodes.Node, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
		gotReq = req
		return nodeapi.TrafficUsageUpdate{
			Previous: nodeapi.TrafficUsage{InBytes: 1 << 30, OutBytes: 2 << 30, CycleStart: req.ExpectedCycleStart},
			Applied: nodeapi.TrafficUsage{
				InBytes: req.InBytes, OutBytes: req.OutBytes, CycleStart: req.ExpectedCycleStart, Package: *req.Package,
			},
		}, nil
	}
	tm.editNodeID = list[1].ID
	tm.values["current_in_traffic"] = "7GB"
	tm.values["current_out_traffic"] = "8GB"
	tm.values[uiparams.KeyPackageIn] = "0"
	tm.values[uiparams.KeyPackageOut] = "0"
	tm.values[uiparams.KeyPackageTotal] = "9GB"
	logCh := make(chan runMsg, 8)
	if err := tm.applySpokeTrafficUsage(context.Background(), &logWriter{ch: logCh}, func(deploy.Event) {}); err != nil {
		t.Fatalf("applySpokeTrafficUsage: %v", err)
	}
	if gotReq.Package == nil || *gotReq.Package != (nodeapi.TrafficPackage{TotalBytes: 9 << 30}) ||
		gotReq.InBytes != 7<<30 || gotReq.ExpectedCycleStart != cycleStart {
		t.Fatalf("Agent request = %+v package=%+v", gotReq, gotReq.Package)
	}
}

// The hub's counter form writes the package beside the usage.
func TestHubTrafficUsageFormSetsThePackage(t *testing.T) {
	withMonitorPackageUIDeps(t)
	tm := newMonitorManager()
	tm.action = monitorActionUsage
	tm.parameterForm = newParameterForm(nil)
	tm.values["current_in_traffic"] = "1GB"
	tm.values["current_out_traffic"] = "2GB"
	tm.values[uiparams.KeyPackageIn] = "0"
	tm.values[uiparams.KeyPackageOut] = "0"
	tm.values[uiparams.KeyPackageTotal] = "30GB"
	opts := tm.updateOptions()
	if !opts.SetCurrentTotals || !opts.SetCurrentPackage || opts.CurrentInBytes != 1<<30 ||
		opts.CurrentPackage != (monitor.TrafficPackage{TotalBytes: 30 << 30}) {
		t.Fatalf("update options = %+v", opts)
	}
	// The same limit rule guards the hub's own form.
	if err := tm.validateField(field{key: uiparams.KeyPackageIn}, "5GB", tm.values); err == nil ||
		!strings.Contains(err.Error(), "Hub has no inbound traffic limit") {
		t.Fatalf("hub inbound package validation = %v", err)
	}
	if err := tm.validateField(field{key: uiparams.KeyPackageTotal}, "5GB", tm.values); err != nil {
		t.Fatalf("hub total package validation = %v", err)
	}
}
