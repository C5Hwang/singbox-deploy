package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	corepkg "github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type corePhase int

const (
	corePhaseAction corePhase = iota
	corePhaseStableLoading
	corePhaseStableSelect
	corePhaseConfirm
	corePhaseRunning
	corePhaseDone
	corePhaseLogsLoading
	corePhaseLogs
)

// coreStableTagsMsg and coreLogsMsg carry the results of the async network /
// journalctl lookups that would otherwise block the UI thread.
type coreStableTagsMsg struct {
	tags []string
	err  error
}

type coreLogsMsg struct {
	logs string
	err  error
}

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
	changeFleetCoreRun  = defaultChangeFleetCore
)

type coreActionItem = actionItem[coreAction]

type coreManager struct {
	phase  corePhase
	action coreAction

	width  int
	height int

	host    system.Host
	hostErr error

	currentVersion string
	serviceState   string
	nodes          []nodes.Node
	nodesErr       error
	fieldErr       string

	cursor     int
	stableTags []string
	targetTag  string
	resultTag  string

	svcLogs serviceLogViewport

	commandRun
}

func newCoreManager() *coreManager {
	cm := &coreManager{phase: corePhaseAction, cursor: 1, commandRun: newCommandRun()}
	cm.host, cm.hostErr = detectCoreHost()
	cm.refreshSnapshot()
	return cm
}

func (cm *coreManager) refreshSnapshot() {
	cm.currentVersion = coreCurrentVersion(coreUILayout())
	cm.serviceState = coreServiceSnapshot()
	cm.nodes, cm.nodesErr = nodes.Load(coreUILayout())
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
	case coreStableTagsMsg:
		cm.applyStableTags(msg)
	case coreLogsMsg:
		cm.svcLogs.set(msg.logs, msg.err)
		cm.phase = corePhaseLogs
	case runMsg:
		return cm.handleRun(msg), false
	case tea.KeyMsg:
		return cm.handleKey(msg)
	case tea.MouseMsg:
		return cm.handleMouse(msg), false
	}
	return nil, false
}

func (cm *coreManager) applyStableTags(msg coreStableTagsMsg) {
	if msg.err != nil {
		cm.fieldErr = "fetch stable releases: " + msg.err.Error()
		cm.phase = corePhaseAction
		return
	}
	if len(msg.tags) == 0 {
		cm.fieldErr = "no stable releases found"
		cm.phase = corePhaseAction
		return
	}
	cm.stableTags = msg.tags
	cm.cursor = 0
	cm.phase = corePhaseStableSelect
}

func (cm *coreManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch cm.phase {
	case corePhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: cm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				return cm.activateAction(), false
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
				// Reset to the first selectable action (index 0 is a separator);
				// the cursor was moved within the release list and would land on
				// the separator, showing no highlight while Enter re-triggers the
				// change-version fetch.
				cm.cursor = 1
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
		if msg.String() == "enter" && cm.runComplete {
			cm.refreshSnapshot()
			cm.phase = corePhaseDone
		} else {
			cm.handleScrollKey(msg.String(), cm.logViewportHeight())
		}
	case corePhaseDone:
		return cm.handleDoneKey(msg.String())
	case corePhaseStableLoading, corePhaseLogsLoading:
		if isSelectionCancelKey(msg) || msg.String() == "esc" {
			cm.phase = corePhaseAction
			return nil, false
		}
	case corePhaseLogs:
		switch msg.String() {
		case "r":
			return cm.loadLogsCmd(), false
		case "esc", "q", "enter":
			cm.phase = corePhaseAction
		default:
			cm.svcLogs.handleKey(msg.String(), cm.width, cm.height)
		}
	}
	return nil, false
}

func (cm *coreManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if cm.handleLogWheel(msg.Button, cm.phase == corePhaseRunning || (cm.phase == corePhaseDone && cm.runErr != nil)) {
		return nil
	}
	if cm.phase == corePhaseLogs {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			cm.svcLogs.scrollBy(3, cm.width, cm.height)
		case tea.MouseButtonWheelDown:
			cm.svcLogs.scrollBy(-3, cm.width, cm.height)
		}
	}
	return nil
}

