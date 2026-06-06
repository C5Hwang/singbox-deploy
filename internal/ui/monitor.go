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
	monitorActionRemotes
	monitorActionRefresh
)

var (
	monitorUILayout   = paths.DefaultLayout
	detectMonitorHost = system.DetectHost
	updateMonitorRun = monitor.UpdateSettings
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

	host    system.Host
	hostErr error
	cfg     deploy.Config
	remotes []deploy.RemoteSubscription
	totals  monitor.TrafficTotals
	loadErr error

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
	remotes, err := deploy.LoadRemoteSubscriptions(layout)
	if err != nil {
		tm.loadErr = err
		return tm
	}
	tm.remotes = remotes
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
	if remotes, err := deploy.LoadRemoteSubscriptions(layout); err == nil {
		tm.remotes = remotes
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
	case monitorActionRemotes:
		if len(tm.remotes) == 0 {
			tm.fieldErr = "no remote subscriptions configured"
			return
		}
		tm.startForm(tm.remoteMonitorFields())
	case monitorActionRefresh:
		tm.phase = monitorPhaseConfirm
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

func (tm *monitorManager) remoteMonitorFields() []field {
	options := make([]string, 0, len(tm.remotes))
	selected := make(map[string]bool)
	for _, remote := range tm.remotes {
		label := remoteOptionLabel(remote)
		options = append(options, label)
		if remote.Monitor {
			selected[label] = true
		}
	}
	fields := []field{{
		key:     "remote_monitor_sources",
		label:   "Remote monitor sources",
		def:     selectedOptionsValue(options, selected),
		options: options,
		multi:   true,
		note:    "Select configured remote subscriptions whose /monitor/api/summary should be aggregated into the local /monitor page.",
	}}
	for i, remote := range tm.remotes {
		label := remoteOptionLabel(remote)
		idx := i
		fields = append(fields, field{
			key:   remoteMonitorPublicPortKey(idx),
			label: "Monitor HTTPS port for " + label,
			def:   strconv.Itoa(defaultRemoteMonitorPublicPort(remote, tm.cfg)),
			note:  "Public HTTPS port serving the remote /monitor page.",
			skip: func(vals map[string]string) bool {
				return !selectedOptions(vals["remote_monitor_sources"])[label]
			},
		})
	}
	return fields
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
	case monitorActionRemotes:
		opts := base
		opts.SetRemotes = true
		opts.Remotes = tm.targetRemoteMonitor()
		return opts
	case monitorActionRefresh:
		opts := base
		opts.SetRemotes = true
		opts.Remotes = toManageRemotes(tm.remotes)
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

func (tm *monitorManager) targetRemoteMonitor() []monitor.ManageRemote {
	selected := selectedOptions(tm.values["remote_monitor_sources"])
	remotes := make([]monitor.ManageRemote, 0, len(tm.remotes))
	for i, remote := range tm.remotes {
		label := remoteOptionLabel(remote)
		mr := monitor.ManageRemote{
			Domain:            remote.Domain,
			Port:              remote.Port,
			Alias:             remote.Alias,
			Salt:              remote.Salt,
			Monitor:           selected[label],
			MonitorPublicPort: remote.MonitorPublicPort,
		}
		if mr.Monitor {
			if port, err := strconv.Atoi(strings.TrimSpace(tm.values[remoteMonitorPublicPortKey(i)])); err == nil {
				mr.MonitorPublicPort = port
			}
		} else {
			mr.MonitorPublicPort = 0
		}
		remotes = append(remotes, mr)
	}
	return remotes
}

func remoteMonitorPublicPortKey(index int) string {
	return fmt.Sprintf("remote_monitor_public_port_%d", index)
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
		summaryRow("Monitor alias", or(tm.cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow("Monitor UI port", strconv.Itoa(tm.cfg.MonitorPublicPort)),
		summaryRow("Monitor local port", strconv.Itoa(tm.cfg.MonitorPort)),
		summaryRow("Monitor interface", or(tm.cfg.MonitorInterface, "auto/default")),
		summaryRow("Reset", fmt.Sprintf("day %d hour %d GMT", uiparams.DefaultResetDay(tm.cfg), uiparams.DefaultResetHour(tm.cfg))),
		summaryRow("Current inbound", byteSize(tm.totals.InBytes)),
		summaryRow("Current outbound", byteSize(tm.totals.OutBytes)),
		summaryRow("Remote monitor sources", strconv.Itoa(countRemoteMonitor(tm.remotes))),
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
			summaryRow("Monitor alias", tm.values["monitor_alias"]),
			summaryRow("Monitor UI port", tm.values["monitor_public_port"]),
			summaryRow("Monitor local port", tm.values["monitor_port"]),
			summaryRow("Monitor interface", tm.values["monitor_interface"]),
			summaryRow("Sampling interval", tm.values["monitor_interval_seconds"]+" seconds"),
			summaryRow("Inbound limit", tm.values["traffic_in_limit"]),
			summaryRow("Outbound limit", tm.values["traffic_out_limit"]),
			summaryRow("Total limit", tm.values["traffic_total_limit"]),
			summaryRow("Reset", fmt.Sprintf("day %s hour %s GMT", tm.values["reset_day"], tm.values["reset_hour"])),
		)
	case monitorActionUsage:
		rows = append(rows,
			summaryRow("Current inbound", byteSize(tm.totals.InBytes)+" -> "+tm.values["current_in_traffic"]),
			summaryRow("Current outbound", byteSize(tm.totals.OutBytes)+" -> "+tm.values["current_out_traffic"]),
		)
	case monitorActionRemotes:
		selected := selectedOptions(tm.values["remote_monitor_sources"])
		rows = append(rows, summaryRow("Selected remote monitors", strconv.Itoa(len(selected))))
		for i, remote := range tm.remotes {
			label := remoteOptionLabel(remote)
			if selected[label] {
				rows = append(rows, summaryIndentedRow(2, "Remote", label+" port "+tm.values[remoteMonitorPublicPortKey(i)]))
			}
		}
		if len(selected) == 0 {
			rows = append(rows, summaryIndentedRow(2, "Remote", "none"))
		}
	case monitorActionRefresh:
		rows = append(rows, summaryRow("Refresh remote monitors", strconv.Itoa(countRemoteMonitor(tm.remotes))))
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
		summaryRow("Monitor alias", or(cfg.MonitorAlias, deploy.DefaultMonitorAlias)),
		summaryRow("Monitor UI port", strconv.Itoa(cfg.MonitorPublicPort)),
		summaryRow("Reset", fmt.Sprintf("day %d hour %d GMT", uiparams.DefaultResetDay(cfg), uiparams.DefaultResetHour(cfg))),
		summaryRow("Remote monitor sources", strconv.Itoa(countRemoteMonitor(tm.remotes))),
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
		{action: monitorActionUsage, label: "Edit current in/out usage"},
		{action: monitorActionRemotes, label: "Select remote monitor sources"},
		{action: monitorActionRefresh, label: "Refresh remote monitors"},
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
		LoadRemotes: func(l paths.Layout) ([]monitor.ManageRemote, error) {
			dr, err := deploy.LoadRemoteSubscriptions(l)
			if err != nil {
				return nil, err
			}
			return toManageRemotes(dr), nil
		},
		ValidateRemotes: func(remotes []monitor.ManageRemote) error {
			return deploy.ValidateRemoteSubscriptions(fromManageRemotes(remotes))
		},
		SaveRemotes: func(l paths.Layout, remotes []monitor.ManageRemote) error {
			return deploy.SaveRemoteSubscriptions(l, fromManageRemotes(remotes))
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
		RenderMonitorUnit: func(l paths.Layout, deployBin string, mcfg monitor.ManageConfig) (string, error) {
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
			return deploy.RenderMonitorUnit(l, deployBin, dcfg)
		},
		RefreshRemoteMonitor: func(ctx context.Context, l paths.Layout, remotes []monitor.ManageRemote, fetch func(context.Context, string) ([]byte, error)) error {
			return deploy.RefreshRemoteMonitor(ctx, l, fromManageRemotes(remotes), deploy.SubscriptionFetcher(fetch))
		},
		RunCommands: func(r system.Runner, cmds ...system.Command) error {
			return deploy.RunCommands(r, cmds...)
		},
	}
}

func toManageRemotes(remotes []deploy.RemoteSubscription) []monitor.ManageRemote {
	out := make([]monitor.ManageRemote, len(remotes))
	for i, r := range remotes {
		out[i] = monitor.ManageRemote{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt, Monitor: r.Monitor, MonitorPublicPort: r.MonitorPublicPort}
	}
	return out
}

func fromManageRemotes(remotes []monitor.ManageRemote) []deploy.RemoteSubscription {
	out := make([]deploy.RemoteSubscription, len(remotes))
	for i, r := range remotes {
		out[i] = deploy.RemoteSubscription{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt, Monitor: r.Monitor, MonitorPublicPort: r.MonitorPublicPort}
	}
	return out
}

func countRemoteMonitor(remotes []deploy.RemoteSubscription) int {
	count := 0
	for _, remote := range remotes {
		if remote.Monitor {
			count++
		}
	}
	return count
}

func defaultRemoteMonitorPublicPort(remote deploy.RemoteSubscription, cfg deploy.Config) int {
	if remote.MonitorPublicPort > 0 {
		return remote.MonitorPublicPort
	}
	if cfg.MonitorPublicPort > 0 {
		return cfg.MonitorPublicPort
	}
	return deploy.DefaultMonitorPublicPort
}
