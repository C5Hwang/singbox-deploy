package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/sshexec"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type nodePhase int

const (
	nodePhaseAction nodePhase = iota
	nodePhaseForm
	nodePhaseConfirm
	nodePhaseRunning
	nodePhaseDone
	nodePhaseDeleteSelect
)

type nodeAction int

const (
	nodeActionList nodeAction = iota
	nodeActionAdd
	nodeActionDelete
)

type nodeActionItem = actionItem[nodeAction]

type nodeManager struct {
	phase  nodePhase
	action nodeAction

	width  int
	height int

	host    system.Host
	hostErr error
	nodes   []cluster.Node
	loadErr error
	cursor  int

	parameterForm
	commandRun

	deleteID string
	result   *cluster.Node
}

func newNodeManager() *nodeManager {
	nm := &nodeManager{
		phase:         nodePhaseAction,
		cursor:        1,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	host, err := system.DetectHost()
	nm.host = host
	nm.hostErr = err
	nm.reloadNodes()
	return nm
}

func (nm *nodeManager) reloadNodes() {
	layout := paths.DefaultLayout()
	registry := cluster.NewRegistry(layout)
	nodes, err := registry.List()
	if err != nil {
		nm.loadErr = err
		return
	}
	nm.nodes = nodes
}

func (nm *nodeManager) setSize(width, height int) {
	nm.width = width
	nm.height = height
	nm.parameterForm.setSize(width, height)
	nm.commandRun.setSize(width, height)
}

func (nm *nodeManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		nm.setSize(msg.Width, msg.Height)
	case runMsg:
		return nm.handleRun(msg), false
	case tea.KeyMsg:
		return nm.handleKey(msg)
	}
	if nm.phase == nodePhaseForm && !nm.currentFieldHasOptions() {
		return nm.updateInput(msg), false
	}
	return nil, false
}

func (nm *nodeManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if nm.loadErr != nil {
		switch {
		case isSelectionCancelKey(msg), isSelectionConfirmKey(msg):
			return nil, true
		}
		return nil, false
	}
	switch nm.phase {
	case nodePhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: nm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				nm.activateAction()
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case nodePhaseForm:
		cmd, done, handled := nm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				nm.phase = nodePhaseConfirm
			},
			Back: func() {
				if !nm.previousField() {
					nm.phase = nodePhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case nodePhaseDeleteSelect:
		cmd, done, handled := nm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				nm.deleteID = strings.TrimSpace(nm.values["delete_id"])
				if nm.deleteID == "" {
					nm.fieldErr = "select a node to delete"
					return
				}
				nm.phase = nodePhaseConfirm
			},
			Back: func() {
				if !nm.previousField() {
					nm.phase = nodePhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case nodePhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return nm.startRun(), false
		case msg.String() == "esc", isSelectionNoKey(msg), isSelectionBackKey(msg):
			nm.phase = nodePhaseAction
		}
	case nodePhaseRunning:
		if msg.String() == "enter" && nm.runComplete {
			nm.phase = nodePhaseDone
		}
	case nodePhaseDone:
		return nil, true
	}
	return nil, false
}

func (nm *nodeManager) moveAction(delta int) {
	nm.cursor = moveActionCursor(nm.cursor, nm.actions(), delta)
	nm.fieldErr = ""
}

func (nm *nodeManager) activateAction() {
	nm.fieldErr = ""
	actions := nm.actions()
	idx, ok := selectedIndex(nm.cursor, len(actions))
	if !ok {
		return
	}
	nm.action = actions[idx].action
	switch nm.action {
	case nodeActionList:
		nm.phase = nodePhaseAction
	case nodeActionAdd:
		nm.startForm(nm.addNodeFields(), nodePhaseForm)
	case nodeActionDelete:
		if len(nm.nodes) == 0 {
			nm.fieldErr = "no nodes to delete"
			return
		}
		nm.startForm(nm.deleteSelectFields(), nodePhaseDeleteSelect)
	}
}

func (nm *nodeManager) startForm(fields []field, phase nodePhase) {
	nm.parameterForm.setFields(fields)
	nm.parameterForm.validate = validateNodeField
	nm.phase = phase
	if nm.parameterForm.advanceField() {
		nm.phase = nodePhaseConfirm
	}
}

func (nm *nodeManager) addNodeFields() []field {
	protoOpts := make([]string, 0, len(config.AllProtocols))
	for _, p := range config.AllProtocols {
		protoOpts = append(protoOpts, string(p))
	}
	publicIP := strings.TrimSpace(nm.host.Arch) // unused; placeholder
	_ = publicIP
	return []field{
		{key: "alias", label: "Node alias", note: "Human label shown in subscriptions and the TUI (e.g. Tokyo, Singapore)."},
		{key: "public_ip", label: "Node public IP or host", note: "Where the node is reachable from the public internet for the initial SSH handshake."},
		{key: "ssh_port", label: "SSH port", def: "22"},
		{key: "ssh_user", label: "SSH user", def: "root"},
		{key: "ssh_password", label: "SSH password", note: "Used only during initial provisioning. Not persisted on the master."},
		{key: "domain", label: "Node TLS domain", note: "Fully qualified domain the node will terminate TLS on (e.g. jp.example.com)."},
		{key: "protocols", label: "Enabled protocols", options: protoOpts, multi: true, note: "Pick one or more protocols this node will serve."},
		{key: "master_endpoint", label: "Master public host:port for WireGuard", note: "How the node should reach the master on port 51820/udp (e.g. master.example.com:51820)."},
		{key: "version", label: "Release tag to deploy on the node", def: getVersion(), note: "Defaults to the master's current release."},
	}
}

func (nm *nodeManager) deleteSelectFields() []field {
	options := make([]string, 0, len(nm.nodes))
	for _, n := range nm.nodes {
		options = append(options, nodeOptionLabel(n))
	}
	return []field{{
		key:     "delete_id",
		label:   "Node to delete",
		options: options,
		note:    "Removes the WireGuard peer, asks the node to self-destruct, then deletes the registry entry.",
	}}
}

func validateNodeField(f field, val string, _ map[string]string) error {
	v := strings.TrimSpace(val)
	switch f.key {
	case "alias", "public_ip", "domain", "master_endpoint", "version":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
	case "ssh_port":
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("ssh port must be 1-65535")
		}
	case "ssh_user":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
	case "ssh_password":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
	case "protocols":
		if v == "" {
			return fmt.Errorf("select at least one protocol")
		}
	case "delete_id":
		if v == "" {
			return fmt.Errorf("select a node to delete")
		}
	}
	return nil
}

