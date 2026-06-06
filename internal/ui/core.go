package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	corepkg "github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type corePhase int

const (
	corePhaseAction corePhase = iota
	corePhaseStableSelect
	corePhaseConfirm
	corePhaseRunning
	corePhaseDone
	corePhaseLogs
)

type coreAction int

const (
	coreActionChangeStable coreAction = iota
	coreActionStart
	coreActionStop
	coreActionRestart
	coreActionLogs
)

const coreStableReleaseLimit = 8

var (
	coreUILayout        = paths.DefaultLayout
	detectCoreHost      = system.DetectHost
	coreCurrentVersion  = func(layout paths.Layout) string { return singBoxVersion(layout.SingBoxBin) }
	coreServiceSnapshot = func() string { return serviceState(system.SingBoxService) }
	coreLogOutput       = defaultCoreLogOutput
	coreReleaseClient   = func() *release.Client { return release.NewClient("", nil) }
)

type coreActionItem struct {
	action coreAction
	label  string
}

type coreManager struct {
	phase  corePhase
	action coreAction

	width  int
	height int

	host    system.Host
	hostErr error

	currentVersion string
	serviceState   string
	fieldErr       string

	cursor     int
	stableTags []string
	targetTag  string
	resultTag  string

	logs      string
	logErr    error
	logScroll int

	commandRun
}

func newCoreManager() *coreManager {
	cm := &coreManager{phase: corePhaseAction, commandRun: newCommandRun()}
	cm.host, cm.hostErr = detectCoreHost()
	cm.refreshSnapshot()
	return cm
}

func (cm *coreManager) refreshSnapshot() {
	cm.currentVersion = coreCurrentVersion(coreUILayout())
	cm.serviceState = coreServiceSnapshot()
}

func (cm *coreManager) setSize(width, height int) {
	cm.width = width
	cm.height = height
	cm.commandRun.setSize(width, height)
}

func (cm *coreManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cm.setSize(msg.Width, msg.Height)
	case runMsg:
		return cm.handleRun(msg), false
	case tea.KeyMsg:
		return cm.handleKey(msg)
	case tea.MouseMsg:
		return cm.handleMouse(msg), false
	}
	return nil, false
}

