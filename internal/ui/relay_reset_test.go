package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

// relayResetFleet is one spoke fronted by the hub and one fronted by a spoke,
// which is what makes the two halves of the clear — local and over the overlay —
// both reachable from one menu.
func relayResetFleet() ([]relaylinks.Link, []hubctl.RelayEndpoint) {
	forwards := []relaylinks.Forward{{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567}}
	links := []relaylinks.Link{
		{LandingID: "aa11", RelayID: relaylinks.HubNodeID, Forwards: forwards},
		{LandingID: "bb22", RelayID: "cc33", Forwards: forwards},
	}
	endpoints := []hubctl.RelayEndpoint{
		relayEndpoint(relaylinks.HubNodeID, "HUB", "hub.example.com", 9443),
		relayEndpoint("aa11", "tokyo", "tokyo.example.com", 41234),
		relayEndpoint("bb22", "osaka", "osaka.example.com", 42234),
		relayEndpoint("cc33", "seoul", "seoul.example.com", 43234),
	}
	return links, endpoints
}

func recordRelayResets(t *testing.T) (*[]hubReset, *[]spokeReset) {
	t.Helper()
	oldHub, oldSpoke := resetHubMonitorHistory, resetSpokeMonitorHistory
	t.Cleanup(func() { resetHubMonitorHistory, resetSpokeMonitorHistory = oldHub, oldSpoke })
	hub, spoke := &[]hubReset{}, &[]spokeReset{}
	resetHubMonitorHistory = func(dbPath string, scope monitor.ResetScope, target string) error {
		*hub = append(*hub, hubReset{dbPath: dbPath, scope: scope, target: target})
		return nil
	}
	resetSpokeMonitorHistory = func(_ context.Context, node nodes.Node, req nodeapi.MonitorResetRequest) error {
		*spoke = append(*spoke, spokeReset{nodeID: node.ID, request: req})
		return nil
	}
	return hub, spoke
}

func startRelayReset(t *testing.T, rm *relayManager, landingNames ...string) {
	t.Helper()
	rm.startAction(relayActionResetLatency)
	if rm.phase != relayPhaseLanding {
		t.Fatalf("phase = %d, want the landing picker; err=%q", rm.phase, rm.fieldErr)
	}
	for _, name := range landingNames {
		tickRelayCandidate(t, rm, name)
	}
	rm.confirmPick()
	if rm.phase != relayPhaseConfirm {
		t.Fatalf("phase = %d, want the confirmation without a second picker; err=%q", rm.phase, rm.fieldErr)
	}
}

func tickRelayCandidate(t *testing.T, rm *relayManager, landingName string) {
	t.Helper()
	for i, endpoint := range rm.candidates {
		if endpoint.Name != landingName {
			continue
		}
		rm.cursor = i
		if _, done := rm.handleKey(tea.KeyMsg{Type: tea.KeySpace}); done {
			t.Fatal("ticking a link closed the screen")
		}
		return
	}
	t.Fatalf("%q is not on the picker", landingName)
}

// The clearing entry only appears once something is relayed, because there is
// no link to name otherwise.
func TestRelayMenuOffersClearingOnlyWhileALinkExists(t *testing.T) {
	stubRelayState(t, nil, nil)
	if labels := relayActionLabels(newRelayManager()); strings.Contains(strings.Join(labels, ","), "Clear") {
		t.Fatalf("menu = %v on a fleet with no relay", labels)
	}
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	if labels := relayActionLabels(newRelayManager()); !strings.Contains(strings.Join(labels, ","), "Clear relay latency history") {
		t.Fatalf("menu = %v", labels)
	}
}

func relayActionLabels(rm *relayManager) []string {
	labels := make([]string, 0, 4)
	for _, item := range rm.relayActions() {
		if !item.separator {
			labels = append(labels, item.label)
		}
	}
	return labels
}

// A link the hub carries is cleared in the hub's own store, under the probe ID
// the relay's own sampler writes that landing node's rounds under.
func TestRelayLatencyResetClearsTheLinkOnAHubRelay(t *testing.T) {
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	hub, spoke := recordRelayResets(t)

	rm := newRelayManager()
	startRelayReset(t, rm, "tokyo")
	for _, want := range []string{"tokyo", "HUB", "Relay latency history", "cannot be undone"} {
		if view := rm.View(); !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, view)
		}
	}
	drainRelayRun(t, rm)

	if len(*spoke) != 0 {
		t.Fatalf("spoke resets = %#v, want none for a hub relay", *spoke)
	}
	if len(*hub) != 1 {
		t.Fatalf("hub resets = %#v", *hub)
	}
	got := (*hub)[0]
	if got.scope != monitor.ResetScopeRelayLatency || got.target != "relay:aa11" {
		t.Fatalf("hub reset = %#v, want only this link's probe", got)
	}
	if view := rm.View(); !strings.Contains(view, "Relay latency history cleared") {
		t.Fatalf("a clear must not report that the topology changed:\n%s", view)
	}
}

