package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type ipBanPhase int

const (
	ipBanPhaseMenu ipBanPhase = iota
	ipBanPhasePick
	ipBanPhaseForm
	ipBanPhaseConfirm
	ipBanPhaseRunning
	ipBanPhaseDone
)

type ipBanAction int

const (
	ipBanActionAdd ipBanAction = iota
	ipBanActionEdit
	ipBanActionRemove
	ipBanActionReapply
)

const (
	ipBanFieldAddress = "ban_address"
	ipBanFieldNodes   = "ban_nodes"
)

var (
	ipBanUILayout = paths.DefaultLayout
	// ipBanLoadState reads the ban registry, the relay links and the fleet the
	// screen is built from. It is one seam so a test can describe all of it.
	ipBanLoadState = loadIPBanState
	// ipBanApplyChange performs the registry write and the fleet-wide
	// enforcement as one recorded operation.
	ipBanApplyChange = applyIPBanChange
)

// ipBanChange is one edit to the ban registry. A change with an empty Address
// edits nothing and only re-pushes every node's rules.
type ipBanChange struct {
	Action  ipBanAction
	Address string
	Nodes   []string
}

type ipBanManager struct {
	phase  ipBanPhase
	action ipBanAction

	width  int
	height int

	bans      []ipban.Ban
	links     []relaylinks.Link
	endpoints []hubctl.RelayEndpoint
	loadErr   error
	fieldErr  string

	cursor int
	// picked is the address of the ban being edited or removed.
	picked string
	// address and nodes are what the confirm screen is about to commit.
	address string
	nodes   []string
	// previous is the edited ban's node list before the change, so the confirm
	// and summary screens can say what the edit adds and lifts.
	previous []string

	form    parameterForm
	options []groupMember

	commandRun
}

func newIPBanManager() *ipBanManager {
	m := &ipBanManager{phase: ipBanPhaseMenu, cursor: 1, form: newParameterForm(nil), commandRun: newCommandRun()}
	m.reload()
	return m
}

func (m *ipBanManager) reload() {
	m.bans, m.links, m.endpoints, m.loadErr = ipBanLoadState(ipBanUILayout())
}

func loadIPBanState(layout paths.Layout) ([]ipban.Ban, []relaylinks.Link, []hubctl.RelayEndpoint, error) {
	bans, err := ipban.LoadBans(layout)
	if err != nil {
		return nil, nil, nil, err
	}
	links, err := relaylinks.Load(layout)
	if err != nil {
		return nil, nil, nil, err
	}
	endpoints, err := (&hubctl.Controller{Layout: layout, ExpectedVersion: toolVersion}).RelayEndpoints()
	if err != nil {
		return nil, nil, nil, err
	}
	return bans, links, endpoints, nil
}

func (m *ipBanManager) setSize(width, height int) {
	m.width = width
	m.height = height
	m.form.setSize(width, height)
	m.commandRun.setSize(width, height)
}

func (m *ipBanManager) actions() []actionItem[ipBanAction] {
	items := []actionItem[ipBanAction]{
		{separator: true, label: "Bans"},
		{action: ipBanActionAdd, label: "Ban an IP"},
	}
	if len(m.bans) > 0 {
		items = append(items,
			actionItem[ipBanAction]{action: ipBanActionEdit, label: "Change banned nodes"},
			actionItem[ipBanAction]{action: ipBanActionRemove, label: "Lift a ban"},
			actionItem[ipBanAction]{separator: true, label: "Maintenance"},
			actionItem[ipBanAction]{action: ipBanActionReapply, label: "Reapply bans on every node"},
		)
	}
	return items
}

func (m *ipBanManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
	case runMsg:
		return m.handleRun(msg), false
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		m.handleLogWheel(msg.Button, m.phase == ipBanPhaseRunning || (m.phase == ipBanPhaseDone && m.runErr != nil))
	default:
		if m.phase == ipBanPhaseForm {
			return m.form.updateInput(msg), false
		}
	}
	return nil, false
}

