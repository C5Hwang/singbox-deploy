package monitor

import (
	"path/filepath"
	"testing"
)

func resetTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func seedIPTraffic(t *testing.T, store *Store) {
	t.Helper()
	if err := store.AddIPTraffic(1000, map[string]IPTrafficDelta{
		"203.0.113.7":            {InBytes: 10, OutBytes: 5},
		"relay:aabb|203.0.113.7": {InBytes: 20, OutBytes: 7},
		"relay:198.51.100.4":     {InBytes: 3, OutBytes: 1},
	}); err != nil {
		t.Fatalf("AddIPTraffic: %v", err)
	}
	// Fold part of it so the reset has to reach every tier, not only the raw one.
	if err := store.AggregateIPHourly(2000); err != nil {
		t.Fatalf("AggregateIPHourly: %v", err)
	}
	if err := store.AddIPTraffic(3000, map[string]IPTrafficDelta{"203.0.113.9": {InBytes: 4}}); err != nil {
		t.Fatalf("AddIPTraffic: %v", err)
	}
}

func seedPingSamples(t *testing.T, store *Store) {
	t.Helper()
	ms := 12.5
	if err := store.InsertPingSamples(1000, map[string]PingSample{
		"telecom-beijing": {AvgMS: &ms},
		"relay:aabb":      {AvgMS: &ms},
		"relay:ccdd":      {AvgMS: &ms},
	}); err != nil {
		t.Fatalf("InsertPingSamples: %v", err)
	}
}

func pingTargets(t *testing.T, store *Store) []string {
	t.Helper()
	points, err := store.LatestPingSamples(0)
	if err != nil {
		t.Fatalf("LatestPingSamples: %v", err)
	}
	targets := make([]string, 0, len(points))
	for _, p := range points {
		targets = append(targets, p.Target)
	}
	return targets
}

// Clearing clients clears the whole per-address table: every tier it has been
// folded into, and both the direct rows and the relayed ones.
func TestResetHistoryClearsEveryTierOfClientTraffic(t *testing.T) {
	store, path := resetTestStore(t)
	seedIPTraffic(t, store)
	seedPingSamples(t, store)
	if entries, err := store.TopIPTraffic(10, 0, 0, 0); err != nil || len(entries) != 4 {
		t.Fatalf("TopIPTraffic before the reset = %#v, %v", entries, err)
	}
	if err := ResetHistory(path, ResetScopeClients, ""); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
	entries, err := store.TopIPTraffic(10, 0, 0, 0)
	if err != nil {
		t.Fatalf("TopIPTraffic: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("TopIPTraffic after the reset = %#v, want none", entries)
	}
	// Latency belongs to another page and another entry point.
	if got := pingTargets(t, store); len(got) != 3 {
		t.Fatalf("ping targets = %v, want the latency history untouched", got)
	}
}

// The latency reset is the Latency page's, so it takes the carrier probes and
// leaves the relay links alone: those are cleared through the relay they
// belong to.
func TestResetHistoryClearsCarrierProbesOnly(t *testing.T) {
	store, path := resetTestStore(t)
	seedIPTraffic(t, store)
	seedPingSamples(t, store)
	if err := ResetHistory(path, ResetScopeLatency, ""); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
	got := pingTargets(t, store)
	if len(got) != 2 {
		t.Fatalf("ping targets = %v, want both relay probes kept", got)
	}
	for _, target := range got {
		if target != "relay:aabb" && target != "relay:ccdd" {
			t.Fatalf("ping targets = %v, want only relay probes", got)
		}
	}
	if entries, err := store.TopIPTraffic(10, 0, 0, 0); err != nil || len(entries) != 4 {
		t.Fatalf("TopIPTraffic = %#v, %v, want the client history untouched", entries, err)
	}
}

// A relay-latency reset names one link, and clears that link only.
func TestResetHistoryClearsOneRelayLink(t *testing.T) {
	store, path := resetTestStore(t)
	seedPingSamples(t, store)
	if err := ResetHistory(path, ResetScopeRelayLatency, "relay:aabb"); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
	got := pingTargets(t, store)
	if len(got) != 2 {
		t.Fatalf("ping targets = %v, want the other two kept", got)
	}
	for _, target := range got {
		if target == "relay:aabb" {
			t.Fatalf("ping targets = %v, want the named link cleared", got)
		}
	}
}

// With no link named, every relay probe goes and the carriers stay.
func TestResetHistoryClearsEveryRelayLinkWithoutATarget(t *testing.T) {
	store, path := resetTestStore(t)
	seedPingSamples(t, store)
	if err := ResetHistory(path, ResetScopeRelayLatency, ""); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
	got := pingTargets(t, store)
	if len(got) != 1 || got[0] != "telecom-beijing" {
		t.Fatalf("ping targets = %v, want only the carrier probe", got)
	}
}

// A node whose monitor never ran has no database and is already in the state
// being asked for, which is not a failure to report to an operator.
func TestResetHistoryOnANodeThatNeverSampledIsANoOp(t *testing.T) {
	if err := ResetHistory(filepath.Join(t.TempDir(), "absent.db"), ResetScopeClients, ""); err != nil {
		t.Fatalf("ResetHistory: %v", err)
	}
}

func TestResetHistoryRefusesAnUnknownScope(t *testing.T) {
	_, path := resetTestStore(t)
	if err := ResetHistory(path, ResetScope("everything"), ""); err == nil {
		t.Fatalf("ResetHistory accepted an unknown scope")
	}
}
