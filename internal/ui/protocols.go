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
	"github.com/C5Hwang/singbox-deploy/internal/protocol"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type protocolPhase int

const (
	protocolPhaseTarget protocolPhase = iota
	protocolPhaseAction
	protocolPhaseSelect
	protocolPhaseEditPick
	protocolPhaseForm
	protocolPhaseConfirm
	protocolPhaseRunning
	protocolPhaseDone
)

type protocolAction int

const (
	protocolActionNone protocolAction = iota
	protocolActionChange
	protocolActionEdit
	protocolActionRealitySNI
)

var (
	protocolUILayout   = paths.DefaultLayout
	detectProtocolHost = system.DetectHost
	updateProtocolsRun = protocol.Update
)

type protocolActionItem = actionItem[protocolAction]

type protocolManager struct {
	phase  protocolPhase
	action protocolAction

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     deploy.Config
	loadErr error

	cursor   int
	selected map[string]bool
	parameterForm

	editProto config.Protocol

	picker        targetPicker
	agentOutcomes []agentOutcome

	commandRun
	result deploy.Config
}

func newProtocolManager() *protocolManager {
	pm := &protocolManager{
		phase:         protocolPhaseAction,
		selected:      map[string]bool{},
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	host, err := detectProtocolHost()
	pm.host = host
	pm.hostErr = err
	pm.picker = newTargetPicker(protocolUILayout())
	if pm.picker.hasNodes() {
		pm.phase = protocolPhaseTarget
	}
	cfg, err := deploy.LoadProtocolConfig(protocolUILayout())
	if err != nil {
		pm.loadErr = err
		return pm
	}
	pm.cfg = cfg
	pm.selected = selectedOptions(protocolSelectionValue(cfg.Enabled))
	return pm
}

func (pm *protocolManager) setSize(width, height int) {
	pm.width = width
	pm.height = height
	pm.parameterForm.setSize(width, height)
	pm.commandRun.setSize(width, height)
}

func (pm *protocolManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		pm.setSize(msg.Width, msg.Height)
	case runMsg:
		return pm.handleRun(msg), false
	case tea.KeyMsg:
		return pm.handleKey(msg)
	case tea.MouseMsg:
		return pm.handleMouse(msg), false
	}
	return nil, false
}

