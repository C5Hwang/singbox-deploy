package hubctl

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

// relayHandler is a fake spoke agent that records the relay job it was pushed.
type relayHandler struct{ req nodeapi.RelayRequest }

func (h *relayHandler) Health() nodeapi.HealthResponse {
	return nodeapi.HealthResponse{OK: true, Installed: true, SingBoxActive: true, Domain: "spoke.example.com"}
}
func (h *relayHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error { return nil }
func (h *relayHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error  { return nil }
func (h *relayHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (h *relayHandler) Subscription(string) ([]byte, error) { return nil, nil }
func (h *relayHandler) ApplyRelay(_ context.Context, req nodeapi.RelayRequest, _ io.Writer) error {
	h.req = req
	return nil
}

// fakeResolve keeps the relay tests off the network: the recorded address is
// only a fallback for the relay's own resolver, so a fixed answer is enough.
func fakeResolve(host string) string {
	if host == "" {
		return ""
	}
	return "203.0.113.1"
}

func relayTestHub(t *testing.T) (paths.Layout, deploy.Config) {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	cfg.MonitorPublicPort = 8443
	cfg.MonitorPort = 18080
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	return layout, cfg
}

func TestRelayEndpointsDescribeTheHubAndItsSpokes(t *testing.T) {
	layout, _ := relayTestHub(t)
	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true, MonitorPort: 18081,
		EnabledProtocols: []string{string(config.ProtocolAnyTLS), string(config.ProtocolTUIC)},
		AnyTLSPort:       31000, TUICPort: 31001,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}

	endpoints, err := (&Controller{Layout: layout}).RelayEndpoints()
	if err != nil {
		t.Fatalf("RelayEndpoints: %v", err)
	}
	if len(endpoints) != 2 || endpoints[0].ID != relaylinks.HubNodeID {
		t.Fatalf("endpoints = %#v", endpoints)
	}

	hub := endpoints[0]
	if hub.Domain != "hub.example.com" || len(hub.Protocols) != 1 || hub.Protocols[0].Port != 9443 {
		t.Fatalf("hub endpoint = %#v", hub)
	}
	for _, want := range []int{80, 443, 9443, 8443, 18080} {
		if !containsPort(hub.ReservedPorts, want) {
			t.Fatalf("hub should reserve %d: %#v", want, hub.ReservedPorts)
		}
	}

	spoke, ok := RelayEndpointByID(endpoints, "AA11")
	if !ok {
		t.Fatalf("spoke lookup should be case-insensitive: %#v", endpoints)
	}
	if spoke.Name != "tokyo" || spoke.Domain != "spoke.example.com" || len(spoke.Protocols) != 2 {
		t.Fatalf("spoke endpoint = %#v", spoke)
	}
	for _, want := range []int{31000, 31001, 19091, 18081} {
		if !containsPort(spoke.ReservedPorts, want) {
			t.Fatalf("spoke should reserve %d: %#v", want, spoke.ReservedPorts)
		}
	}
}

func containsPort(ports []int, want int) bool {
	for _, port := range ports {
		if port == want {
			return true
		}
	}
	return false
}

func TestRelayConfigForCarriesEveryLandingTheRelayServes(t *testing.T) {
	endpoints := []RelayEndpoint{
		{ID: relaylinks.HubNodeID, Name: "HUB", Domain: "hub.example.com"},
		{ID: "aa11", Name: "tokyo", Domain: "tokyo.example.com", Protocols: []RelayProtocolPort{
			{Protocol: config.ProtocolAnyTLS, Port: 41234},
		}},
		{ID: "bb22", Name: "osaka", Domain: "osaka.example.com", Protocols: []RelayProtocolPort{
			{Protocol: config.ProtocolTUIC, Port: 41235},
		}},
	}
	links := []relaylinks.Link{
		{LandingID: "aa11", RelayID: relaylinks.HubNodeID, Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
		}},
		{LandingID: "bb22", RelayID: relaylinks.HubNodeID, Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolTUIC, Network: "udp", RelayPort: 34568},
		}},
	}

	ctrl := &Controller{Layout: paths.LayoutForRoot(t.TempDir()), ResolveHostIPv4: fakeResolve}
	cfg, err := ctrl.RelayConfigFor(relaylinks.HubNodeID, links, endpoints)
	if err != nil {
		t.Fatalf("RelayConfigFor: %v", err)
	}
	if len(cfg.Landings) != 2 {
		t.Fatalf("landings = %#v", cfg.Landings)
	}
	if cfg.Landings[0].Host != "tokyo.example.com" || cfg.Landings[0].Name != "tokyo" {
		t.Fatalf("first landing = %#v", cfg.Landings[0])
	}
	if got := cfg.Landings[1].Forwards[0]; got.Network != "udp" || got.ListenPort != 34568 || got.TargetPort != 41235 {
		t.Fatalf("forward = %#v", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("assembled configuration should be installable: %v", err)
	}

	// A node that relays for nobody owes an empty job, which is what withdraws
	// its data plane rather than leaving the previous rules in place.
	empty, err := ctrl.RelayConfigFor("aa11", links, endpoints)
	if err != nil || !empty.Empty() {
		t.Fatalf("RelayConfigFor on a non-relay = %#v (%v)", empty, err)
	}
}

