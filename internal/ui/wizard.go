package ui

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/install"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// wizardPhase is the install wizard's current screen.
type wizardPhase int

const (
	phasePreflight wizardPhase = iota
	phaseForm
	phaseConfirm
	phaseRunning
	phaseDone
)

// field is one collected input.
type field struct {
	key       string
	label     string
	def       string
	note      string
	options   []string
	multi     bool
	paramsFor []config.Protocol
	// skip reports whether this field is hidden given the values so far.
	skip func(vals map[string]string) bool
}

// installFields defines the wizard's input sequence.
func installFields() []field {
	isDNS := func(v map[string]string) bool { return v["challenge"] != "dns-01" }
	missingProtocol := func(p config.Protocol) func(map[string]string) bool {
		return func(v map[string]string) bool { return !protocolSelected(v, p) }
	}
	noReality := func(v map[string]string) bool {
		return !protocolSelected(v, config.ProtocolRealityVision) && !protocolSelected(v, config.ProtocolRealityGRPC)
	}
	monitorDisabled := func(v map[string]string) bool { return !trafficMonitorEnabled(v) }
	return []field{
		{key: "domain", label: "Domain (must resolve to this server)", note: "Used for certificate issuance, Nginx server_name, subscription URLs, and TLS SNI."},
		{key: "email", label: "ACME account email (optional)", note: "Optional Let's Encrypt account contact used for certificate notices."},
		{key: "challenge", label: "ACME challenge", def: "http-01", options: []string{"http-01", "dns-01"}, note: "http-01 validates through port 80; dns-01 validates through the DNS API provider."},
		{key: "dns_provider", label: "DNS provider", def: "cloudflare", options: []string{"cloudflare", "aliyun"}, note: "Only used for dns-01. Supported providers are Cloudflare and Aliyun.", skip: isDNS},
		{key: "dns_credential", label: "DNS API credential", skip: isDNS},
		{key: "protocols", label: "Protocols to install", def: defaultProtocolValue(), options: protocolOptions(), multi: true, note: "Select one or more protocols. At least one protocol must remain selected."},
		{key: "reality_sni", label: "Reality URL/SNI (camouflage server)", def: "www.microsoft.com", note: "You may enter a URL or host; the host is used for the Reality handshake.", paramsFor: []config.Protocol{config.ProtocolRealityVision, config.ProtocolRealityGRPC}, skip: noReality},
		{key: "reality_vision_uuid", label: "Reality Vision UUID (optional)", note: "Blank generates a random UUID.", paramsFor: []config.Protocol{config.ProtocolRealityVision}, skip: missingProtocol(config.ProtocolRealityVision)},
		{key: "reality_vision_port", label: "Reality Vision port (optional)", note: "Blank chooses a random listen port.", paramsFor: []config.Protocol{config.ProtocolRealityVision}, skip: missingProtocol(config.ProtocolRealityVision)},
		{key: "reality_grpc_uuid", label: "Reality gRPC UUID (optional)", note: "Blank generates a random UUID.", paramsFor: []config.Protocol{config.ProtocolRealityGRPC}, skip: missingProtocol(config.ProtocolRealityGRPC)},
		{key: "reality_grpc_port", label: "Reality gRPC port (optional)", note: "Blank chooses a random listen port.", paramsFor: []config.Protocol{config.ProtocolRealityGRPC}, skip: missingProtocol(config.ProtocolRealityGRPC)},
		{key: "hysteria2_password", label: "Hysteria2 password (optional)", note: "Blank generates a random password.", paramsFor: []config.Protocol{config.ProtocolHysteria2}, skip: missingProtocol(config.ProtocolHysteria2)},
		{key: "hysteria2_port", label: "Hysteria2 port (optional)", note: "Blank chooses a random listen port.", paramsFor: []config.Protocol{config.ProtocolHysteria2}, skip: missingProtocol(config.ProtocolHysteria2)},
		{key: "tuic_uuid", label: "TUIC UUID (optional)", note: "Blank generates a random UUID.", paramsFor: []config.Protocol{config.ProtocolTUIC}, skip: missingProtocol(config.ProtocolTUIC)},
		{key: "tuic_password", label: "TUIC password (optional)", note: "Blank generates a random password.", paramsFor: []config.Protocol{config.ProtocolTUIC}, skip: missingProtocol(config.ProtocolTUIC)},
		{key: "tuic_port", label: "TUIC port (optional)", note: "Blank chooses a random listen port.", paramsFor: []config.Protocol{config.ProtocolTUIC}, skip: missingProtocol(config.ProtocolTUIC)},
		{key: "anytls_password", label: "AnyTLS password (optional)", note: "Blank generates a random password.", paramsFor: []config.Protocol{config.ProtocolAnyTLS}, skip: missingProtocol(config.ProtocolAnyTLS)},
		{key: "anytls_port", label: "AnyTLS port (optional)", note: "Blank chooses a random listen port.", paramsFor: []config.Protocol{config.ProtocolAnyTLS}, skip: missingProtocol(config.ProtocolAnyTLS)},
		{key: "display_name", label: "Node display name", def: "Node", note: "Used only in generated node names shown by clients."},
		{key: "subscribe_port", label: "Subscription/Nginx HTTPS port", def: "2096", note: "Nginx listens on this public HTTPS port for /s subscriptions and the masquerade site."},
		{key: "subscribe_salt", label: "Subscription salt (optional)", note: "Blank generates a random salt. The URL token is md5(salt + newline)."},
		{key: "traffic_monitor", label: "Deploy traffic monitor", def: "yes", options: []string{"yes", "no"}, note: "Choose no to skip the traffic monitor service and /traffic/ UI."},
		{key: "traffic_port", label: "Traffic monitor public HTTPS port", def: "2097", note: "Nginx listens on this public HTTPS port for /traffic.", skip: monitorDisabled},
		{key: "monitor_port", label: "Traffic monitor local port", def: "19090", note: "The monitor listens on 127.0.0.1 and Nginx proxies /traffic to this port.", skip: monitorDisabled},
		{key: "traffic_in_limit_gb", label: "Monthly inbound traffic limit in GB (0 = unlimited)", def: "0", note: "Inbound uses the monitored interface RX counter. When any configured limit is exceeded, sing-box.service is stopped automatically.", skip: monitorDisabled},
		{key: "traffic_out_limit_gb", label: "Monthly outbound traffic limit in GB (0 = unlimited)", def: "0", note: "Outbound uses the monitored interface TX counter.", skip: monitorDisabled},
		{key: "traffic_total_limit_gb", label: "Monthly total traffic limit in GB (0 = unlimited)", def: "0", note: "Total traffic is inbound + outbound.", skip: monitorDisabled},
		{key: "reset_day", label: "Monthly reset day (1-28)", def: "1", note: "Day of month when the traffic quota cycle resets and service can be restored.", skip: monitorDisabled},
	}
}

