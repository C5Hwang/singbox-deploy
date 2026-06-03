package ui

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	key     string
	label   string
	def     string
	note    string
	options []string
	// skip reports whether this field is hidden given the values so far.
	skip func(vals map[string]string) bool
}

// installFields defines the wizard's input sequence.
func installFields() []field {
	isDNS := func(v map[string]string) bool { return v["challenge"] != "dns-01" }
	return []field{
		{key: "domain", label: "Domain (must resolve to this server)", note: "Used for certificate issuance, Nginx server_name, subscription URLs, and TLS SNI."},
		{key: "email", label: "ACME account email (optional)", note: "Optional Let's Encrypt account contact used for certificate notices."},
		{key: "challenge", label: "ACME challenge", def: "http-01", options: []string{"http-01", "dns-01"}, note: "http-01 validates through port 80; dns-01 validates through the DNS API provider."},
		{key: "dns_provider", label: "DNS provider", def: "cloudflare", options: []string{"cloudflare", "aliyun"}, note: "Only used for dns-01. Supported providers are Cloudflare and Aliyun.", skip: isDNS},
		{key: "dns_credential", label: "DNS credential (CF token, or aliyun accessKey:secretKey)", note: "Cloudflare uses an API token. Aliyun uses accessKey:secretKey. Passed to ACME for DNS validation.", skip: isDNS},
		{key: "reality_sni", label: "Reality SNI (camouflage server)", def: "www.microsoft.com", note: "Used as the Reality handshake server name; choose a real HTTPS host suitable for camouflage."},
		{key: "display_name", label: "Node display name", def: "Node", note: "Used only in generated node names shown by clients."},
		{key: "traffic_limit_gb", label: "Monthly traffic limit in GB (0 = unlimited)", def: "0", note: "Used by the traffic monitor quota. When exceeded, sing-box.service is stopped automatically."},
		{key: "reset_day", label: "Monthly reset day (1-28)", def: "1", note: "Day of month when the traffic quota cycle resets and service can be restored."},
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

	fields   []field
	fieldIx  int
	values   map[string]string
	input    textinput.Model
	optionIx int
	fieldErr string

	validateDomain func(context.Context, string) error

	bar                 progress.Model
	events              []install.Event
	logBuf              []string
	logScroll           int
	runErr              error
	cfg                 install.Config
	dryRun              bool
	dryRunAwaitingEnter bool

	ch chan runMsg
}

// newWizard builds the wizard, running host preflight immediately.
func newWizard(dryRun bool) *wizard {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Prompt = "› "

	w := &wizard{
		phase:          phasePreflight,
		fields:         installFields(),
		values:         map[string]string{},
		input:          ti,
		bar:            progress.New(progress.WithDefaultGradient()),
		dryRun:         dryRun,
		validateDomain: validateDomainResolvesToCurrentIP,
	}
	host, err := system.DetectHost()
	w.host = host
	switch {
	case err != nil:
		w.hosts = "Failed to detect host: " + err.Error()
	case !host.IsRoot && !dryRun:
		w.hosts = "This installer must be run as root."
	case !host.Supported():
		w.hosts = fmt.Sprintf("Unsupported system: family=%q arch=%q", host.OS.Family, host.Arch)
	case host.SELinux && !dryRun:
		w.hosts = "SELinux is enforcing; installation is blocked. Set it permissive and retry."
	default:
		if dryRun {
			w.hosts = fmt.Sprintf("Detected %s/%s, firewall=%s - dry-run ready.", host.OS.ID, host.Arch, firewallName(host.Firewall))
		} else {
			w.hosts = fmt.Sprintf("Detected %s/%s, firewall=%s — ready.", host.OS.ID, host.Arch, firewallName(host.Firewall))
		}
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
	return (w.dryRun || w.host.IsRoot) && w.host.Supported() && (w.dryRun || !w.host.SELinux)
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
		w.optionIx = optionIndex(f.options, value)
		w.input.Blur()
		return
	}
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
		return f.options[min(max(0, w.optionIx), len(f.options)-1)]
	}
	val := strings.TrimSpace(w.input.Value())
	if val == "" {
		return f.def
	}
	return val
}

