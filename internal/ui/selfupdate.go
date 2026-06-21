package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/selfupdate"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type selfUpdatePhase int

const (
	selfUpdatePhaseCheck selfUpdatePhase = iota
	selfUpdatePhaseConfirm
	selfUpdatePhaseRunning
	selfUpdatePhaseDone
)

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
		phase:          selfUpdatePhaseCheck,
		currentVersion: toolVersion,
		commandRun:     newCommandRun(),
	}
	sm.host, sm.hostErr = detectSelfUpdateHost()
	sm.checkLatest()
	return sm
}

func (sm *selfUpdateManager) setSize(width, height int) {
	sm.width = width
	sm.height = height
	sm.commandRun.setSize(width, height)
}

func (sm *selfUpdateManager) checkLatest() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr := sm.backendManager(nil)
	tag, err := mgr.CheckLatest(ctx)
	if err != nil {
		sm.checkErr = "fetch latest release: " + err.Error()
		return
	}
	sm.latestTag = tag
	sm.upToDate = sm.latestTag == sm.currentVersion || sm.latestTag == "v"+sm.currentVersion
}

func (sm *selfUpdateManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sm.setSize(msg.Width, msg.Height)
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
		switch msg.String() {
		case "enter":
			if sm.runComplete {
				sm.phase = selfUpdatePhaseDone
			}
		case "up", "k":
			sm.scrollLog(1, sm.logViewportHeight())
		case "down", "j":
			sm.scrollLog(-1, sm.logViewportHeight())
		case "pgup":
			sm.scrollLog(sm.logViewportHeight(), sm.logViewportHeight())
		case "pgdown":
			sm.scrollLog(-sm.logViewportHeight(), sm.logViewportHeight())
		case "home":
			sm.logScroll = sm.maxLogScroll(sm.logViewportHeight())
		case "end":
			sm.logScroll = 0
		}
	case selfUpdatePhaseDone:
		if sm.runErr != nil {
			switch msg.String() {
			case "up", "k":
				sm.scrollLog(1, sm.doneLogHeight())
				return nil, false
			case "down", "j":
				sm.scrollLog(-1, sm.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (sm *selfUpdateManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if sm.phase == selfUpdatePhaseRunning || (sm.phase == selfUpdatePhaseDone && sm.runErr != nil) {
			sm.scrollLog(3, sm.logViewportHeight())
		}
	case tea.MouseButtonWheelDown:
		if sm.phase == selfUpdatePhaseRunning || (sm.phase == selfUpdatePhaseDone && sm.runErr != nil) {
			sm.scrollLog(-3, sm.logViewportHeight())
		}
	}
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
	}
	return mgr
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
