package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/sshexec"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
	"github.com/C5Hwang/singbox-deploy/internal/wireguard"
)

type nodePhase int

const (
	nodePhaseAction nodePhase = iota
	nodePhaseForm
	nodePhaseMissingDNSCreds
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

	deleteID  string
	result    *cluster.Node
	subForm   *dnsCredentialForm
	statusErr string

	validateDomain func(ctx context.Context, domain, expectedIP string) error
	validateSSH    func(ctx context.Context, target sshexec.Target, auth sshexec.Auth) error
}

func newNodeManager() *nodeManager {
	nm := &nodeManager{
		phase:          nodePhaseAction,
		cursor:         1,
		parameterForm:  newParameterForm(nil),
		commandRun:     newCommandRun(),
		validateDomain: validateDomainResolvesToIP,
		validateSSH:    validateSSHReachable,
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
	if nm.phase == nodePhaseMissingDNSCreds && nm.subForm != nil {
		cmd := nm.subForm.Update(msg)
		nm.advanceSubFormState()
		return cmd, false
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
		prevKey := nm.parameterForm.currentFieldKey()
		cmd, done, handled := nm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				if !nm.ensureNodeDNSCredentials() {
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
			if prevKey == "domain" && nm.parameterForm.currentFieldKey() != "domain" && nm.parameterForm.fieldErr == "" {
				if domain := nm.values["domain"]; domain != "" {
					store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
					if _, err := store.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
						nm.enterMissingDNSCreds(domain, "")
					}
				}
			}
			return cmd, done
		}
	case nodePhaseMissingDNSCreds:
		if nm.subForm == nil {
			nm.phase = nodePhaseForm
			return nil, false
		}
		cmd := nm.subForm.Update(msg)
		nm.advanceSubFormState()
		return cmd, false
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
	nm.parameterForm.validate = nm.validateNodeField
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
	// The install-flow helper protocolSelected treats an empty value as
	// "everything", which fits the install form's default-all behaviour.
	// Node forms start with no protocols picked, so use a stricter predicate
	// that hides per-protocol port fields until the operator selects them.
	missingProtocol := func(p config.Protocol) func(map[string]string) bool {
		return func(v map[string]string) bool {
			return !selectedOptions(v["protocols"])[string(p)]
		}
	}
	noReality := func(v map[string]string) bool {
		selected := selectedOptions(v["protocols"])
		return !selected[string(config.ProtocolRealityVision)] && !selected[string(config.ProtocolRealityGRPC)]
	}
	fields := []field{
		{key: "alias", label: "Node alias", note: "Human label shown in subscriptions and the TUI (e.g. Tokyo, Singapore)."},
		{key: "public_ip", label: "Node public IP or host", note: "Where the node is reachable from the public internet for the initial SSH handshake."},
		{key: "ssh_port", label: "SSH port", def: "22"},
		{key: "ssh_user", label: "SSH user", def: "root"},
		{key: "ssh_password", label: "SSH password", note: "Used only during initial provisioning. Not persisted on the master."},
		{key: "domain", label: "Domain (must resolve to this node)", note: "Used for certificate issuance, Nginx server_name, and TLS SNI."},
		{key: "protocols", label: "Enabled protocols", options: protoOpts, multi: true, note: "Pick one or more protocols this node will serve."},
	}
	// Reality SNI + per-protocol UUID/password/port fields mirror the install
	// form so the add-node interaction matches the host install flow.
	fields = append(fields, installProtocolParameterFields(missingProtocol, noReality)...)
	fields = append(fields,
		field{key: "master_host", label: "Master public IP or domain", note: "Where the node reaches the master over WireGuard (e.g. master.example.com or 198.51.100.1)."},
	)
	return fields
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

func (nm *nodeManager) validateNodeField(f field, val string, vals map[string]string) error {
	v := strings.TrimSpace(val)
	switch f.key {
	case "alias", "public_ip":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
	case "master_host":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
		if strings.ContainsAny(v, ":/ ") {
			return fmt.Errorf("%s must be a bare IP or domain (no port or path); port %d is added automatically", f.label, wireguard.DefaultListenPort)
		}
	case "ssh_port":
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("ssh port must be 1-65535")
		}
		return nil
	case "ssh_user":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
	case "ssh_password":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
		if nm.validateSSH == nil {
			return nil
		}
		host := strings.TrimSpace(vals["public_ip"])
		if host == "" {
			return fmt.Errorf("node public IP must be entered before the SSH password")
		}
		port, _ := strconv.Atoi(strings.TrimSpace(vals["ssh_port"]))
		if port == 0 {
			port = 22
		}
		user := strings.TrimSpace(vals["ssh_user"])
		if user == "" {
			user = "root"
		}
		return nm.validateSSH(context.Background(),
			sshexec.Target{Host: host, Port: port},
			sshexec.Auth{User: user, Password: v},
		)
	case "domain":
		if v == "" {
			return fmt.Errorf("%s is required", f.label)
		}
		if nm.validateDomain == nil {
			return nil
		}
		host := strings.TrimSpace(vals["public_ip"])
		if host == "" {
			return nil
		}
		return nm.validateDomain(context.Background(), v, host)
	case "protocols":
		if v == "" {
			return fmt.Errorf("select at least one protocol")
		}
	case "delete_id":
		if v == "" {
			return fmt.Errorf("select a node to delete")
		}
	}
	return uiparams.ValidateSharedParameterValue(f.key, v)
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

// enterMissingDNSCreds opens the inline DNS credential sub-form for the given
// node domain. headerErr is shown above the form when the previous save did
// not cover the domain.
func (nm *nodeManager) enterMissingDNSCreds(domain, headerErr string) {
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	nm.phase = nodePhaseMissingDNSCreds
	nm.subForm = newDNSCredentialForm(domain, store)
	if headerErr != "" {
		nm.subForm.SetHeaderError(headerErr)
	}
	nm.subForm.setSize(nm.width, nm.height)
}

// ensureNodeDNSCredentials runs the lookup at Complete. Returns true on hit so
// the form can advance to confirm; false when it transitioned to the
// missing-creds sub-form.
func (nm *nodeManager) ensureNodeDNSCredentials() bool {
	domain := strings.TrimSpace(nm.values["domain"])
	if domain == "" {
		return true
	}
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	if _, err := store.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
		nm.enterMissingDNSCreds(domain, "")
		return false
	}
	return true
}

// advanceSubFormState consumes saved/cancelled signals from the sub-form.
func (nm *nodeManager) advanceSubFormState() {
	if nm.subForm == nil {
		return
	}
	saved, cancelled, _ := nm.subForm.State()
	switch {
	case saved:
		domain := strings.TrimSpace(nm.values["domain"])
		store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
		if _, err := store.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
			nm.enterMissingDNSCreds(domain, fmt.Sprintf("Saved credentials do not cover %s — adjust the root domain.", domain))
			return
		}
		nm.subForm = nil
		nm.phase = nodePhaseForm
	case cancelled:
		nm.subForm = nil
		nm.phase = nodePhaseForm
		nm.statusErr = "DNS credentials are still required for this node."
		nm.parameterForm.backToFieldKey("domain")
	}
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
		req, err := nm.buildAddRequest()
		if err != nil {
			nm.fieldErr = err.Error()
			nm.phase = nodePhaseAction
			ch <- runMsg{done: true, err: err}
			return nm.waitForRun()
		}
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

func (nm *nodeManager) buildAddRequest() (cluster.AddNodeRequest, error) {
	v := nm.values
	sshPort, _ := strconv.Atoi(strings.TrimSpace(v["ssh_port"]))
	if sshPort == 0 {
		sshPort = 22
	}
	protocols := protocolListFromCSV(v["protocols"])
	ports, err := allocateNodeProtocolPorts(protocols, v)
	if err != nil {
		return cluster.AddNodeRequest{}, err
	}
	coreVersion := parseSingBoxCoreVersion(coreCurrentVersion(paths.DefaultLayout()))
	if coreVersion == "" {
		return cluster.AddNodeRequest{}, fmt.Errorf("could not detect master sing-box core version; install sing-box on the master before adding nodes")
	}
	realityServerName := ""
	if hasProtocol(protocols, config.ProtocolRealityVision) || hasProtocol(protocols, config.ProtocolRealityGRPC) {
		realityServerName, err = uiparams.NormalizeRealityServerName(v["reality_sni"])
		if err != nil {
			return cluster.AddNodeRequest{}, fmt.Errorf("reality SNI: %w", err)
		}
	}
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
		Ports:                ports,
		RealityServerName:    realityServerName,
		MasterPublicEndpoint: fmt.Sprintf("%s:%d", strings.TrimSpace(v["master_host"]), wireguard.DefaultListenPort),
		Version:              toolVersion,
		CoreVersion:          coreVersion,
		CredentialOverrides: deploy.Credentials{
			RealityVisionUUID: strings.TrimSpace(v["reality_vision_uuid"]),
			RealityGRPCUUID:   strings.TrimSpace(v["reality_grpc_uuid"]),
			HysteriaPassword:  strings.TrimSpace(v["hysteria2_password"]),
			TUICUUID:          strings.TrimSpace(v["tuic_uuid"]),
			TUICPassword:      strings.TrimSpace(v["tuic_password"]),
			AnyTLSPassword:    strings.TrimSpace(v["anytls_password"]),
		},
	}, nil
}

