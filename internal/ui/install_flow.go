package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
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
	phaseMissingDNSCreds
	phaseConfirm
	phaseRunning
	phaseDone
)

// installFields defines the install form's input sequence.
func installFields() []field {
	missingProtocol := func(p config.Protocol) func(map[string]string) bool {
		return func(v map[string]string) bool { return !protocolSelected(v, p) }
	}
	noReality := func(v map[string]string) bool {
		return !protocolSelected(v, config.ProtocolRealityVision) && !protocolSelected(v, config.ProtocolRealityGRPC)
	}
	monitorDisabled := func(v map[string]string) bool { return !monitorEnabled(v) }
	fields := []field{
		{key: "domain", label: "Domain (must resolve to this server)", note: "Used for certificate issuance, Nginx server_name, subscription URLs, and TLS SNI."},
		{key: "protocols", label: "Protocols to install", def: defaultProtocolValue(), options: protocolOptions(), multi: true, note: "Select one or more protocols. At least one protocol must remain selected."},
		{key: "site_template", label: "Masquerade site template", def: deploy.DefaultSiteTemplate, options: deploy.SiteTemplateOptions(), note: "HML5 UP template deployed to /etc/singbox-deploy/www."},
	}
	fields = append(fields, installProtocolParameterFields(missingProtocol, noReality)...)
	fields = append(fields, fieldsFromParameters(uiparams.SubscriptionInstallFields())...)
	fields = append(fields, fieldsFromParameters(uiparams.MonitorInstallFields(monitorDisabled))...)
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

// runMsg carries an orchestrator progress event, a streamed log line, or
// completion into the UI. It is the only channel the orchestrator goroutine
// uses to communicate, so all UI state stays mutated on the UI goroutine.
type runMsg struct {
	event     *deploy.Event
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

	form      installForm
	run       commandRun
	cfg       deploy.Config
	dnsStore  cluster.DNSStore
	subForm   *dnsCredentialForm
	statusErr string // optional banner shown above the form after a cancelled sub-form
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
		phase:    phasePreflight,
		form:     newInstallForm(),
		run:      newCommandRun(),
		dnsStore: cluster.NewRegistry(paths.DefaultLayout()).DNS(),
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

func monitorEnabled(vals map[string]string) bool {
	value := vals["monitor"]
	if value == "" {
		value = vals["traffic_monitor"]
	}
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
	case "site_template":
		_, err := deploy.NormalizeSiteTemplate(val)
		return err
	}
	if err := uiparams.ValidateSharedParameterValue(field.key, val); err != nil {
		return err
	}
	if err := uiparams.ValidateMonitorParameterValue(field.key, val); err != nil {
		return err
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
	if f.phase == phaseMissingDNSCreds && f.subForm != nil {
		cmd := f.subForm.Update(msg)
		f.advanceSubFormState()
		return cmd, false
	}
	if f.phase == phaseForm && !f.form.currentFieldHasOptions() {
		return f.form.updateInput(msg), false
	}
	return nil, false
}

// enterMissingDNSCreds switches to the inline DNS credential sub-form for
// domain. headerErr, when non-empty, is shown above the form to explain why
// the previous attempt (if any) did not succeed.
func (f *installFlow) enterMissingDNSCreds(domain, headerErr string) {
	f.phase = phaseMissingDNSCreds
	f.subForm = newDNSCredentialForm(domain, f.dnsStore)
	if headerErr != "" {
		f.subForm.SetHeaderError(headerErr)
	}
	f.subForm.setSize(f.form.width, f.form.height)
}

func (f *installFlow) handleMissingDNSKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if f.subForm == nil {
		f.phase = phaseForm
		return nil, false
	}
	cmd := f.subForm.Update(msg)
	f.advanceSubFormState()
	return cmd, false
}

// advanceSubFormState consumes a saved/cancelled signal from the sub-form and
// either resumes the install (when the saved root now covers the install
// domain) or returns the user to the form with a banner.
func (f *installFlow) advanceSubFormState() {
	if f.subForm == nil {
		return
	}
	saved, cancelled, _ := f.subForm.State()
	switch {
	case saved:
		domain := f.form.values["domain"]
		if _, err := f.dnsStore.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
			f.enterMissingDNSCreds(domain, fmt.Sprintf("Saved credentials do not cover %s — adjust the root domain.", domain))
			return
		}
		f.subForm = nil
		f.phase = phaseForm
	case cancelled:
		f.subForm = nil
		f.phase = phaseForm
		f.statusErr = "DNS credentials are still required. Configure them before continuing."
		f.form.backToFieldKey("domain")
	}
}

