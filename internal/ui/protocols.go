package ui

import (
	"context"
	"fmt"
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
	protocolPhaseSelect protocolPhase = iota
	protocolPhaseReality
	protocolPhaseConfirm
	protocolPhaseRunning
	protocolPhaseDone
)

var (
	protocolUILayout   = paths.DefaultLayout
	detectProtocolHost = system.DetectHost
	updateProtocolsRun = install.UpdateProtocols
)

type protocolManager struct {
	phase  protocolPhase
	dryRun bool

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     install.Config
	loadErr error

	cursor   int
	selected map[string]bool
	fieldErr string

	input           textinput.Model
	realityOverride string

	bar       progress.Model
	events    []install.Event
	logBuf    []string
	logScroll int
	runErr    error
	result    install.Config
	ch        chan runMsg
}

func newProtocolManager(dryRun bool) *protocolManager {
	input := textinput.New()
	input.CharLimit = 256
	input.Prompt = "› "
	input.Placeholder = "www.microsoft.com"

	pm := &protocolManager{
		phase:    protocolPhaseSelect,
		dryRun:   dryRun,
		selected: map[string]bool{},
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
	if cfg.RealityServerName != "" {
		pm.input.SetValue(cfg.RealityServerName)
	}
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
	if pm.phase == protocolPhaseReality {
		var cmd tea.Cmd
		pm.input, cmd = pm.input.Update(msg)
		return cmd, false
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
	case protocolPhaseSelect:
		switch msg.String() {
		case "up", "k", "left", "h":
			pm.move(-1)
		case "down", "j", "right", "l":
			pm.move(1)
		case " ", "space":
			pm.toggle()
		case "enter":
			return pm.enterConfirm()
		case "esc", "q":
			return nil, true
		}
	case protocolPhaseReality:
		switch msg.String() {
		case "enter":
			return pm.commitRealityServerName()
		case "shift+tab", "ctrl+b":
			pm.phase = protocolPhaseSelect
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
			pm.phase = protocolPhaseSelect
		case "esc", "n":
			return nil, true
		}
	case protocolPhaseRunning:
		switch msg.String() {
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

func (pm *protocolManager) move(delta int) {
	options := protocolOptions()
	pm.cursor = (pm.cursor + delta + len(options)) % len(options)
	pm.fieldErr = ""
}

func (pm *protocolManager) toggle() {
	options := protocolOptions()
	opt := options[min(max(0, pm.cursor), len(options)-1)]
	if pm.selected[opt] {
		delete(pm.selected, opt)
	} else {
		pm.selected[opt] = true
	}
	pm.fieldErr = ""
}

func (pm *protocolManager) enterConfirm() (tea.Cmd, bool) {
	target := pm.targetProtocols()
	if len(target) == 0 {
		pm.fieldErr = "select at least one protocol"
		return nil, false
	}
	if !pm.canApply() {
		pm.fieldErr = pm.applyBlocker()
		return nil, false
	}
	if sameProtocolSet(pm.cfg.Enabled, target) {
		pm.fieldErr = "selection is unchanged"
		return nil, false
	}
	if needsRealityProtocol(target) && pm.cfg.RealityServerName == "" && pm.realityOverride == "" {
		pm.phase = protocolPhaseReality
		pm.input.Focus()
		return nil, false
	}
	pm.phase = protocolPhaseConfirm
	return nil, false
}

func (pm *protocolManager) commitRealityServerName() (tea.Cmd, bool) {
	value := pm.input.Value()
	if strings.TrimSpace(value) == "" {
		value = pm.input.Placeholder
	}
	host, err := normalizeRealityServerName(value)
	if err != nil {
		pm.fieldErr = err.Error()
		return nil, false
	}
	pm.realityOverride = host
	pm.fieldErr = ""
	pm.phase = protocolPhaseConfirm
	return nil, false
}

func (pm *protocolManager) canApply() bool {
	if pm.dryRun {
		return true
	}
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
	pm.ch = make(chan runMsg, 64)
	ch := pm.ch
	if pm.dryRun {
		go simulateProtocolDryRun(ch, pm.cfg.Enabled, pm.targetProtocols())
		return pm.waitForRun()
	}
	logs := &logWriter{ch: ch}
	runner := system.NewExecRunner(logs)
	opts := install.ProtocolUpdateOptions{
		Layout:            protocolUILayout(),
		Runner:            runner,
		Firewall:          pm.host.Firewall,
		Selected:          pm.targetProtocols(),
		RealityServerName: pm.realityOverride,
		Progress: func(e install.Event) {
			ev := e
			ch <- runMsg{event: &ev}
		},
	}
	go func() {
		_, err := updateProtocolsRun(context.Background(), opts)
		ch <- runMsg{done: true, err: err}
	}()
	return pm.waitForRun()
}

func simulateProtocolDryRun(ch chan runMsg, current, target []config.Protocol) {
	steps := []install.Event{
		{Index: 1, Total: 3, Label: "Plan", Detail: "compute protocol changes", Status: "running"},
		{Index: 1, Total: 3, Label: "Plan", Detail: "compute protocol changes", Status: "ok"},
		{Index: 2, Total: 3, Label: "Config", Detail: "would render and validate config.json", Status: "running"},
		{Index: 2, Total: 3, Label: "Config", Detail: "would render and validate config.json", Status: "ok"},
		{Index: 3, Total: 3, Label: "Restart", Detail: "would restart sing-box.service", Status: "running"},
		{Index: 3, Total: 3, Label: "Restart", Detail: "would restart sing-box.service", Status: "ok"},
	}
	ch <- runMsg{logLine: "[dry-run] current protocols: " + protocolLabels(current)}
	ch <- runMsg{logLine: "[dry-run] target protocols: " + protocolLabels(target)}
	for _, e := range steps {
		ev := e
		ch <- runMsg{event: &ev}
	}
	ch <- runMsg{logLine: "[dry-run] no files, firewall rules, or services were changed"}
	ch <- runMsg{done: true}
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
		if msg.err == nil && !pm.dryRun {
			if cfg, err := install.LoadProtocolConfig(protocolUILayout()); err == nil {
				pm.cfg = cfg
				pm.result = cfg
				pm.selected = selectedOptions(protocolsValue(cfg.Enabled))
			}
		} else if msg.err == nil {
			pm.result = pm.cfg
			pm.result.Enabled = pm.targetProtocols()
		}
		pm.phase = protocolPhaseDone
		return nil
	}
	return pm.waitForRun()
}

func (pm *protocolManager) View() string {
	if pm.loadErr != nil {
		return wizardTitle.Render("Protocol Management") + "\n\n" + wizardErr.Render(pm.loadErr.Error()) + "\n\n" + dimStyle.Render("run install first · press enter/esc to return")
	}
	switch pm.phase {
	case protocolPhaseSelect:
		return pm.selectView()
	case protocolPhaseReality:
		return pm.realityView()
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

func (pm *protocolManager) selectView() string {
	var b strings.Builder
	b.WriteString(wizardTitle.Render("Protocol Management") + "\n\n")
	b.WriteString(dimStyle.Render("Current: ") + protocolLabels(pm.cfg.Enabled) + "\n")
	b.WriteString(dimStyle.Render("Target:  ") + protocolLabels(pm.targetProtocols()) + "\n")
	if !pm.canApply() {
		b.WriteString(wizardErr.Render(pm.applyBlocker()) + "\n")
	}
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(pm.optionsView() + "\n\n")
	b.WriteString(dimStyle.Render("space toggle · enter confirm · ↑/↓ move · esc cancel"))
	return b.String()
}

func (pm *protocolManager) optionsView() string {
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

func (pm *protocolManager) realityView() string {
	var b strings.Builder
	b.WriteString(wizardTitle.Render("Protocol Management · Reality") + "\n\n")
	b.WriteString("Reality URL/SNI (camouflage server)\n")
	b.WriteString(dimStyle.Render("Required before enabling Reality Vision or Reality gRPC. You may enter a URL or host; the host is stored in state.") + "\n")
	if pm.fieldErr != "" {
		b.WriteString(wizardErr.Render(pm.fieldErr) + "\n")
	}
	b.WriteString(pm.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("enter continue · shift+tab back · esc cancel"))
	return b.String()
}

func (pm *protocolManager) confirmView() string {
	added, removed := protocolDiff(pm.cfg.Enabled, pm.targetProtocols())
	rows := []string{
		"Current: " + protocolLabels(pm.cfg.Enabled),
		"Target:  " + protocolLabels(pm.targetProtocols()),
		"Add:     " + or(strings.Join(added, ","), "none"),
		"Remove:  " + or(strings.Join(removed, ","), "none"),
	}
	if pm.realityOverride != "" {
		rows = append(rows, "Reality SNI: "+pm.realityOverride)
	}
	rows = append(rows,
		"",
		"This will regenerate sing-box config and all subscription files.",
		"Newly enabled protocols receive generated credentials and random free ports when needed.",
		"Firewall rules are opened for newly enabled protocols when ufw/firewalld is detected; removed ports are not closed automatically.",
	)
	return wizardTitle.Render("Protocol Management · Confirm") + "\n\n" + strings.Join(rows, "\n") + "\n\n" + dimStyle.Render("enter/y apply · shift+tab back · esc/n cancel")
}

func (pm *protocolManager) runningView() string {
	body := wizardTitle.Render("Protocol Management · Running") + "\n\n" + pm.bar.ViewAs(pm.percent())
	if logs := pm.logView(pm.logViewportHeight()); logs != "" {
		body += "\n\n" + logs
	}
	return body + "\n\n" + dimStyle.Render("↑/↓ scroll log")
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
	case protocolPhaseSelect:
		return []string{"space toggle", "enter confirm", "esc cancel"}
	case protocolPhaseReality:
		return []string{"enter continue", "shift+tab back", "esc cancel"}
	case protocolPhaseConfirm:
		return []string{"enter apply", "shift+tab back", "esc cancel"}
	case protocolPhaseRunning:
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

func (pm *protocolManager) targetProtocols() []config.Protocol {
	return protocolsFromValue(selectedOptionsValue(protocolOptions(), pm.selected))
}

func sameProtocolSet(a, b []config.Protocol) bool {
	as, bs := selectedProtocolSet(a), selectedProtocolSet(b)
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

func selectedProtocolSet(protocols []config.Protocol) map[config.Protocol]bool {
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

func protocolDiff(current, target []config.Protocol) (added, removed []string) {
	cur, tgt := selectedProtocolSet(current), selectedProtocolSet(target)
	for _, p := range config.AllProtocols {
		if tgt[p] && !cur[p] {
			added = append(added, string(p))
		}
		if cur[p] && !tgt[p] {
			removed = append(removed, string(p))
		}
	}
	return added, removed
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
