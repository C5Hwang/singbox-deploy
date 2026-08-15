package ui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type relayPhase int

const (
	relayPhaseMenu relayPhase = iota
	relayPhaseLanding
	relayPhaseRelay
	relayPhaseConfirm
	relayPhaseRunning
	relayPhaseDone
)

type relayAction int

const (
	relayActionAdd relayAction = iota
	relayActionChange
	relayActionRemove
)

var (
	relayUILayout = paths.DefaultLayout
	// relayLoadState reads the registry and the fleet the pickers are built
	// from. It is one seam so a test can describe a whole fleet at once.
	relayLoadState = loadRelayState
	// relayApplyChange performs the registry write, the data-plane reinstall,
	// and the republish as one recorded operation.
	relayApplyChange = applyRelayChange
)

// relayChange is one edit to the relay registry, together with every relay
// whose ruleset has to be reinstalled because of it.
type relayChange struct {
	// LandingID names the node being fronted, or un-fronted when Remove is set.
	LandingID string
	Remove    bool
	Link      relaylinks.Link
	// Relays lists the relays to reinstall: the new one, and the one a moved or
	// removed landing node left behind. Order is not significant.
	Relays []string
}

type relayManager struct {
	phase  relayPhase
	action relayAction

	width  int
	height int

	links     []relaylinks.Link
	endpoints []hubctl.RelayEndpoint
	loadErr   error
	fieldErr  string

	cursor int
	// candidates is the list the current picker shows.
	candidates []hubctl.RelayEndpoint

	landingID string
	relayID   string
	// previousRelayID is the relay a moved landing node is leaving, whose rules
	// have to be withdrawn alongside installing the new one's.
	previousRelayID string
	forwards        []relaylinks.Forward

	commandRun
}

func newRelayManager() *relayManager {
	rm := &relayManager{phase: relayPhaseMenu, commandRun: newCommandRun()}
	rm.reload()
	return rm
}

func (rm *relayManager) reload() {
	rm.links, rm.endpoints, rm.loadErr = relayLoadState(relayUILayout())
}

func loadRelayState(layout paths.Layout) ([]relaylinks.Link, []hubctl.RelayEndpoint, error) {
	links, err := relaylinks.Load(layout)
	if err != nil {
		return nil, nil, err
	}
	endpoints, err := (&hubctl.Controller{Layout: layout, ExpectedVersion: toolVersion}).RelayEndpoints()
	if err != nil {
		return nil, nil, err
	}
	return links, endpoints, nil
}

func (rm *relayManager) setSize(width, height int) {
	rm.width = width
	rm.height = height
	rm.commandRun.setSize(width, height)
}

// relayActions is the menu, with the entries that need an existing link hidden
// while there is none.
func (rm *relayManager) relayActions() []actionItem[relayAction] {
	items := []actionItem[relayAction]{{action: relayActionAdd, label: "Add relay"}}
	if len(rm.links) > 0 {
		items = append(items,
			actionItem[relayAction]{action: relayActionChange, label: "Change relay"},
			actionItem[relayAction]{action: relayActionRemove, label: "Remove relay"},
		)
	}
	return items
}

// landingCandidates lists the nodes that can be fronted. A node that already has
// a relay is left out, as the operator asked; so is one that relays for someone
// else, because chaining is refused.
func (rm *relayManager) landingCandidates() []hubctl.RelayEndpoint {
	var out []hubctl.RelayEndpoint
	for _, endpoint := range rm.endpoints {
		if !relayEndpointUsable(endpoint) || len(endpoint.Protocols) == 0 {
			continue
		}
		if relaylinks.IsLanding(rm.links, endpoint.ID) || relaylinks.IsRelay(rm.links, endpoint.ID) {
			continue
		}
		out = append(out, endpoint)
	}
	return out
}

