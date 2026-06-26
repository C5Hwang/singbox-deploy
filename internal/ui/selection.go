package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/ui/common"
)

type selectionKeyHandlers = common.SelectionKeyHandlers

func handleSelectionKey(msg tea.KeyMsg, h selectionKeyHandlers) (tea.Cmd, bool, bool) {
	return common.HandleSelectionKey(msg, h)
}

func moveSelection(cursor, length, delta int) int {
	return common.MoveSelection(cursor, length, delta)
}

func selectedIndex(cursor, length int) (int, bool) {
	return common.SelectedIndex(cursor, length)
}

func selectedStringOption(options []string, cursor int) (string, bool) {
	return common.SelectedStringOption(options, cursor)
}

func toggleStringSelection(selected map[string]bool, options []string, cursor int) bool {
	return common.ToggleStringSelection(selected, options, cursor)
}

func isSelectionPreviousKey(msg tea.KeyMsg) bool { return common.IsSelectionPreviousKey(msg) }
func isSelectionNextKey(msg tea.KeyMsg) bool     { return common.IsSelectionNextKey(msg) }
func isSelectionToggleKey(msg tea.KeyMsg) bool   { return common.IsSelectionToggleKey(msg) }
func isSelectionConfirmKey(msg tea.KeyMsg) bool  { return common.IsSelectionConfirmKey(msg) }
func isSelectionYesKey(msg tea.KeyMsg) bool      { return common.IsSelectionYesKey(msg) }
func isSelectionNoKey(msg tea.KeyMsg) bool       { return common.IsSelectionNoKey(msg) }
func isSelectionBackKey(msg tea.KeyMsg) bool     { return common.IsSelectionBackKey(msg) }
func isSelectionCancelKey(msg tea.KeyMsg) bool   { return common.IsSelectionCancelKey(msg) }
