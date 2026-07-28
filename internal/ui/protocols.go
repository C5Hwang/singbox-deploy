package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/protocol"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type protocolPhase int

const (
	protocolPhaseAction protocolPhase = iota
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
	protocolActionEditSpoke
)

var (
	protocolUILayout             = paths.DefaultLayout
	detectProtocolHost           = system.DetectHost
	updateProtocolsRun           = protocol.Update
	refreshProtocolSubscriptions = refreshHubSubscriptions
	applySpokeProtocolRun        = (*protocolManager).applySpokeProtocol
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
	nodes   []nodes.Node
	loadErr error

	cursor   int
	selected map[string]bool
	parameterForm

	editProto  config.Protocol
	editNodeID string

	commandRun
	result deploy.Config
}

func newProtocolManager() *protocolManager {
	pm := &protocolManager{
		phase:         protocolPhaseAction,
		cursor:        1,
		selected:      map[string]bool{},
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	host, err := detectProtocolHost()
	pm.host = host
	pm.hostErr = err
	layout := protocolUILayout()
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		pm.loadErr = err
		return pm
	}
	pm.cfg = cfg
	list, err := nodes.Load(layout)
	if err != nil {
		pm.loadErr = err
		return pm
	}
	pm.nodes = list
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
	case protocolPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: pm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				pm.activateAction()
				return nil, false
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
				// Reset the shared cursor: it was moved within the longer
				// protocol list and would otherwise land out of range on the
				// action list (no highlight, Enter hits a clamped item).
				pm.cursor = pm.actionCursor(protocolActionChange)
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
				pm.cursor = pm.actionCursor(protocolActionEdit)
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
			Complete: func() {
				if pm.action == protocolActionEditSpoke && pm.editNodeID == "" {
					selectedLabel := pm.values["edit_spoke_select"]
					if node, ok := spokeNodeForLabel(pm.nodes, selectedLabel); ok {
						pm.editNodeID = node.ID
						pm.startEditSpokeForm()
					} else {
						pm.fieldErr = "selected spoke no longer exists"
					}
					return
				}
				pm.phase = protocolPhaseConfirm
			},
			Back: pm.previousField,
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
		if msg.String() == "enter" && pm.runComplete {
			layout := protocolUILayout()
			if cfg, err := deploy.LoadProtocolConfig(layout); err == nil {
				pm.cfg = cfg
				pm.result = cfg
				pm.selected = selectedOptions(protocolSelectionValue(cfg.Enabled))
			}
			if list, err := nodes.Load(layout); err == nil {
				pm.nodes = list
			}
			pm.phase = protocolPhaseDone
		} else {
			pm.handleScrollKey(msg.String(), pm.logViewportHeight())
		}
	case protocolPhaseDone:
		return pm.handleDoneKey(msg.String())
	}
	return nil, false
}

func (pm *protocolManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	pm.handleLogWheel(msg.Button, pm.phase == protocolPhaseRunning || (pm.phase == protocolPhaseDone && pm.runErr != nil))
	return nil
}

func (pm *protocolManager) moveAction(delta int) {
	pm.cursor = moveActionCursor(pm.cursor, pm.actions(), delta)
	pm.fieldErr = ""
}