// runMsg carries an orchestrator progress event, a streamed log line, or
// completion into the UI. It is the only channel the orchestrator goroutine
// uses to communicate, so all wizard state stays mutated on the UI goroutine.
type runMsg struct {
	event   *install.Event
	logLine string
	done    bool
	err     error
}

// wizard is the interactive install flow.
type wizard struct {
	phase  wizardPhase
	host   system.Host
	hosts  string // preflight summary / error text
	width  int
	height int

	fields         []field
	fieldIx        int
	values         map[string]string
	input          textinput.Model
	optionIx       int
	optionSelected map[string]bool
	fieldErr       string

	validateDomain func(context.Context, string) error

	bar           progress.Model
	events        []install.Event
	logBuf        []string
	logScroll     int
	confirmScroll int
	runErr        error
	cfg           install.Config
	runComplete   bool

	ch chan runMsg
}

// newWizard builds the wizard, running host preflight immediately.
func newWizard() *wizard {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Prompt = "› "

	w := &wizard{
		phase:          phasePreflight,
		fields:         installFields(),
		values:         map[string]string{},
		input:          ti,
		bar:            progress.New(progress.WithDefaultGradient()),
		validateDomain: validateDomainResolvesToCurrentIP,
	}
	host, err := system.DetectHost()
	w.host = host
	switch {
	case err != nil:
		w.hosts = "Failed to detect host: " + err.Error()
	case !host.IsRoot:
		w.hosts = "This installer must be run as root."
	case !host.Supported():
		w.hosts = fmt.Sprintf("Unsupported system: family=%q arch=%q", host.OS.Family, host.Arch)
	case host.SELinux:
		w.hosts = "SELinux is enforcing; installation is blocked. Set it permissive and retry."
	default:
		w.hosts = fmt.Sprintf("Detected %s/%s, firewall=%s — ready.", host.OS.ID, host.Arch, firewallName(host.Firewall))
	}
	return w
}

func firewallName(f system.Firewall) string {
	if f == system.FirewallNone {
		return "none"
	}
	return string(f)
}

// canProceed reports whether preflight passed.
func (w *wizard) canProceed() bool {
	return w.host.IsRoot && w.host.Supported() && !w.host.SELinux
}

func (w *wizard) setSize(width, height int) {
	w.width = width
	w.height = height
	w.bar.Width = min(width-4, 60)
}

func (w *wizard) setField(index int) {
	f := w.fields[index]
	w.fieldIx = index
	w.fieldErr = ""
	if len(f.options) > 0 {
		value := w.values[f.key]
		if value == "" {
			value = f.def
		}
		if f.multi {
			w.optionSelected = selectedOptions(value)
			w.optionIx = 0
			w.input.Blur()
			return
		}
		w.optionSelected = nil
		w.optionIx = optionIndex(f.options, value)
		w.input.Blur()
		return
	}
	w.optionSelected = nil
	w.input.SetValue(w.values[f.key])
	w.input.Placeholder = f.def
	w.input.Focus()
}

func optionIndex(options []string, value string) int {
	for i, opt := range options {
		if opt == value {
			return i
		}
	}
	return 0
}

func protocolOptions() []string {
	options := make([]string, 0, len(config.AllProtocols))
	for _, p := range config.AllProtocols {
		options = append(options, string(p))
	}
	return options
}

func defaultProtocolValue() string {
	return protocolSelectionValue(config.AllProtocols)
}

// protocolSelectionValue is the machine-readable value used by form state.
// Display text must use protocolLabels instead.
func protocolSelectionValue(protocols []config.Protocol) string {
	parts := make([]string, 0, len(protocols))
	for _, p := range protocols {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, ",")
}

func selectedOptions(value string) map[string]bool {
	selected := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			selected[part] = true
		}
	}
	return selected
}

func selectedOptionsValue(options []string, selected map[string]bool) string {
	values := make([]string, 0, len(options))
	for _, opt := range options {
		if selected[opt] {
			values = append(values, opt)
		}
	}
	return strings.Join(values, ",")
}

func protocolsFromValue(value string) []config.Protocol {
	selected := selectedOptions(value)
	protocols := make([]config.Protocol, 0, len(config.AllProtocols))
	for _, p := range config.AllProtocols {
		if selected[string(p)] {
			protocols = append(protocols, p)
		}
	}
	return protocols
}

