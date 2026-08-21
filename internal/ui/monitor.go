package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type monitorPhase int

const (
	monitorPhaseAction monitorPhase = iota
	monitorPhaseForm
	monitorPhaseConfirm
	monitorPhaseRunning
	monitorPhaseDone
	monitorPhaseServiceConfirm
	monitorPhaseLogsLoading
	monitorPhaseLogs
	monitorPhaseSpokeUsageLoading
)

// monitorLogsMsg carries the result of the async journalctl read.
type monitorLogsMsg struct {
	logs string
	err  error
}

type spokeTrafficUsageMsg struct {
	loadID uint64
	nodeID string
	usage  nodeapi.TrafficUsage
	err    error
}

type monitorAction int

const (
	monitorActionLocal monitorAction = iota
	monitorActionUsage
	monitorActionEditSpoke
	monitorActionSpokeUsage
	monitorActionResetClients
	monitorActionResetLatency
	monitorActionStart
	monitorActionStop
	monitorActionRestart
	monitorActionLogs
)

var (
	monitorUILayout        = paths.DefaultLayout
	detectMonitorHost      = system.DetectHost
	updateMonitorRun       = monitor.UpdateSettings
	applySpokeMonitorRun   = (*monitorManager).applySpokeMonitor
	fetchSpokeTrafficUsage = func(ctx context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
		ctrl := &hubctl.Controller{Layout: monitorUILayout(), ExpectedVersion: toolVersion}
		return ctrl.TrafficUsage(ctx, node)
	}
	setSpokeTrafficUsage = func(ctx context.Context, node nodes.Node, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
		ctrl := &hubctl.Controller{Layout: monitorUILayout(), ExpectedVersion: toolVersion}
		return ctrl.SetTrafficUsage(ctx, node, req)
	}
	refreshSpokeMonitorSnapshot = func(ctx context.Context) error {
		ctrl := &hubctl.Controller{Layout: monitorUILayout(), ExpectedVersion: toolVersion}
		return ctrl.RefreshMonitor(ctx)
	}
	applySpokeTrafficUsageRun = (*monitorManager).applySpokeTrafficUsage
	monitorServiceSnapshot    = func() string { return serviceState(system.MonitorService) }
	monitorServiceRun         = runMonitorServiceAction
	monitorLogOutput          = defaultMonitorLogOutput
	// validateMonitorDomain is the whole certificate precondition for the monitor
	// domain: a name the hub cannot already serve is refused here and the
	// operator is handed to Certificate management, so by the time the update
	// runs its pair is on disk.
	validateMonitorDomain = func(domain string) error {
		return ensureDomainManaged(monitorUILayout(), domain)
	}
)

type monitorActionItem = actionItem[monitorAction]

type monitorManager struct {
	phase  monitorPhase
	action monitorAction

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     deploy.Config
	nodes   []nodes.Node
	totals  monitor.TrafficTotals
	loadErr error

	serviceState string
	fieldErr     string

	svcLogs serviceLogViewport

	cursor        int
	editNodeIndex int
	editNodeID    string

	// certificateDomainRequest asks the root model to suspend this screen and
	// open Certificate management for the monitor domain just entered.
	certificateDomainRequest string

	spokeUsage       nodeapi.TrafficUsage
	spokeUsageUpdate nodeapi.TrafficUsageUpdate
	haveSpokeUsage   bool
	spokeUsageStop   context.CancelFunc
	spokeUsageLoad   uint64

	parameterForm
	commandRun
	result deploy.Config
}