// A link a spoke carries is cleared on that spoke, over the overlay, and the
// hub's own history is left alone.
func TestRelayLatencyResetClearsTheLinkOnASpokeRelay(t *testing.T) {
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	hub, spoke := recordRelayResets(t)
	if err := nodes.Save(relayUILayout(), []nodes.Node{
		{ID: "cc33", Alias: "seoul", WGIP: "10.90.0.5", Token: "t", Installed: true, Monitor: true},
	}); err != nil {
		t.Fatal(err)
	}

	rm := newRelayManager()
	startRelayReset(t, rm, "osaka")
	drainRelayRun(t, rm)

	if len(*hub) != 0 {
		t.Fatalf("hub resets = %#v, want none", *hub)
	}
	if len(*spoke) != 1 {
		t.Fatalf("spoke resets = %#v", *spoke)
	}
	got := (*spoke)[0]
	if got.nodeID != "cc33" || got.request.Scope != nodeapi.MonitorResetRelayLatency || got.request.Target != "relay:bb22" {
		t.Fatalf("spoke reset = %#v, want the relay asked for this link only", got)
	}
}

// A relay whose spoke has gone from the registry is refused before anything is
// deleted, rather than reported as a clear that did nothing.
func TestRelayLatencyResetRefusesARelayThatLeftTheFleet(t *testing.T) {
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	recordRelayResets(t)

	rm := newRelayManager()
	startRelayReset(t, rm, "osaka")
	if cmd := rm.startRun(); cmd != nil {
		t.Fatal("a clear started against a relay that is not in the registry")
	}
	if !strings.Contains(rm.fieldErr, "no longer in the fleet") {
		t.Fatalf("fieldErr = %q", rm.fieldErr)
	}
}

// Ticking both links clears each on the relay that carries it, under that
// link's own probe ID, so one pass covers a fleet-wide tidy-up.
func TestRelayLatencyResetClearsEveryTickedLink(t *testing.T) {
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	hub, spoke := recordRelayResets(t)
	if err := nodes.Save(relayUILayout(), []nodes.Node{
		{ID: "cc33", Alias: "seoul", WGIP: "10.90.0.5", Token: "t", Installed: true, Monitor: true},
	}); err != nil {
		t.Fatal(err)
	}

	rm := newRelayManager()
	rm.setSize(120, 40)
	startRelayReset(t, rm, "tokyo", "osaka")
	for _, want := range []string{"tokyo", "osaka", "via HUB", "via seoul"} {
		if view := rm.View(); !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, view)
		}
	}
	drainRelayRun(t, rm)

	if len(*hub) != 1 || (*hub)[0].target != "relay:aa11" {
		t.Fatalf("hub resets = %#v, want the hub-carried link only", *hub)
	}
	if len(*spoke) != 1 || (*spoke)[0].nodeID != "cc33" || (*spoke)[0].request.Target != "relay:bb22" {
		t.Fatalf("spoke resets = %#v, want the spoke-carried link only", *spoke)
	}
}

// Enter on an untouched picker must not report a clear that deleted nothing.
func TestRelayLatencyResetRefusesAnEmptySelection(t *testing.T) {
	links, endpoints := relayResetFleet()
	stubRelayState(t, links, endpoints)
	hub, spoke := recordRelayResets(t)

	rm := newRelayManager()
	rm.startAction(relayActionResetLatency)
	rm.confirmPick()

	if rm.phase != relayPhaseLanding {
		t.Fatalf("phase = %d, want the picker to hold with an error", rm.phase)
	}
	if !strings.Contains(rm.fieldErr, "at least one link") {
		t.Fatalf("fieldErr = %q", rm.fieldErr)
	}
	if len(*hub) != 0 || len(*spoke) != 0 {
		t.Fatalf("an empty selection cleared something: hub=%#v spoke=%#v", *hub, *spoke)
	}
}

func drainRelayRun(t *testing.T, rm *relayManager) {
	t.Helper()
	if cmd := rm.startRun(); cmd == nil {
		t.Fatalf("startRun returned no command; err=%q", rm.fieldErr)
	}
	for msg := range rm.ch {
		if msg.done {
			if msg.err != nil {
				t.Fatalf("relay latency reset: %v", msg.err)
			}
			rm.runComplete = true
			rm.phase = relayPhaseDone
			return
		}
	}
}