func protocolSelected(vals map[string]string, p config.Protocol) bool {
	value := vals["protocols"]
	if value == "" {
		value = defaultProtocolValue()
	}
	return selectedOptions(value)[string(p)]
}

func trafficMonitorEnabled(vals map[string]string) bool {
	value := vals["traffic_monitor"]
	if value == "" {
		value = "yes"
	}
	return value == "yes"
}

func hasProtocol(protocols []config.Protocol, want config.Protocol) bool {
	for _, p := range protocols {
		if p == want {
			return true
		}
	}
	return false
}

func protocolLabels(protocols []config.Protocol) string {
	if len(protocols) == 0 {
		return "none"
	}
	return protocolStrings(protocols)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func normalizeRealityServerName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("reality URL/SNI is required")
	}
	if !strings.Contains(raw, "://") && strings.Contains(raw, "/") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("reality URL/SNI host is required")
		}
		return host, nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	if raw == "" || strings.ContainsAny(raw, "/?#") {
		return "", fmt.Errorf("reality URL/SNI must be a URL or host")
	}
	return raw, nil
}

// startForm activates the first visible field.
func (w *wizard) startForm() {
	w.phase = phaseForm
	w.fieldIx = -1
	w.advanceField()
}

// advanceField moves to the next visible field, or to confirm at the end.
func (w *wizard) advanceField() {
	for i := w.fieldIx + 1; i < len(w.fields); i++ {
		f := w.fields[i]
		if f.skip != nil && f.skip(w.values) {
			continue
		}
		w.setField(i)
		return
	}
	w.confirmScroll = 0
	w.phase = phaseConfirm
}

func (w *wizard) previousField() {
	if w.fieldIx < 0 {
		return
	}
	w.saveFieldDraft()
	for i := w.fieldIx - 1; i >= 0; i-- {
		f := w.fields[i]
		if f.skip != nil && f.skip(w.values) {
			continue
		}
		w.setField(i)
		return
	}
}

func (w *wizard) backToLastField() {
	w.phase = phaseForm
	for i := len(w.fields) - 1; i >= 0; i-- {
		f := w.fields[i]
		if f.skip != nil && f.skip(w.values) {
			continue
		}
		w.setField(i)
		return
	}
}

func (w *wizard) saveFieldDraft() {
	if w.fieldIx < 0 || w.fieldIx >= len(w.fields) {
		return
	}
	f := w.fields[w.fieldIx]
	w.values[f.key] = w.fieldValue(f)
}

// commitField stores the current field value (or its default) and advances.
func (w *wizard) commitField() {
	f := w.fields[w.fieldIx]
	val := w.fieldValue(f)
	if err := w.validateField(f, val); err != nil {
		w.fieldErr = err.Error()
		return
	}
	w.fieldErr = ""
	w.values[f.key] = val
	w.advanceField()
}

func (w *wizard) fieldValue(f field) string {
	if len(f.options) > 0 {
		if f.multi {
			return selectedOptionsValue(f.options, w.optionSelected)
		}
		return f.options[min(max(0, w.optionIx), len(f.options)-1)]
	}
	val := strings.TrimSpace(w.input.Value())
	if val == "" {
		return f.def
	}
	return val
}

func (w *wizard) validateField(f field, val string) error {
	switch {
	case f.key == "domain":
		if val == "" {
			return fmt.Errorf("domain is required")
		}
		if w.validateDomain == nil {
			return nil
		}
		return w.validateDomain(context.Background(), val)
	case f.key == "protocols":
		if len(protocolsFromValue(val)) == 0 {
			return fmt.Errorf("select at least one protocol")
		}
	case f.key == "reality_sni":
		if _, err := normalizeRealityServerName(val); err != nil {
			return err
		}
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
	case strings.HasPrefix(f.key, "traffic_") && strings.HasSuffix(f.key, "_limit_gb"):
		if _, err := strconv.ParseUint(val, 10, 64); err != nil {
			return fmt.Errorf("traffic limit must be a non-negative integer")
		}
	case f.key == "reset_day":
		day, err := strconv.Atoi(val)
		if err != nil || day < 1 || day > 28 {
			return fmt.Errorf("reset day must be between 1 and 28")
		}
	}
	return nil
}

// Update handles wizard messages. The bool return reports whether the wizard is
// finished and the caller should return to the menu.
func (w *wizard) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.setSize(msg.Width, msg.Height)
	case runMsg:
		return w.handleRun(msg), false
	case tea.KeyMsg:
		return w.handleKey(msg)
	case tea.MouseMsg:
		return w.handleMouse(msg), false
	}
	if w.phase == phaseForm && !w.currentFieldHasOptions() {
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(msg)
		return cmd, false
	}
	return nil, false
}

