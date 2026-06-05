package ui

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

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
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// installPhase is the install flow's current screen.
type installPhase int

const (
	phasePreflight installPhase = iota
	phaseForm
	phaseConfirm
	phaseRunning
	phaseDone
)

// installFields defines the install form's input sequence.
func installFields() []field {
	isDNS := func(v map[string]string) bool { return v["challenge"] != "dns-01" }
	missingProtocol := func(p config.Protocol) func(map[string]string) bool {
		return func(v map[string]string) bool { return !protocolSelected(v, p) }
	}
	noReality := func(v map[string]string) bool {
		return !protocolSelected(v, config.ProtocolRealityVision) && !protocolSelected(v, config.ProtocolRealityGRPC)
	}
	monitorDisabled := func(v map[string]string) bool { return !trafficMonitorEnabled(v) }
	fields := []field{
		{key: "domain", label: "Domain (must resolve to this server)", note: "Used for certificate issuance, Nginx server_name, subscription URLs, and TLS SNI."},
		{key: "email", label: "ACME account email (optional)", note: "Optional Let's Encrypt account contact used for certificate notices."},
		{key: "challenge", label: "ACME challenge", def: "http-01", options: []string{"http-01", "dns-01"}, note: "http-01 validates through port 80; dns-01 validates through the DNS API provider."},
		{key: "dns_provider", label: "DNS provider", def: "cloudflare", options: []string{"cloudflare", "aliyun"}, note: "Only used for dns-01. Supported providers are Cloudflare and Aliyun.", skip: isDNS},
		{key: "dns_credential", label: "DNS API credential", skip: isDNS, noteFunc: dnsCredentialNote},
		{key: "protocols", label: "Protocols to install", def: defaultProtocolValue(), options: protocolOptions(), multi: true, note: "Select one or more protocols. At least one protocol must remain selected."},
	}
	fields = append(fields, installProtocolParameterFields(missingProtocol, noReality)...)
	fields = append(fields, fieldsFromParameters(uiparams.SubscriptionInstallFields())...)
	fields = append(fields, []field{
		{key: "traffic_monitor", label: "Deploy traffic monitor", def: "yes", options: []string{"yes", "no"}, note: "Choose no to skip the traffic monitor service and /traffic/ UI."},
		{key: "traffic_port", label: "Traffic monitor public HTTPS port", def: strconv.Itoa(install.DefaultTrafficPort), note: "Nginx listens on this public HTTPS port for /traffic.", skip: monitorDisabled},
		{key: "monitor_port", label: "Traffic monitor local port", def: strconv.Itoa(install.DefaultMonitorPort), note: "The monitor listens on 127.0.0.1 and Nginx proxies /traffic to this port.", skip: monitorDisabled},
		{key: "traffic_in_limit_gb", label: "Monthly inbound traffic limit in GB (0 = unlimited)", def: "0", note: "Inbound uses the monitored interface RX counter. When any configured limit is exceeded, sing-box.service is stopped automatically.", skip: monitorDisabled},
		{key: "traffic_out_limit_gb", label: "Monthly outbound traffic limit in GB (0 = unlimited)", def: "0", note: "Outbound uses the monitored interface TX counter.", skip: monitorDisabled},
		{key: "traffic_total_limit_gb", label: "Monthly total traffic limit in GB (0 = unlimited)", def: "0", note: "Total traffic is inbound + outbound.", skip: monitorDisabled},
		{key: "reset_day", label: "Monthly reset day (1-28)", def: strconv.Itoa(install.DefaultResetDay), note: "Day of month when the traffic quota cycle resets and service can be restored.", skip: monitorDisabled},
	}...)
	return fields
}

