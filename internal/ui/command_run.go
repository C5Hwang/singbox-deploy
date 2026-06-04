package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/install"
)

type commandRun struct {
	width  int
	height int

	bar         progress.Model
	events      []install.Event
	logBuf      []string
	logScroll   int
	runErr      error
	runComplete bool
	ch          chan runMsg
}

type commandRunView interface {
	runState() *commandRun
}

type commandRunTarget interface {
	commandRunView
	markRunFailed()
}

func newCommandRun() commandRun {
	return commandRun{bar: progress.New(progress.WithDefaultGradient())}
}

func (r *commandRun) setSize(width, height int) {
	r.width = width
	r.height = height
	r.bar.Width = min(width-4, 60)
}

func (r *commandRun) resetRun(ch chan runMsg) {
	r.events = nil
	r.logBuf = nil
	r.logScroll = 0
	r.runErr = nil
	r.runComplete = false
	r.ch = ch
}

func handleCommandRun(target commandRunTarget, msg runMsg) tea.Cmd {
	r := target.runState()
	if msg.event != nil {
		r.events = append(r.events, *msg.event)
		r.appendLog(formatRunEvent(*msg.event))
	}
	if msg.logLine != "" {
		r.appendLog(dimStyle.Render(msg.logLine))
	}
	if msg.done {
		r.runErr = msg.err
		if msg.err != nil {
			target.markRunFailed()
			return nil
		}
		r.runComplete = true
		r.logScroll = 0
		return nil
	}
	return r.waitForRun()
}

func formatRunEvent(e install.Event) string {
	line := fmt.Sprintf("[%d/%d] %s - %s", e.Index, e.Total, e.Label, e.Status)
	if e.Err != nil {
		line += ": " + e.Err.Error()
	}
	return line
}

func (r *commandRun) waitForRun() tea.Cmd {
	ch := r.ch
	return func() tea.Msg { return <-ch }
}

func commandRunningView(view commandRunView, title string) string {
	r := view.runState()
	logs := r.logView(r.logViewportHeight())
	hint := "↑/↓ scroll log"
	if r.runComplete {
		hint = "complete · press enter to show summary · " + hint
	}
	body := flowTitle.Render(title) + "\n\n" + r.bar.ViewAs(r.percent())
	if logs != "" {
		body += "\n\n" + logs
	}
	return body + "\n\n" + dimStyle.Render(hint)
}

func commandFailedView(view commandRunView, title string) string {
	r := view.runState()
	body := flowErr.Render(title) + "\n\n" + r.runErr.Error()
	if logs := r.logView(r.doneLogHeight()); logs != "" {
		body += "\n\n" + logs + "\n\n" + dimStyle.Render("↑/↓ scroll log · any other key to return")
		return body
	}
	return body + "\n\n" + dimStyle.Render("press any key to return")
}

func (r *commandRun) appendLog(line string) {
	if r.logScroll > 0 {
		r.logScroll += r.logLineHeight(line)
	}
	r.logBuf = append(r.logBuf, line)
	r.clampLogScroll(r.logViewportHeight())
}

func (r *commandRun) logView(height int) string {
	lines := r.visibleLogLines(height)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (r *commandRun) visibleLogLines(height int) []string {
	rows := r.logRows()
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	visible := min(height, len(rows))
	r.clampLogScroll(height)
	start := len(rows) - visible - r.logScroll
	return rows[start : start+visible]
}

func (r *commandRun) scrollLog(delta, height int) {
	r.logScroll += delta
	r.clampLogScroll(height)
}

func (r *commandRun) clampLogScroll(height int) {
	r.logScroll = min(max(0, r.logScroll), r.maxLogScroll(height))
}

func (r *commandRun) maxLogScroll(height int) int {
	if height <= 0 {
		return 0
	}
	return max(0, len(r.logRows())-height)
}

func (r *commandRun) logRows() []string {
	var rows []string
	for _, line := range r.logBuf {
		rows = append(rows, r.wrapLogLine(line)...)
	}
	return rows
}

func (r *commandRun) wrapLogLine(line string) []string {
	wrapped := lipgloss.NewStyle().Width(r.logWrapWidth()).Render(line)
	return strings.Split(wrapped, "\n")
}

func (r *commandRun) logLineHeight(line string) int {
	return max(1, lipgloss.Height(lipgloss.NewStyle().Width(r.logWrapWidth()).Render(line)))
}

func (r *commandRun) logWrapWidth() int {
	if r.width <= 0 {
		return 80
	}
	return max(1, r.width)
}

func (r *commandRun) logViewportHeight() int {
	if r.height <= 0 {
		return 12
	}
	return max(1, r.height-6)
}

func (r *commandRun) doneLogHeight() int {
	if r.height <= 0 {
		return 12
	}
	return max(1, r.height-7)
}

func (r *commandRun) percent() float64 {
	if len(r.events) == 0 {
		return 0
	}
	last := r.events[len(r.events)-1]
	if last.Total == 0 {
		return 0
	}
	return float64(last.Index) / float64(last.Total)
}
