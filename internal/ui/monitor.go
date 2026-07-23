package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
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
)

// monitorLogsMsg carries the result of the async journalctl read.
type monitorLogsMsg struct {
	logs string
	err  error
}

type monitorAction int

const (
	monitorActionLocal monitorAction = iota
	monitorActionUsage
	monitorActionEditSpoke
	monitorActionStart
	monitorActionStop
	monitorActionRestart
	monitorActionLogs
)

var (
	monitorUILayout        = paths.DefaultLayout
	detectMonitorHost      = system.DetectHost
	updateMonitorRun       = monitor.UpdateSettings
	monitorServiceSnapshot = func() string { return serviceState(system.MonitorService) }
	monitorLogOutput       = defaultMonitorLogOutput
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
				tm.phase = monitorPhaseConfirm
			},
			Back: func() {
				if !tm.previousField() {
					if tm.action == monitorActionEditSpoke && tm.editNodeIndex >= 0 {
						tm.editNodeIndex = -1
						tm.startForm(tm.editSpokeMonitorSelectField())
						return
					}
					tm.phase = monitorPhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
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
	return fieldsFromParameters(uiparams.MonitorLocalFields(tm.cfg, monitorDisabled))
}

func (tm *monitorManager) usageFields() []field {
	return fieldsFromParameters(uiparams.MonitorUsageFields(tm.totals.InBytes, tm.totals.OutBytes))
}

func (tm *monitorManager) editSpokeMonitorSelectField() []field {
	return []field{{
		key:     "edit_spoke_monitor_select",
		label:   "Spoke monitor settings to edit",
		options: spokeLabels(tm.nodes),
		note:    "Monitor configuration and data are exchanged only over the WireGuard overlay.",
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
		{key: "monitor", label: "Enable monitor on spoke", options: []string{"yes", "no"}, note: "Monitor data is served only through the authenticated Agent API over WireGuard."},
		{key: "monitor_alias", label: "Spoke monitor alias", skip: disabled},
		{key: "monitor_interface", label: "Monitored network interface", note: "Use auto to detect the default egress interface.", skip: disabled},
		{key: "monitor_interval_seconds", label: "Sampling interval (seconds)", skip: disabled},
		{key: "traffic_in_limit", label: "Inbound traffic limit", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: disabled},
		{key: "traffic_out_limit", label: "Outbound traffic limit", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: disabled},
		{key: "traffic_total_limit", label: "Total traffic limit", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: disabled},
		{key: "reset_day", label: "Monthly reset day (1-28)", skip: disabled},
		{key: "reset_hour", label: "Monthly reset hour GMT (0-23)", skip: disabled},
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
	return uiparams.ValidateMonitorParameterValue(f.key, val)
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
	if tm.action == monitorActionEditSpoke {
		go func() {
			err := tm.applySpokeMonitor(context.Background(), logs)
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

func (tm *monitorManager) applySpokeMonitor(ctx context.Context, logs *logWriter) error {
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
		return err
	}
	ctrl := &hubctl.Controller{Layout: layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion}
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
		rollbackRemoteErr := ctrl.Reconfigure(rollbackCtx, original, logs)
		cancel()
		if rollbackStateErr != nil || rollbackRemoteErr != nil {
			return fmt.Errorf("apply spoke monitor settings over WireGuard: %w (rollback state: %v; rollback spoke: %v)", err, rollbackStateErr, rollbackRemoteErr)
		}
		return fmt.Errorf("apply spoke monitor settings over WireGuard: %w (previous settings restored)", err)
	}
	if err := ctrl.RefreshMonitor(ctx); err != nil {
		fmt.Fprintf(logs, "warning: refresh monitor snapshot: %v\n", err)
	}
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
		return flowTitle.Render("Monitor") + "\n\n" + flowErr.Render(tm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run install first.")
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
		return flowOK.Render("Monitor settings updated") + "\n\n" + tm.doneSummary()
	case monitorPhaseServiceConfirm:
		return tm.serviceConfirmView()
	case monitorPhaseLogsLoading:
		return flowTitle.Render("Monitor · Logs") + "\n\n" + dimStyle.Render("Loading service logs…")
	case monitorPhaseLogs:
		return tm.serviceLogsView()
	default:
		return ""
	}
}

func (tm *monitorManager) actionView() string {
	rows := []summaryLine{
		summaryRow("Target", "Hub"),
		summaryRow("Monitor", yesNoString(tm.cfg.DeployMonitor)),
		summaryRow("Monitor frontend", yesNoString(tm.cfg.DeployMonitorFrontend)),
		summaryRow("Monitor alias", or(tm.cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow("Monitor UI port", strconv.Itoa(tm.cfg.MonitorPublicPort)),
		summaryRow("Monitor local port", strconv.Itoa(tm.cfg.MonitorPort)),
		summaryRow("Monitor interface", or(tm.cfg.MonitorInterface, "auto/default")),
		summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(tm.cfg), uiparams.DefaultResetHour(tm.cfg))),
		summaryRow("Current inbound", byteSize(tm.totals.InBytes)),
		summaryRow("Current outbound", byteSize(tm.totals.OutBytes)),
		summaryRow("Registered spokes", strconv.Itoa(len(tm.nodes))),
		summaryRow("Monitored spokes", strconv.Itoa(tm.monitoredSpokeCount())),
		summaryRow("Spoke transport", "WireGuard only"),
		summaryRow("Hub monitor service", or(tm.serviceState, "unknown")),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render("Monitor") + "\n\n")
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
			summaryRow("Deploy monitor", tm.values["monitor"]),
			summaryRow("Monitor frontend", tm.values["monitor_frontend"]),
			summaryRow("Monitor alias", tm.values["monitor_alias"]),
			summaryRow("Monitor UI port", tm.values["monitor_public_port"]),
			summaryRow("Monitor local port", tm.values["monitor_port"]),
			summaryRow("Monitor interface", tm.values["monitor_interface"]),
			summaryRow("Sampling interval", tm.values["monitor_interval_seconds"]+" seconds"),
			summaryRow("Inbound limit", tm.values["traffic_in_limit"]),
			summaryRow("Outbound limit", tm.values["traffic_out_limit"]),
			summaryRow("Total limit", tm.values["traffic_total_limit"]),
			summaryRow("Next reset", nextResetFromValues(tm.values["reset_day"], tm.values["reset_hour"])),
		)
	case monitorActionUsage:
		rows = append(rows,
			summaryRow("Current inbound", byteSize(tm.totals.InBytes)+" -> "+tm.values["current_in_traffic"]),
			summaryRow("Current outbound", byteSize(tm.totals.OutBytes)+" -> "+tm.values["current_out_traffic"]),
		)
	case monitorActionEditSpoke:
		if tm.editNodeIndex >= 0 && tm.editNodeIndex < len(tm.nodes) {
			node := tm.nodes[tm.editNodeIndex]
			rows = append(rows,
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Monitor enabled", tm.values["monitor"]),
				summaryRow("Monitor alias", tm.values["monitor_alias"]),
				summaryRow("Interface", tm.values["monitor_interface"]),
				summaryRow("Sampling interval", tm.values["monitor_interval_seconds"]+" seconds"),
				summaryRow("Inbound limit", tm.values["traffic_in_limit"]),
				summaryRow("Outbound limit", tm.values["traffic_out_limit"]),
				summaryRow("Total limit", tm.values["traffic_total_limit"]),
				summaryRow("Next reset", nextResetFromValues(tm.values["reset_day"], tm.values["reset_hour"])),
			)
		}
	}
	rows = append(rows, summaryBlank(), summaryText("This will update monitor state and refresh /monitor data."))
	return flowTitle.Render("Monitor · Confirm") + "\n\n" + renderSummary(rows)
}

func (tm *monitorManager) doneSummary() string {
	cfg := tm.result
	if cfg.Domain == "" {
		cfg = tm.cfg
	}
	return renderSummary([]summaryLine{
		summaryRow("Monitor", yesNoString(cfg.DeployMonitor)),
		summaryRow("Monitor frontend", yesNoString(cfg.DeployMonitorFrontend)),
		summaryRow("Monitor alias", or(cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
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
	default:
		return nil
	}
}

func (tm *monitorManager) actions() []monitorActionItem {
	return []monitorActionItem{
		{separator: true, label: "Hub"},
		{action: monitorActionLocal, label: "Edit hub monitor settings"},
		{action: monitorActionUsage, label: "Adjust hub traffic counters"},
		{separator: true, label: "Spokes (WireGuard)"},
		{action: monitorActionEditSpoke, label: "Edit spoke monitor settings"},
		{separator: true, label: "Hub service"},
		{action: monitorActionStart, label: "Start hub monitor service"},
		{action: monitorActionStop, label: "Stop hub monitor service"},
		{action: monitorActionRestart, label: "Restart hub monitor service"},
		{action: monitorActionLogs, label: "View hub monitor service logs"},
	}
}

func (tm *monitorManager) serviceConfirmView() string {
	rows := []summaryLine{
		summaryRow("Action", tm.serviceActionLabel()),
		summaryRow("Target", "Hub"),
		summaryRow("Service", or(tm.serviceState, "unknown")),
		summaryBlank(),
		summaryText("This will run systemctl " + tm.serviceSystemctlAction() + " " + system.MonitorService + "."),
	}
	return flowTitle.Render("Monitor · Confirm") + "\n\n" + renderSummary(rows)
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
		out, err := exec.Command("systemctl", action, system.MonitorService).CombinedOutput()
		if len(out) > 0 {
			ch <- runMsg{logLine: strings.TrimSpace(string(out))}
		}
		if err == nil {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Monitor service", Detail: action, Status: "done"}}
		}
		ch <- runMsg{done: true, err: err}
	}()
	return tm.waitForRun()
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
	body := flowTitle.Render("Monitor · Logs") + "\n\n"
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
				DeployMonitor:          dcfg.DeployMonitor,
				DeployMonitorFrontend:  dcfg.DeployMonitorFrontend,
				MonitorAlias:           dcfg.MonitorAlias,
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
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}