func (cm *coreManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch cm.phase {
	case corePhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: cm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				cm.activateAction()
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case corePhaseStableSelect:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: cm.moveStable,
			Confirm: func() (tea.Cmd, bool) {
				if idx, ok := selectedIndex(cm.cursor, len(cm.stableTags)); ok {
					cm.targetTag = cm.stableTags[idx]
					cm.phase = corePhaseConfirm
				}
				return nil, false
			},
			Back: func() (tea.Cmd, bool) {
				cm.cursor = 0
				cm.phase = corePhaseAction
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case corePhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return cm.startRun(), false
		case isSelectionBackKey(msg):
			if cm.action == coreActionChangeStable {
				cm.phase = corePhaseStableSelect
			} else {
				cm.phase = corePhaseAction
			}
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case corePhaseRunning:
		switch msg.String() {
		case "enter":
			if cm.runComplete {
				cm.refreshSnapshot()
				cm.phase = corePhaseDone
			}
		case "up", "k":
			cm.scrollLog(1, cm.logViewportHeight())
		case "down", "j":
			cm.scrollLog(-1, cm.logViewportHeight())
		case "pgup":
			cm.scrollLog(cm.logViewportHeight(), cm.logViewportHeight())
		case "pgdown":
			cm.scrollLog(-cm.logViewportHeight(), cm.logViewportHeight())
		case "home":
			cm.logScroll = cm.maxLogScroll(cm.logViewportHeight())
		case "end":
			cm.logScroll = 0
		}
	case corePhaseDone:
		if cm.runErr != nil {
			switch msg.String() {
			case "up", "k":
				cm.scrollLog(1, cm.doneLogHeight())
				return nil, false
			case "down", "j":
				cm.scrollLog(-1, cm.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	case corePhaseLogs:
		switch msg.String() {
		case "up", "k":
			cm.scrollLogs(1)
		case "down", "j":
			cm.scrollLogs(-1)
		case "pgup":
			cm.scrollLogs(cm.logsHeight())
		case "pgdown":
			cm.scrollLogs(-cm.logsHeight())
		case "home":
			cm.logScroll = cm.maxLogsScroll()
		case "end":
			cm.logScroll = 0
		case "r":
			cm.loadLogs()
		case "esc", "q", "enter":
			cm.phase = corePhaseAction
		}
	}
	return nil, false
}

func (cm *coreManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if cm.phase == corePhaseRunning || (cm.phase == corePhaseDone && cm.runErr != nil) {
			cm.scrollLog(3, cm.logViewportHeight())
		} else if cm.phase == corePhaseLogs {
			cm.scrollLogs(3)
		}
	case tea.MouseButtonWheelDown:
		if cm.phase == corePhaseRunning || (cm.phase == corePhaseDone && cm.runErr != nil) {
			cm.scrollLog(-3, cm.logViewportHeight())
		} else if cm.phase == corePhaseLogs {
			cm.scrollLogs(-3)
		}
	}
	return nil
}

func (cm *coreManager) moveAction(delta int) {
	actions := cm.actions()
	cm.cursor = moveSelection(cm.cursor, len(actions), delta)
	cm.fieldErr = ""
}

func (cm *coreManager) moveStable(delta int) {
	if len(cm.stableTags) == 0 {
		return
	}
	cm.cursor = moveSelection(cm.cursor, len(cm.stableTags), delta)
	cm.fieldErr = ""
}

func (cm *coreManager) activateAction() {
	cm.fieldErr = ""
	actions := cm.actions()
	idx, ok := selectedIndex(cm.cursor, len(actions))
	if !ok {
		return
	}
	cm.action = actions[idx].action
	if cm.action == coreActionLogs {
		cm.loadLogs()
		return
	}
	if !cm.canApply() {
		cm.fieldErr = cm.applyBlocker()
		return
	}
	if cm.action == coreActionChangeStable {
		cm.loadStableTags()
		return
	}
	cm.phase = corePhaseConfirm
}

func (cm *coreManager) loadStableTags() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr := cm.backendManager(nil)
	tags, err := mgr.RecentStable(ctx, coreStableReleaseLimit)
	if err != nil {
		cm.fieldErr = "fetch stable releases: " + err.Error()
		cm.phase = corePhaseAction
		return
	}
	if len(tags) == 0 {
		cm.fieldErr = "no stable releases found"
		cm.phase = corePhaseAction
		return
	}
	cm.stableTags = tags
	cm.cursor = 0
	cm.phase = corePhaseStableSelect
}

func (cm *coreManager) loadLogs() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cm.logs, cm.logErr = coreLogOutput(ctx, 200)
	cm.logScroll = 0
	cm.phase = corePhaseLogs
}

func (cm *coreManager) canApply() bool {
	return cm.hostErr == nil && cm.host.IsRoot && cm.host.Supported() && !cm.host.SELinux
}

func (cm *coreManager) applyBlocker() string {
	if cm.hostErr != nil {
		return "failed to detect host: " + cm.hostErr.Error()
	}
	if !cm.host.IsRoot {
		return "core management must be run as root"
	}
	if !cm.host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", cm.host.OS.Family, cm.host.Arch)
	}
	if cm.host.SELinux {
		return "SELinux is enforcing; core management is blocked"
	}
	return "cannot apply core management action"
}

func (cm *coreManager) startRun() tea.Cmd {
	cm.phase = corePhaseRunning
	cm.resetRun(make(chan runMsg, 64))
	ch := cm.ch
	logs := &logWriter{ch: ch}
	mgr := cm.backendManager(logs)
	action, tag := cm.backendAction()
	go func() {
		res, err := mgr.Run(context.Background(), action, tag)
		ch <- runMsg{done: true, err: err, resultTag: res.Tag}
	}()
	return cm.waitForRun()
}

