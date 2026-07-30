package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func removeConfirmationManager(t *testing.T) *nodeManager {
	t.Helper()
	m := newNodeManager()
	m.layout = paths.LayoutForRoot(t.TempDir())
	m.hubReady = true
	m.list = []nodes.Node{{
		ID:        "node-id",
		Alias:     "london",
		Domain:    "uk.example.com",
		WGIP:      "127.0.0.1",
		AgentPort: 1,
	}}
	m.actionCur = 1

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseDeletePick {
		t.Fatalf("remove action did not open picker: phase=%d", m.phase)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseRemoveConfirm {
		t.Fatalf("picker did not open remove confirmation: phase=%d", m.phase)
	}
	return m
}

func TestRemoveSpokeRequiresExplicitYConfirmation(t *testing.T) {
	m := removeConfirmationManager(t)

	view := m.View()
	for _, want := range []string{
		"london (uk.example.com)",
		"removes its proxy runtime",
		"proxy runtime",
		"Agent",
		"WireGuard",
		"Press y",
		"n/Esc",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("remove confirmation missing %q:\n%s", want, view)
		}
	}

	// Enter is intentionally not affirmative for this destructive operation.
	cmd, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.phase != nodePhaseRemoveConfirm {
		t.Fatalf("Enter started removal: phase=%d cmd=%v", m.phase, cmd != nil)
	}

	cmd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || m.phase != nodePhaseRunning || m.action != "Removing node" {
		t.Fatalf("explicit y did not start removal: phase=%d action=%q cmd=%v", m.phase, m.action, cmd != nil)
	}
	if m.pendingRemove.ID != "" {
		t.Fatalf("confirmed node remained pending: %+v", m.pendingRemove)
	}
}

func TestRemoveSpokeConfirmationCanBeCancelled(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyEsc},
	} {
		m := removeConfirmationManager(t)
		cmd, _ := m.Update(key)
		if cmd != nil || m.phase != nodePhaseList {
			t.Errorf("%q did not cancel removal: phase=%d cmd=%v", key.String(), m.phase, cmd != nil)
		}
		if m.pendingRemove.ID != "" {
			t.Errorf("%q left a pending removal: %+v", key.String(), m.pendingRemove)
		}
	}
}
