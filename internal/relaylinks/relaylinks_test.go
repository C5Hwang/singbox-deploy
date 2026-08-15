package relaylinks_test

import (
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

func testLayout(t *testing.T) paths.Layout {
	t.Helper()
	return paths.LayoutForRoot(t.TempDir())
}

func link(landing, relay string, forwards ...relaylinks.Forward) relaylinks.Link {
	return relaylinks.Link{LandingID: landing, RelayID: relay, Forwards: forwards}
}

func tcpForward(protocol config.Protocol, relayPort int) relaylinks.Forward {
	return relaylinks.Forward{Protocol: protocol, Network: "tcp", RelayPort: relayPort}
}

func TestSetLoadRoundTrip(t *testing.T) {
	layout := testLayout(t)
	want := link("aa11", relaylinks.HubNodeID,
		tcpForward(config.ProtocolRealityVision, 30001),
		relaylinks.Forward{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 30002},
	)
	if err := relaylinks.Set(layout, want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	list, err := relaylinks.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Load = %#v", list)
	}
	got, ok := relaylinks.Find(list, "AA11")
	if !ok {
		t.Fatalf("Find should match a landing ID regardless of case: %#v", list)
	}
	if got.RelayID != relaylinks.HubNodeID || len(got.Forwards) != 2 {
		t.Fatalf("round trip = %#v", got)
	}
	if got.Forwards[1] != want.Forwards[1] {
		t.Fatalf("forward round trip = %#v, want %#v", got.Forwards[1], want.Forwards[1])
	}
}

// Re-pointing a landing node at a different relay replaces its entry rather
// than leaving it fronted by two relays at once.
func TestSetReplacesTheExistingRelay(t *testing.T) {
	layout := testLayout(t)
	if err := relaylinks.Set(layout, link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001))); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := relaylinks.Set(layout, link("aa11", "cc33", tcpForward(config.ProtocolAnyTLS, 30009))); err != nil {
		t.Fatalf("Set replacement: %v", err)
	}
	list, err := relaylinks.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 || list[0].RelayID != "cc33" || list[0].Forwards[0].RelayPort != 30009 {
		t.Fatalf("replacement = %#v", list)
	}
}

func TestValidateRefusesSelfRelay(t *testing.T) {
	err := relaylinks.Validate(nil, link("aa11", "aa11", tcpForward(config.ProtocolAnyTLS, 30001)))
	if err == nil || !strings.Contains(err.Error(), "cannot relay for itself") {
		t.Fatalf("self relay error = %v", err)
	}
}

// Chaining is refused from both ends: neither A→B→C nor C→B→A may be built by
// adding one link to an existing one.
func TestValidateRefusesChaining(t *testing.T) {
	existing := []relaylinks.Link{link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001))}

	err := relaylinks.Validate(existing, link("bb22", "cc33", tcpForward(config.ProtocolAnyTLS, 30002)))
	if err == nil || !strings.Contains(err.Error(), "already relays for another node") {
		t.Fatalf("relaying an existing relay = %v", err)
	}

	err = relaylinks.Validate(existing, link("cc33", "aa11", tcpForward(config.ProtocolAnyTLS, 30002)))
	if err == nil || !strings.Contains(err.Error(), "already relayed") {
		t.Fatalf("relaying through an existing landing node = %v", err)
	}
}

func TestValidateRefusesAPortAlreadyForwardedOnTheSameRelay(t *testing.T) {
	existing := []relaylinks.Link{link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001))}
	err := relaylinks.Validate(existing, link("cc33", "bb22", tcpForward(config.ProtocolAnyTLS, 30001)))
	if err == nil || !strings.Contains(err.Error(), "already forwarding") {
		t.Fatalf("duplicate relay port = %v", err)
	}
	// The same number on a different relay is a different socket entirely.
	if err := relaylinks.Validate(existing, link("cc33", "dd44", tcpForward(config.ProtocolAnyTLS, 30001))); err != nil {
		t.Fatalf("same port on another relay: %v", err)
	}
}

func TestValidateRefusesAMismatchedNetwork(t *testing.T) {
	err := relaylinks.Validate(nil, link("aa11", "bb22",
		relaylinks.Forward{Protocol: config.ProtocolHysteria2, Network: "tcp", RelayPort: 30001}))
	if err == nil || !strings.Contains(err.Error(), "udp protocol") {
		t.Fatalf("mismatched network = %v", err)
	}
}