// ensureDNSCredentials runs the lookup one last time at Complete. Returns true
// when the lookup succeeds and the flow can advance to confirm; false when it
// transitioned to the missing-creds sub-form.
func (f *installFlow) ensureDNSCredentials() bool {
	domain := f.form.values["domain"]
	if domain == "" {
		return true
	}
	if _, err := f.dnsStore.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
		f.enterMissingDNSCreds(domain, "")
		return false
	}
	return true
}

func (f *installFlow) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch f.phase {
	case phasePreflight:
		switch {
		case isSelectionConfirmKey(msg):
			if f.canProceed() {
				f.phase = phaseForm
				f.form.startForm()
			}
		case isSelectionCancelKey(msg):
			return nil, true
		}
	case phaseForm:
		prevKey := f.form.currentFieldKey()
		cmd, done, handled := f.form.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				if !f.ensureDNSCredentials() {
					return
				}
				f.phase = phaseConfirm
			},
			Back: func() { f.form.previousField() },
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
		})
		if handled {
			if prevKey == "domain" && f.form.currentFieldKey() != "domain" && f.form.fieldErr == "" {
				if domain := f.form.values["domain"]; domain != "" {
					if _, err := f.dnsStore.FindForDomain(domain); errors.Is(err, os.ErrNotExist) {
						f.enterMissingDNSCreds(domain, "")
					}
				}
			}
			return cmd, done
		}
	case phaseMissingDNSCreds:
		return f.handleMissingDNSKey(msg)
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

	dnsStore := cluster.NewRegistry(layout).DNS()
	orch := &deploy.Orchestrator{
		Runner:    runner,
		Layout:    layout,
		ACME:      acmeManager,
		Releases:  releases,
		GOOS:      "linux",
		GOARCH:    w.host.Arch,
		DNSLookup: dnsLookupAdapter(dnsStore),
	}
	orch.Progress = func(e deploy.Event) {
		ev := e
		ch <- runMsg{event: &ev}
	}
	go func() {
		err := orch.Run(context.Background(), cfg)
		ch <- runMsg{done: true, err: err}
	}()
	return w.run.waitForRun()
}

