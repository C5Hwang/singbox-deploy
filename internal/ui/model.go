package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/ui/common"
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
	Protocols    string
	Subscription string
	ClashMetaSub string
	SingBoxSub   string
	SurgeSub     string
	MonitorUI    string
	TrafficQuota string
	Salt         string
}

// MenuItem is a single selectable action within a group.
type MenuItem struct {
	Label string
}

// MenuGroup is a titled section of the grouped menu.
type MenuGroup struct {
	Title string
	Items []MenuItem
}

// Model is the root Bubble Tea model.
type Model struct {
	width       int
	height      int
	status      Status
	groups      []MenuGroup
	cursor      int // flat index across all items
	install     *installFlow
	protocols   *protocolManager
	subscribe   *subscriptionManager
	monitor     *monitorManager
	core        *coreManager
	selfupdate  *selfUpdateManager
	uninstall   *uninstallManager
	nodes       *nodeManager
	cert        *certManager
	placeholder *placeholderManager
}

// NewModel returns a Model populated with the default grouped menu.
func NewModel() *Model {
	return &Model{groups: defaultGroups(), status: loadStatus()}
}

func defaultGroups() []MenuGroup {
	return []MenuGroup{
		{Title: "Setup", Items: []MenuItem{
			{Label: "Installation"},
			{Label: "Node Management"},
		}},
		{Title: "Proxy", Items: []MenuItem{
			{Label: "Protocol settings"},
			{Label: "Subscription settings"},
		}},
		{Title: "Server", Items: []MenuItem{
			{Label: "Certificate"},
			{Label: "Monitor & quota"},
			{Label: "Routing rules"},
			{Label: "sing-box core"},
		}},
		{Title: "System", Items: []MenuItem{
			{Label: "Self-update"},
			{Label: "Uninstall"},
		}},
	}
}

// RefreshStatus reloads the status panel from the current host and state files.
func (m *Model) RefreshStatus() { m.status = loadStatus() }

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

	// While a sub-flow is active, delegate everything to it so its state machine
	// and async run messages are handled in one place.
	if m.install != nil {
		flow := m.install
		cmd, done := m.install.Update(msg)
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
	if m.nodes != nil {
		n := m.nodes
		cmd, done := m.nodes.Update(msg)
		if done {
			if n.phase == nodePhaseDone && n.runErr == nil {
				m.RefreshStatus()
			}
			m.nodes = nil
		}
		return m, cmd
	}
	if m.cert != nil {
		cmd, done := m.cert.Update(msg)
		if done {
			m.cert = nil
		}
		return m, cmd
	}
	if m.placeholder != nil {
		cmd, done := m.placeholder.Update(msg)
		if done {
			m.placeholder = nil
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
		case isSelectionConfirmKey(msg):
			return m, m.activate()
		}
	}
	return m, nil
}

// activate runs the action for the highlighted menu item.
func (m *Model) activate() tea.Cmd {
	switch m.cursor {
	case 0:
		flow := newInstallFlow()
		flow.setSize(m.width, m.height)
		m.install = flow
	case 1:
		n := newNodeManager()
		n.setSize(m.width, m.height)
		m.nodes = n
	case 2:
		p := newProtocolManager()
		p.setSize(m.width, m.height)
		m.protocols = p
	case 3:
		s := newSubscriptionManager()
		s.setSize(m.width, m.height)
		m.subscribe = s
	case 4:
		c := newCertManager()
		c.setSize(m.width, m.height)
		m.cert = c
	case 5:
		t := newMonitorManager()
		t.setSize(m.width, m.height)
		m.monitor = t
	case 6:
		m.placeholder = newPlaceholderManager("Routing rules")
	case 7:
		c := newCoreManager()
		c.setSize(m.width, m.height)
		m.core = c
	case 8:
		s := newSelfUpdateManager()
		s.setSize(m.width, m.height)
		m.selfupdate = s
	case 9:
		u := newUninstallManager()
		u.setSize(m.width, m.height)
		m.uninstall = u
	}
	return nil
}

