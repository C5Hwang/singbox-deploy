package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

func relayEndpoint(id, name, domain string, ports ...int) hubctl.RelayEndpoint {
	protocols := []config.Protocol{config.ProtocolHysteria2, config.ProtocolAnyTLS}
	endpoint := hubctl.RelayEndpoint{ID: id, Name: name, Domain: domain, Installed: true}
	for i, port := range ports {
		endpoint.Protocols = append(endpoint.Protocols, hubctl.RelayProtocolPort{Protocol: protocols[i], Port: port})
		endpoint.ReservedPorts = append(endpoint.ReservedPorts, port)
	}
	return endpoint
}

// stubRelayState wires the manager to a fixed fleet and records every change it
// applies, so the flow can be driven without touching a registry or a host.
func stubRelayState(t *testing.T, links []relaylinks.Link, endpoints []hubctl.RelayEndpoint) *[]relayChange {
	t.Helper()
	applied := &[]relayChange{}
	originalLayout, originalLoad, originalApply := relayUILayout, relayLoadState, relayApplyChange
	root := t.TempDir()
	relayUILayout = func() paths.Layout { return paths.LayoutForRoot(root) }
	relayLoadState = func(paths.Layout) ([]relaylinks.Link, []hubctl.RelayEndpoint, error) {
		return links, endpoints, nil
	}
	relayApplyChange = func(_ context.Context, _ paths.Layout, change relayChange, _ io.Writer, _ func(deploy.Event)) error {
		*applied = append(*applied, change)
		return nil
	}
	t.Cleanup(func() {
		relayUILayout, relayLoadState, relayApplyChange = originalLayout, originalLoad, originalApply
	})
	return applied
}

var (
	keyEnterMsg = tea.KeyMsg{Type: tea.KeyEnter}
	keyDownMsg  = tea.KeyMsg{Type: tea.KeyDown}
)

func TestRelayLandingCandidatesHideNodesAlreadyInALink(t *testing.T) {
	stubRelayState(t,
		[]relaylinks.Link{{LandingID: "aa11", RelayID: "bb22", Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
		}}},
		[]hubctl.RelayEndpoint{
			relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
			relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
			relayEndpoint("bb22", "osaka", "osaka.example.com", 42234),
			{ID: "cc33", Name: "pending", Domain: "pending.example.com"},
			relayEndpoint("dd44", "nodomain", ""),
		})
	rm := newRelayManager()

	var names []string
	for _, endpoint := range rm.landingCandidates() {
		names = append(names, endpoint.Name)
	}
	if strings.Join(names, ",") != "HUB" {
		t.Fatalf("landing candidates = %v; a fronted node, a relay, an uninstalled node and one with no domain must all be hidden", names)
	}

	// A node that already relays for someone can take on another landing node,
	// but one that is itself fronted cannot.
	var relays []string
	for _, endpoint := range rm.relayCandidates(relaylinks.HubNodeID) {
		relays = append(relays, endpoint.Name)
	}
	if strings.Join(relays, ",") != "osaka" {
		t.Fatalf("relay candidates = %v", relays)
	}
}

func TestRelayAddFlowGeneratesPortsAndAppliesTheLink(t *testing.T) {
	applied := stubRelayState(t, nil, []hubctl.RelayEndpoint{
		relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443, 9444),
		relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
	})
	rm := newRelayManager()
	rm.setSize(120, 40)

	rm.Update(keyEnterMsg) // Add relay
	if rm.phase != relayPhaseLanding {
		t.Fatalf("phase = %v, want the landing picker (%s)", rm.phase, rm.fieldErr)
	}
	rm.Update(keyEnterMsg) // the hub is the only landing candidate
	if rm.phase != relayPhaseRelay || rm.landingID != relaylinks.HubNodeID {
		t.Fatalf("phase = %v landing = %q (%s)", rm.phase, rm.landingID, rm.fieldErr)
	}
	rm.Update(keyEnterMsg) // tokyo is the only relay candidate
	if rm.phase != relayPhaseConfirm || rm.relayID != "aa11" {
		t.Fatalf("phase = %v relay = %q (%s)", rm.phase, rm.relayID, rm.fieldErr)
	}

	if len(rm.forwards) != 2 {
		t.Fatalf("both of the hub's protocols should be forwarded: %#v", rm.forwards)
	}
	seen := map[int]bool{}
	for _, forward := range rm.forwards {
		if forward.RelayPort == 41234 {
			t.Fatalf("a generated port must not collide with the relay's own: %#v", rm.forwards)
		}
		if seen[forward.RelayPort] {
			t.Fatalf("generated ports must be distinct: %#v", rm.forwards)
		}
		seen[forward.RelayPort] = true
	}
	view := rm.View()
	if !strings.Contains(view, "HUB") || !strings.Contains(view, "tokyo") {
		t.Fatalf("the confirm screen should name both nodes:\n%s", view)
	}
	// The forwards name protocols; the landing node's own ports are looked up
	// from its current state and shown for confirmation.
	if !strings.Contains(view, "→ 9443") || !strings.Contains(view, "→ 9444") {
		t.Fatalf("the confirm screen should show each landing port:\n%s", view)
	}

	rm.Update(keyEnterMsg) // apply
	if rm.phase != relayPhaseRunning {
		t.Fatalf("phase = %v", rm.phase)
	}
	waitForRelayRun(t, rm)
	if len(*applied) != 1 {
		t.Fatalf("applied = %#v", *applied)
	}
	change := (*applied)[0]
	if change.Remove || change.Link.LandingID != relaylinks.HubNodeID || change.Link.RelayID != "aa11" {
		t.Fatalf("change = %#v", change)
	}
	if len(change.Relays) != 1 || change.Relays[0] != "aa11" {
		t.Fatalf("only the new relay needs reinstalling: %#v", change.Relays)
	}
}