func (cm *coreManager) moveAction(delta int) {
	cm.cursor = moveActionCursor(cm.cursor, cm.actions(), delta)
	cm.fieldErr = ""
}

func (cm *coreManager) moveStable(delta int) {
	if len(cm.stableTags) == 0 {
		return
	}
	cm.cursor = moveSelection(cm.cursor, len(cm.stableTags), delta)
	cm.fieldErr = ""
}

func (cm *coreManager) activateAction() tea.Cmd {
	cm.fieldErr = ""
	actions := cm.actions()
	idx, ok := selectedIndex(cm.cursor, len(actions))
	if !ok {
		return nil
	}
	cm.action = actions[idx].action
	if cm.action == coreActionLogs {
		return cm.loadLogsCmd()
	}
	if !cm.canApply() {
		cm.fieldErr = cm.applyBlocker()
		return nil
	}
	if cm.action == coreActionChangeStable {
		return cm.loadStableTagsCmd()
	}
	cm.phase = corePhaseConfirm
	return nil
}

// loadStableTagsCmd fetches the recent stable releases off the UI thread; the
// screen shows a loading state until coreStableTagsMsg arrives.
func (cm *coreManager) loadStableTagsCmd() tea.Cmd {
	cm.phase = corePhaseStableLoading
	mgr := cm.backendManager(nil)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tags, err := mgr.RecentStable(ctx, coreStableReleaseLimit)
		return coreStableTagsMsg{tags: tags, err: err}
	}
}

// loadLogsCmd reads journalctl off the UI thread.
func (cm *coreManager) loadLogsCmd() tea.Cmd {
	cm.phase = corePhaseLogsLoading
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logs, err := coreLogOutput(ctx, 200)
		return coreLogsMsg{logs: logs, err: err}
	}
}

func (cm *coreManager) canApply() bool { return hostCanApply(cm.host, cm.hostErr) }

func (cm *coreManager) applyBlocker() string {
	return hostApplyBlocker(cm.host, cm.hostErr,
		"core management must be run as root",
		"SELinux is enforcing; core management is blocked",
		"cannot apply core management action")
}

func (cm *coreManager) startRun() tea.Cmd {
	cm.phase = corePhaseRunning
	cm.resetRun(make(chan runMsg, 64))
	ch := cm.ch
	logs := &logWriter{ch: ch}
	uiAction := cm.action
	tag := cm.targetTag
	layout := coreUILayout()
	backendAction, _ := cm.backendAction()
	mgr := cm.backendManager(logs)
	go func() {
		if uiAction == coreActionChangeStable {
			err := changeFleetCoreRun(
				context.Background(), layout, tag, logs,
				runProgressSender(ch),
			)
			if err != nil {
				ch <- runMsg{done: true, err: err}
				return
			}
			ch <- runMsg{done: true, resultTag: tag}
			return
		}
		res, err := mgr.Run(context.Background(), backendAction, "")
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
	case corePhaseStableLoading:
		return flowTitle.Render(titleCore+" · Change version") + "\n\n" + dimStyle.Render("Fetching the latest stable releases…")
	case corePhaseStableSelect:
		return cm.stableView()
	case corePhaseConfirm:
		return cm.confirmView()
	case corePhaseRunning:
		return commandRunningView(cm, titleCore+" · Running")
	case corePhaseDone:
		if cm.runErr != nil {
			return commandFailedView(cm, titleCore+" action failed")
		}
		return flowOK.Render(titleCore+" action complete") + "\n\n" + cm.doneSummary()
	case corePhaseLogsLoading:
		return flowTitle.Render(titleCore+" · Logs") + "\n\n" + dimStyle.Render("Loading service logs…")
	case corePhaseLogs:
		return cm.logsView()
	default:
		return ""
	}
}

