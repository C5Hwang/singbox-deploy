package ui

import tea "github.com/charmbracelet/bubbletea"

type placeholderManager struct {
	title string
}

func newPlaceholderManager(title string) *placeholderManager {
	return &placeholderManager{title: title}
}

func (pm *placeholderManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		_ = msg
		return nil, true
	}
	return nil, false
}

func (pm *placeholderManager) View() string {
	return flowTitle.Render(pm.title) + "\n\n" +
		dimStyle.Render("This feature will be available in a future version.") + "\n"
}

func (pm *placeholderManager) footerHints() []operationHint {
	return returnFooterHints()
}