func newMonitorManager() *monitorManager {
	tm := &monitorManager{
		phase:         monitorPhaseAction,
		cursor:        1,
		editNodeIndex: -1,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	tm.host, tm.hostErr = detectMonitorHost()
	tm.refreshServiceState()
	layout := monitorUILayout()
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.cfg = cfg
	list, err := nodes.Load(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.nodes = list
	totals, err := monitor.CurrentTrafficTotals(layout, cfg.ResetDay, cfg.ResetHour, time.Now().UTC())
	if err == nil {
		tm.totals = totals
	}
	return tm
}

func (tm *monitorManager) setSize(width, height int) {
	tm.width = width
	tm.height = height
	tm.parameterForm.setSize(width, height)
	tm.commandRun.setSize(width, height)
}

func (tm *monitorManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		tm.setSize(msg.Width, msg.Height)
	case monitorLogsMsg:
		tm.svcLogs.set(msg.logs, msg.err)
		tm.phase = monitorPhaseLogs
		return nil, false
	case spokeTrafficUsageMsg:
		tm.handleSpokeTrafficUsage(msg)
		return nil, false
	case runMsg:
		return tm.handleRun(msg), false
	case tea.KeyMsg:
		return tm.handleKey(msg)
	case tea.MouseMsg:
		return tm.handleMouse(msg), false
	}
	if tm.phase == monitorPhaseForm && !tm.currentFieldHasOptions() {
		return tm.updateInput(msg), false
	}
	return nil, false
}

func (tm *monitorManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if tm.loadErr != nil {
		switch {
		case isSelectionCancelKey(msg), isSelectionConfirmKey(msg):
			return nil, true
		}
		return nil, false
	}
	switch tm.phase {
	case monitorPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: tm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				return tm.activateAction(), false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case monitorPhaseForm:
		var completeCmd tea.Cmd
		cmd, done, handled := tm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				if tm.action == monitorActionEditSpoke && tm.editNodeIndex < 0 {
					selectedLabel := tm.values["edit_spoke_monitor_select"]
					for i, node := range tm.nodes {
						if spokeOptionLabel(node) == selectedLabel {
							tm.editNodeIndex = i
							break
						}
					}
					tm.startEditSpokeMonitorForm()
					return
				}
				if tm.action == monitorActionSpokeUsage && tm.editNodeID == "" {
					selectedLabel := tm.values["adjust_spoke_traffic_select"]
					node, ok := spokeNodeForLabel(tm.trafficSpokes(), selectedLabel)
					if !ok {
						tm.parameterForm.fieldErr = "selected spoke no longer exists"
						return
					}
					tm.editNodeID = node.ID
					completeCmd = tm.startSpokeTrafficUsageLoad()
					return
				}
				tm.phase = monitorPhaseConfirm
			},
			Back: func() {
				if !tm.previousField() {
					if tm.action == monitorActionEditSpoke && tm.editNodeIndex >= 0 {
						tm.editNodeIndex = -1
						tm.startForm(tm.editSpokeMonitorSelectField())
						return
					}
					if tm.action == monitorActionSpokeUsage && tm.editNodeID != "" {
						tm.startSpokeTrafficSelector()
						return
					}
					tm.phase = monitorPhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if domain := certificateRedirectDomain(tm.parameterForm.validationErr); domain != "" {
			tm.certificateDomainRequest = domain
		}
		if handled {
			if completeCmd != nil {
				return completeCmd, done
			}
			return cmd, done
		}
	case monitorPhaseSpokeUsageLoading:
		switch {
		case isSelectionBackKey(msg):
			tm.startSpokeTrafficSelector()
		case msg.String() == "esc", isSelectionCancelKey(msg):
			tm.cancelSpokeUsageLoad()
			return nil, true
		}
	case monitorPhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return tm.startRun(), false
		case isSelectionBackKey(msg):
			if len(tm.fields) > 0 {
				tm.phase = monitorPhaseForm
				tm.backToLastField()
			} else {
				tm.phase = monitorPhaseAction
			}
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case monitorPhaseRunning:
		if msg.String() == "enter" && tm.runComplete {
			tm.reloadState()
			tm.phase = monitorPhaseDone
		} else {
			tm.handleScrollKey(msg.String(), tm.logViewportHeight())
		}
	case monitorPhaseDone:
		return tm.handleDoneKey(msg.String())
	case monitorPhaseServiceConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return tm.startServiceRun(), false
		case isSelectionBackKey(msg):
			tm.phase = monitorPhaseAction
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case monitorPhaseLogsLoading:
		if isSelectionCancelKey(msg) || msg.String() == "esc" {
			tm.phase = monitorPhaseAction
			return nil, false
		}
	case monitorPhaseLogs:
		switch msg.String() {
		case "r":
			return tm.loadServiceLogsCmd(), false
		case "esc", "q", "enter":
			tm.phase = monitorPhaseAction
		default:
			tm.svcLogs.handleKey(msg.String(), tm.width, tm.height)
		}
	}
	return nil, false
}

func (tm *monitorManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if tm.handleLogWheel(msg.Button, tm.phase == monitorPhaseRunning || (tm.phase == monitorPhaseDone && tm.runErr != nil)) {
		return nil
	}
	if tm.phase == monitorPhaseLogs {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			tm.svcLogs.scrollBy(3, tm.width, tm.height)
		case tea.MouseButtonWheelDown:
			tm.svcLogs.scrollBy(-3, tm.width, tm.height)
		}
	}
	return nil
}

func (tm *monitorManager) refreshServiceState() {
	tm.serviceState = monitorServiceSnapshot()
}

func (tm *monitorManager) reloadState() {
	layout := monitorUILayout()
	if cfg, err := deploy.LoadProtocolConfig(layout); err == nil {
		tm.cfg = cfg
		tm.result = cfg
	}
	if list, err := nodes.Load(layout); err == nil {
		tm.nodes = list
	}
	if totals, err := monitor.CurrentTrafficTotals(layout, tm.cfg.ResetDay, tm.cfg.ResetHour, time.Now().UTC()); err == nil {
		tm.totals = totals
	}
	tm.refreshServiceState()
}

func (tm *monitorManager) moveAction(delta int) {
	tm.cursor = moveActionCursor(tm.cursor, tm.actions(), delta)
	tm.fieldErr = ""
}

func (tm *monitorManager) activateAction() tea.Cmd {
	tm.fieldErr = ""
	tm.editNodeIndex = -1
	tm.editNodeID = ""
	tm.cancelSpokeUsageLoad()
	tm.haveSpokeUsage = false
	tm.spokeUsage = nodeapi.TrafficUsage{}
	tm.spokeUsageUpdate = nodeapi.TrafficUsageUpdate{}
	actions := tm.actions()
	idx, ok := selectedIndex(tm.cursor, len(actions))
	if !ok {
		return nil
	}
	tm.action = actions[idx].action
	switch tm.action {
	case monitorActionLocal:
		tm.startForm(tm.localFields())
	case monitorActionUsage:
		tm.startForm(tm.usageFields())
	case monitorActionEditSpoke:
		if len(tm.nodes) == 0 {
			tm.fieldErr = "no spoke nodes are registered; add one under Spoke → Spoke nodes"
			return nil
		}
		tm.startForm(tm.editSpokeMonitorSelectField())
	case monitorActionSpokeUsage:
		if len(tm.trafficSpokes()) == 0 {
			tm.fieldErr = "no installed spoke nodes are registered; add and install one under Spoke → Spoke nodes"
			return nil
		}
		if !tm.canApply() {
			tm.fieldErr = tm.applyBlocker()
			return nil
		}
		tm.startSpokeTrafficSelector()
	case monitorActionResetClients, monitorActionResetLatency:
		if !tm.canApply() {
			tm.fieldErr = tm.applyBlocker()
			return nil
		}
		tm.startForm(tm.resetTargetField())
	case monitorActionLogs:
		return tm.loadServiceLogsCmd()
	case monitorActionStart, monitorActionStop, monitorActionRestart:
		if !tm.canApply() {
			tm.fieldErr = tm.applyBlocker()
			return nil
		}
		tm.phase = monitorPhaseServiceConfirm
	}
	return nil
}

func (tm *monitorManager) startForm(fields []field) {
	tm.phase = monitorPhaseForm
	if tm.parameterForm.begin(fields, nil, validateMonitorField) {
		tm.phase = monitorPhaseConfirm
	}
}

func (tm *monitorManager) localFields() []field {
	monitorDisabled := func(v map[string]string) bool { return !monitorEnabled(v) }
	return withCoveredZones(monitorUILayout(), fieldsFromParameters(uiparams.MonitorLocalFields(tm.cfg, monitorDisabled)))
}

func (tm *monitorManager) usageFields() []field {
	return fieldsFromParameters(uiparams.MonitorUsageFields(tm.totals.InBytes, tm.totals.OutBytes))
}

func (tm *monitorManager) trafficSpokes() []nodes.Node {
	out := make([]nodes.Node, 0, len(tm.nodes))
	for _, node := range tm.nodes {
		if node.Installed {
			out = append(out, node)
		}
	}
	return out
}

func (tm *monitorManager) spokeTrafficNode() (nodes.Node, bool) {
	for _, node := range tm.nodes {
		if node.ID == tm.editNodeID && node.Installed {
			return node, true
		}
	}
	return nodes.Node{}, false
}

func (tm *monitorManager) startSpokeTrafficSelector() {
	tm.cancelSpokeUsageLoad()
	tm.editNodeID = ""
	tm.haveSpokeUsage = false
	tm.spokeUsage = nodeapi.TrafficUsage{}
	tm.spokeUsageUpdate = nodeapi.TrafficUsageUpdate{}
	spokes := tm.trafficSpokes()
	if len(spokes) == 0 {
		tm.phase = monitorPhaseAction
		tm.fieldErr = "no installed spoke nodes are registered; add and install one under Spoke → Spoke nodes"
		return
	}
	tm.startForm([]field{{
		key:     "adjust_spoke_traffic_select",
		label:   "Spoke traffic counters to adjust",
		options: spokeLabels(spokes),
		note:    noteSpokeTransport,
	}})
}

func (tm *monitorManager) startSpokeTrafficUsageLoad() tea.Cmd {
	node, ok := tm.spokeTrafficNode()
	if !ok {
		tm.startSpokeTrafficSelector()
		tm.parameterForm.fieldErr = "selected spoke no longer exists"
		return nil
	}
	tm.cancelSpokeUsageLoad()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	tm.spokeUsageStop = cancel
	tm.haveSpokeUsage = false
	tm.spokeUsage = nodeapi.TrafficUsage{}
	tm.spokeUsageUpdate = nodeapi.TrafficUsageUpdate{}
	tm.phase = monitorPhaseSpokeUsageLoading
	loadID, nodeID := tm.spokeUsageLoad, node.ID
	return func() tea.Msg {
		usage, err := fetchSpokeTrafficUsage(ctx, node)
		return spokeTrafficUsageMsg{loadID: loadID, nodeID: nodeID, usage: usage, err: err}
	}
}

func (tm *monitorManager) handleSpokeTrafficUsage(msg spokeTrafficUsageMsg) {
	if tm.phase != monitorPhaseSpokeUsageLoading ||
		msg.loadID != tm.spokeUsageLoad ||
		msg.nodeID != tm.editNodeID {
		return
	}
	tm.cancelSpokeUsageLoad()
	if msg.err != nil {
		tm.startSpokeTrafficSelector()
		tm.parameterForm.fieldErr = "read current spoke traffic counters: " + msg.err.Error()
		return
	}
	if _, ok := tm.spokeTrafficNode(); !ok {
		tm.startSpokeTrafficSelector()
		tm.parameterForm.fieldErr = "selected spoke no longer exists"
		return
	}
	if msg.usage.CycleStart <= 0 {
		tm.startSpokeTrafficSelector()
		tm.parameterForm.fieldErr = "Agent returned an invalid traffic quota cycle"
		return
	}
	tm.spokeUsage = msg.usage
	tm.haveSpokeUsage = true
	tm.startForm(fieldsFromParameters(uiparams.MonitorUsageFields(msg.usage.InBytes, msg.usage.OutBytes)))
}

func (tm *monitorManager) cancelSpokeUsageLoad() {
	if tm.spokeUsageStop != nil {
		tm.spokeUsageStop()
		tm.spokeUsageStop = nil
	}
	tm.spokeUsageLoad++
}

func (tm *monitorManager) editSpokeMonitorSelectField() []field {
	return []field{{
		key:     "edit_spoke_monitor_select",
		label:   "Spoke monitor settings to edit",
		options: spokeLabels(tm.nodes),
		note:    noteSpokeTransport,
	}}
}

func (tm *monitorManager) startEditSpokeMonitorForm() {
	if tm.editNodeIndex < 0 || tm.editNodeIndex >= len(tm.nodes) {
		return
	}
	node := tm.nodes[tm.editNodeIndex]
	disabled := func(v map[string]string) bool { return !monitorEnabled(v) }
	interval := node.MonitorIntervalSeconds
	if interval <= 0 {
		interval = deploy.DefaultMonitorIntervalSeconds
	}
	resetDay := node.ResetDay
	if resetDay <= 0 {
		resetDay = deploy.DefaultResetDay
	}
	iface := node.MonitorInterface
	if iface == "" {
		iface = "auto"
	}
	fields := []field{
		{key: "monitor", label: labelSpokeMonitorEnabled, options: []string{"yes", "no"}, note: "Keep showing this node on the monitor dashboard."},
		{key: "monitor_alias", label: labelSpokeMonitorAlias, note: uiparams.NoteSpokeMonitorAlias, skip: disabled},
		{key: "monitor_interface", label: uiparams.LabelMonitorInterface, note: uiparams.NoteMonitorInterface, skip: disabled},
		{key: "monitor_interval_seconds", label: uiparams.LabelMonitorInterval, note: uiparams.NoteMonitorInterval, skip: disabled},
		{key: "traffic_in_limit", label: uiparams.LabelTrafficIn, note: uiparams.NoteTrafficIn, skip: disabled},
		{key: "traffic_out_limit", label: uiparams.LabelTrafficOut, note: uiparams.NoteTrafficOut, skip: disabled},
		{key: "traffic_total_limit", label: uiparams.LabelTrafficTotal, note: uiparams.NoteTrafficTotal, skip: disabled},
		{key: "reset_day", label: uiparams.LabelResetDay, note: uiparams.NoteResetDay, skip: disabled},
		{key: "reset_hour", label: uiparams.LabelResetHour, note: uiparams.NoteResetHour, skip: disabled},
	}
	seed := map[string]string{
		"monitor":                  yesNoString(node.Monitor),
		"monitor_alias":            or(node.MonitorAlias, node.EffectiveAlias()),
		"monitor_interface":        iface,
		"monitor_interval_seconds": strconv.Itoa(interval),
		"traffic_in_limit":         uiparams.FormatTrafficSizeInput(node.TrafficInLimitBytes),
		"traffic_out_limit":        uiparams.FormatTrafficSizeInput(node.TrafficOutLimitBytes),
		"traffic_total_limit":      uiparams.FormatTrafficSizeInput(node.TrafficTotalLimitBytes),
		"reset_day":                strconv.Itoa(resetDay),
		"reset_hour":               strconv.Itoa(node.ResetHour),
	}
	tm.phase = monitorPhaseForm
	if tm.parameterForm.begin(fields, seed, validateMonitorField) {
		tm.phase = monitorPhaseConfirm
	}
}

func validateMonitorField(f field, val string, _ map[string]string) error {
	if err := uiparams.ValidateMonitorParameterValue(f.key, val); err != nil {
		return err
	}
	if f.key == "monitor_domain" {
		// Same gate as setup: the hub can only publish the monitor under a name
		// it is able to issue a certificate for.
		return validateMonitorDomain(val)
	}
	return nil
}

func (tm *monitorManager) canApply() bool { return hostCanApply(tm.host, tm.hostErr) }

func (tm *monitorManager) applyBlocker() string {
	return hostApplyBlocker(tm.host, tm.hostErr,
		"monitor changes must be run as root",
		"SELinux is enforcing; monitor changes are blocked",
		"cannot apply monitor changes")
}

func (tm *monitorManager) startRun() tea.Cmd {
	if !tm.canApply() {
		tm.fieldErr = tm.applyBlocker()
		tm.phase = monitorPhaseAction
		return nil
	}
	tm.phase = monitorPhaseRunning
	tm.resetRun(make(chan runMsg, 64))
	ch := tm.ch
	logs := &logWriter{ch: ch}
	progress := runProgressSender(ch)
	if tm.action == monitorActionEditSpoke {
		go func() {
			err := applySpokeMonitorRun(tm, context.Background(), logs, progress)
			ch <- runMsg{done: true, err: err}
		}()
		return tm.waitForRun()
	}
	if tm.action == monitorActionSpokeUsage {
		go func() {
			err := applySpokeTrafficUsageRun(tm, context.Background(), logs, progress)
			ch <- runMsg{done: true, err: err}
		}()
		return tm.waitForRun()
	}
	if scope, ok := tm.resetScope(); ok {
		targets := tm.resetTargets()
		go func() {
			err := resetMonitorHistoryRun(context.Background(), targets, scope, "", logs, progress)
			ch <- runMsg{done: true, err: err}
		}()
		return tm.waitForRun()
	}
	opts := tm.updateOptions()
	opts.Layout = monitorUILayout()
	opts.Runner = system.NewExecRunner(logs)
	opts.Firewall = tm.host.Firewall
	opts.Progress = func(e monitor.ManageEvent) {
		de := deploy.Event{Index: e.Index, Total: e.Total, Label: e.Label, Detail: e.Detail, Status: e.Status, Err: e.Err}
		ch <- runMsg{event: &de}
	}
	go func() {
		_, err := updateMonitorRun(context.Background(), opts)
		ch <- runMsg{done: true, err: err}
	}()
	return tm.waitForRun()
}

func (tm *monitorManager) applySpokeTrafficUsage(ctx context.Context, logs *logWriter, progress func(deploy.Event)) error {
	if !tm.haveSpokeUsage || tm.spokeUsage.CycleStart <= 0 {
		return fmt.Errorf("current spoke traffic counters were not loaded")
	}
	layout := monitorUILayout()
	list, err := nodes.Load(layout)
	if err != nil {
		return fmt.Errorf("reload spoke registry: %w", err)
	}
	var selected nodes.Node
	for _, node := range list {
		if node.ID == tm.editNodeID && node.Installed {
			selected = node
			break
		}
	}
	if selected.ID == "" {
		return fmt.Errorf("selected spoke no longer exists")
	}
	inBytes, err := uiparams.ParseTrafficSize(tm.values["current_in_traffic"])
	if err != nil {
		return err
	}
	outBytes, err := uiparams.ParseTrafficSize(tm.values["current_out_traffic"])
	if err != nil {
		return err
	}
	req := nodeapi.TrafficUsageRequest{
		InBytes:            inBytes,
		OutBytes:           outBytes,
		ExpectedCycleStart: tm.spokeUsage.CycleStart,
	}
	usageEvent := deploy.Event{
		Index: 1, Total: 2, Label: "Spoke traffic counters",
		Detail: "set the current quota-cycle usage through the authenticated Agent", Status: "running",
	}
	deploy.EmitProgress(progress, usageEvent)
	update, err := setSpokeTrafficUsage(ctx, selected, req)
	if err != nil {
		usageEvent.Status = "fail"
		usageEvent.Err = err
		deploy.EmitProgress(progress, usageEvent)
		return fmt.Errorf("adjust spoke traffic counters over WireGuard: %w", err)
	}
	tm.spokeUsageUpdate = update
	applied := update.Applied
	if applied.CycleStart != req.ExpectedCycleStart ||
		applied.InBytes != req.InBytes || applied.OutBytes != req.OutBytes {
		err := fmt.Errorf(
			"Agent confirmed unexpected traffic counters (cycle=%d in=%d out=%d)",
			applied.CycleStart, applied.InBytes, applied.OutBytes,
		)
		usageEvent.Status = "fail"
		usageEvent.Err = err
		deploy.EmitProgress(progress, usageEvent)
		return err
	}
	if strings.TrimSpace(update.Warning) != "" {
		usageEvent.Status = "warn"
		usageEvent.Err = errors.New(strings.TrimSpace(update.Warning))
		fmt.Fprintf(
			logs,
			"warning: spoke traffic counters were updated, but quota reconciliation reported: %s; inspect the Agent service state before retrying\n",
			strings.TrimSpace(update.Warning),
		)
	} else {
		usageEvent.Status = "ok"
	}
	deploy.EmitProgress(progress, usageEvent)
	fmt.Fprintf(logs, "updated traffic counters on %s for quota cycle %d\n", selected.EffectiveAlias(), applied.CycleStart)

	monitorEvent := deploy.Event{
		Index: 2, Total: 2, Label: "Monitor snapshot",
		Detail: "refresh the hub dashboard from the spoke", Status: "running",
	}
	deploy.EmitProgress(progress, monitorEvent)
	if err := refreshSpokeMonitorSnapshot(ctx); err != nil {
		fmt.Fprintf(
			logs,
			"warning: spoke traffic counters were updated, but the Hub monitor snapshot could not be refreshed: %v; the periodic refresh will retry\n",
			err,
		)
		monitorEvent.Status = "warn"
		monitorEvent.Err = err
	} else {
		monitorEvent.Status = "ok"
	}
	deploy.EmitProgress(progress, monitorEvent)
	return nil
}

func (tm *monitorManager) applySpokeMonitor(ctx context.Context, logs *logWriter, progress func(deploy.Event)) error {
	if tm.editNodeIndex < 0 || tm.editNodeIndex >= len(tm.nodes) {
		return fmt.Errorf("selected spoke no longer exists")
	}
	selected := tm.nodes[tm.editNodeIndex]
	updated := selected
	updated.Monitor = monitorEnabled(tm.values)
	updated.MonitorAlias = strings.TrimSpace(tm.values["monitor_alias"])
	updated.MonitorInterface = strings.TrimSpace(tm.values["monitor_interface"])
	if updated.MonitorInterface == "auto" {
		updated.MonitorInterface = ""
	}
	updated.MonitorIntervalSeconds, _ = strconv.Atoi(strings.TrimSpace(tm.values["monitor_interval_seconds"]))
	updated.TrafficInLimitBytes, _ = uiparams.ParseTrafficSize(tm.values["traffic_in_limit"])
	updated.TrafficOutLimitBytes, _ = uiparams.ParseTrafficSize(tm.values["traffic_out_limit"])
	updated.TrafficTotalLimitBytes, _ = uiparams.ParseTrafficSize(tm.values["traffic_total_limit"])
	updated.ResetDay, _ = strconv.Atoi(strings.TrimSpace(tm.values["reset_day"]))
	updated.ResetHour, _ = strconv.Atoi(strings.TrimSpace(tm.values["reset_hour"]))

	layout := monitorUILayout()
	var original nodes.Node
	registryEvent := deploy.Event{
		Index: 1, Total: 6, Label: "Registry settings",
		Detail: "save the requested spoke monitor settings", Status: "running",
	}
	deploy.EmitProgress(progress, registryEvent)
	if err := nodes.Mutate(layout, selected.ID, func(current *nodes.Node) error {
		original = *current
		current.Monitor = updated.Monitor
		current.MonitorAlias = updated.MonitorAlias
		current.MonitorInterface = updated.MonitorInterface
		current.MonitorIntervalSeconds = updated.MonitorIntervalSeconds
		current.TrafficInLimitBytes = updated.TrafficInLimitBytes
		current.TrafficOutLimitBytes = updated.TrafficOutLimitBytes
		current.TrafficTotalLimitBytes = updated.TrafficTotalLimitBytes
		current.ResetDay = updated.ResetDay
		current.ResetHour = updated.ResetHour
		updated = *current
		return nil
	}); err != nil {
		registryEvent.Status = "fail"
		registryEvent.Err = err
		deploy.EmitProgress(progress, registryEvent)
		return err
	}
	registryEvent.Status = "ok"
	deploy.EmitProgress(progress, registryEvent)
	ctrl := &hubctl.Controller{
		Layout: layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion,
		Progress: offsetRunProgress(progress, 1, 6),
	}
	if err := ctrl.Reconfigure(ctx, updated, logs); err != nil {
		rollbackStateErr := nodes.Mutate(layout, selected.ID, func(current *nodes.Node) error {
			current.Monitor = original.Monitor
			current.MonitorAlias = original.MonitorAlias
			current.MonitorInterface = original.MonitorInterface
			current.MonitorIntervalSeconds = original.MonitorIntervalSeconds
			current.TrafficInLimitBytes = original.TrafficInLimitBytes
			current.TrafficOutLimitBytes = original.TrafficOutLimitBytes
			current.TrafficTotalLimitBytes = original.TrafficTotalLimitBytes
			current.ResetDay = original.ResetDay
			current.ResetHour = original.ResetHour
			return nil
		})
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		rollbackCtrl := *ctrl
		rollbackCtrl.Progress = nil
		rollbackRemoteErr := rollbackCtrl.Reconfigure(rollbackCtx, original, logs)
		cancel()
		if rollbackStateErr != nil || rollbackRemoteErr != nil {
			return fmt.Errorf("apply spoke monitor settings over WireGuard: %w (rollback state: %v; rollback spoke: %v)", err, rollbackStateErr, rollbackRemoteErr)
		}
		return fmt.Errorf("apply spoke monitor settings over WireGuard: %w (previous settings restored)", err)
	}
	monitorEvent := deploy.Event{
		Index: 6, Total: 6, Label: "Monitor snapshot",
		Detail: "refresh the hub dashboard from the spoke", Status: "running",
	}
	deploy.EmitProgress(progress, monitorEvent)
	if err := ctrl.RefreshMonitor(ctx); err != nil {
		fmt.Fprintf(logs, "warning: refresh monitor snapshot: %v\n", err)
		monitorEvent.Status = "warn"
		monitorEvent.Err = err
	} else {
		monitorEvent.Status = "ok"
	}
	deploy.EmitProgress(progress, monitorEvent)
	return nil
}

func (tm *monitorManager) updateOptions() monitor.UpdateOptions {
	base := monitorDeployCallbacks()
	switch tm.action {
	case monitorActionLocal:
		return tm.localUpdateOptions()
	case monitorActionUsage:
		inBytes, _ := uiparams.ParseTrafficSize(tm.values["current_in_traffic"])
		outBytes, _ := uiparams.ParseTrafficSize(tm.values["current_out_traffic"])
		opts := base
		opts.SetCurrentTotals = true
		opts.CurrentInBytes = inBytes
		opts.CurrentOutBytes = outBytes
		return opts
	default:
		return base
	}
}

func (tm *monitorManager) localUpdateOptions() monitor.UpdateOptions {
	inLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_in_limit"])
	outLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_out_limit"])
	totalLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_total_limit"])
	monitorPublicPort, _ := strconv.Atoi(strings.TrimSpace(tm.values["monitor_public_port"]))
	monitorPort, _ := strconv.Atoi(strings.TrimSpace(tm.values["monitor_port"]))
	interval, _ := strconv.Atoi(strings.TrimSpace(tm.values["monitor_interval_seconds"]))
	resetDay, _ := strconv.Atoi(strings.TrimSpace(tm.values["reset_day"]))
	resetHour, _ := strconv.Atoi(strings.TrimSpace(tm.values["reset_hour"]))
	opts := monitorDeployCallbacks()
	opts.SetLocal = true
	opts.SetMonitor = true
	opts.DeployMonitor = monitorEnabled(tm.values)
	opts.DeployMonitorFrontend = monitorFrontendEnabled(tm.values)
	opts.MonitorAlias = strings.TrimSpace(tm.values["monitor_alias"])
	opts.MonitorToken = uiparams.MonitorTokenValue(tm.values["monitor_token"])
	opts.MonitorDomain = strings.TrimSpace(tm.values["monitor_domain"])
	opts.MonitorPublicPort = monitorPublicPort
	opts.MonitorPort = monitorPort
	opts.Interface = strings.TrimSpace(tm.values["monitor_interface"])
	opts.IntervalSeconds = interval
	opts.InLimitBytes = inLimit
	opts.OutLimitBytes = outLimit
	opts.TotalLimitBytes = totalLimit
	opts.ResetDay = resetDay
	opts.ResetHour = resetHour
	return opts
}

func (tm *monitorManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(tm, msg) }

func (tm *monitorManager) runState() *commandRun { return &tm.commandRun }

func (tm *monitorManager) markRunFailed() { tm.phase = monitorPhaseDone }

func (tm *monitorManager) View() string {
	if tm.loadErr != nil {
		return flowTitle.Render(titleMonitoring) + "\n\n" + flowErr.Render(tm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run Setup first.")
	}
	switch tm.phase {
	case monitorPhaseAction:
		return tm.actionView()
	case monitorPhaseForm:
		return tm.parameterForm.View("Monitor · Parameters")
	case monitorPhaseConfirm:
		return tm.confirmView()
	case monitorPhaseRunning:
		return commandRunningView(tm, "Monitor · Running")
	case monitorPhaseDone:
		if tm.runErr != nil {
			return commandFailedView(tm, "Monitor update failed")
		}
		title := "Monitor settings updated"
		if tm.action == monitorActionSpokeUsage {
			title = "Spoke traffic counters updated"
		}
		return flowOK.Render(title) + "\n\n" + tm.doneSummary()
	case monitorPhaseServiceConfirm:
		return tm.serviceConfirmView()
	case monitorPhaseLogsLoading:
		return flowTitle.Render(titleMonitoring+" · Logs") + "\n\n" + dimStyle.Render("Loading service logs…")
	case monitorPhaseLogs:
		return tm.serviceLogsView()
	case monitorPhaseSpokeUsageLoading:
		return tm.loadingSpokeTrafficUsageView()
	default:
		return ""
	}
}

func (tm *monitorManager) loadingSpokeTrafficUsageView() string {
	title := "Monitor · Spoke · Loading traffic counters"
	if node, ok := tm.spokeTrafficNode(); ok {
		title += " · " + node.EffectiveAlias()
	}
	return flowTitle.Render(title) + "\n\n" +
		dimStyle.Render("Reading fresh quota-cycle usage from the authenticated WireGuard Agent…")
}

func (tm *monitorManager) actionView() string {
	rows := []summaryLine{
		summaryRow("Target", "Hub"),
		summaryRow(uiparams.LabelMonitorEnabled, yesNoString(tm.cfg.DeployMonitor)),
		summaryRow(uiparams.LabelMonitorWebUI, yesNoString(tm.cfg.DeployMonitorFrontend)),
		summaryRow(uiparams.LabelMonitorAlias, or(tm.cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow(uiparams.LabelMonitorToken, uiparams.MonitorTokenSummary(tm.cfg.MonitorToken)),
		summaryRow(uiparams.LabelMonitorDomain, or(tm.cfg.MonitorHost(), "unknown")),
		summaryRow(uiparams.LabelMonitorPublic, strconv.Itoa(tm.cfg.MonitorPublicPort)),
		summaryRow(uiparams.LabelMonitorPort, strconv.Itoa(tm.cfg.MonitorPort)),
		summaryRow(uiparams.LabelMonitorInterface, or(tm.cfg.MonitorInterface, "auto")),
		summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(tm.cfg), uiparams.DefaultResetHour(tm.cfg))),
		summaryRow("Current inbound", byteSize(tm.totals.InBytes)),
		summaryRow("Current outbound", byteSize(tm.totals.OutBytes)),
		summaryRow("Registered spokes", strconv.Itoa(len(tm.nodes))),
		summaryRow("Monitored spokes", strconv.Itoa(tm.monitoredSpokeCount())),
		summaryRow("Spoke transport", "WireGuard only"),
		summaryRow("Hub monitor service", or(tm.serviceState, "unknown")),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render(titleMonitoring) + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if !tm.canApply() {
		b.WriteString(flowErr.Render(tm.applyBlocker()) + "\n")
	}
	if tm.fieldErr != "" {
		b.WriteString(flowErr.Render(tm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(tm.actions(), tm.cursor))
	return b.String()
}

func (tm *monitorManager) monitoredSpokeCount() int {
	count := 0
	for _, node := range tm.nodes {
		if node.Installed && node.Monitor {
			count++
		}
	}
	return count
}

func (tm *monitorManager) confirmView() string {
	var rows []summaryLine
	switch tm.action {
	case monitorActionLocal:
		rows = append(rows,
			summaryRow(uiparams.LabelMonitorEnabled, tm.values["monitor"]),
			summaryRow(uiparams.LabelMonitorWebUI, tm.values["monitor_frontend"]),
			summaryRow(uiparams.LabelMonitorAlias, tm.values["monitor_alias"]),
			summaryRow(uiparams.LabelMonitorToken, uiparams.MonitorTokenSummary(tm.values["monitor_token"])),
			summaryRow(uiparams.LabelMonitorDomain, tm.values["monitor_domain"]),
			summaryRow(uiparams.LabelMonitorPublic, tm.values["monitor_public_port"]),
			summaryRow(uiparams.LabelMonitorPort, tm.values["monitor_port"]),
			summaryRow(uiparams.LabelMonitorInterface, tm.values["monitor_interface"]),
			summaryRow(uiparams.LabelMonitorInterval, tm.values["monitor_interval_seconds"]),
			summaryRow(uiparams.LabelTrafficIn, tm.values["traffic_in_limit"]),
			summaryRow(uiparams.LabelTrafficOut, tm.values["traffic_out_limit"]),
			summaryRow(uiparams.LabelTrafficTotal, tm.values["traffic_total_limit"]),
			summaryRow("Next reset", nextResetFromValues(tm.values["reset_day"], tm.values["reset_hour"])),
		)
	case monitorActionUsage:
		rows = append(rows,
			summaryRow("Current inbound", byteSize(tm.totals.InBytes)+" -> "+tm.values["current_in_traffic"]),
			summaryRow("Current outbound", byteSize(tm.totals.OutBytes)+" -> "+tm.values["current_out_traffic"]),
		)
	case monitorActionSpokeUsage:
		if node, ok := tm.spokeTrafficNode(); ok {
			rows = append(rows,
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Quota cycle start", formatTrafficCycleStart(tm.spokeUsage.CycleStart)),
				summaryRow("Current inbound", byteSize(tm.spokeUsage.InBytes)+" -> "+tm.values["current_in_traffic"]),
				summaryRow("Current outbound", byteSize(tm.spokeUsage.OutBytes)+" -> "+tm.values["current_out_traffic"]),
			)
		}
	case monitorActionEditSpoke:
		if tm.editNodeIndex >= 0 && tm.editNodeIndex < len(tm.nodes) {
			node := tm.nodes[tm.editNodeIndex]
			rows = append(rows,
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow(labelSpokeMonitorEnabled, tm.values["monitor"]),
				summaryRow(uiparams.LabelMonitorAlias, tm.values["monitor_alias"]),
				summaryRow(uiparams.LabelMonitorInterface, tm.values["monitor_interface"]),
				summaryRow(uiparams.LabelMonitorInterval, tm.values["monitor_interval_seconds"]),
				summaryRow(uiparams.LabelTrafficIn, tm.values["traffic_in_limit"]),
				summaryRow(uiparams.LabelTrafficOut, tm.values["traffic_out_limit"]),
				summaryRow(uiparams.LabelTrafficTotal, tm.values["traffic_total_limit"]),
				summaryRow("Next reset", nextResetFromValues(tm.values["reset_day"], tm.values["reset_hour"])),
			)
		}
	}
	if scope, ok := tm.resetScope(); ok {
		targets := tm.resetTargets()
		labels := make([]string, 0, len(targets))
		for _, target := range targets {
			labels = append(labels, target.label)
		}
		rows = append(rows,
			summaryRow("Clearing", monitorResetLabel(scope)),
			summaryRow("Nodes", strconv.Itoa(len(targets))),
		)
		for _, label := range labels {
			rows = append(rows, summaryRow("", label))
		}
	}
	rows = append(rows, summaryBlank())
	switch {
	case tm.action == monitorActionSpokeUsage:
		rows = append(rows, summaryText("Replaces the selected spoke's current quota-cycle counters and refreshes /monitor data."))
	case tm.action == monitorActionResetClients:
		rows = append(rows, summaryText("Deletes the recorded per-address history. Sampling continues, so the table refills from now on. This cannot be undone."))
	case tm.action == monitorActionResetLatency:
		rows = append(rows, summaryText("Deletes the recorded carrier probe history. Probing continues, so the chart refills from now on. This cannot be undone."))
	default:
		rows = append(rows, summaryText("Updates the monitor state and refreshes /monitor data."))
	}
	return flowTitle.Render(titleMonitoring+" · Confirm") + "\n\n" + renderSummary(rows)
}

// monitorResetLabel names a scope the way the dashboard does, so the confirm
// screen and the page the operator is about to see agree on what went.
func monitorResetLabel(scope monitor.ResetScope) string {
	switch scope {
	case monitor.ResetScopeClients:
		return "Client traffic history (Top IPs)"
	case monitor.ResetScopeLatency:
		return "Carrier latency history (Latency)"
	default:
		return "Relay latency history (Relay)"
	}
}

func (tm *monitorManager) doneSummary() string {
	if scope, ok := tm.resetScope(); ok {
		targets := tm.resetTargets()
		rows := []summaryLine{
			summaryRow("Cleared", monitorResetLabel(scope)),
			summaryRow("Nodes", strconv.Itoa(len(targets))),
		}
		for _, target := range targets {
			rows = append(rows, summaryRow("", target.label))
		}
		return renderSummary(rows)
	}
	if tm.action == monitorActionSpokeUsage {
		rows := []summaryLine{
			summaryRow("Spoke traffic counters", "updated"),
			summaryRow("Current inbound", tm.values["current_in_traffic"]),
			summaryRow("Current outbound", tm.values["current_out_traffic"]),
			summaryRow("Quota cycle start", formatTrafficCycleStart(tm.spokeUsage.CycleStart)),
			summaryRow("Spoke transport", "WireGuard"),
		}
		if node, ok := tm.spokeTrafficNode(); ok {
			rows = append([]summaryLine{summaryRow("Spoke", spokeOptionLabel(node))}, rows...)
		}
		quota := "reconciled"
		snapshot := "refreshed"
		for _, event := range tm.events {
			if event.Label == "Spoke traffic counters" && event.Status == "warn" {
				quota = "warning; counters committed, inspect Agent service state before retrying"
				if warning := strings.TrimSpace(tm.spokeUsageUpdate.Warning); warning != "" {
					quota = "warning: " + warning + "; counters committed, inspect Agent service state before retrying"
				}
			}
			if event.Label == "Monitor snapshot" && event.Status == "warn" {
				snapshot = "refresh warning; periodic refresh will retry"
			}
		}
		rows = append(rows,
			summaryRow("Agent quota reconciliation", quota),
			summaryRow("Hub monitor snapshot", snapshot),
		)
		return renderSummary(rows)
	}
	cfg := tm.result
	if cfg.Domain == "" {
		cfg = tm.cfg
	}
	return renderSummary([]summaryLine{
		summaryRow("Monitor", yesNoString(cfg.DeployMonitor)),
		summaryRow("Monitor frontend", yesNoString(cfg.DeployMonitorFrontend)),
		summaryRow("Monitor alias", or(cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow(uiparams.LabelMonitorToken, uiparams.MonitorTokenSummary(cfg.MonitorToken)),
		summaryRow(uiparams.LabelMonitorDomain, or(cfg.MonitorHost(), "unknown")),
		summaryRow("Monitor UI port", strconv.Itoa(cfg.MonitorPublicPort)),
		summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(cfg), uiparams.DefaultResetHour(cfg))),
		summaryRow("Monitored spokes", strconv.Itoa(tm.monitoredSpokeCount())),
		summaryRow("Spoke transport", "WireGuard"),
	})
}

func (tm *monitorManager) footerHints() []operationHint {
	if tm.loadErr != nil {
		return returnFooterHints()
	}
	switch tm.phase {
	case monitorPhaseAction:
		return actionFooterHints("Select")
	case monitorPhaseForm:
		return tm.parameterForm.footerHints()
	case monitorPhaseConfirm:
		return applyFooterHints("Apply")
	case monitorPhaseRunning:
		return runningFooterHints(tm.runComplete)
	case monitorPhaseDone:
		return doneFooterHints(tm.runErr != nil)
	case monitorPhaseServiceConfirm:
		return applyFooterHints("Apply")
	case monitorPhaseLogsLoading:
		return nil
	case monitorPhaseLogs:
		return []operationHint{hint(keyMoveMouse, "Scroll"), hint(keyRefresh, "Refresh"), hint(keyReturn, "Return")}
	case monitorPhaseSpokeUsageLoading:
		return []operationHint{hint(keyBack, "Back"), hint(keyCancel, "Cancel")}
	default:
		return nil
	}
}

func (tm *monitorManager) actions() []monitorActionItem {
	return []monitorActionItem{
		{separator: true, label: "Hub"},
		{action: monitorActionLocal, label: "Edit hub monitor settings"},
		{action: monitorActionUsage, label: "Adjust hub traffic counters"},
		{separator: true, label: "Spokes"},
		{action: monitorActionEditSpoke, label: "Edit spoke monitor settings"},
		{action: monitorActionSpokeUsage, label: "Adjust spoke traffic counters"},
		{separator: true, label: "Recorded data"},
		{action: monitorActionResetClients, label: "Clear client traffic history"},
		{action: monitorActionResetLatency, label: "Clear carrier latency history"},
		{separator: true, label: "Service"},
		{action: monitorActionStart, label: "Start monitor service"},
		{action: monitorActionStop, label: "Stop monitor service"},
		{action: monitorActionRestart, label: "Restart monitor service"},
		{action: monitorActionLogs, label: "View monitor service logs"},
	}
}

func formatTrafficCycleStart(unix int64) string {
	if unix <= 0 {
		return "unknown"
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 GMT")
}

func (tm *monitorManager) serviceConfirmView() string {
	rows := []summaryLine{
		summaryRow("Action", tm.serviceActionLabel()),
		summaryRow("Target", "Hub"),
		summaryRow("Service", or(tm.serviceState, "unknown")),
		summaryBlank(),
		summaryText("Runs systemctl " + tm.serviceSystemctlAction() + " " + system.MonitorService + "."),
	}
	return flowTitle.Render(titleMonitoring+" · Confirm") + "\n\n" + renderSummary(rows)
}

func (tm *monitorManager) serviceActionLabel() string {
	return currentActionLabel(tm.actions(), tm.action)
}

func (tm *monitorManager) serviceSystemctlAction() string {
	switch tm.action {
	case monitorActionStart:
		return "start"
	case monitorActionStop:
		return "stop"
	case monitorActionRestart:
		return "restart"
	default:
		return ""
	}
}

func (tm *monitorManager) startServiceRun() tea.Cmd {
	if !tm.canApply() {
		tm.fieldErr = tm.applyBlocker()
		tm.phase = monitorPhaseAction
		return nil
	}
	tm.phase = monitorPhaseRunning
	tm.resetRun(make(chan runMsg, 64))
	ch := tm.ch
	action := tm.serviceSystemctlAction()
	go func() {
		ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Monitor service", Detail: action, Status: "running"}}
		logs := &logWriter{ch: ch}
		err := monitorServiceRun(monitorUILayout(), system.NewExecRunner(logs), action)
		if err == nil {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Monitor service", Detail: action, Status: "done"}}
		}
		ch <- runMsg{done: true, err: err}
	}()
	return tm.waitForRun()
}

func runMonitorServiceAction(layout paths.Layout, runner system.Runner, action string) error {
	if err := runner.Run(system.Systemctl(action, system.MonitorService)); err != nil {
		return err
	}
	if action != "stop" {
		return nil
	}
	releaseErr := monitor.ReleaseQuotaStop(layout.MonitorDB, func() error {
		return runner.Run(system.Systemctl("start", system.SingBoxService))
	})
	if releaseErr == nil {
		return nil
	}
	// A failed quota release must not strand sing-box with no owner left to
	// retry it. Restore the monitor action the operator just stopped.
	restoreErr := runner.Run(system.Systemctl("start", system.MonitorService))
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore monitor service after quota release failure: %w", restoreErr)
	}
	return errors.Join(releaseErr, restoreErr)
}

// loadServiceLogsCmd reads journalctl off the UI thread so opening the logs
// view (or refreshing it) does not freeze the TUI.
func (tm *monitorManager) loadServiceLogsCmd() tea.Cmd {
	tm.phase = monitorPhaseLogsLoading
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logs, err := monitorLogOutput(ctx, 200)
		return monitorLogsMsg{logs: logs, err: err}
	}
}

func (tm *monitorManager) serviceLogsView() string {
	body := flowTitle.Render(titleMonitoring+" · Logs") + "\n\n"
	if tm.svcLogs.logErr != nil {
		body += flowErr.Render(tm.svcLogs.logErr.Error()) + "\n\n"
	}
	if strings.TrimSpace(tm.svcLogs.logs) == "" {
		body += dimStyle.Render("no logs returned")
	} else {
		body += strings.Join(tm.svcLogs.visible(tm.width, tm.height), "\n")
	}
	return body
}

func defaultMonitorLogOutput(ctx context.Context, lines int) (string, error) {
	return journalctlOutput(ctx, system.MonitorService, lines)
}

func monitorDeployCallbacks() monitor.UpdateOptions {
	return monitor.UpdateOptions{
		LoadConfig: func(l paths.Layout) (monitor.ManageConfig, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return monitor.ManageConfig{}, err
			}
			return monitor.ManageConfig{
				Domain:                 dcfg.Domain,
				MonitorDomain:          dcfg.MonitorHost(),
				DeployMonitor:          dcfg.DeployMonitor,
				DeployMonitorFrontend:  dcfg.DeployMonitorFrontend,
				MonitorAlias:           dcfg.MonitorAlias,
				MonitorToken:           dcfg.MonitorToken,
				MonitorPublicPort:      dcfg.MonitorPublicPort,
				MonitorPort:            dcfg.MonitorPort,
				MonitorInterface:       dcfg.MonitorInterface,
				MonitorIntervalSeconds: dcfg.MonitorIntervalSeconds,
				TrafficInLimitBytes:    dcfg.TrafficInLimitBytes,
				TrafficOutLimitBytes:   dcfg.TrafficOutLimitBytes,
				TrafficTotalLimitBytes: dcfg.TrafficTotalLimitBytes,
				ResetDay:               dcfg.ResetDay,
				ResetHour:              dcfg.ResetHour,
				SubscribePort:          dcfg.SubscribePort,
			}, nil
		},
		LoadMonitorSources:     func(paths.Layout) ([]monitor.ManageMonitorSource, error) { return nil, nil },
		ValidateMonitorSources: func([]monitor.ManageMonitorSource) error { return nil },
		SaveMonitorSources:     func(l paths.Layout, _ []monitor.ManageMonitorSource) error { return deploy.SaveMonitorSources(l, nil) },
		WriteState: func(stateDir string, mcfg monitor.ManageConfig) error {
			layout := monitorUILayout()
			dcfg, err := deploy.LoadProtocolConfig(layout)
			if err != nil {
				return err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.DeployMonitorFrontend = mcfg.DeployMonitorFrontend
			dcfg.MonitorAlias = mcfg.MonitorAlias
			dcfg.MonitorToken = mcfg.MonitorToken
			dcfg.MonitorDomain = mcfg.MonitorDomain
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.MonitorInterface = mcfg.MonitorInterface
			dcfg.MonitorIntervalSeconds = mcfg.MonitorIntervalSeconds
			dcfg.TrafficInLimitBytes = mcfg.TrafficInLimitBytes
			dcfg.TrafficOutLimitBytes = mcfg.TrafficOutLimitBytes
			dcfg.TrafficTotalLimitBytes = mcfg.TrafficTotalLimitBytes
			dcfg.ResetDay = mcfg.ResetDay
			dcfg.ResetHour = mcfg.ResetHour
			return deploy.WriteInstallState(stateDir, dcfg)
		},
		WriteManagedNginxConfig: func(l paths.Layout, mcfg monitor.ManageConfig, confPath string) error {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.DeployMonitorFrontend = mcfg.DeployMonitorFrontend
			dcfg.MonitorDomain = mcfg.MonitorDomain
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.SubscribePort = mcfg.SubscribePort
			return deploy.WriteManagedNginxConfig(l, dcfg, confPath)
		},
		RenderMonitorUnit: func(l paths.Layout, deployBin string, mcfg monitor.ManageConfig) (string, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return "", err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
			dcfg.DeployMonitorFrontend = mcfg.DeployMonitorFrontend
			dcfg.MonitorAlias = mcfg.MonitorAlias
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.MonitorInterface = mcfg.MonitorInterface
			dcfg.MonitorIntervalSeconds = mcfg.MonitorIntervalSeconds
			dcfg.TrafficInLimitBytes = mcfg.TrafficInLimitBytes
			dcfg.TrafficOutLimitBytes = mcfg.TrafficOutLimitBytes
			dcfg.TrafficTotalLimitBytes = mcfg.TrafficTotalLimitBytes
			dcfg.ResetDay = mcfg.ResetDay
			dcfg.ResetHour = mcfg.ResetHour
			return deploy.RenderMonitorUnit(l, deployBin, dcfg)
		},
		RefreshRemoteMonitor: func(ctx context.Context, l paths.Layout, _ []monitor.ManageMonitorSource, _ func(context.Context, string) ([]byte, error)) error {
			return (&hubctl.Controller{Layout: l, ExpectedVersion: toolVersion}).RefreshMonitor(ctx)
		},
		RefreshSubscriptions: func(ctx context.Context, l paths.Layout) error {
			return (&hubctl.Controller{Layout: l, ExpectedVersion: toolVersion}).RefreshSubscriptions(ctx)
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}
