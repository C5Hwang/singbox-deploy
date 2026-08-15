package hubctl

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

func writeSpokeUsage(t *testing.T, layout paths.Layout, sources ...monitor.SourceSummary) {
	t.Helper()
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := monitor.WriteRemoteSources(deploy.RemoteMonitorPath(layout), sources); err != nil {
		t.Fatalf("WriteRemoteSources: %v", err)
	}
}

func TestSourceQuotaExhaustedChecksEachDirection(t *testing.T) {
	cases := map[string]struct {
		source monitor.SourceSummary
		want   bool
	}{
		"no limits":         {monitor.SourceSummary{}, false},
		"room left":         {monitor.SourceSummary{TotalLimitBytes: 100, TotalRemainingBytes: 20}, false},
		"total spent":       {monitor.SourceSummary{TotalLimitBytes: 100, TotalRemainingBytes: 0}, true},
		"inbound spent":     {monitor.SourceSummary{InLimitBytes: 100, InRemainingBytes: 0, TotalRemainingBytes: 50}, true},
		"outbound spent":    {monitor.SourceSummary{OutLimitBytes: 100, OutRemainingBytes: 0}, true},
		"unlimited at zero": {monitor.SourceSummary{TotalRemainingBytes: 0}, false},
	}
	for name, tc := range cases {
		if got := sourceQuotaExhausted(tc.source); got != tc.want {
			t.Errorf("%s: sourceQuotaExhausted = %v, want %v", name, got, tc.want)
		}
	}
}

func TestRelayAvailableTreatsUnknownNodesAsUsable(t *testing.T) {
	layout, _ := relayTestHub(t)
	writeSpokeUsage(t, layout,
		monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 0},
		monitor.SourceSummary{ID: "bb22", TotalLimitBytes: 100, TotalRemainingBytes: 40},
	)
	available, err := (&Controller{Layout: layout}).RelayAvailable()
	if err != nil {
		t.Fatalf("RelayAvailable: %v", err)
	}
	if available("aa11") {
		t.Fatal("a spoke with nothing left must not carry relayed traffic")
	}
	if !available("BB22") {
		t.Fatal("a spoke with traffic left should stay available, case-insensitively")
	}
	// A node the hub has no figures for is presumed fine: withdrawing a working
	// relay because its snapshot has not arrived yet would be worse.
	if !available("cc33") || !available(relaylinks.HubNodeID) {
		t.Fatal("a node with no usage snapshot should count as available")
	}
}

// The whole point of the fallback: once the relay is out of traffic, clients
// are handed the landing node's own address again.
func TestRefreshSubscriptionsFallsBackWhenTheRelayIsOutOfQuota(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 40})
	relayed, _, _ := f.publish(t)
	if !strings.Contains(relayed, "@spoke.example.com:34568?") {
		t.Fatalf("the hub should be fronted while the relay has traffic left:\n%s", relayed)
	}

	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 0})
	direct, _, _ := f.publish(t)
	if !strings.Contains(direct, "@hub.example.com:9443?") {
		t.Fatalf("the hub should be published directly once the relay is spent:\n%s", direct)
	}
	if strings.Contains(direct, ":34568") {
		t.Fatalf("no relay port should survive:\n%s", direct)
	}
	if countLinks(relayed) != countLinks(direct) {
		t.Fatalf("the node list must keep its shape:\nbefore %s\nafter %s", relayed, direct)
	}

	// And back again when the cycle resets.
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 100})
	restored, _, _ := f.publish(t)
	if !strings.Contains(restored, "@spoke.example.com:34568?") {
		t.Fatalf("the relay should be used again after its reset:\n%s", restored)
	}
}

func TestReconcileRelayPublicationRepublishesOnlyOnAChange(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 40})

	ctx := context.Background()
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	marker, err := f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker != relaylinks.HubNodeID+"=aa11|spoke.example.com|34568>9443" {
		t.Fatalf("marker = %q", marker)
	}

	// Nothing changed, so the second pass must not republish. Emptying the
	// published file is how a rewrite would show up.
	token := deploy.SubscriptionToken(f.hubCfg.Salt)
	published := f.hubLayout.SubscribeDir + "/default/" + token
	if err := os.WriteFile(published, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("overwrite published file: %v", err)
	}
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	body, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("read published: %v", err)
	}
	if string(body) != "sentinel" {
		t.Fatalf("an unchanged topology must not rewrite the published files")
	}

	// Spending the relay's quota changes the topology, so this pass publishes.
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 0})
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if body, _ := os.ReadFile(published); string(body) == "sentinel" {
		t.Fatalf("an exhausted relay must trigger a republish")
	}
	marker, err = f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker != relaylinks.HubNodeID+"=direct" {
		t.Fatalf("marker = %q", marker)
	}
}

