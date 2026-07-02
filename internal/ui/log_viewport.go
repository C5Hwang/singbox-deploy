package ui

import "strings"

// Shared helpers for the scrollable log viewers in the core and monitor
// managers, which otherwise carried byte-for-byte identical copies.

// logRows word-wraps text into styled display rows at the given width.
func logRows(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	style := dimStyle.Width(max(1, width))
	var rows []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		rows = append(rows, strings.Split(style.Render(line), "\n")...)
	}
	return rows
}

// logBudget is the number of log lines a viewer of the given total height can
// show, accounting for the title/chrome around it.
func logBudget(height int) int {
	if height <= 0 {
		return 12
	}
	return max(1, height-5)
}

// clampLogScroll bounds a scroll offset to [0, maxScroll] for the given row
// count and line budget.
func clampLogScroll(scroll, rowCount, budget int) int {
	return min(max(0, scroll), max(0, rowCount-budget))
}

// visibleLogRows returns the bottom-anchored window of rows for a viewer with
// the given budget and (already clamped) scroll offset.
func visibleLogRows(rows []string, budget, scroll int) []string {
	if len(rows) == 0 {
		return nil
	}
	visible := min(budget, len(rows))
	start := len(rows) - visible - scroll
	if start < 0 {
		start = 0
	}
	return rows[start : start+visible]
}