// frontedNodes lists the nodes that already have a relay, for the change and
// remove flows.
func (rm *relayManager) frontedNodes() []hubctl.RelayEndpoint {
	var out []hubctl.RelayEndpoint
	for _, link := range rm.links {
		if endpoint, ok := hubctl.RelayEndpointByID(rm.endpoints, link.LandingID); ok {
			out = append(out, endpoint)
		}
	}
	return out
}

// relayCandidates lists the nodes that can front landingID. A node that is
// itself fronted cannot relay, and a node cannot relay for itself; a node that
// already relays for others can take on another.
func (rm *relayManager) relayCandidates(landingID string) []hubctl.RelayEndpoint {
	var out []hubctl.RelayEndpoint
	for _, endpoint := range rm.endpoints {
		if !relayEndpointUsable(endpoint) || endpoint.ID == landingID {
			continue
		}
		if relaylinks.IsLanding(rm.links, endpoint.ID) {
			continue
		}
		out = append(out, endpoint)
	}
	return out
}

// relayEndpointUsable reports whether a node can take part in a relay link at
// all: the ruleset needs a running deployment, and the forwarding needs a name
// to resolve.
func relayEndpointUsable(endpoint hubctl.RelayEndpoint) bool {
	return endpoint.Installed && strings.TrimSpace(endpoint.Domain) != ""
}

func (rm *relayManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		rm.setSize(msg.Width, msg.Height)
	case runMsg:
		return rm.handleRun(msg), false
	case tea.KeyMsg:
		return rm.handleKey(msg)
	case tea.MouseMsg:
		rm.handleLogWheel(msg.Button, rm.phase == relayPhaseRunning || (rm.phase == relayPhaseDone && rm.runErr != nil))
	}
	return nil, false
}

func (rm *relayManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch rm.phase {
	case relayPhaseMenu:
		return rm.handleMenuKey(msg)
	case relayPhaseLanding, relayPhaseRelay:
		return rm.handlePickerKey(msg)
	case relayPhaseConfirm:
		return rm.handleConfirmKey(msg)
	case relayPhaseRunning:
		if msg.String() == "enter" && rm.runComplete {
			rm.phase = relayPhaseDone
		} else {
			rm.handleScrollKey(msg.String(), rm.logViewportHeight())
		}
	case relayPhaseDone:
		return rm.handleDoneKey(msg.String())
	}
	return nil, false
}

func (rm *relayManager) handleMenuKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	items := rm.relayActions()
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		Move: func(delta int) {
			rm.cursor = moveActionCursor(rm.cursor, items, delta)
			rm.fieldErr = ""
		},
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(rm.cursor, len(items)); ok {
				rm.startAction(items[idx].action)
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

// startAction opens the landing-node picker for the chosen action, refusing up
// front when the fleet has nothing to offer it.
func (rm *relayManager) startAction(action relayAction) {
	rm.action = action
	rm.fieldErr = ""
	rm.landingID, rm.relayID, rm.previousRelayID, rm.forwards = "", "", "", nil
	if action == relayActionAdd {
		rm.candidates = rm.landingCandidates()
		if len(rm.candidates) == 0 {
			rm.fieldErr = "no node can be fronted: a landing node has to be installed, have a domain, serve at least one protocol, and not already take part in a relay"
			return
		}
	} else {
		rm.candidates = rm.frontedNodes()
		if len(rm.candidates) == 0 {
			rm.fieldErr = "no node has a relay yet"
			return
		}
	}
	rm.cursor = 0
	rm.phase = relayPhaseLanding
}

func (rm *relayManager) handlePickerKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		Move: func(delta int) {
			rm.cursor = moveSelection(rm.cursor, len(rm.candidates), delta)
			rm.fieldErr = ""
		},
		Confirm: func() (tea.Cmd, bool) { return rm.confirmPick(), false },
		Back:    func() (tea.Cmd, bool) { rm.back(); return nil, false },
		Cancel:  func() (tea.Cmd, bool) { rm.back(); return nil, false },
	})
	if handled {
		return cmd, done
	}
	return nil, false
}