func (w *wizard) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch w.phase {
	case phasePreflight:
		switch msg.String() {
		case "enter":
			if w.canProceed() {
				w.startForm()
			}
		case "esc", "q":
			return nil, true
		}
	case phaseForm:
		switch msg.String() {
		case "enter":
			w.commitField()
		case " ", "space":
			if w.currentFieldIsMulti() {
				w.toggleOption()
				break
			}
			return w.updateInput(msg), false
		case "up", "k", "left", "h":
			if w.currentFieldHasOptions() {
				w.moveOption(-1)
				break
			}
			return w.updateInput(msg), false
		case "down", "j", "right", "l":
			if w.currentFieldHasOptions() {
				w.moveOption(1)
				break
			}
			return w.updateInput(msg), false
		case "shift+tab", "ctrl+b":
			w.previousField()
		case "esc":
			return nil, true
		default:
			if w.currentFieldHasOptions() {
				return nil, false
			}
			return w.updateInput(msg), false
		}
	case phaseConfirm:
		switch msg.String() {
		case "up", "k":
			w.scrollConfirm(-1, w.confirmViewportHeight())
			return nil, false
		case "down", "j":
			w.scrollConfirm(1, w.confirmViewportHeight())
			return nil, false
		case "pgup":
			w.scrollConfirm(-w.confirmViewportHeight(), w.confirmViewportHeight())
			return nil, false
		case "pgdown":
			w.scrollConfirm(w.confirmViewportHeight(), w.confirmViewportHeight())
			return nil, false
		case "home":
			w.confirmScroll = 0
			return nil, false
		case "end":
			w.confirmScroll = w.maxConfirmScroll(w.confirmViewportHeight())
			return nil, false
		case "enter", "y":
			return w.startRun(), false
		case "shift+tab", "ctrl+b":
			w.backToLastField()
		case "esc", "n":
			return nil, true
		}
	case phaseRunning:
		switch msg.String() {
		case "enter":
			if w.runComplete {
				w.phase = phaseDone
			}
		case "up", "k":
			w.scrollLog(1, w.logViewportHeight())
			return nil, false
		case "down", "j":
			w.scrollLog(-1, w.logViewportHeight())
			return nil, false
		case "pgup":
			w.scrollLog(w.logViewportHeight(), w.logViewportHeight())
			return nil, false
		case "pgdown":
			w.scrollLog(-w.logViewportHeight(), w.logViewportHeight())
			return nil, false
		case "home":
			w.logScroll = w.maxLogScroll(w.logViewportHeight())
			return nil, false
		case "end":
			w.logScroll = 0
			return nil, false
		}
	case phaseDone:
		if w.runErr != nil {
			switch msg.String() {
			case "up", "k":
				w.scrollLog(1, w.doneLogHeight())
				return nil, false
			case "down", "j":
				w.scrollLog(-1, w.doneLogHeight())
				return nil, false
			case "pgup":
				w.scrollLog(w.doneLogHeight(), w.doneLogHeight())
				return nil, false
			case "pgdown":
				w.scrollLog(-w.doneLogHeight(), w.doneLogHeight())
				return nil, false
			case "home":
				w.logScroll = w.maxLogScroll(w.doneLogHeight())
				return nil, false
			case "end":
				w.logScroll = 0
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (w *wizard) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		switch w.phase {
		case phaseConfirm:
			w.scrollConfirm(-3, w.confirmViewportHeight())
		case phaseRunning:
			w.scrollLog(3, w.logViewportHeight())
		case phaseDone:
			if w.runErr != nil {
				w.scrollLog(3, w.doneLogHeight())
			}
		}
	case tea.MouseButtonWheelDown:
		switch w.phase {
		case phaseConfirm:
			w.scrollConfirm(3, w.confirmViewportHeight())
		case phaseRunning:
			w.scrollLog(-3, w.logViewportHeight())
		case phaseDone:
			if w.runErr != nil {
				w.scrollLog(-3, w.doneLogHeight())
			}
		}
	}
	return nil
}

func (w *wizard) updateInput(msg tea.Msg) tea.Cmd {
	w.fieldErr = ""
	var cmd tea.Cmd
	w.input, cmd = w.input.Update(msg)
	return cmd
}

func (w *wizard) currentFieldHasOptions() bool {
	if w.fieldIx < 0 || w.fieldIx >= len(w.fields) {
		return false
	}
	return len(w.fields[w.fieldIx].options) > 0
}

func (w *wizard) currentFieldIsMulti() bool {
	if w.fieldIx < 0 || w.fieldIx >= len(w.fields) {
		return false
	}
	return w.fields[w.fieldIx].multi
}

func (w *wizard) moveOption(delta int) {
	if !w.currentFieldHasOptions() {
		return
	}
	options := w.fields[w.fieldIx].options
	w.optionIx = (w.optionIx + delta + len(options)) % len(options)
	w.fieldErr = ""
}

func (w *wizard) toggleOption() {
	if !w.currentFieldIsMulti() {
		return
	}
	options := w.fields[w.fieldIx].options
	if len(options) == 0 {
		return
	}
	if w.optionSelected == nil {
		w.optionSelected = map[string]bool{}
	}
	opt := options[min(max(0, w.optionIx), len(options)-1)]
	if w.optionSelected[opt] {
		delete(w.optionSelected, opt)
	} else {
		w.optionSelected[opt] = true
	}
	w.fieldErr = ""
}

func (w *wizard) handleRun(msg runMsg) tea.Cmd {
	if msg.event != nil {
		w.events = append(w.events, *msg.event)
		e := *msg.event
		line := fmt.Sprintf("[%d/%d] %s — %s", e.Index, e.Total, e.Label, e.Status)
		if e.Err != nil {
			line += ": " + e.Err.Error()
		}
		w.appendLog(line)
	}
	if msg.logLine != "" {
		w.appendLog(dimStyle.Render(msg.logLine))
	}
	if msg.done {
		w.runErr = msg.err
		if msg.err != nil {
			w.phase = phaseDone
			return nil
		}
		w.runComplete = true
		w.logScroll = 0
		return nil
	}
	return w.waitForRun()
}

func (w *wizard) appendLog(line string) {
	if w.logScroll > 0 {
		w.logScroll += w.logLineHeight(line)
	}
	w.logBuf = append(w.logBuf, line)
	w.clampLogScroll(w.logViewportHeight())
}

// startRun builds the config and launches the orchestrator goroutine.
func (w *wizard) startRun() tea.Cmd {
	cfg, err := w.buildConfig()
	if err != nil {
		w.runErr = err
		w.phase = phaseDone
		return nil
	}
	w.cfg = cfg
	w.phase = phaseRunning
	w.runComplete = false
	w.ch = make(chan runMsg, 64)

	ch := w.ch
	logs := &logWriter{ch: ch}
	runner := system.Runner(system.NewExecRunner(logs))
	layout := paths.DefaultLayout()
	issuer := acme.NewLegoIssuer()
	issuer.Output = logs
	acmeManager := acme.NewManager(issuer)
	releases := release.NewClient("", nil)

	orch := &install.Orchestrator{
		Runner:   runner,
		Layout:   layout,
		ACME:     acmeManager,
		Releases: releases,
		GOOS:     "linux",
		GOARCH:   w.host.Arch,
	}
	orch.Progress = func(e install.Event) {
		ev := e
		ch <- runMsg{event: &ev}
	}
	go func() {
		err := orch.Run(context.Background(), cfg)
		ch <- runMsg{done: true, err: err}
	}()
	return w.waitForRun()
}

// waitForRun returns a command that reads the next orchestrator message.
func (w *wizard) waitForRun() tea.Cmd {
	ch := w.ch
	return func() tea.Msg { return <-ch }
}

// buildConfig assembles install.Config from the collected values and host.
func (w *wizard) buildConfig() (install.Config, error) {
	creds, err := install.GenerateCredentials()
	if err != nil {
		return install.Config{}, err
	}
	w.applyCredentialOverrides(&creds)
	enabled := protocolsFromValue(w.values["protocols"])
	if len(enabled) == 0 {
		enabled = config.AllProtocols
	}
	deployMonitor := trafficMonitorEnabled(w.values)
	subscribePort, err := parseInstallPort(w.values["subscribe_port"], 2096, "subscription port")
	if err != nil {
		return install.Config{}, err
	}
	trafficPort, err := parseInstallPort(w.values["traffic_port"], 2097, "traffic monitor public port")
	if err != nil {
		return install.Config{}, err
	}
	monitorPort, err := parseInstallPort(w.values["monitor_port"], 19090, "traffic monitor port")
	if err != nil {
		return install.Config{}, err
	}
	ports, err := w.protocolPorts(enabled, subscribePort, trafficPort, monitorPort, deployMonitor)
	if err != nil {
		return install.Config{}, err
	}
	salt := strings.TrimSpace(w.values["subscribe_salt"])
	if salt == "" {
		salt, err = credentials.Salt()
		if err != nil {
			return install.Config{}, err
		}
	}
	inLimitBytes := parseTrafficLimitGB(w.values["traffic_in_limit_gb"])
	outLimitBytes := parseTrafficLimitGB(w.values["traffic_out_limit_gb"])
	totalLimitBytes := parseTrafficLimitGB(w.values["traffic_total_limit_gb"])
	if !deployMonitor {
		inLimitBytes = 0
		outLimitBytes = 0
		totalLimitBytes = 0
	}
	resetDay, _ := strconv.Atoi(w.values["reset_day"])
	if !deployMonitor || resetDay < 1 || resetDay > 28 {
		resetDay = 1
	}

	challenge := acme.Challenge(w.values["challenge"])
	dnsCreds := map[string]string{}
	if challenge == acme.ChallengeDNS01 {
		switch w.values["dns_provider"] {
		case "cloudflare":
			dnsCreds["CF_API_TOKEN"] = w.values["dns_credential"]
		case "aliyun":
			if key, secret, ok := strings.Cut(w.values["dns_credential"], ":"); ok {
				dnsCreds["ALICLOUD_ACCESS_KEY"] = key
				dnsCreds["ALICLOUD_SECRET_KEY"] = secret
			}
		}
	}

	iface := ""
	if deployMonitor {
		iface, _ = monitor.DefaultInterface()
	}
	realityServerName := ""
	if hasProtocol(enabled, config.ProtocolRealityVision) || hasProtocol(enabled, config.ProtocolRealityGRPC) {
		realityServerName, err = normalizeRealityServerName(w.values["reality_sni"])
		if err != nil {
			return install.Config{}, err
		}
	}

	return install.Config{
		Domain:                 w.values["domain"],
		Email:                  w.values["email"],
		Challenge:              challenge,
		DNSProvider:            w.values["dns_provider"],
		DNSCredentials:         dnsCreds,
		Ports:                  ports,
		Enabled:                enabled,
		DisplayName:            w.values["display_name"],
		Salt:                   salt,
		RealityServerName:      realityServerName,
		RealityHandshakePort:   443,
		SubscribePort:          subscribePort,
		TrafficPort:            trafficPort,
		MonitorPort:            monitorPort,
		DeployMonitor:          deployMonitor,
		TrafficInLimitBytes:    inLimitBytes,
		TrafficOutLimitBytes:   outLimitBytes,
		TrafficTotalLimitBytes: totalLimitBytes,
		ResetDay:               resetDay,
		MonitorInterface:       iface,
		OS:                     w.host.OS,
		Firewall:               w.host.Firewall,
		Creds:                  creds,
	}, nil
}

func parseTrafficLimitGB(value string) uint64 {
	gb, _ := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return gb << 30
}

func parseInstallPort(value string, fallback int, label string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be between 1 and 65535", label)
	}
	return port, nil
}