func (m *ipBanManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch m.phase {
	case ipBanPhaseMenu:
		return m.handleMenuKey(msg)
	case ipBanPhasePick:
		return m.handlePickKey(msg)
	case ipBanPhaseForm:
		cmd, _, _ := m.form.handleKey(msg, parameterFormKeyHandlers{
			Complete: m.completeForm,
			Back: func() {
				if !m.form.previousField() {
					m.back()
				}
			},
			Cancel: func() (tea.Cmd, bool) { m.toMenu(); return nil, false },
		})
		return cmd, false
	case ipBanPhaseConfirm:
		return m.handleConfirmKey(msg)
	case ipBanPhaseRunning:
		if msg.String() == "enter" && m.runComplete {
			m.phase = ipBanPhaseDone
		} else {
			m.handleScrollKey(msg.String(), m.logViewportHeight())
		}
	case ipBanPhaseDone:
		return m.handleDoneKey(msg.String())
	}
	return nil, false
}

func (m *ipBanManager) handleMenuKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	items := m.actions()
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		Move: func(delta int) {
			m.cursor = moveActionCursor(m.cursor, items, delta)
			m.fieldErr = ""
		},
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.cursor, len(items)); ok && !items[idx].separator {
				m.startAction(items[idx].action)
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { return nil, true },
	})
	if handled {
		return cmd, done
	}
	return nil, false
}

func (m *ipBanManager) startAction(action ipBanAction) {
	m.action = action
	m.fieldErr = ""
	m.picked, m.address, m.nodes, m.previous = "", "", nil, nil
	if m.loadErr != nil {
		m.fieldErr = m.loadErr.Error()
		return
	}
	if len(m.endpoints) == 0 {
		m.fieldErr = "the hub is not installed yet, so there is no node to ban an address from"
		return
	}
	switch action {
	case ipBanActionAdd:
		m.beginForm(true, nil)
	case ipBanActionEdit, ipBanActionRemove:
		m.cursor = 0
		m.phase = ipBanPhasePick
	case ipBanActionReapply:
		m.phase = ipBanPhaseConfirm
	}
}

func (m *ipBanManager) handlePickKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		Move: func(delta int) {
			m.cursor = moveSelection(m.cursor, len(m.bans), delta)
			m.fieldErr = ""
		},
		Confirm: func() (tea.Cmd, bool) {
			idx, ok := selectedIndex(m.cursor, len(m.bans))
			if !ok {
				return nil, false
			}
			ban := m.bans[idx]
			m.picked = ban.Address
			m.previous = append([]string(nil), ban.Nodes...)
			if m.action == ipBanActionRemove {
				m.address, m.nodes = ban.Address, append([]string(nil), ban.Nodes...)
				m.phase = ipBanPhaseConfirm
				return nil, false
			}
			m.beginForm(false, ban.Nodes)
			return nil, false
		},
		Back:   func() (tea.Cmd, bool) { m.toMenu(); return nil, false },
		Cancel: func() (tea.Cmd, bool) { m.toMenu(); return nil, false },
	})
	if handled {
		return cmd, done
	}
	return nil, false
}

// nodeOptions lists every fleet member a ban can name, the hub first, as
// labels the multi-choice form can round-trip.
func (m *ipBanManager) nodeOptions() []groupMember {
	options := make([]groupMember, 0, len(m.endpoints))
	for _, endpoint := range m.endpoints {
		label := m.nodeName(endpoint.ID)
		if endpoint.Domain != "" {
			label += " (" + endpoint.Domain + ")"
		}
		if link, fronted := relaylinks.Find(m.links, endpoint.ID); fronted {
			label += " via " + m.nodeName(link.RelayID)
		}
		options = append(options, groupMember{id: endpoint.ID, label: optionLabel(label)})
	}
	return options
}

func (m *ipBanManager) beginForm(withAddress bool, selected []string) {
	m.options = m.nodeOptions()
	var fields []field
	if withAddress {
		fields = append(fields, field{
			key:   ipBanFieldAddress,
			label: "IP address to ban",
			note:  "A single IPv4 or IPv6 address, or a CIDR prefix such as 203.0.113.0/24.",
		})
	}
	fields = append(fields, field{
		key:     ipBanFieldNodes,
		label:   "Nodes this address may not use",
		def:     groupMemberValue(m.options, selected),
		options: groupMemberLabels(m.options),
		multi:   true,
		// The ticked boxes already show the current list.
		hideDefault: true,
		note: "Only proxy ports are blocked; the cover site and subscription stay reachable.\n" +
			"A node behind a relay is blocked through that relay too, while the relay itself stays usable unless ticked.",
	})
	m.form.begin(fields, nil, m.validateField)
	m.phase = ipBanPhaseForm
}