func (cm *coreManager) backendManager(logs *logWriter) *corepkg.Manager {
	mgr := &corepkg.Manager{
		Layout:   coreUILayout(),
		Releases: coreReleaseClient(),
		GOOS:     "linux",
		GOARCH:   cm.host.Arch,
	}
	if logs != nil {
		mgr.Runner = system.NewExecRunner(logs)
		mgr.Progress = func(e deploy.Event) {
			ev := e
			logs.ch <- runMsg{event: &ev}
		}
	}
	return mgr
}

func (cm *coreManager) backendAction() (corepkg.Action, string) {
	switch cm.action {
	case coreActionChangeStable:
		return corepkg.ActionChangeStable, cm.targetTag
	case coreActionStart:
		return corepkg.ActionStart, ""
	case coreActionStop:
		return corepkg.ActionStop, ""
	case coreActionRestart:
		return corepkg.ActionRestart, ""
	default:
		return corepkg.ActionRestart, ""
	}
}

func (cm *coreManager) handleRun(msg runMsg) tea.Cmd {
	if msg.resultTag != "" {
		cm.resultTag = msg.resultTag
	}
	return handleCommandRun(cm, msg)
}

func (cm *coreManager) runState() *commandRun { return &cm.commandRun }

func (cm *coreManager) markRunFailed() { cm.phase = corePhaseDone }

func (cm *coreManager) View() string {
	switch cm.phase {
	case corePhaseAction:
		return cm.actionView()
	case corePhaseStableSelect:
		return cm.stableView()
	case corePhaseConfirm:
		return cm.confirmView()
	case corePhaseRunning:
		return commandRunningView(cm, "sing-box Core · Running")
	case corePhaseDone:
		if cm.runErr != nil {
			return commandFailedView(cm, "sing-box core action failed")
		}
		return flowOK.Render("sing-box core action complete") + "\n\n" + cm.doneSummary()
	case corePhaseLogs:
		return cm.logsView()
	default:
		return ""
	}
}