func (pm *protocolManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if pm.loadErr != nil {
		switch {
		case isSelectionCancelKey(msg), isSelectionConfirmKey(msg):
			return nil, true
		}
		return nil, false
	}
	switch pm.phase {
	case protocolPhaseTarget:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: pm.moveTarget,
			Confirm: func() (tea.Cmd, bool) {
				pm.phase = protocolPhaseAction
				pm.cursor = 0
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case protocolPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: pm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				pm.activateAction()
				return nil, false
			},
			Back: func() (tea.Cmd, bool) {
				if pm.picker.hasNodes() {
					pm.phase = protocolPhaseTarget
					return nil, false
				}
				return nil, true
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case protocolPhaseSelect:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move:   pm.moveProtocol,
			Toggle: pm.toggleProtocol,
			Confirm: func() (tea.Cmd, bool) {
				pm.prepareChangeConfirm()
				return nil, false
			},
			Back: func() (tea.Cmd, bool) {
				pm.phase = protocolPhaseAction
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case protocolPhaseEditPick:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: pm.moveInstalled,
			Confirm: func() (tea.Cmd, bool) {
				pm.startEditForm()
				return nil, false
			},
			Back: func() (tea.Cmd, bool) {
				pm.phase = protocolPhaseAction
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case protocolPhaseForm:
		cmd, done, handled := pm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() { pm.phase = protocolPhaseConfirm },
			Back:     pm.previousField,
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			return cmd, done
		}
	case protocolPhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return pm.startRun(), false
		case isSelectionBackKey(msg):
			pm.backFromConfirm()
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case protocolPhaseRunning:
		switch msg.String() {
		case "enter":
			if pm.runComplete {
				if cfg, err := deploy.LoadProtocolConfig(protocolUILayout()); err == nil {
					pm.cfg = cfg
					pm.result = cfg
					pm.selected = selectedOptions(protocolSelectionValue(cfg.Enabled))
				}
				pm.phase = protocolPhaseDone
			}
		case "up", "k":
			pm.scrollLog(1, pm.logViewportHeight())
		case "down", "j":
			pm.scrollLog(-1, pm.logViewportHeight())
		case "pgup":
			pm.scrollLog(pm.logViewportHeight(), pm.logViewportHeight())
		case "pgdown":
			pm.scrollLog(-pm.logViewportHeight(), pm.logViewportHeight())
		case "home":
			pm.logScroll = pm.maxLogScroll(pm.logViewportHeight())
		case "end":
			pm.logScroll = 0
		}
	case protocolPhaseDone:
		if pm.runErr != nil {
			switch msg.String() {
			case "up", "k":
				pm.scrollLog(1, pm.doneLogHeight())
				return nil, false
			case "down", "j":
				pm.scrollLog(-1, pm.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (pm *protocolManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if pm.phase == protocolPhaseRunning || (pm.phase == protocolPhaseDone && pm.runErr != nil) {
			pm.scrollLog(3, pm.logViewportHeight())
		}
	case tea.MouseButtonWheelDown:
		if pm.phase == protocolPhaseRunning || (pm.phase == protocolPhaseDone && pm.runErr != nil) {
			pm.scrollLog(-3, pm.logViewportHeight())
		}
	}
	return nil
}

func (pm *protocolManager) moveAction(delta int) {
	pm.cursor = moveActionCursor(pm.cursor, pm.actions(), delta)
	pm.fieldErr = ""
}

func (pm *protocolManager) moveTarget(delta int) {
	pm.picker.move(delta)
	pm.fieldErr = ""
}

func (pm *protocolManager) activateAction() {
	pm.fieldErr = ""
	actions := pm.actions()
	idx, ok := selectedIndex(pm.cursor, len(actions))
	if !ok {
		return
	}
	switch actions[idx].action {
	case protocolActionChange:
		pm.action = protocolActionChange
		pm.phase = protocolPhaseSelect
		pm.cursor = 0
		pm.selected = selectedOptions(protocolSelectionValue(pm.cfg.Enabled))
	case protocolActionEdit:
		pm.action = protocolActionEdit
		pm.phase = protocolPhaseEditPick
		pm.cursor = 0
	case protocolActionRealitySNI:
		pm.startRealitySNIForm()
	}
}

func (pm *protocolManager) moveProtocol(delta int) {
	options := protocolOptions()
	pm.cursor = moveSelection(pm.cursor, len(options), delta)
	pm.fieldErr = ""
}

func (pm *protocolManager) toggleProtocol() {
	options := protocolOptions()
	if toggleStringSelection(pm.selected, options, pm.cursor) {
		pm.fieldErr = ""
	}
}

func (pm *protocolManager) moveInstalled(delta int) {
	installed := pm.cfg.Enabled
	pm.cursor = moveSelection(pm.cursor, len(installed), delta)
	pm.fieldErr = ""
}

func (pm *protocolManager) prepareChangeConfirm() {
	target := pm.targetProtocols()
	if len(target) == 0 {
		pm.fieldErr = "select at least one protocol"
		return
	}
	if pm.picker.selected().isLocal() && !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	if sameProtocolSet(pm.cfg.Enabled, target) {
		pm.fieldErr = "selection is unchanged"
		return
	}
	fields := pm.installFieldsForAdded(target)
	if len(fields) == 0 {
		pm.parameterForm.setFields(nil)
		pm.phase = protocolPhaseConfirm
		return
	}
	pm.startForm(fields)
}

func (pm *protocolManager) startEditForm() {
	if pm.picker.selected().isLocal() && !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	installed := pm.cfg.Enabled
	idx, ok := selectedIndex(pm.cursor, len(installed))
	if !ok {
		pm.fieldErr = "no installed protocols"
		return
	}
	pm.editProto = installed[idx]
	pm.startForm(pm.editFields(pm.editProto))
}

func (pm *protocolManager) startRealitySNIForm() {
	if pm.picker.selected().isLocal() && !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	pm.action = protocolActionRealitySNI
	pm.startForm([]field{fieldFromParameter(uiparams.RealitySNIEditField(pm.cfg.RealityServerName))})
}

func (pm *protocolManager) startForm(fields []field) {
	pm.parameterForm.setFields(fields)
	pm.parameterForm.validate = validateProtocolParameterField
	pm.phase = protocolPhaseForm
	if pm.parameterForm.advanceField() {
		pm.phase = protocolPhaseConfirm
	}
}

func validateProtocolParameterField(f field, val string, vals map[string]string) error {
	return uiparams.ValidateProtocolParameterField(parameterFromField(f), val, vals)
}

func (pm *protocolManager) previousField() {
	if pm.parameterForm.previousField() {
		return
	}
	if pm.action == protocolActionEdit {
		pm.phase = protocolPhaseEditPick
		return
	}
	pm.phase = protocolPhaseAction
	if pm.action == protocolActionChange {
		pm.phase = protocolPhaseSelect
	}
}

func (pm *protocolManager) commitField() {
	pm.parameterForm.validate = validateProtocolParameterField
	if pm.parameterForm.commitField() {
		pm.phase = protocolPhaseConfirm
	}
}

func (pm *protocolManager) backFromConfirm() {
	if len(pm.fields) > 0 {
		pm.phase = protocolPhaseForm
		pm.parameterForm.setField(len(pm.fields) - 1)
		return
	}
	if pm.action == protocolActionEdit {
		pm.phase = protocolPhaseEditPick
		return
	}
	if pm.action == protocolActionRealitySNI {
		pm.phase = protocolPhaseAction
		return
	}
	pm.phase = protocolPhaseSelect
}

func (pm *protocolManager) installFieldsForAdded(target []config.Protocol) []field {
	added, _ := protocolDiff(pm.cfg.Enabled, target)
	addedSet := map[config.Protocol]bool{}
	for _, p := range added {
		addedSet[p] = true
	}
	var fields []field
	if needsRealityProtocol(target) && pm.cfg.RealityServerName == "" {
		fields = append(fields, fieldFromParameter(uiparams.RealitySNIField()))
	}
	for _, proto := range config.AllProtocols {
		if addedSet[proto] {
			fields = append(fields, fieldsFromParameters(uiparams.ProtocolInstallFieldsForProtocol(proto))...)
		}
	}
	return fields
}

func (pm *protocolManager) editFields(proto config.Protocol) []field {
	fields := fieldsFromParameters(uiparams.ProtocolEditFieldsForProtocol(pm.cfg, proto))
	if (proto == config.ProtocolRealityVision || proto == config.ProtocolRealityGRPC) && pm.cfg.RealityServerName == "" {
		fields = append([]field{fieldFromParameter(uiparams.RealitySNIField())}, fields...)
	}
	return fields
}

func (pm *protocolManager) canApply() bool {
	return pm.hostErr == nil && pm.host.IsRoot && pm.host.Supported() && !pm.host.SELinux
}

func (pm *protocolManager) applyBlocker() string {
	if pm.hostErr != nil {
		return "failed to detect host: " + pm.hostErr.Error()
	}
	if !pm.host.IsRoot {
		return "protocol changes must be run as root"
	}
	if !pm.host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", pm.host.OS.Family, pm.host.Arch)
	}
	if pm.host.SELinux {
		return "SELinux is enforcing; protocol changes are blocked"
	}
	return "cannot apply protocol changes"
}

func (pm *protocolManager) startRun() tea.Cmd {
	pm.phase = protocolPhaseRunning
	pm.resetRun(make(chan runMsg, 64))
	ch := pm.ch
	pm.agentOutcomes = nil
	t := pm.picker.selected()
	if t.isLocal() {
		opts := pm.updateOptions()
		logs := &logWriter{ch: ch}
		opts.Layout = protocolUILayout()
		opts.Runner = system.NewExecRunner(logs)
		opts.Firewall = pm.host.Firewall
		opts.Progress = func(e deploy.Event) {
			ev := e
			ch <- runMsg{event: &ev}
		}
		go func() {
			_, err := updateProtocolsRun(context.Background(), opts)
			ch <- runMsg{done: true, err: err}
		}()
		return pm.waitForRun()
	}
	go pm.runAgentProtocolUpdate(ch, t)
	return pm.waitForRun()
}

func (pm *protocolManager) runAgentProtocolUpdate(ch chan runMsg, t target) {
	nodes := []cluster.Node{t.node}
	if t.isAll() {
		nodes = agentNodes(pm.picker)
		opts := pm.updateOptions()
		logs := &logWriter{ch: ch}
		opts.Layout = protocolUILayout()
		opts.Runner = system.NewExecRunner(logs)
		opts.Firewall = pm.host.Firewall
		opts.Progress = func(e deploy.Event) {
			ev := e
			ev.Label = "Local " + ev.Label
			ch <- runMsg{event: &ev}
		}
		if _, err := updateProtocolsRun(context.Background(), opts); err != nil {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Local protocol update", Detail: err.Error(), Status: "failed", Err: err}}
		} else {
			ch <- runMsg{event: &deploy.Event{Index: 1, Total: 1, Label: "Local protocol update", Detail: "done", Status: "done"}}
		}
	}
	var firstErr error
	pm.agentOutcomes = make([]agentOutcome, 0, len(nodes))
	for i, node := range nodes {
		ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: node.Alias + " (" + node.WGIP + ")", Status: "running"}}
		req := pm.agentConfigRequest(node)
		client := cluster.NewAgentClient(node)
		err := client.UpdateConfig(context.Background(), req)
		pm.agentOutcomes = append(pm.agentOutcomes, agentOutcome{node: node, err: err})
		if err != nil {
			ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: err.Error(), Status: "failed", Err: err}}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ch <- runMsg{event: &deploy.Event{Index: i + 1, Total: len(nodes), Label: "Update node", Detail: node.Alias + " (" + node.WGIP + ")", Status: "done"}}
	}
	ch <- runMsg{done: true, err: firstErr}
}

func (pm *protocolManager) agentConfigRequest(node cluster.Node) cluster.ConfigUpdate {
	enabledProtocols := node.EnabledProtocols
	if pm.action == protocolActionChange {
		enabledProtocols = pm.targetProtocols()
	}
	enabled := make([]string, 0, len(enabledProtocols))
	for _, p := range enabledProtocols {
		enabled = append(enabled, string(p))
	}
	req := cluster.ConfigUpdate{
		EnabledProtocols:     enabled,
		ProtocolPorts:        map[string]int{},
		Domain:               node.Domain,
		Credentials:          map[string]string{},
		RealityServerName:    node.RealityServerName,
		RealityHandshakePort: node.RealityHandshakePort,
	}
	applyAgentPort := func(key string, current int, name string) {
		if v := strings.TrimSpace(pm.values[key]); v != "" {
			if port, err := strconv.Atoi(v); err == nil && port > 0 {
				req.ProtocolPorts[name] = port
				return
			}
		}
		if current > 0 {
			req.ProtocolPorts[name] = current
		}
	}
	applyAgentPort("reality_vision_port", node.Ports.RealityVision, string(config.ProtocolRealityVision))
	applyAgentPort("reality_grpc_port", node.Ports.RealityGRPC, string(config.ProtocolRealityGRPC))
	applyAgentPort("hysteria2_port", node.Ports.Hysteria2, string(config.ProtocolHysteria2))
	applyAgentPort("tuic_port", node.Ports.TUIC, string(config.ProtocolTUIC))
	applyAgentPort("anytls_port", node.Ports.AnyTLS, string(config.ProtocolAnyTLS))
	applyAgentCred := func(key string, current string, name string) {
		if v := strings.TrimSpace(pm.values[key]); v != "" {
			req.Credentials[name] = v
			return
		}
		if current != "" {
			req.Credentials[name] = current
		}
	}
	applyAgentCred("reality_vision_uuid", node.Creds.RealityVisionUUID, "reality_vision_uuid")
	applyAgentCred("reality_grpc_uuid", node.Creds.RealityGRPCUUID, "reality_grpc_uuid")
	applyAgentCred("hysteria2_password", node.Creds.HysteriaPassword, "hysteria2_password")
	applyAgentCred("tuic_uuid", node.Creds.TUICUUID, "tuic_uuid")
	applyAgentCred("tuic_password", node.Creds.TUICPassword, "tuic_password")
	applyAgentCred("anytls_password", node.Creds.AnyTLSPassword, "anytls_password")
	if node.Creds.RealityPrivateKey != "" {
		req.Credentials["reality_private_key"] = node.Creds.RealityPrivateKey
	}
	if node.Creds.RealityPublicKey != "" {
		req.Credentials["reality_public_key"] = node.Creds.RealityPublicKey
	}
	if node.Creds.RealityShortID != "" {
		req.Credentials["reality_short_id"] = node.Creds.RealityShortID
	}
	if v := strings.TrimSpace(pm.values["reality_sni"]); v != "" {
		if host, err := uiparams.NormalizeRealityServerName(v); err == nil {
			req.RealityServerName = host
		}
	}
	return req
}

func (pm *protocolManager) updateOptions() protocol.UpdateOptions {
	selected := pm.cfg.Enabled
	if pm.action == protocolActionChange {
		selected = pm.targetProtocols()
	}
	opts := protocol.UpdateOptions{Selected: selected}
	if v := strings.TrimSpace(pm.values["reality_sni"]); v != "" {
		if host, err := uiparams.NormalizeRealityServerName(v); err == nil {
			opts.RealityServerName = host
		}
	}
	applyPortOverride := func(key string, set func(int)) {
		v := strings.TrimSpace(pm.values[key])
		if v == "" {
			return
		}
		port, err := strconv.Atoi(v)
		if err == nil && port > 0 {
			set(port)
		}
	}
	applyPortOverride("reality_vision_port", func(p int) { opts.Ports.RealityVision = p })
	applyPortOverride("reality_grpc_port", func(p int) { opts.Ports.RealityGRPC = p })
	applyPortOverride("hysteria2_port", func(p int) { opts.Ports.Hysteria2 = p })
	applyPortOverride("tuic_port", func(p int) { opts.Ports.TUIC = p })
	applyPortOverride("anytls_port", func(p int) { opts.Ports.AnyTLS = p })
	opts.Creds.RealityVisionUUID = strings.TrimSpace(pm.values["reality_vision_uuid"])
	opts.Creds.RealityGRPCUUID = strings.TrimSpace(pm.values["reality_grpc_uuid"])
	opts.Creds.HysteriaPassword = strings.TrimSpace(pm.values["hysteria2_password"])
	opts.Creds.TUICUUID = strings.TrimSpace(pm.values["tuic_uuid"])
	opts.Creds.TUICPassword = strings.TrimSpace(pm.values["tuic_password"])
	opts.Creds.AnyTLSPassword = strings.TrimSpace(pm.values["anytls_password"])
	return opts
}

func (pm *protocolManager) handleRun(msg runMsg) tea.Cmd {
	return handleCommandRun(pm, msg)
}

func (pm *protocolManager) runState() *commandRun {
	return &pm.commandRun
}

func (pm *protocolManager) markRunFailed() {
	pm.phase = protocolPhaseDone
}

func (pm *protocolManager) View() string {
	if pm.loadErr != nil {
		return flowTitle.Render("Protocol Management") + "\n\n" + flowErr.Render(pm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run install first.")
	}
	switch pm.phase {
	case protocolPhaseTarget:
		return renderTargetPicker("Protocol Management · Target", pm.picker)
	case protocolPhaseAction:
		return pm.actionView()
	case protocolPhaseSelect:
		return pm.selectView()
	case protocolPhaseEditPick:
		return pm.editPickView()
	case protocolPhaseForm:
		return pm.formView()
	case protocolPhaseConfirm:
		return pm.confirmView()
	case protocolPhaseRunning:
		return pm.runningView()
	case protocolPhaseDone:
		if pm.runErr != nil {
			return pm.failedView()
		}
		return flowOK.Render("Protocol management complete") + "\n\n" + pm.doneSummary()
	default:
		return ""
	}
}

func (pm *protocolManager) actionView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Protocol Management") + "\n\n")
	if pm.picker.hasNodes() {
		b.WriteString(renderTargetBadge(pm.picker.selected()) + "\n\n")
	}
	t := pm.picker.selected()
	if t.isLocal() {
		b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.cfg.Enabled) + "\n")
		if !pm.canApply() {
			b.WriteString(flowErr.Render(pm.applyBlocker()) + "\n")
		}
	} else if t.isNode() {
		b.WriteString(dimStyle.Render("Node protocols: ") + protocolLabels(t.node.EnabledProtocols) + "\n")
	}
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(pm.actions(), pm.cursor))
	return b.String()
}

