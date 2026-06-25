package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type monitorPhase int

const (
	monitorPhaseTarget monitorPhase = iota
	monitorPhaseAction
	monitorPhaseForm
	monitorPhaseConfirm
	monitorPhaseRunning
	monitorPhaseDone
	monitorPhaseServiceConfirm
	monitorPhaseLogs
)

type monitorAction int

const (
	monitorActionLocal monitorAction = iota
	monitorActionUsage
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
	totals  monitor.TrafficTotals
	loadErr error

	serviceState string
	fieldErr     string

	logs         string
	logErr       error
	svcLogScroll int

	picker        targetPicker
	agentOutcomes []agentOutcome

	cursor int
	parameterForm
	commandRun
	result deploy.Config
}

type agentOutcome struct {
	node cluster.Node
	err  error
}

func newMonitorManager() *monitorManager {
	tm := &monitorManager{
		phase:         monitorPhaseAction,
		cursor:        1,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	tm.host, tm.hostErr = detectMonitorHost()
	tm.refreshServiceState()
	tm.picker = newTargetPicker(monitorUILayout())
	if tm.picker.hasNodes() {
		tm.phase = monitorPhaseTarget
		tm.cursor = 0
	}
	layout := monitorUILayout()
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.cfg = cfg
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
	case monitorPhaseTarget:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: tm.moveTarget,
			Confirm: func() (tea.Cmd, bool) {
				tm.phase = monitorPhaseAction
				tm.cursor = 1
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case monitorPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: tm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				tm.activateAction()
				return nil, false
			},
			Back: func() (tea.Cmd, bool) {
				if tm.picker.hasNodes() {
					tm.phase = monitorPhaseTarget
					tm.cursor = 0
					return nil, false
				}
				return nil, true
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case monitorPhaseForm:
		cmd, done, handled := tm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				tm.phase = monitorPhaseConfirm
			},
			Back: func() {
				if !tm.previousField() {
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
		switch msg.String() {
		case "enter":
			if tm.runComplete {
				tm.reloadState()
				tm.phase = monitorPhaseDone
			}
		case "up", "k":
			tm.scrollLog(1, tm.logViewportHeight())
		case "down", "j":
			tm.scrollLog(-1, tm.logViewportHeight())
		case "pgup":
			tm.scrollLog(tm.logViewportHeight(), tm.logViewportHeight())
		case "pgdown":
			tm.scrollLog(-tm.logViewportHeight(), tm.logViewportHeight())
		case "home":
			tm.logScroll = tm.maxLogScroll(tm.logViewportHeight())
		case "end":
			tm.logScroll = 0
		}
	case monitorPhaseDone:
		if tm.runErr != nil {
			switch msg.String() {
			case "up", "k":
				tm.scrollLog(1, tm.doneLogHeight())
				return nil, false
			case "down", "j":
				tm.scrollLog(-1, tm.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	case monitorPhaseServiceConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return tm.startServiceRun(), false
		case isSelectionBackKey(msg):
			tm.phase = monitorPhaseAction
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case monitorPhaseLogs:
		switch msg.String() {
		case "up", "k":
			tm.scrollServiceLogs(1)
		case "down", "j":
			tm.scrollServiceLogs(-1)
		case "pgup":
			tm.scrollServiceLogs(tm.serviceLogsHeight())
		case "pgdown":
			tm.scrollServiceLogs(-tm.serviceLogsHeight())
		case "home":
			tm.svcLogScroll = tm.maxServiceLogsScroll()
		case "end":
			tm.svcLogScroll = 0
		case "r":
			tm.loadServiceLogs()
		case "esc", "q", "enter":
			tm.phase = monitorPhaseAction
		}
	}
	return nil, false
}

func (tm *monitorManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if tm.phase == monitorPhaseRunning || (tm.phase == monitorPhaseDone && tm.runErr != nil) {
			tm.scrollLog(3, tm.logViewportHeight())
		} else if tm.phase == monitorPhaseLogs {
			tm.scrollServiceLogs(3)
		}
	case tea.MouseButtonWheelDown:
		if tm.phase == monitorPhaseRunning || (tm.phase == monitorPhaseDone && tm.runErr != nil) {
			tm.scrollLog(-3, tm.logViewportHeight())
		} else if tm.phase == monitorPhaseLogs {
			tm.scrollServiceLogs(-3)
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
	if totals, err := monitor.CurrentTrafficTotals(layout, tm.cfg.ResetDay, tm.cfg.ResetHour, time.Now().UTC()); err == nil {
		tm.totals = totals
	}
	tm.refreshServiceState()
}

func (tm *monitorManager) moveAction(delta int) {
	tm.cursor = moveActionCursor(tm.cursor, tm.actions(), delta)
	tm.fieldErr = ""
}

func (tm *monitorManager) moveTarget(delta int) {
	tm.picker.move(delta)
	tm.fieldErr = ""
}

func (tm *monitorManager) activateAction() {
	tm.fieldErr = ""
	actions := tm.actions()
	idx, ok := selectedIndex(tm.cursor, len(actions))
	if !ok {
		return
	}
	tm.action = actions[idx].action
	switch tm.action {
	case monitorActionLocal:
		tm.startForm(tm.localFields())
	case monitorActionUsage:
		tm.startForm(tm.usageFields())
	case monitorActionLogs:
		tm.loadServiceLogs()
		return
	case monitorActionStart, monitorActionStop, monitorActionRestart:
		if !tm.canApply() {
			tm.fieldErr = tm.applyBlocker()
			return
		}
		tm.phase = monitorPhaseServiceConfirm
	}
}

func (tm *monitorManager) requiresLocalCheck() bool { return tm.picker.selected().isLocal() }

func (tm *monitorManager) startForm(fields []field) {
	tm.parameterForm.setFields(fields)
	tm.parameterForm.validate = validateMonitorField
	tm.phase = monitorPhaseForm
	if tm.parameterForm.advanceField() {
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

func validateMonitorField(f field, val string, _ map[string]string) error {
	return uiparams.ValidateMonitorParameterValue(f.key, val)
}

func (tm *monitorManager) canApply() bool {
	return tm.hostErr == nil && tm.host.IsRoot && tm.host.Supported() && !tm.host.SELinux
}

func (tm *monitorManager) applyBlocker() string {
	if tm.hostErr != nil {
		return "failed to detect host: " + tm.hostErr.Error()
	}
	if !tm.host.IsRoot {
		return "monitor changes must be run as root"
	}
	if !tm.host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", tm.host.OS.Family, tm.host.Arch)
	}
	if tm.host.SELinux {
		return "SELinux is enforcing; monitor changes are blocked"
	}
	return "cannot apply monitor changes"
}

func (tm *monitorManager) startRun() tea.Cmd {
	t := tm.picker.selected()
	tm.agentOutcomes = nil
	if t.isLocal() {
		if !tm.canApply() {
			tm.fieldErr = tm.applyBlocker()
			tm.phase = monitorPhaseAction
			return nil
		}
		tm.phase = monitorPhaseRunning
		tm.resetRun(make(chan runMsg, 64))
		ch := tm.ch
		logs := &logWriter{ch: ch}
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
	tm.phase = monitorPhaseRunning
	tm.resetRun(make(chan runMsg, 64))
	ch := tm.ch
	go tm.runAgentMonitorUpdate(ch, t)
	return tm.waitForRun()
}

func (tm *monitorManager) runAgentMonitorUpdate(ch chan runMsg, t target) {
	req := tm.agentMonitorRequest()
	nodes := []cluster.Node{t.node}
	if t.isAll() {
		nodes = agentNodes(tm.picker)
		opts := tm.updateOptions()
		opts.Layout = monitorUILayout()
		opts.Runner = system.NewExecRunner(&logWriter{ch: ch})
		opts.Firewall = tm.host.Firewall
		opts.Progress = func(e monitor.ManageEvent) {
			de := deploy.Event{Index: e.Index, Total: e.Total, Label: "Local " + e.Label, Detail: e.Detail, Status: e.Status, Err: e.Err}
			ch <- runMsg{event: &de}
		}
		if _, err := updateMonitorRun(context.Background(), opts); err != nil {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Local monitor update", Detail: err.Error(), Status: "failed", Err: err}}
		} else {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Local monitor update", Detail: "done", Status: "done"}}
		}
	}
	var firstErr error
	tm.agentOutcomes = make([]agentOutcome, 0, len(nodes))
	for i, node := range nodes {
		ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: node.Alias + " (" + node.WGIP + ")", Status: "running"}}
		client := cluster.NewAgentClient(node)
		err := client.UpdateMonitor(context.Background(), req)
		tm.agentOutcomes = append(tm.agentOutcomes, agentOutcome{node: node, err: err})
		if err != nil {
			ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: err.Error(), Status: "failed", Err: err}}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: node.Alias + " (" + node.WGIP + ")", Status: "done"}}
	}
	ch <- runMsg{done: true, err: firstErr}
}

func (tm *monitorManager) agentMonitorRequest() cluster.MonitorUpdate {
	if !monitorEnabled(tm.values) {
		return cluster.MonitorUpdate{Disabled: true}
	}
	inLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_in_limit"])
	outLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_out_limit"])
	totalLimit, _ := uiparams.ParseTrafficSize(tm.values["traffic_total_limit"])
	interval, _ := strconv.Atoi(strings.TrimSpace(tm.values["monitor_interval_seconds"]))
	resetDay, _ := strconv.Atoi(strings.TrimSpace(tm.values["reset_day"]))
	resetHour, _ := strconv.Atoi(strings.TrimSpace(tm.values["reset_hour"]))
	return cluster.MonitorUpdate{
		Interface:        strings.TrimSpace(tm.values["monitor_interface"]),
		SamplingInterval: strconv.Itoa(interval),
		InLimitBytes:     inLimit,
		OutLimitBytes:    outLimit,
		TotalLimitBytes:  totalLimit,
		ResetDay:         resetDay,
		ResetHour:        resetHour,
	}
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
	case monitorPhaseTarget:
		return renderTargetPicker("Monitor · Target", tm.picker)
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
	case monitorPhaseLogs:
		return tm.serviceLogsView()
	default:
		return ""
	}
}

func (tm *monitorManager) actionView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Monitor") + "\n\n")
	if tm.picker.hasNodes() {
		b.WriteString(renderTargetBadge(tm.picker.selected()) + "\n\n")
	}
	if !tm.cfg.DeployMonitor {
		b.WriteString(dimStyle.Render("Monitor was not deployed at install time.") + "\n")
		b.WriteString(dimStyle.Render("Traffic/resource sampling, quota auto-stop, slave summary fetch, and the /monitor endpoint are all off.") + "\n")
		b.WriteString(dimStyle.Render("To enable monitoring, run install again.") + "\n")
		return b.String()
	}
	if tm.picker.selected().isLocal() {
		rows := []summaryLine{
			summaryRow("Monitor", yesNoString(tm.cfg.DeployMonitor)),
			summaryRow("Monitor alias", or(tm.cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
			summaryRow("Monitor UI port", strconv.Itoa(tm.cfg.MonitorPublicPort)),
			summaryRow("Monitor local port", strconv.Itoa(tm.cfg.MonitorPort)),
			summaryRow("Monitor interface", or(tm.cfg.MonitorInterface, "auto/default")),
			summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(tm.cfg), uiparams.DefaultResetHour(tm.cfg))),
			summaryRow("Current inbound", byteSize(tm.totals.InBytes)),
			summaryRow("Current outbound", byteSize(tm.totals.OutBytes)),
			summaryRow("Monitor service", or(tm.serviceState, "unknown")),
		}
		b.WriteString(renderSummary(rows) + "\n")
		if !tm.canApply() {
			b.WriteString(flowErr.Render(tm.applyBlocker()) + "\n")
		}
	}
	if tm.fieldErr != "" {
		b.WriteString(flowErr.Render(tm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(tm.actions(), tm.cursor))
	return b.String()
}

func (tm *monitorManager) confirmView() string {
	t := tm.picker.selected()
	rows := []summaryLine{summaryRow("Target", t.badge())}
	switch tm.action {
	case monitorActionLocal:
		rows = append(rows,
			summaryRow("Deploy monitor", tm.values["monitor"]),
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
	}
	rows = append(rows, summaryBlank())
	switch {
	case t.isLocal():
		rows = append(rows, summaryText("This will update monitor state and refresh /monitor data."))
	case t.isNode():
		rows = append(rows, summaryText("This will send the new monitor configuration to "+t.node.Alias+" and restart its monitor service."))
	case t.isAll():
		rows = append(rows, summaryText("This will update the master monitor and broadcast the configuration to every registered node."))
	}
	return flowTitle.Render("Monitor · Confirm") + "\n\n" + renderSummary(rows)
}

func (tm *monitorManager) doneSummary() string {
	t := tm.picker.selected()
	cfg := tm.result
	if cfg.Domain == "" {
		cfg = tm.cfg
	}
	rows := []summaryLine{summaryRow("Target", t.badge())}
	if t.isLocal() {
		rows = append(rows,
			summaryRow("Monitor", yesNoString(cfg.DeployMonitor)),
			summaryRow("Monitor alias", or(cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
			summaryRow("Monitor UI port", strconv.Itoa(cfg.MonitorPublicPort)),
			summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(cfg), uiparams.DefaultResetHour(cfg))),
		)
	}
	if len(tm.agentOutcomes) > 0 {
		rows = append(rows, summaryBlank(), summaryText("Per-node outcomes:"))
		for _, o := range tm.agentOutcomes {
			label := o.node.Alias + " (" + o.node.WGIP + ")"
			value := "ok"
			if o.err != nil {
				value = "failed: " + o.err.Error()
			}
			rows = append(rows, summaryIndentedRow(2, label, value))
		}
	}
	return renderSummary(rows)
}

func (tm *monitorManager) footerHints() []operationHint {
	if tm.loadErr != nil {
		return returnFooterHints()
	}
	switch tm.phase {
	case monitorPhaseTarget:
		return actionFooterHints("Select")
	case monitorPhaseAction:
		if !tm.cfg.DeployMonitor {
			return returnFooterHints()
		}
		if tm.picker.hasNodes() {
			return actionBackFooterHints("Select")
		}
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
	case monitorPhaseLogs:
		return []operationHint{hint(keyMoveMouse, "Scroll"), hint(keyRefresh, "Refresh"), hint(keyReturn, "Return")}
	default:
		return nil
	}
}

func (tm *monitorManager) actions() []monitorActionItem {
	if !tm.cfg.DeployMonitor {
		return nil
	}
	items := []monitorActionItem{
		{separator: true, label: "Monitor"},
		{action: monitorActionLocal, label: "Edit monitor settings"},
	}
	if tm.picker.selected().isLocal() {
		items = append(items,
			monitorActionItem{action: monitorActionUsage, label: "Adjust traffic counters"},
			monitorActionItem{separator: true, label: "Service"},
			monitorActionItem{action: monitorActionStart, label: "Start monitor service"},
			monitorActionItem{action: monitorActionStop, label: "Stop monitor service"},
			monitorActionItem{action: monitorActionRestart, label: "Restart monitor service"},
			monitorActionItem{action: monitorActionLogs, label: "View monitor service logs"},
		)
	}
	return items
}

func (tm *monitorManager) serviceConfirmView() string {
	rows := []summaryLine{
		summaryRow("Action", tm.serviceActionLabel()),
		summaryRow("Service", or(tm.serviceState, "unknown")),
		summaryBlank(),
		summaryText("This will run systemctl " + tm.serviceSystemctlAction() + " " + system.MonitorService + "."),
	}
	return flowTitle.Render("Monitor · Confirm") + "\n\n" + renderSummary(rows)
}

func (tm *monitorManager) serviceActionLabel() string {
	for _, a := range tm.actions() {
		if a.action == tm.action {
			return a.label
		}
	}
	return "unknown"
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

func (tm *monitorManager) loadServiceLogs() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tm.logs, tm.logErr = monitorLogOutput(ctx, 200)
	tm.svcLogScroll = 0
	tm.phase = monitorPhaseLogs
}

func (tm *monitorManager) serviceLogsView() string {
	body := flowTitle.Render("Monitor · Logs") + "\n\n"
	if tm.logErr != nil {
		body += flowErr.Render(tm.logErr.Error()) + "\n\n"
	}
	if strings.TrimSpace(tm.logs) == "" {
		body += dimStyle.Render("no logs returned")
	} else {
		body += strings.Join(tm.visibleServiceLogOutput(), "\n")
	}
	return body
}

func (tm *monitorManager) visibleServiceLogOutput() []string {
	rows := tm.serviceLogRows()
	if len(rows) == 0 {
		return nil
	}
	visible := min(tm.serviceLogsHeight(), len(rows))
	tm.clampServiceLogsScroll()
	start := len(rows) - visible - tm.svcLogScroll
	return rows[start : start+visible]
}

func (tm *monitorManager) serviceLogRows() []string {
	width := tm.width
	if width <= 0 {
		width = 80
	}
	style := dimStyle.Width(max(1, width))
	var rows []string
	for _, line := range strings.Split(strings.TrimRight(tm.logs, "\n"), "\n") {
		rows = append(rows, strings.Split(style.Render(line), "\n")...)
	}
	return rows
}

func (tm *monitorManager) scrollServiceLogs(delta int) {
	tm.svcLogScroll += delta
	tm.clampServiceLogsScroll()
}

func (tm *monitorManager) clampServiceLogsScroll() {
	tm.svcLogScroll = min(max(0, tm.svcLogScroll), tm.maxServiceLogsScroll())
}

func (tm *monitorManager) maxServiceLogsScroll() int {
	return max(0, len(tm.serviceLogRows())-tm.serviceLogsHeight())
}

func (tm *monitorManager) serviceLogsHeight() int {
	if tm.height <= 0 {
		return 12
	}
	return max(1, tm.height-5)
}

func defaultMonitorLogOutput(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	out, err := exec.CommandContext(ctx, "journalctl", "-u", system.MonitorService, "-n", strconv.Itoa(lines), "--no-pager").CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return "", err
		}
		return string(out), fmt.Errorf("%w: %s", err, text)
	}
	return string(out), nil
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
		WriteState: func(stateDir string, mcfg monitor.ManageConfig) error {
			layout := monitorUILayout()
			dcfg, err := deploy.LoadProtocolConfig(layout)
			if err != nil {
				return err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
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
			dcfg.MonitorPublicPort = mcfg.MonitorPublicPort
			dcfg.MonitorPort = mcfg.MonitorPort
			dcfg.SubscribePort = mcfg.SubscribePort
			return deploy.WriteManagedNginxConfig(l, dcfg, confPath)
		},
		RenderMonitorUnit: func(l paths.Layout, monitorBin string, mcfg monitor.ManageConfig) (string, error) {
			dcfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return "", err
			}
			dcfg.DeployMonitor = mcfg.DeployMonitor
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
			return deploy.RenderMonitorUnit(l, monitorBin, dcfg)
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}
