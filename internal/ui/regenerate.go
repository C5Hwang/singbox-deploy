package ui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type regeneratePhase int

const (
	regeneratePhaseSelect regeneratePhase = iota
	regeneratePhaseConfirm
	regeneratePhaseRunning
	regeneratePhaseDone
)

// regenerateHubKey identifies the Hub row; spoke rows use their stable node ID.
const regenerateHubKey = "hub"

const keyToggleAll = "A"

var (
	regenerateUILayout   = paths.DefaultLayout
	detectRegenerateHost = system.DetectHost
	regenerateHubPresent = nodes.HubInstalled
	regenerateLoadNodes  = nodes.Load
	regenerateConfigsRun = defaultRegenerateConfigs
)

type regenerateTarget struct {
	key   string
	label string
	note  string
}

// regenerateManager rebuilds the sing-box and Nginx configs from the current
// templates on the nodes the operator picks, restarts both services and
// republishes subscriptions. Nothing is selected up front because the restart
// drops every live connection on a node.
type regenerateManager struct {
	phase regeneratePhase

	width  int
	height int

	host     system.Host
	hostErr  error
	loadErr  error
	fieldErr string

	targets   []regenerateTarget
	cursor    int
	selection map[string]bool
	// results is written by the run goroutine before it sends its done message
	// and read only after that message arrives, so the channel orders the two.
	results []hubctl.RegenerateResult

	commandRun
}

func newRegenerateManager() *regenerateManager {
	rm := &regenerateManager{phase: regeneratePhaseSelect, selection: map[string]bool{}, commandRun: newCommandRun()}
	rm.host, rm.hostErr = detectRegenerateHost()
	rm.targets, rm.loadErr = loadRegenerateTargets(regenerateUILayout())
	return rm
}

func loadRegenerateTargets(layout paths.Layout) ([]regenerateTarget, error) {
	var targets []regenerateTarget
	if regenerateHubPresent(layout) {
		targets = append(targets, regenerateTarget{key: regenerateHubKey, label: "Hub", note: "this server"})
	}
	list, err := regenerateLoadNodes(layout)
	if err != nil {
		return targets, err
	}
	for _, node := range list {
		if !node.Installed {
			continue
		}
		targets = append(targets, regenerateTarget{key: node.ID, label: node.EffectiveAlias(), note: node.Domain})
	}
	return targets, nil
}

func defaultRegenerateConfigs(
	ctx context.Context,
	layout paths.Layout,
	includeHub bool,
	spokeIDs []string,
	log io.Writer,
	progress func(deploy.Event),
) ([]hubctl.RegenerateResult, error) {
	ctrl := &hubctl.Controller{
		Layout:          layout,
		Runner:          system.NewExecRunner(log),
		ExpectedVersion: toolVersion,
		Progress:        progress,
	}
	return ctrl.RegenerateConfigs(ctx, includeHub, spokeIDs, log)
}

func (rm *regenerateManager) setSize(width, height int) {
	rm.width = width
	rm.height = height
	rm.commandRun.setSize(width, height)
}

func (rm *regenerateManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		rm.setSize(msg.Width, msg.Height)
	case runMsg:
		return rm.handleRun(msg), false
	case tea.KeyMsg:
		return rm.handleKey(msg)
	case tea.MouseMsg:
		rm.handleLogWheel(msg.Button, rm.phase == regeneratePhaseRunning || (rm.phase == regeneratePhaseDone && rm.runErr != nil))
	}
	return nil, false
}

