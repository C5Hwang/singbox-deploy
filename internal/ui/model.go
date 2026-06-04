package ui

import (
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
	TrafficUI    string
	TrafficQuota string
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
	width     int
	height    int
	status    Status
	groups    []MenuGroup
	cursor    int // flat index across all items
	wizard    *wizard
	protocols *protocolManager
}

// NewModel returns a Model populated with the default grouped menu.
func NewModel() *Model {
	return &Model{groups: defaultGroups(), status: loadStatus()}
}

func defaultGroups() []MenuGroup {
	return []MenuGroup{
		{Title: "Install", Items: []MenuItem{{Label: "Install / reinstall"}}},
		{Title: "Protocols", Items: []MenuItem{{Label: "Manage protocols"}}},
		{Title: "User & Subscription", Items: []MenuItem{{Label: "Account & subscriptions"}}},
		{Title: "Certificate & Nginx", Items: []MenuItem{{Label: "Certificate / site management"}}},
		{Title: "Traffic", Items: []MenuItem{{Label: "Traffic monitor"}}},
		{Title: "Routing", Items: []MenuItem{{Label: "Domain/IP blacklist"}}},
		{Title: "Core", Items: []MenuItem{{Label: "sing-box core management"}}},
		{Title: "System", Items: []MenuItem{
			{Label: "Self-update"},
			{Label: "Uninstall"},
		}},
	}
}

// SetStatus replaces the status panel contents.
func (m *Model) SetStatus(s Status) { m.status = s }

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
	if m.wizard != nil {
		w := m.wizard
		cmd, done := m.wizard.Update(msg)
		if done {
			if w.phase == phaseDone && w.runErr == nil {
				m.RefreshStatus()
			}
			m.wizard = nil
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

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.flatItems())-1 {
				m.cursor++
			}
		case "enter":
			return m, m.activate()
		}
	}
	return m, nil
}

// activate runs the action for the highlighted menu item.
func (m *Model) activate() tea.Cmd {
	switch m.cursor {
	case 0:
		w := newWizard()
		w.setSize(m.width, m.height)
		m.wizard = w
	case 1:
		p := newProtocolManager()
		p.setSize(m.width, m.height)
		m.protocols = p
	}
	return nil
}

var (
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	titleStyle = lipgloss.NewStyle().Bold(true)
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle   = lipgloss.NewStyle().Faint(true)
	statusOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statusBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	statusWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
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
	if m.wizard != nil {
		m.wizard.setSize(width, height)
		return m.wizard.View()
	}
	if m.protocols != nil {
		m.protocols.setSize(width, height)
		return m.protocols.View()
	}
	return m.statusView()
}

func (m *Model) footerView() string {
	var parts []string
	if m.wizard == nil {
		if m.protocols == nil {
			parts = append(parts, "↑/↓ move", "enter select", "esc/q quit")
		} else {
			parts = append(parts, m.protocols.footerHints()...)
		}
	} else {
		parts = append(parts, m.wizard.footerHints()...)
	}
	return dimStyle.Render(strings.Join(parts, " · "))
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
	rows := [][2]string{
		{"Domain", or(s.Domain, "unknown")},
		{"Public IP", or(s.PublicIP, "unknown")},
		{"OS/Arch", or(s.OSArch, "unknown")},
		{"sing-box", or(s.SingBoxVer, "not installed")},
		{"Service", or(s.SingBoxState, "unknown")},
		{"Nginx", or(s.NginxState, "unknown")},
		{"Monitor", or(s.MonitorState, "unknown")},
		{"Certificate", or(s.CertState, "unknown")},
		{"Protocols", or(s.Protocols, "none")},
		{"Subscription", or(s.Subscription, "none")},
		{"Clash Meta", or(s.ClashMetaSub, "none")},
		{"sing-box Sub", or(s.SingBoxSub, "none")},
		{"Traffic UI", or(s.TrafficUI, "none")},
		{"Traffic", or(s.TrafficQuota, "unknown")},
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Status") + "\n")
	for _, r := range rows {
		b.WriteString(dimStyle.Render(r[0]+": ") + renderStatusField(r[0], r[1]) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderStatusField(label, value string) string {
	if !isRunningStatusField(label) {
		return value
	}
	switch runningStatusLevel(value) {
	case statusLevelRunning:
		return statusOK.Render(value)
	case statusLevelStopped:
		return statusBad.Render(value)
	default:
		return statusWarn.Render(value)
	}
}

func isRunningStatusField(label string) bool {
	switch label {
	case "Service", "Nginx", "Monitor":
		return true
	default:
		return false
	}
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
