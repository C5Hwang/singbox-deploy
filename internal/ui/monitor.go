package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
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
)

type monitorAction int

const (
	monitorActionLocal monitorAction = iota
	monitorActionUsage
	monitorActionAddSource
	monitorActionDeleteSources
)

var (
	monitorUILayout   = paths.DefaultLayout
	detectMonitorHost = system.DetectHost
	updateMonitorRun  = monitor.UpdateSettings
)

type monitorActionItem struct {
	action monitorAction
	label  string
}

type monitorManager struct {
	phase  monitorPhase
	action monitorAction

	width  int
	height int

	host           system.Host
	hostErr        error
	cfg            deploy.Config
	monitorSources []deploy.MonitorSource
	totals         monitor.TrafficTotals
	loadErr        error

	cursor int
	parameterForm
	commandRun
	result deploy.Config
}

func newMonitorManager() *monitorManager {
	tm := &monitorManager{
		phase:         monitorPhaseAction,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	tm.host, tm.hostErr = detectMonitorHost()
	layout := monitorUILayout()
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.cfg = cfg
	if err := deploy.MigrateMonitorSources(layout); err != nil {
		tm.loadErr = err
		return tm
	}
	sources, err := deploy.LoadMonitorSources(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.monitorSources = sources
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
	case monitorPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: tm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				tm.activateAction()
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case monitorPhaseForm:
		cmd, done, handled := tm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() { tm.phase = monitorPhaseConfirm },
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
	}
	return nil, false
}

func (tm *monitorManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if tm.phase == monitorPhaseRunning || (tm.phase == monitorPhaseDone && tm.runErr != nil) {
			tm.scrollLog(3, tm.logViewportHeight())
		}
	case tea.MouseButtonWheelDown:
		if tm.phase == monitorPhaseRunning || (tm.phase == monitorPhaseDone && tm.runErr != nil) {
			tm.scrollLog(-3, tm.logViewportHeight())
		}
	}
	return nil
}

func (tm *monitorManager) reloadState() {
	layout := monitorUILayout()
	if cfg, err := deploy.LoadProtocolConfig(layout); err == nil {
		tm.cfg = cfg
		tm.result = cfg
	}
	if sources, err := deploy.LoadMonitorSources(layout); err == nil {
		tm.monitorSources = sources
	}
	if totals, err := monitor.CurrentTrafficTotals(layout, tm.cfg.ResetDay, tm.cfg.ResetHour, time.Now().UTC()); err == nil {
		tm.totals = totals
	}
}

func (tm *monitorManager) moveAction(delta int) {
	tm.cursor = moveSelection(tm.cursor, len(tm.actions()), delta)
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
	case monitorActionAddSource:
		tm.startForm(tm.addMonitorSourceFields())
	case monitorActionDeleteSources:
		if len(tm.monitorSources) == 0 {
			tm.fieldErr = "no monitor sources configured"
			return
		}
		tm.startForm(tm.deleteMonitorSourceFields())
	}
}

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

func (tm *monitorManager) addMonitorSourceFields() []field {
	return []field{
		{key: "monitor_source_domain", label: "Monitor source domain", note: "Domain name of the remote server whose /monitor/api/summary will be aggregated."},
		{key: "monitor_source_alias", label: "Monitor source alias", note: "Alias used as the traffic source label on /monitor."},
		{key: "monitor_source_port", label: "Monitor HTTPS port", def: strconv.Itoa(deploy.DefaultMonitorPublicPort), note: "Public HTTPS port serving the remote /monitor page."},
	}
}

func (tm *monitorManager) deleteMonitorSourceFields() []field {
	options := make([]string, 0, len(tm.monitorSources))
	for _, src := range tm.monitorSources {
		options = append(options, monitorSourceOptionLabel(src))
	}
	return []field{{
		key:     "delete_monitor_sources",
		label:   "Monitor sources to delete",
		options: options,
		multi:   true,
		note:    "Select one or more monitor sources to delete.",
	}}
}

func monitorSourceOptionLabel(src deploy.MonitorSource) string {
	alias := strings.TrimSpace(src.Alias)
	if alias == "" {
		alias = strings.TrimSpace(src.Domain)
	}
	return fmt.Sprintf("%s (%s:%d)", alias, strings.TrimSpace(src.Domain), src.MonitorPublicPort)
}

func validateMonitorField(f field, val string, _ map[string]string) error {
	switch f.key {
	case "monitor_source_domain":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("monitor source domain is required")
		}
		return nil
	case "monitor_source_alias":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("monitor source alias is required")
		}
		return nil
	case "monitor_source_port":
		return uiparams.ValidateMonitorParameterValue("monitor_public_port", val)
	case "delete_monitor_sources":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("select at least one monitor source to delete")
		}
		return nil
	}
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
	case monitorActionAddSource:
		opts := base
		opts.SetMonitorSources = true
		port, _ := strconv.Atoi(strings.TrimSpace(tm.values["monitor_source_port"]))
		sources := append([]deploy.MonitorSource(nil), tm.monitorSources...)
		sources = append(sources, deploy.MonitorSource{
			Domain:            strings.TrimSpace(tm.values["monitor_source_domain"]),
			Alias:             strings.TrimSpace(tm.values["monitor_source_alias"]),
			MonitorPublicPort: port,
		})
		opts.MonitorSources = toManageMonitorSources(sources)
		return opts
	case monitorActionDeleteSources:
		opts := base
		opts.SetMonitorSources = true
		deleted := selectedOptions(tm.values["delete_monitor_sources"])
		sources := make([]deploy.MonitorSource, 0, len(tm.monitorSources))
		for _, src := range tm.monitorSources {
			if deleted[monitorSourceOptionLabel(src)] {
				continue
			}
			sources = append(sources, src)
		}
		opts.MonitorSources = toManageMonitorSources(sources)
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
	default:
		return ""
	}
}

