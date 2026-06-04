package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/install"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
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
)

var (
	protocolUILayout   = paths.DefaultLayout
	detectProtocolHost = system.DetectHost
	updateProtocolsRun = install.UpdateProtocols
)

type protocolField struct {
	key   string
	label string
	def   string
	note  string
}

type protocolActionItem struct {
	action protocolAction
	label  string
}

type protocolManager struct {
	phase  protocolPhase
	action protocolAction

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     install.Config
	loadErr error

	cursor   int
	selected map[string]bool
	fieldErr string

	fields  []protocolField
	fieldIx int
	values  map[string]string
	input   textinput.Model

	editProto config.Protocol

	bar         progress.Model
	events      []install.Event
	logBuf      []string
	logScroll   int
	runErr      error
	runComplete bool
	result      install.Config
	ch          chan runMsg
}

func newProtocolManager() *protocolManager {
	input := textinput.New()
	input.CharLimit = 256
	input.Prompt = "› "

	pm := &protocolManager{
		phase:    protocolPhaseAction,
		selected: map[string]bool{},
		values:   map[string]string{},
		input:    input,
		bar:      progress.New(progress.WithDefaultGradient()),
	}
	host, err := detectProtocolHost()
	pm.host = host
	pm.hostErr = err
	cfg, err := install.LoadProtocolConfig(protocolUILayout())
	if err != nil {
		pm.loadErr = err
		return pm
	}
	pm.cfg = cfg
	pm.selected = selectedOptions(protocolsValue(cfg.Enabled))
	return pm
}

