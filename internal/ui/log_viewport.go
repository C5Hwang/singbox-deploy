package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Shared helpers for the scrollable log viewers in the core and monitor
// managers, which otherwise carried byte-for-byte identical copies.

// serviceLogViewport is a scrollable viewer over fetched journalctl output,
// shared by the core and monitor service-log screens.
type serviceLogViewport struct {
	logs   string
	logErr error
	scroll int
}

func (v *serviceLogViewport) set(logs string, err error) {
	v.logs, v.logErr = logs, err
	v.scroll = 0
}

func (v *serviceLogViewport) rows(width int) []string { return logRows(v.logs, width) }

func (v *serviceLogViewport) visible(width, height int) []string {
	v.clamp(width, height)
	return visibleLogRows(v.rows(width), logBudget(height), v.scroll)
}

func (v *serviceLogViewport) scrollBy(delta, width, height int) {
	v.scroll += delta
	v.clamp(width, height)
}

func (v *serviceLogViewport) clamp(width, height int) {
	v.scroll = clampLogScroll(v.scroll, len(v.rows(width)), logBudget(height))
}

// handleKey processes the shared scroll keys of the service-log screens,
// reporting whether the key was consumed.
func (v *serviceLogViewport) handleKey(key string, width, height int) bool {
	switch key {
	case "up", "k":
		v.scrollBy(1, width, height)
	case "down", "j":
		v.scrollBy(-1, width, height)
	case "pgup":
		v.scrollBy(logBudget(height), width, height)
	case "pgdown":
		v.scrollBy(-logBudget(height), width, height)
	case "home":
		v.scroll = max(0, len(v.rows(width))-logBudget(height))
	case "end":
		v.scroll = 0
	default:
		return false
	}
	return true
}

// journalctlOutput returns the last lines of a systemd unit's journal.
func journalctlOutput(ctx context.Context, unit string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	out, err := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager").CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return "", err
		}
		return string(out), fmt.Errorf("%w: %s", err, text)
	}
	return string(out), nil
}

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