func (pm *protocolManager) selectView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Protocol Management · Install / Remove") + "\n\n")
	b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.cfg.Enabled) + "\n")
	b.WriteString(dimStyle.Render("Target:  ") + protocolLabels(pm.targetProtocols()) + "\n")
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n" + pm.protocolOptionsView())
	return b.String()
}

func (pm *protocolManager) editPickView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Protocol Management · Edit") + "\n\n")
	b.WriteString(dimStyle.Render("Choose an installed protocol to edit its uuid/password and port.") + "\n")
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, proto := range pm.cfg.Enabled {
		label := string(proto) + "  " + dimStyle.Render("port "+uiparams.PortDefault(installedPort(proto, pm.cfg.Ports)))
		row := "  " + label
		if i == pm.cursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (pm *protocolManager) formView() string {
	title := "Protocol Management · Parameters"
	if pm.action == protocolActionEdit {
		title = "Protocol Management · Edit " + string(pm.editProto)
	}
	return pm.parameterForm.View(title)
}

func (pm *protocolManager) protocolOptionsView() string {
	options := protocolOptions()
	rows := make([]string, 0, len(options))
	current := selectedProtocolNames(pm.cfg.Enabled)
	for i, opt := range options {
		mark := "[ ]"
		if pm.selected[opt] {
			mark = "[x]"
		}
		status := ""
		if current[opt] {
			status = dimStyle.Render(" (installed)")
		}
		label := mark + " " + opt + status
		row := "  " + label
		if i == pm.cursor {
			row = selStyle.Render("> " + label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (pm *protocolManager) confirmView() string {
	t := pm.picker.selected()
	rows := []summaryLine{summaryRow("Target", t.badge())}
	switch pm.action {
	case protocolActionRealitySNI:
		rows = append(rows,
			summaryRow("Edit", "Reality SNI"),
			summaryRow("Current", or(pm.cfg.RealityServerName, "not set")),
			summaryRow("Selection", or(pm.values["reality_sni"], "not set")),
		)
	case protocolActionEdit:
		rows = append(rows, summaryRow("Edit", string(pm.editProto)))
		for _, f := range pm.fields {
			rows = append(rows, summaryRow(f.label, or(pm.values[f.key], "generate/keep current")))
		}
	default:
		current := pm.cfg.Enabled
		if !t.isLocal() && t.isNode() {
			current = t.node.EnabledProtocols
		}
		added, removed := protocolDiff(current, pm.targetProtocols())
		rows = append(rows,
			summaryRow("Current", protocolLabels(current)),
			summaryRow("Selection", protocolLabels(pm.targetProtocols())),
			summaryRow("Add", or(protocolStrings(added), "none")),
			summaryRow("Remove", or(protocolStrings(removed), "none")),
		)
		if len(pm.fields) > 0 {
			rows = append(rows, summaryBlank(), summaryText("New protocol parameters:"))
			for _, f := range pm.fields {
				rows = append(rows, summaryIndentedRow(2, f.label, or(pm.values[f.key], "generate/default")))
			}
		}
	}
	rows = append(rows, summaryBlank())
	switch {
	case t.isLocal():
		rows = append(rows, summaryText("This will regenerate sing-box config and all subscription files."))
	case t.isNode():
		rows = append(rows, summaryText("This will push the new config to node "+t.node.Alias+" and restart its sing-box service."))
	case t.isAll():
		rows = append(rows, summaryText("This will apply locally and broadcast the same config to every registered node."))
	}
	return flowTitle.Render("Protocol Management · Confirm") + "\n\n" + renderSummary(rows)
}

func (pm *protocolManager) runningView() string {
	return commandRunningView(pm, "Protocol Management · Running")
}

func (pm *protocolManager) failedView() string {
	return commandFailedView(pm, "Protocol management failed")
}

func (pm *protocolManager) doneSummary() string {
	t := pm.picker.selected()
	cfg := pm.result
	if len(cfg.Enabled) == 0 {
		cfg = pm.cfg
	}
	rows := []summaryLine{summaryRow("Target", t.badge())}
	if t.isLocal() {
		rows = append(rows,
			summaryRow("Protocols", protocolLabels(cfg.Enabled)),
			summaryRow("Ports", installedPortsSummary(cfg.Enabled, cfg.Ports)),
			summaryRow("Subscriptions", "refreshed"),
		)
	}
	if len(pm.agentOutcomes) > 0 {
		rows = append(rows, summaryBlank(), summaryText("Per-node outcomes:"))
		for _, o := range pm.agentOutcomes {
			label := o.node.Alias + " (" + o.node.WGIP + ")"
			value := "ok"
			if o.err != nil {
				value = "failed: " + o.err.Error()
			}
			rows = append(rows, summaryIndentedRow(2, label, value))
		}
	}
	return renderSummary(rows)
}

func (pm *protocolManager) footerHints() []operationHint {
	if pm.loadErr != nil {
		return returnFooterHints()
	}
	switch pm.phase {
	case protocolPhaseTarget:
		return actionFooterHints("Select")
	case protocolPhaseAction:
		if pm.picker.hasNodes() {
			return actionBackFooterHints("Select")
		}
		return actionFooterHints("Select")
	case protocolPhaseSelect:
		return []operationHint{hint(keyMove, "Move"), hint(keySpace, "Toggle"), hint(keyEnter, "Continue"), hint(keyBack, "Back"), hint(keyCancel, "Cancel")}
	case protocolPhaseEditPick:
		return actionBackFooterHints("Edit")
	case protocolPhaseForm:
		return pm.parameterForm.footerHints()
	case protocolPhaseConfirm:
		return applyFooterHints("Apply")
	case protocolPhaseRunning:
		return runningFooterHints(pm.runComplete)
	case protocolPhaseDone:
		return doneFooterHints(pm.runErr != nil)
	default:
		return nil
	}
}

func (pm *protocolManager) actions() []protocolActionItem {
	actions := []protocolActionItem{
		{action: protocolActionChange, label: "Install / remove protocols"},
		{action: protocolActionEdit, label: "Edit protocol credentials / ports"},
	}
	if needsRealityProtocol(pm.cfg.Enabled) {
		actions = append(actions, protocolActionItem{action: protocolActionRealitySNI, label: "Edit Reality SNI"})
	}
	return actions
}

func (pm *protocolManager) targetProtocols() []config.Protocol {
	return protocolsFromValue(selectedOptionsValue(protocolOptions(), pm.selected))
}

func sameProtocolSet(a, b []config.Protocol) bool {
	as, bs := protocolSet(a), protocolSet(b)
	if len(as) != len(bs) {
		return false
	}
	for p := range as {
		if !bs[p] {
			return false
		}
	}
	return true
}

func protocolSet(protocols []config.Protocol) map[config.Protocol]bool {
	set := map[config.Protocol]bool{}
	for _, p := range protocols {
		set[p] = true
	}
	return set
}

func selectedProtocolNames(protocols []config.Protocol) map[string]bool {
	set := map[string]bool{}
	for _, p := range protocols {
		set[string(p)] = true
	}
	return set
}

func protocolDiff(current, target []config.Protocol) (added, removed []config.Protocol) {
	cur, tgt := protocolSet(current), protocolSet(target)
	for _, p := range config.AllProtocols {
		if tgt[p] && !cur[p] {
			added = append(added, p)
		}
		if cur[p] && !tgt[p] {
			removed = append(removed, p)
		}
	}
	return added, removed
}

func protocolStrings(protocols []config.Protocol) string {
	parts := make([]string, 0, len(protocols))
	for _, p := range protocols {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, ", ")
}

func needsRealityProtocol(protocols []config.Protocol) bool {
	for _, p := range protocols {
		if p == config.ProtocolRealityVision || p == config.ProtocolRealityGRPC {
			return true
		}
	}
	return false
}
