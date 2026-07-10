package ui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/assets/agentbin"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/selfupdate"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type selfUpdatePhase int

const (
	selfUpdatePhaseChecking selfUpdatePhase = iota
	selfUpdatePhaseCheck
	selfUpdatePhaseConfirm
	selfUpdatePhaseRunning
	selfUpdatePhaseDone
)

// selfUpdateCheckedMsg carries the result of the async latest-release lookup.
type selfUpdateCheckedMsg struct {
	tag string
	err error
}

var (
	detectSelfUpdateHost = system.DetectHost
	selfUpdateRelease    = func() *release.Client { return release.NewClient("", nil) }
)

type selfUpdateManager struct {
	phase selfUpdatePhase

	width  int
	height int

	host    system.Host
	hostErr error

	currentVersion string
	latestTag      string
	checkErr       string
	upToDate       bool
	resultTag      string

	commandRun
}

func newSelfUpdateManager() *selfUpdateManager {
	sm := &selfUpdateManager{
		phase:          selfUpdatePhaseChecking,
		currentVersion: toolVersion,
		commandRun:     newCommandRun(),
	}
	sm.host, sm.hostErr = detectSelfUpdateHost()
	return sm
}

// checkCmd looks up the latest release off the UI thread so opening the
// self-update screen does not freeze the whole TUI on a slow network.
func (sm *selfUpdateManager) checkCmd() tea.Cmd {
	mgr := sm.backendManager(nil)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tag, err := mgr.CheckLatest(ctx)
		return selfUpdateCheckedMsg{tag: tag, err: err}
	}
}

func (sm *selfUpdateManager) setSize(width, height int) {
	sm.width = width
	sm.height = height
	sm.commandRun.setSize(width, height)
}

func (sm *selfUpdateManager) applyCheckResult(msg selfUpdateCheckedMsg) {
	if msg.err != nil {
		sm.checkErr = "fetch latest release: " + msg.err.Error()
	} else {
		sm.latestTag = msg.tag
		sm.upToDate = sm.latestTag == sm.currentVersion || sm.latestTag == "v"+sm.currentVersion
	}
	sm.phase = selfUpdatePhaseCheck
}

func (sm *selfUpdateManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sm.setSize(msg.Width, msg.Height)
	case selfUpdateCheckedMsg:
		sm.applyCheckResult(msg)
	case runMsg:
		return sm.handleRun(msg), false
	case tea.KeyMsg:
		return sm.handleKey(msg)
	case tea.MouseMsg:
		return sm.handleMouse(msg), false
	}
	return nil, false
}

func (sm *selfUpdateManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch sm.phase {
	case selfUpdatePhaseChecking:
		if isSelectionCancelKey(msg) || msg.String() == "esc" {
			return nil, true
		}
	case selfUpdatePhaseCheck:
		if sm.checkErr != "" || sm.upToDate {
			return nil, true
		}
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			sm.phase = selfUpdatePhaseConfirm
		case isSelectionCancelKey(msg), isSelectionNoKey(msg):
			return nil, true
		}
	case selfUpdatePhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return sm.startRun(), false
		case isSelectionBackKey(msg):
			sm.phase = selfUpdatePhaseCheck
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case selfUpdatePhaseRunning:
		if msg.String() == "enter" && sm.runComplete {
			sm.phase = selfUpdatePhaseDone
		} else {
			sm.handleScrollKey(msg.String(), sm.logViewportHeight())
		}
	case selfUpdatePhaseDone:
		return sm.handleDoneKey(msg.String())
	}
	return nil, false
}

func (sm *selfUpdateManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	sm.handleLogWheel(msg.Button, sm.phase == selfUpdatePhaseRunning || (sm.phase == selfUpdatePhaseDone && sm.runErr != nil))
	return nil
}

func (sm *selfUpdateManager) canApply() bool {
	return sm.hostErr == nil && sm.host.IsRoot
}