// parseSingBoxCoreVersion extracts the bare version number (e.g. "1.12.0") from
// the first line of `sing-box version` output ("sing-box version 1.12.0"). It
// returns an empty string when the raw value is empty or unparseable so the
// caller can surface a useful error.
func parseSingBoxCoreVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	for i, f := range fields {
		if strings.EqualFold(f, "version") && i+1 < len(fields) {
			return strings.TrimPrefix(fields[i+1], "v")
		}
	}
	return ""
}

// allocateNodeProtocolPorts honours user-supplied port values from the form
// and fills any blanks with a random unprivileged port. 80 is reserved (HTTP
// challenge / nginx) and 443 is rejected by config.ValidateProtocolPort as
// the masquerade site.
func allocateNodeProtocolPorts(selected []config.Protocol, vals map[string]string) (config.Ports, error) {
	used := map[int]bool{80: true}
	var ports config.Ports
	for _, p := range selected {
		key := portFieldKey(p)
		raw := strings.TrimSpace(vals[key])
		var port int
		if raw == "" {
			alloc, err := config.RandomProtocolPort(used)
			if err != nil {
				return config.Ports{}, err
			}
			port = alloc
		} else {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return config.Ports{}, fmt.Errorf("%s port: must be between 1 and 65535", p)
			}
			if err := config.ValidateProtocolPort(n, used); err != nil {
				return config.Ports{}, fmt.Errorf("%s: %w", p, err)
			}
			used[n] = true
			port = n
		}
		switch p {
		case config.ProtocolRealityVision:
			ports.RealityVision = port
		case config.ProtocolRealityGRPC:
			ports.RealityGRPC = port
		case config.ProtocolHysteria2:
			ports.Hysteria2 = port
		case config.ProtocolTUIC:
			ports.TUIC = port
		case config.ProtocolAnyTLS:
			ports.AnyTLS = port
		}
	}
	return ports, nil
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
		view := nm.parameterForm.View("Node Management · Parameters")
		if nm.statusErr != "" {
			view = flowErr.Render(nm.statusErr) + "\n\n" + view
		}
		return view
	case nodePhaseMissingDNSCreds:
		if nm.subForm != nil {
			return nm.subForm.View()
		}
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
		protocols := protocolListFromCSV(nm.values["protocols"])
		rows = append(rows,
			summaryRow("Add node", nm.values["alias"]),
			summaryRow("SSH target", nm.values["public_ip"]+":"+orDefault(nm.values["ssh_port"], "22")),
			summaryRow("Domain", nm.values["domain"]),
			summaryRow("Protocols", protocolLabels(protocols)),
			summaryRow("Master endpoint", fmt.Sprintf("%s:%d", nm.values["master_host"], wireguard.DefaultListenPort)),
			summaryRow("Release tag", toolVersion),
			summaryRow("sing-box core version", orDefault(parseSingBoxCoreVersion(coreCurrentVersion(paths.DefaultLayout())), "unknown")),
		)
		if hasProtocol(protocols, config.ProtocolRealityVision) || hasProtocol(protocols, config.ProtocolRealityGRPC) {
			rows = append(rows, summaryRow("Reality URL/SNI", nm.values["reality_sni"]))
		}
		if len(protocols) > 0 {
			rows = append(rows, summaryText("Protocol parameters:"))
			for _, p := range protocols {
				rows = append(rows, nm.protocolParamRows(p)...)
			}
		}
	case nodeActionDelete:
		rows = append(rows,
			summaryRow("Delete node", nm.deleteID),
			summaryText("This will tear down the node and remove its WireGuard peer."),
		)
	}
	return flowTitle.Render("Node Management · Confirm") + "\n\n" + renderSummary(rows)
}