func installProtocolParameterFields(missingProtocol func(config.Protocol) func(map[string]string) bool, noReality func(map[string]string) bool) []field {
	fields := []field{fieldFromParameter(uiparams.RealitySNIField())}
	fields[0].skip = noReality
	fields[0].badgeFunc = protocolParameterBadge(config.ProtocolRealityVision, config.ProtocolRealityGRPC)
	for _, proto := range config.AllProtocols {
		for _, field := range fieldsFromParameters(uiparams.ProtocolInstallFieldsForProtocol(proto)) {
			field.skip = missingProtocol(proto)
			field.badgeFunc = protocolParameterBadge(proto)
			fields = append(fields, field)
		}
	}
	return fields
}

func protocolParameterBadge(protocols ...config.Protocol) func(map[string]string) string {
	return func(vals map[string]string) string {
		selected := make([]config.Protocol, 0, len(protocols))
		for _, p := range protocols {
			if protocolSelected(vals, p) {
				selected = append(selected, p)
			}
		}
		if len(selected) == 0 {
			selected = protocols
		}
		return "Setting parameters for: " + protocolLabels(selected)
	}
}

func dnsCredentialNote(vals map[string]string) string {
	if vals["dns_provider"] == "aliyun" {
		return "Aliyun uses accessKey:secretKey (AccessKey ID:AccessKey Secret).\nYou can apply at https://ram.console.aliyun.com/manage/ak"
	}
	return "Cloudflare uses an API token.\nYou can apply at https://dash.cloudflare.com/profile/api-tokens"
}

// runMsg carries an orchestrator progress event, a streamed log line, or
// completion into the UI. It is the only channel the orchestrator goroutine
// uses to communicate, so all UI state stays mutated on the UI goroutine.
type runMsg struct {
	event     *install.Event
	logLine   string
	done      bool
	err       error
	resultTag string
}

// installForm owns only install input collection and confirmation rendering.
type installForm struct {
	parameterForm

	validateDomain func(context.Context, string) error
	confirmScroll  int
}

// installFlow owns the install lifecycle and delegates input collection to form
// and command execution UI to commandRun.
type installFlow struct {
	phase installPhase
	host  system.Host
	hosts string // preflight summary / error text

	form installForm
	run  commandRun
	cfg  install.Config
}

func newInstallForm() installForm {
	return installForm{
		parameterForm:  newParameterForm(installFields()),
		validateDomain: validateDomainResolvesToCurrentIP,
	}
}

// newInstallFlow builds the install flow, running host preflight immediately.
func newInstallFlow() *installFlow {
	flow := &installFlow{
		phase: phasePreflight,
		form:  newInstallForm(),
		run:   newCommandRun(),
	}
	host, err := system.DetectHost()
	flow.host = host
	switch {
	case err != nil:
		flow.hosts = "Failed to detect host: " + err.Error()
	case !host.IsRoot:
		flow.hosts = "This installer must be run as root."
	case !host.Supported():
		flow.hosts = fmt.Sprintf("Unsupported system: family=%q arch=%q", host.OS.Family, host.Arch)
	case host.SELinux:
		flow.hosts = "SELinux is enforcing; installation is blocked. Set it permissive and retry."
	default:
		flow.hosts = fmt.Sprintf("Detected %s/%s, firewall=%s — ready.", host.OS.ID, host.Arch, firewallName(host.Firewall))
	}
	return flow
}

func firewallName(f system.Firewall) string {
	if f == system.FirewallNone {
		return "none"
	}
	return string(f)
}

// canProceed reports whether preflight passed.
func (f *installFlow) canProceed() bool {
	return f.host.IsRoot && f.host.Supported() && !f.host.SELinux
}

func (f *installFlow) setSize(width, height int) {
	f.form.setSize(width, height)
	f.run.setSize(width, height)
}

