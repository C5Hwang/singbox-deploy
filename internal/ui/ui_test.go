package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestProgressPercent(t *testing.T) {
	p := Progress{Current: 4, Total: 10, Label: "Install sing-box"}
	if p.Percent() != 0.4 {
		t.Fatalf("Percent = %v", p.Percent())
	}
	if p.Title() != "4/10 Install sing-box" {
		t.Fatalf("Title = %q", p.Title())
	}
}

func TestProgressZeroTotal(t *testing.T) {
	p := Progress{Current: 0, Total: 0, Label: "Init"}
	if p.Percent() != 0 {
		t.Fatalf("Percent = %v", p.Percent())
	}
}

func TestNarrowLayoutMode(t *testing.T) {
	m := NewModel()
	m.SetSize(70, 24)
	if m.LayoutMode() != LayoutNarrow {
		t.Fatalf("mode = %v", m.LayoutMode())
	}
	m.SetSize(120, 30)
	if m.LayoutMode() != LayoutWide {
		t.Fatalf("mode = %v", m.LayoutMode())
	}
}

func TestInstallStepsLabeled(t *testing.T) {
	steps := InstallSteps()
	if len(steps) == 0 {
		t.Fatalf("expected install steps")
	}
	if steps[0].Label == "" {
		t.Fatalf("first step has no label")
	}
}

func TestViewUsesInternalPanels(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	view := m.View()
	if strings.Count(view, "╭") < 2 {
		t.Fatalf("view should render menu and content panels:\n%s", view)
	}
}

func TestInstallViewKeepsMenuVisible(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	m.wizard = &wizard{phase: phasePreflight, hosts: "ready", host: supportedTestHost()}
	view := m.View()
	if !strings.Contains(view, "Menu") {
		t.Fatalf("install view should keep menu and wizard visible:\n%s", view)
	}
}

func TestWideInstallContentUsesMenuHeight(t *testing.T) {
	m := NewModel()
	m.SetSize(160, 60)
	w := &wizard{phase: phaseRunning, bar: progressBarForTest()}
	m.wizard = w
	_ = m.View()
	if want := lipgloss.Height(m.menuView(sidebarWidth - 4)); w.height != want {
		t.Fatalf("wizard height = %d, want menu height %d", w.height, want)
	}
}

func supportedTestHost() system.Host {
	return system.Host{OS: system.OSRelease{Family: system.FamilyDebian, ID: "ubuntu"}, Arch: "amd64", IsRoot: true}
}

func TestInstallFieldShowsUsageNote(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New(), width: 80}
	w.startForm()
	view := w.View()
	if !strings.Contains(view, "Used for certificate issuance") {
		t.Fatalf("field usage note missing:\n%s", view)
	}
}

func TestDryRunShortcutShowsIndicator(t *testing.T) {
	m := NewModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !m.dryRun {
		t.Fatalf("dry-run shortcut did not enable mode")
	}
	if !strings.Contains(m.View(), "dry-run mode") {
		t.Fatalf("view missing dry-run indicator:\n%s", m.View())
	}
}

func TestDryRunRunningPausesAfterVisibleUpdate(t *testing.T) {
	w := &wizard{phase: phaseRunning, dryRun: true, ch: make(chan runMsg, 1)}
	cmd := w.handleRun(runMsg{logLine: "[dry-run] apt-get update"})
	if cmd != nil {
		t.Fatalf("dry-run visible update should wait for enter")
	}
	if !w.dryRunAwaitingEnter {
		t.Fatalf("dry-run should wait for enter after visible update")
	}
	if !strings.Contains(w.View(), "enter to show next dry-run update") {
		t.Fatalf("running view missing dry-run prompt:\n%s", w.View())
	}
}

func TestDryRunRunningEnterReadsNextUpdate(t *testing.T) {
	w := &wizard{phase: phaseRunning, dryRun: true, ch: make(chan runMsg, 1)}
	w.handleRun(runMsg{logLine: "first"})
	w.ch <- runMsg{logLine: "second"}
	cmd, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done {
		t.Fatalf("enter should continue dry-run, not close wizard")
	}
	if cmd == nil {
		t.Fatalf("enter should read next dry-run update")
	}
	msg, ok := cmd().(runMsg)
	if !ok || msg.logLine != "second" {
		t.Fatalf("next message = %#v", msg)
	}
	w.handleRun(msg)
	if !strings.Contains(strings.Join(w.logBuf, "\n"), "second") {
		t.Fatalf("log buffer missing next update: %#v", w.logBuf)
	}
}

func TestRunningLogKeepsHistoryAndScrolls(t *testing.T) {
	w := &wizard{phase: phaseRunning, height: 10, bar: progressBarForTest()}
	for i := 1; i <= 20; i++ {
		w.appendLog(fmt.Sprintf("line-%02d", i))
	}
	if len(w.logBuf) != 20 {
		t.Fatalf("log lines = %d, want 20", len(w.logBuf))
	}
	view := w.View()
	if !strings.Contains(view, "line-20") || strings.Contains(view, "line-01") {
		t.Fatalf("running view should start at latest logs:\n%s", view)
	}
	for range 20 {
		_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyUp})
		if done {
			t.Fatalf("scrolling log should not close wizard")
		}
	}
	view = w.View()
	if !strings.Contains(view, "line-01") || strings.Contains(view, "line-20") {
		t.Fatalf("running view should scroll to older logs:\n%s", view)
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if done {
		t.Fatalf("jumping to latest log should not close wizard")
	}
	view = w.View()
	if !strings.Contains(view, "line-20") {
		t.Fatalf("running view should jump back to latest logs:\n%s", view)
	}
}

func TestRunningViewFitsAssignedHeightWithWrappedLog(t *testing.T) {
	w := &wizard{phase: phaseRunning, bar: progressBarForTest()}
	w.setSize(32, 10)
	w.appendLog("[dry-run] " + strings.Repeat("long-command ", 20))
	if got := lipgloss.Height(w.View()); got > w.height {
		t.Fatalf("running view height = %d, want <= %d:\n%s", got, w.height, w.View())
	}
}

func progressBarForTest() progress.Model {
	return progress.New(progress.WithDefaultGradient())
}

func TestInstallFormCanGoBackToPreviousField(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New()}
	w.startForm()
	w.input.SetValue("example.com")
	w.commitField()
	w.input.SetValue("admin@example.com")
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if done {
		t.Fatalf("back should not close wizard")
	}
	if w.fieldIx != 0 {
		t.Fatalf("fieldIx = %d, want 0", w.fieldIx)
	}
	if got := w.input.Value(); got != "example.com" {
		t.Fatalf("restored input = %q, want domain", got)
	}
}
