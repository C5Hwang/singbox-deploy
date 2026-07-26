package ui

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

const (
	defaultRealityServerName = "www.microsoft.com"
)

// defaultSpokePorts are the fixed protocol listen ports assigned to a new spoke.
// They avoid 80/443 (Nginx) and are independent per host.
var defaultSpokePorts = nodes.Node{
	RealityVisionPort: 8443,
	RealityGRPCPort:   8444,
	Hysteria2Port:     9443,
	TUICPort:          10443,
	AnyTLSPort:        11443,
}

type nodePhase int

const (
	nodePhaseList nodePhase = iota
	nodePhaseForm
	nodePhaseHostKeyScan
	nodePhaseHostKeyConfirm
	nodePhaseDeletePick
	nodePhaseForceConfirm
	nodePhaseRunning
	nodePhaseDone
)

// nodeManager is the spoke-node management page: it lists registered spokes and
// adds or removes them. Adding is gated on the hub being installed (spokes join
// the hub's overlay) and on the node's domain being covered by a DNS credential
// (the hub issues the spoke's certificate). Add/remove run through hubctl and
// stream their progress.
type nodeManager struct {
	run  commandRun
	form parameterForm

	layout     paths.Layout
	phase      nodePhase
	list       []nodes.Node
	hubReady   bool
	actionCur  int
	pickCursor int
	result     string
	action     string // "add" or "remove" for the running phase label
	startCmd   tea.Cmd
	// certificateDomainRequest asks the root model to suspend node creation and
	// open Certificate management for this domain.
	certificateDomainRequest string

	// SSH authentication is held only while the operator verifies the server
	// key and while the provisioning goroutine runs. It is never persisted in
	// the spoke registry.
	pendingTarget   bootstrap.Target
	pendingRegistry nodes.Node
	hostKeyInfo     bootstrap.HostKeyInfo
	pendingRemove   nodes.Node
}

type nodeHostKeyScanMsg struct {
	info bootstrap.HostKeyInfo
	err  error
}

var scanSpokeHostKey = func(ctx context.Context, target bootstrap.Target) (bootstrap.HostKeyInfo, error) {
	return (&bootstrap.Bootstrapper{}).ScanHostKey(ctx, target)
}

var nodeActions = []string{"Add spoke node", "Remove spoke node", "Force detach unreachable spoke"}

func newNodeManager() *nodeManager {
	m := &nodeManager{
		run:    newCommandRun(),
		form:   newParameterForm(nil),
		layout: paths.DefaultLayout(),
		phase:  nodePhaseList,
	}
	m.reload()
	return m
}

func (m *nodeManager) reload() {
	m.hubReady = nodes.HubInstalled(m.layout)
	list, err := nodes.Load(m.layout)
	if err != nil {
		m.result = "load nodes failed: " + err.Error()
	}
	m.list = list
}

func (m *nodeManager) runState() *commandRun { return &m.run }
func (m *nodeManager) markRunFailed()        { m.phase = nodePhaseDone }

func (m *nodeManager) setSize(w, h int) {
	m.run.setSize(w, h)
	m.form.setSize(w, h)
}

func (m *nodeManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m.phase {
	case nodePhaseRunning:
		return m.updateRunning(msg)
	case nodePhaseDone:
		if _, ok := msg.(tea.KeyMsg); ok {
			m.reload()
			m.phase = nodePhaseList
		}
		return nil, false
	case nodePhaseForm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateForm(key)
		}
	case nodePhaseHostKeyScan:
		if scanned, ok := msg.(nodeHostKeyScanMsg); ok {
			if scanned.err != nil {
				m.result = "scan SSH host key failed: " + scanned.err.Error()
				m.clearPendingTarget()
				m.phase = nodePhaseList
				return nil, false
			}
			m.hostKeyInfo = scanned.info
			m.phase = nodePhaseHostKeyConfirm
		}
	case nodePhaseHostKeyConfirm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateHostKeyConfirm(key)
		}
	case nodePhaseList:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateList(key)
		}
	case nodePhaseDeletePick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updatePick(key)
		}
	case nodePhaseForceConfirm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateForceConfirm(key)
		}
	}
	return nil, false
}

func (m *nodeManager) updateRunning(msg tea.Msg) (tea.Cmd, bool) {
	if rm, ok := msg.(runMsg); ok {
		cmd := handleCommandRun(m, rm)
		if rm.done {
			m.phase = nodePhaseDone
			m.reload()
		}
		return cmd, false
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		m.run.handleScrollKey(key.String(), m.run.logViewportHeight())
	}
	return nil, false
}

