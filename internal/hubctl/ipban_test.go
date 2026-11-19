package hubctl

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

// ipBanTopology is a landing node A (tokyo) fronted by relay B (osaka), plus
// the hub serving on its own.
func ipBanTopology() ([]relaylinks.Link, []RelayEndpoint) {
	endpoints := []RelayEndpoint{
		{ID: relaylinks.HubNodeID, Name: "HUB", Domain: "hub.example.com", Installed: true, Protocols: []RelayProtocolPort{
			{Protocol: config.ProtocolHysteria2, Port: 9443},
		}},
		{ID: "aa11", Name: "tokyo", Domain: "tokyo.example.com", Installed: true, Protocols: []RelayProtocolPort{
			{Protocol: config.ProtocolAnyTLS, Port: 41234},
			{Protocol: config.ProtocolTUIC, Port: 41235},
		}},
		{ID: "bb22", Name: "osaka", Domain: "osaka.example.com", Installed: true, Protocols: []RelayProtocolPort{
			{Protocol: config.ProtocolRealityVision, Port: 42000},
		}},
	}
	links := []relaylinks.Link{{LandingID: "aa11", RelayID: "bb22", Forwards: []relaylinks.Forward{
		{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
		{Protocol: config.ProtocolTUIC, Network: "udp", RelayPort: 34568},
	}}}
	return links, endpoints
}

func TestIPBanOnALandingNodeIsEnforcedOnItsRelayToo(t *testing.T) {
	links, endpoints := ipBanTopology()
	bans := []ipban.Ban{{Address: "203.0.113.7", Nodes: []string{"aa11"}}}

	landing := IPBanConfigFor("aa11", bans, links, endpoints)
	if len(landing.Rules) != 1 {
		t.Fatalf("landing rules = %#v", landing.Rules)
	}
	if r := landing.Rules[0]; r.Label != "own protocols" || !equalInts(r.TCP, 41234) || !equalInts(r.UDP, 41235) {
		t.Fatalf("landing rule = %#v", r)
	}

	// The relay masquerades what it forwards, so it is the only place the
	// client's address can still be matched for the relayed path — but only on
	// the ports forwarding to the banned node. Its own protocol stays usable.
	relayCfg := IPBanConfigFor("bb22", bans, links, endpoints)
	if len(relayCfg.Rules) != 1 {
		t.Fatalf("relay rules = %#v", relayCfg.Rules)
	}
	r := relayCfg.Rules[0]
	if r.Label != "relay to tokyo" || !equalInts(r.TCP, 34567) || !equalInts(r.UDP, 34568) {
		t.Fatalf("relay rule = %#v", r)
	}
	if containsPort(r.TCP, 42000) {
		t.Fatalf("a ban on the landing node must not block the relay's own protocol: %#v", r)
	}

	if hub := IPBanConfigFor(relaylinks.HubNodeID, bans, links, endpoints); !hub.Empty() {
		t.Fatalf("an unrelated node owes no rules: %#v", hub)
	}
}

func TestIPBanOnARelayLeavesTheNodesItFrontsReachable(t *testing.T) {
	links, endpoints := ipBanTopology()
	bans := []ipban.Ban{{Address: "203.0.113.7", Nodes: []string{"bb22"}}}

	relayCfg := IPBanConfigFor("bb22", bans, links, endpoints)
	if len(relayCfg.Rules) != 1 {
		t.Fatalf("relay rules = %#v", relayCfg.Rules)
	}
	if r := relayCfg.Rules[0]; r.Label != "own protocols" || !equalInts(r.TCP, 42000) || len(r.UDP) != 0 {
		t.Fatalf("a relay ban covers only the relay's own protocol ports: %#v", r)
	}
	if landing := IPBanConfigFor("aa11", bans, links, endpoints); !landing.Empty() {
		t.Fatalf("the fronted node is not banned: %#v", landing)
	}
}

func TestIPBanOnBothEndsCombinesTheRules(t *testing.T) {
	links, endpoints := ipBanTopology()
	bans := []ipban.Ban{
		{Address: "203.0.113.7", Nodes: []string{"aa11", "bb22"}},
		{Address: "198.51.100.0/24", Nodes: []string{"aa11"}},
	}
	relayCfg := IPBanConfigFor("bb22", bans, links, endpoints)
	if len(relayCfg.Rules) != 2 {
		t.Fatalf("relay rules = %#v", relayCfg.Rules)
	}
	if got := relayCfg.Rules[0].Addresses; len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("own-protocol addresses = %v", got)
	}
	if got := relayCfg.Rules[1].Addresses; len(got) != 2 {
		t.Fatalf("relay-port addresses should carry both of the landing node's bans: %v", got)
	}
}