func TestAllocateForwardsAvoidsReservedAndClaimedPorts(t *testing.T) {
	// Leave exactly three free ports in the generator's range so the allocation
	// has to find them rather than stumble on them.
	var reserved []int
	for port := 20000; port <= 59999; port++ {
		switch port {
		case 30001, 30002, 30003, 30004:
		default:
			reserved = append(reserved, port)
		}
	}
	existing := []relaylinks.Link{link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001))}

	forwards, err := relaylinks.AllocateForwards(existing, "bb22", reserved, []relaylinks.Target{
		{Protocol: config.ProtocolRealityVision},
		{Protocol: config.ProtocolHysteria2},
		{Protocol: config.ProtocolTUIC},
	})
	if err != nil {
		t.Fatalf("AllocateForwards: %v", err)
	}
	if len(forwards) != 3 {
		t.Fatalf("forwards = %#v", forwards)
	}
	seen := map[int]bool{}
	for _, f := range forwards {
		if f.RelayPort == 30001 {
			t.Fatalf("allocation reused a port already forwarding on that relay: %#v", forwards)
		}
		if f.RelayPort < 30002 || f.RelayPort > 30004 {
			t.Fatalf("allocation used a reserved port: %#v", forwards)
		}
		if seen[f.RelayPort] {
			t.Fatalf("allocation repeated a port: %#v", forwards)
		}
		seen[f.RelayPort] = true
	}
	if forwards[1].Network != "udp" || forwards[0].Network != "tcp" {
		t.Fatalf("networks = %#v", forwards)
	}
	if err := relaylinks.Validate(existing, link("cc33", "bb22", forwards...)); err != nil {
		t.Fatalf("a generated link should validate: %v", err)
	}
}

func TestAllocateForwardsReportsAnExhaustedRange(t *testing.T) {
	reserved := make([]int, 0, 40000)
	for port := 20000; port <= 59999; port++ {
		reserved = append(reserved, port)
	}
	_, err := relaylinks.AllocateForwards(nil, "bb22", reserved, []relaylinks.Target{
		{Protocol: config.ProtocolAnyTLS},
	})
	if err == nil || !strings.Contains(err.Error(), "already in use on this relay") {
		t.Fatalf("exhausted range = %v", err)
	}
}

func TestAllocateForwardsRejectsUnsupportedProtocol(t *testing.T) {
	_, err := relaylinks.AllocateForwards(nil, "bb22", nil, []relaylinks.Target{{Protocol: "shadowsocks"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Fatalf("unsupported protocol = %v", err)
	}
}

func TestDropNodeClearsBothSidesAndReportsOrphanedRelays(t *testing.T) {
	layout := testLayout(t)
	for _, l := range []relaylinks.Link{
		link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001)),
		link("cc33", "bb22", tcpForward(config.ProtocolAnyTLS, 30002)),
		link("dd44", "ee55", tcpForward(config.ProtocolAnyTLS, 30003)),
	} {
		if err := relaylinks.Set(layout, l); err != nil {
			t.Fatalf("Set %s: %v", l.LandingID, err)
		}
	}

	orphaned, err := relaylinks.DropNode(layout, "aa11")
	if err != nil {
		t.Fatalf("DropNode landing: %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != "bb22" {
		t.Fatalf("dropping a landing node should name the relay left with stale rules: %#v", orphaned)
	}

	if _, err := relaylinks.DropNode(layout, "bb22"); err != nil {
		t.Fatalf("DropNode relay: %v", err)
	}
	list, err := relaylinks.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 || list[0].LandingID != "dd44" {
		t.Fatalf("dropping a relay should remove every link it served: %#v", list)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	layout := testLayout(t)
	if err := relaylinks.Remove(layout, "aa11"); err != nil {
		t.Fatalf("Remove on an empty registry: %v", err)
	}
	if err := relaylinks.Set(layout, link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001))); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := relaylinks.Remove(layout, "AA11"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := relaylinks.Remove(layout, "aa11"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	list, err := relaylinks.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("Load after Remove = %#v", list)
	}
}

func TestServedByAndRoleQueries(t *testing.T) {
	list := []relaylinks.Link{
		link("aa11", "bb22", tcpForward(config.ProtocolAnyTLS, 30001)),
		link("cc33", "bb22", tcpForward(config.ProtocolAnyTLS, 30002)),
	}
	if served := relaylinks.ServedBy(list, "BB22"); len(served) != 2 {
		t.Fatalf("ServedBy = %#v", served)
	}
	if !relaylinks.IsRelay(list, "bb22") || relaylinks.IsRelay(list, "aa11") {
		t.Fatal("IsRelay should hold only for the forwarding node")
	}
	if !relaylinks.IsLanding(list, "cc33") || relaylinks.IsLanding(list, "bb22") {
		t.Fatal("IsLanding should hold only for the fronted nodes")
	}
	if relaylinks.IsRelay(list, "") || relaylinks.IsLanding(list, "") {
		t.Fatal("an empty node ID must never match a link")
	}
}

// A hand-edited registry must still load: validation is what refuses a link
// with nothing usable left, which reads better than a registry that will not
// open at all.
func TestMalformedForwardRecordsAreDropped(t *testing.T) {
	layout := testLayout(t)
	if err := relaylinks.Save(layout, []relaylinks.Link{{
		LandingID: "aa11",
		RelayID:   "bb22",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 30001},
		},
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	list, err := relaylinks.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 || len(list[0].Forwards) != 1 {
		t.Fatalf("Load = %#v", list)
	}
}