func (rm *relayManager) confirmPick() tea.Cmd {
	idx, ok := selectedIndex(rm.cursor, len(rm.candidates))
	if !ok {
		return nil
	}
	picked := rm.candidates[idx]
	if rm.phase == relayPhaseRelay {
		rm.relayID = picked.ID
		if err := rm.planForwards(); err != nil {
			rm.fieldErr = err.Error()
			return nil
		}
		rm.phase = relayPhaseConfirm
		return nil
	}

	rm.landingID = picked.ID
	if link, found := relaylinks.Find(rm.links, picked.ID); found {
		rm.previousRelayID = link.RelayID
	}
	if rm.action == relayActionRemove {
		rm.phase = relayPhaseConfirm
		return nil
	}
	rm.candidates = rm.relayCandidates(picked.ID)
	if len(rm.candidates) == 0 {
		rm.fieldErr = "no node can relay for " + picked.Name + ": a relay has to be installed, have a domain, and not be fronted itself"
		return nil
	}
	rm.cursor = 0
	rm.phase = relayPhaseRelay
	return nil
}

// planForwards generates the relay's listen ports and checks the resulting link
// against the registry, so a selection that cannot be stored is refused while
// the operator is still choosing rather than half way through provisioning.
func (rm *relayManager) planForwards() error {
	landing, ok := hubctl.RelayEndpointByID(rm.endpoints, rm.landingID)
	if !ok {
		return fmt.Errorf("landing node is no longer in the fleet")
	}
	relayNode, ok := hubctl.RelayEndpointByID(rm.endpoints, rm.relayID)
	if !ok {
		return fmt.Errorf("relay node is no longer in the fleet")
	}
	targets := make([]relaylinks.Target, 0, len(landing.Protocols))
	for _, protocol := range landing.Protocols {
		targets = append(targets, relaylinks.Target{Protocol: protocol.Protocol})
	}
	forwards, err := relaylinks.AllocateForwards(rm.links, relayNode.ID, relayNode.ReservedPorts, targets)
	if err != nil {
		return err
	}
	link := relaylinks.Link{LandingID: rm.landingID, RelayID: rm.relayID, Forwards: forwards}
	if err := relaylinks.Validate(rm.links, link); err != nil {
		return err
	}
	rm.forwards = forwards
	return nil
}

func (rm *relayManager) back() {
	rm.fieldErr = ""
	switch rm.phase {
	case relayPhaseRelay:
		rm.candidates = relayLandingPickerList(rm)
		rm.cursor = 0
		rm.phase = relayPhaseLanding
	case relayPhaseConfirm:
		if rm.action == relayActionRemove {
			rm.candidates = rm.frontedNodes()
			rm.cursor = 0
			rm.phase = relayPhaseLanding
			return
		}
		rm.candidates = rm.relayCandidates(rm.landingID)
		rm.cursor = 0
		rm.phase = relayPhaseRelay
	default:
		rm.cursor = 0
		rm.phase = relayPhaseMenu
	}
}

func relayLandingPickerList(rm *relayManager) []hubctl.RelayEndpoint {
	if rm.action == relayActionAdd {
		return rm.landingCandidates()
	}
	return rm.frontedNodes()
}

func (rm *relayManager) handleConfirmKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
		ConfirmYes: true,
		CancelNo:   true,
		Confirm:    func() (tea.Cmd, bool) { return rm.startRun(), false },
		Back:       func() (tea.Cmd, bool) { rm.back(); return nil, false },
		Cancel:     func() (tea.Cmd, bool) { rm.back(); return nil, false },
	})
	if handled {
		return cmd, done
	}
	return nil, false
}

// change describes the edit the confirm screen is about to commit.
func (rm *relayManager) change() relayChange {
	touched := []string{}
	if rm.relayID != "" {
		touched = append(touched, rm.relayID)
	}
	if rm.previousRelayID != "" && rm.previousRelayID != rm.relayID {
		touched = append(touched, rm.previousRelayID)
	}
	if rm.action == relayActionRemove {
		return relayChange{LandingID: rm.landingID, Remove: true, Relays: touched}
	}
	return relayChange{
		LandingID: rm.landingID,
		Link:      relaylinks.Link{LandingID: rm.landingID, RelayID: rm.relayID, Forwards: rm.forwards},
		Relays:    touched,
	}
}