// buildConfig assembles deploy.Config from the collected values and host.
func (w *installFlow) buildConfig() (deploy.Config, error) {
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		return deploy.Config{}, err
	}
	vals := w.form.values
	w.form.applyCredentialOverrides(&creds)
	enabled := protocolsFromValue(vals["protocols"])
	if len(enabled) == 0 {
		enabled = config.AllProtocols
	}
	deployMonitor := monitorEnabled(vals)
	siteTemplate, err := deploy.NormalizeSiteTemplate(vals["site_template"])
	if err != nil {
		return deploy.Config{}, err
	}
	subscribePort, err := parseInstallPort(vals["subscribe_port"], deploy.DefaultSubscribePort, "subscription port")
	if err != nil {
		return deploy.Config{}, err
	}
	monitorPublicPort, err := parseInstallPort(vals["monitor_public_port"], deploy.DefaultMonitorPublicPort, "monitor public port")
	if err != nil {
		return deploy.Config{}, err
	}
	monitorPort, err := parseInstallPort(vals["monitor_port"], deploy.DefaultMonitorPort, "monitor service port")
	if err != nil {
		return deploy.Config{}, err
	}
	ports, err := w.form.protocolPorts(enabled, subscribePort, monitorPublicPort, monitorPort, deployMonitor)
	if err != nil {
		return deploy.Config{}, err
	}
	salt := strings.TrimSpace(vals["subscribe_salt"])
	if salt == "" {
		salt, err = credentials.Salt()
		if err != nil {
			return deploy.Config{}, err
		}
	}
	inLimitBytes, _ := uiparams.ParseTrafficSize(vals["traffic_in_limit"])
	outLimitBytes, _ := uiparams.ParseTrafficSize(vals["traffic_out_limit"])
	totalLimitBytes, _ := uiparams.ParseTrafficSize(vals["traffic_total_limit"])
	if !deployMonitor {
		inLimitBytes = 0
		outLimitBytes = 0
		totalLimitBytes = 0
	}
	resetDay, _ := strconv.Atoi(vals["reset_day"])
	if !deployMonitor || resetDay < 1 || resetDay > 28 {
		resetDay = deploy.DefaultResetDay
	}
	resetHour, _ := strconv.Atoi(vals["reset_hour"])
	if !deployMonitor || resetHour < 0 || resetHour > 23 {
		resetHour = deploy.DefaultResetHour
	}
	monitorInterval, _ := strconv.Atoi(vals["monitor_interval_seconds"])
	if !deployMonitor || monitorInterval < 10 {
		monitorInterval = deploy.DefaultMonitorIntervalSeconds
	}
	monitorAlias := strings.TrimSpace(vals["monitor_alias"])
	if monitorAlias == "" {
		monitorAlias = deploy.DefaultMonitorAlias
	}

	iface := ""
	if deployMonitor {
		iface, _ = monitor.DefaultInterface()
	}
	realityServerName := ""
	if hasProtocol(enabled, config.ProtocolRealityVision) || hasProtocol(enabled, config.ProtocolRealityGRPC) {
		realityServerName, err = uiparams.NormalizeRealityServerName(vals["reality_sni"])
		if err != nil {
			return deploy.Config{}, err
		}
	}

	return deploy.Config{
		Domain:                 vals["domain"],
		Ports:                  ports,
		Enabled:                enabled,
		DisplayName:            vals["display_name"],
		Salt:                   salt,
		SiteTemplate:           siteTemplate,
		RealityServerName:      realityServerName,
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		SubscribePort:          subscribePort,
		MonitorPublicPort:      monitorPublicPort,
		MonitorPort:            monitorPort,
		DeployMonitor:          deployMonitor,
		MonitorAlias:           monitorAlias,
		TrafficInLimitBytes:    inLimitBytes,
		TrafficOutLimitBytes:   outLimitBytes,
		TrafficTotalLimitBytes: totalLimitBytes,
		ResetDay:               resetDay,
		ResetHour:              resetHour,
		MonitorInterface:       iface,
		MonitorIntervalSeconds: monitorInterval,
		OS:                     w.host.OS,
		Firewall:               w.host.Firewall,
		Creds:                  creds,
	}, nil
}

