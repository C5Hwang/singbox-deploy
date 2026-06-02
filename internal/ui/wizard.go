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
	phaseDryRunReview
	phaseDone
)

// field is one collected input.
type field struct {
	key   string
	label string
	def   string
	// skip reports whether this field is hidden given the values so far.
	skip func(vals map[string]string) bool
}

// installFields defines the wizard's input sequence.
func installFields() []field {
	isDNS := func(v map[string]string) bool { return v["challenge"] != "dns-01" }
	return []field{
		{key: "domain", label: "Domain (must resolve to this server)"},
		{key: "email", label: "ACME account email"},
		{key: "challenge", label: "ACME challenge (http-01 / dns-01)", def: "http-01"},
		{key: "dns_provider", label: "DNS provider (cloudflare / aliyun)", def: "cloudflare", skip: isDNS},
		{key: "dns_credential", label: "DNS credential (CF token, or aliyun accessKey:secretKey)", skip: isDNS},
		{key: "reality_sni", label: "Reality SNI (camouflage server)", def: "www.microsoft.com"},
		{key: "display_name", label: "Node display name", def: "Node"},
		{key: "traffic_limit_gb", label: "Monthly traffic limit in GB (0 = unlimited)", def: "0"},
		{key: "reset_day", label: "Monthly reset day (1-28)", def: "1"},
	}
}

// runMsg carries an orchestrator progress event, a streamed log line, or
// completion into the UI. It is the only channel the orchestrator goroutine
// uses to communicate, so all wizard state stays mutated on the UI goroutine.
type runMsg struct {
	event         *install.Event
	logLine       string
	dryRunCommand string
	done          bool
	err           error
}

// wizard is the interactive install flow.
type wizard struct {
	phase  wizardPhase
	host   system.Host
	hosts  string // preflight summary / error text
	width  int
	height int

	fields  []field
	fieldIx int
	values  map[string]string
	input   textinput.Model

	bar    progress.Model
	events []install.Event
	logBuf []string
	runErr error
	cfg    install.Config
	dryRun bool
	cmds   []string

	ch chan runMsg
}

// newWizard builds the wizard, running host preflight immediately.
func newWizard(dryRun bool) *wizard {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Prompt = "› "

	w := &wizard{
		phase:  phasePreflight,
		fields: installFields(),
		values: map[string]string{},
		input:  ti,
		bar:    progress.New(progress.WithDefaultGradient()),
		dryRun: dryRun,
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
		w.fieldIx = i
		w.input.SetValue("")
		w.input.Placeholder = f.def
		w.input.Focus()
		return
	}
	w.phase = phaseConfirm
}

// commitField stores the current field value (or its default) and advances.
func (w *wizard) commitField() {
	f := w.fields[w.fieldIx]
	val := strings.TrimSpace(w.input.Value())
	if val == "" {
		val = f.def
	}
	w.values[f.key] = val
	w.advanceField()
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
	if w.phase == phaseForm {
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
		case "esc":
			return nil, true
		default:
			var cmd tea.Cmd
			w.input, cmd = w.input.Update(msg)
			return cmd, false
		}
	case phaseConfirm:
		switch msg.String() {
		case "enter", "y":
			return w.startRun(), false
		case "esc", "n":
			return nil, true
		}
	case phaseRunning:
		// Ignore input while running, except quit-after-failure handled in done.
	case phaseDryRunReview:
		w.phase = phaseDone
		return nil, false
	case phaseDone:
		return nil, true
	}
	return nil, false
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
	if msg.dryRunCommand != "" {
		w.cmds = append(w.cmds, msg.dryRunCommand)
	}
	if msg.done {
		w.runErr = msg.err
		if w.dryRun && msg.err == nil {
			w.phase = phaseDryRunReview
		} else {
			w.phase = phaseDone
		}
		return nil
	}
	return w.waitForRun()
}

func (w *wizard) appendLog(line string) {
	w.logBuf = append(w.logBuf, line)
	if len(w.logBuf) > 12 {
		w.logBuf = w.logBuf[len(w.logBuf)-12:]
	}
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
	logs := &logWriter{ch: ch}
	runner := system.Runner(system.NewExecRunner(logs))
	layout := paths.DefaultLayout()
	acmeManager := acme.NewManager(acme.NewLegoIssuer())
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
		dryRunner.OnCommand = func(c system.Command) {
			ch <- runMsg{dryRunCommand: c.String()}
		}
		runner = dryRunner
		layout = paths.LayoutForRoot(root)
		acmeManager = acme.NewManager(dryRunIssuer{})
		releases = nil
		latestSingBox = func(context.Context) (string, error) { return "v1.12.0", nil }
		download = func(_ context.Context, url, dest string) error {
			cmd := "download " + url + " -> " + dest
			ch <- runMsg{dryRunCommand: cmd}
			select {
			case ch <- runMsg{logLine: "[dry-run] " + cmd}:
			default:
			}
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
		if f.def != "" {
			b.WriteString(dimStyle.Render("default: "+f.def) + "\n")
		}
		b.WriteString(w.input.View() + "\n\n")
		b.WriteString(dimStyle.Render("enter to continue · esc to cancel"))
		return b.String()
	case phaseConfirm:
		return wizardTitle.Render("Install · Confirm") + "\n\n" + w.summary() + "\n" +
			dimStyle.Render("enter/y to install · esc/n to cancel")
	case phaseRunning:
		return wizardTitle.Render("Install · Running") + "\n\n" +
			w.bar.ViewAs(w.percent()) + "\n\n" + strings.Join(w.logBuf, "\n")
	case phaseDryRunReview:
		return wizardTitle.Render("Install · Dry-run commands") + "\n\n" +
			w.dryRunCommandReview() + "\n\n" + dimStyle.Render("press any key to continue to completion")
	case phaseDone:
		if w.runErr != nil {
			return wizardErr.Render("Install failed") + "\n\n" + w.runErr.Error() + "\n\n" +
				strings.Join(w.logBuf, "\n") + "\n\n" + dimStyle.Render("press any key to return")
		}
		return wizardOK.Render("Install complete") + "\n\n" + w.doneSummary() + "\n\n" +
			dimStyle.Render("press any key to return")
	}
	return ""
}

func (w *wizard) dryRunCommandReview() string {
	if len(w.cmds) == 0 {
		return "No system commands were captured."
	}
	var b strings.Builder
	b.WriteString("dry-run mode: commands were printed only and not executed.\n\n")
	for i, cmd := range w.cmds {
		cmd = strings.ReplaceAll(cmd, "\n", "\n    ")
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, cmd))
	}
	return strings.TrimRight(b.String(), "\n")
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
		"Email:         " + w.values["email"],
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

// logWriter forwards streamed command output to the UI via the run channel.
// It runs on the orchestrator goroutine, so it must not touch wizard state
// directly. Sends are non-blocking: if the buffer is full, lines are dropped
// rather than stalling the install.
type logWriter struct{ ch chan runMsg }

func (lw *logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		select {
		case lw.ch <- runMsg{logLine: line}:
		default:
		}
	}
	return len(p), nil
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
