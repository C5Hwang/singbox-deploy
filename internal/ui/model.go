package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LayoutMode selects between the side-by-side and single-column layouts.
type LayoutMode int

const (
	LayoutNarrow LayoutMode = iota
	LayoutWide
)

// wideThreshold is the minimum width for the side-by-side layout.
const wideThreshold = 100

const (
	defaultWidth  = 100
	defaultHeight = 30
	sidebarWidth  = 38
	panelGap      = 1
)

// Status is the snapshot rendered in the top status panel. Empty fields render
// as "unknown" so the panel is meaningful before installation.
type Status struct {
	ToolVersion  string
	Domain       string
	PublicIP     string
	OSArch       string
	SingBoxVer   string
	SingBoxState string
	NginxState   string
	MonitorState string
	CertState    string
	// MonitorCertState is set only when the monitor is published under its own
	// name and therefore carries a second managed certificate.
	MonitorCertState string
	Protocols        string
	MonitorUI        string
	// MonitorToken is the token the dashboard asks for. It is shown in full:
	// the operator has to be able to read it back to open the dashboard.
	MonitorToken string
	TrafficQuota string
	// Groups holds every published subscription group. They are rendered in
	// their own panel, one at a time, rather than in the status list: a fleet
	// with several groups has more subscription URLs than the panel can show.
	Groups []SubscriptionGroupStatus
}

// SubscriptionGroupStatus is one subscription group as shown in the
// subscription-groups panel.
type SubscriptionGroupStatus struct {
	Alias       string
	Salt        string
	Members     string
	MemberCount int
	// Published reports whether the hub serves this group's URLs. A group that
	// lost its last node keeps its registry entry and salt, but nothing is
	// written for its token, so the URL fields stay empty.
	Published    bool
	Subscription string
	ClashMetaSub string
	SingBoxSub   string
	SurgeSub     string
}

// MenuItem is a single selectable action within a group. Each item carries its
// own activation callback so the dispatch is driven by the item rather than by
// a hardcoded cursor index that must track defaultGroups' ordering.
type MenuItem struct {
	Label    string
	Activate func(*Model) tea.Cmd
}

// MenuGroup is a titled section of the grouped menu.
type MenuGroup struct {
	Title string
	Items []MenuItem
}

// Model is the root Bubble Tea model.
type Model struct {
	width  int
	height int
	status Status
	groups []MenuGroup
	cursor int // flat index across all items
	// groupIndex selects which subscription group the bottom-right panel
	// shows. It is clamped on every read so a group deleted behind the UI
	// cannot leave the panel pointing past the end of the list.
	groupIndex   int
	install      *installFlow
	protocols    *protocolManager
	relay        *relayManager
	subscribe    *subscriptionManager
	monitor      *monitorManager
	core         *coreManager
	certificates *certManager
	nodes        *nodeManager
	// A domain field can suspend its flow and redirect into Certificate
	// management. The exact form state is retained here and restored when the
	// certificate flow finishes or is cancelled.
	suspendedInstall *installFlow
	suspendedNodes   *nodeManager
	suspendedMonitor *monitorManager
	selfupdate       *selfUpdateManager
	uninstall        *uninstallManager
}

// NewModel returns a Model populated with the default grouped menu.
func NewModel() *Model {
	return &Model{groups: defaultGroups(), status: loadStatus()}
}

func defaultGroups() []MenuGroup {
	return []MenuGroup{
		{Title: "Deployment", Items: []MenuItem{
			{Label: "Setup", Activate: activateInstall},
			{Label: "Certificate management", Activate: activateCertificates},
		}},
		{Title: "Proxy", Items: []MenuItem{
			{Label: "Protocol settings", Activate: activateProtocols},
			{Label: "Relay", Activate: activateRelay},
		}},
		{Title: "Services", Items: []MenuItem{
			{Label: "Subscription settings", Activate: activateSubscriptions},
			{Label: "Monitoring", Activate: activateMonitor},
		}},
		{Title: "Spoke", Items: []MenuItem{
			{Label: "Spoke nodes", Activate: activateNodes},
		}},
		{Title: "System", Items: []MenuItem{
			{Label: "sing-box core", Activate: activateCore},
			{Label: "Self-update", Activate: activateSelfUpdate},
			{Label: "Uninstall", Activate: activateUninstall},
		}},
	}
}

