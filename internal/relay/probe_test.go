package relay_test

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
)

func TestPingTargetsDescribeEachLandingNode(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := sampleConfig()
	cfg.Landings = append(cfg.Landings, relay.Landing{
		NodeID: "bb22", Host: "second.example.com",
		Forwards: []relay.Forward{{Protocol: "anytls", Network: "tcp", ListenPort: 39000, TargetPort: 42000}},
	})
	if err := relay.Save(layout, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	targets := relay.PingTargets(layout)()
	if len(targets) != 2 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].ID != relay.PingTargetID("aa11") || targets[0].Kind != relay.PingTargetKind {
		t.Fatalf("target = %#v", targets[0])
	}
	// The probe goes to the landing node's HTTPS port, never to a forwarded
	// protocol port.
	if targets[0].Address != "land.example.com:443" || targets[0].Name != "HK" {
		t.Fatalf("target = %#v", targets[0])
	}
	// A landing node with no name of its own falls back to something readable.
	if targets[1].Name != "second.example.com" {
		t.Fatalf("target = %#v", targets[1])
	}
}

func TestPingTargetsAreEmptyOnANodeThatRelaysNothing(t *testing.T) {
	if targets := relay.PingTargets(paths.LayoutForRoot(t.TempDir()))(); len(targets) != 0 {
		t.Fatalf("targets = %#v", targets)
	}
}