func (m *nodeManager) updateList(key tea.KeyMsg) (tea.Cmd, bool) {
	_, done, _ := handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.actionCur = moveSelection(m.actionCur, len(nodeActions), d) },
		Confirm: func() (tea.Cmd, bool) {
			switch m.actionCur {
			case 0:
				if !m.hubReady {
					m.result = "install the hub before adding spoke nodes"
					return nil, false
				}
				m.beginForm()
			case 1:
				if len(m.list) == 0 {
					m.result = "no nodes to remove"
					return nil, false
				}
				m.action = ""
				m.pickCursor = 0
				m.phase = nodePhaseDeletePick
			case 2:
				if len(m.list) == 0 {
					m.result = "no nodes to detach"
					return nil, false
				}
				m.pickCursor = 0
				m.phase = nodePhaseDeletePick
				m.action = "Force detach"
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { return nil, true },
	})
	return nil, done
}

func (m *nodeManager) updatePick(key tea.KeyMsg) (tea.Cmd, bool) {
	m.startCmd = nil
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.pickCursor = moveSelection(m.pickCursor, len(m.list), d) },
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.pickCursor, len(m.list)); ok {
				if m.action == "Force detach" {
					m.pendingRemove = m.list[idx]
					m.phase = nodePhaseForceConfirm
				} else {
					m.startRemove(m.list[idx])
				}
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.action = ""; m.phase = nodePhaseList; return nil, false },
	})
	if m.startCmd != nil {
		return m.startCmd, false
	}
	return nil, false
}

func (m *nodeManager) updateForceConfirm(key tea.KeyMsg) (tea.Cmd, bool) {
	switch strings.ToLower(key.String()) {
	case "y":
		node := m.pendingRemove
		m.pendingRemove = nodes.Node{}
		m.startForceDetach(node)
		return m.startCmd, false
	case "n", "esc":
		m.pendingRemove = nodes.Node{}
		m.action = ""
		m.phase = nodePhaseList
	}
	return nil, false
}

func (m *nodeManager) updateForm(key tea.KeyMsg) (tea.Cmd, bool) {
	m.startCmd = nil
	cmd, _, _ := m.form.handleKey(key, parameterFormKeyHandlers{
		Complete: m.completeForm,
		Cancel:   func() (tea.Cmd, bool) { m.phase = nodePhaseList; return nil, false },
	})
	if domain := certificateRedirectDomain(m.form.validationErr); domain != "" {
		m.certificateDomainRequest = domain
	}
	if m.startCmd != nil {
		return m.startCmd, false
	}
	return cmd, false
}