func activateInstall(m *Model) tea.Cmd {
	flow := newInstallFlow()
	flow.setSize(m.width, m.height)
	m.install = flow
	return nil
}

func activateProtocols(m *Model) tea.Cmd {
	p := newProtocolManager()
	p.setSize(m.width, m.height)
	m.protocols = p
	return nil
}

func activateRelay(m *Model) tea.Cmd {
	r := newRelayManager()
	r.setSize(m.width, m.height)
	m.relay = r
	return nil
}

func activateSubscriptions(m *Model) tea.Cmd {
	s := newSubscriptionManager()
	s.setSize(m.width, m.height)
	m.subscribe = s
	return nil
}

func activateMonitor(m *Model) tea.Cmd {
	t := newMonitorManager()
	t.setSize(m.width, m.height)
	m.monitor = t
	return nil
}

func activateCore(m *Model) tea.Cmd {
	c := newCoreManager()
	c.setSize(m.width, m.height)
	m.core = c
	return nil
}

func activateCertificates(m *Model) tea.Cmd {
	c := newCertManager()
	c.setSize(m.width, m.height)
	m.certificates = c
	return nil
}

func activateNodes(m *Model) tea.Cmd {
	n := newNodeManager()
	n.setSize(m.width, m.height)
	m.nodes = n
	return nil
}

func activateSelfUpdate(m *Model) tea.Cmd {
	s := newSelfUpdateManager()
	s.setSize(m.width, m.height)
	m.selfupdate = s
	return s.checkCmd()
}

func activateUninstall(m *Model) tea.Cmd {
	u := newUninstallManager()
	u.setSize(m.width, m.height)
	m.uninstall = u
	return nil
}

// RefreshStatus reloads the status panel from the current host and state files.
func (m *Model) RefreshStatus() {
	m.status = loadStatus()
	m.groupIndex = clampGroupIndex(m.groupIndex, len(m.status.Groups))
}