func (sm *selfUpdateManager) applyBlocker() string {
	if sm.hostErr != nil {
		return "failed to detect host: " + sm.hostErr.Error()
	}
	if !sm.host.IsRoot {
		return "self-update must be run as root"
	}
	return "cannot run self-update"
}

func (sm *selfUpdateManager) startRun() tea.Cmd {
	if !sm.canApply() {
		sm.checkErr = sm.applyBlocker()
		sm.phase = selfUpdatePhaseCheck
		return nil
	}
	sm.phase = selfUpdatePhaseRunning
	sm.resetRun(make(chan runMsg, 64))
	ch := sm.ch
	logs := &logWriter{ch: ch}
	mgr := sm.backendManager(logs)
	tag := sm.latestTag
	go func() {
		res, err := mgr.Run(context.Background(), tag)
		ch <- runMsg{done: true, err: err, resultTag: res.Tag}
	}()
	return sm.waitForRun()
}

func (sm *selfUpdateManager) backendManager(logs *logWriter) *selfupdate.Manager {
	mgr := &selfupdate.Manager{
		Releases: selfUpdateRelease(),
		Version:  sm.currentVersion,
		GOARCH:   sm.host.Arch,
	}
	if logs != nil {
		mgr.Progress = func(e deploy.Event) {
			ev := e
			logs.ch <- runMsg{event: &ev}
		}
		mgr.BeforeReplace = func(ctx context.Context, candidatePath, targetVersion string) error {
			return upgradeSpokeAgentsBeforeHub(ctx, candidatePath, targetVersion, logs)
		}
		mgr.ReplaceFailed = func(_ context.Context, _ string) error {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			return restoreSpokeAgentsToCurrentHub(rollbackCtx, logs)
		}
	}
	return mgr
}

func upgradeSpokeAgentsBeforeHub(ctx context.Context, candidatePath, targetVersion string, logs *logWriter) error {
	layout := paths.DefaultLayout()
	list, err := nodes.Load(layout)
	if err != nil {
		return err
	}
	cache := map[string][]byte{}
	loadAgent := func(arch string) ([]byte, error) {
		if binary, ok := cache[arch]; ok {
			return binary, nil
		}
		cmd := exec.CommandContext(ctx, candidatePath, "agent", "export", "--arch", arch)
		binary, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("export %s agent from candidate hub: %w", arch, err)
		}
		if len(binary) == 0 {
			return nil, fmt.Errorf("candidate hub exported an empty %s agent", arch)
		}
		cache[arch] = binary
		return binary, nil
	}
	ctrl := &hubctl.Controller{
		Layout:          layout,
		ExpectedVersion: targetVersion,
		AgentBinary:     loadAgent,
	}
	var upgraded []nodes.Node
	for _, node := range list {
		if !node.Installed {
			continue
		}
		fmt.Fprintf(logs, "upgrading %s agent over WireGuard before replacing the hub...\n", node.EffectiveAlias())
		if _, err := ctrl.CheckHealth(ctx, node, logs); err != nil {
			upgradeErr := fmt.Errorf("upgrade %s: %w", node.EffectiveAlias(), err)
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			rollbackErr := restoreSelectedSpokeAgents(rollbackCtx, upgraded, logs)
			cancel()
			return errors.Join(upgradeErr, rollbackErr)
		}
		upgraded = append(upgraded, node)
	}
	return nil
}

// restoreSpokeAgentsToCurrentHub is used when the candidate agents were all
// upgraded but the atomic hub-binary rename failed. The old process still owns
// the current embedded agents, so it can restore version equality immediately.
func restoreSpokeAgentsToCurrentHub(ctx context.Context, logs *logWriter) error {
	list, err := nodes.Load(paths.DefaultLayout())
	if err != nil {
		return err
	}
	return restoreSelectedSpokeAgents(ctx, list, logs)
}