func (m *nodeManager) beginForm() {
	isKey := func(v map[string]string) bool { return v["ssh_auth"] != "key" }
	isPass := func(v map[string]string) bool { return v["ssh_auth"] != "password" }
	missingProtocol := func(protocol config.Protocol) func(map[string]string) bool {
		return func(values map[string]string) bool { return !protocolSelected(values, protocol) }
	}
	noReality := func(values map[string]string) bool {
		return !protocolSelected(values, config.ProtocolRealityVision) &&
			!protocolSelected(values, config.ProtocolRealityGRPC)
	}
	monitorDisabled := func(values map[string]string) bool { return !monitorEnabled(values) }
	fields := []field{
		{key: "alias", label: "Node alias", note: "Shown in the aggregated subscription and monitor views."},
		{key: "ssh_host", label: "SSH host (public IP or hostname)"},
		{key: "ssh_port", label: "SSH port", def: "22"},
		{key: "ssh_user", label: "SSH user", def: "root"},
		{key: "ssh_auth", label: "SSH auth method", def: "password", options: []string{"password", "key"}},
		{key: "ssh_password", label: "SSH password", secret: true, skip: isPass},
		{key: "ssh_key_path", label: "SSH private key path", skip: isKey, note: "Path on this hub to the private key file used to authenticate."},
		{key: "ssh_key_passphrase", label: "SSH private key passphrase (optional)", secret: true, skip: isKey},
		{key: "domain", label: "Node domain", note: "The spoke's proxy domain. Must be covered by a DNS credential in Certificate management."},
		{key: "protocols", label: "Protocols to install", def: defaultProtocolValue(), options: protocolOptions(), multi: true, note: "Credentials are generated on the spoke and retained across later edits."},
	}
	realitySNI := fieldFromParameter(uiparams.RealitySNIField())
	realitySNI.skip = noReality
	realitySNI.badgeFunc = protocolParameterBadge(config.ProtocolRealityVision, config.ProtocolRealityGRPC)
	fields = append(fields, realitySNI)
	for _, protocol := range config.AllProtocols {
		for _, parameter := range uiparams.ProtocolInstallFieldsForProtocol(protocol) {
			if !strings.HasSuffix(parameter.Key, "_port") {
				continue // credentials are generated and kept only on the spoke
			}
			portField := fieldFromParameter(parameter)
			portField.def = strconv.Itoa(defaultSpokePort(protocol))
			portField.note = "Public listen port on this spoke. Credentials are generated locally on first install."
			portField.skip = missingProtocol(protocol)
			portField.badgeFunc = protocolParameterBadge(protocol)
			fields = append(fields, portField)
		}
	}
	fields = append(fields,
		field{key: "monitor", label: "Enable monitor on spoke", def: "yes", options: []string{"yes", "no"}, note: "Monitor data is available only to the hub through the authenticated Agent API over WireGuard."},
		field{key: "monitor_alias", label: "Spoke monitor alias (optional)", note: "Blank uses the node alias.", skip: monitorDisabled},
		field{key: "monitor_interface", label: "Monitored network interface", def: "auto", note: "Use auto to detect the spoke's default egress interface.", skip: monitorDisabled},
		field{key: "monitor_interval_seconds", label: "Sampling interval (seconds)", def: strconv.Itoa(deploy.DefaultMonitorIntervalSeconds), skip: monitorDisabled},
		field{key: "traffic_in_limit", label: "Inbound traffic limit", def: "0", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: monitorDisabled},
		field{key: "traffic_out_limit", label: "Outbound traffic limit", def: "0", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: monitorDisabled},
		field{key: "traffic_total_limit", label: "Total traffic limit", def: "0", note: uiparams.TrafficSizeNote("0 means unlimited."), skip: monitorDisabled},
		field{key: "reset_day", label: "Monthly reset day (1-28)", def: strconv.Itoa(deploy.DefaultResetDay), skip: monitorDisabled},
		field{key: "reset_hour", label: "Monthly reset hour GMT (0-23)", def: strconv.Itoa(deploy.DefaultResetHour), skip: monitorDisabled},
	)
	m.form.begin(fields, nil, m.validateForm)
	m.phase = nodePhaseForm
}

func (m *nodeManager) validateForm(f field, value string, values map[string]string) error {
	switch f.key {
	case "alias":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("alias is required")
		}
		// Reject a duplicate here rather than after SSH provisioning has already
		// run: the alias names this spoke's nodes in every aggregated output.
		if existing, clash := nodes.AliasConflict(m.list, value, ""); clash {
			return fmt.Errorf("alias is already used by %s", existing.EffectiveAlias())
		}
	case "ssh_host":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("SSH host is required")
		}
	case "ssh_port":
		if n, err := strconv.Atoi(value); err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("enter a valid port")
		}
	case "ssh_user":
		if strings.TrimSpace(value) != "root" {
			return fmt.Errorf("SSH bootstrap currently requires the root user")
		}
	case "domain":
		if !looksLikeDomain(value) {
			return fmt.Errorf("enter a valid domain")
		}
		return ensureDomainManaged(m.layout, value)
	case "protocols":
		if len(protocolsFromValue(value)) == 0 {
			return fmt.Errorf("select at least one protocol")
		}
	case "monitor_alias":
		if strings.TrimSpace(value) == "" {
			return nil // completeForm falls back to the node alias
		}
	}
	if err := uiparams.ValidateSharedParameterValue(f.key, value); err != nil {
		return err
	}
	if err := uiparams.ValidateMonitorParameterValue(f.key, value); err != nil {
		return err
	}
	portValues := values
	if monitorEnabled(values) && values["monitor_port"] == "" {
		portValues = make(map[string]string, len(values)+1)
		for key, current := range values {
			portValues[key] = current
		}
		portValues["monitor_port"] = strconv.Itoa(deploy.DefaultMonitorPort)
	}
	return validateInstallPortConflict(f.key, value, portValues)
}