func (rm *relayManager) startRun() tea.Cmd {
	rm.phase = relayPhaseRunning
	rm.resetRun(make(chan runMsg, 64))
	ch := rm.ch
	logs := &logWriter{ch: ch}
	layout := relayUILayout()
	change := rm.change()
	go func() {
		err := relayApplyChange(context.Background(), layout, change, logs, runProgressSender(ch))
		ch <- runMsg{done: true, err: err}
	}()
	return rm.waitForRun()
}

// applyRelayChange writes the registry entry, reinstalls the data plane on
// every relay the edit touched, and republishes the subscriptions.
//
// The registry write is its own step so a relay that cannot be reached does not
// make the edit look rejected: the stored topology is what every later refresh
// and reboot converges on, and the failed relay is retried by reapplying.
func applyRelayChange(ctx context.Context, layout paths.Layout, change relayChange, log io.Writer, progress func(deploy.Event)) error {
	ctrl := &hubctl.Controller{
		Layout:          layout,
		Runner:          system.NewExecRunner(log),
		ExpectedVersion: toolVersion,
	}
	steps := []deploy.Step{{
		Label:  "Relay registry",
		Detail: "record which node fronts which",
		Run: func(context.Context) error {
			if change.Remove {
				return relaylinks.Remove(layout, change.LandingID)
			}
			return relaylinks.Set(layout, change.Link)
		},
	}}
	for _, relayID := range change.Relays {
		steps = append(steps, deploy.Step{
			Label:  "Forwarding",
			Detail: "install the forwarding rules on " + relayID,
			Run: func(ctx context.Context) error {
				return ctrl.ApplyRelayFor(ctx, relayID, log)
			},
		})
	}
	steps = append(steps, deploy.Step{
		Label:  "Subscriptions",
		Detail: "republish every subscription group",
		Run:    func(ctx context.Context) error { return ctrl.RefreshSubscriptions(ctx) },
	})
	return deploy.RunSteps(ctx, progress, steps)
}

func (rm *relayManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(rm, msg) }

func (rm *relayManager) runState() *commandRun { return &rm.commandRun }

func (rm *relayManager) markRunFailed() { rm.phase = relayPhaseDone }

func (rm *relayManager) View() string {
	switch rm.phase {
	case relayPhaseMenu:
		return rm.menuView()
	case relayPhaseLanding:
		return rm.pickerView("Relay · Landing node", rm.landingPrompt())
	case relayPhaseRelay:
		return rm.pickerView("Relay · Relay node", "Choose the node that forwards to it. Its traffic is passed through untouched, so the landing node still terminates TLS.")
	case relayPhaseConfirm:
		return rm.confirmView()
	case relayPhaseRunning:
		return commandRunningView(rm, "Relay · Running")
	case relayPhaseDone:
		if rm.runErr != nil {
			return commandFailedView(rm, "Relay change failed")
		}
		return flowOK.Render("Relay updated") + "\n\n" + renderSummary(rm.summaryRows())
	default:
		return ""
	}
}

func (rm *relayManager) landingPrompt() string {
	if rm.action == relayActionAdd {
		return "Choose the node to be fronted. Its subscription entries will point at the relay you pick next."
	}
	return "Choose the node whose relay you want to change."
}

func (rm *relayManager) menuView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Relay") + "\n\n")
	b.WriteString(renderSummary(rm.linkRows()) + "\n")
	if rm.loadErr != nil {
		b.WriteString("\n" + flowErr.Render(rm.loadErr.Error()) + "\n")
	}
	if rm.fieldErr != "" {
		b.WriteString("\n" + flowErr.Render(rm.fieldErr) + "\n")
	}
	b.WriteString("\n" + renderActionList(rm.relayActions(), rm.cursor))
	return b.String()
}