// Moving a landing node has to reinstall both relays: the new one gains rules
// and the old one has to lose the ones it still has.
func TestRelayChangeFlowReinstallsBothRelays(t *testing.T) {
	applied := stubRelayState(t,
		[]relaylinks.Link{{LandingID: relaylinks.HubNodeID, RelayID: "aa11", Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		}}},
		[]hubctl.RelayEndpoint{
			relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
			relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
			relayEndpoint("bb22", "osaka", "osaka.example.com", 42234),
		})
	rm := newRelayManager()
	rm.setSize(120, 40)

	rm.Update(keyDownMsg) // Change relay
	rm.Update(keyEnterMsg)
	if rm.phase != relayPhaseLanding {
		t.Fatalf("phase = %v (%s)", rm.phase, rm.fieldErr)
	}
	rm.Update(keyEnterMsg) // the hub is the only fronted node
	if rm.phase != relayPhaseRelay || rm.previousRelayID != "aa11" {
		t.Fatalf("phase = %v previous = %q", rm.phase, rm.previousRelayID)
	}
	rm.Update(keyDownMsg) // move from tokyo to osaka
	rm.Update(keyEnterMsg)
	if rm.phase != relayPhaseConfirm || rm.relayID != "bb22" {
		t.Fatalf("phase = %v relay = %q (%s)", rm.phase, rm.relayID, rm.fieldErr)
	}
	if !strings.Contains(rm.View(), "Replaces") {
		t.Fatalf("the confirm screen should name the relay being left:\n%s", rm.View())
	}

	rm.Update(keyEnterMsg)
	waitForRelayRun(t, rm)
	change := (*applied)[0]
	if len(change.Relays) != 2 || change.Relays[0] != "bb22" || change.Relays[1] != "aa11" {
		t.Fatalf("both relays should be reinstalled: %#v", change.Relays)
	}
}

func TestRelayRemoveFlowWithdrawsTheLink(t *testing.T) {
	applied := stubRelayState(t,
		[]relaylinks.Link{{LandingID: relaylinks.HubNodeID, RelayID: "aa11", Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		}}},
		[]hubctl.RelayEndpoint{
			relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
			relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
		})
	rm := newRelayManager()
	rm.setSize(120, 40)

	rm.Update(keyDownMsg)
	rm.Update(keyDownMsg) // Remove relay
	rm.Update(keyEnterMsg)
	// The picker has to say what it is about to do. Removing and changing are
	// the same screen, and one of them takes the relay away entirely.
	if picker := rm.View(); !strings.Contains(picker, "stop fronting") {
		t.Fatalf("the removal picker should say the relay goes away:\n%s", picker)
	}
	rm.Update(keyEnterMsg) // the hub is the only fronted node
	if rm.phase != relayPhaseConfirm {
		t.Fatalf("removing should skip the relay picker: phase = %v", rm.phase)
	}
	if !strings.Contains(rm.View(), "removed") {
		t.Fatalf("the confirm screen should say the relay goes away:\n%s", rm.View())
	}

	rm.Update(keyEnterMsg)
	waitForRelayRun(t, rm)
	change := (*applied)[0]
	if !change.Remove || change.LandingID != relaylinks.HubNodeID {
		t.Fatalf("change = %#v", change)
	}
	if len(change.Relays) != 1 || change.Relays[0] != "aa11" {
		t.Fatalf("the relay left behind must be reinstalled: %#v", change.Relays)
	}
}

// A fleet with nothing to relay says so on the menu instead of opening an
// empty picker.
func TestRelayAddRefusesAFleetWithNoCandidate(t *testing.T) {
	stubRelayState(t, nil, []hubctl.RelayEndpoint{
		relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
	})
	rm := newRelayManager()
	// The hub is a valid landing node, so the picker opens; it is then the only
	// node in the fleet, so nothing is left that could front it.
	rm.Update(keyEnterMsg)
	if rm.phase != relayPhaseLanding {
		t.Fatalf("phase = %v (%s)", rm.phase, rm.fieldErr)
	}
	rm.Update(keyEnterMsg)
	if rm.phase != relayPhaseLanding || !strings.Contains(rm.fieldErr, "no node can relay") {
		t.Fatalf("phase = %v err = %q", rm.phase, rm.fieldErr)
	}
}

func TestRelayMenuHidesChangeAndRemoveWithoutLinks(t *testing.T) {
	stubRelayState(t, nil, []hubctl.RelayEndpoint{
		relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
		relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
	})
	rm := newRelayManager()
	if len(rm.relayActions()) != 1 {
		t.Fatalf("actions = %#v", rm.relayActions())
	}
	if !strings.Contains(rm.View(), "No node is relayed") {
		t.Fatalf("the menu should say the fleet is served directly:\n%s", rm.View())
	}
}

// waitForRelayRun drains the run channel until the goroutine reports done.
func waitForRelayRun(t *testing.T, rm *relayManager) {
	t.Helper()
	for range 32 {
		msg := <-rm.ch
		rm.Update(msg)
		if msg.done {
			return
		}
	}
	t.Fatal("relay run never completed")
}
