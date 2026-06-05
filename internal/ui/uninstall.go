package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/install"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type uninstallPhase int

const (
	uninstallPhaseConfirm uninstallPhase = iota
	uninstallPhaseRunning
	uninstallPhaseDone
)

const (
	uninstallRuntimeKey       = "runtime"
	uninstallCertificatesKey  = "certificates"
	uninstallTrafficDBKey     = "traffic_db"
	uninstallSiteKey          = "site"
	uninstallSubscriptionsKey = "subscriptions"
)

var (
	uninstallUILayout   = paths.DefaultLayout
	detectUninstallHost = system.DetectHost
	uninstallRun        = install.Uninstall
)

type uninstallDataOption struct {
	key           string
	label         string
	path          string
	defaultDelete bool
}

type uninstallManager struct {
	phase uninstallPhase

	width  int
	height int

	host     system.Host
	hostErr  error
	fieldErr string

	cursor    int
	selection map[string]bool

	commandRun
}

func newUninstallManager() *uninstallManager {
	um := &uninstallManager{phase: uninstallPhaseConfirm, commandRun: newCommandRun()}
	um.host, um.hostErr = detectUninstallHost()
	um.selection = map[string]bool{}
	for _, opt := range uninstallOptions(uninstallUILayout()) {
		um.selection[opt.key] = opt.defaultDelete
	}
	return um
}

func (um *uninstallManager) setSize(width, height int) {
	um.width = width
	um.height = height
	um.commandRun.setSize(width, height)
}

func (um *uninstallManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		um.setSize(msg.Width, msg.Height)
	case runMsg:
		return um.handleRun(msg), false
	case tea.KeyMsg:
		return um.handleKey(msg)
	case tea.MouseMsg:
		return um.handleMouse(msg), false
	}
	return nil, false
}

