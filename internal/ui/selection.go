package ui

import tea "github.com/charmbracelet/bubbletea"

type selectionKeyHandlers struct {
	Move       func(delta int)
	Toggle     func()
	Confirm    func() (tea.Cmd, bool)
	Back       func() (tea.Cmd, bool)
	Cancel     func() (tea.Cmd, bool)
	ConfirmYes bool
	CancelNo   bool
}

func handleSelectionKey(msg tea.KeyMsg, h selectionKeyHandlers) (tea.Cmd, bool, bool) {
	if isSelectionPreviousKey(msg) && h.Move != nil {
		h.Move(-1)
		return nil, false, true
	}
	if isSelectionNextKey(msg) && h.Move != nil {
		h.Move(1)
		return nil, false, true
	}
	if isSelectionToggleKey(msg) && h.Toggle != nil {
		h.Toggle()
		return nil, false, true
	}
	if (isSelectionConfirmKey(msg) || h.ConfirmYes && isSelectionYesKey(msg)) && h.Confirm != nil {
		cmd, done := h.Confirm()
		return cmd, done, true
	}
	if isSelectionBackKey(msg) && h.Back != nil {
		cmd, done := h.Back()
		return cmd, done, true
	}
	if (isSelectionCancelKey(msg) || h.CancelNo && isSelectionNoKey(msg)) && h.Cancel != nil {
		cmd, done := h.Cancel()
		return cmd, done, true
	}
	return nil, false, false
}

func moveSelection(cursor, length, delta int) int {
	if length <= 0 {
		return 0
	}
	next := (cursor + delta) % length
	if next < 0 {
		next += length
	}
	return next
}

func selectedIndex(cursor, length int) (int, bool) {
	if length <= 0 {
		return 0, false
	}
	return min(max(0, cursor), length-1), true
}

func selectedStringOption(options []string, cursor int) (string, bool) {
	idx, ok := selectedIndex(cursor, len(options))
	if !ok {
		return "", false
	}
	return options[idx], true
}

func toggleStringSelection(selected map[string]bool, options []string, cursor int) bool {
	opt, ok := selectedStringOption(options, cursor)
	if !ok {
		return false
	}
	if selected[opt] {
		delete(selected, opt)
	} else {
		selected[opt] = true
	}
	return true
}

func isSelectionPreviousKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k", "left", "h":
		return true
	default:
		return false
	}
}

func isSelectionNextKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "down", "j", "right", "l":
		return true
	default:
		return false
	}
}

func isSelectionToggleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case " ", "space":
		return true
	default:
		return false
	}
}

func isSelectionConfirmKey(msg tea.KeyMsg) bool {
	return msg.String() == "enter"
}

func isSelectionYesKey(msg tea.KeyMsg) bool {
	return msg.String() == "y"
}

func isSelectionNoKey(msg tea.KeyMsg) bool {
	return msg.String() == "n"
}

func isSelectionBackKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "shift+tab", "ctrl+b":
		return true
	default:
		return false
	}
}

func isSelectionCancelKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "esc", "q":
		return true
	default:
		return false
	}
}
