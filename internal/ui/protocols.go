package ui

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
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
	protocolPhaseLoadingSpokeState
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
	protocolActionChangeSpoke
	protocolActionEditSpoke
	protocolActionRealitySNISpoke
)

var (
	protocolUILayout             = paths.DefaultLayout
	detectProtocolHost           = system.DetectHost
	updateProtocolsRun           = protocol.Update
	refreshProtocolSubscriptions = refreshHubSubscriptions
	applySpokeProtocolRun        = (*protocolManager).applySpokeProtocol
	fetchSpokeProtocolState      = func(ctx context.Context, node nodes.Node) (nodeapi.ProtocolStateResponse, error) {
		ctrl := &hubctl.Controller{Layout: protocolUILayout(), ExpectedVersion: toolVersion}
		checked, err := ctrl.CheckHealth(ctx, node, io.Discard)
		if err != nil {
			return nodeapi.ProtocolStateResponse{}, fmt.Errorf("reconcile Agent version: %w", err)
		}
		return ctrl.ProtocolState(ctx, checked)
	}
)

type protocolActionItem = actionItem[protocolAction]

type spokeProtocolStateMsg struct {
	loadID uint64
	nodeID string
	proto  config.Protocol
	state  nodeapi.ProtocolStateResponse
	err    error
}

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

	editProto      config.Protocol
	editNodeID     string
	editSpokeState nodeapi.ProtocolStateResponse
	haveSpokeState bool
	spokeStateStop context.CancelFunc
	spokeStateLoad uint64

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
	case spokeProtocolStateMsg:
		pm.handleSpokeProtocolState(msg)
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
				if pm.action == protocolActionChangeSpoke {
					pm.startSpokeSelector()
					return nil, false
				}
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
				return pm.startEditForm(), false
			},
			Back: func() (tea.Cmd, bool) {
				if pm.action == protocolActionEditSpoke {
					pm.startSpokeSelector()
					return nil, false
				}
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
	case protocolPhaseLoadingSpokeState:
		switch {
		case isSelectionBackKey(msg):
			pm.cancelSpokeStateLoad()
			pm.phase = protocolPhaseEditPick
			pm.fieldErr = ""
		case msg.String() == "esc", isSelectionCancelKey(msg):
			pm.cancelSpokeStateLoad()
			pm.phase = protocolPhaseEditPick
			return nil, true
		}
	case protocolPhaseForm:
		cmd, done, handled := pm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				if isSpokeProtocolAction(pm.action) && pm.editNodeID == "" {
					selectedLabel := pm.values["edit_spoke_select"]
					if node, ok := spokeNodeForLabel(pm.nodes, selectedLabel); ok {
						pm.editNodeID = node.ID
						pm.continueSpokeAction()
					} else {
						pm.fieldErr = "selected spoke no longer exists"
					}
					return
				}
				if pm.action == protocolActionEditSpoke {
					if _, _, err := spokeProtocolEditRegistryChange(pm.editProto, pm.values); err != nil {
						pm.fieldErr = err.Error()
						pm.parameterForm.backToLastField()
						return
					}
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
	pm.cancelSpokeStateLoad()
	pm.haveSpokeState = false
	pm.editSpokeState = nodeapi.ProtocolStateResponse{}
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
	case protocolActionChangeSpoke, protocolActionEditSpoke, protocolActionRealitySNISpoke:
		if len(pm.nodes) == 0 {
			pm.fieldErr = "no spoke nodes are registered; add one under Spoke → Spoke nodes"
			return
		}
		if !pm.canApply() {
			pm.fieldErr = pm.applyBlocker()
			return
		}
		pm.startSpokeSelector()
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
	installed := pm.installedProtocols()
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
	current := pm.installedProtocols()
	if sameProtocolSet(current, target) {
		pm.fieldErr = "selection is unchanged"
		return
	}
	if pm.action == protocolActionChangeSpoke {
		node, ok := pm.editSpokeNode()
		if !ok {
			pm.fieldErr = "selected spoke no longer exists"
			return
		}
		added, _ := protocolDiff(current, target)
		if needsRealityProtocol(added) && strings.TrimSpace(node.RealityServerName) == "" {
			pm.fieldErr = "set this spoke's Reality SNI with the separate Edit Reality SNI action before adding a Reality protocol"
			return
		}
		values := spokeProtocolValues(node)
		values["protocols"] = protocolSelectionValue(target)
		pm.startSpokeForm(pm.spokeInstallFieldsForAdded(current, target), values)
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

func (pm *protocolManager) startEditForm() tea.Cmd {
	if !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return nil
	}
	installed := pm.installedProtocols()
	idx, ok := selectedIndex(pm.cursor, len(installed))
	if !ok {
		pm.fieldErr = "no installed protocols"
		return nil
	}
	pm.editProto = installed[idx]
	if pm.action == protocolActionEditSpoke {
		node, ok := pm.editSpokeNode()
		if !ok {
			pm.fieldErr = "selected spoke no longer exists"
			return nil
		}
		pm.cancelSpokeStateLoad()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		pm.spokeStateStop = cancel
		pm.haveSpokeState = false
		pm.editSpokeState = nodeapi.ProtocolStateResponse{}
		pm.phase = protocolPhaseLoadingSpokeState
		loadID, nodeID, proto := pm.spokeStateLoad, node.ID, pm.editProto
		return func() tea.Msg {
			state, err := fetchSpokeProtocolState(ctx, node)
			return spokeProtocolStateMsg{loadID: loadID, nodeID: nodeID, proto: proto, state: state, err: err}
		}
	}
	pm.startForm(pm.editFields(pm.editProto))
	return nil
}

func (pm *protocolManager) handleSpokeProtocolState(msg spokeProtocolStateMsg) {
	if pm.phase != protocolPhaseLoadingSpokeState ||
		msg.loadID != pm.spokeStateLoad ||
		msg.nodeID != pm.editNodeID || msg.proto != pm.editProto {
		return
	}
	pm.cancelSpokeStateLoad()
	if msg.err != nil {
		pm.phase = protocolPhaseEditPick
		pm.fieldErr = "read current spoke protocol settings: " + msg.err.Error()
		return
	}
	node, ok := pm.editSpokeNode()
	if !ok {
		pm.phase = protocolPhaseEditPick
		pm.fieldErr = "selected spoke no longer exists"
		return
	}
	if err := validateSpokeProtocolState(node, msg.state); err != nil {
		pm.phase = protocolPhaseEditPick
		pm.fieldErr = err.Error()
		return
	}
	pm.editSpokeState = msg.state
	pm.haveSpokeState = true
	pm.startSpokeForm(spokeProtocolEditFields(pm.editProto), spokeProtocolEditValues(node, msg.state))
}

func (pm *protocolManager) cancelSpokeStateLoad() {
	if pm.spokeStateStop != nil {
		pm.spokeStateStop()
		pm.spokeStateStop = nil
	}
	pm.spokeStateLoad++
}

func (pm *protocolManager) startRealitySNIForm() {
	if !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	pm.action = protocolActionRealitySNI
	pm.startForm([]field{fieldFromParameter(uiparams.RealitySNIEditField(pm.cfg.RealityServerName))})
}

func (pm *protocolManager) startSpokeSelector() {
	pm.editNodeID = ""
	pm.startForm(pm.editSpokeSelectField())
}

func (pm *protocolManager) editSpokeSelectField() []field {
	return []field{{
		key:     "edit_spoke_select",
		label:   "Spoke to manage",
		options: spokeLabels(pm.nodes),
		note:    noteSpokeTransport,
	}}
}

func (pm *protocolManager) continueSpokeAction() {
	node, ok := pm.editSpokeNode()
	if !ok {
		pm.startSpokeSelector()
		pm.fieldErr = "selected spoke no longer exists"
		return
	}
	switch pm.action {
	case protocolActionChangeSpoke:
		pm.selected = selectedOptions(strings.Join(node.EnabledProtocols, ","))
		pm.cursor = 0
		pm.phase = protocolPhaseSelect
	case protocolActionEditSpoke:
		pm.cursor = 0
		pm.phase = protocolPhaseEditPick
	case protocolActionRealitySNISpoke:
		pm.startSpokeForm(
			[]field{fieldFromParameter(uiparams.RealitySNIEditField(node.RealityServerName))},
			spokeProtocolValues(node),
		)
	}
}

func (pm *protocolManager) spokeInstallFieldsForAdded(current, target []config.Protocol) []field {
	added, _ := protocolDiff(current, target)
	node, ok := pm.editSpokeNode()
	if !ok {
		return nil
	}
	fields := make([]field, 0, len(added))
	for _, proto := range added {
		field := spokePortEditField(proto, node)
		field.note = uiparams.NotePortListen
		fields = append(fields, field)
	}
	return fields
}

func spokePortEditField(proto config.Protocol, node nodes.Node) field {
	return field{
		key:   portFieldKey(proto),
		label: string(proto) + " listen port",
		def:   spokeProtocolPortValue(spokeProtocolPort(node, proto), proto),
		note:  uiparams.NotePortListen,
	}
}

func spokeProtocolEditFields(proto config.Protocol) []field {
	port := field{
		key: portFieldKey(proto), label: string(proto) + " listen port",
		note: uiparams.NotePortListen,
	}
	secret := func(key, label string) field {
		return field{key: key, label: label, secret: true}
	}
	switch proto {
	case config.ProtocolRealityVision:
		return []field{
			secret("reality_vision_uuid", "VLESS Reality Vision UUID"),
			port,
		}
	case config.ProtocolRealityGRPC:
		return []field{
			secret("reality_grpc_uuid", "VLESS Reality gRPC UUID"),
			port,
		}
	case config.ProtocolHysteria2:
		return []field{secret("hysteria2_password", "Hysteria2 password"), port}
	case config.ProtocolTUIC:
		return []field{
			secret("tuic_uuid", "TUIC UUID"),
			secret("tuic_password", "TUIC password"),
			port,
		}
	case config.ProtocolAnyTLS:
		return []field{secret("anytls_password", "AnyTLS password"), port}
	default:
		return nil
	}
}

func spokeProtocolValues(node nodes.Node) map[string]string {
	monitorPort := node.MonitorPort
	if monitorPort <= 0 {
		monitorPort = deploy.DefaultMonitorPort
	}
	return map[string]string{
		"protocols":           strings.Join(node.EnabledProtocols, ","),
		"reality_sni":         node.RealityServerName,
		"reality_vision_port": spokeProtocolPortValue(node.RealityVisionPort, config.ProtocolRealityVision),
		"reality_grpc_port":   spokeProtocolPortValue(node.RealityGRPCPort, config.ProtocolRealityGRPC),
		"hysteria2_port":      spokeProtocolPortValue(node.Hysteria2Port, config.ProtocolHysteria2),
		"tuic_port":           spokeProtocolPortValue(node.TUICPort, config.ProtocolTUIC),
		"anytls_port":         spokeProtocolPortValue(node.AnyTLSPort, config.ProtocolAnyTLS),
		"monitor":             yesNoString(node.Monitor),
		"monitor_port":        strconv.Itoa(monitorPort),
	}
}

func spokeProtocolEditValues(node nodes.Node, state nodeapi.ProtocolStateResponse) map[string]string {
	values := spokeProtocolValues(node)
	values["reality_sni"] = state.RealityServerName
	values["reality_handshake_port"] = strconv.Itoa(state.RealityHandshakePort)
	values["reality_vision_uuid"] = state.Credentials.RealityVisionUUID
	values["reality_grpc_uuid"] = state.Credentials.RealityGRPCUUID
	values["hysteria2_password"] = state.Credentials.HysteriaPassword
	values["tuic_uuid"] = state.Credentials.TUICUUID
	values["tuic_password"] = state.Credentials.TUICPassword
	values["anytls_password"] = state.Credentials.AnyTLSPassword
	values["reality_private_key"] = state.Credentials.RealityPrivateKey
	values["reality_public_key"] = state.Credentials.RealityPublicKey
	values["reality_short_id"] = state.Credentials.RealityShortID
	return values
}

func validateSpokeProtocolState(node nodes.Node, state nodeapi.ProtocolStateResponse) error {
	if state.Domain != node.Domain {
		return fmt.Errorf("spoke protocol state belongs to %q, expected %q", state.Domain, node.Domain)
	}
	nodeRealityHandshakePort := node.RealityHandshakePort
	if nodeRealityHandshakePort <= 0 {
		nodeRealityHandshakePort = config.DefaultRealityHandshakePort
	}
	if strings.Join(state.EnabledProtocols, ",") != strings.Join(node.EnabledProtocols, ",") ||
		state.RealityServerName != node.RealityServerName ||
		state.RealityHandshakePort != nodeRealityHandshakePort ||
		state.Ports.RealityVision != node.RealityVisionPort ||
		state.Ports.RealityGRPC != node.RealityGRPCPort ||
		state.Ports.Hysteria2 != node.Hysteria2Port ||
		state.Ports.TUIC != node.TUICPort ||
		state.Ports.AnyTLS != node.AnyTLSPort {
		return fmt.Errorf("spoke protocol state differs from the Hub registry; reconfigure the spoke before editing credentials")
	}
	return nodeapi.ValidateProtocolStateResponse(state)
}

func (pm *protocolManager) startSpokeForm(fields []field, values map[string]string) {
	pm.phase = protocolPhaseForm
	if pm.parameterForm.begin(fields, values, validateSpokeProtocolField) {
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
	if isSpokeProtocolAction(pm.action) {
		if pm.editNodeID == "" {
			pm.cursor = pm.actionCursor(pm.action)
			pm.phase = protocolPhaseAction
			return
		}
		switch pm.action {
		case protocolActionChangeSpoke:
			pm.phase = protocolPhaseSelect
		case protocolActionEditSpoke:
			pm.cursor = 0
			pm.phase = protocolPhaseEditPick
		case protocolActionRealitySNISpoke:
			pm.startSpokeSelector()
		}
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
	if isSpokeProtocolAction(pm.action) {
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
	var (
		change spokeRegistryChange
		err    error
	)
	switch pm.action {
	case protocolActionChangeSpoke:
		change, err = spokeProtocolSelectionRegistryChange(
			protocolsFromValue(strings.Join(node.EnabledProtocols, ",")),
			pm.values,
		)
	case protocolActionEditSpoke:
		if !pm.haveSpokeState {
			return fmt.Errorf("current spoke credentials were not loaded")
		}
		latest, fetchErr := fetchSpokeProtocolState(ctx, node)
		if fetchErr != nil {
			return fmt.Errorf("recheck current spoke protocol settings: %w", fetchErr)
		}
		if !reflect.DeepEqual(latest, pm.editSpokeState) {
			return fmt.Errorf("spoke protocol settings changed after this form was opened; reopen the editor to avoid overwriting newer credentials")
		}
		var targetPatch nodeapi.ProtocolPatch
		change, targetPatch, err = spokeProtocolEditRegistryChange(pm.editProto, pm.values)
		if err == nil {
			layout := protocolUILayout()
			ctrl := &hubctl.Controller{
				Layout: layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion,
				Progress: offsetRunProgress(progress, 1, 5),
			}
			rollbackCtrl := *ctrl
			rollbackCtrl.Progress = nil
			targetRevision, revisionErr := spokeProtocolTargetRevision(latest, targetPatch)
			if revisionErr != nil {
				return revisionErr
			}
			rollbackPatch, patchErr := spokeProtocolRollbackPatch(pm.editProto, latest)
			if patchErr != nil {
				return patchErr
			}
			revisionConflict := false
			return applySpokeRegistryReconfigure(
				ctx, layout, node.ID, logs, progress, change,
				func(ctx context.Context, node nodes.Node, log io.Writer) error {
					err := ctrl.PatchProtocolRevision(ctx, node, targetPatch, latest.Revision, log)
					revisionConflict = nodeapi.IsProtocolRevisionConflict(err)
					return err
				},
				func(ctx context.Context, node nodes.Node, log io.Writer) error {
					if revisionConflict {
						return rollbackCtrl.RefreshSubscriptions(ctx)
					}
					current, err := rollbackCtrl.ProtocolState(ctx, node)
					if err != nil {
						return fmt.Errorf("inspect spoke before credential rollback: %w", err)
					}
					switch current.Revision {
					case pm.editSpokeState.Revision:
						// The failed request did not commit any editable state.
						return rollbackCtrl.RefreshSubscriptions(ctx)
					case targetRevision:
						return rollbackCtrl.PatchProtocolRevision(ctx, node, rollbackPatch, targetRevision, log)
					default:
						return nodeapi.ProtocolRevisionConflict()
					}
				},
			)
		}
	case protocolActionRealitySNISpoke:
		change, err = spokeRealitySNIRegistryChange(pm.values)
	default:
		return fmt.Errorf("unsupported spoke protocol action")
	}
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
	latest, fetchErr := fetchSpokeProtocolState(ctx, node)
	if fetchErr != nil {
		return fmt.Errorf("read current spoke protocol settings before replacement: %w", fetchErr)
	}
	var targetRevision string
	revisionConflict := false
	return applySpokeRegistryReconfigure(
		ctx, layout, node.ID, logs, progress, change,
		func(ctx context.Context, updated nodes.Node, log io.Writer) error {
			var revisionErr error
			targetRevision, revisionErr = spokeProtocolNodeRevision(latest, updated)
			if revisionErr != nil {
				return revisionErr
			}
			err := ctrl.ReplaceProtocolStateRevision(ctx, updated, latest.Revision, log)
			revisionConflict = nodeapi.IsProtocolRevisionConflict(err)
			return err
		},
		func(ctx context.Context, restored nodes.Node, log io.Writer) error {
			if revisionConflict {
				return rollbackCtrl.RefreshSubscriptions(ctx)
			}
			current, err := rollbackCtrl.ProtocolState(ctx, restored)
			if err != nil {
				return fmt.Errorf("inspect spoke before protocol-state rollback: %w", err)
			}
			switch current.Revision {
			case latest.Revision:
				return rollbackCtrl.RefreshSubscriptions(ctx)
			case targetRevision:
				return rollbackCtrl.ReplaceProtocolStateRevision(ctx, restored, targetRevision, log)
			default:
				return nodeapi.ProtocolRevisionConflict()
			}
		},
	)
}

func spokeProtocolSelectionRegistryChange(currentProtocols []config.Protocol, values map[string]string) (spokeRegistryChange, error) {
	enabledProtocols := protocolsFromValue(values["protocols"])
	enabled := protocolStringSlice(enabledProtocols)
	if len(enabled) == 0 {
		return spokeRegistryChange{}, fmt.Errorf("select at least one protocol")
	}
	added, _ := protocolDiff(currentProtocols, enabledProtocols)
	addedPorts := make(map[config.Protocol]int, len(added))
	for _, proto := range added {
		port, err := parseSpokeProtocolPort(proto, values)
		if err != nil {
			return spokeRegistryChange{}, err
		}
		addedPorts[proto] = port
	}
	return spokeRegistryChange{
		Detail:     "install or remove spoke protocols while preserving credentials",
		Generation: spokeRegistryGenerationProtocol,
		Apply: func(current *nodes.Node) error {
			current.EnabledProtocols = append([]string(nil), enabled...)
			for proto, port := range addedPorts {
				setSpokeProtocolPort(current, proto, port)
			}
			return nil
		},
		Restore: func(current *nodes.Node, original, applied nodes.Node) {
			if reflect.DeepEqual(current.EnabledProtocols, applied.EnabledProtocols) {
				current.EnabledProtocols = append([]string(nil), original.EnabledProtocols...)
			}
			for _, proto := range added {
				if spokeProtocolPort(*current, proto) == spokeProtocolPort(applied, proto) {
					setSpokeProtocolPort(current, proto, spokeProtocolPort(original, proto))
				}
			}
		},
	}, nil
}

func spokeProtocolPortRegistryChange(proto config.Protocol, values map[string]string) (spokeRegistryChange, error) {
	port, err := parseSpokeProtocolPort(proto, values)
	if err != nil {
		return spokeRegistryChange{}, err
	}
	return spokeRegistryChange{
		Detail:     fmt.Sprintf("change the %s spoke listen port; preserve its credential", proto),
		Generation: spokeRegistryGenerationProtocol,
		Apply: func(current *nodes.Node) error {
			setSpokeProtocolPort(current, proto, port)
			return nil
		},
		Restore: func(current *nodes.Node, original, applied nodes.Node) {
			if spokeProtocolPort(*current, proto) == spokeProtocolPort(applied, proto) {
				setSpokeProtocolPort(current, proto, spokeProtocolPort(original, proto))
			}
		},
	}, nil
}

func spokeProtocolEditRegistryChange(
	proto config.Protocol,
	values map[string]string,
) (spokeRegistryChange, nodeapi.ProtocolPatch, error) {
	port, err := parseSpokeProtocolPort(proto, values)
	if err != nil {
		return spokeRegistryChange{}, nodeapi.ProtocolPatch{}, err
	}
	credentials := spokeProtocolCredentialsFromValues(values)
	patch := nodeapi.ProtocolPatch{
		Protocol:    string(proto),
		Port:        port,
		Credentials: credentials,
	}
	if err := nodeapi.ValidateProtocolPatch(patch); err != nil {
		return spokeRegistryChange{}, nodeapi.ProtocolPatch{}, err
	}
	return spokeRegistryChange{
		Detail:     fmt.Sprintf("change the %s spoke credential and listen port", proto),
		Generation: spokeRegistryGenerationProtocol,
		Apply: func(current *nodes.Node) error {
			setSpokeProtocolPort(current, proto, port)
			return nil
		},
		Restore: func(current *nodes.Node, original, applied nodes.Node) {
			if spokeProtocolPort(*current, proto) == spokeProtocolPort(applied, proto) {
				setSpokeProtocolPort(current, proto, spokeProtocolPort(original, proto))
			}
		},
	}, patch, nil
}

func spokeProtocolTargetRevision(
	current nodeapi.ProtocolStateResponse,
	patch nodeapi.ProtocolPatch,
) (string, error) {
	if err := applyProtocolPatchToState(&current, patch); err != nil {
		return "", err
	}
	current.Revision = ""
	return nodeapi.ProtocolStateRevision(current)
}

func spokeProtocolNodeRevision(
	current nodeapi.ProtocolStateResponse,
	node nodes.Node,
) (string, error) {
	enabled := deploy.CanonicalProtocols(protocolsFromValue(strings.Join(node.EnabledProtocols, ",")))
	if len(enabled) == 0 {
		return "", fmt.Errorf("select at least one protocol")
	}
	current.Revision = ""
	current.RealityServerName = node.RealityServerName
	current.RealityHandshakePort = node.RealityHandshakePort
	if current.RealityHandshakePort <= 0 {
		current.RealityHandshakePort = config.DefaultRealityHandshakePort
	}
	current.EnabledProtocols = protocolStringSlice(enabled)
	current.Ports = nodeapi.PortSet{
		RealityVision: node.RealityVisionPort,
		RealityGRPC:   node.RealityGRPCPort,
		Hysteria2:     node.Hysteria2Port,
		TUIC:          node.TUICPort,
		AnyTLS:        node.AnyTLSPort,
	}
	return nodeapi.ProtocolStateRevision(current)
}

func spokeProtocolRollbackPatch(
	proto config.Protocol,
	current nodeapi.ProtocolStateResponse,
) (nodeapi.ProtocolPatch, error) {
	patch := nodeapi.ProtocolPatch{
		Protocol:    string(proto),
		Credentials: current.Credentials,
	}
	switch proto {
	case config.ProtocolRealityVision:
		patch.Port = current.Ports.RealityVision
	case config.ProtocolRealityGRPC:
		patch.Port = current.Ports.RealityGRPC
	case config.ProtocolHysteria2:
		patch.Port = current.Ports.Hysteria2
	case config.ProtocolTUIC:
		patch.Port = current.Ports.TUIC
	case config.ProtocolAnyTLS:
		patch.Port = current.Ports.AnyTLS
	default:
		return nodeapi.ProtocolPatch{}, fmt.Errorf("unsupported protocol %q", proto)
	}
	if err := nodeapi.ValidateProtocolPatch(patch); err != nil {
		return nodeapi.ProtocolPatch{}, err
	}
	return patch, nil
}

func applyProtocolPatchToState(current *nodeapi.ProtocolStateResponse, patch nodeapi.ProtocolPatch) error {
	if current == nil {
		return fmt.Errorf("protocol state is required")
	}
	if err := nodeapi.ValidateProtocolPatch(patch); err != nil {
		return err
	}
	switch config.Protocol(patch.Protocol) {
	case config.ProtocolRealityVision:
		current.Ports.RealityVision = patch.Port
		current.Credentials.RealityVisionUUID = patch.Credentials.RealityVisionUUID
	case config.ProtocolRealityGRPC:
		current.Ports.RealityGRPC = patch.Port
		current.Credentials.RealityGRPCUUID = patch.Credentials.RealityGRPCUUID
	case config.ProtocolHysteria2:
		current.Ports.Hysteria2 = patch.Port
		current.Credentials.HysteriaPassword = patch.Credentials.HysteriaPassword
	case config.ProtocolTUIC:
		current.Ports.TUIC = patch.Port
		current.Credentials.TUICUUID = patch.Credentials.TUICUUID
		current.Credentials.TUICPassword = patch.Credentials.TUICPassword
	case config.ProtocolAnyTLS:
		current.Ports.AnyTLS = patch.Port
		current.Credentials.AnyTLSPassword = patch.Credentials.AnyTLSPassword
	default:
		return fmt.Errorf("unsupported protocol %q", patch.Protocol)
	}
	return nil
}

func spokeProtocolCredentialsFromValues(values map[string]string) nodeapi.ProtocolCredentials {
	return nodeapi.ProtocolCredentials{
		RealityVisionUUID: strings.TrimSpace(values["reality_vision_uuid"]),
		RealityGRPCUUID:   strings.TrimSpace(values["reality_grpc_uuid"]),
		HysteriaPassword:  strings.TrimSpace(values["hysteria2_password"]),
		TUICUUID:          strings.TrimSpace(values["tuic_uuid"]),
		TUICPassword:      strings.TrimSpace(values["tuic_password"]),
		AnyTLSPassword:    strings.TrimSpace(values["anytls_password"]),
		RealityPrivateKey: strings.TrimSpace(values["reality_private_key"]),
		RealityPublicKey:  strings.TrimSpace(values["reality_public_key"]),
		RealityShortID:    strings.TrimSpace(values["reality_short_id"]),
	}
}

func spokeRealitySNIRegistryChange(values map[string]string) (spokeRegistryChange, error) {
	realitySNI, err := uiparams.NormalizeRealityServerName(values["reality_sni"])
	if err != nil {
		return spokeRegistryChange{}, err
	}
	return spokeRegistryChange{
		Detail:     "change the spoke Reality SNI",
		Generation: spokeRegistryGenerationProtocol,
		Apply: func(current *nodes.Node) error {
			current.RealityServerName = realitySNI
			return nil
		},
		Restore: func(current *nodes.Node, original, applied nodes.Node) {
			if current.RealityServerName == applied.RealityServerName {
				current.RealityServerName = original.RealityServerName
			}
		},
	}, nil
}

func parseSpokeProtocolPort(proto config.Protocol, values map[string]string) (int, error) {
	key := portFieldKey(proto)
	if key == "" {
		return 0, fmt.Errorf("unsupported protocol %q", proto)
	}
	port, err := strconv.Atoi(strings.TrimSpace(values[key]))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s port must be between 1 and 65535", proto)
	}
	return port, nil
}

func spokeProtocolPort(node nodes.Node, proto config.Protocol) int {
	switch proto {
	case config.ProtocolRealityVision:
		return node.RealityVisionPort
	case config.ProtocolRealityGRPC:
		return node.RealityGRPCPort
	case config.ProtocolHysteria2:
		return node.Hysteria2Port
	case config.ProtocolTUIC:
		return node.TUICPort
	case config.ProtocolAnyTLS:
		return node.AnyTLSPort
	default:
		return 0
	}
}

func setSpokeProtocolPort(node *nodes.Node, proto config.Protocol, port int) {
	switch proto {
	case config.ProtocolRealityVision:
		node.RealityVisionPort = port
	case config.ProtocolRealityGRPC:
		node.RealityGRPCPort = port
	case config.ProtocolHysteria2:
		node.Hysteria2Port = port
	case config.ProtocolTUIC:
		node.TUICPort = port
	case config.ProtocolAnyTLS:
		node.AnyTLSPort = port
	}
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
		return flowTitle.Render(titleProtocols) + "\n\n" + flowErr.Render(pm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run Setup first.")
	}
	switch pm.phase {
	case protocolPhaseAction:
		return pm.actionView()
	case protocolPhaseSelect:
		return pm.selectView()
	case protocolPhaseEditPick:
		return pm.editPickView()
	case protocolPhaseLoadingSpokeState:
		return pm.loadingSpokeStateView()
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
		return flowOK.Render(titleProtocols+" complete") + "\n\n" + pm.doneSummary()
	default:
		return ""
	}
}

func (pm *protocolManager) loadingSpokeStateView() string {
	title := titleProtocols + " · Spoke · Loading settings"
	if node, ok := pm.editSpokeNode(); ok {
		title += " · " + node.EffectiveAlias()
	}
	return flowTitle.Render(title) + "\n\n" +
		dimStyle.Render("Reading the spoke's protocol settings…")
}

func (pm *protocolManager) actionView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(titleProtocols) + "\n\n")
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
	title := titleProtocols + " · Hub · Enabled protocols"
	if pm.action == protocolActionChangeSpoke {
		title = titleProtocols + " · Spoke · Enabled protocols"
		if node, ok := pm.editSpokeNode(); ok {
			title += " · " + node.EffectiveAlias()
		}
	}
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.installedProtocols()) + "\n")
	b.WriteString(dimStyle.Render("Target:  ") + protocolLabels(pm.targetProtocols()) + "\n")
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n" + pm.protocolOptionsView())
	return b.String()
}