func (m *nodeManager) completeForm() {
	vals := m.form.values
	auth := bootstrap.Auth{}
	if vals["ssh_auth"] == "key" {
		pem, err := os.ReadFile(strings.TrimSpace(vals["ssh_key_path"]))
		if err != nil {
			m.result = "read SSH key failed: " + err.Error()
			m.phase = nodePhaseList
			return
		}
		auth.PrivateKeyPEM = pem
		auth.Passphrase = vals["ssh_key_passphrase"]
	} else {
		auth.Password = vals["ssh_password"]
	}
	// Remove form-held references as soon as the ephemeral Target owns what is
	// needed for provisioning. The values are never written to the node store.
	vals["ssh_password"] = ""
	vals["ssh_key_path"] = ""
	vals["ssh_key_passphrase"] = ""
	port, _ := strconv.Atoi(vals["ssh_port"])
	target := bootstrap.Target{
		Host: strings.TrimSpace(vals["ssh_host"]),
		Port: port,
		User: strings.TrimSpace(vals["ssh_user"]),
		Auth: auth,
	}
	enabled := protocolsFromValue(vals["protocols"])
	if len(enabled) == 0 {
		enabled = config.AllProtocols
	}
	realityServerName := defaultRealityServerName
	if value := strings.TrimSpace(vals["reality_sni"]); value != "" {
		if normalized, err := uiparams.NormalizeRealityServerName(value); err == nil {
			realityServerName = normalized
		}
	}
	monitorOn := monitorEnabled(vals)
	monitorAlias := strings.TrimSpace(vals["monitor_alias"])
	if monitorAlias == "" {
		monitorAlias = strings.TrimSpace(vals["alias"])
	}
	monitorInterface := strings.TrimSpace(vals["monitor_interface"])
	if monitorInterface == "auto" {
		monitorInterface = ""
	}
	monitorInterval := intValueOr(vals["monitor_interval_seconds"], deploy.DefaultMonitorIntervalSeconds)
	inLimit, _ := uiparams.ParseTrafficSize(vals["traffic_in_limit"])
	outLimit, _ := uiparams.ParseTrafficSize(vals["traffic_out_limit"])
	totalLimit, _ := uiparams.ParseTrafficSize(vals["traffic_total_limit"])
	registry := nodes.Node{
		Alias:                  strings.TrimSpace(vals["alias"]),
		Domain:                 strings.TrimSpace(vals["domain"]),
		RealityServerName:      realityServerName,
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		EnabledProtocols:       protocolNames(enabled),
		RealityVisionPort:      intValueOr(vals["reality_vision_port"], defaultSpokePorts.RealityVisionPort),
		RealityGRPCPort:        intValueOr(vals["reality_grpc_port"], defaultSpokePorts.RealityGRPCPort),
		Hysteria2Port:          intValueOr(vals["hysteria2_port"], defaultSpokePorts.Hysteria2Port),
		TUICPort:               intValueOr(vals["tuic_port"], defaultSpokePorts.TUICPort),
		AnyTLSPort:             intValueOr(vals["anytls_port"], defaultSpokePorts.AnyTLSPort),
		Monitor:                monitorOn,
		MonitorAlias:           monitorAlias,
		MonitorInterface:       monitorInterface,
		MonitorPort:            deploy.DefaultMonitorPort,
		MonitorIntervalSeconds: monitorInterval,
		TrafficInLimitBytes:    inLimit,
		TrafficOutLimitBytes:   outLimit,
		TrafficTotalLimitBytes: totalLimit,
		ResetDay:               intValueOr(vals["reset_day"], deploy.DefaultResetDay),
		ResetHour:              intValueOr(vals["reset_hour"], deploy.DefaultResetHour),
	}
	m.pendingTarget = target
	m.pendingRegistry = registry
	m.hostKeyInfo = bootstrap.HostKeyInfo{}
	m.phase = nodePhaseHostKeyScan
	scanTarget := target
	scanTarget.Auth = bootstrap.Auth{} // scanning never receives credentials
	m.startCmd = func() tea.Msg {
		info, err := scanSpokeHostKey(context.Background(), scanTarget)
		return nodeHostKeyScanMsg{info: info, err: err}
	}
}

func defaultSpokePort(protocol config.Protocol) int {
	switch protocol {
	case config.ProtocolRealityVision:
		return defaultSpokePorts.RealityVisionPort
	case config.ProtocolRealityGRPC:
		return defaultSpokePorts.RealityGRPCPort
	case config.ProtocolHysteria2:
		return defaultSpokePorts.Hysteria2Port
	case config.ProtocolTUIC:
		return defaultSpokePorts.TUICPort
	case config.ProtocolAnyTLS:
		return defaultSpokePorts.AnyTLSPort
	default:
		return 0
	}
}