var (
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle  = lipgloss.NewStyle().Bold(true)
	selStyle    = common.SelStyle
	dimStyle    = common.DimStyle
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
	contentHeight := max(1, height-panelFrameY)
	if m.LayoutMode() == LayoutWide {
		available := max(48, width-8-panelGap)
		menuWidth := min(sidebarWidth, max(28, available/3))
		contentWidth := max(24, available-menuWidth)
		menuBody := m.menuView(menuWidth - 4)
		if lipgloss.Height(menuBody) > contentHeight {
			menuBody = fitViewHeight(menuBody, contentHeight)
		}
		menu := panelStyle.Width(menuWidth).Render(menuBody)
		contentBody := m.contentView(contentWidth-4, contentHeight)
		contentBody = lipgloss.NewStyle().Width(contentWidth - 4).MaxHeight(contentHeight).Render(contentBody)
		content := panelStyle.Width(contentWidth).Height(contentHeight).Render(contentBody)
		return lipgloss.JoinHorizontal(lipgloss.Top, menu, strings.Repeat(" ", panelGap), content)
	}
	panelWidth := max(24, width-4)
	menuBody := m.menuView(panelWidth - 4)
	menuHeight := min(lipgloss.Height(menuBody), max(1, height-panelFrameY-3))
	menu := panelStyle.Width(panelWidth).Height(menuHeight).Render(fitViewHeight(menuBody, menuHeight))
	contentHeight = max(1, height-lipgloss.Height(menu)-panelFrameY)
	contentBody := m.contentView(panelWidth-4, contentHeight)
	contentBody = lipgloss.NewStyle().Width(panelWidth - 4).MaxHeight(contentHeight).Render(contentBody)
	content := panelStyle.Width(panelWidth).Height(contentHeight).Render(contentBody)
	return lipgloss.JoinVertical(lipgloss.Left, menu, content)
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
	if m.selfupdate != nil {
		m.selfupdate.setSize(width, height)
		return m.selfupdate.View()
	}
	if m.uninstall != nil {
		m.uninstall.setSize(width, height)
		return m.uninstall.View()
	}
	if m.nodes != nil {
		m.nodes.setSize(width, height)
		return m.nodes.View()
	}
	if m.cert != nil {
		m.cert.setSize(width, height)
		return m.cert.View()
	}
	if m.placeholder != nil {
		return m.placeholder.View()
	}
	return m.statusView()
}

func (m *Model) footerView() string {
	var parts []operationHint
	if m.install == nil {
		if m.protocols == nil && m.subscribe == nil && m.monitor == nil && m.core == nil && m.selfupdate == nil && m.uninstall == nil && m.nodes == nil && m.cert == nil && m.placeholder == nil {
			parts = append(parts, menuFooterHints()...)
		} else if m.protocols != nil {
			parts = append(parts, m.protocols.footerHints()...)
		} else if m.subscribe != nil {
			parts = append(parts, m.subscribe.footerHints()...)
		} else if m.monitor != nil {
			parts = append(parts, m.monitor.footerHints()...)
		} else if m.core != nil {
			parts = append(parts, m.core.footerHints()...)
		} else if m.selfupdate != nil {
			parts = append(parts, m.selfupdate.footerHints()...)
		} else if m.uninstall != nil {
			parts = append(parts, m.uninstall.footerHints()...)
		} else if m.nodes != nil {
			parts = append(parts, m.nodes.footerHints()...)
		} else if m.cert != nil {
			parts = append(parts, m.cert.footerHints()...)
		} else if m.placeholder != nil {
			parts = append(parts, m.placeholder.footerHints()...)
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
		summaryRow("OS/Arch", or(s.OSArch, "unknown")),
		summaryRow("sing-box version", or(s.SingBoxVer, "not installed")),
		summaryRow("sing-box service", or(s.SingBoxState, "unknown")),
		summaryRow("Nginx service", or(s.NginxState, "unknown")),
		summaryRow("Monitor service", or(s.MonitorState, "unknown")),
		summaryRow("Certificate", or(s.CertState, "unknown")),
		summaryRow("Protocols", or(s.Protocols, "none")),
		summaryRow("Salt", or(s.Salt, "not set")),
		summaryRow("Subscription (universal)", or(s.Subscription, "none")),
		summaryRow("Subscription (Clash Meta)", or(s.ClashMetaSub, "none")),
		summaryRow("Subscription (sing-box)", or(s.SingBoxSub, "none")),
		summaryRow("Subscription (Surge)", or(s.SurgeSub, "none")),
		summaryRow("Monitor URL", or(s.MonitorUI, "none")),
		summaryRow("Traffic quota", or(s.TrafficQuota, "unknown")),
	}
	return titleStyle.Render("Status") + "\n" + renderSummary(rows)
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