func (nm *nodeManager) canApply() bool {
	return nm.hostErr == nil && nm.host.IsRoot && nm.host.Supported() && !nm.host.SELinux
}

func (nm *nodeManager) applyBlocker() string {
	if nm.hostErr != nil {
		return "failed to detect host: " + nm.hostErr.Error()
	}
	if !nm.host.IsRoot {
		return "node management must run as root"
	}
	if !nm.host.Supported() {
		return "unsupported host"
	}
	if nm.host.SELinux {
		return "SELinux is enforcing; node management is blocked"
	}
	return "cannot manage nodes"
}

func (nm *nodeManager) startRun() tea.Cmd {
	if !nm.canApply() {
		nm.fieldErr = nm.applyBlocker()
		nm.phase = nodePhaseAction
		return nil
	}
	nm.phase = nodePhaseRunning
	nm.resetRun(make(chan runMsg, 64))
	ch := nm.ch
	logs := &logWriter{ch: ch}
	layout := paths.DefaultLayout()
	registry := cluster.NewRegistry(layout)
	orch := &cluster.Orchestrator{
		Registry: registry,
		Runner:   system.NewExecRunner(logs),
		Progress: func(e cluster.Event) {
			ch <- runMsg{event: &deploy.Event{Index: e.Index, Total: e.Total, Label: e.Label, Detail: e.Detail, Status: e.Status, Err: e.Err}}
		},
	}
	switch nm.action {
	case nodeActionAdd:
		req := nm.buildAddRequest()
		go func() {
			node, err := orch.AddNode(context.Background(), req)
			if err == nil {
				nm.result = &node
			}
			ch <- runMsg{done: true, err: err}
		}()
	case nodeActionDelete:
		id := nm.deleteID
		go func() {
			err := orch.RemoveNode(context.Background(), id, true)
			ch <- runMsg{done: true, err: err}
		}()
	default:
		go func() {
			ch <- runMsg{done: true, err: fmt.Errorf("nothing to do")}
		}()
	}
	return nm.waitForRun()
}

func (nm *nodeManager) buildAddRequest() cluster.AddNodeRequest {
	v := nm.values
	sshPort, _ := strconv.Atoi(strings.TrimSpace(v["ssh_port"]))
	if sshPort == 0 {
		sshPort = 22
	}
	protocols := protocolListFromCSV(v["protocols"])
	return cluster.AddNodeRequest{
		Alias:    strings.TrimSpace(v["alias"]),
		PublicIP: strings.TrimSpace(v["public_ip"]),
		SSHTarget: sshexec.Target{
			Host: strings.TrimSpace(v["public_ip"]),
			Port: sshPort,
		},
		SSHAuth: sshexec.Auth{
			User:     strings.TrimSpace(v["ssh_user"]),
			Password: v["ssh_password"],
		},
		Domain:               strings.TrimSpace(v["domain"]),
		EnabledProtocols:     protocols,
		MasterPublicEndpoint: strings.TrimSpace(v["master_endpoint"]),
		Version:              strings.TrimSpace(v["version"]),
	}
}