func intValueOr(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func (m *nodeManager) updateHostKeyConfirm(key tea.KeyMsg) (tea.Cmd, bool) {
	switch strings.ToLower(key.String()) {
	case "y":
		if m.hostKeyInfo.Fingerprint == "" {
			m.result = "SSH server did not present a host key fingerprint"
			m.clearPendingTarget()
			m.phase = nodePhaseList
			return nil, false
		}
		target := m.pendingTarget
		target.HostKeyFingerprint = m.hostKeyInfo.Fingerprint
		registry := m.pendingRegistry
		// Drop the manager's copy without wiping the key slice still needed by
		// startAdd. That goroutine wipes the last application-owned copy.
		m.pendingTarget = bootstrap.Target{}
		m.pendingRegistry = nodes.Node{}
		m.hostKeyInfo = bootstrap.HostKeyInfo{}
		m.startAdd(target, registry)
		return m.startCmd, false
	case "n", "esc":
		m.clearPendingTarget()
		m.phase = nodePhaseList
		m.result = "SSH host key was not trusted; node was not added"
	}
	return nil, false
}

func (m *nodeManager) clearPendingTarget() {
	wipeBootstrapAuth(&m.pendingTarget.Auth)
	m.pendingTarget = bootstrap.Target{}
	m.pendingRegistry = nodes.Node{}
	m.hostKeyInfo = bootstrap.HostKeyInfo{}
}

func wipeBootstrapAuth(auth *bootstrap.Auth) {
	for i := range auth.PrivateKeyPEM {
		auth.PrivateKeyPEM[i] = 0
	}
	*auth = bootstrap.Auth{}
}

func (m *nodeManager) startAdd(target bootstrap.Target, registry nodes.Node) {
	m.action = "Adding node"
	m.phase = nodePhaseRunning
	ch := make(chan runMsg, 64)
	m.run.resetRun(ch)
	logs := &logWriter{ch: ch}
	ctrl := &hubctl.Controller{
		Layout: m.layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion,
		Progress: runProgressSender(ch),
	}
	go func() {
		defer wipeBootstrapAuth(&target.Auth)
		_, err := ctrl.AddNode(context.Background(), hubctl.AddNodeParams{Node: target, Registry: registry}, logs)
		ch <- runMsg{done: true, err: err}
	}()
	m.startCmd = m.run.waitForRun()
}

func (m *nodeManager) startRemove(node nodes.Node) {
	m.action = "Removing node"
	m.phase = nodePhaseRunning
	ch := make(chan runMsg, 64)
	m.run.resetRun(ch)
	logs := &logWriter{ch: ch}
	ctrl := &hubctl.Controller{Layout: m.layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion}
	go func() {
		err := ctrl.RemoveNode(context.Background(), node, logs)
		ch <- runMsg{done: true, err: err}
	}()
	m.startCmd = m.run.waitForRun()
}

func (m *nodeManager) startForceDetach(node nodes.Node) {
	m.action = "Force detaching node"
	m.phase = nodePhaseRunning
	ch := make(chan runMsg, 64)
	m.run.resetRun(ch)
	logs := &logWriter{ch: ch}
	ctrl := &hubctl.Controller{Layout: m.layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion}
	go func() {
		err := ctrl.ForceDetachNode(context.Background(), node, logs)
		ch <- runMsg{done: true, err: err}
	}()
	m.startCmd = m.run.waitForRun()
}

func (m *nodeManager) View() string {
	switch m.phase {
	case nodePhaseRunning:
		return commandRunningView(m, m.action)
	case nodePhaseDone:
		if m.run.runErr != nil {
			return commandFailedView(m, m.action+" failed")
		}
		return flowTitle.Render(m.action+" complete") + "\n\n" + flowOK.Render("Press any key to return")
	case nodePhaseForm:
		return m.form.View("Add spoke node")
	case nodePhaseHostKeyScan:
		return flowTitle.Render("Verify SSH host key") + "\n\n" +
			dimStyle.Render("Scanning "+bootstrapTargetLabel(m.pendingTarget)+" without sending credentials…")
	case nodePhaseHostKeyConfirm:
		return m.hostKeyConfirmView()
	case nodePhaseDeletePick:
		return m.pickView()
	case nodePhaseForceConfirm:
		return m.forceConfirmView()
	default:
		return m.listView()
	}
}

func (m *nodeManager) forceConfirmView() string {
	return flowTitle.Render("Force detach unreachable spoke") + "\n\n" +
		statusWarn.Render("The Hub will remove this peer without contacting its Agent.") + "\n" +
		"Remote sing-box, Agent, and WireGuard files may remain active and require manual cleanup.\n\n" +
		"Spoke: " + nodeLabel(m.pendingRemove) + "\n\n" +
		"Press y to force detach, or n/Esc to cancel."
}

func (m *nodeManager) hostKeyConfirmView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Verify SSH host key") + "\n\n")
	b.WriteString("Server:      " + bootstrapTargetLabel(m.pendingTarget) + "\n")
	b.WriteString("Key type:    " + m.hostKeyInfo.Algorithm + "\n")
	b.WriteString("Fingerprint: " + m.hostKeyInfo.Fingerprint + "\n\n")
	b.WriteString(statusWarn.Render("Compare this SHA256 fingerprint with a trusted source.") + "\n\n")
	b.WriteString("Press y to trust this exact key and continue, or n/Esc to cancel.")
	return b.String()
}

