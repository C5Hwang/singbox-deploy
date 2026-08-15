package hubctl

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// relayFleet is a hub publishing its own Hysteria2 node plus one spoke's, with
// the spoke's real subscription bodies served by a fake agent.
type relayFleet struct {
	hubLayout paths.Layout
	hubCfg    deploy.Config
	spokeCfg  deploy.Config
	ctrl      *Controller
}

func newRelayFleet(t *testing.T) *relayFleet {
	t.Helper()
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := deploy.WriteSubscriptions(hubLayout, hubCfg); err != nil {
		t.Fatalf("hub WriteSubscriptions: %v", err)
	}

	spokeLayout := paths.LayoutForRoot(t.TempDir())
	spokeCfg := hysteriaConfig(t, "spoke.example.com", "SPOKE", "spokesalt", 8443)
	if err := deploy.WriteSubscriptions(spokeLayout, spokeCfg); err != nil {
		t.Fatalf("spoke WriteSubscriptions: %v", err)
	}
	srv := httptest.NewServer((&nodeapi.Server{
		Token:   "tok",
		Handler: &subHandler{layout: spokeLayout, salt: spokeCfg.Salt},
	}).Mux())
	t.Cleanup(srv.Close)

	if err := nodes.Add(hubLayout, nodes.Node{
		ID: "aa11", Alias: "tokyo", SubscriptionAlias: "tokyo", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", AgentPort: 19091, Installed: true,
		EnabledProtocols: []string{string(config.ProtocolHysteria2)}, Hysteria2Port: 8443,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}

	return &relayFleet{
		hubLayout: hubLayout,
		hubCfg:    hubCfg,
		spokeCfg:  spokeCfg,
		ctrl: &Controller{
			Layout:          hubLayout,
			ResolveHostIPv4: fakeResolve,
			// The hub applies its own relay ruleset locally, and a test must
			// never load one into the machine it is running on.
			NewRelayApplier: func() *relay.Applier { return recordingApplier(hubLayout, t) },
			NewClient: func(n nodes.Node) *nodeapi.Client {
				return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
			},
		},
	}
}

func (f *relayFleet) publish(t *testing.T) (links string, clash string, singbox string) {
	t.Helper()
	if err := f.ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	token := deploy.SubscriptionToken(f.hubCfg.Salt)
	read := func(dir string) string {
		body, err := os.ReadFile(filepath.Join(f.hubLayout.SubscribeDir, dir, token))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		return string(body)
	}
	decoded, err := subscription.DecodeBase64(read("default"))
	if err != nil {
		t.Fatalf("decode default: %v", err)
	}
	return decoded, read("clashMeta"), read("singboxProfiles")
}

// The spoke is fronted by the hub: every published format must name the hub's
// address on the relay port while the node's name and SNI still describe the
// spoke, which is what makes the swap invisible to a client.
func TestRefreshSubscriptionsPublishesARelayedSpokeUnderTheRelayAddress(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: "aa11", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}

	links, clash, singbox := f.publish(t)
	if !strings.Contains(links, "@hub.example.com:34568?") {
		t.Fatalf("the spoke's link should dial the relay:\n%s", links)
	}
	if !strings.Contains(links, "sni=spoke.example.com") {
		t.Fatalf("the SNI must still name the landing node:\n%s", links)
	}
	if !strings.Contains(links, "@hub.example.com:9443?") {
		t.Fatalf("the hub's own node must keep its own port:\n%s", links)
	}
	if !strings.Contains(links, "tokyo") {
		t.Fatalf("the node name must not change:\n%s", links)
	}
	if !strings.Contains(clash, "server: hub.example.com\n    port: 34568") {
		t.Fatalf("the Clash proxy should dial the relay:\n%s", clash)
	}
	if !strings.Contains(clash, "sni: spoke.example.com") {
		t.Fatalf("the Clash SNI must still name the landing node:\n%s", clash)
	}
	if !relayedOutbound(t, singbox, "tokyo", "hub.example.com", 34568, "spoke.example.com") {
		t.Fatalf("the sing-box outbound should dial the relay:\n%s", singbox)
	}
}