func (rm *regenerateManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch rm.phase {
	case regeneratePhaseSelect:
		if strings.EqualFold(msg.String(), keyToggleAll) {
			rm.toggleAll()
			return nil, false
		}
		cmd, done, _ := handleSelectionKey(msg, selectionKeyHandlers{
			Move: func(delta int) {
				rm.cursor = moveSelection(rm.cursor, len(rm.targets), delta)
				rm.fieldErr = ""
			},
			Toggle: rm.toggle,
			Confirm: func() (tea.Cmd, bool) {
				if len(rm.selectedKeys()) == 0 {
					rm.fieldErr = "select at least one node"
					return nil, false
				}
				rm.fieldErr = ""
				rm.phase = regeneratePhaseConfirm
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		return cmd, done
	case regeneratePhaseConfirm:
		cmd, done, _ := handleSelectionKey(msg, selectionKeyHandlers{
			ConfirmYes: true,
			CancelNo:   true,
			Confirm:    func() (tea.Cmd, bool) { return rm.startRun(), false },
			Back: func() (tea.Cmd, bool) {
				rm.phase = regeneratePhaseSelect
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		return cmd, done
	case regeneratePhaseRunning:
		if msg.String() == "enter" && rm.runComplete {
			rm.phase = regeneratePhaseDone
		} else {
			rm.handleScrollKey(msg.String(), rm.logViewportHeight())
		}
	case regeneratePhaseDone:
		return rm.handleDoneKey(msg.String())
	}
	return nil, false
}

func (rm *regenerateManager) toggle() {
	idx, ok := selectedIndex(rm.cursor, len(rm.targets))
	if !ok {
		return
	}
	key := rm.targets[idx].key
	rm.selection[key] = !rm.selection[key]
	rm.fieldErr = ""
}

// toggleAll selects every node, or clears the selection when all already are.
func (rm *regenerateManager) toggleAll() {
	all := len(rm.selectedKeys()) == len(rm.targets)
	for _, t := range rm.targets {
		rm.selection[t.key] = !all
	}
	rm.fieldErr = ""
}

// selectedKeys returns the chosen rows in display order.
func (rm *regenerateManager) selectedKeys() []string {
	var keys []string
	for _, t := range rm.targets {
		if rm.selection[t.key] {
			keys = append(keys, t.key)
		}
	}
	return keys
}

func (rm *regenerateManager) startRun() tea.Cmd {
	if !hostCanApply(rm.host, rm.hostErr) {
		rm.fieldErr = hostApplyBlocker(rm.host, rm.hostErr,
			"regenerating configs must be run as root",
			"SELinux is enforcing; regenerating configs is blocked",
			"cannot regenerate configs")
		rm.phase = regeneratePhaseSelect
		return nil
	}
	includeHub := false
	var spokeIDs []string
	for _, key := range rm.selectedKeys() {
		if key == regenerateHubKey {
			includeHub = true
		} else {
			spokeIDs = append(spokeIDs, key)
		}
	}

	rm.phase = regeneratePhaseRunning
	rm.results = nil
	rm.resetRun(make(chan runMsg, 64))
	ch := rm.ch
	logs := &logWriter{ch: ch}
	layout := regenerateUILayout()
	go func() {
		results, err := regenerateConfigsRun(context.Background(), layout, includeHub, spokeIDs, logs, runProgressSender(ch))
		rm.results = results
		ch <- runMsg{done: true, err: err}
	}()
	return rm.waitForRun()
}

func (rm *regenerateManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(rm, msg) }

func (rm *regenerateManager) runState() *commandRun { return &rm.commandRun }

func (rm *regenerateManager) markRunFailed() { rm.phase = regeneratePhaseDone }

func (rm *regenerateManager) View() string {
	switch rm.phase {
	case regeneratePhaseSelect:
		return rm.selectView()
	case regeneratePhaseConfirm:
		return rm.confirmView()
	case regeneratePhaseRunning:
		return commandRunningView(rm, "Regenerate node configs · Running")
	case regeneratePhaseDone:
		if rm.runErr != nil {
			return flowErr.Render("Regenerate node configs · Some nodes failed") + "\n\n" +
				rm.resultSummary() + "\n\n" + rm.logView(max(1, rm.doneLogHeight()-len(rm.results)-3))
		}
		return flowOK.Render("Regenerate node configs · Complete") + "\n\n" + rm.resultSummary()
	default:
		return ""
	}
}

func (rm *regenerateManager) selectView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Regenerate node configs") + "\n\n")
	b.WriteString(renderSummary([]summaryLine{
		summaryText("Rebuild the sing-box and Nginx configs from the current templates,"),
		summaryText("restart both services and republish subscriptions."),
		summaryText("Restarting drops every live connection on that node."),
	}) + "\n\n")
	if rm.loadErr != nil {
		b.WriteString(flowErr.Render("cannot load spoke registry: "+rm.loadErr.Error()) + "\n\n")
	}
	if len(rm.targets) == 0 {
		b.WriteString(dimStyle.Render("No installed Hub or spoke nodes.") + "\n")
		return b.String()
	}
	for i, t := range rm.targets {
		row := checkbox(rm.selection[t.key]) + " " + t.label
		if t.note != "" {
			row += "  " + dimStyle.Render(t.note)
		}
		b.WriteString(cursorRow(row, i == rm.cursor) + "\n")
	}
	if rm.fieldErr != "" {
		b.WriteString("\n" + flowErr.Render(rm.fieldErr) + "\n")
	}
	return b.String()
}

func (rm *regenerateManager) confirmView() string {
	rows := []summaryLine{summaryText("Regenerate the sing-box and Nginx configs and restart both on:")}
	for _, t := range rm.targets {
		if rm.selection[t.key] {
			rows = append(rows, summaryIndentedText(2, t.label))
		}
	}
	rows = append(rows, summaryBlank(),
		summaryText("sing-box only switches after `sing-box check` accepts the new config,"),
		summaryText("and a failure on one node does not stop the others."))
	return flowTitle.Render("Regenerate node configs · Confirm") + "\n\n" + renderSummary(rows)
}

func (rm *regenerateManager) resultSummary() string {
	rows := make([]summaryLine, 0, len(rm.results))
	for _, r := range rm.results {
		state := "regenerated"
		if r.Err != nil {
			state = "failed: " + r.Err.Error()
		}
		rows = append(rows, summaryRow(r.Name, state))
	}
	return renderSummary(rows)
}

func (rm *regenerateManager) footerHints() []operationHint {
	switch rm.phase {
	case regeneratePhaseSelect:
		return []operationHint{hint(keySpace, "Toggle"), hint(keyToggleAll, "Toggle all"), hint(keyEnter, "Continue"), hint(keyMove, "Move"), hint(keyCancel, "Cancel")}
	case regeneratePhaseConfirm:
		return applyFooterHints("Regenerate")
	case regeneratePhaseRunning:
		return runningFooterHints(rm.runComplete)
	case regeneratePhaseDone:
		return doneFooterHints(rm.runErr != nil)
	default:
		return nil
	}
}