func (m *ipBanManager) validateField(f field, value string, _ map[string]string) error {
	switch f.key {
	case ipBanFieldAddress:
		canonical, err := ipban.CanonicalAddress(value)
		if err != nil {
			return err
		}
		for _, ban := range m.bans {
			if ban.Address == canonical {
				return fmt.Errorf("%s is already banned; use Change banned nodes instead", canonical)
			}
		}
	case ipBanFieldNodes:
		if len(groupMemberIDs(m.options, value)) == 0 {
			return fmt.Errorf("tick at least one node; use Lift a ban to unban the address everywhere")
		}
	}
	return nil
}

func (m *ipBanManager) completeForm() {
	if m.action == ipBanActionAdd {
		canonical, err := ipban.CanonicalAddress(m.form.values[ipBanFieldAddress])
		if err != nil {
			m.fieldErr = err.Error()
			return
		}
		m.address = canonical
	} else {
		m.address = m.picked
	}
	m.nodes = groupMemberIDs(m.options, m.form.values[ipBanFieldNodes])
	m.phase = ipBanPhaseConfirm
}

func (m *ipBanManager) handleConfirmKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		ConfirmYes: true,
		CancelNo:   true,
		Confirm:    func() (tea.Cmd, bool) { return m.startRun(), false },
		Back:       func() (tea.Cmd, bool) { m.back(); return nil, false },
		Cancel:     func() (tea.Cmd, bool) { m.back(); return nil, false },
	})
	if handled {
		return cmd, done
	}
	return nil, false
}

func (m *ipBanManager) back() {
	m.fieldErr = ""
	switch {
	case m.phase == ipBanPhaseConfirm && (m.action == ipBanActionAdd || m.action == ipBanActionEdit):
		m.form.backToLastField()
		m.phase = ipBanPhaseForm
	case m.phase == ipBanPhaseConfirm && m.action == ipBanActionRemove,
		m.phase == ipBanPhaseForm && m.action == ipBanActionEdit:
		m.phase = ipBanPhasePick
	default:
		m.toMenu()
	}
}

func (m *ipBanManager) toMenu() {
	m.fieldErr = ""
	m.cursor = 1
	for i, item := range m.actions() {
		if !item.separator && item.action == m.action {
			m.cursor = i
		}
	}
	m.phase = ipBanPhaseMenu
}

func (m *ipBanManager) change() ipBanChange {
	return ipBanChange{Action: m.action, Address: m.address, Nodes: append([]string(nil), m.nodes...)}
}

func (m *ipBanManager) startRun() tea.Cmd {
	m.phase = ipBanPhaseRunning
	m.resetRun(make(chan runMsg, 64))
	ch := m.ch
	logs := &logWriter{ch: ch}
	layout := ipBanUILayout()
	change := m.change()
	go func() {
		err := ipBanApplyChange(context.Background(), layout, change, logs, runProgressSender(ch))
		ch <- runMsg{done: true, err: err}
	}()
	return m.waitForRun()
}

// applyIPBanChange writes the registry and then converges every node on it.
//
// The registry write is its own step, so a spoke that cannot be reached does
// not make the edit look rejected: the stored bans are what the monitor's
// refresh keeps retrying, and what Reapply pushes again on demand.
func applyIPBanChange(ctx context.Context, layout paths.Layout, change ipBanChange, log io.Writer, progress func(deploy.Event)) error {
	ctrl := &hubctl.Controller{
		Layout:          layout,
		Runner:          system.NewExecRunner(log),
		ExpectedVersion: toolVersion,
	}
	var steps []deploy.Step
	if change.Action != ipBanActionReapply {
		steps = append(steps, deploy.Step{
			Label:  "Ban registry",
			Detail: "record which address is banned from which node",
			Run: func(context.Context) error {
				switch change.Action {
				case ipBanActionAdd:
					return ipban.AddBan(layout, ipban.Ban{Address: change.Address, Nodes: change.Nodes})
				case ipBanActionEdit:
					return ipban.SetBan(layout, ipban.Ban{Address: change.Address, Nodes: change.Nodes})
				case ipBanActionRemove:
					return ipban.RemoveBan(layout, change.Address)
				}
				return nil
			},
		})
	}
	steps = append(steps, deploy.Step{
		Label:  "Enforcement",
		Detail: "install the drop rules on every node whose bans changed",
		Run: func(ctx context.Context) error {
			reconcile := ctrl.ReconcileIPBans
			if change.Action == ipBanActionReapply {
				reconcile = ctrl.ForceReconcileIPBans
			}
			if err := reconcile(ctx, log); err != nil {
				return &ipBanEnforcementError{err: err}
			}
			return nil
		},
	})
	return deploy.RunSteps(ctx, progress, steps)
}