func (pm *protocolManager) activateAction() {
	pm.fieldErr = ""
	pm.editNodeID = ""
	actions := pm.actions()
	idx, ok := selectedIndex(pm.cursor, len(actions))
	if !ok {
		return
	}
	pm.action = actions[idx].action
	switch actions[idx].action {
	case protocolActionChange:
		pm.phase = protocolPhaseSelect
		pm.cursor = 0
		pm.selected = selectedOptions(protocolSelectionValue(pm.cfg.Enabled))
	case protocolActionEdit:
		pm.phase = protocolPhaseEditPick
		pm.cursor = 0
	case protocolActionRealitySNI:
		pm.startRealitySNIForm()
	case protocolActionEditSpoke:
		if len(pm.nodes) == 0 {
			pm.fieldErr = "no spoke nodes are registered; add one under Spoke → Spoke nodes"
			return
		}
		if !pm.canApply() {
			pm.fieldErr = pm.applyBlocker()
			return
		}
		pm.startForm(pm.editSpokeSelectField())
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
	if !pm.canApply() {
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
	if !pm.canApply() {
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
	if !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	pm.action = protocolActionRealitySNI
	pm.startForm([]field{fieldFromParameter(uiparams.RealitySNIEditField(pm.cfg.RealityServerName))})
}

func (pm *protocolManager) editSpokeSelectField() []field {
	return []field{{
		key:     "edit_spoke_select",
		label:   "Spoke protocol settings to edit",
		options: spokeLabels(pm.nodes),
		note:    "The stable node ID identifies the spoke; changes are delivered through its authenticated WireGuard Agent.",
	}}
}

func (pm *protocolManager) startEditSpokeForm() {
	node, ok := pm.editSpokeNode()
	if !ok {
		pm.editNodeID = ""
		pm.startForm(pm.editSpokeSelectField())
		pm.fieldErr = "selected spoke no longer exists"
		return
	}
	protocols := strings.Join(node.EnabledProtocols, ",")
	if protocols == "" {
		protocols = defaultProtocolValue()
	}
	noReality := func(values map[string]string) bool {
		return !protocolSelected(values, config.ProtocolRealityVision) &&
			!protocolSelected(values, config.ProtocolRealityGRPC)
	}
	missingProtocol := func(proto config.Protocol) func(map[string]string) bool {
		return func(values map[string]string) bool { return !protocolSelected(values, proto) }
	}
	fields := []field{{
		key: "protocols", label: "Enabled spoke protocols", options: protocolOptions(), multi: true,
		note: "Only protocol selection, Reality SNI, and listen ports change. Existing spoke credentials are preserved.",
	}}
	realitySNI := fieldFromParameter(uiparams.RealitySNIEditField(or(node.RealityServerName, defaultRealityServerName)))
	realitySNI.skip = noReality
	fields = append(fields, realitySNI)
	for _, proto := range config.AllProtocols {
		portField := field{
			key:   portFieldKey(proto),
			label: string(proto) + " listen port",
			skip:  missingProtocol(proto),
			note:  "Public listen port on this spoke; its existing credential is retained.",
		}
		fields = append(fields, portField)
	}
	monitorPort := node.MonitorPort
	if monitorPort <= 0 {
		monitorPort = deploy.DefaultMonitorPort
	}
	pm.phase = protocolPhaseForm
	if pm.parameterForm.begin(fields, map[string]string{
		"protocols":           protocols,
		"reality_sni":         or(node.RealityServerName, defaultRealityServerName),
		"reality_vision_port": spokeProtocolPortValue(node.RealityVisionPort, config.ProtocolRealityVision),
		"reality_grpc_port":   spokeProtocolPortValue(node.RealityGRPCPort, config.ProtocolRealityGRPC),
		"hysteria2_port":      spokeProtocolPortValue(node.Hysteria2Port, config.ProtocolHysteria2),
		"tuic_port":           spokeProtocolPortValue(node.TUICPort, config.ProtocolTUIC),
		"anytls_port":         spokeProtocolPortValue(node.AnyTLSPort, config.ProtocolAnyTLS),
		"monitor":             yesNoString(node.Monitor),
		"monitor_port":        strconv.Itoa(monitorPort),
	}, validateSpokeProtocolField) {
		pm.phase = protocolPhaseConfirm
	}
}

func (pm *protocolManager) startForm(fields []field) {
	pm.phase = protocolPhaseForm
	if pm.parameterForm.begin(fields, nil, validateProtocolParameterField) {
		pm.phase = protocolPhaseConfirm
	}
}

func validateProtocolParameterField(f field, val string, vals map[string]string) error {
	return uiparams.ValidateProtocolParameterField(parameterFromField(f), val, vals)
}

func validateSpokeProtocolField(f field, val string, vals map[string]string) error {
	if f.key == "protocols" && len(protocolsFromValue(val)) == 0 {
		return fmt.Errorf("select at least one protocol")
	}
	if err := uiparams.ValidateProtocolParameterField(parameterFromField(f), val, vals); err != nil {
		return err
	}
	return validateInstallPortConflict(f.key, val, vals)
}

func (pm *protocolManager) previousField() {
	if pm.parameterForm.previousField() {
		return
	}
	if pm.action == protocolActionEditSpoke {
		if pm.editNodeID != "" {
			pm.editNodeID = ""
			pm.startForm(pm.editSpokeSelectField())
			return
		}
		pm.cursor = pm.actionCursor(protocolActionEditSpoke)
		pm.phase = protocolPhaseAction
		return
	}
	if pm.action == protocolActionEdit {
		pm.cursor = 0
		pm.phase = protocolPhaseEditPick
		return
	}
	pm.cursor = pm.actionCursor(pm.action)
	pm.phase = protocolPhaseAction
	if pm.action == protocolActionChange {
		pm.phase = protocolPhaseSelect
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

func (pm *protocolManager) canApply() bool { return hostCanApply(pm.host, pm.hostErr) }

func (pm *protocolManager) applyBlocker() string {
	return hostApplyBlocker(pm.host, pm.hostErr,
		"protocol changes must be run as root",
		"SELinux is enforcing; protocol changes are blocked",
		"cannot apply protocol changes")
}

func (pm *protocolManager) startRun() tea.Cmd {
	pm.phase = protocolPhaseRunning
	pm.resetRun(make(chan runMsg, 64))
	ch := pm.ch
	logs := &logWriter{ch: ch}
	if pm.action == protocolActionEditSpoke {
		progress := runProgressSender(ch)
		go func() {
			err := applySpokeProtocolRun(pm, context.Background(), logs, progress)
			ch <- runMsg{done: true, err: err}
		}()
		return pm.waitForRun()
	}
	opts := pm.updateOptions()
	opts.Layout = protocolUILayout()
	opts.Runner = system.NewExecRunner(logs)
	opts.Firewall = pm.host.Firewall
	opts.Progress = func(e deploy.Event) {
		ev := e
		ch <- runMsg{event: &ev}
	}
	go func() {
		_, err := updateProtocolsRun(context.Background(), opts)
		if err == nil {
			refreshProtocolSubscriptions(logs)
		}
		ch <- runMsg{done: true, err: err}
	}()
	return pm.waitForRun()
}

func (pm *protocolManager) applySpokeProtocol(ctx context.Context, logs *logWriter, progress func(deploy.Event)) error {
	node, ok := pm.editSpokeNode()
	if !ok {
		return fmt.Errorf("selected spoke no longer exists")
	}
	change, err := spokeProtocolRegistryChange(pm.values)
	if err != nil {
		return err
	}

	layout := protocolUILayout()
	ctrl := &hubctl.Controller{
		Layout: layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion,
		Progress: offsetRunProgress(progress, 1, 5),
	}
	rollbackCtrl := *ctrl
	rollbackCtrl.Progress = nil
	return applySpokeRegistryReconfigure(
		ctx, layout, node.ID, logs, progress, change,
		ctrl.Reconfigure,
		rollbackCtrl.Reconfigure,
	)
}

func spokeProtocolRegistryChange(values map[string]string) (spokeRegistryChange, error) {
	enabledProtocols := protocolsFromValue(values["protocols"])
	enabled := protocolStringSlice(enabledProtocols)
	if len(enabled) == 0 {
		return spokeRegistryChange{}, fmt.Errorf("select at least one protocol")
	}
	realitySNI, err := uiparams.NormalizeRealityServerName(values["reality_sni"])
	if err != nil && needsRealityProtocol(enabledProtocols) {
		return spokeRegistryChange{}, err
	}
	ports := map[string]int{}
	for _, proto := range config.AllProtocols {
		key := portFieldKey(proto)
		port, parseErr := strconv.Atoi(strings.TrimSpace(values[key]))
		if parseErr != nil || port < 1 || port > 65535 {
			return spokeRegistryChange{}, fmt.Errorf("%s port must be between 1 and 65535", proto)
		}
		ports[key] = port
	}
	return spokeRegistryChange{
		Detail: "save the requested spoke protocol settings",
		Apply: func(current *nodes.Node) error {
			current.EnabledProtocols = append([]string(nil), enabled...)
			if realitySNI != "" {
				current.RealityServerName = realitySNI
			}
			current.RealityVisionPort = ports["reality_vision_port"]
			current.RealityGRPCPort = ports["reality_grpc_port"]
			current.Hysteria2Port = ports["hysteria2_port"]
			current.TUICPort = ports["tuic_port"]
			current.AnyTLSPort = ports["anytls_port"]
			return nil
		},
		Restore: func(current *nodes.Node, original nodes.Node) {
			current.EnabledProtocols = append([]string(nil), original.EnabledProtocols...)
			current.RealityServerName = original.RealityServerName
			current.RealityVisionPort = original.RealityVisionPort
			current.RealityGRPCPort = original.RealityGRPCPort
			current.Hysteria2Port = original.Hysteria2Port
			current.TUICPort = original.TUICPort
			current.AnyTLSPort = original.AnyTLSPort
		},
	}, nil
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
	b.WriteString(dimStyle.Render("Current: Hub · ") + protocolLabels(pm.cfg.Enabled) + "\n")
	b.WriteString(dimStyle.Render("Registered spokes: ") + strconv.Itoa(len(pm.nodes)) + "\n")
	if !pm.canApply() {
		b.WriteString(flowErr.Render(pm.applyBlocker()) + "\n")
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
	b.WriteString(flowTitle.Render("Protocol Management · Hub · Install / Remove") + "\n\n")
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
	b.WriteString(flowTitle.Render("Protocol Management · Hub · Edit") + "\n\n")
	b.WriteString(dimStyle.Render("Choose an installed Hub protocol to edit its uuid/password and port.") + "\n")
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, proto := range pm.cfg.Enabled {
		label := string(proto) + "  " + dimStyle.Render("port "+uiparams.PortDefault(uiparams.PortForProtocol(proto, pm.cfg.Ports)))
		row := "  " + label
		if i == pm.cursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (pm *protocolManager) formView() string {
	title := "Protocol Management · Hub · Parameters"
	if pm.action == protocolActionEdit {
		title = "Protocol Management · Hub · Edit " + string(pm.editProto)
	}
	if pm.action == protocolActionEditSpoke {
		title = "Protocol Management · Choose Spoke"
		if node, ok := pm.editSpokeNode(); ok {
			title = "Protocol Management · Spoke · " + node.EffectiveAlias()
		}
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
	var rows []summaryLine
	switch pm.action {
	case protocolActionEditSpoke:
		if node, ok := pm.editSpokeNode(); ok {
			rows = append(rows,
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Stable node ID", node.ID),
				summaryRow("Current protocols", protocolLabels(protocolsFromValue(strings.Join(node.EnabledProtocols, ",")))),
				summaryRow("Target protocols", protocolLabels(protocolsFromValue(pm.values["protocols"]))),
				summaryRow("Reality SNI", pm.values["reality_sni"]),
				summaryRow("Reality Vision port", pm.values["reality_vision_port"]),
				summaryRow("Reality gRPC port", pm.values["reality_grpc_port"]),
				summaryRow("Hysteria2 port", pm.values["hysteria2_port"]),
				summaryRow("TUIC port", pm.values["tuic_port"]),
				summaryRow("AnyTLS port", pm.values["anytls_port"]),
				summaryRow("Credentials", "preserve existing spoke credentials"),
				summaryRow("Transport", "authenticated Agent over WireGuard"),
			)
		}
	case protocolActionRealitySNI:
		rows = append(rows,
			summaryRow("Target", "Hub"),
			summaryRow("Edit", "Reality SNI"),
			summaryRow("Current", or(pm.cfg.RealityServerName, "not set")),
			summaryRow("Target", or(pm.values["reality_sni"], "not set")),
		)
	case protocolActionEdit:
		rows = append(rows,
			summaryRow("Target", "Hub"),
			summaryRow("Edit", string(pm.editProto)),
		)
		for _, f := range pm.fields {
			rows = append(rows, summaryRow(f.label, or(pm.values[f.key], "generate/keep current")))
		}
	default:
		added, removed := protocolDiff(pm.cfg.Enabled, pm.targetProtocols())
		rows = append(rows,
			summaryRow("Target host", "Hub"),
			summaryRow("Current", protocolLabels(pm.cfg.Enabled)),
			summaryRow("Target", protocolLabels(pm.targetProtocols())),
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
	rows = append(rows,
		summaryBlank(),
		summaryText("This will regenerate sing-box config and all subscription files on the selected host."),
	)
	return flowTitle.Render("Protocol Management · Confirm") + "\n\n" + renderSummary(rows)
}

func (pm *protocolManager) runningView() string {
	return commandRunningView(pm, "Protocol Management · Running")
}

func (pm *protocolManager) failedView() string {
	return commandFailedView(pm, "Protocol management failed")
}

func (pm *protocolManager) doneSummary() string {
	if pm.action == protocolActionEditSpoke {
		if node, ok := pm.editSpokeNode(); ok {
			return renderSummary([]summaryLine{
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Protocols", protocolLabels(protocolsFromValue(strings.Join(node.EnabledProtocols, ",")))),
				summaryRow("Credentials", "preserved"),
				summaryRow("Subscriptions", "refreshed on Hub"),
			})
		}
	}
	cfg := pm.result
	if len(cfg.Enabled) == 0 {
		cfg = pm.cfg
	}
	return renderSummary([]summaryLine{
		summaryRow("Protocols", protocolLabels(cfg.Enabled)),
		summaryRow("Ports", installedPortsSummary(cfg.Enabled, cfg.Ports)),
		summaryRow("Subscriptions", "refreshed"),
	})
}

func (pm *protocolManager) footerHints() []operationHint {
	if pm.loadErr != nil {
		return returnFooterHints()
	}
	switch pm.phase {
	case protocolPhaseAction:
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
		{separator: true, label: "Hub"},
		{action: protocolActionChange, label: "Hub · Install / remove protocols"},
		{action: protocolActionEdit, label: "Hub · Edit protocol credentials / ports"},
	}
	if needsRealityProtocol(pm.cfg.Enabled) {
		actions = append(actions, protocolActionItem{action: protocolActionRealitySNI, label: "Hub · Edit Reality SNI"})
	}
	actions = append(actions,
		protocolActionItem{separator: true, label: "Spokes (WireGuard)"},
		protocolActionItem{action: protocolActionEditSpoke, label: "Spoke · Edit protocols / SNI / ports"},
	)
	return actions
}

func (pm *protocolManager) actionCursor(action protocolAction) int {
	for i, item := range pm.actions() {
		if !item.separator && item.action == action {
			return i
		}
	}
	return 1
}

func (pm *protocolManager) editSpokeNode() (nodes.Node, bool) {
	for _, node := range pm.nodes {
		if node.ID == pm.editNodeID {
			return node, true
		}
	}
	return nodes.Node{}, false
}

func spokeNodeForLabel(list []nodes.Node, label string) (nodes.Node, bool) {
	for _, node := range list {
		if spokeOptionLabel(node) == label {
			return node, true
		}
	}
	return nodes.Node{}, false
}

func spokeProtocolPortValue(port int, proto config.Protocol) string {
	if port <= 0 {
		port = defaultSpokePort(proto)
	}
	return strconv.Itoa(port)
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
	return strings.Join(protocolStringSlice(protocols), ", ")
}

func protocolStringSlice(protocols []config.Protocol) []string {
	parts := make([]string, 0, len(protocols))
	for _, p := range protocols {
		parts = append(parts, string(p))
	}
	return parts
}

func needsRealityProtocol(protocols []config.Protocol) bool {
	for _, p := range protocols {
		if p == config.ProtocolRealityVision || p == config.ProtocolRealityGRPC {
			return true
		}
	}
	return false
}