// Editing a landing node's protocols moves the port its relay has to forward
// to, and nothing else about the topology says so. The reconcile has to notice
// and push the corrected ruleset, or the relay keeps sending to a closed port.
func TestReconcileRelayPublicationFollowsALandingPortChange(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: "aa11", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}

	ctx := context.Background()
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	marker, err := f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !strings.HasSuffix(marker, "34568>8443") {
		t.Fatalf("marker should carry the landing node's current port: %q", marker)
	}

	// The operator moves the spoke's Hysteria2 port. The relay link never
	// mentioned a port, so it follows.
	if err := nodes.Mutate(f.hubLayout, "aa11", func(n *nodes.Node) error {
		n.Hysteria2Port = 8500
		return nil
	}); err != nil {
		t.Fatalf("move the landing port: %v", err)
	}
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	marker, err = f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !strings.HasSuffix(marker, "34568>8500") {
		t.Fatalf("the relay should follow the landing port: %q", marker)
	}

	links, err := relaylinks.Load(f.hubLayout)
	if err != nil {
		t.Fatalf("Load links: %v", err)
	}
	endpoints, err := f.ctrl.RelayEndpoints()
	if err != nil {
		t.Fatalf("RelayEndpoints: %v", err)
	}
	cfg, err := f.ctrl.RelayConfigFor(relaylinks.HubNodeID, links, endpoints)
	if err != nil {
		t.Fatalf("RelayConfigFor: %v", err)
	}
	if len(cfg.Landings) != 1 || cfg.Landings[0].Forwards[0].TargetPort != 8500 {
		t.Fatalf("the ruleset should forward to the new port: %#v", cfg.Landings)
	}
}

// A landing node that stops serving a protocol has nothing left to forward to,
// so the mapping is dropped rather than left pointing at a closed port.
func TestRelayConfigForDropsAProtocolTheLandingNodeNoLongerServes(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: "aa11", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
			{Protocol: config.ProtocolAnyTLS, Network: "tcp", RelayPort: 34569},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	links, err := relaylinks.Load(f.hubLayout)
	if err != nil {
		t.Fatalf("Load links: %v", err)
	}
	endpoints, err := f.ctrl.RelayEndpoints()
	if err != nil {
		t.Fatalf("RelayEndpoints: %v", err)
	}
	cfg, err := f.ctrl.RelayConfigFor(relaylinks.HubNodeID, links, endpoints)
	if err != nil {
		t.Fatalf("RelayConfigFor: %v", err)
	}
	// The spoke serves Hysteria2 only, so the AnyTLS mapping has no target.
	if len(cfg.Landings) != 1 || len(cfg.Landings[0].Forwards) != 1 {
		t.Fatalf("forwards = %#v", cfg.Landings)
	}
	if cfg.Landings[0].Forwards[0].ListenPort != 34568 {
		t.Fatalf("the served protocol should survive: %#v", cfg.Landings[0].Forwards[0])
	}
}

// A relay that runs out of traffic withdraws its own forwarding rules, and the
// reconcile pass that notices must not push them straight back: reinstalling
// them would leave an exhausted relay carrying other nodes' clients — and
// spending an allowance it no longer has — for the rest of the cycle.
func TestReconcileRelayPublicationLeavesAnExhaustedRelayWithdrawn(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: relaylinks.HubNodeID, RelayID: "aa11",
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 40})

	ctx := context.Background()
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if pushes := f.agent.relayPushes.Load(); pushes != 1 {
		t.Fatalf("a relay with traffic left should be installed once, got %d pushes", pushes)
	}

	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 0})
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("reconcile after the relay was spent: %v", err)
	}
	if pushes := f.agent.relayPushes.Load(); pushes != 1 {
		t.Fatalf("an exhausted relay must not be re-armed, got %d pushes", pushes)
	}
	marker, err := f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker != relaylinks.HubNodeID+"=direct" {
		t.Fatalf("marker = %q", marker)
	}

	// The allowance comes back at the cycle reset, and with it the ruleset.
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 100})
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("reconcile after the reset: %v", err)
	}
	if pushes := f.agent.relayPushes.Load(); pushes != 2 {
		t.Fatalf("a recovered relay should be installed again, got %d pushes", pushes)
	}
}

