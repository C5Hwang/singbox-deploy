package monitor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaExceededStopsService(t *testing.T) {
	q := Quota{LimitBytes: 100, UsedBytes: 101}
	if !q.Exceeded() {
		t.Fatalf("quota should be exceeded")
	}
}

func TestQuotaUnlimited(t *testing.T) {
	q := Quota{LimitBytes: 0, UsedBytes: 1 << 40}
	if q.Exceeded() {
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

func TestStoreInsertAndTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	base := time.Now().Unix()
	if err := store.InsertSample(base, "eth0", 100, 50, 150); err != nil {
		t.Fatalf("InsertSample error: %v", err)
	}
	if err := store.InsertSample(base+60, "eth0", 200, 100, 250); err != nil {
		t.Fatalf("InsertSample error: %v", err)
	}
	total, err := store.TotalSince(base - 1)
	if err != nil {
		t.Fatalf("TotalSince error: %v", err)
	}
	if total != 400 {
		t.Fatalf("total = %d, want 400", total)
	}
}