func TestRelayConfigForRefusesALandingThatLeftTheFleet(t *testing.T) {
	links := []relaylinks.Link{{LandingID: "gone", RelayID: relaylinks.HubNodeID, Forwards: []relaylinks.Forward{
		{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
	}}}
	ctrl := &Controller{Layout: paths.LayoutForRoot(t.TempDir()), ResolveHostIPv4: fakeResolve}
	_, err := ctrl.RelayConfigFor(relaylinks.HubNodeID, links, []RelayEndpoint{{ID: relaylinks.HubNodeID}})
	if err == nil || !strings.Contains(err.Error(), "no longer in the fleet") {
		t.Fatalf("RelayConfigFor = %v", err)
	}
}

func TestApplyRelayForPushesTheWholeJobToASpoke(t *testing.T) {
	layout, _ := relayTestHub(t)
	handler := &relayHandler{}
	srv := httptest.NewServer((&nodeapi.Server{Token: "tok", Handler: handler}).Mux())
	defer srv.Close()

	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "relay-node", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
	}); err != nil {
		t.Fatalf("register relay node: %v", err)
	}
	if err := relaylinks.Set(layout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}

	ctrl := &Controller{
		Layout:          layout,
		ResolveHostIPv4: fakeResolve,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if err := ctrl.ApplyRelayFor(context.Background(), "aa11", io.Discard); err != nil {
		t.Fatalf("ApplyRelayFor: %v", err)
	}
	if len(handler.req.Landings) != 1 {
		t.Fatalf("pushed job = %+v", handler.req)
	}
	landing := handler.req.Landings[0]
	if landing.NodeID != relaylinks.HubNodeID || landing.Host != "hub.example.com" {
		t.Fatalf("landing = %+v", landing)
	}
	if len(landing.Forwards) != 1 || landing.Forwards[0].ListenPort != 34568 || landing.Forwards[0].TargetPort != 9443 {
		t.Fatalf("forwards = %+v", landing.Forwards)
	}

	// Removing the link leaves the relay owing nothing, and the withdrawal has
	// to reach the spoke or it keeps forwarding forever.
	if err := relaylinks.Remove(layout, relaylinks.HubNodeID); err != nil {
		t.Fatalf("Remove link: %v", err)
	}
	if err := ctrl.ApplyRelayFor(context.Background(), "aa11", io.Discard); err != nil {
		t.Fatalf("ApplyRelayFor after removal: %v", err)
	}
	if len(handler.req.Landings) != 0 {
		t.Fatalf("withdrawal should carry no landing: %+v", handler.req)
	}
}

func TestApplyRelayRefusesANodeThatIsNotInstalled(t *testing.T) {
	layout, _ := relayTestHub(t)
	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "pending", Domain: "spoke.example.com", WGIP: "10.90.0.2", Token: "tok",
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	err := (&Controller{Layout: layout, ResolveHostIPv4: fakeResolve}).ApplyRelayFor(context.Background(), "aa11", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("ApplyRelayFor = %v", err)
	}
}