func (w *wizard) applyCredentialOverrides(creds *install.Credentials) {
	if v := strings.TrimSpace(w.values["reality_vision_uuid"]); v != "" {
		creds.RealityVisionUUID = v
	}
	if v := strings.TrimSpace(w.values["reality_grpc_uuid"]); v != "" {
		creds.RealityGRPCUUID = v
	}
	if v := strings.TrimSpace(w.values["hysteria2_password"]); v != "" {
		creds.HysteriaPassword = v
	}
	if v := strings.TrimSpace(w.values["tuic_uuid"]); v != "" {
		creds.TUICUUID = v
	}
	if v := strings.TrimSpace(w.values["tuic_password"]); v != "" {
		creds.TUICPassword = v
	}
	if v := strings.TrimSpace(w.values["anytls_password"]); v != "" {
		creds.AnyTLSPassword = v
	}
}

func (w *wizard) protocolPorts(enabled []config.Protocol, subscribePort, trafficPort, monitorPort int, deployMonitor bool) (config.Ports, error) {
	used := map[int]bool{80: true, subscribePort: true}
	if deployMonitor {
		if used[trafficPort] {
			return config.Ports{}, fmt.Errorf("traffic monitor public port %d conflicts with another required port", trafficPort)
		}
		used[trafficPort] = true
		if used[monitorPort] {
			return config.Ports{}, fmt.Errorf("traffic monitor port %d conflicts with another required port", monitorPort)
		}
		used[monitorPort] = true
	}
	var ports config.Ports
	for _, proto := range enabled {
		port, err := w.portForProtocol(proto, used)
		if err != nil {
			return config.Ports{}, err
		}
		switch proto {
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

func (w *wizard) portForProtocol(proto config.Protocol, used map[int]bool) (int, error) {
	key := portFieldKey(proto)
	raw := strings.TrimSpace(w.values[key])
	if raw == "" {
		return randomListenPort(used)
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s port must be between 1 and 65535", proto)
	}
	if used[port] {
		return 0, fmt.Errorf("%s port %d conflicts with another selected port", proto, port)
	}
	used[port] = true
	return port, nil
}

func portFieldKey(proto config.Protocol) string {
	switch proto {
	case config.ProtocolRealityVision:
		return "reality_vision_port"
	case config.ProtocolRealityGRPC:
		return "reality_grpc_port"
	case config.ProtocolHysteria2:
		return "hysteria2_port"
	case config.ProtocolTUIC:
		return "tuic_port"
	case config.ProtocolAnyTLS:
		return "anytls_port"
	default:
		return ""
	}
}

func installedPortsSummary(enabled []config.Protocol, ports config.Ports) string {
	parts := make([]string, 0, len(enabled))
	for _, proto := range enabled {
		parts = append(parts, fmt.Sprintf("%s:%d", proto, installedPort(proto, ports)))
	}
	return strings.Join(parts, ", ")
}

func installedPort(proto config.Protocol, ports config.Ports) int {
	switch proto {
	case config.ProtocolRealityVision:
		return ports.RealityVision
	case config.ProtocolRealityGRPC:
		return ports.RealityGRPC
	case config.ProtocolHysteria2:
		return ports.Hysteria2
	case config.ProtocolTUIC:
		return ports.TUIC
	case config.ProtocolAnyTLS:
		return ports.AnyTLS
	default:
		return 0
	}
}

func randomListenPort(used map[int]bool) (int, error) {
	const minPort = 20000
	const maxPort = 59999
	span := big.NewInt(maxPort - minPort + 1)
	for range 1000 {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			return 0, err
		}
		port := int(n.Int64()) + minPort
		if !used[port] {
			used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("could not choose an unused random port")
}

var (
	wizardTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	wizardOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	wizardErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	wizardRandom = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

func (w *wizard) parameterProtocolLabel(f field) string {
	if len(f.paramsFor) == 0 {
		return ""
	}
	selected := make([]config.Protocol, 0, len(f.paramsFor))
	for _, p := range f.paramsFor {
		if protocolSelected(w.values, p) {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 {
		selected = f.paramsFor
	}
	return protocolLabels(selected)
}

func (w *wizard) fieldNote(f field) string {
	if f.key != "dns_credential" {
		return f.note
	}
	if w.values["dns_provider"] == "aliyun" {
		return "Aliyun uses accessKey:secretKey (AccessKey ID:AccessKey Secret).\nYou can apply at https://ram.console.aliyun.com/manage/ak"
	}
	return "Cloudflare uses an API token.\nYou can apply at https://dash.cloudflare.com/profile/api-tokens"
}

// View renders the wizard.
func (w *wizard) View() string {
	switch w.phase {
	case phasePreflight:
		body := w.hosts + "\n\n"
		if w.canProceed() {
			body += dimStyle.Render("enter to begin · esc to cancel")
		} else {
			body += wizardErr.Render("Cannot proceed. ") + dimStyle.Render("esc to go back")
		}
		return wizardTitle.Render("Install · Preflight") + "\n\n" + body
	case phaseForm:
		f := w.fields[w.fieldIx]
		var b strings.Builder
		b.WriteString(wizardTitle.Render("Install · Configuration") + "\n\n")
		b.WriteString(f.label + "\n")
		if label := w.parameterProtocolLabel(f); label != "" {
			b.WriteString(wizardOK.Render("Setting parameters for: "+label) + "\n")
		}
		if note := w.fieldNote(f); note != "" {
			for _, line := range wrapFieldNote(note, w.width) {
				b.WriteString(dimStyle.Render(line) + "\n")
			}
		}
		if f.def != "" {
			b.WriteString(dimStyle.Render("default: "+f.def) + "\n")
		}
		if w.fieldErr != "" {
			b.WriteString(wizardErr.Render(w.fieldErr) + "\n")
		}
		if len(f.options) > 0 {
			b.WriteString(w.optionsView(f) + "\n\n")
			if f.multi {
				b.WriteString(dimStyle.Render("space toggle · enter to continue · ↑/↓ or ←/→ move · shift+tab/ctrl+b back · esc to cancel"))
				return b.String()
			}
			b.WriteString(dimStyle.Render("enter to continue · ↑/↓ or ←/→ select · shift+tab/ctrl+b back · esc to cancel"))
			return b.String()
		}
		b.WriteString(w.input.View() + "\n\n")
		b.WriteString(dimStyle.Render("enter to continue · shift+tab/ctrl+b back · esc to cancel"))
		return b.String()
	case phaseConfirm:
		return w.confirmView()
	case phaseRunning:
		return w.runningView()
	case phaseDone:
		if w.runErr != nil {
			return w.failedView()
		}
		return wizardOK.Render("Install complete") + "\n\n" + w.doneSummary() + "\n\n" +
			dimStyle.Render("press any key to return")
	}
	return ""
}

func (w *wizard) footerHints() []string {
	switch w.phase {
	case phasePreflight:
		return []string{"enter continue", "esc/q cancel"}
	case phaseForm:
		return []string{"enter continue", "shift+tab back", "esc cancel"}
	case phaseConfirm:
		return []string{"↑/↓ scroll", "enter install", "esc cancel"}
	case phaseRunning:
		if w.runComplete {
			return []string{"enter summary", "↑/↓ scroll log"}
		}
		return []string{"↑/↓ scroll log"}
	case phaseDone:
		if w.runErr != nil {
			return []string{"↑/↓ scroll log", "any other key return"}
		}
		return []string{"any key return"}
	default:
		return nil
	}
}

func (w *wizard) confirmView() string {
	viewportHeight := w.confirmViewportHeight()
	lines := w.visibleConfirmLines(viewportHeight)
	return wizardTitle.Render("Install · Confirm") + "\n\n" + strings.Join(lines, "\n") + "\n\n" +
		dimStyle.Render("↑/↓ or mouse wheel scroll · enter/y to install · shift+tab/ctrl+b back · esc/n to cancel")
}

func (w *wizard) visibleConfirmLines(height int) []string {
	rows := w.confirmRows()
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	w.clampConfirmScroll(height)
	start := min(w.confirmScroll, max(0, len(rows)-height))
	end := min(start+height, len(rows))
	return rows[start:end]
}

func (w *wizard) scrollConfirm(delta, height int) {
	w.confirmScroll += delta
	w.clampConfirmScroll(height)
}

func (w *wizard) clampConfirmScroll(height int) {
	w.confirmScroll = min(max(0, w.confirmScroll), w.maxConfirmScroll(height))
}

func (w *wizard) maxConfirmScroll(height int) int {
	if height <= 0 {
		return 0
	}
	return max(0, len(w.confirmRows())-height)
}

func (w *wizard) confirmRows() []string {
	summary := strings.TrimRight(w.summary(), "\n")
	if summary == "" {
		return nil
	}
	wrapped := lipgloss.NewStyle().Width(w.confirmWrapWidth()).Render(summary)
	return strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
}

func (w *wizard) confirmViewportHeight() int {
	if w.height <= 0 {
		return 12
	}
	return max(1, w.height-4)
}

func (w *wizard) confirmWrapWidth() int {
	if w.width <= 0 {
		return 80
	}
	return max(1, w.width)
}

func (w *wizard) optionsView(f field) string {
	var rows []string
	for i, opt := range f.options {
		label := opt
		if f.multi {
			mark := "[ ]"
			if w.optionSelected[opt] {
				mark = "[x]"
			}
			label = mark + " " + opt
		}
		row := "  " + label
		if i == w.optionIx {
			row = selStyle.Render("> " + label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (w *wizard) runningView() string {
	logs := w.logView(w.logViewportHeight())
	hint := "↑/↓ scroll log"
	if w.runComplete {
		hint = "complete · press enter to show summary · " + hint
	}
	body := wizardTitle.Render("Install · Running") + "\n\n" + w.bar.ViewAs(w.percent())
	if logs != "" {
		body += "\n\n" + logs
	}
	return body + "\n\n" + dimStyle.Render(hint)
}

func (w *wizard) failedView() string {
	body := wizardErr.Render("Install failed") + "\n\n" + w.runErr.Error()
	if logs := w.logView(w.doneLogHeight()); logs != "" {
		body += "\n\n" + logs + "\n\n" + dimStyle.Render("↑/↓ scroll log · any other key to return")
		return body
	}
	return body + "\n\n" + dimStyle.Render("press any key to return")
}

func (w *wizard) logView(height int) string {
	lines := w.visibleLogLines(height)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (w *wizard) visibleLogLines(height int) []string {
	rows := w.logRows()
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	visible := min(height, len(rows))
	w.clampLogScroll(height)
	start := len(rows) - visible - w.logScroll
	return rows[start : start+visible]
}

func (w *wizard) scrollLog(delta, height int) {
	w.logScroll += delta
	w.clampLogScroll(height)
}

func (w *wizard) clampLogScroll(height int) {
	w.logScroll = min(max(0, w.logScroll), w.maxLogScroll(height))
}

func (w *wizard) maxLogScroll(height int) int {
	if height <= 0 {
		return 0
	}
	return max(0, len(w.logRows())-height)
}

func (w *wizard) logRows() []string {
	var rows []string
	for _, line := range w.logBuf {
		rows = append(rows, w.wrapLogLine(line)...)
	}
	return rows
}

func (w *wizard) wrapLogLine(line string) []string {
	wrapped := lipgloss.NewStyle().Width(w.logWrapWidth()).Render(line)
	return strings.Split(wrapped, "\n")
}

func (w *wizard) logLineHeight(line string) int {
	return max(1, lipgloss.Height(lipgloss.NewStyle().Width(w.logWrapWidth()).Render(line)))
}

func (w *wizard) logWrapWidth() int {
	if w.width <= 0 {
		return 80
	}
	return max(1, w.width)
}

func (w *wizard) logViewportHeight() int {
	if w.height <= 0 {
		return 12
	}
	return max(1, w.height-6)
}

func (w *wizard) doneLogHeight() int {
	if w.height <= 0 {
		return 12
	}
	return max(1, w.height-7)
}

func wrapFieldNote(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	wrapWidth := max(24, width-4)
	var lines []string
	for _, part := range strings.Split(s, "\n") {
		lines = append(lines, wrapWords(part, wrapWidth)...)
	}
	return lines
}

func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}

func (w *wizard) percent() float64 {
	if len(w.events) == 0 {
		return 0
	}
	last := w.events[len(w.events)-1]
	if last.Total == 0 {
		return 0
	}
	return float64(last.Index) / float64(last.Total)
}

func (w *wizard) summary() string {
	protocols := protocolsFromValue(w.values["protocols"])
	if len(protocols) == 0 {
		protocols = config.AllProtocols
	}
	deployMonitor := trafficMonitorEnabled(w.values)
	rows := []summaryLine{
		summaryRow("Domain", w.values["domain"]),
		summaryRow("Email", or(w.values["email"], "not set")),
		summaryRow("ACME challenge", w.values["challenge"]),
		summaryRow("Protocols", protocolLabels(protocols)),
		summaryRow("Display name", w.values["display_name"]),
		summaryRow("Subscription port", or(w.values["subscribe_port"], "2096")),
		summaryRow("Subscription salt", summaryValueOrRandom(w.values["subscribe_salt"])),
		summaryRow("Traffic monitor", yesNoString(deployMonitor)),
		summaryRow("Operating system / architecture", w.host.OS.ID+" / "+w.host.Arch),
		summaryRow("Firewall", firewallName(w.host.Firewall)),
	}
	if deployMonitor {
		rows = append(rows,
			summaryRow("Traffic monitor public port", or(w.values["traffic_port"], "2097")),
			summaryRow("Traffic monitor local port", or(w.values["monitor_port"], "19090")),
			summaryRow("Inbound traffic limit", w.values["traffic_in_limit_gb"]+" GB"),
			summaryRow("Outbound traffic limit", w.values["traffic_out_limit_gb"]+" GB"),
			summaryRow("Total traffic limit", w.values["traffic_total_limit_gb"]+" GB"),
		)
	}
	if hasProtocol(protocols, config.ProtocolRealityVision) || hasProtocol(protocols, config.ProtocolRealityGRPC) {
		rows = append(rows, summaryRow("Reality URL/SNI", w.values["reality_sni"]))
	}
	rows = append(rows, summaryText("Protocol parameters:"))
	for _, proto := range protocols {
		rows = append(rows, summaryIndentedRow(2, fmt.Sprintf("%s port", proto), summaryValueOrRandom(w.values[portFieldKey(proto)])))
	}
	return renderSummary(rows) + "\n"
}

func summaryValueOrRandom(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "random"
	}
	return value
}

func subscriptionSaltSummary(value string) string {
	return highlightSummaryText(summaryValueOrRandom(value))
}

func (w *wizard) doneSummary() string {
	token := install.SubscriptionToken(w.cfg.Salt)
	subscriptionBase := fmt.Sprintf("https://%s:%d", w.cfg.Domain, w.cfg.SubscribePort)
	rows := []summaryLine{
		summaryRow("Account", w.cfg.DisplayName),
		summaryRow("Protocols", protocolLabels(w.cfg.Enabled)),
		summaryRow("Ports", installedPortsSummary(w.cfg.Enabled, w.cfg.Ports)),
		summaryRow("Subscription", subscriptionBase+"/s/default/"+token),
		summaryRow("Clash", subscriptionBase+"/s/clashMetaProfiles/"+token),
		summaryRow("sing-box", subscriptionBase+"/s/sing-box/"+token),
	}
	if w.cfg.DeployMonitor {
		trafficBase := fmt.Sprintf("https://%s:%d", w.cfg.Domain, w.cfg.TrafficPort)
		rows = append(rows, summaryRow("Traffic UI", trafficBase+"/traffic/"))
	}
	return renderSummary(rows)
}

func yesNoString(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// logWriter forwards streamed command output to the UI via the run channel. It
// runs on the orchestrator goroutine, so it must not touch wizard state directly.
type logWriter struct {
	ch chan runMsg
}

func (lw *logWriter) Write(p []byte) (int, error) {
	text := sanitizeLogOutput(string(p))
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		msg := runMsg{logLine: line}
		select {
		case lw.ch <- msg:
		default:
		}
	}
	return len(p), nil
}

func sanitizeLogOutput(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\x1b':
			i = skipANSIEscape(s, i)
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			b.WriteByte('\n')
		case '\n', '\t':
			b.WriteByte(c)
		default:
			if c >= 0x20 || c >= 0x80 {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func skipANSIEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i
	}
	i++
	switch s[i] {
	case '[':
		for i+1 < len(s) {
			i++
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i
			}
		}
	case ']', 'P', '^', '_':
		for i+1 < len(s) {
			i++
			if s[i] == '\a' {
				return i
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 1
			}
		}
	}
	return i
}