func (tm *monitorManager) actionView() string {
	rows := []summaryLine{
		summaryRow("Monitor", yesNoString(tm.cfg.DeployMonitor)),
		summaryRow("Monitor frontend", yesNoString(tm.cfg.DeployMonitorFrontend)),
		summaryRow("Monitor alias", or(tm.cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow("Monitor UI port", strconv.Itoa(tm.cfg.MonitorPublicPort)),
		summaryRow("Monitor local port", strconv.Itoa(tm.cfg.MonitorPort)),
		summaryRow("Monitor interface", or(tm.cfg.MonitorInterface, "auto/default")),
		summaryRow("Next reset", nextResetLabel(uiparams.DefaultResetDay(tm.cfg), uiparams.DefaultResetHour(tm.cfg))),
		summaryRow("Current inbound", byteSize(tm.totals.InBytes)),
		summaryRow("Current outbound", byteSize(tm.totals.OutBytes)),
		summaryRow("Monitor sources", strconv.Itoa(len(tm.monitorSources))),
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
	for i, action := range tm.actions() {
		row := "  " + action.label
		if i == tm.cursor {
			row = selStyle.Render("> " + action.label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
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
	case monitorActionAddSource:
		rows = append(rows,
			summaryRow("Add monitor source domain", tm.values["monitor_source_domain"]),
			summaryRow("Monitor source alias", tm.values["monitor_source_alias"]),
			summaryRow("Monitor HTTPS port", tm.values["monitor_source_port"]),
		)
	case monitorActionDeleteSources:
		selected := selectedOptions(tm.values["delete_monitor_sources"])
		remaining := make([]deploy.MonitorSource, 0, len(tm.monitorSources))
		for _, src := range tm.monitorSources {
			if !selected[monitorSourceOptionLabel(src)] {
				remaining = append(remaining, src)
			}
		}
		rows = append(rows, summaryRow("Delete monitor sources", strconv.Itoa(len(selected))))
		for _, src := range tm.monitorSources {
			label := monitorSourceOptionLabel(src)
			if selected[label] {
				rows = append(rows, summaryIndentedRow(2, "Delete", label))
			}
		}
		rows = append(rows, summaryRow("Remaining monitor sources", strconv.Itoa(len(remaining))))
		if len(remaining) == 0 {
			rows = append(rows, summaryIndentedRow(2, "Keep", "none"))
		}
		for _, src := range remaining {
			rows = append(rows, summaryIndentedRow(2, "Keep", monitorSourceOptionLabel(src)))
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
		summaryRow("Monitor sources", strconv.Itoa(len(tm.monitorSources))),
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
	default:
		return nil
	}
}

func (tm *monitorManager) actions() []monitorActionItem {
	return []monitorActionItem{
		{action: monitorActionLocal, label: "Edit monitor settings"},
		{action: monitorActionUsage, label: "Adjust traffic counters"},
		{action: monitorActionAddSource, label: "Add monitor source"},
		{action: monitorActionDeleteSources, label: "Delete monitor sources"},
	}
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
		LoadMonitorSources: func(l paths.Layout) ([]monitor.ManageMonitorSource, error) {
			srcs, err := deploy.LoadMonitorSources(l)
			if err != nil {
				return nil, err
			}
			return toManageMonitorSources(srcs), nil
		},
		ValidateMonitorSources: func(sources []monitor.ManageMonitorSource) error {
			return deploy.ValidateMonitorSources(fromManageMonitorSources(sources))
		},
		SaveMonitorSources: func(l paths.Layout, sources []monitor.ManageMonitorSource) error {
			return deploy.SaveMonitorSources(l, fromManageMonitorSources(sources))
		},
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
		RefreshRemoteMonitor: func(ctx context.Context, l paths.Layout, sources []monitor.ManageMonitorSource, fetch func(context.Context, string) ([]byte, error)) error {
			return deploy.RefreshRemoteMonitor(ctx, l, fromManageMonitorSources(sources), deploy.SubscriptionFetcher(fetch))
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}

func toManageMonitorSources(sources []deploy.MonitorSource) []monitor.ManageMonitorSource {
	out := make([]monitor.ManageMonitorSource, len(sources))
	for i, s := range sources {
		out[i] = monitor.ManageMonitorSource{Domain: s.Domain, Alias: s.Alias, MonitorPublicPort: s.MonitorPublicPort}
	}
	return out
}

func fromManageMonitorSources(sources []monitor.ManageMonitorSource) []deploy.MonitorSource {
	out := make([]deploy.MonitorSource, len(sources))
	for i, s := range sources {
		out[i] = deploy.MonitorSource{Domain: s.Domain, Alias: s.Alias, MonitorPublicPort: s.MonitorPublicPort}
	}
	return out
}