// dnsLookupAdapter wraps a cluster.DNSStore so the deploy orchestrator can
// look up credentials without importing cluster (which would be a cycle).
func dnsLookupAdapter(store cluster.DNSStore) deploy.DNSCredentialLookup {
	return func(host string) (string, map[string]string, error) {
		creds, err := store.FindForDomain(host)
		if err != nil {
			return "", nil, err
		}
		return creds.Provider, creds.EnvMap(), nil
	}
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

func (w *installForm) applyCredentialOverrides(creds *deploy.Credentials) {
	creds.ApplyOverrides(deploy.Credentials{
		RealityVisionUUID: strings.TrimSpace(w.values["reality_vision_uuid"]),
		RealityGRPCUUID:   strings.TrimSpace(w.values["reality_grpc_uuid"]),
		HysteriaPassword:  strings.TrimSpace(w.values["hysteria2_password"]),
		TUICUUID:          strings.TrimSpace(w.values["tuic_uuid"]),
		TUICPassword:      strings.TrimSpace(w.values["tuic_password"]),
		AnyTLSPassword:    strings.TrimSpace(w.values["anytls_password"]),
	})
}

func (w *installForm) protocolPorts(enabled []config.Protocol, subscribePort, monitorPublicPort, monitorPort int, deployMonitor bool) (config.Ports, error) {
	used := map[int]bool{80: true, subscribePort: true}
	if deployMonitor {
		if used[monitorPublicPort] {
			return config.Ports{}, fmt.Errorf("monitor public port %d conflicts with another required port", monitorPublicPort)
		}
		used[monitorPublicPort] = true
		if used[monitorPort] {
			return config.Ports{}, fmt.Errorf("monitor service port %d conflicts with another required port", monitorPort)
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
		return config.RandomProtocolPort(used)
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s port must be between 1 and 65535", proto)
	}
	if err := config.ValidateProtocolPort(port, used); err != nil {
		return 0, fmt.Errorf("%s %w", proto, err)
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
		body := w.hosts
		if !w.canProceed() {
			body += "\n\n" + flowErr.Render("Cannot proceed.")
		}
		return flowTitle.Render("Install · Preflight") + "\n\n" + body
	case phaseForm:
		view := w.form.View()
		if w.statusErr != "" {
			view = flowErr.Render(w.statusErr) + "\n\n" + view
		}
		return view
	case phaseMissingDNSCreds:
		if w.subForm != nil {
			return w.subForm.View()
		}
		return w.form.View()
	case phaseConfirm:
		return w.form.confirmView(w.host)
	case phaseRunning:
		return w.runningView()
	case phaseDone:
		if w.run.runErr != nil {
			return w.failedView()
		}
		return flowOK.Render("Install complete") + "\n\n" + w.doneSummary()
	}
	return ""
}

func (w *installForm) View() string {
	return w.parameterForm.View("Install · Configuration")
}

func (w *installFlow) footerHints() []operationHint {
	switch w.phase {
	case phasePreflight:
		if w.canProceed() {
			return []operationHint{hint(keyEnter, "Begin"), hint(keyCancel, "Cancel")}
		}
		return []operationHint{hint(keyCancel, "Cancel")}
	case phaseForm:
		return w.form.footerHints()
	case phaseMissingDNSCreds:
		if w.subForm != nil {
			return w.subForm.footerHints()
		}
		return w.form.footerHints()
	case phaseConfirm:
		return []operationHint{
			hint(keyMoveMouse, "Scroll"),
			hint(keyEnterYes, "Install"),
			hint(keyBack, "Back"),
			hint(keyConfirmNo, "Cancel"),
		}
	case phaseRunning:
		return runningFooterHints(w.run.runComplete)
	case phaseDone:
		return doneFooterHints(w.run.runErr != nil)
	default:
		return nil
	}
}

func (w *installForm) confirmView(host system.Host) string {
	viewportHeight := w.confirmViewportHeight()
	lines := w.visibleConfirmLines(viewportHeight, host)
	return flowTitle.Render("Install · Confirm") + "\n\n" + strings.Join(lines, "\n")
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
	deployMonitor := monitorEnabled(w.values)
	rows := []summaryLine{
		summaryRow("Domain", w.values["domain"]),
		summaryRow("DNS credentials", dnsCredentialSummary(w.values["domain"])),
		summaryRow("Protocols", protocolLabels(protocols)),
		summaryRow("Display name", w.values["display_name"]),
		summaryRow("Masquerade site", or(w.values["site_template"], deploy.DefaultSiteTemplate)),
		summaryRow("Subscription port", or(w.values["subscribe_port"], strconv.Itoa(deploy.DefaultSubscribePort))),
		summaryRow("Subscription salt", summaryValueOrRandom(w.values["subscribe_salt"])),
		summaryRow("Monitor", yesNoString(deployMonitor)),
	}
	rows = append(rows,
		summaryRow("Operating system / architecture", host.OS.ID+" / "+host.Arch),
		summaryRow("Firewall", firewallName(host.Firewall)),
	)
	if deployMonitor {
		rows = append(rows,
			summaryRow("Monitor alias", or(w.values["monitor_alias"], deploy.DefaultMonitorAlias)),
			summaryRow("Monitor public port", or(w.values["monitor_public_port"], strconv.Itoa(deploy.DefaultMonitorPublicPort))),
			summaryRow("Monitor local port", or(w.values["monitor_port"], strconv.Itoa(deploy.DefaultMonitorPort))),
			summaryRow("Sampling interval", or(w.values["monitor_interval_seconds"], strconv.Itoa(deploy.DefaultMonitorIntervalSeconds))+" seconds"),
			summaryRow("Inbound traffic limit", trafficLimitSummary(w.values["traffic_in_limit"])),
			summaryRow("Outbound traffic limit", trafficLimitSummary(w.values["traffic_out_limit"])),
			summaryRow("Total traffic limit", trafficLimitSummary(w.values["traffic_total_limit"])),
			summaryRow("Next reset", nextResetFromValues(w.values["reset_day"], w.values["reset_hour"])),
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

func nextResetFromValues(dayStr, hourStr string) string {
	day, _ := strconv.Atoi(strings.TrimSpace(dayStr))
	hour, _ := strconv.Atoi(strings.TrimSpace(hourStr))
	if day < 1 || day > 28 {
		day = deploy.DefaultResetDay
	}
	if hour < 0 || hour > 23 {
		hour = deploy.DefaultResetHour
	}
	return nextResetLabel(day, hour)
}

// dnsCredentialSummary resolves the DNS credentials for the given install
// domain at confirm-render time, returning a short "<root> (<provider>)"
// label or "(none configured)" when nothing matches.
func dnsCredentialSummary(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "(none configured)"
	}
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	creds, err := store.FindForDomain(domain)
	if err != nil {
		return "(none configured)"
	}
	return fmt.Sprintf("%s (%s)", creds.RootDomain, creds.Provider)
}

func trafficLimitSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return "unlimited"
	}
	return value
}

func summaryValueOrRandom(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "random"
	}
	return value
}

func (w *installFlow) doneSummary() string {
	token := deploy.SubscriptionToken(w.cfg.Salt)
	subscriptionBase := fmt.Sprintf("https://%s:%d", w.cfg.Domain, w.cfg.SubscribePort)
	rows := []summaryLine{
		summaryRow("Account", w.cfg.DisplayName),
		summaryRow("Protocols", protocolLabels(w.cfg.Enabled)),
		summaryRow("Masquerade site", or(w.cfg.SiteTemplate, deploy.DefaultSiteTemplate)),
		summaryRow("Ports", installedPortsSummary(w.cfg.Enabled, w.cfg.Ports)),
		summaryRow("Subscription", subscriptionBase+"/s/default/"+token),
		summaryRow("Clash", subscriptionBase+"/s/clashMetaProfiles/"+token),
		summaryRow("sing-box", subscriptionBase+"/s/singboxProfiles/"+token),
		summaryRow("Surge", subscriptionBase+"/s/surgeProfiles/"+token),
	}
	if w.cfg.DeployMonitor {
		monitorBase := fmt.Sprintf("https://%s:%d", w.cfg.Domain, w.cfg.MonitorPublicPort)
		rows = append(rows,
			summaryRow("Monitor UI", monitorBase+"/monitor/"),
			summaryRow("Monitor API", monitorBase+"/monitor/api/summary"),
		)
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