func (um *uninstallManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch um.phase {
	case uninstallPhaseConfirm:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move:       um.moveOption,
			Toggle:     um.toggleOption,
			ConfirmYes: true,
			CancelNo:   true,
			Confirm: func() (tea.Cmd, bool) {
				return um.startRun(), false
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case uninstallPhaseRunning:
		switch msg.String() {
		case "enter":
			if um.runComplete {
				um.phase = uninstallPhaseDone
			}
		case "up", "k":
			um.scrollLog(1, um.logViewportHeight())
		case "down", "j":
			um.scrollLog(-1, um.logViewportHeight())
		case "pgup":
			um.scrollLog(um.logViewportHeight(), um.logViewportHeight())
		case "pgdown":
			um.scrollLog(-um.logViewportHeight(), um.logViewportHeight())
		case "home":
			um.logScroll = um.maxLogScroll(um.logViewportHeight())
		case "end":
			um.logScroll = 0
		}
	case uninstallPhaseDone:
		if um.runErr != nil {
			switch msg.String() {
			case "up", "k":
				um.scrollLog(1, um.doneLogHeight())
				return nil, false
			case "down", "j":
				um.scrollLog(-1, um.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (um *uninstallManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if um.phase == uninstallPhaseRunning || (um.phase == uninstallPhaseDone && um.runErr != nil) {
			um.scrollLog(3, um.logViewportHeight())
		}
	case tea.MouseButtonWheelDown:
		if um.phase == uninstallPhaseRunning || (um.phase == uninstallPhaseDone && um.runErr != nil) {
			um.scrollLog(-3, um.logViewportHeight())
		}
	}
	return nil
}

func (um *uninstallManager) moveOption(delta int) {
	options := uninstallOptions(uninstallUILayout())
	um.cursor = moveSelection(um.cursor, len(options), delta)
	um.fieldErr = ""
}

func (um *uninstallManager) toggleOption() {
	options := uninstallOptions(uninstallUILayout())
	if len(options) == 0 {
		return
	}
	idx, ok := selectedIndex(um.cursor, len(options))
	if !ok {
		return
	}
	key := options[idx].key
	um.selection[key] = !um.selection[key]
	um.fieldErr = ""
}

func (um *uninstallManager) canApply() bool {
	return um.hostErr == nil && um.host.IsRoot && um.host.Supported() && !um.host.SELinux
}

func (um *uninstallManager) applyBlocker() string {
	if um.hostErr != nil {
		return "failed to detect host: " + um.hostErr.Error()
	}
	if !um.host.IsRoot {
		return "uninstall must be run as root"
	}
	if !um.host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", um.host.OS.Family, um.host.Arch)
	}
	if um.host.SELinux {
		return "SELinux is enforcing; uninstall is blocked"
	}
	return "cannot run uninstall"
}

func (um *uninstallManager) startRun() tea.Cmd {
	if !um.canApply() {
		um.fieldErr = um.applyBlocker()
		return nil
	}
	um.phase = uninstallPhaseRunning
	um.resetRun(make(chan runMsg, 64))
	ch := um.ch
	logs := &logWriter{ch: ch}
	opts := install.UninstallOptions{
		Layout:              uninstallUILayout(),
		Runner:              system.NewExecRunner(logs),
		DeleteRuntime:       um.selected(uninstallRuntimeKey),
		DeleteCertificates:  um.selected(uninstallCertificatesKey),
		DeleteTrafficDB:     um.selected(uninstallTrafficDBKey),
		DeleteSite:          um.selected(uninstallSiteKey),
		DeleteSubscriptions: um.selected(uninstallSubscriptionsKey),
		Progress: func(e install.Event) {
			ev := e
			ch <- runMsg{event: &ev}
		},
	}
	go func() {
		err := uninstallRun(context.Background(), opts)
		ch <- runMsg{done: true, err: err}
	}()
	return um.waitForRun()
}

func (um *uninstallManager) selected(key string) bool { return um.selection[key] }

func (um *uninstallManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(um, msg) }

func (um *uninstallManager) runState() *commandRun { return &um.commandRun }

func (um *uninstallManager) markRunFailed() { um.phase = uninstallPhaseDone }

func (um *uninstallManager) View() string {
	switch um.phase {
	case uninstallPhaseConfirm:
		return um.confirmView()
	case uninstallPhaseRunning:
		return commandRunningView(um, "Uninstall · Running")
	case uninstallPhaseDone:
		if um.runErr != nil {
			return commandFailedView(um, "Uninstall failed")
		}
		return flowOK.Render("Uninstall complete") + "\n\n" + um.doneSummary()
	default:
		return ""
	}
}

func (um *uninstallManager) confirmView() string {
	layout := uninstallUILayout()
	rows := []summaryLine{
		summaryText("Always remove managed integration points:"),
		summaryIndentedRow(2, "Services", system.SingBoxService+", "+system.MonitorService),
		summaryIndentedRow(2, "ACME renewal", system.CertRenewTimer+" or managed cron entry"),
		summaryIndentedRow(2, "Nginx config", "/etc/nginx/conf.d/singbox-deploy.conf"),
		summaryIndentedRow(2, "Unrelated Nginx configs", "kept"),
		summaryBlank(),
		summaryText("Choose data to delete under " + layout.Root + ":"),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render("Uninstall · Confirm") + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if um.fieldErr != "" {
		b.WriteString(flowErr.Render(um.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, opt := range uninstallOptions(layout) {
		row := um.optionRow(opt)
		if i == um.cursor {
			row = selStyle.Render("> " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (um *uninstallManager) optionRow(opt uninstallDataOption) string {
	mark := "[ ]"
	action := "Keep"
	if um.selected(opt.key) {
		mark = "[x]"
		action = "Delete"
	}
	return fmt.Sprintf("%s %-28s %s  %s", mark, opt.label, action, dimStyle.Render(opt.path))
}

func (um *uninstallManager) doneSummary() string {
	rows := []summaryLine{
		summaryRow("Services", "removed"),
		summaryRow("ACME renewal", "removed"),
		summaryRow("Managed Nginx config", "removed"),
	}
	for _, opt := range uninstallOptions(uninstallUILayout()) {
		state := "kept"
		if um.selected(opt.key) {
			state = "deleted"
		}
		rows = append(rows, summaryRow(opt.label, state))
	}
	return renderSummary(rows)
}

func (um *uninstallManager) footerHints() []operationHint {
	switch um.phase {
	case uninstallPhaseConfirm:
		return []operationHint{hint(keyMove, "Move"), hint(keySpace, "Toggle"), hint(keyEnterYes, "Uninstall"), hint("Esc/N/Q", "Cancel")}
	case uninstallPhaseRunning:
		return runningFooterHints(um.runComplete)
	case uninstallPhaseDone:
		return doneFooterHints(um.runErr != nil)
	default:
		return nil
	}
}

func uninstallOptions(layout paths.Layout) []uninstallDataOption {
	return []uninstallDataOption{
		{key: uninstallRuntimeKey, label: "Runtime state/config", path: layout.StateDir + " and " + filepath.Dir(layout.SingBoxBin), defaultDelete: true},
		{key: uninstallCertificatesKey, label: "Certificates", path: layout.TLSDir},
		{key: uninstallTrafficDBKey, label: "SQLite traffic database", path: layout.TrafficDB},
		{key: uninstallSiteKey, label: "Masquerade site files", path: layout.WebRoot},
		{key: uninstallSubscriptionsKey, label: "Subscription outputs", path: layout.SubscribeDir},
	}
}