func (f *installForm) setSize(width, height int) {
	f.width = width
	f.height = height
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

func (f *installForm) startForm() {
	f.parameterForm.validate = f.validateField
	f.parameterForm.startForm()
}

// commitField stores the current field value (or its default) and advances.
func (f *installForm) commitField() bool {
	f.parameterForm.validate = f.validateField
	done := f.parameterForm.commitField()
	if done {
		f.confirmScroll = 0
	}
	return done
}

func (f *installForm) validateField(field field, val string, _ map[string]string) error {
	switch field.key {
	case "domain":
		if val == "" {
			return fmt.Errorf("domain is required")
		}
		if f.validateDomain == nil {
			return nil
		}
		return f.validateDomain(context.Background(), val)
	case "protocols":
		if len(protocolsFromValue(val)) == 0 {
			return fmt.Errorf("select at least one protocol")
		}
	}
	if err := uiparams.ValidateSharedParameterValue(field.key, val); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(field.key, "traffic_") && strings.HasSuffix(field.key, "_limit_gb"):
		if _, err := strconv.ParseUint(val, 10, 64); err != nil {
			return fmt.Errorf("traffic limit must be a non-negative integer")
		}
	case field.key == "reset_day":
		day, err := strconv.Atoi(val)
		if err != nil || day < 1 || day > 28 {
			return fmt.Errorf("reset day must be between 1 and 28")
		}
	}
	return nil
}

// Update handles install flow messages. The bool return reports whether the flow is
// finished and the caller should return to the menu.
func (f *installFlow) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.setSize(msg.Width, msg.Height)
	case runMsg:
		return f.handleRun(msg), false
	case tea.KeyMsg:
		return f.handleKey(msg)
	case tea.MouseMsg:
		return f.handleMouse(msg), false
	}
	if f.phase == phaseForm && !f.form.currentFieldHasOptions() {
		return f.form.updateInput(msg), false
	}
	return nil, false
}