// linkRows describes the current topology, one row per fronted node.
func (rm *relayManager) linkRows() []summaryLine {
	if len(rm.links) == 0 {
		return []summaryLine{summaryText("No node is relayed. Every node is reached at its own address.")}
	}
	rows := make([]summaryLine, 0, len(rm.links))
	for _, link := range rm.links {
		rows = append(rows, summaryRow(rm.nodeName(link.LandingID),
			fmt.Sprintf("via %s (%d port%s)", rm.nodeName(link.RelayID), len(link.Forwards), plural(len(link.Forwards)))))
	}
	return rows
}

func (rm *relayManager) nodeName(id string) string {
	if endpoint, ok := hubctl.RelayEndpointByID(rm.endpoints, id); ok && endpoint.Name != "" {
		return endpoint.Name
	}
	return id
}

func (rm *relayManager) pickerView(title, prompt string) string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(dimStyle.Render(prompt) + "\n")
	if rm.fieldErr != "" {
		b.WriteString("\n" + flowErr.Render(rm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, endpoint := range rm.candidates {
		row := relayCandidateRow(rm, endpoint)
		if i == rm.cursor {
			row = selStyle.Render("> " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func relayCandidateRow(rm *relayManager, endpoint hubctl.RelayEndpoint) string {
	detail := endpoint.Domain
	if link, found := relaylinks.Find(rm.links, endpoint.ID); found {
		detail += "  " + dimStyle.Render("via "+rm.nodeName(link.RelayID))
	} else if served := relaylinks.ServedBy(rm.links, endpoint.ID); len(served) > 0 {
		detail += "  " + dimStyle.Render(fmt.Sprintf("already relays %d node%s", len(served), plural(len(served))))
	}
	return fmt.Sprintf("%-24s %s", endpoint.Name, detail)
}

func (rm *relayManager) confirmView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Relay · Confirm") + "\n\n")
	b.WriteString(renderSummary(rm.summaryRows()) + "\n")
	if rm.fieldErr != "" {
		b.WriteString("\n" + flowErr.Render(rm.fieldErr) + "\n")
	}
	return b.String()
}

func (rm *relayManager) summaryRows() []summaryLine {
	rows := []summaryLine{summaryRow("Landing node", rm.nodeName(rm.landingID))}
	if rm.action == relayActionRemove {
		rows = append(rows,
			summaryRow("Relay", rm.nodeName(rm.previousRelayID)+" (removed)"),
			summaryBlank(),
			summaryText("Its subscription entries go back to its own address. The node keeps its name, so a client only has to refetch."),
		)
		return rows
	}
	rows = append(rows, summaryRow("Relay", rm.nodeName(rm.relayID)))
	if rm.previousRelayID != "" && rm.previousRelayID != rm.relayID {
		rows = append(rows, summaryRow("Replaces", rm.nodeName(rm.previousRelayID)))
	}
	rows = append(rows, summaryBlank(), summaryText("Generated forwarding ports:"))
	landing, _ := hubctl.RelayEndpointByID(rm.endpoints, rm.landingID)
	for _, forward := range rm.forwards {
		target := "not served"
		if port, served := landing.ProtocolPort(forward.Protocol); served {
			target = strconv.Itoa(port)
		}
		rows = append(rows, summaryIndentedRow(2, string(forward.Protocol),
			fmt.Sprintf("%s/%d → %s", forward.Network, forward.RelayPort, target)))
	}
	return rows
}

func (rm *relayManager) footerHints() []operationHint {
	switch rm.phase {
	case relayPhaseMenu:
		return actionFooterHints("Select")
	case relayPhaseLanding:
		return actionBackFooterHints("Continue")
	case relayPhaseRelay:
		return actionBackFooterHints("Continue")
	case relayPhaseConfirm:
		return applyFooterHints("Apply")
	case relayPhaseRunning:
		return runningFooterHints(rm.runComplete)
	case relayPhaseDone:
		return doneFooterHints(rm.runErr != nil)
	default:
		return nil
	}
}