func protocolListFromCSV(raw string) []config.Protocol {
	out := []config.Protocol{}
	allowed := map[config.Protocol]bool{}
	for _, p := range config.AllProtocols {
		allowed[p] = true
	}
	for _, part := range strings.Split(raw, ",") {
		p := config.Protocol(strings.TrimSpace(part))
		if allowed[p] {
			out = append(out, p)
		}
	}
	return out
}

func (nm *nodeManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(nm, msg) }

func (nm *nodeManager) runState() *commandRun { return &nm.commandRun }

func (nm *nodeManager) markRunFailed() { nm.phase = nodePhaseDone }

func (nm *nodeManager) View() string {
	if nm.loadErr != nil {
		return flowTitle.Render("Node Management") + "\n\n" + flowErr.Render(nm.loadErr.Error())
	}
	switch nm.phase {
	case nodePhaseAction:
		return nm.actionView()
	case nodePhaseForm, nodePhaseDeleteSelect:
		return nm.parameterForm.View("Node Management · Parameters")
	case nodePhaseConfirm:
		return nm.confirmView()
	case nodePhaseRunning:
		return commandRunningView(nm, "Node Management · Running")
	case nodePhaseDone:
		if nm.runErr != nil {
			return commandFailedView(nm, "Node operation failed")
		}
		nm.reloadNodes()
		return flowOK.Render("Node operation complete") + "\n\n" + nm.listSummary()
	}
	return ""
}

func (nm *nodeManager) actionView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Node Management") + "\n\n")
	b.WriteString(nm.listSummary() + "\n")
	if !nm.canApply() {
		b.WriteString(flowErr.Render(nm.applyBlocker()) + "\n")
	}
	if nm.fieldErr != "" {
		b.WriteString(flowErr.Render(nm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(nm.actions(), nm.cursor))
	return b.String()
}

func (nm *nodeManager) listSummary() string {
	rows := []summaryLine{summaryRow("Registered nodes", strconv.Itoa(len(nm.nodes)))}
	for _, n := range nm.nodes {
		rows = append(rows, summaryIndentedRow(2, n.ID, fmt.Sprintf("%s (%s) → %s", n.Alias, n.Domain, n.WGIP)))
	}
	return renderSummary(rows)
}

func (nm *nodeManager) confirmView() string {
	var rows []summaryLine
	switch nm.action {
	case nodeActionAdd:
		rows = append(rows,
			summaryRow("Add node", nm.values["alias"]),
			summaryRow("SSH target", nm.values["public_ip"]+":"+orDefault(nm.values["ssh_port"], "22")),
			summaryRow("Domain", nm.values["domain"]),
			summaryRow("Protocols", nm.values["protocols"]),
			summaryRow("Master endpoint", nm.values["master_endpoint"]),
			summaryRow("Release tag", nm.values["version"]),
		)
	case nodeActionDelete:
		rows = append(rows,
			summaryRow("Delete node", nm.deleteID),
			summaryText("This will tear down the node and remove its WireGuard peer."),
		)
	}
	return flowTitle.Render("Node Management · Confirm") + "\n\n" + renderSummary(rows)
}

func (nm *nodeManager) footerHints() []operationHint {
	switch nm.phase {
	case nodePhaseAction:
		return actionFooterHints("Select")
	case nodePhaseForm, nodePhaseDeleteSelect:
		return nm.parameterForm.footerHints()
	case nodePhaseConfirm:
		return applyFooterHints("Apply")
	case nodePhaseRunning:
		return runningFooterHints(nm.runComplete)
	case nodePhaseDone:
		return doneFooterHints(nm.runErr != nil)
	}
	return returnFooterHints()
}

func (nm *nodeManager) actions() []nodeActionItem {
	return []nodeActionItem{
		{separator: true, label: "View"},
		{action: nodeActionList, label: "Refresh node list"},
		{separator: true, label: "Manage"},
		{action: nodeActionAdd, label: "Add node (SSH provisioning)"},
		{action: nodeActionDelete, label: "Delete node"},
	}
}

func nodeOptionLabel(n cluster.Node) string {
	alias := n.Alias
	if alias == "" {
		alias = n.Domain
	}
	return fmt.Sprintf("[%s] %s (%s)", n.ID, alias, n.WGIP)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// getVersion returns the master's current version for use as a form default.
func getVersion() string { return toolVersion }
