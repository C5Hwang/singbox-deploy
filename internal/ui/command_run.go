package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

type commandRun struct {
	width  int
	height int

	bar         progress.Model
	events      []deploy.Event
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

func runProgressSender(ch chan<- runMsg) func(deploy.Event) {
	return func(event deploy.Event) {
		ev := event
		ch <- runMsg{event: &ev}
	}
}

func offsetRunProgress(progress func(deploy.Event), offset, total int) func(deploy.Event) {
	return func(event deploy.Event) {
		event.Index += offset
		event.Total = total
		deploy.EmitProgress(progress, event)
	}
}

// shiftRunProgress also reserves the first offset steps for work the caller has
// already reported, but keeps the callee's own step count by shifting Total
// alongside Index. Use it instead of offsetRunProgress when the callee's fan-out
// size is only known once it starts, so the bar still lands on 100%.
func shiftRunProgress(progress func(deploy.Event), offset int) func(deploy.Event) {
	return func(event deploy.Event) {
		event.Index += offset
		event.Total += offset
		deploy.EmitProgress(progress, event)
	}
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

func formatRunEvent(e deploy.Event) string {
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
	body := flowTitle.Render(title) + "\n\n" + r.bar.ViewAs(r.percent())
	if logs != "" {
		body += "\n\n" + logs
	}
	if r.runComplete {
		body += "\n\n" + flowOK.Render("Complete")
	}
	return body
}

func commandFailedView(view commandRunView, title string) string {
	r := view.runState()
	body := flowErr.Render(title) + "\n\n" + r.runErr.Error()
	if logs := r.logView(r.doneLogHeight()); logs != "" {
		body += "\n\n" + logs
	}
	return body
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

// handleScrollKey processes the shared log-scroll keys against the given
// viewport height, reporting whether the key was consumed.
func (r *commandRun) handleScrollKey(key string, height int) bool {
	switch key {
	case "up", "k":
		r.scrollLog(1, height)
	case "down", "j":
		r.scrollLog(-1, height)
	case "pgup":
		r.scrollLog(height, height)
	case "pgdown":
		r.scrollLog(-height, height)
	case "home":
		r.logScroll = r.maxLogScroll(height)
	case "end":
		r.logScroll = 0
	default:
		return false
	}
	return true
}

// handleDoneKey implements the shared done-phase keys: after a failed run the
// scroll keys page through the error log; any other key closes the screen.
func (r *commandRun) handleDoneKey(key string) (tea.Cmd, bool) {
	if r.runErr != nil && r.handleScrollKey(key, r.doneLogHeight()) {
		return nil, false
	}
	return nil, true
}

// handleLogWheel scrolls the run log on mouse-wheel events while it is
// visible (running phase, or done phase after a failure), reporting whether
// the event was consumed.
func (r *commandRun) handleLogWheel(button tea.MouseButton, visible bool) bool {
	if !visible {
		return false
	}
	switch button {
	case tea.MouseButtonWheelUp:
		r.scrollLog(3, r.logViewportHeight())
	case tea.MouseButtonWheelDown:
		r.scrollLog(-3, r.logViewportHeight())
	default:
		return false
	}
	return true
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

// percent is the fraction of the run that has finished, not the fraction it
// has reached: a step that is still running counts the steps before it. A run
// whose slowest step is also its last would otherwise sit at 100% for its whole
// duration. Every flow's terminal event carries ok/fail/done/warn, so this
// still reaches 100%.
func (r *commandRun) percent() float64 {
	if len(r.events) == 0 {
		return 0
	}
	last := r.events[len(r.events)-1]
	if last.Total == 0 {
		return 0
	}
	done := last.Index
	if last.Status == "running" {
		done--
	}
	return max(0, float64(done)/float64(last.Total))
}