func restoreSelectedSpokeAgents(ctx context.Context, list []nodes.Node, logs *logWriter) error {
	ctrl := &hubctl.Controller{
		Layout:          paths.DefaultLayout(),
		ExpectedVersion: toolVersion,
		AgentBinary:     agentbin.Binary,
	}
	var errs []error
	for _, node := range list {
		if !node.Installed {
			continue
		}
		fmt.Fprintf(logs, "restoring %s agent to hub version %s...\n", node.EffectiveAlias(), toolVersion)
		if _, err := ctrl.CheckHealth(ctx, node, logs); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", node.EffectiveAlias(), err))
		}
	}
	return errors.Join(errs...)
}

func (sm *selfUpdateManager) handleRun(msg runMsg) tea.Cmd {
	if msg.resultTag != "" {
		sm.resultTag = msg.resultTag
	}
	return handleCommandRun(sm, msg)
}

func (sm *selfUpdateManager) runState() *commandRun { return &sm.commandRun }

func (sm *selfUpdateManager) markRunFailed() { sm.phase = selfUpdatePhaseDone }

func (sm *selfUpdateManager) View() string {
	switch sm.phase {
	case selfUpdatePhaseChecking:
		return flowTitle.Render("Self-update") + "\n\n" + dimStyle.Render("Checking for the latest release…")
	case selfUpdatePhaseCheck:
		return sm.checkView()
	case selfUpdatePhaseConfirm:
		return sm.confirmView()
	case selfUpdatePhaseRunning:
		return commandRunningView(sm, "Self-update · Running")
	case selfUpdatePhaseDone:
		if sm.runErr != nil {
			return commandFailedView(sm, "Self-update failed")
		}
		return sm.doneView()
	default:
		return ""
	}
}

func (sm *selfUpdateManager) checkView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Self-update") + "\n\n")
	rows := []summaryLine{
		summaryRow("Current version", or(sm.currentVersion, "dev")),
	}
	if sm.checkErr != "" {
		b.WriteString(renderSummary(rows) + "\n\n")
		b.WriteString(flowErr.Render(sm.checkErr) + "\n")
		return b.String()
	}
	rows = append(rows, summaryRow("Latest version", sm.latestTag))
	b.WriteString(renderSummary(rows) + "\n\n")
	if sm.upToDate {
		b.WriteString(flowOK.Render("Already up to date") + "\n")
	} else {
		b.WriteString(fmt.Sprintf("Update available: %s → %s\n", sm.currentVersion, sm.latestTag))
	}
	return b.String()
}

func (sm *selfUpdateManager) confirmView() string {
	rows := []summaryLine{
		summaryRow("Current version", or(sm.currentVersion, "dev")),
		summaryRow("Target version", sm.latestTag),
		summaryBlank(),
		summaryText("This will download the new release and replace the current binary. Please restart singbox-deploy after the update completes."),
	}
	return flowTitle.Render("Self-update · Confirm") + "\n\n" + renderSummary(rows)
}

func (sm *selfUpdateManager) doneView() string {
	rows := []summaryLine{
		summaryRow("Previous version", or(sm.currentVersion, "dev")),
		summaryRow("Updated to", sm.resultTag),
	}
	return flowOK.Render("Self-update complete") + "\n\n" +
		renderSummary(rows) + "\n\n" +
		summaryInfo.Render("Please restart singbox-deploy to use the new version.") + "\n"
}

func (sm *selfUpdateManager) footerHints() []operationHint {
	switch sm.phase {
	case selfUpdatePhaseChecking:
		return nil
	case selfUpdatePhaseCheck:
		if sm.checkErr != "" || sm.upToDate {
			return doneFooterHints(sm.checkErr != "")
		}
		return applyFooterHints("Update")
	case selfUpdatePhaseConfirm:
		return applyFooterHints("Apply")
	case selfUpdatePhaseRunning:
		return runningFooterHints(sm.runComplete)
	case selfUpdatePhaseDone:
		return doneFooterHints(sm.runErr != nil)
	default:
		return nil
	}
}