func clampGroupIndex(index, count int) int {
	if count <= 0 || index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

// SetSize records the terminal dimensions.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// LayoutMode reports the active layout based on terminal width.
func (m *Model) LayoutMode() LayoutMode {
	width := m.width
	if width <= 0 {
		width = defaultWidth
	}
	if width < wideThreshold {
		return LayoutNarrow
	}
	return LayoutWide
}

// flatItems returns every menu item in display order.
func (m *Model) flatItems() []MenuItem {
	var items []MenuItem
	for _, g := range m.groups {
		items = append(items, g.Items...)
	}
	return items
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(sz.Width, sz.Height)
	}

	// Ctrl+C always quits, even while a sub-flow is active. Sub-flows swallow
	// every message they receive, so without this a user in any screen (or a
	// long-running install) would have no way to exit short of killing the
	// process externally.
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// While a sub-flow is active, delegate everything to it so its state machine
	// and async run messages are handled in one place.
	if m.install != nil {
		flow := m.install
		cmd, done := m.install.Update(msg)
		if domain := flow.certificateDomainRequest; domain != "" {
			flow.certificateDomainRequest = ""
			m.suspendedInstall = flow
			m.install = nil
			m.openCertificateManagerFor(domain)
			return m, cmd
		}
		if done {
			if flow.phase == phaseDone && flow.run.runErr == nil {
				m.RefreshStatus()
			}
			m.install = nil
		}
		return m, cmd
	}
	if m.protocols != nil {
		p := m.protocols
		cmd, done := m.protocols.Update(msg)
		if done {
			if p.phase == protocolPhaseDone && p.runErr == nil {
				m.RefreshStatus()
			}
			m.protocols = nil
		}
		return m, cmd
	}
	if m.relay != nil {
		r := m.relay
		cmd, done := m.relay.Update(msg)
		if done {
			if r.phase == relayPhaseDone && r.runErr == nil {
				m.RefreshStatus()
			}
			m.relay = nil
		}
		return m, cmd
	}
	if m.subscribe != nil {
		s := m.subscribe
		cmd, done := m.subscribe.Update(msg)
		if done {
			if s.phase == subscriptionPhaseDone && s.runErr == nil {
				m.RefreshStatus()
			}
			m.subscribe = nil
		}
		return m, cmd
	}
	if m.monitor != nil {
		flow := m.monitor
		cmd, done := m.monitor.Update(msg)
		if domain := flow.certificateDomainRequest; domain != "" {
			flow.certificateDomainRequest = ""
			m.suspendedMonitor = flow
			m.monitor = nil
			m.openCertificateManagerFor(domain)
			return m, cmd
		}
		if done {
			if flow.phase == monitorPhaseDone && flow.runErr == nil {
				m.RefreshStatus()
			}
			m.monitor = nil
		}
		return m, cmd
	}
	if m.core != nil {
		c := m.core
		cmd, done := m.core.Update(msg)
		if done {
			if c.phase == corePhaseDone && c.runErr == nil {
				m.RefreshStatus()
			}
			m.core = nil
		}
		return m, cmd
	}
	if m.certificates != nil {
		cmd, done := m.certificates.Update(msg)
		if done {
			m.certificates = nil
			m.resumeSuspendedFlow()
		}
		return m, cmd
	}
	if m.nodes != nil {
		nodeFlow := m.nodes
		cmd, done := nodeFlow.Update(msg)
		if domain := nodeFlow.certificateDomainRequest; domain != "" {
			nodeFlow.certificateDomainRequest = ""
			m.suspendedNodes = nodeFlow
			m.nodes = nil
			m.openCertificateManagerFor(domain)
			return m, cmd
		}
		if done {
			m.RefreshStatus()
			m.nodes = nil
		}
		return m, cmd
	}
	if m.selfupdate != nil {
		s := m.selfupdate
		cmd, done := m.selfupdate.Update(msg)
		if done {
			if s.phase == selfUpdatePhaseDone && s.runErr == nil {
				m.RefreshStatus()
			}
			m.selfupdate = nil
		}
		return m, cmd
	}
	if m.uninstall != nil {
		u := m.uninstall
		cmd, done := m.uninstall.Update(msg)
		if done {
			if u.phase == uninstallPhaseDone && u.runErr == nil {
				m.RefreshStatus()
			}
			m.uninstall = nil
		}
		return m, cmd
	}
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case msg.String() == "ctrl+c", isSelectionCancelKey(msg):
			return m, tea.Quit
		case isSelectionPreviousKey(msg):
			m.cursor = moveSelection(m.cursor, len(m.flatItems()), -1)
		case isSelectionNextKey(msg):
			m.cursor = moveSelection(m.cursor, len(m.flatItems()), 1)
		// The subscription-groups panel is only on screen here, so its keys are
		// bound here too and can never swallow a character typed into a form.
		case msg.String() == "[":
			m.groupIndex = moveSelection(m.groupIndex, len(m.status.Groups), -1)
		case msg.String() == "]":
			m.groupIndex = moveSelection(m.groupIndex, len(m.status.Groups), 1)
		case isSelectionConfirmKey(msg):
			return m, m.activate()
		}
	}
	return m, nil
}

// activate runs the highlighted menu item's own activation callback.
func (m *Model) activate() tea.Cmd {
	items := m.flatItems()
	if m.cursor < 0 || m.cursor >= len(items) {
		return nil
	}
	if fn := items[m.cursor].Activate; fn != nil {
		return fn(m)
	}
	return nil
}

