package ui

import "testing"

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
