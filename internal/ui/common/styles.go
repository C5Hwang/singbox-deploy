package common

import "github.com/charmbracelet/lipgloss"

var (
	FlowTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	FlowOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	FlowErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	FlowRandom = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	SelStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	DimStyle   = lipgloss.NewStyle().Faint(true)
)