func (pm *protocolManager) setSize(width, height int) {
	pm.width = width
	pm.height = height
	pm.bar.Width = min(width-4, 60)
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
		switch msg.String() {
		case "esc", "q", "enter":
			return nil, true
		}
		return nil, false
	}
	switch pm.phase {
	case protocolPhaseAction:
		switch msg.String() {
		case "up", "k", "left", "h":
			pm.moveAction(-1)
		case "down", "j", "right", "l":
			pm.moveAction(1)
		case "enter":
			pm.activateAction()
		case "esc", "q":
			return nil, true
		}
	case protocolPhaseSelect:
		switch msg.String() {
		case "up", "k", "left", "h":
			pm.moveProtocol(-1)
		case "down", "j", "right", "l":
			pm.moveProtocol(1)
		case " ", "space":
			pm.toggleProtocol()
		case "enter":
			pm.prepareChangeConfirm()
		case "shift+tab", "ctrl+b":
			pm.phase = protocolPhaseAction
		case "esc", "q":
			return nil, true
		}
	case protocolPhaseEditPick:
		switch msg.String() {
		case "up", "k", "left", "h":
			pm.moveInstalled(-1)
		case "down", "j", "right", "l":
			pm.moveInstalled(1)
		case "enter":
			pm.startEditForm()
		case "shift+tab", "ctrl+b":
			pm.phase = protocolPhaseAction
		case "esc", "q":
			return nil, true
		}
	case protocolPhaseForm:
		switch msg.String() {
		case "enter":
			pm.commitField()
		case "shift+tab", "ctrl+b":
			pm.previousField()
		case "esc":
			return nil, true
		default:
			pm.fieldErr = ""
			var cmd tea.Cmd
			pm.input, cmd = pm.input.Update(msg)
			return cmd, false
		}
	case protocolPhaseConfirm:
		switch msg.String() {
		case "enter", "y":
			return pm.startRun(), false
		case "shift+tab", "ctrl+b":
			pm.backFromConfirm()
		case "esc", "n":
			return nil, true
		}
	case protocolPhaseRunning:
		switch msg.String() {
		case "enter":
			if pm.runComplete {
				if cfg, err := install.LoadProtocolConfig(protocolUILayout()); err == nil {
					pm.cfg = cfg
					pm.result = cfg
					pm.selected = selectedOptions(protocolsValue(cfg.Enabled))
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
	actions := pm.actions()
	pm.cursor = (pm.cursor + delta + len(actions)) % len(actions)
	pm.fieldErr = ""
}

func (pm *protocolManager) activateAction() {
	pm.fieldErr = ""
	actions := pm.actions()
	if len(actions) == 0 {
		return
	}
	switch actions[min(max(0, pm.cursor), len(actions)-1)].action {
	case protocolActionChange:
		pm.action = protocolActionChange
		pm.phase = protocolPhaseSelect
		pm.cursor = 0
		pm.selected = selectedOptions(protocolsValue(pm.cfg.Enabled))
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
	pm.cursor = (pm.cursor + delta + len(options)) % len(options)
	pm.fieldErr = ""
}

func (pm *protocolManager) toggleProtocol() {
	options := protocolOptions()
	opt := options[min(max(0, pm.cursor), len(options)-1)]
	if pm.selected[opt] {
		delete(pm.selected, opt)
	} else {
		pm.selected[opt] = true
	}
	pm.fieldErr = ""
}

func (pm *protocolManager) moveInstalled(delta int) {
	installed := pm.cfg.Enabled
	pm.cursor = (pm.cursor + delta + len(installed)) % len(installed)
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
		pm.values = map[string]string{}
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
	if len(installed) == 0 {
		pm.fieldErr = "no installed protocols"
		return
	}
	pm.editProto = installed[min(max(0, pm.cursor), len(installed)-1)]
	pm.startForm(pm.editFields(pm.editProto))
}

func (pm *protocolManager) startRealitySNIForm() {
	if !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return
	}
	pm.action = protocolActionRealitySNI
	pm.startForm([]protocolField{realitySNIEditField(pm.cfg.RealityServerName)})
}

func (pm *protocolManager) startForm(fields []protocolField) {
	pm.fields = fields
	pm.values = map[string]string{}
	pm.fieldIx = -1
	pm.phase = protocolPhaseForm
	pm.advanceField()
}

func (pm *protocolManager) advanceField() {
	pm.fieldIx++
	if pm.fieldIx >= len(pm.fields) {
		pm.fieldErr = ""
		pm.phase = protocolPhaseConfirm
		return
	}
	f := pm.fields[pm.fieldIx]
	pm.input.SetValue(pm.values[f.key])
	pm.input.Placeholder = f.def
	pm.input.Focus()
	pm.fieldErr = ""
}

func (pm *protocolManager) previousField() {
	if pm.fieldIx <= 0 {
		if pm.action == protocolActionEdit {
			pm.phase = protocolPhaseEditPick
		} else {
			pm.phase = protocolPhaseAction
			if pm.action == protocolActionChange {
				pm.phase = protocolPhaseSelect
			}
		}
		return
	}
	pm.saveFieldDraft()
	pm.fieldIx--
	f := pm.fields[pm.fieldIx]
	pm.input.SetValue(pm.values[f.key])
	pm.input.Placeholder = f.def
	pm.input.Focus()
	pm.fieldErr = ""
}

func (pm *protocolManager) saveFieldDraft() {
	if pm.fieldIx < 0 || pm.fieldIx >= len(pm.fields) {
		return
	}
	f := pm.fields[pm.fieldIx]
	pm.values[f.key] = pm.fieldValue(f)
}

func (pm *protocolManager) commitField() {
	f := pm.fields[pm.fieldIx]
	val := pm.fieldValue(f)
	if err := validateProtocolField(f, val); err != nil {
		pm.fieldErr = err.Error()
		return
	}
	pm.values[f.key] = val
	pm.advanceField()
}

func (pm *protocolManager) fieldValue(f protocolField) string {
	val := strings.TrimSpace(pm.input.Value())
	if val == "" {
		return f.def
	}
	return val
}

func validateProtocolField(f protocolField, val string) error {
	switch {
	case f.key == "reality_sni":
		_, err := normalizeRealityServerName(val)
		return err
	case strings.HasSuffix(f.key, "_port"):
		if val == "" {
			return nil
		}
		port, err := strconv.Atoi(val)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	case strings.HasSuffix(f.key, "_uuid"):
		if val != "" && !validUUID(val) {
			return fmt.Errorf("uuid must be an RFC 4122 value")
		}
	}
	return nil
}

func (pm *protocolManager) backFromConfirm() {
	if len(pm.fields) > 0 {
		pm.phase = protocolPhaseForm
		pm.fieldIx = len(pm.fields) - 1
		f := pm.fields[pm.fieldIx]
		pm.input.SetValue(pm.values[f.key])
		pm.input.Placeholder = f.def
		pm.input.Focus()
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

func (pm *protocolManager) installFieldsForAdded(target []config.Protocol) []protocolField {
	added, _ := protocolDiff(pm.cfg.Enabled, target)
	addedSet := map[config.Protocol]bool{}
	for _, p := range added {
		addedSet[p] = true
	}
	var fields []protocolField
	if needsRealityProtocol(target) && pm.cfg.RealityServerName == "" {
		fields = append(fields, realitySNIField())
	}
	for _, proto := range config.AllProtocols {
		if addedSet[proto] {
			fields = append(fields, installFieldsForProtocol(proto)...)
		}
	}
	return fields
}

func (pm *protocolManager) editFields(proto config.Protocol) []protocolField {
	fields := editFieldsForProtocol(pm.cfg, proto)
	if (proto == config.ProtocolRealityVision || proto == config.ProtocolRealityGRPC) && pm.cfg.RealityServerName == "" {
		fields = append([]protocolField{realitySNIField()}, fields...)
	}
	return fields
}

func realitySNIField() protocolField {
	return protocolField{
		key:   "reality_sni",
		label: "Reality URL/SNI (camouflage server)",
		def:   "www.microsoft.com",
		note:  "You may enter a URL or host; the host is used for the Reality handshake.",
	}
}

func realitySNIEditField(current string) protocolField {
	f := realitySNIField()
	f.label = "Reality URL/SNI (camouflage server)"
	f.def = current
	if f.def == "" {
		f.def = "www.microsoft.com"
	}
	f.note = "Updates the shared Reality handshake SNI for Reality Vision and Reality gRPC."
	return f
}

func installFieldsForProtocol(proto config.Protocol) []protocolField {
	switch proto {
	case config.ProtocolRealityVision:
		return []protocolField{
			{key: "reality_vision_uuid", label: "Reality Vision UUID (optional)", note: "Blank generates a random UUID."},
			{key: "reality_vision_port", label: "Reality Vision port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolRealityGRPC:
		return []protocolField{
			{key: "reality_grpc_uuid", label: "Reality gRPC UUID (optional)", note: "Blank generates a random UUID."},
			{key: "reality_grpc_port", label: "Reality gRPC port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolHysteria2:
		return []protocolField{
			{key: "hysteria2_password", label: "Hysteria2 password (optional)", note: "Blank generates a random password."},
			{key: "hysteria2_port", label: "Hysteria2 port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolTUIC:
		return []protocolField{
			{key: "tuic_uuid", label: "TUIC UUID (optional)", note: "Blank generates a random UUID."},
			{key: "tuic_password", label: "TUIC password (optional)", note: "Blank generates a random password."},
			{key: "tuic_port", label: "TUIC port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolAnyTLS:
		return []protocolField{
			{key: "anytls_password", label: "AnyTLS password (optional)", note: "Blank generates a random password."},
			{key: "anytls_port", label: "AnyTLS port (optional)", note: "Blank chooses a random listen port."},
		}
	default:
		return nil
	}
}

func editFieldsForProtocol(cfg install.Config, proto config.Protocol) []protocolField {
	switch proto {
	case config.ProtocolRealityVision:
		return []protocolField{
			{key: "reality_vision_uuid", label: "Reality Vision UUID", def: cfg.Creds.RealityVisionUUID},
			{key: "reality_vision_port", label: "Reality Vision port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolRealityGRPC:
		return []protocolField{
			{key: "reality_grpc_uuid", label: "Reality gRPC UUID", def: cfg.Creds.RealityGRPCUUID},
			{key: "reality_grpc_port", label: "Reality gRPC port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolHysteria2:
		return []protocolField{
			{key: "hysteria2_password", label: "Hysteria2 password", def: cfg.Creds.HysteriaPassword},
			{key: "hysteria2_port", label: "Hysteria2 port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolTUIC:
		return []protocolField{
			{key: "tuic_uuid", label: "TUIC UUID", def: cfg.Creds.TUICUUID},
			{key: "tuic_password", label: "TUIC password", def: cfg.Creds.TUICPassword},
			{key: "tuic_port", label: "TUIC port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolAnyTLS:
		return []protocolField{
			{key: "anytls_password", label: "AnyTLS password", def: cfg.Creds.AnyTLSPassword},
			{key: "anytls_port", label: "AnyTLS port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	default:
		return nil
	}
}

func portDefault(port int) string {
	if port <= 0 {
		return ""
	}
	return strconv.Itoa(port)
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
	pm.events = nil
	pm.logBuf = nil
	pm.logScroll = 0
	pm.runErr = nil
	pm.runComplete = false
	pm.ch = make(chan runMsg, 64)
	ch := pm.ch
	opts := pm.updateOptions()
	logs := &logWriter{ch: ch}
	opts.Layout = protocolUILayout()
	opts.Runner = system.NewExecRunner(logs)
	opts.Firewall = pm.host.Firewall
	opts.Progress = func(e install.Event) {
		ev := e
		ch <- runMsg{event: &ev}
	}
	go func() {
		_, err := updateProtocolsRun(context.Background(), opts)
		ch <- runMsg{done: true, err: err}
	}()
	return pm.waitForRun()
}

func (pm *protocolManager) updateOptions() install.ProtocolUpdateOptions {
	selected := pm.cfg.Enabled
	if pm.action == protocolActionChange {
		selected = pm.targetProtocols()
	}
	opts := install.ProtocolUpdateOptions{Selected: selected}
	if v := strings.TrimSpace(pm.values["reality_sni"]); v != "" {
		if host, err := normalizeRealityServerName(v); err == nil {
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

func (pm *protocolManager) waitForRun() tea.Cmd {
	ch := pm.ch
	return func() tea.Msg { return <-ch }
}

func (pm *protocolManager) handleRun(msg runMsg) tea.Cmd {
	if msg.event != nil {
		pm.events = append(pm.events, *msg.event)
		e := *msg.event
		line := fmt.Sprintf("[%d/%d] %s - %s", e.Index, e.Total, e.Label, e.Status)
		if e.Err != nil {
			line += ": " + e.Err.Error()
		}
		pm.appendLog(line)
	}
	if msg.logLine != "" {
		pm.appendLog(dimStyle.Render(msg.logLine))
	}
	if msg.done {
		pm.runErr = msg.err
		if msg.err != nil {
			pm.phase = protocolPhaseDone
			return nil
		}
		pm.runComplete = true
		pm.logScroll = 0
		return nil
	}
	return pm.waitForRun()
}

func (pm *protocolManager) View() string {
	if pm.loadErr != nil {
		return wizardTitle.Render("Protocol Management") + "\n\n" + wizardErr.Render(pm.loadErr.Error()) + "\n\n" + dimStyle.Render("run install first · press enter/esc to return")
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
		return wizardOK.Render("Protocol management complete") + "\n\n" + pm.doneSummary() + "\n\n" + dimStyle.Render("press any key to return")
	default:
		return ""
	}
}

func (pm *protocolManager) actionView() string {
	var b strings.Builder
	b.WriteString(wizardTitle.Render("Protocol Management") + "\n\n")
	b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.cfg.Enabled) + "\n")
	if !pm.canApply() {
		b.WriteString(wizardErr.Render(pm.applyBlocker()) + "\n")
	}
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, action := range pm.actions() {
		row := "  " + action.label
		if i == pm.cursor {
			row = selStyle.Render("> " + action.label)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("enter select · ↑/↓ move · esc cancel"))
	return b.String()
}

func (pm *protocolManager) selectView() string {
	var b strings.Builder
	b.WriteString(wizardTitle.Render("Protocol Management · Install / Remove") + "\n\n")
	b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.cfg.Enabled) + "\n")
	b.WriteString(dimStyle.Render("Target:  ") + protocolLabels(pm.targetProtocols()) + "\n")
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n" + pm.protocolOptionsView() + "\n\n")
	b.WriteString(dimStyle.Render("space toggle · enter continue · shift+tab back · esc cancel"))
	return b.String()
}

func (pm *protocolManager) editPickView() string {
	var b strings.Builder
	b.WriteString(wizardTitle.Render("Protocol Management · Edit") + "\n\n")
	b.WriteString(dimStyle.Render("Choose an installed protocol to edit its uuid/password and port.") + "\n")
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	for i, proto := range pm.cfg.Enabled {
		label := string(proto) + "  " + dimStyle.Render("port "+portDefault(installedPort(proto, pm.cfg.Ports)))
		row := "  " + label
		if i == pm.cursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("enter edit · shift+tab back · esc cancel"))
	return b.String()
}

func (pm *protocolManager) formView() string {
	f := pm.fields[pm.fieldIx]
	var b strings.Builder
	title := "Protocol Management · Parameters"
	if pm.action == protocolActionEdit {
		title = "Protocol Management · Edit " + string(pm.editProto)
	}
	b.WriteString(wizardTitle.Render(title) + "\n\n")
	b.WriteString(f.label + "\n")
	if f.note != "" {
		for _, line := range wrapFieldNote(f.note, pm.width) {
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	if f.def != "" {
		b.WriteString(dimStyle.Render("default: "+f.def) + "\n")
	}
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString(pm.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("enter continue · shift+tab back · esc cancel"))
	return b.String()
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
	var rows []string
	switch pm.action {
	case protocolActionRealitySNI:
		rows = append(rows,
			"Edit: Reality SNI",
			"Current: "+or(pm.cfg.RealityServerName, "not set"),
			"Target:  "+or(pm.values["reality_sni"], "not set"),
		)
	case protocolActionEdit:
		rows = append(rows, "Edit: "+string(pm.editProto))
		for _, f := range pm.fields {
			rows = append(rows, f.label+": "+or(pm.values[f.key], "generate/keep current"))
		}
	default:
		added, removed := protocolDiff(pm.cfg.Enabled, pm.targetProtocols())
		rows = append(rows,
			"Current: "+protocolLabels(pm.cfg.Enabled),
			"Target:  "+protocolLabels(pm.targetProtocols()),
			"Add:     "+or(protocolStrings(added), "none"),
			"Remove:  "+or(protocolStrings(removed), "none"),
		)
		if len(pm.fields) > 0 {
			rows = append(rows, "", "New protocol parameters:")
			for _, f := range pm.fields {
				rows = append(rows, "  "+f.label+": "+or(pm.values[f.key], "generate/default"))
			}
		}
	}
	rows = append(rows,
		"",
		"This will regenerate sing-box config and all subscription files.",
	)
	return wizardTitle.Render("Protocol Management · Confirm") + "\n\n" + strings.Join(rows, "\n") + "\n\n" + dimStyle.Render("enter/y apply · shift+tab back · esc/n cancel")
}

func (pm *protocolManager) runningView() string {
	body := wizardTitle.Render("Protocol Management · Running") + "\n\n" + pm.bar.ViewAs(pm.percent())
	if logs := pm.logView(pm.logViewportHeight()); logs != "" {
		body += "\n\n" + logs
	}
	hint := "↑/↓ scroll log"
	if pm.runComplete {
		hint = "complete · press enter to show summary · " + hint
	}
	return body + "\n\n" + dimStyle.Render(hint)
}

func (pm *protocolManager) failedView() string {
	body := wizardErr.Render("Protocol management failed") + "\n\n" + pm.runErr.Error()
	if logs := pm.logView(pm.doneLogHeight()); logs != "" {
		body += "\n\n" + logs + "\n\n" + dimStyle.Render("↑/↓ scroll log · any other key to return")
		return body
	}
	return body + "\n\n" + dimStyle.Render("press any key to return")
}

func (pm *protocolManager) doneSummary() string {
	cfg := pm.result
	if len(cfg.Enabled) == 0 {
		cfg = pm.cfg
	}
	return strings.Join([]string{
		"Protocols:     " + protocolLabels(cfg.Enabled),
		"Ports:         " + installedPortsSummary(cfg.Enabled, cfg.Ports),
		"Subscriptions: refreshed",
	}, "\n")
}

func (pm *protocolManager) footerHints() []string {
	if pm.loadErr != nil {
		return []string{"enter/esc return"}
	}
	switch pm.phase {
	case protocolPhaseAction:
		return []string{"enter select", "esc cancel"}
	case protocolPhaseSelect:
		return []string{"space toggle", "enter continue", "shift+tab back"}
	case protocolPhaseEditPick:
		return []string{"enter edit", "shift+tab back", "esc cancel"}
	case protocolPhaseForm:
		return []string{"enter continue", "shift+tab back", "esc cancel"}
	case protocolPhaseConfirm:
		return []string{"enter apply", "shift+tab back", "esc cancel"}
	case protocolPhaseRunning:
		if pm.runComplete {
			return []string{"enter summary", "↑/↓ scroll log"}
		}
		return []string{"↑/↓ scroll log"}
	case protocolPhaseDone:
		if pm.runErr != nil {
			return []string{"↑/↓ scroll log", "any other key return"}
		}
		return []string{"any key return"}
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
	return strings.Join(parts, ",")
}

func needsRealityProtocol(protocols []config.Protocol) bool {
	for _, p := range protocols {
		if p == config.ProtocolRealityVision || p == config.ProtocolRealityGRPC {
			return true
		}
	}
	return false
}

func (pm *protocolManager) percent() float64 {
	if len(pm.events) == 0 {
		return 0
	}
	last := pm.events[len(pm.events)-1]
	if last.Total == 0 {
		return 0
	}
	return float64(last.Index) / float64(last.Total)
}

func (pm *protocolManager) appendLog(line string) {
	if pm.logScroll > 0 {
		pm.logScroll += pm.logLineHeight(line)
	}
	pm.logBuf = append(pm.logBuf, line)
	pm.clampLogScroll(pm.logViewportHeight())
}

func (pm *protocolManager) logView(height int) string {
	lines := pm.visibleLogLines(height)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (pm *protocolManager) visibleLogLines(height int) []string {
	rows := pm.logRows()
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	visible := min(height, len(rows))
	pm.clampLogScroll(height)
	start := len(rows) - visible - pm.logScroll
	return rows[start : start+visible]
}

func (pm *protocolManager) scrollLog(delta, height int) {
	pm.logScroll += delta
	pm.clampLogScroll(height)
}

func (pm *protocolManager) clampLogScroll(height int) {
	pm.logScroll = min(max(0, pm.logScroll), pm.maxLogScroll(height))
}

func (pm *protocolManager) maxLogScroll(height int) int {
	if height <= 0 {
		return 0
	}
	return max(0, len(pm.logRows())-height)
}

func (pm *protocolManager) logRows() []string {
	var rows []string
	for _, line := range pm.logBuf {
		rows = append(rows, pm.wrapLogLine(line)...)
	}
	return rows
}

func (pm *protocolManager) wrapLogLine(line string) []string {
	wrapped := lipgloss.NewStyle().Width(pm.logWrapWidth()).Render(line)
	return strings.Split(wrapped, "\n")
}

func (pm *protocolManager) logLineHeight(line string) int {
	return max(1, lipgloss.Height(lipgloss.NewStyle().Width(pm.logWrapWidth()).Render(line)))
}

func (pm *protocolManager) logWrapWidth() int {
	if pm.width <= 0 {
		return 80
	}
	return max(1, pm.width)
}

func (pm *protocolManager) logViewportHeight() int {
	if pm.height <= 0 {
		return 12
	}
	return max(1, pm.height-6)
}

func (pm *protocolManager) doneLogHeight() int {
	if pm.height <= 0 {
		return 12
	}
	return max(1, pm.height-7)
}