func (w *wizard) validateField(f field, val string) error {
	if f.key != "domain" {
		return nil
	}
	if val == "" {
		return fmt.Errorf("domain is required")
	}
	if w.dryRun {
		return nil
	}
	if w.validateDomain == nil {
		return nil
	}
	return w.validateDomain(context.Background(), val)
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
		case "enter", "y":
			return w.startRun(), false
		case "shift+tab", "ctrl+b":
			w.backToLastField()
		case "esc", "n":
			return nil, true
		}
	case phaseRunning:
		switch msg.String() {
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
		case "enter":
			if !w.dryRun || !w.dryRunAwaitingEnter {
				break
			}
			w.dryRunAwaitingEnter = false
			w.logScroll = 0
			return w.waitForRun(), false
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

func (w *wizard) moveOption(delta int) {
	if !w.currentFieldHasOptions() {
		return
	}
	options := w.fields[w.fieldIx].options
	w.optionIx = (w.optionIx + delta + len(options)) % len(options)
	w.fieldErr = ""
}

func (w *wizard) handleRun(msg runMsg) tea.Cmd {
	visible := false
	if msg.event != nil {
		visible = true
		w.events = append(w.events, *msg.event)
		e := *msg.event
		line := fmt.Sprintf("[%d/%d] %s — %s", e.Index, e.Total, e.Label, e.Status)
		if e.Err != nil {
			line += ": " + e.Err.Error()
		}
		w.appendLog(line)
	}
	if msg.logLine != "" {
		visible = true
		w.appendLog(dimStyle.Render(msg.logLine))
	}
	if msg.done {
		w.dryRunAwaitingEnter = false
		w.runErr = msg.err
		w.phase = phaseDone
		return nil
	}
	if w.dryRun && visible {
		w.dryRunAwaitingEnter = true
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
	w.ch = make(chan runMsg, 64)

	ch := w.ch
	logs := &logWriter{ch: ch, block: w.dryRun}
	runner := system.Runner(system.NewExecRunner(logs))
	layout := paths.DefaultLayout()
	issuer := acme.NewLegoIssuer()
	issuer.Output = logs
	acmeManager := acme.NewManager(issuer)
	releases := release.NewClient("", nil)
	var latestSingBox func(context.Context) (string, error)
	var download func(context.Context, string, string) error
	var systemdDir, nginxConfPath string

	if w.dryRun {
		root, err := os.MkdirTemp("", "singbox-deploy-dry-run-*")
		if err != nil {
			w.runErr = err
			w.phase = phaseDone
			return nil
		}
		ch <- runMsg{logLine: "dry-run mode: system commands will be printed only"}
		ch <- runMsg{logLine: "dry-run output root: " + root}
		dryRunner := system.NewDryRunRunner(logs)
		runner = dryRunner
		layout = paths.LayoutForRoot(root)
		acmeManager = acme.NewManager(dryRunIssuer{})
		releases = nil
		latestSingBox = func(context.Context) (string, error) { return "v1.12.0", nil }
		download = func(_ context.Context, url, dest string) error {
			cmd := "download " + url + " -> " + dest
			ch <- runMsg{logLine: "[dry-run] " + cmd}
			return writeDryRunSingBoxArchive(dest)
		}
		systemdDir = filepath.Join(root, "systemd")
		nginxConfPath = filepath.Join(root, "nginx", "singbox-deploy.conf")
	}

	orch := &install.Orchestrator{
		Runner:        runner,
		Layout:        layout,
		ACME:          acmeManager,
		Releases:      releases,
		Download:      download,
		LatestSingBox: latestSingBox,
		GOOS:          "linux",
		GOARCH:        w.host.Arch,
		SystemdDir:    systemdDir,
		NginxConfPath: nginxConfPath,
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
	salt, err := credentials.Salt()
	if err != nil {
		return install.Config{}, err
	}
	limitGB, _ := strconv.ParseUint(w.values["traffic_limit_gb"], 10, 64)
	resetDay, _ := strconv.Atoi(w.values["reset_day"])
	if resetDay < 1 || resetDay > 28 {
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

	iface, _ := monitor.DefaultInterface()

	return install.Config{
		Domain:               w.values["domain"],
		Email:                w.values["email"],
		Challenge:            challenge,
		DNSProvider:          w.values["dns_provider"],
		DNSCredentials:       dnsCreds,
		Ports:                config.Ports{RealityVision: 443, RealityGRPC: 8443, Hysteria2: 2087, TUIC: 2088, AnyTLS: 2089},
		DisplayName:          w.values["display_name"],
		Salt:                 salt,
		RealityServerName:    w.values["reality_sni"],
		RealityHandshakePort: 443,
		SubscribePort:        2096,
		MonitorPort:          19090,
		TrafficLimitBytes:    limitGB << 30,
		ResetDay:             resetDay,
		MonitorInterface:     iface,
		OS:                   w.host.OS,
		Firewall:             w.host.Firewall,
		Creds:                creds,
	}, nil
}

var (
	wizardTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	wizardOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	wizardErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

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
		if f.note != "" {
			for _, line := range wrapFieldNote(f.note, w.width) {
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
			b.WriteString(dimStyle.Render("enter to continue · ↑/↓ or ←/→ select · shift+tab/ctrl+b back · esc to cancel"))
			return b.String()
		}
		b.WriteString(w.input.View() + "\n\n")
		b.WriteString(dimStyle.Render("enter to continue · shift+tab/ctrl+b back · esc to cancel"))
		return b.String()
	case phaseConfirm:
		return wizardTitle.Render("Install · Confirm") + "\n\n" + w.summary() + "\n" +
			dimStyle.Render("enter/y to install · shift+tab/ctrl+b back · esc/n to cancel")
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

func (w *wizard) optionsView(f field) string {
	var rows []string
	for i, opt := range f.options {
		row := "  " + opt
		if i == w.optionIx {
			row = selStyle.Render("> " + opt)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (w *wizard) runningView() string {
	logs := w.logView(w.logViewportHeight())
	hint := "↑/↓ scroll log"
	if w.dryRunAwaitingEnter {
		hint = "enter to show next dry-run update · " + hint
	} else if w.dryRun {
		hint = "waiting for next dry-run update · " + hint
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
	return wrapWords(s, max(24, width-4))
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
	rows := []string{
		"Domain:        " + w.values["domain"],
		"Email:         " + or(w.values["email"], "not set"),
		"Challenge:     " + w.values["challenge"],
		"Reality SNI:   " + w.values["reality_sni"],
		"Display name:  " + w.values["display_name"],
		"Traffic limit: " + w.values["traffic_limit_gb"] + " GB",
		"OS / Arch:     " + w.host.OS.ID + " / " + w.host.Arch,
		"Firewall:      " + firewallName(w.host.Firewall),
	}
	return strings.Join(rows, "\n") + "\n"
}

func (w *wizard) doneSummary() string {
	token := install.SubscriptionToken(w.cfg.Salt)
	base := fmt.Sprintf("https://%s:%d", w.cfg.Domain, w.cfg.SubscribePort)
	return strings.Join([]string{
		"Account:       " + w.cfg.DisplayName,
		"Subscription:  " + base + "/s/default/" + token,
		"Clash:         " + base + "/s/clashMetaProfiles/" + token,
		"sing-box:      " + base + "/s/sing-box/" + token,
		"Traffic UI:    " + base + "/traffic/",
	}, "\n")
}

// logWriter forwards streamed command output to the UI via the run channel. It
// runs on the orchestrator goroutine, so it must not touch wizard state directly.
type logWriter struct {
	ch    chan runMsg
	block bool
}

func (lw *logWriter) Write(p []byte) (int, error) {
	text := sanitizeLogOutput(string(p))
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		msg := runMsg{logLine: line}
		if lw.block {
			lw.ch <- msg
			continue
		}
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

type dryRunIssuer struct{}

func (dryRunIssuer) Issue(context.Context, acme.Request) (acme.Certificate, error) {
	return acme.Certificate{
		CertificatePEM: []byte("-----BEGIN CERTIFICATE-----\ndry-run\n-----END CERTIFICATE-----\n"),
		PrivateKeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\ndry-run\n-----END PRIVATE KEY-----\n"),
	}, nil
}

func writeDryRunSingBoxArchive(dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := "#!/bin/sh\nexit 0\n"
	hdr := &tar.Header{
		Name:     "sing-box-dry-run/sing-box",
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
