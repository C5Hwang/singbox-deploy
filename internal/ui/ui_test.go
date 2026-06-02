package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func TestDryRunStopsAtCommandReviewBeforeComplete(t *testing.T) {
	w := &wizard{phase: phaseRunning, dryRun: true}
	w.handleRun(runMsg{dryRunCommand: "systemctl restart nginx"})
	w.handleRun(runMsg{done: true})
	if w.phase != phaseDryRunReview {
		t.Fatalf("phase = %v, want dry-run review", w.phase)
	}
	view := w.View()
	if !strings.Contains(view, "Install · Dry-run commands") || !strings.Contains(view, "systemctl restart nginx") {
		t.Fatalf("review view missing command:\n%s", view)
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done {
		t.Fatalf("review should continue to completion screen, not close wizard")
	}
	if w.phase != phaseDone {
		t.Fatalf("phase after review key = %v, want done", w.phase)
	}
}