func (cm *coreManager) actionView() string {
	rows := []summaryLine{
		summaryRow("Current version (Hub)", or(cm.currentVersion, "not set up")),
		summaryRow("Hub service", or(cm.serviceState, "unknown")),
		summaryRow("Hub binary", coreUILayout().SingBoxBin),
		summaryRow("Hub config", coreUILayout().ConfigJSON),
	}
	if cm.nodesErr != nil {
		rows = append(rows, summaryRow("Spokes", "registry error: "+cm.nodesErr.Error()))
	} else if len(cm.nodes) == 0 {
		rows = append(rows, summaryRow("Spokes", "none registered"))
	} else {
		rows = append(rows, summaryBlank(), summaryText("Spokes (last authenticated health):"))
		for _, node := range cm.nodes {
			state := or(node.SingBoxVersion, "unknown")
			if !node.Installed {
				state = "not installed"
			}
			rows = append(rows, summaryIndentedRow(2, node.EffectiveAlias(), state))
		}
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render(titleCore) + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if cm.fieldErr != "" {
		b.WriteString(flowErr.Render(cm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(cm.actions(), cm.cursor))
	return b.String()
}

func (cm *coreManager) stableView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(titleCore+" · Change version") + "\n\n")
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
		rows = append(rows,
			summaryRow("Target release", cm.targetTag),
			summaryRow("Scope", fmt.Sprintf("Hub + %d installed Spoke(s)", cm.installedSpokeCount())),
		)
	}
	rows = append(rows, summaryBlank())
	if cm.isReplaceAction() {
		rows = append(rows, summaryText("Spokes are upgraded and verified first, then the Hub. If any node fails, every changed node is rolled back to its previous version."))
	} else {
		rows = append(rows, summaryText("Runs systemctl "+cm.systemctlAction()+" sing-box.service."))
	}
	return flowTitle.Render(titleCore+" · Confirm") + "\n\n" + renderSummary(rows)
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
	body := flowTitle.Render(titleCore+" · Logs") + "\n\n"
	if cm.svcLogs.logErr != nil {
		body += flowErr.Render(cm.svcLogs.logErr.Error()) + "\n\n"
	}
	if strings.TrimSpace(cm.svcLogs.logs) == "" {
		body += dimStyle.Render("no logs returned")
	} else {
		body += strings.Join(cm.svcLogs.visible(cm.width, cm.height), "\n")
	}
	return body
}

func (cm *coreManager) footerHints() []operationHint {
	switch cm.phase {
	case corePhaseAction:
		return actionFooterHints("Select")
	case corePhaseStableLoading, corePhaseLogsLoading:
		return nil
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
		{separator: true, label: "Version"},
		{action: coreActionChangeStable, label: "Change sing-box version"},
		{separator: true, label: "Service"},
		{action: coreActionStart, label: "Start sing-box.service"},
		{action: coreActionStop, label: "Stop sing-box.service"},
		{action: coreActionRestart, label: "Restart sing-box.service"},
		{action: coreActionLogs, label: "View sing-box.service logs"},
	}
}

func (cm *coreManager) installedSpokeCount() int {
	count := 0
	for _, node := range cm.nodes {
		if node.Installed {
			count++
		}
	}
	return count
}

func defaultChangeFleetCore(
	ctx context.Context,
	layout paths.Layout,
	target string,
	log io.Writer,
	progress func(deploy.Event),
) error {
	controller := &hubctl.Controller{
		Layout:          layout,
		Runner:          system.NewExecRunner(log),
		ExpectedVersion: toolVersion,
		Progress:        progress,
	}
	return controller.ChangeFleetCore(ctx, target, log)
}

func (cm *coreManager) actionLabel() string {
	return currentActionLabel(cm.actions(), cm.action)
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

func defaultCoreLogOutput(ctx context.Context, lines int) (string, error) {
	return journalctlOutput(ctx, system.SingBoxService, lines)
}
