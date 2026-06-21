package cluster

import (
	"context"
	"testing"
)

func TestBroadcastUpgradeCollectsPerNodeOutcomes(t *testing.T) {
	// Two nodes pointing at non-routable WG IPs; both should fail fast and
	// produce one outcome each with Err != nil.
	nodes := []Node{
		{Alias: "Tokyo", WGIP: "10.10.0.2", APIToken: "tok"},
		{Alias: "Singapore", WGIP: "10.10.0.3", APIToken: "tok"},
	}
	got := BroadcastUpgrade(context.Background(), nodes, UpgradeRequest{Version: "v1.0.0"})
	if len(got) != 2 {
		t.Fatalf("len = %d want 2", len(got))
	}
	for i, outcome := range got {
		if outcome.Node.Alias != nodes[i].Alias {
			t.Errorf("[%d] node = %s want %s", i, outcome.Node.Alias, nodes[i].Alias)
		}
		if outcome.Succeeded() {
			t.Errorf("[%d] unexpected success — node has no live agent", i)
		}
	}
}

func TestBroadcastUpgradeEmptyTargets(t *testing.T) {
	got := BroadcastUpgrade(context.Background(), nil, UpgradeRequest{Version: "v1.0.0"})
	if len(got) != 0 {
		t.Errorf("expected empty outcome slice, got %d", len(got))
	}
}