// ipBanEnforcementError marks a failure to push rules to some node after the
// registry was already written: the change stands, only its enforcement lags.
type ipBanEnforcementError struct{ err error }

func (e *ipBanEnforcementError) Error() string { return e.err.Error() }
func (e *ipBanEnforcementError) Unwrap() error { return e.err }

func (m *ipBanManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(m, msg) }

func (m *ipBanManager) runState() *commandRun { return &m.commandRun }

func (m *ipBanManager) markRunFailed() { m.phase = ipBanPhaseDone }

func (m *ipBanManager) View() string {
	switch m.phase {
	case ipBanPhaseMenu:
		return m.menuView()
	case ipBanPhasePick:
		return m.pickView()
	case ipBanPhaseForm:
		title := "IP ban · Add"
		if m.action == ipBanActionEdit {
			title = "IP ban · " + m.picked
		}
		view := m.form.View(title)
		if m.fieldErr != "" {
			view += "\n" + flowErr.Render(m.fieldErr)
		}
		return view
	case ipBanPhaseConfirm:
		return flowTitle.Render("IP ban · Confirm") + "\n\n" + renderSummary(m.summaryRows()) + "\n"
	case ipBanPhaseRunning:
		return commandRunningView(m, "IP ban · Running")
	case ipBanPhaseDone:
		if m.runErr != nil {
			var partial *ipBanEnforcementError
			if errors.As(m.runErr, &partial) {
				return m.partialFailureView()
			}
			return commandFailedView(m, "IP ban change failed")
		}
		return flowOK.Render(m.successTitle()) + "\n\n" + renderSummary(m.summaryRows())
	default:
		return ""
	}
}

// partialFailureView reports a change that was saved but not pushed to every
// node. The generic failure screen would read as if the edit were rejected,
// when the registry already holds it and the monitor keeps retrying.
func (m *ipBanManager) partialFailureView() string {
	var title, note string
	switch m.action {
	case ipBanActionRemove:
		title = "Ban lifted, but not on every node yet"
		note = "The ban is removed from the list, and nodes that applied the change no longer block the address. "
	case ipBanActionReapply:
		title = "Bans reapplied, but not on every node"
		note = "Nodes that applied the rules are enforcing the current bans. "
	default:
		title = "Ban saved, but not applied on every node yet"
		note = "The ban is saved, and nodes that applied it are already enforcing it. "
	}
	note += "The monitor service retries the failed nodes on its next refresh; " +
		"use Reapply bans on every node to push again now."
	var b strings.Builder
	b.WriteString(statusWarn.Render(title) + "\n\n")
	noteLines := wrapFieldNote(note, m.width)
	for _, line := range noteLines {
		b.WriteString(dimStyle.Render(line) + "\n")
	}
	b.WriteString("\n" + flowErr.Render(m.runErr.Error()))
	if logs := m.logView(max(1, m.doneLogHeight()-len(noteLines)-1)); logs != "" {
		b.WriteString("\n\n" + logs)
	}
	return b.String()
}

func (m *ipBanManager) successTitle() string {
	switch m.action {
	case ipBanActionRemove:
		return "Ban lifted"
	case ipBanActionReapply:
		return "Bans reapplied"
	default:
		return "Ban saved"
	}
}

func (m *ipBanManager) menuView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("IP ban") + "\n\n")
	b.WriteString(renderSummary(m.banRows()) + "\n")
	if m.loadErr != nil {
		b.WriteString("\n" + flowErr.Render(m.loadErr.Error()) + "\n")
	}
	if m.fieldErr != "" {
		b.WriteString("\n" + flowErr.Render(m.fieldErr) + "\n")
	}
	b.WriteString("\n" + renderActionList(m.actions(), m.cursor))
	return b.String()
}

func (m *ipBanManager) banRows() []summaryLine {
	if len(m.bans) == 0 {
		return []summaryLine{summaryText("No address is banned.")}
	}
	rows := make([]summaryLine, 0, len(m.bans))
	for _, ban := range m.bans {
		rows = append(rows, summaryRow(ban.Address, m.nodeNames(ban.Nodes)))
	}
	return rows
}

