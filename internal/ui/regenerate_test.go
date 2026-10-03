package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type regenerateCall struct {
	includeHub bool
	spokeIDs   []string
}

// withRegenerateDeps stubs the page's host, registry and backend. The registry
// holds one installed and one uninstalled spoke; run answers every call.
func withRegenerateDeps(t *testing.T, run func(regenerateCall) ([]hubctl.RegenerateResult, error)) *[]regenerateCall {
	t.Helper()
	oldLayout, oldDetect, oldHub, oldLoad, oldRun := regenerateUILayout, detectRegenerateHost, regenerateHubPresent, regenerateLoadNodes, regenerateConfigsRun
	t.Cleanup(func() {
		regenerateUILayout, detectRegenerateHost, regenerateHubPresent, regenerateLoadNodes, regenerateConfigsRun = oldLayout, oldDetect, oldHub, oldLoad, oldRun
	})
	regenerateUILayout = func() paths.Layout { return paths.LayoutForRoot(t.TempDir()) }
	detectRegenerateHost = func() (system.Host, error) { return supportedTestHost(), nil }
	regenerateHubPresent = func(paths.Layout) bool { return true }
	regenerateLoadNodes = func(paths.Layout) ([]nodes.Node, error) {
		return []nodes.Node{
			{ID: "001", Alias: "shanghai", Domain: "cn.example.com", Installed: true},
			{ID: "002", Alias: "pending", Installed: false},
		}, nil
	}
	var calls []regenerateCall
	regenerateConfigsRun = func(_ context.Context, _ paths.Layout, includeHub bool, spokeIDs []string, _ io.Writer, _ func(deploy.Event)) ([]hubctl.RegenerateResult, error) {
		call := regenerateCall{includeHub: includeHub, spokeIDs: spokeIDs}
		calls = append(calls, call)
		return run(call)
	}
	return &calls
}

// drainRegenerateRun feeds the run's messages back until it finishes.
func drainRegenerateRun(rm *regenerateManager, cmd tea.Cmd) {
	for cmd != nil {
		cmd, _ = rm.Update(cmd())
	}
}

func TestRegenerateMenuEntryListsHubAndInstalledSpokesUnselected(t *testing.T) {
	withRegenerateDeps(t, nil)
	m := NewModel()
	m.SetSize(180, 40)
	setMenuCursor(t, m, "Regenerate node configs")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.regenerate == nil {
		t.Fatal("regenerate manager was not opened")
	}
	view := m.View()
	for _, want := range []string{"[ ] Hub", "[ ] shanghai", "cn.example.com"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "pending") {
		t.Fatalf("an uninstalled spoke should not be offered:\n%s", view)
	}
}

func TestRegenerateRequiresASelection(t *testing.T) {
	calls := withRegenerateDeps(t, nil)
	rm := newRegenerateManager()
	rm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if rm.phase != regeneratePhaseSelect || !strings.Contains(rm.View(), "select at least one node") {
		t.Fatalf("phase = %v, want to stay on selection with an error:\n%s", rm.phase, rm.View())
	}
	if len(*calls) != 0 {
		t.Fatalf("backend called without a selection: %+v", *calls)
	}
}

func TestRegenerateToggleAllSelectsThenClears(t *testing.T) {
	withRegenerateDeps(t, nil)
	rm := newRegenerateManager()
	rm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := rm.selectedKeys(); len(got) != 2 {
		t.Fatalf("selected = %v, want every node", got)
	}
	rm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if got := rm.selectedKeys(); len(got) != 0 {
		t.Fatalf("selected = %v, want none after toggling all again", got)
	}
}

func TestRegenerateRunsOnlyTheChosenSpoke(t *testing.T) {
	calls := withRegenerateDeps(t, func(regenerateCall) ([]hubctl.RegenerateResult, error) {
		return []hubctl.RegenerateResult{{Name: "shanghai"}}, nil
	})
	rm := newRegenerateManager()
	rm.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	rm.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	rm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if rm.phase != regeneratePhaseConfirm || !strings.Contains(rm.View(), "shanghai") {
		t.Fatalf("want the confirm screen naming the spoke, phase=%v:\n%s", rm.phase, rm.View())
	}
	cmd, _ := rm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	drainRegenerateRun(rm, cmd)

	if len(*calls) != 1 || (*calls)[0].includeHub || strings.Join((*calls)[0].spokeIDs, ",") != "001" {
		t.Fatalf("calls = %+v, want only spoke 001 without the Hub", *calls)
	}
	rm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if view := rm.View(); !strings.Contains(view, "Complete") || !strings.Contains(view, "regenerated") {
		t.Fatalf("done view should report success:\n%s", view)
	}
}

func TestRegenerateReportsEachNodesOutcomeAfterAFailure(t *testing.T) {
	withRegenerateDeps(t, func(regenerateCall) ([]hubctl.RegenerateResult, error) {
		failure := errors.New("agent unreachable")
		return []hubctl.RegenerateResult{{Name: "Hub"}, {Name: "shanghai", Err: failure}}, failure
	})
	rm := newRegenerateManager()
	rm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	rm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	cmd, _ := rm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	drainRegenerateRun(rm, cmd)

	view := rm.View()
	for _, want := range []string{"Some nodes failed", "regenerated", "failed: agent unreachable"} {
		if !strings.Contains(view, want) {
			t.Fatalf("done view missing %q:\n%s", want, view)
		}
	}
}