// Withdrawing the link republishes the landing node's own address, with nothing
// else about the node changed, so a client recovers by refetching alone.
func TestRefreshSubscriptionsFallsBackWhenTheRelayLinkIsRemoved(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: "aa11", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	before, _, _ := f.publish(t)

	if err := relaylinks.Remove(f.hubLayout, "aa11"); err != nil {
		t.Fatalf("Remove link: %v", err)
	}
	after, _, singbox := f.publish(t)

	if !strings.Contains(after, "@spoke.example.com:8443?") {
		t.Fatalf("the spoke should be published directly again:\n%s", after)
	}
	if strings.Contains(after, ":34568") {
		t.Fatalf("no relay port should survive the withdrawal:\n%s", after)
	}
	if countLinks(before) != countLinks(after) {
		t.Fatalf("the node list must keep its shape:\nbefore %s\nafter %s", before, after)
	}
	if !relayedOutbound(t, singbox, "tokyo", "spoke.example.com", 8443, "spoke.example.com") {
		t.Fatalf("the sing-box outbound should point back at the landing node:\n%s", singbox)
	}
}

// The hub itself can be the landing node, and its own generated nodes go
// through the same rewrite as a fetched spoke's.
func TestRefreshSubscriptionsPublishesARelayedHubUnderItsRelay(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}

	links, _, _ := f.publish(t)
	if !strings.Contains(links, "@spoke.example.com:34568?") {
		t.Fatalf("the hub's node should dial its relay:\n%s", links)
	}
	if !strings.Contains(links, "sni=hub.example.com") {
		t.Fatalf("the hub's SNI must still name the hub:\n%s", links)
	}
	if !strings.Contains(links, "@spoke.example.com:8443?") {
		t.Fatalf("the spoke's own node must keep its own port:\n%s", links)
	}
}

// A relay whose agent was never installed has no ruleset, so publishing its
// address would black-hole the landing node.
func TestRefreshSubscriptionsIgnoresARelayThatIsNotInstalled(t *testing.T) {
	f := newRelayFleet(t)
	if err := nodes.Mutate(f.hubLayout, "aa11", func(n *nodes.Node) error {
		n.Installed = false
		return nil
	}); err != nil {
		t.Fatalf("mark node pending: %v", err)
	}
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}

	links, _, _ := f.publish(t)
	if !strings.Contains(links, "@hub.example.com:9443?") || strings.Contains(links, ":34568") {
		t.Fatalf("the hub should still publish its own address:\n%s", links)
	}
}

func countLinks(body string) int {
	return strings.Count(body, "hysteria2://")
}

// relayedOutbound reports whether the sing-box profile carries an outbound
// whose tag contains alias, dialing host:port with the given SNI.
func relayedOutbound(t *testing.T, profile, alias, host string, port int, sni string) bool {
	t.Helper()
	var root struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(profile), &root); err != nil {
		t.Fatalf("decode sing-box profile: %v", err)
	}
	for _, ob := range root.Outbounds {
		tag, _ := ob["tag"].(string)
		if !strings.Contains(tag, alias) {
			continue
		}
		serverPort, _ := ob["server_port"].(float64)
		if ob["server"] != host || int(serverPort) != port {
			continue
		}
		tls, _ := ob["tls"].(map[string]any)
		if tls != nil && tls["server_name"] == sni {
			return true
		}
	}
	return false
}

// recordingApplier installs a relay ruleset nowhere: it accepts every command
// and every nft load, so the hub's own relay path can be exercised without
// touching the host's firewall or nftables.
func recordingApplier(layout paths.Layout, t *testing.T) *relay.Applier {
	return &relay.Applier{
		Layout:     layout,
		Bin:        "/usr/bin/singbox-deploy",
		SystemdDir: t.TempDir(),
		Firewall:   system.FirewallNone,
		Runner:     noopRunner{},
		NFT:        func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Resolve:    func(_ context.Context, host string) (string, error) { return fakeResolve(host), nil },
	}
}

type noopRunner struct{}

func (noopRunner) Run(system.Command) error { return nil }