func (m *ipBanManager) pickView() string {
	var b strings.Builder
	title := "IP ban · Change"
	prompt := "Choose the ban whose nodes you want to change."
	if m.action == ipBanActionRemove {
		title = "IP ban · Lift"
		prompt = "Choose the ban to lift on every node."
	}
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(dimStyle.Render(prompt) + "\n\n")
	for i, ban := range m.bans {
		row := fmt.Sprintf("%-28s %s", ban.Address, dimStyle.Render(m.nodeNames(ban.Nodes)))
		b.WriteString(cursorRow(row, i == m.cursor) + "\n")
	}
	return b.String()
}

func (m *ipBanManager) summaryRows() []summaryLine {
	if m.action == ipBanActionReapply {
		return []summaryLine{
			summaryRow("Bans", fmt.Sprintf("%d", len(m.bans))),
			summaryBlank(),
			summaryText("Pushes every node's complete drop rules again, whether or not they are recorded as applied."),
		}
	}
	rows := []summaryLine{summaryRow("Address", m.address)}
	if m.action == ipBanActionRemove {
		return append(rows,
			summaryRow("Lifted on", m.nodeNames(m.nodes)),
			summaryBlank(),
			summaryText("The address can use every node again."),
		)
	}
	rows = append(rows, summaryRow("Banned on", m.nodeNames(m.nodes)))
	if m.action == ipBanActionEdit {
		added, lifted := diffNodes(m.previous, m.nodes)
		switch {
		case len(added) == 0 && len(lifted) == 0:
			rows = append(rows, summaryRow("Change", "none; the node list is unchanged"))
		default:
			if len(added) > 0 {
				rows = append(rows, summaryRow("Newly banned on", m.nodeNames(added)))
			}
			if len(lifted) > 0 {
				rows = append(rows, summaryRow("Lifted on", m.nodeNames(lifted)))
			}
		}
	}
	if enforcement := m.enforcementRows(); len(enforcement) > 0 {
		rows = append(rows, summaryBlank(), summaryText("Enforced on:"))
		rows = append(rows, enforcement...)
	}
	return rows
}

// enforcementRows spells out where the drop rules land, so the relay rule —
// a ban on a fronted node is enforced on its relay — is visible before it is
// applied rather than a surprise in the log.
func (m *ipBanManager) enforcementRows() []summaryLine {
	var rows []summaryLine
	for _, id := range m.nodes {
		rows = append(rows, summaryIndentedRow(2, m.nodeName(id), "its own protocol ports"))
		if link, fronted := relaylinks.Find(m.links, id); fronted {
			rows = append(rows, summaryIndentedRow(2, m.nodeName(link.RelayID),
				"the relay ports forwarding to "+m.nodeName(id)))
		}
	}
	return rows
}

// diffNodes returns the nodes in after but not before, and the reverse, each in
// the order of the list it comes from.
func diffNodes(before, after []string) (added, lifted []string) {
	in := func(list []string, id string) bool {
		for _, candidate := range list {
			if strings.EqualFold(candidate, id) {
				return true
			}
		}
		return false
	}
	for _, id := range after {
		if !in(before, id) {
			added = append(added, id)
		}
	}
	for _, id := range before {
		if !in(after, id) {
			lifted = append(lifted, id)
		}
	}
	return added, lifted
}

func (m *ipBanManager) nodeName(id string) string {
	if endpoint, ok := hubctl.RelayEndpointByID(m.endpoints, id); ok {
		if endpoint.ID == relaylinks.HubNodeID {
			return "Hub"
		}
		if endpoint.Name != "" {
			return endpoint.Name
		}
	}
	return id
}

func (m *ipBanManager) nodeNames(ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, m.nodeName(id))
	}
	return strings.Join(names, ", ")
}

func (m *ipBanManager) footerHints() []operationHint {
	switch m.phase {
	case ipBanPhaseMenu:
		return actionFooterHints("Select")
	case ipBanPhasePick:
		return actionBackFooterHints("Continue")
	case ipBanPhaseForm:
		return m.form.footerHints()
	case ipBanPhaseConfirm:
		return applyFooterHints("Apply")
	case ipBanPhaseRunning:
		return runningFooterHints(m.runComplete)
	case ipBanPhaseDone:
		return doneFooterHints(m.runErr != nil)
	default:
		return nil
	}
}