func (cm *coreManager) actionView() string {
	rows := []summaryLine{
		summaryRow("Current version", or(cm.currentVersion, "not installed")),
		summaryRow("Service", or(cm.serviceState, "unknown")),
		summaryRow("Binary", coreUILayout().SingBoxBin),
		summaryRow("Config", coreUILayout().ConfigJSON),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render("sing-box Core Management") + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if cm.fieldErr != "" {
		b.WriteString(flowErr.Render(cm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, action := range cm.actions() {
		row := "  " + action.label
		if i == cm.cursor {
			row = selStyle.Render("> " + action.label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (cm *coreManager) stableView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("sing-box Core · Change Version") + "\n\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("Choose one of the latest %d stable sing-box releases.", coreStableReleaseLimit)) + "\n\n")
	for i, tag := range cm.stableTags {
		row := "  " + tag
		if i == cm.cursor {
			row = selStyle.Render("> " + tag)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (cm *coreManager) confirmView() string {
	rows := []summaryLine{
		summaryRow("Action", cm.actionLabel()),
		summaryRow("Current version", or(cm.currentVersion, "not installed")),
		summaryRow("Service", or(cm.serviceState, "unknown")),
	}
	if cm.action == coreActionChangeStable {
		rows = append(rows, summaryRow("Target release", cm.targetTag))
	}
	rows = append(rows, summaryBlank())
	if cm.isReplaceAction() {
		rows = append(rows, summaryText("This will stop sing-box.service, download the selected stable release, replace the managed binary, validate config.json, and restart sing-box.service."))
	} else {
		rows = append(rows, summaryText("This will run systemctl "+cm.systemctlAction()+" sing-box.service."))
	}
	return flowTitle.Render("sing-box Core · Confirm") + "\n\n" + renderSummary(rows)
}

func (cm *coreManager) doneSummary() string {
	rows := []summaryLine{
		summaryRow("Action", cm.actionLabel()),
		summaryRow("Current version", or(cm.currentVersion, "unknown")),
		summaryRow("Service", or(cm.serviceState, "unknown")),
	}
	if cm.resultTag != "" {
		rows = append(rows, summaryRow("Applied release", cm.resultTag))
	}
	return renderSummary(rows)
}

func (cm *coreManager) logsView() string {
	body := flowTitle.Render("sing-box Core · Logs") + "\n\n"
	if cm.logErr != nil {
		body += flowErr.Render(cm.logErr.Error()) + "\n\n"
	}
	if strings.TrimSpace(cm.logs) == "" {
		body += dimStyle.Render("no logs returned")
	} else {
		body += strings.Join(cm.visibleLogOutput(), "\n")
	}
	return body
}

func (cm *coreManager) footerHints() []operationHint {
	switch cm.phase {
	case corePhaseAction:
		return actionFooterHints("Select")
	case corePhaseStableSelect:
		return actionBackFooterHints("Continue")
	case corePhaseConfirm:
		return applyFooterHints("Apply")
	case corePhaseRunning:
		return runningFooterHints(cm.runComplete)
	case corePhaseDone:
		return doneFooterHints(cm.runErr != nil)
	case corePhaseLogs:
		return []operationHint{hint(keyMoveMouse, "Scroll"), hint(keyRefresh, "Refresh"), hint(keyReturn, "Return")}
	default:
		return nil
	}
}

func (cm *coreManager) actions() []coreActionItem {
	return []coreActionItem{
		{action: coreActionChangeStable, label: "Change to recent stable core"},
		{action: coreActionStart, label: "Start sing-box.service"},
		{action: coreActionStop, label: "Stop sing-box.service"},
		{action: coreActionRestart, label: "Restart sing-box.service"},
		{action: coreActionLogs, label: "View sing-box.service logs"},
	}
}

func (cm *coreManager) actionLabel() string {
	for _, action := range cm.actions() {
		if action.action == cm.action {
			return action.label
		}
	}
	return "unknown"
}

func (cm *coreManager) isReplaceAction() bool {
	return cm.action == coreActionChangeStable
}

func (cm *coreManager) systemctlAction() string {
	switch cm.action {
	case coreActionStart:
		return "start"
	case coreActionStop:
		return "stop"
	case coreActionRestart:
		return "restart"
	default:
		return ""
	}
}

func (cm *coreManager) visibleLogOutput() []string {
	rows := cm.logRowsForOutput()
	if len(rows) == 0 {
		return nil
	}
	visible := min(cm.logsHeight(), len(rows))
	cm.clampLogsScroll()
	start := len(rows) - visible - cm.logScroll
	return rows[start : start+visible]
}

func (cm *coreManager) logRowsForOutput() []string {
	width := cm.width
	if width <= 0 {
		width = 80
	}
	style := dimStyle.Width(max(1, width))
	var rows []string
	for _, line := range strings.Split(strings.TrimRight(cm.logs, "\n"), "\n") {
		rows = append(rows, strings.Split(style.Render(line), "\n")...)
	}
	return rows
}

func (cm *coreManager) scrollLogs(delta int) {
	cm.logScroll += delta
	cm.clampLogsScroll()
}

func (cm *coreManager) clampLogsScroll() {
	cm.logScroll = min(max(0, cm.logScroll), cm.maxLogsScroll())
}

func (cm *coreManager) maxLogsScroll() int {
	return max(0, len(cm.logRowsForOutput())-cm.logsHeight())
}

func (cm *coreManager) logsHeight() int {
	if cm.height <= 0 {
		return 12
	}
	return max(1, cm.height-5)
}

func defaultCoreLogOutput(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	out, err := exec.CommandContext(ctx, "journalctl", "-u", system.SingBoxService, "-n", strconv.Itoa(lines), "--no-pager").CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return "", err
		}
		return string(out), fmt.Errorf("%w: %s", err, text)
	}
	return string(out), nil
}