func (pm *protocolManager) editPickView() string {
	var b strings.Builder
	title := titleProtocols + " · Hub · Edit"
	note := "Pick a protocol to edit its credentials and port."
	if pm.action == protocolActionEditSpoke {
		title = titleProtocols + " · Spoke · Edit"
		if node, ok := pm.editSpokeNode(); ok {
			title += " · " + node.EffectiveAlias()
		}
	}
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(dimStyle.Render(note) + "\n")
	if pm.fieldErr != "" {
		b.WriteString(flowErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, proto := range pm.installedProtocols() {
		port := uiparams.PortForProtocol(proto, pm.cfg.Ports)
		if node, ok := pm.editSpokeNode(); ok && pm.action == protocolActionEditSpoke {
			port = spokeProtocolPort(node, proto)
		}
		label := string(proto) + "  " + dimStyle.Render("port "+uiparams.PortDefault(port))
		row := "  " + label
		if i == pm.cursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (pm *protocolManager) formView() string {
	title := titleProtocols + " · Hub · Parameters"
	if pm.action == protocolActionEdit {
		title = titleProtocols + " · Hub · Edit " + string(pm.editProto)
	}
	if isSpokeProtocolAction(pm.action) {
		title = titleProtocols + " · Choose spoke"
		if node, ok := pm.editSpokeNode(); ok {
			switch pm.action {
			case protocolActionChangeSpoke:
				title = titleProtocols + " · Spoke · Enabled protocols · " + node.EffectiveAlias()
			case protocolActionEditSpoke:
				title = titleProtocols + " · Spoke · Edit " + string(pm.editProto) + " · " + node.EffectiveAlias()
			case protocolActionRealitySNISpoke:
				title = titleProtocols + " · Spoke · " + uiparams.LabelRealitySNI + " · " + node.EffectiveAlias()
			}
		}
	}
	return pm.parameterForm.View(title)
}

func (pm *protocolManager) protocolOptionsView() string {
	options := protocolOptions()
	rows := make([]string, 0, len(options))
	current := selectedProtocolNames(pm.installedProtocols())
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
	case protocolActionChangeSpoke:
		if node, ok := pm.editSpokeNode(); ok {
			current := protocolsFromValue(strings.Join(node.EnabledProtocols, ","))
			added, removed := protocolDiff(current, pm.targetProtocols())
			rows = append(rows,
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Stable node ID", node.ID),
				summaryRow("Action", "Enabled protocols"),
				summaryRow("Current protocols", protocolLabels(current)),
				summaryRow("Target protocols", protocolLabels(pm.targetProtocols())),
				summaryRow("Add", or(protocolStrings(added), "none")),
				summaryRow("Remove", or(protocolStrings(removed), "none")),
			)
			if len(pm.fields) > 0 {
				rows = append(rows, summaryBlank(), summaryText("Ports for newly installed protocols:"))
				for _, f := range pm.fields {
					rows = append(rows, summaryIndentedRow(2, f.label, pm.values[f.key]))
				}
			}
			rows = append(rows,
				summaryRow("Credentials", "preserve existing spoke credentials"),
				summaryRow("Transport", "authenticated Agent over WireGuard"),
			)
		}
	case protocolActionEditSpoke:
		if node, ok := pm.editSpokeNode(); ok {
			rows = append(rows,
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Stable node ID", node.ID),
				summaryRow("Edit", string(pm.editProto)+" complete settings"),
				summaryRow("Transport", "authenticated Agent over WireGuard"),
			)
			for _, f := range pm.fields {
				value := pm.values[f.key]
				if f.secret {
					value = "•••••••• (set)"
				}
				rows = append(rows, summaryRow(f.label, value))
			}
		}
	case protocolActionRealitySNISpoke:
		if node, ok := pm.editSpokeNode(); ok {
			rows = append(rows,
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Stable node ID", node.ID),
				summaryRow("Edit", uiparams.LabelRealitySNI),
				summaryRow("Current", or(node.RealityServerName, "not set")),
				summaryRow("Target", or(pm.values["reality_sni"], "not set")),
				summaryRow("Transport", "authenticated Agent over WireGuard"),
			)
		}
	case protocolActionRealitySNI:
		rows = append(rows,
			summaryRow("Target", "Hub"),
			summaryRow("Edit", uiparams.LabelRealitySNI),
			summaryRow("Current", or(pm.cfg.RealityServerName, "not set")),
			summaryRow("Target", or(pm.values["reality_sni"], "not set")),
		)
	case protocolActionEdit:
		rows = append(rows,
			summaryRow("Target", "Hub"),
			summaryRow("Edit", string(pm.editProto)),
		)
		for _, f := range pm.fields {
			value := or(pm.values[f.key], "generate/keep current")
			if f.secret && pm.values[f.key] != "" {
				value = "•••••••• (set)"
			}
			rows = append(rows, summaryRow(f.label, value))
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
				value := or(pm.values[f.key], "generate/default")
				if f.secret && pm.values[f.key] != "" {
					value = "•••••••• (set)"
				}
				rows = append(rows, summaryIndentedRow(2, f.label, value))
			}
		}
	}
	rows = append(rows,
		summaryBlank(),
		summaryText("Regenerates the sing-box config and all subscription files on the selected host."),
	)
	return flowTitle.Render(titleProtocols+" · Confirm") + "\n\n" + renderSummary(rows)
}

func (pm *protocolManager) runningView() string {
	return commandRunningView(pm, titleProtocols+" · Running")
}

func (pm *protocolManager) failedView() string {
	return commandFailedView(pm, "Protocol management failed")
}

func (pm *protocolManager) doneSummary() string {
	if isSpokeProtocolAction(pm.action) {
		if node, ok := pm.editSpokeNode(); ok {
			rows := []summaryLine{
				summaryRow("Target", "Spoke"),
				summaryRow("Spoke", spokeOptionLabel(node)),
				summaryRow("Subscriptions", "refreshed on Hub"),
			}
			switch pm.action {
			case protocolActionChangeSpoke:
				rows = append(rows,
					summaryRow("Protocols", protocolLabels(protocolsFromValue(strings.Join(node.EnabledProtocols, ",")))),
					summaryRow("Credentials", "preserved"),
				)
			case protocolActionEditSpoke:
				rows = append(rows,
					summaryRow("Protocol", string(pm.editProto)),
					summaryRow("Port", strconv.Itoa(spokeProtocolPort(node, pm.editProto))),
					summaryRow("Settings", "updated; credentials remain masked"),
				)
			case protocolActionRealitySNISpoke:
				rows = append(rows, summaryRow(uiparams.LabelRealitySNI, node.RealityServerName))
			}
			return renderSummary(rows)
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
	case protocolPhaseLoadingSpokeState:
		return []operationHint{hint(keyBack, "Back"), hint(keyCancel, "Cancel")}
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
		{action: protocolActionChange, label: "Hub · Enabled protocols"},
		{action: protocolActionEdit, label: "Hub · Edit protocol settings"},
	}
	if needsRealityProtocol(pm.cfg.Enabled) {
		actions = append(actions, protocolActionItem{action: protocolActionRealitySNI, label: "Hub · Edit Reality SNI"})
	}
	actions = append(actions,
		protocolActionItem{separator: true, label: "Spokes"},
		protocolActionItem{action: protocolActionChangeSpoke, label: "Spoke · Enabled protocols"},
		protocolActionItem{action: protocolActionEditSpoke, label: "Spoke · Edit protocol settings"},
	)
	if len(pm.nodes) > 0 {
		actions = append(actions, protocolActionItem{action: protocolActionRealitySNISpoke, label: "Spoke · Edit Reality SNI"})
	}
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

func isSpokeProtocolAction(action protocolAction) bool {
	switch action {
	case protocolActionChangeSpoke, protocolActionEditSpoke, protocolActionRealitySNISpoke:
		return true
	default:
		return false
	}
}

func (pm *protocolManager) installedProtocols() []config.Protocol {
	if isSpokeProtocolAction(pm.action) {
		if node, ok := pm.editSpokeNode(); ok {
			return protocolsFromValue(strings.Join(node.EnabledProtocols, ","))
		}
	}
	return pm.cfg.Enabled
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