func bootstrapTargetLabel(target bootstrap.Target) string {
	port := target.Port
	if port <= 0 {
		port = 22
	}
	host := strings.TrimSpace(target.Host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (m *nodeManager) listView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Spoke nodes") + "\n\n")
	if !m.hubReady {
		b.WriteString(statusWarn.Render("Install the hub first — spoke nodes join the hub's overlay.") + "\n\n")
	}
	if len(m.list) == 0 {
		b.WriteString(dimStyle.Render("No spoke nodes registered.") + "\n\n")
	} else {
		for _, n := range m.list {
			b.WriteString("  " + renderNodeRow(n) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(renderActionMenu(nodeActions, m.actionCur))
	if m.result != "" {
		b.WriteString("\n\n" + summaryInfo.Render(m.result))
	}
	return b.String()
}

func (m *nodeManager) pickView() string {
	var b strings.Builder
	title := "Remove spoke node"
	if m.action == "Force detach" {
		title = "Force detach unreachable spoke"
	}
	b.WriteString(flowTitle.Render(title) + "\n\n")
	for i, n := range m.list {
		label := nodeLabel(n)
		row := "  " + label
		if i == m.pickCursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (m *nodeManager) footerHints() []operationHint {
	switch m.phase {
	case nodePhaseRunning:
		return runningFooterHints(false)
	case nodePhaseDone:
		return doneFooterHints(m.run.runErr != nil)
	case nodePhaseForm:
		return m.form.footerHints()
	case nodePhaseHostKeyScan:
		return []operationHint{{key: "Wait", action: "Scanning host key"}}
	case nodePhaseHostKeyConfirm:
		return []operationHint{{key: "Y", action: "Trust exact key"}, {key: "N/Esc", action: "Cancel"}}
	case nodePhaseDeletePick:
		if m.action == "Force detach" {
			return actionFooterHints("Choose")
		}
		return actionFooterHints("Remove")
	case nodePhaseForceConfirm:
		return []operationHint{{key: "Y", action: "Force detach"}, {key: "N/Esc", action: "Cancel"}}
	default:
		return actionFooterHints("Select")
	}
}

func renderNodeRow(n nodes.Node) string {
	status := statusWarn.Render("provisioning")
	if n.Installed {
		switch {
		case n.AgentVersion == "":
			status = statusWarn.Render("installed · agent status unknown")
		case toolVersion != "" && n.AgentVersion != toolVersion:
			status = statusWarn.Render("agent " + n.AgentVersion + " · hub " + toolVersion)
		default:
			status = statusOK.Render("agent " + n.AgentVersion)
		}
	}
	if n.PendingCertificate {
		status += " · " + statusWarn.Render("certificate pending")
	}
	id := n.ID
	if len(id) > 8 {
		id = id[:8]
	}
	seen := "never seen"
	if !n.LastSeen.IsZero() {
		seen = "seen " + n.LastSeen.Local().Format("01-02 15:04")
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s",
		n.EffectiveAlias(),
		dimStyle.Render(n.Domain),
		dimStyle.Render(n.WGIP+" · "+id),
		dimStyle.Render(seen),
		status)
}

func nodeLabel(n nodes.Node) string {
	return fmt.Sprintf("%s (%s)", n.EffectiveAlias(), n.Domain)
}

func protocolNames(protocols []config.Protocol) []string {
	out := make([]string, len(protocols))
	for i, p := range protocols {
		out[i] = string(p)
	}
	return out
}