// ipBanHandler is a fake spoke agent recording every ban rule set pushed.
type ipBanHandler struct {
	relayHandler
	pushes []nodeapi.IPBanRequest
}

func (h *ipBanHandler) ApplyIPBan(_ context.Context, req nodeapi.IPBanRequest, _ io.Writer) error {
	h.pushes = append(h.pushes, req)
	return nil
}

func TestReconcileIPBansPushesOnlyWhatChanged(t *testing.T) {
	layout, _ := relayTestHub(t)
	handler := &ipBanHandler{}
	srv := httptest.NewServer((&nodeapi.Server{Token: "tok", Handler: handler}).Mux())
	defer srv.Close()
	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
		EnabledProtocols: []string{string(config.ProtocolAnyTLS)}, AnyTLSPort: 41234,
	}); err != nil {
		t.Fatalf("register spoke: %v", err)
	}

	var hubLoads []string
	ctrl := &Controller{
		Layout:          layout,
		ResolveHostIPv4: fakeResolve,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
		NewIPBanApplier: func() *ipban.Applier {
			return &ipban.Applier{
				Layout:     layout,
				SystemdDir: t.TempDir(),
				Runner:     &noopRunner{},
				NFT: func(_ context.Context, stdin string, _ ...string) ([]byte, error) {
					hubLoads = append(hubLoads, stdin)
					return nil, nil
				},
			}
		},
	}
	ctx := context.Background()

	// No bans and nothing ever applied: no node is contacted, so an agent that
	// predates bans is never asked about them.
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans (empty): %v", err)
	}
	if len(handler.pushes) != 0 || len(hubLoads) != 0 {
		t.Fatalf("empty reconcile contacted nodes: %d pushes, %d hub loads", len(handler.pushes), len(hubLoads))
	}

	if err := ipban.AddBan(layout, ipban.Ban{Address: "203.0.113.7", Nodes: []string{"aa11"}}); err != nil {
		t.Fatalf("AddBan: %v", err)
	}
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans: %v", err)
	}
	if len(handler.pushes) != 1 || len(hubLoads) != 0 {
		t.Fatalf("ban on the spoke: %d pushes, %d hub loads", len(handler.pushes), len(hubLoads))
	}
	pushed := handler.pushes[0]
	if len(pushed.Rules) != 1 || !equalInts(pushed.Rules[0].TCP, 41234) || pushed.Rules[0].Addresses[0] != "203.0.113.7" {
		t.Fatalf("pushed = %#v", pushed)
	}

	// Nothing changed, so nothing is pushed again.
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans (steady): %v", err)
	}
	if len(handler.pushes) != 1 {
		t.Fatalf("steady state re-pushed: %d", len(handler.pushes))
	}

	// Banning on the hub as well reaches only the hub.
	if err := ipban.SetBan(layout, ipban.Ban{Address: "203.0.113.7", Nodes: []string{"aa11", "hub"}}); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans (hub): %v", err)
	}
	if len(handler.pushes) != 1 || len(hubLoads) != 1 || !strings.Contains(hubLoads[0], "udp dport { 9443 } drop") {
		t.Fatalf("hub ban: %d pushes, hub loads %q", len(handler.pushes), hubLoads)
	}

	// A spoke port that moves underneath the ban is followed.
	if err := nodes.Mutate(layout, "aa11", func(n *nodes.Node) error { n.AnyTLSPort = 41299; return nil }); err != nil {
		t.Fatalf("move port: %v", err)
	}
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans (moved): %v", err)
	}
	if len(handler.pushes) != 2 || !equalInts(handler.pushes[1].Rules[0].TCP, 41299) {
		t.Fatalf("moved port not followed: %#v", handler.pushes)
	}

	// Lifting the ban sends the spoke an empty rule set, which removes its table.
	if err := ipban.RemoveBan(layout, "203.0.113.7"); err != nil {
		t.Fatalf("RemoveBan: %v", err)
	}
	if err := ctrl.ReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ReconcileIPBans (lifted): %v", err)
	}
	if len(handler.pushes) != 3 || len(handler.pushes[2].Rules) != 0 {
		t.Fatalf("lift = %#v", handler.pushes)
	}
	if len(hubLoads) != 2 || strings.Contains(hubLoads[1], "drop") {
		t.Fatalf("hub lift loads = %q", hubLoads)
	}

	// Force pushes even what is recorded as applied, but a node owed nothing
	// that never had anything is still left alone.
	if err := ctrl.ForceReconcileIPBans(ctx, io.Discard); err != nil {
		t.Fatalf("ForceReconcileIPBans: %v", err)
	}
	if len(handler.pushes) != 3 {
		t.Fatalf("force re-pushed an empty set to a node with nothing recorded: %d", len(handler.pushes))
	}
}

func equalInts(got []int, want ...int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