// A landing node that runs out of traffic has its own sing-box stopped, so the
// relay in front of it must stop forwarding rather than spend its allowance
// carrying clients to a dead port — and the published address goes back to the
// landing node, which is where it will answer again once its cycle resets.
func TestReconcileRelayPublicationStandsDownAnExhaustedLandingNode(t *testing.T) {
	f := newRelayFleet(t)
	if err := relaylinks.Set(f.hubLayout, relaylinks.Link{
		LandingID: "aa11", RelayID: relaylinks.HubNodeID,
		Forwards: []relaylinks.Forward{
			{Protocol: config.ProtocolHysteria2, Network: "udp", RelayPort: 34568},
		},
	}); err != nil {
		t.Fatalf("Set link: %v", err)
	}
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 40})

	relayed, _, _ := f.publish(t)
	if !strings.Contains(relayed, "@hub.example.com:34568?") {
		t.Fatalf("the spoke should be fronted while it has traffic left:\n%s", relayed)
	}

	// The landing node spends its allowance. Its relay has plenty left.
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 0})

	ctx := context.Background()
	if err := f.ctrl.ReconcileRelayPublication(ctx); err != nil {
		t.Fatalf("reconcile after the landing node was spent: %v", err)
	}
	marker, err := f.ctrl.readRelayPublication()
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker != "aa11=direct" {
		t.Fatalf("marker = %q", marker)
	}

	direct, _, _ := f.publish(t)
	if strings.Contains(direct, ":34568") {
		t.Fatalf("no relay port should be published for a spent landing node:\n%s", direct)
	}
	if !strings.Contains(direct, "@spoke.example.com:8443?") {
		t.Fatalf("the spoke should be published at its own address:\n%s", direct)
	}

	// And the ruleset the relay is told to run no longer mentions it at all,
	// so the forwarding ports are closed rather than pointed at a dead port.
	links, err := relaylinks.Load(f.hubLayout)
	if err != nil {
		t.Fatalf("Load links: %v", err)
	}
	endpoints, err := f.ctrl.RelayEndpoints()
	if err != nil {
		t.Fatalf("RelayEndpoints: %v", err)
	}
	cfg, err := f.ctrl.RelayConfigFor(relaylinks.HubNodeID, links, endpoints)
	if err != nil {
		t.Fatalf("RelayConfigFor: %v", err)
	}
	if !cfg.Empty() {
		t.Fatalf("the relay should forward nothing for a spent landing node: %#v", cfg.Landings)
	}

	// The landing node's cycle resets and the relay picks it back up.
	writeSpokeUsage(t, f.hubLayout, monitor.SourceSummary{ID: "aa11", TotalLimitBytes: 100, TotalRemainingBytes: 100})
	restored, _, _ := f.publish(t)
	if !strings.Contains(restored, "@hub.example.com:34568?") {
		t.Fatalf("the relay should front it again after its reset:\n%s", restored)
	}
	cfg, err = f.ctrl.RelayConfigFor(relaylinks.HubNodeID, links, endpoints)
	if err != nil {
		t.Fatalf("RelayConfigFor after the reset: %v", err)
	}
	if len(cfg.Landings) != 1 {
		t.Fatalf("the ruleset should carry it again: %#v", cfg.Landings)
	}
}

func TestReconcileRelayPublicationIsANoOpWithoutLinks(t *testing.T) {
	f := newRelayFleet(t)
	if err := f.ctrl.ReconcileRelayPublication(context.Background()); err != nil {
		t.Fatalf("ReconcileRelayPublication: %v", err)
	}
	if _, err := os.Stat(f.ctrl.relayPublicationPath()); !os.IsNotExist(err) {
		t.Fatalf("a fleet with no relay should leave no marker: %v", err)
	}
}