func (f *installFlow) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch f.phase {
	case phasePreflight:
		switch msg.String() {
		case "enter":
			if f.canProceed() {
				f.phase = phaseForm
				f.form.startForm()
			}
		case "esc", "q":
			return nil, true
		}
	case phaseForm:
		switch msg.String() {
		case "enter":
			if f.form.commitField() {
				f.phase = phaseConfirm
			}
		case " ", "space":
			if f.form.currentFieldIsMulti() {
				f.form.toggleOption()
				break
			}
			return f.form.updateInput(msg), false
		case "up", "k", "left", "h":
			if f.form.currentFieldHasOptions() {
				f.form.moveOption(-1)
				break
			}
			return f.form.updateInput(msg), false
		case "down", "j", "right", "l":
			if f.form.currentFieldHasOptions() {
				f.form.moveOption(1)
				break
			}
			return f.form.updateInput(msg), false
		case "shift+tab", "ctrl+b":
			f.form.previousField()
		case "esc":
			return nil, true
		default:
			if f.form.currentFieldHasOptions() {
				return nil, false
			}
			return f.form.updateInput(msg), false
		}
	case phaseConfirm:
		switch msg.String() {
		case "up", "k":
			f.form.scrollConfirm(-1, f.form.confirmViewportHeight(), f.host)
			return nil, false
		case "down", "j":
			f.form.scrollConfirm(1, f.form.confirmViewportHeight(), f.host)
			return nil, false
		case "pgup":
			f.form.scrollConfirm(-f.form.confirmViewportHeight(), f.form.confirmViewportHeight(), f.host)
			return nil, false
		case "pgdown":
			f.form.scrollConfirm(f.form.confirmViewportHeight(), f.form.confirmViewportHeight(), f.host)
			return nil, false
		case "home":
			f.form.confirmScroll = 0
			return nil, false
		case "end":
			f.form.confirmScroll = f.form.maxConfirmScroll(f.form.confirmViewportHeight(), f.host)
			return nil, false
		case "enter", "y":
			return f.startRun(), false
		case "shift+tab", "ctrl+b":
			f.phase = phaseForm
			f.form.backToLastField()
		case "esc", "n":
			return nil, true
		}
	case phaseRunning:
		switch msg.String() {
		case "enter":
			if f.run.runComplete {
				f.phase = phaseDone
			}
		case "up", "k":
			f.run.scrollLog(1, f.run.logViewportHeight())
			return nil, false
		case "down", "j":
			f.run.scrollLog(-1, f.run.logViewportHeight())
			return nil, false
		case "pgup":
			f.run.scrollLog(f.run.logViewportHeight(), f.run.logViewportHeight())
			return nil, false
		case "pgdown":
			f.run.scrollLog(-f.run.logViewportHeight(), f.run.logViewportHeight())
			return nil, false
		case "home":
			f.run.logScroll = f.run.maxLogScroll(f.run.logViewportHeight())
			return nil, false
		case "end":
			f.run.logScroll = 0
			return nil, false
		}
	case phaseDone:
		if f.run.runErr != nil {
			switch msg.String() {
			case "up", "k":
				f.run.scrollLog(1, f.run.doneLogHeight())
				return nil, false
			case "down", "j":
				f.run.scrollLog(-1, f.run.doneLogHeight())
				return nil, false
			case "pgup":
				f.run.scrollLog(f.run.doneLogHeight(), f.run.doneLogHeight())
				return nil, false
			case "pgdown":
				f.run.scrollLog(-f.run.doneLogHeight(), f.run.doneLogHeight())
				return nil, false
			case "home":
				f.run.logScroll = f.run.maxLogScroll(f.run.doneLogHeight())
				return nil, false
			case "end":
				f.run.logScroll = 0
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (f *installFlow) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		switch f.phase {
		case phaseConfirm:
			f.form.scrollConfirm(-3, f.form.confirmViewportHeight(), f.host)
		case phaseRunning:
			f.run.scrollLog(3, f.run.logViewportHeight())
		case phaseDone:
			if f.run.runErr != nil {
				f.run.scrollLog(3, f.run.doneLogHeight())
			}
		}
	case tea.MouseButtonWheelDown:
		switch f.phase {
		case phaseConfirm:
			f.form.scrollConfirm(3, f.form.confirmViewportHeight(), f.host)
		case phaseRunning:
			f.run.scrollLog(-3, f.run.logViewportHeight())
		case phaseDone:
			if f.run.runErr != nil {
				f.run.scrollLog(-3, f.run.doneLogHeight())
			}
		}
	}
	return nil
}

func (w *installFlow) handleRun(msg runMsg) tea.Cmd {
	return handleCommandRun(w, msg)
}

func (w *installFlow) runState() *commandRun {
	return &w.run
}

func (w *installFlow) markRunFailed() {
	w.phase = phaseDone
}

// startRun builds the config and launches the orchestrator goroutine.
func (w *installFlow) startRun() tea.Cmd {
	cfg, err := w.buildConfig()
	if err != nil {
		w.run.runErr = err
		w.phase = phaseDone
		return nil
	}
	w.cfg = cfg
	w.phase = phaseRunning
	w.run.resetRun(make(chan runMsg, 64))

	ch := w.run.ch
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
	return w.run.waitForRun()
}

// buildConfig assembles install.Config from the collected values and host.
func (w *installFlow) buildConfig() (install.Config, error) {
	creds, err := install.GenerateCredentials()
	if err != nil {
		return install.Config{}, err
	}
	vals := w.form.values
	w.form.applyCredentialOverrides(&creds)
	enabled := protocolsFromValue(vals["protocols"])
	if len(enabled) == 0 {
		enabled = config.AllProtocols
	}
	deployMonitor := trafficMonitorEnabled(vals)
	subscribePort, err := parseInstallPort(vals["subscribe_port"], install.DefaultSubscribePort, "subscription port")
	if err != nil {
		return install.Config{}, err
	}
	trafficPort, err := parseInstallPort(vals["traffic_port"], install.DefaultTrafficPort, "traffic monitor public port")
	if err != nil {
		return install.Config{}, err
	}
	monitorPort, err := parseInstallPort(vals["monitor_port"], install.DefaultMonitorPort, "traffic monitor port")
	if err != nil {
		return install.Config{}, err
	}
	ports, err := w.form.protocolPorts(enabled, subscribePort, trafficPort, monitorPort, deployMonitor)
	if err != nil {
		return install.Config{}, err
	}
	hysteria2UpMbps := config.DefaultHysteria2UpMbps
	hysteria2DownMbps := config.DefaultHysteria2DownMbps
	if hasProtocol(enabled, config.ProtocolHysteria2) {
		hysteria2UpMbps, err = parseHysteria2Mbps(vals["hysteria2_up_mbps"], config.DefaultHysteria2UpMbps, "hysteria2 up limit")
		if err != nil {
			return install.Config{}, err
		}
		hysteria2DownMbps, err = parseHysteria2Mbps(vals["hysteria2_down_mbps"], config.DefaultHysteria2DownMbps, "hysteria2 down limit")
		if err != nil {
			return install.Config{}, err
		}
	}
	salt := strings.TrimSpace(vals["subscribe_salt"])
	if salt == "" {
		salt, err = credentials.Salt()
		if err != nil {
			return install.Config{}, err
		}
	}
	inLimitBytes := parseTrafficLimitGB(vals["traffic_in_limit_gb"])
	outLimitBytes := parseTrafficLimitGB(vals["traffic_out_limit_gb"])
	totalLimitBytes := parseTrafficLimitGB(vals["traffic_total_limit_gb"])
	if !deployMonitor {
		inLimitBytes = 0
		outLimitBytes = 0
		totalLimitBytes = 0
	}
	resetDay, _ := strconv.Atoi(vals["reset_day"])
	if !deployMonitor || resetDay < 1 || resetDay > 28 {
		resetDay = install.DefaultResetDay
	}

	challenge := acme.Challenge(vals["challenge"])
	dnsCreds := map[string]string{}
	if challenge == acme.ChallengeDNS01 {
		switch vals["dns_provider"] {
		case "cloudflare":
			dnsCreds["CF_API_TOKEN"] = vals["dns_credential"]
		case "aliyun":
			if key, secret, ok := strings.Cut(vals["dns_credential"], ":"); ok {
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
		realityServerName, err = uiparams.NormalizeRealityServerName(vals["reality_sni"])
		if err != nil {
			return install.Config{}, err
		}
	}

	return install.Config{
		Domain:                 vals["domain"],
		Email:                  vals["email"],
		Challenge:              challenge,
		DNSProvider:            vals["dns_provider"],
		DNSCredentials:         dnsCreds,
		Ports:                  ports,
		Enabled:                enabled,
		DisplayName:            vals["display_name"],
		Salt:                   salt,
		RealityServerName:      realityServerName,
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		Hysteria2UpMbps:        hysteria2UpMbps,
		Hysteria2DownMbps:      hysteria2DownMbps,
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

func parseHysteria2Mbps(value string, fallback int, label string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	mbps, err := strconv.Atoi(value)
	if err != nil || mbps <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer Mbps value", label)
	}
	return mbps, nil
}

func (w *installForm) applyCredentialOverrides(creds *install.Credentials) {
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

func (w *installForm) protocolPorts(enabled []config.Protocol, subscribePort, trafficPort, monitorPort int, deployMonitor bool) (config.Ports, error) {
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

func (w *installForm) portForProtocol(proto config.Protocol, used map[int]bool) (int, error) {
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
	flowTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	flowOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	flowErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	flowRandom = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

// View renders the install flow.
func (w *installFlow) View() string {
	switch w.phase {
	case phasePreflight:
		body := w.hosts + "\n\n"
		if w.canProceed() {
			body += dimStyle.Render("enter to begin · esc to cancel")
		} else {
			body += flowErr.Render("Cannot proceed. ") + dimStyle.Render("esc to go back")
		}
		return flowTitle.Render("Install · Preflight") + "\n\n" + body
	case phaseForm:
		return w.form.View()
	case phaseConfirm:
		return w.form.confirmView(w.host)
	case phaseRunning:
		return w.runningView()
	case phaseDone:
		if w.run.runErr != nil {
			return w.failedView()
		}
		return flowOK.Render("Install complete") + "\n\n" + w.doneSummary() + "\n\n" +
			dimStyle.Render("press any key to return")
	}
	return ""
}

func (w *installForm) View() string {
	return w.parameterForm.View("Install · Configuration")
}

func (w *installFlow) footerHints() []string {
	switch w.phase {
	case phasePreflight:
		return []string{"enter continue", "esc/q cancel"}
	case phaseForm:
		return []string{"enter continue", "shift+tab back", "esc cancel"}
	case phaseConfirm:
		return []string{"↑/↓ scroll", "enter install", "esc cancel"}
	case phaseRunning:
		if w.run.runComplete {
			return []string{"enter summary", "↑/↓ scroll log"}
		}
		return []string{"↑/↓ scroll log"}
	case phaseDone:
		if w.run.runErr != nil {
			return []string{"↑/↓ scroll log", "any other key return"}
		}
		return []string{"any key return"}
	default:
		return nil
	}
}

func (w *installForm) confirmView(host system.Host) string {
	viewportHeight := w.confirmViewportHeight()
	lines := w.visibleConfirmLines(viewportHeight, host)
	return flowTitle.Render("Install · Confirm") + "\n\n" + strings.Join(lines, "\n") + "\n\n" +
		dimStyle.Render("↑/↓ or mouse wheel scroll · enter/y to install · shift+tab/ctrl+b back · esc/n to cancel")
}

func (w *installForm) visibleConfirmLines(height int, host system.Host) []string {
	rows := w.confirmRows(host)
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	w.clampConfirmScroll(height, host)
	start := min(w.confirmScroll, max(0, len(rows)-height))
	end := min(start+height, len(rows))
	return rows[start:end]
}

func (w *installForm) scrollConfirm(delta, height int, host system.Host) {
	w.confirmScroll += delta
	w.clampConfirmScroll(height, host)
}

func (w *installForm) clampConfirmScroll(height int, host system.Host) {
	w.confirmScroll = min(max(0, w.confirmScroll), w.maxConfirmScroll(height, host))
}

func (w *installForm) maxConfirmScroll(height int, host system.Host) int {
	if height <= 0 {
		return 0
	}
	return max(0, len(w.confirmRows(host))-height)
}

func (w *installForm) confirmRows(host system.Host) []string {
	summary := strings.TrimRight(w.summary(host), "\n")
	if summary == "" {
		return nil
	}
	wrapped := lipgloss.NewStyle().Width(w.confirmWrapWidth()).Render(summary)
	return strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
}

func (w *installForm) confirmViewportHeight() int {
	if w.height <= 0 {
		return 12
	}
	return max(1, w.height-4)
}

func (w *installForm) confirmWrapWidth() int {
	if w.width <= 0 {
		return 80
	}
	return max(1, w.width)
}

func (w *installFlow) runningView() string {
	return commandRunningView(w, "Install · Running")
}

func (w *installFlow) failedView() string {
	return commandFailedView(w, "Install failed")
}

func (w *installForm) summary(host system.Host) string {
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
		summaryRow("Subscription port", or(w.values["subscribe_port"], strconv.Itoa(install.DefaultSubscribePort))),
		summaryRow("Subscription salt", summaryValueOrRandom(w.values["subscribe_salt"])),
		summaryRow("Traffic monitor", yesNoString(deployMonitor)),
		summaryRow("Operating system / architecture", host.OS.ID+" / "+host.Arch),
		summaryRow("Firewall", firewallName(host.Firewall)),
	}
	if deployMonitor {
		rows = append(rows,
			summaryRow("Traffic monitor public port", or(w.values["traffic_port"], strconv.Itoa(install.DefaultTrafficPort))),
			summaryRow("Traffic monitor local port", or(w.values["monitor_port"], strconv.Itoa(install.DefaultMonitorPort))),
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

func (w *installFlow) doneSummary() string {
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
// runs on the orchestrator goroutine, so it must not touch UI state directly.
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
