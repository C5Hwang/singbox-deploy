package monitor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficLimitsExceeded(t *testing.T) {
	limits := TrafficLimits{InBytes: 100, OutBytes: 200, TotalBytes: 250}
	used := TrafficTotals{InBytes: 90, OutBytes: 160}
	if !limits.Exceeded(used) {
		t.Fatalf("quota should be exceeded")
	}
}

func TestTrafficLimitsUnlimited(t *testing.T) {
	limits := TrafficLimits{}
	used := TrafficTotals{InBytes: 1 << 40, OutBytes: 1 << 40}
	if limits.Exceeded(used) {
		t.Fatalf("zero limit means unlimited")
	}
}

func TestDeltaCounterHandlesIncrease(t *testing.T) {
	if d := Delta(1000, 1500); d != 500 {
		t.Fatalf("delta = %d", d)
	}
}

func TestDeltaCounterHandlesReset(t *testing.T) {
	if d := Delta(1500, 100); d != 0 {
		t.Fatalf("delta after reset = %d", d)
	}
}

func TestCycleStart(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// reset day 15 -> cycle started May 15.
	start := CycleStart(now, 15)
	if start.Month() != time.May || start.Day() != 15 {
		t.Fatalf("cycle start = %v", start)
	}
	// reset day 1 -> cycle started June 1 (today).
	start = CycleStart(now, 1)
	if start.Month() != time.June || start.Day() != 1 {
		t.Fatalf("cycle start = %v", start)
	}
}

func TestStoreInsertAndTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	base := time.Now().Unix()
	if err := store.InsertSample(base, "eth0", 100, 50, 100, 50); err != nil {
		t.Fatalf("InsertSample error: %v", err)
	}
	if err := store.InsertSample(base+60, "eth0", 200, 100, 200, 100); err != nil {
		t.Fatalf("InsertSample error: %v", err)
	}
	totals, err := store.TotalsSince(base - 1)
	if err != nil {
		t.Fatalf("TotalsSince error: %v", err)
	}
	if totals.InBytes != 300 || totals.OutBytes != 150 || totals.Total() != 450 {
		t.Fatalf("totals = %#v, want in=300 out=150 total=450", totals)
	}
}

func TestRemoteSourcesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "remote_traffic.json")
	want := []SourceSummary{{Name: "remote.example.com", TotalUsedBytes: 300, ResetTime: "2026-06-01T00:00:00Z"}}
	if err := WriteRemoteSources(path, want); err != nil {
		t.Fatalf("WriteRemoteSources error: %v", err)
	}
	got, err := ReadRemoteSources(path)
	if err != nil {
		t.Fatalf("ReadRemoteSources error: %v", err)
	}
	if len(got) != 1 || got[0].Name != want[0].Name || got[0].TotalUsedBytes != want[0].TotalUsedBytes {
		t.Fatalf("remote sources = %#v", got)
	}
}
