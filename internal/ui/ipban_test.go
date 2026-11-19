package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

func stubIPBanState(t *testing.T, bans []ipban.Ban) *[]ipBanChange {
	t.Helper()
	applied := &[]ipBanChange{}
	originalLayout, originalLoad, originalApply := ipBanUILayout, ipBanLoadState, ipBanApplyChange
	root := t.TempDir()
	ipBanUILayout = func() paths.Layout { return paths.LayoutForRoot(root) }
	links := []relaylinks.Link{{LandingID: "aa11", RelayID: "bb22", Forwards: []relaylinks.Forward{
		{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
	}}}
	endpoints := []hubctl.RelayEndpoint{
		relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
		relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
		relayEndpoint("bb22", "osaka", "osaka.example.com", 42234),
	}
	ipBanLoadState = func(paths.Layout) ([]ipban.Ban, []relaylinks.Link, []hubctl.RelayEndpoint, error) {
		return bans, links, endpoints, nil
	}
	ipBanApplyChange = func(_ context.Context, _ paths.Layout, change ipBanChange, _ io.Writer, _ func(deploy.Event)) error {
		*applied = append(*applied, change)
		return nil
	}
	t.Cleanup(func() {
		ipBanUILayout, ipBanLoadState, ipBanApplyChange = originalLayout, originalLoad, originalApply
	})
	return applied
}

func typeText(m *ipBanManager, text string) {
	for _, r := range text {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// drain runs the background apply to completion so the recorded change lands.
func drain(t *testing.T, m *ipBanManager, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		run, ok := msg.(runMsg)
		if !ok {
			t.Fatalf("unexpected message %T", msg)
		}
		cmd, _ = m.Update(run)
		if run.done {
			return
		}
	}
}

func TestIPBanAddFlowRecordsAddressAndTickedNodes(t *testing.T) {
	applied := stubIPBanState(t, nil)
	m := newIPBanManager()

	m.Update(keyEnterMsg) // "Ban an IP"
	if m.phase != ipBanPhaseForm {
		t.Fatalf("phase = %v, want form", m.phase)
	}
	typeText(m, "203.0.113.77/24")
	m.Update(keyEnterMsg)
	if m.form.fieldIx != 1 {
		t.Fatalf("address should be accepted, field error %q", m.form.fieldErr)
	}

	if view := m.View(); !strings.Contains(view, "Hub (hub.example.com)") || strings.Contains(view, "default:") {
		t.Fatalf("node list should name the hub by its domain and show no default line:\n%s", view)
	}
	// Nothing ticked is refused.
	m.Update(keyEnterMsg)
	if !strings.Contains(m.form.fieldErr, "at least one node") {
		t.Fatalf("field error = %q", m.form.fieldErr)
	}
	// Tick tokyo, the relayed node.
	m.Update(keyDownMsg)
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.Update(keyEnterMsg)
	if m.phase != ipBanPhaseConfirm {
		t.Fatalf("phase = %v, want confirm (field error %q)", m.phase, m.form.fieldErr)
	}
	view := m.View()
	for _, want := range []string{"203.0.113.0/24", "tokyo", "osaka", "relay ports forwarding to tokyo"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm view lacks %q:\n%s", want, view)
		}
	}

	cmd, _ := m.Update(keyEnterMsg)
	drain(t, m, cmd)
	if len(*applied) != 1 {
		t.Fatalf("applied = %#v", *applied)
	}
	got := (*applied)[0]
	if got.Action != ipBanActionAdd || got.Address != "203.0.113.0/24" || len(got.Nodes) != 1 || got.Nodes[0] != "aa11" {
		t.Fatalf("change = %#v", got)
	}
}

func TestIPBanAddRefusesAnInvalidOrDuplicateAddress(t *testing.T) {
	stubIPBanState(t, []ipban.Ban{{Address: "203.0.113.7", Nodes: []string{"hub"}}})
	m := newIPBanManager()
	m.Update(keyEnterMsg)
	typeText(m, "not-an-ip")
	m.Update(keyEnterMsg)
	if m.form.fieldIx != 0 || m.form.fieldErr == "" {
		t.Fatalf("invalid address accepted (field %d, err %q)", m.form.fieldIx, m.form.fieldErr)
	}
	m.form.input.SetValue("203.0.113.7/32")
	m.Update(keyEnterMsg)
	if !strings.Contains(m.form.fieldErr, "already banned") {
		t.Fatalf("duplicate not refused: %q", m.form.fieldErr)
	}
}

func TestIPBanEditPreselectsCurrentNodes(t *testing.T) {
	applied := stubIPBanState(t, []ipban.Ban{{Address: "203.0.113.7", Nodes: []string{"hub", "bb22"}}})
	m := newIPBanManager()
	m.Update(keyDownMsg) // "Change banned nodes"
	m.Update(keyEnterMsg)
	if m.phase != ipBanPhasePick {
		t.Fatalf("phase = %v, want pick", m.phase)
	}
	m.Update(keyEnterMsg)
	if m.phase != ipBanPhaseForm || m.form.fields[m.form.fieldIx].key != ipBanFieldNodes {
		t.Fatalf("edit should open straight on the node list")
	}
	if view := m.View(); strings.Contains(view, "default:") {
		t.Fatalf("the ticked boxes already show the current list:\n%s", view)
	}
	// Untick the hub (first option), keep osaka, add tokyo.
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.Update(keyDownMsg)
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.Update(keyEnterMsg)
	view := m.View()
	for _, want := range []string{"Newly banned on: tokyo", "Lifted on:       Hub"} {
		if !strings.Contains(view, want) {
			t.Errorf("edit confirm lacks %q:\n%s", want, view)
		}
	}
	cmd, _ := m.Update(keyEnterMsg)
	drain(t, m, cmd)
	if len(*applied) != 1 {
		t.Fatalf("applied = %#v", *applied)
	}
	got := (*applied)[0]
	if got.Action != ipBanActionEdit || got.Address != "203.0.113.7" || strings.Join(got.Nodes, ",") != "aa11,bb22" {
		t.Fatalf("change = %#v", got)
	}
}

func TestIPBanEditWithNoChangeSaysSo(t *testing.T) {
	stubIPBanState(t, []ipban.Ban{{Address: "203.0.113.7", Nodes: []string{"bb22"}}})
	m := newIPBanManager()
	m.Update(keyDownMsg)
	m.Update(keyEnterMsg)
	m.Update(keyEnterMsg)
	m.Update(keyEnterMsg)
	if view := m.View(); !strings.Contains(view, "node list is unchanged") || strings.Contains(view, "Lifted on") {
		t.Fatalf("unchanged edit confirm:\n%s", view)
	}
}

func TestIPBanEnforcementFailureSaysTheBanIsSaved(t *testing.T) {
	stubIPBanState(t, nil)
	ipBanApplyChange = func(context.Context, paths.Layout, ipBanChange, io.Writer, func(deploy.Event)) error {
		return fmt.Errorf("Enforcement: %w", &ipBanEnforcementError{err: errors.New("apply IP bans on osaka: unreachable")})
	}
	m := newIPBanManager()
	m.Update(keyEnterMsg)
	typeText(m, "203.0.113.7")
	m.Update(keyEnterMsg)
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.Update(keyEnterMsg)
	cmd, _ := m.Update(keyEnterMsg)
	drain(t, m, cmd)
	view := m.View()
	for _, want := range []string{"Ban saved, but not applied on every node yet", "retries the failed nodes", "use Reapply", "osaka: unreachable"} {
		if !strings.Contains(view, want) {
			t.Errorf("partial failure view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "IP ban change failed") {
		t.Errorf("a saved ban must not read as a rejected change:\n%s", view)
	}
}

func TestIPBanRegistryFailureIsAPlainFailure(t *testing.T) {
	stubIPBanState(t, nil)
	ipBanApplyChange = func(context.Context, paths.Layout, ipBanChange, io.Writer, func(deploy.Event)) error {
		return errors.New("Ban registry: disk full")
	}
	m := newIPBanManager()
	m.Update(keyEnterMsg)
	typeText(m, "203.0.113.7")
	m.Update(keyEnterMsg)
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.Update(keyEnterMsg)
	cmd, _ := m.Update(keyEnterMsg)
	drain(t, m, cmd)
	if view := m.View(); !strings.Contains(view, "IP ban change failed") {
		t.Fatalf("registry failure view:\n%s", view)
	}
}