// A landing node that runs out of traffic loses its forwarding rules, and with
// them the relay's probe of it. The topology is what keeps the dashboard able to
// show that landing node at all while it waits for the cycle to reset.
func TestRelayTopologyKeepsAStoodDownLink(t *testing.T) {
	layout, _ := relayTestHub(t)
	for _, n := range []nodes.Node{
		{
			ID: "aa11", Alias: "tokyo", Domain: "relay.example.com", WGIP: "10.90.0.2",
			Token: "tok", AgentPort: 19091, Installed: true, MonitorPort: 18081,
			EnabledProtocols: []string{string(config.ProtocolAnyTLS)}, AnyTLSPort: 31000,
		},
		{
			ID: "bb22", Alias: "osaka", Domain: "landing.example.com", WGIP: "10.90.0.3",
			Token: "tok", AgentPort: 19092, Installed: true, MonitorPort: 18082,
			EnabledProtocols: []string{string(config.ProtocolAnyTLS)}, AnyTLSPort: 31001,
		},
	} {
		if err := nodes.Add(layout, n); err != nil {
			t.Fatalf("register node %s: %v", n.ID, err)
		}
	}
	for _, link := range []relaylinks.Link{
		{LandingID: "bb22", RelayID: "aa11", Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567},
		}},
		{LandingID: relaylinks.HubNodeID, RelayID: "aa11", Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		}},
	} {
		if err := relaylinks.Set(layout, link); err != nil {
			t.Fatalf("set link for %s: %v", link.LandingID, err)
		}
	}
	writeSpokeUsage(t, layout, monitor.SourceSummary{ID: "bb22", TotalLimitBytes: 100, TotalRemainingBytes: 0})

	topology, err := (&Controller{Layout: layout}).RelayTopology()
	if err != nil {
		t.Fatalf("RelayTopology: %v", err)
	}
	if len(topology) != 2 {
		t.Fatalf("topology = %#v, want both links", topology)
	}
	byLanding := map[string]monitor.RelayLink{}
	for _, link := range topology {
		byLanding[link.Landing] = link
	}
	stood := byLanding["bb22"]
	if stood.Forwarding || stood.Reason != "landing node is out of quota" {
		t.Fatalf("the exhausted landing node = %#v, want it reported as stood down", stood)
	}
	// Named even though nothing forwards to it: the name is what the row on the
	// relay page is headed with, and it is exactly the one it had while it stood.
	if stood.Name != "osaka" || stood.Relay != "aa11" {
		t.Fatalf("the exhausted landing node = %#v, want it named and attributed", stood)
	}
	// The relay's other link is untouched by its neighbour running out.
	if hub := byLanding[relaylinks.HubNodeID]; !hub.Forwarding || hub.Name != "HUB" || hub.Reason != "" {
		t.Fatalf("the hub's link = %#v, want it still forwarding", hub)
	}
}

// The hub is its own monitor's "local" source, so a link it relays for has to be
// attributed to that ID rather than to the registry's name for it, or the page
// would ask a source that does not exist.
func TestRelayTopologyNamesTheHubAsTheLocalSource(t *testing.T) {
	layout, _ := relayTestHub(t)
	if err := nodes.Add(layout, nodes.Node{
		ID: "bb22", Alias: "osaka", Domain: "landing.example.com", WGIP: "10.90.0.3",
		Token: "tok", AgentPort: 19092, Installed: true, MonitorPort: 18082,
		EnabledProtocols: []string{string(config.ProtocolAnyTLS)}, AnyTLSPort: 31001,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := relaylinks.Set(layout, relaylinks.Link{
		LandingID: "bb22", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34567}},
	}); err != nil {
		t.Fatalf("set link: %v", err)
	}

	topology, err := (&Controller{Layout: layout}).RelayTopology()
	if err != nil {
		t.Fatalf("RelayTopology: %v", err)
	}
	if len(topology) != 1 || topology[0].Relay != "local" {
		t.Fatalf("topology = %#v, want the hub reported as the local source", topology)
	}
}