// protocolParamRows renders the indented credential/port summary lines for one
// selected protocol, matching the install confirm view so the two flows stay
// visually consistent.
func (nm *nodeManager) protocolParamRows(p config.Protocol) []summaryLine {
	rows := []summaryLine{
		summaryIndentedRow(2, string(p)+" port", summaryValueOrRandom(nm.values[portFieldKey(p)])),
	}
	switch p {
	case config.ProtocolRealityVision:
		rows = append(rows, summaryIndentedRow(2, "VLESS Reality Vision UUID", summaryValueOrRandom(nm.values["reality_vision_uuid"])))
	case config.ProtocolRealityGRPC:
		rows = append(rows, summaryIndentedRow(2, "VLESS Reality gRPC UUID", summaryValueOrRandom(nm.values["reality_grpc_uuid"])))
	case config.ProtocolHysteria2:
		rows = append(rows, summaryIndentedRow(2, "Hysteria2 password", summaryValueOrRandom(nm.values["hysteria2_password"])))
	case config.ProtocolTUIC:
		rows = append(rows,
			summaryIndentedRow(2, "TUIC UUID", summaryValueOrRandom(nm.values["tuic_uuid"])),
			summaryIndentedRow(2, "TUIC password", summaryValueOrRandom(nm.values["tuic_password"])),
		)
	case config.ProtocolAnyTLS:
		rows = append(rows, summaryIndentedRow(2, "AnyTLS password", summaryValueOrRandom(nm.values["anytls_password"])))
	}
	return rows
}

func (nm *nodeManager) footerHints() []operationHint {
	switch nm.phase {
	case nodePhaseAction:
		return actionFooterHints("Select")
	case nodePhaseForm, nodePhaseDeleteSelect:
		return nm.parameterForm.footerHints()
	case nodePhaseMissingDNSCreds:
		if nm.subForm != nil {
			return nm.subForm.footerHints()
		}
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
		{action: nodeActionAdd, label: "Add node"},
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
