package common

import tea "github.com/charmbracelet/bubbletea"

type SelectionKeyHandlers struct {
	Move       func(delta int)
	Toggle     func()
	Confirm    func() (tea.Cmd, bool)
	Back       func() (tea.Cmd, bool)
	Cancel     func() (tea.Cmd, bool)
	ConfirmYes bool
	CancelNo   bool
}

func HandleSelectionKey(msg tea.KeyMsg, h SelectionKeyHandlers) (tea.Cmd, bool, bool) {
	if IsSelectionPreviousKey(msg) && h.Move != nil {
		h.Move(-1)
		return nil, false, true
	}
	if IsSelectionNextKey(msg) && h.Move != nil {
		h.Move(1)
		return nil, false, true
	}
	if IsSelectionToggleKey(msg) && h.Toggle != nil {
		h.Toggle()
		return nil, false, true
	}
	if (IsSelectionConfirmKey(msg) || h.ConfirmYes && IsSelectionYesKey(msg)) && h.Confirm != nil {
		cmd, done := h.Confirm()
		return cmd, done, true
	}
	if IsSelectionBackKey(msg) && h.Back != nil {
		cmd, done := h.Back()
		return cmd, done, true
	}
	if (IsSelectionCancelKey(msg) || h.CancelNo && IsSelectionNoKey(msg)) && h.Cancel != nil {
		cmd, done := h.Cancel()
		return cmd, done, true
	}
	return nil, false, false
}

func MoveSelection(cursor, length, delta int) int {
	if length <= 0 {
		return 0
	}
	next := (cursor + delta) % length
	if next < 0 {
		next += length
	}
	return next
}

func SelectedIndex(cursor, length int) (int, bool) {
	if length <= 0 {
		return 0, false
	}
	return min(max(0, cursor), length-1), true
}

func SelectedStringOption(options []string, cursor int) (string, bool) {
	idx, ok := SelectedIndex(cursor, len(options))
	if !ok {
		return "", false
	}
	return options[idx], true
}

func ToggleStringSelection(selected map[string]bool, options []string, cursor int) bool {
	opt, ok := SelectedStringOption(options, cursor)
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

func IsSelectionPreviousKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k", "left", "h":
		return true
	default:
		return false
	}
}

func IsSelectionNextKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "down", "j", "right", "l":
		return true
	default:
		return false
	}
}

func IsSelectionToggleKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case " ", "space":
		return true
	default:
		return false
	}
}

func IsSelectionConfirmKey(msg tea.KeyMsg) bool {
	return msg.String() == "enter"
}

func IsSelectionYesKey(msg tea.KeyMsg) bool {
	return msg.String() == "y"
}

func IsSelectionNoKey(msg tea.KeyMsg) bool {
	return msg.String() == "n"
}

func IsSelectionBackKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "shift+tab", "ctrl+b":
		return true
	default:
		return false
	}
}

func IsSelectionCancelKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "esc", "q":
		return true
	default:
		return false
	}
}