var (
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle  = lipgloss.NewStyle().Bold(true)
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle    = lipgloss.NewStyle().Faint(true)
	statusOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statusBad   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	statusWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	summaryInfo = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	summaryDate = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// View implements tea.Model.
func (m *Model) View() string {
	width, height := m.frameSize()
	footer := m.footerView()
	bodyHeight := max(1, height-lipgloss.Height(footer))
	body := fitViewHeight(m.bodyView(width, bodyHeight), bodyHeight)
	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

func (m *Model) frameSize() (int, int) {
	width, height := m.width, m.height
	if width <= 0 {
		width = defaultWidth
	}
	if height <= 0 {
		height = defaultHeight
	}
	return max(40, width), max(12, height)
}

func (m *Model) bodyView(width, height int) string {
	panelFrameY := panelStyle.GetVerticalFrameSize()
	if m.LayoutMode() == LayoutWide {
		contentHeight := max(1, height-panelFrameY)
		available := max(48, width-8-panelGap)
		menuWidth := min(sidebarWidth, max(28, available/3))
		contentWidth := max(24, available-menuWidth)
		menuBody := m.menuView(menuWidth - 4)
		if lipgloss.Height(menuBody) > contentHeight {
			menuBody = fitViewHeight(menuBody, contentHeight)
		}
		menu := panelStyle.Width(menuWidth).Render(menuBody)
		content := m.contentColumn(contentWidth, height)
		return lipgloss.JoinHorizontal(lipgloss.Top, menu, strings.Repeat(" ", panelGap), content)
	}
	panelWidth := max(24, width-4)
	menuBody := m.menuView(panelWidth - 4)
	menuHeight := min(lipgloss.Height(menuBody), max(1, height-panelFrameY-3))
	menu := panelStyle.Width(panelWidth).Height(menuHeight).Render(fitViewHeight(menuBody, menuHeight))
	content := m.contentColumn(panelWidth, max(1, height-lipgloss.Height(menu)))
	return lipgloss.JoinVertical(lipgloss.Left, menu, content)
}

const (
	// statusPanelMinBody is the number of status rows kept visible before the
	// subscription-groups panel is dropped entirely: a groups panel that
	// squeezes the status list down to a couple of lines helps nobody.
	statusPanelMinBody = 8
	// groupsPanelMinBody is the smallest useful groups panel: a title plus at
	// least one subscription URL.
	groupsPanelMinBody = 3
)

// contentColumn renders everything to the right of the menu (below it in the
// narrow layout). An active sub-flow owns the whole column; the main screen
// splits it into the status panel and a dedicated subscription-groups panel.
func (m *Model) contentColumn(width, height int) string {
	panelFrameY := panelStyle.GetVerticalFrameSize()
	singlePanel := func(body string) string {
		panelHeight := max(1, height-panelFrameY)
		body = lipgloss.NewStyle().Width(width - 4).MaxHeight(panelHeight).Render(body)
		return panelStyle.Width(width).Height(panelHeight).Render(body)
	}
	if !m.showsStatus() {
		return singlePanel(m.contentView(width-4, max(1, height-panelFrameY)))
	}
	groupsBody := m.subscriptionGroupsView(width - 4)
	budget := height - 2*panelFrameY
	if groupsBody == "" || budget < statusPanelMinBody+groupsPanelMinBody {
		return singlePanel(m.statusView())
	}
	groupsHeight := min(lipgloss.Height(groupsBody), budget-statusPanelMinBody)
	groups := panelStyle.Width(width).Height(groupsHeight).
		Render(lipgloss.NewStyle().Width(width - 4).Render(fitViewHeight(groupsBody, groupsHeight)))
	statusHeight := max(1, height-lipgloss.Height(groups)-panelFrameY)
	status := panelStyle.Width(width).Height(statusHeight).
		Render(lipgloss.NewStyle().Width(width - 4).MaxHeight(statusHeight).Render(m.statusView()))
	return lipgloss.JoinVertical(lipgloss.Left, status, groups)
}

// showsStatus reports whether the main status screen is on display, meaning no
// sub-flow has taken over the content column.
func (m *Model) showsStatus() bool {
	return m.install == nil && m.protocols == nil && m.relay == nil && m.subscribe == nil &&
		m.monitor == nil && m.core == nil && m.certificates == nil && m.nodes == nil &&
		m.selfupdate == nil && m.uninstall == nil
}

func (m *Model) contentView(width, height int) string {
	if m.install != nil {
		m.install.setSize(width, height)
		return m.install.View()
	}
	if m.protocols != nil {
		m.protocols.setSize(width, height)
		return m.protocols.View()
	}
	if m.relay != nil {
		m.relay.setSize(width, height)
		return m.relay.View()
	}
	if m.subscribe != nil {
		m.subscribe.setSize(width, height)
		return m.subscribe.View()
	}
	if m.monitor != nil {
		m.monitor.setSize(width, height)
		return m.monitor.View()
	}
	if m.core != nil {
		m.core.setSize(width, height)
		return m.core.View()
	}
	if m.certificates != nil {
		m.certificates.setSize(width, height)
		return m.certificates.View()
	}
	if m.nodes != nil {
		m.nodes.setSize(width, height)
		return m.nodes.View()
	}
	if m.selfupdate != nil {
		m.selfupdate.setSize(width, height)
		return m.selfupdate.View()
	}
	if m.uninstall != nil {
		m.uninstall.setSize(width, height)
		return m.uninstall.View()
	}
	return m.statusView()
}

func (m *Model) footerView() string {
	var parts []operationHint
	if m.install == nil {
		if m.showsStatus() {
			parts = append(parts, menuFooterHints()...)
			if len(m.status.Groups) > 1 {
				parts = append(parts, hint("[ / ]", "Subscription group"))
			}
		} else if m.protocols != nil {
			parts = append(parts, m.protocols.footerHints()...)
		} else if m.relay != nil {
			parts = append(parts, m.relay.footerHints()...)
		} else if m.subscribe != nil {
			parts = append(parts, m.subscribe.footerHints()...)
		} else if m.monitor != nil {
			parts = append(parts, m.monitor.footerHints()...)
		} else if m.core != nil {
			parts = append(parts, m.core.footerHints()...)
		} else if m.certificates != nil {
			parts = append(parts, m.certificates.footerHints()...)
		} else if m.nodes != nil {
			parts = append(parts, m.nodes.footerHints()...)
		} else if m.selfupdate != nil {
			parts = append(parts, m.selfupdate.footerHints()...)
		} else if m.uninstall != nil {
			parts = append(parts, m.uninstall.footerHints()...)
		}
	} else {
		parts = append(parts, m.install.footerHints()...)
	}
	return hintLine(parts...)
}

func fitViewHeight(view string, height int) string {
	if height <= 0 {
		return ""
	}
	view = strings.TrimRight(view, "\n")
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// openCertificateManagerFor hands control to Certificate management for a
// domain the caller could not accept. The caller stores itself in the matching
// suspended field first; resumeSuspendedFlow puts it back afterwards.
func (m *Model) openCertificateManagerFor(domain string) {
	certs := newCertManagerForDomain(domain)
	certs.setSize(m.width, m.height)
	m.certificates = certs
}

// resumeSuspendedFlow returns to whichever screen handed control to Certificate
// management, clearing the rejected-domain error so the operator can retry the
// field with the credential they just added.
func (m *Model) resumeSuspendedFlow() {
	switch {
	case m.suspendedInstall != nil:
		m.install = m.suspendedInstall
		m.suspendedInstall = nil
		m.install.form.fieldErr = ""
		m.install.form.validationErr = nil
	case m.suspendedNodes != nil:
		m.nodes = m.suspendedNodes
		m.suspendedNodes = nil
		m.nodes.form.fieldErr = ""
		m.nodes.form.validationErr = nil
	case m.suspendedMonitor != nil:
		m.monitor = m.suspendedMonitor
		m.suspendedMonitor = nil
		m.monitor.parameterForm.fieldErr = ""
		m.monitor.parameterForm.validationErr = nil
	}
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (m *Model) statusView() string {
	s := m.status
	rows := []summaryLine{
		summaryRow("singbox-deploy", or(s.ToolVersion, "dev")),
		summaryRow("Domain", or(s.Domain, "unknown")),
		summaryRow("Public IP", or(s.PublicIP, "unknown")),
		summaryRow("Platform", or(s.OSArch, "unknown")),
		summaryRow("sing-box version", or(s.SingBoxVer, "not installed")),
		summaryRow("sing-box service", or(s.SingBoxState, "unknown")),
		summaryRow("Nginx service", or(s.NginxState, "unknown")),
		summaryRow("Monitor service", or(s.MonitorState, "unknown")),
		summaryRow("Certificate", or(s.CertState, "unknown")),
	}
	if s.MonitorCertState != "" {
		rows = append(rows, summaryRow("Monitor certificate", s.MonitorCertState))
	}
	rows = append(rows,
		summaryRow("Protocols", or(s.Protocols, "none")),
		summaryRow("Monitor URL", or(s.MonitorUI, "none")),
		summaryRow("Monitor token", or(s.MonitorToken, "none")),
		summaryRow("Traffic quota", or(s.TrafficQuota, "unknown")),
	)
	return titleStyle.Render("Status") + "\n" + renderSummary(rows)
}

// subscriptionGroupsView renders the currently selected subscription group.
// Every subscription URL the hub serves belongs to a group, so this panel — not
// the status list — is where they are read. It returns "" when there is nothing
// to show, and the caller then gives the whole column to the status panel.
func (m *Model) subscriptionGroupsView(width int) string {
	total := len(m.status.Groups)
	title := titleStyle.Render("Subscription groups")
	if total == 0 {
		if m.status.Domain == "" {
			return ""
		}
		return title + "\n" + dimStyle.Render("None published yet.")
	}
	index := clampGroupIndex(m.groupIndex, total)
	g := m.status.Groups[index]
	header := title + "  " + dimStyle.Render(fmt.Sprintf("[%d/%d]", index+1, total))
	if total > 1 {
		header += "  " + dimStyle.Render("[ / ] switch")
	}
	return header + "\n" + renderSummary([]summaryLine{
		summaryRow("Name", or(g.Alias, "unnamed")),
		summaryRow("Salt", or(g.Salt, "not set")),
		summaryRow("Members", or(truncateSummaryValue(g.Members, width-12), "none")),
		summaryRow("universal", groupURLValue(g, g.Subscription)),
		summaryRow("Clash Meta", groupURLValue(g, g.ClashMetaSub)),
		summaryRow("sing-box", groupURLValue(g, g.SingBoxSub)),
		summaryRow("Surge", groupURLValue(g, g.SurgeSub)),
	})
}

// groupURLValue renders one subscription URL, naming the reason it is absent:
// a group with no nodes is deliberately not served, which is a different state
// from a URL the status page merely could not assemble.
func groupURLValue(g SubscriptionGroupStatus, url string) string {
	if !g.Published {
		return labelGroupNotPublished
	}
	return or(url, "none")
}

// truncateSummaryValue shortens a one-line value that would otherwise wrap and
// push the panel's remaining rows out of view.
func truncateSummaryValue(value string, width int) string {
	if width < 8 || len(value) <= width {
		return value
	}
	return value[:width-1] + "…"
}

func (m *Model) menuView(width int) string {
	width = max(18, width)
	var b strings.Builder
	b.WriteString(titleStyle.Render("Menu") + "\n")
	idx := 0
	for _, g := range m.groups {
		b.WriteString(dimStyle.Render(g.Title) + "\n")
		for _, it := range g.Items {
			line := "  " + it.Label
			if idx == m.cursor {
				line = "› " + selStyle.Render(it.Label)
			}
			b.WriteString(line + "\n")
			idx++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
