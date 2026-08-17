package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSummaryServesSnapshotWithoutRefreshing(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		Name:           "JP-remote",
		TotalUsedBytes: 900,
		ResetTime:      "2026-06-01T00:00:00Z",
	}}); err != nil {
		t.Fatalf("WriteRemoteSources error: %v", err)
	}
	calls := 0
	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		RefreshRemoteSources: func(_ context.Context) error {
			calls++
			return nil
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		},
	}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// Refreshing happens on the background ticker, never on the request path,
	// so a slow or looping peer cannot stall the summary API.
	if calls != 0 {
		t.Fatalf("refresh calls on request path = %d, want 0", calls)
	}
	var got summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[1].Name != "JP-remote" || got.Sources[1].TotalUsedBytes != 900 {
		t.Fatalf("sources = %#v", got.Sources)
	}
}

// The dashboard hides its relay page unless something is relayed, and asks only
// the relays for their probes once it shows one. Both facts come from here:
// whether a node relays is a fact about the hub's registry, not about any node's
// traffic.
func TestSummaryReportsTheRelayRegistry(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	read := func(cfg Config) summary {
		t.Helper()
		rec := httptest.NewRecorder()
		New(store, cfg, nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got summary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		return got
	}

	registry := func() ([]string, int) { return []string{"local", "spoke-a"}, 3 }
	got := read(Config{Alias: "local", RelayRegistry: registry})
	if got.RelayLinks != 3 {
		t.Fatalf("relayLinks = %d, want 3", got.RelayLinks)
	}
	if len(got.RelayNodes) != 2 || got.RelayNodes[0] != "local" || got.RelayNodes[1] != "spoke-a" {
		t.Fatalf("relayNodes = %#v, want the two relays", got.RelayNodes)
	}
	// A spoke's own monitor has no registry to ask, and neither does a hub
	// running a unit written before relaying existed.
	if got := read(Config{Alias: "local"}); got.RelayLinks != 0 || len(got.RelayNodes) != 0 {
		t.Fatalf("relay registry without one = %d links, %#v nodes, want none", got.RelayLinks, got.RelayNodes)
	}
}

func TestSummaryKeepsOldRemoteSnapshotWhenRefreshFails(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		Name:           "JP-old",
		TotalUsedBytes: 300,
		ResetTime:      "2026-06-01T00:00:00Z",
	}}); err != nil {
		t.Fatalf("WriteRemoteSources error: %v", err)
	}

	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		RefreshRemoteSources: func(_ context.Context) error {
			return errors.New("remote unavailable")
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		},
	}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[1].Name != "JP-old" || got.Sources[1].TotalUsedBytes != 300 {
		t.Fatalf("sources = %#v", got.Sources)
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
	loc := time.FixedZone("local", 8*60*60)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, loc)
	// reset day 15 -> cycle started May 15.
	start := CycleStart(now, 15)
	if start.Location() != time.UTC || start.Month() != time.May || start.Day() != 15 || start.Hour() != 0 {
		t.Fatalf("cycle start = %v", start)
	}
	// reset day 1 -> cycle started June 1 (today).
	start = CycleStart(now, 1)
	if start.Month() != time.June || start.Day() != 1 {
		t.Fatalf("cycle start = %v", start)
	}
	start = CycleStart(time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC), 1, 4)
	if start.Month() != time.May || start.Day() != 1 || start.Hour() != 4 || start.Location() != time.UTC {
		t.Fatalf("hourly GMT cycle start = %v", start)
	}
}

func TestStoreInsertAndTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
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

func TestOpenStoreCreatesPrivateDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not available on Windows")
	}
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	assertStoreFileMode(t, path, storeFileMode)
	if _, err := os.Stat(path + "-journal"); err == nil {
		assertStoreFileMode(t, path+"-journal", storeFileMode)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat rollback journal: %v", err)
	}
}

func TestOpenStoreTightensLegacyDatabaseAndPreservesData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not available on Windows")
	}
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore initial database: %v", err)
	}
	const ts = int64(1_700_000_000)
	if err := store.InsertSample(ts, "eth0", 120, 80, 120, 80); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	if err := store.SetQuotaStopped(true); err != nil {
		t.Fatalf("SetQuotaStopped: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial database: %v", err)
	}
	journalPath := path + "-journal"
	for _, candidate := range []string{path, journalPath} {
		if err := os.Chmod(candidate, 0o644); err != nil {
			t.Fatalf("set legacy mode on %s: %v", candidate, err)
		}
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen legacy database: %v", err)
	}
	defer store.Close()

	assertStoreFileMode(t, path, storeFileMode)
	assertStoreFileMode(t, journalPath, storeFileMode)
	totals, err := store.TotalsSince(ts)
	if err != nil {
		t.Fatalf("TotalsSince after permission migration: %v", err)
	}
	if totals != (TrafficTotals{InBytes: 120, OutBytes: 80}) {
		t.Fatalf("totals after permission migration = %#v", totals)
	}
	stopped, err := store.QuotaStopped()
	if err != nil {
		t.Fatalf("QuotaStopped after permission migration: %v", err)
	}
	if !stopped {
		t.Fatal("quota ownership marker was lost during permission migration")
	}
}

func TestSecureStorePermissionsTightensExistingSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not available on Windows")
	}
	path := filepath.Join(t.TempDir(), "monitor.db")
	for _, candidate := range append([]string{path}, sidecarPaths(path)...) {
		if err := os.WriteFile(candidate, []byte("legacy"), 0o644); err != nil {
			t.Fatalf("write legacy file %s: %v", candidate, err)
		}
		if err := os.Chmod(candidate, 0o644); err != nil {
			t.Fatalf("set legacy mode on %s: %v", candidate, err)
		}
	}

	if err := secureStorePermissions(path); err != nil {
		t.Fatalf("secureStorePermissions: %v", err)
	}
	assertStoreFileMode(t, path, storeFileMode)
	for _, sidecar := range sidecarPaths(path) {
		assertStoreFileMode(t, sidecar, storeFileMode)
	}
}

func sidecarPaths(path string) []string {
	paths := make([]string, 0, len(sqliteSidecarSuffixes))
	for _, suffix := range sqliteSidecarSuffixes {
		paths = append(paths, path+suffix)
	}
	return paths
}

func assertStoreFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}

func TestStoreSetTotalsSinceAddsSignedAdjustment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	base := time.Now().Unix()
	if err := store.InsertSample(base, "eth0", 100, 80, 100, 80); err != nil {
		t.Fatalf("InsertSample error: %v", err)
	}
	previous, err := store.ReplaceTotalsSince(base-1, base+1, TrafficTotals{InBytes: 40, OutBytes: 200})
	if err != nil {
		t.Fatalf("ReplaceTotalsSince error: %v", err)
	}
	if previous != (TrafficTotals{InBytes: 100, OutBytes: 80}) {
		t.Fatalf("previous totals = %#v", previous)
	}
	totals, err := store.TotalsSince(base - 1)
	if err != nil {
		t.Fatalf("TotalsSince error: %v", err)
	}
	if totals.InBytes != 40 || totals.OutBytes != 200 {
		t.Fatalf("totals = %#v", totals)
	}
}

func TestRemoteSourcesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "remote_monitor.json")
	want := []SourceSummary{{Name: "remote.example.com", MonitorURL: "https://remote.example.com:9444/monitor", TotalUsedBytes: 300, ResetTime: "2026-06-01T00:00:00Z"}}
	if err := WriteRemoteSources(path, want); err != nil {
		t.Fatalf("WriteRemoteSources error: %v", err)
	}
	got, err := ReadRemoteSources(path)
	if err != nil {
		t.Fatalf("ReadRemoteSources error: %v", err)
	}
	if len(got) != 1 || got[0].Name != want[0].Name || got[0].MonitorURL != want[0].MonitorURL || got[0].TotalUsedBytes != want[0].TotalUsedBytes {
		t.Fatalf("remote sources = %#v", got)
	}
}

func TestSummaryOmitsRemoteMonitorURL(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		Name:           "JP-remote",
		MonitorURL:     "https://remote.example.com:9444/monitor",
		TotalUsedBytes: 300,
		ResetTime:      "2026-06-01T00:00:00Z",
	}}); err != nil {
		t.Fatalf("WriteRemoteSources error: %v", err)
	}

	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		Now: func() time.Time {
			return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		},
	}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "monitorURL") || strings.Contains(body, "remote.example.com") {
		t.Fatalf("summary leaked remote monitor URL: %s", body)
	}

	var got summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[1].Name != "JP-remote" || got.Sources[1].TotalUsedBytes != 300 {
		t.Fatalf("sources = %#v", got.Sources)
	}
	if got.Sources[1].MonitorURL != "" {
		t.Fatalf("monitor URL should be omitted, got %q", got.Sources[1].MonitorURL)
	}
}

// ---------------------------------------------------------------------------
// Remaining / NextCycleReset helpers
// ---------------------------------------------------------------------------

func TestRemainingReturnsZeroWhenOverLimit(t *testing.T) {
	if r := Remaining(100, 150); r != 0 {
		t.Fatalf("Remaining(100, 150) = %d, want 0", r)
	}
}

func TestRemainingReturnsZeroForUnlimited(t *testing.T) {
	if r := Remaining(0, 500); r != 0 {
		t.Fatalf("Remaining(0, 500) = %d, want 0", r)
	}
}

func TestRemainingReturnsCorrectValue(t *testing.T) {
	if r := Remaining(1000, 300); r != 700 {
		t.Fatalf("Remaining(1000, 300) = %d, want 700", r)
	}
}

func TestNextCycleReset(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	next := NextCycleReset(now, 15)
	if next.Month() != time.June || next.Day() != 15 || next.Hour() != 0 {
		t.Fatalf("next reset = %v, want 2026-06-15 00:00 UTC", next)
	}
	now = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	next = NextCycleReset(now, 15)
	if next.Month() != time.July || next.Day() != 15 {
		t.Fatalf("next reset = %v, want 2026-07-15", next)
	}
}

func TestNextCycleResetWithHour(t *testing.T) {
	now := time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)
	next := NextCycleReset(now, 1, 4)
	if next.Day() != 1 || next.Hour() != 4 || next.Month() != time.June {
		t.Fatalf("next reset = %v, want 2026-06-01 04:00 UTC", next)
	}
}

// ---------------------------------------------------------------------------
// Quota enforcement
// ---------------------------------------------------------------------------

type fakeController struct {
	active    bool
	starts    int
	stops     int
	activeErr error
	startFn   func() error
	stopFn    func() error
}

func (c *fakeController) IsActive() (bool, error) { return c.active, c.activeErr }
func (c *fakeController) Start() error {
	c.starts++
	c.active = true
	if c.startFn != nil {
		return c.startFn()
	}
	return nil
}
func (c *fakeController) Stop() error {
	c.stops++
	if c.stopFn != nil {
		return c.stopFn()
	}
	c.active = false
	return nil
}

func TestEnforceQuotaStopsServiceWhenExceeded(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cycleStart := CycleStart(now, 1)
	if err := store.InsertSample(cycleStart.Unix()+100, "eth0", 500, 600, 500, 600); err != nil {
		t.Fatal(err)
	}

	ctrl := &fakeController{active: true}
	m := New(store, Config{
		InLimitBytes:    400,
		OutLimitBytes:   0,
		TotalLimitBytes: 0,
		ResetDay:        1,
		Now:             func() time.Time { return now },
	}, ctrl)

	m.enforceQuota(now)

	if ctrl.stops != 1 {
		t.Fatalf("expected 1 stop, got %d", ctrl.stops)
	}
	if !m.stoppedByQuota {
		t.Fatal("stoppedByQuota should be true")
	}
}

func TestEnforceQuotaRestartsOnNewCycle(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	ctrl := &fakeController{active: false}
	m := New(store, Config{
		InLimitBytes: 1000,
		ResetDay:     1,
		Now:          func() time.Time { return now },
	}, ctrl)
	m.stoppedByQuota = true

	m.enforceQuota(now)

	if ctrl.starts != 1 {
		t.Fatalf("expected 1 start, got %d", ctrl.starts)
	}
	if m.stoppedByQuota {
		t.Fatal("stoppedByQuota should be false after restart")
	}
}

func TestEnforceQuotaNoopWithoutController(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	m := New(store, Config{InLimitBytes: 1, ResetDay: 1}, nil)
	m.enforceQuota(time.Now())
}

func TestEnforceQuotaNoopWithZeroLimits(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if err := store.InsertSample(now.Unix(), "eth0", 1<<40, 1<<40, 1<<40, 1<<40); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{active: true}
	m := New(store, Config{ResetDay: 1, Now: func() time.Time { return now }}, ctrl)

	m.enforceQuota(now)

	if ctrl.stops != 0 {
		t.Fatal("should not stop with zero limits (unlimited)")
	}
}

func TestEnforceQuotaDoesNotStopTwice(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if err := store.InsertSample(now.Unix(), "eth0", 500, 500, 500, 500); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{active: false}
	m := New(store, Config{InLimitBytes: 100, ResetDay: 1, Now: func() time.Time { return now }}, ctrl)
	m.stoppedByQuota = true

	m.enforceQuota(now)
	if ctrl.stops != 0 {
		t.Fatal("should not stop when already inactive")
	}
}

func TestQuotaStopSurvivesMonitorRestart(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cycleStart := CycleStart(now, 1)
	if err := store.InsertSample(cycleStart.Unix()+100, "eth0", 500, 600, 500, 600); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{active: true}
	m := New(store, Config{InLimitBytes: 400, ResetDay: 1, Now: func() time.Time { return now }}, ctrl)
	m.enforceQuota(now)
	if !m.stoppedByQuota {
		t.Fatal("precondition: quota stop should trigger")
	}

	// A fresh Monitor over the same store (monitor restart) must see the
	// persisted flag and restart sing-box once the new cycle begins.
	later := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	ctrl2 := &fakeController{active: false}
	m2 := New(store, Config{InLimitBytes: 400, ResetDay: 1, Now: func() time.Time { return later }}, ctrl2)
	if !m2.stoppedByQuota {
		t.Fatal("stoppedByQuota should be restored from the store")
	}
	m2.enforceQuota(later)
	if ctrl2.starts != 1 {
		t.Fatalf("expected 1 start after new cycle, got %d", ctrl2.starts)
	}
	if m2.stoppedByQuota {
		t.Fatal("stoppedByQuota should clear after restart")
	}
}

func TestEnforceQuotaRestartsWhenLimitsRemoved(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	ctrl := &fakeController{active: false}
	m := New(store, Config{ResetDay: 1, Now: func() time.Time { return now }}, ctrl)
	m.stoppedByQuota = true

	m.enforceQuota(now)
	if ctrl.starts != 1 {
		t.Fatalf("expected 1 start when limits removed, got %d", ctrl.starts)
	}
	if m.stoppedByQuota {
		t.Fatal("stoppedByQuota should clear when limits removed")
	}
}

func TestSetCurrentTrafficUsageReconcilesQuotaImmediately(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	cycleStart := CycleStart(now, 1)
	ctrl := &fakeController{active: true}
	m := New(store, Config{
		TotalLimitBytes: 100, ResetDay: 1, Now: func() time.Time { return now },
	}, ctrl)

	update, err := m.SetCurrentTrafficUsage(cycleStart.Unix(), TrafficTotals{InBytes: 60, OutBytes: 50})
	if err != nil {
		t.Fatalf("set usage above quota: %v", err)
	}
	if update.Previous.Totals != (TrafficTotals{}) ||
		update.Applied.Totals != (TrafficTotals{InBytes: 60, OutBytes: 50}) ||
		update.Applied.CycleStart != cycleStart || ctrl.stops != 1 || !m.stoppedByQuota {
		t.Fatalf("above quota result=%+v stops=%d stopped=%v", update, ctrl.stops, m.stoppedByQuota)
	}
	stopped, err := store.QuotaStopped()
	if err != nil || !stopped {
		t.Fatalf("quota marker after stop = %v, err=%v", stopped, err)
	}

	update, err = m.SetCurrentTrafficUsage(cycleStart.Unix(), TrafficTotals{InBytes: 10, OutBytes: 20})
	if err != nil {
		t.Fatalf("set usage below quota: %v", err)
	}
	if update.Previous.Totals != (TrafficTotals{InBytes: 60, OutBytes: 50}) ||
		update.Applied.Totals != (TrafficTotals{InBytes: 10, OutBytes: 20}) ||
		ctrl.starts != 1 || m.stoppedByQuota {
		t.Fatalf("below quota result=%+v starts=%d stopped=%v", update, ctrl.starts, m.stoppedByQuota)
	}
	stopped, err = store.QuotaStopped()
	if err != nil || stopped {
		t.Fatalf("quota marker after release = %v, err=%v", stopped, err)
	}

	if _, err := m.SetCurrentTrafficUsage(cycleStart.AddDate(0, -1, 0).Unix(), TrafficTotals{InBytes: 1}); !errors.Is(err, ErrTrafficCycleChanged) {
		t.Fatalf("stale cycle error = %v", err)
	}
	current, err := m.CurrentTrafficUsage()
	if err != nil || current.Totals != update.Applied.Totals {
		t.Fatalf("stale request changed usage: %+v err=%v", current, err)
	}
}

func TestSetCurrentTrafficUsageReturnsBoundedSingleLineReconciliationWarning(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	ctrl := &fakeController{
		active:    true,
		activeErr: errors.New(strings.Repeat("failure", 400) + "\r\nsecond line"),
	}
	m := New(store, Config{
		TotalLimitBytes: 1, ResetDay: 1, Now: func() time.Time { return now },
	}, ctrl)

	update, err := m.SetCurrentTrafficUsage(
		CycleStart(now, 1).Unix(),
		TrafficTotals{InBytes: 2},
	)
	if err != nil {
		t.Fatalf("SetCurrentTrafficUsage error = %v", err)
	}
	if update.Applied.Totals.InBytes != 2 {
		t.Fatalf("committed usage = %+v", update.Applied)
	}
	if len(update.Warning) > maxTrafficUsageWarningBytes {
		t.Fatalf("warning length = %d", len(update.Warning))
	}
	if strings.ContainsAny(update.Warning, "\r\n") {
		t.Fatalf("warning is not single-line: %q", update.Warning)
	}
	if !strings.HasSuffix(update.Warning, "...") {
		t.Fatalf("truncated warning = %q", update.Warning)
	}
}

func TestSetCurrentTrafficUsageKeepsQuotaOwnershipAfterStopFailure(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	stopErr := errors.New("systemd stop failed")
	ctrl := &fakeController{active: true, stopFn: func() error { return stopErr }}
	m := New(store, Config{
		TotalLimitBytes: 10, ResetDay: 1, Now: func() time.Time { return now },
	}, ctrl)
	cycleStart := CycleStart(now, 1).Unix()

	high, err := m.SetCurrentTrafficUsage(cycleStart, TrafficTotals{InBytes: 11})
	if err != nil {
		t.Fatalf("SetCurrentTrafficUsage above quota error = %v", err)
	}
	if !strings.Contains(high.Warning, stopErr.Error()) || !m.stoppedByQuota {
		t.Fatalf("failed stop update=%+v stoppedByQuota=%v", high, m.stoppedByQuota)
	}
	stopped, err := store.QuotaStopped()
	if err != nil || !stopped {
		t.Fatalf("persisted quota ownership = %v, err=%v", stopped, err)
	}

	ctrl.stopFn = nil
	low, err := m.SetCurrentTrafficUsage(cycleStart, TrafficTotals{InBytes: 1})
	if err != nil {
		t.Fatalf("SetCurrentTrafficUsage below quota error = %v", err)
	}
	if low.Warning != "" || ctrl.starts != 1 || m.stoppedByQuota {
		t.Fatalf("recovery update=%+v starts=%d stoppedByQuota=%v", low, ctrl.starts, m.stoppedByQuota)
	}
	stopped, err = store.QuotaStopped()
	if err != nil || stopped {
		t.Fatalf("quota ownership after recovery = %v, err=%v", stopped, err)
	}
}

// ---------------------------------------------------------------------------
// Store: TrendHourly
// ---------------------------------------------------------------------------

func TestStoreTrendHourlyFromRawSamples(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	hour := int64(1717200000)
	if err := store.InsertSample(hour+10, "eth0", 100, 50, 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSample(hour+70, "eth0", 200, 100, 100, 50); err != nil {
		t.Fatal(err)
	}

	trend, err := store.TrendHourly(hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 {
		t.Fatalf("trend points = %d, want 1", len(trend))
	}
	if trend[0].InBytes != 200 || trend[0].OutBytes != 100 || trend[0].TotalBytes != 300 {
		t.Fatalf("trend[0] = %+v", trend[0])
	}
}

func TestStoreTrendHourlyUnionsAggregatedAndRaw(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	hour1 := int64(1717200000)
	hour2 := hour1 + 3600
	if err := store.InsertSample(hour1+10, "eth0", 100, 50, 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := store.AggregateHourly(hour1 + 3600); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSample(hour2+10, "eth0", 300, 200, 200, 150); err != nil {
		t.Fatal(err)
	}

	trend, err := store.TrendHourly(hour1)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 2 {
		t.Fatalf("trend points = %d, want 2", len(trend))
	}
	if trend[0].InBytes != 100 || trend[1].InBytes != 200 {
		t.Fatalf("trend = %+v", trend)
	}
}

// ---------------------------------------------------------------------------
// Store: AggregateHourly + Cleanup
// ---------------------------------------------------------------------------

func TestAggregateHourlyFoldsAndDeletes(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	hour := int64(1717200000)
	if err := store.InsertSample(hour+10, "eth0", 100, 50, 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSample(hour+70, "eth0", 200, 100, 100, 50); err != nil {
		t.Fatal(err)
	}

	if err := store.AggregateHourly(hour + 3600); err != nil {
		t.Fatal(err)
	}

	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&count)
	if count != 0 {
		t.Fatalf("raw samples remaining = %d, want 0", count)
	}

	var hourlyIn, hourlyOut int64
	store.db.QueryRow(`SELECT in_bytes, out_bytes FROM hourly WHERE ts_hour = ?`, (hour/3600)*3600).Scan(&hourlyIn, &hourlyOut)
	if hourlyIn != 200 || hourlyOut != 100 {
		t.Fatalf("hourly bucket in=%d out=%d, want 200/100", hourlyIn, hourlyOut)
	}
}

func TestCleanupRemovesOldData(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	old := int64(1000000)
	recent := int64(2000000)

	store.db.Exec(`INSERT INTO hourly(ts_hour, in_bytes, out_bytes) VALUES(?, 100, 50)`, old)
	store.db.Exec(`INSERT INTO hourly(ts_hour, in_bytes, out_bytes) VALUES(?, 200, 100)`, recent)
	store.db.Exec(`INSERT INTO adjustments(ts, in_bytes, out_bytes) VALUES(?, 10, 5)`, old)
	store.db.Exec(`INSERT INTO adjustments(ts, in_bytes, out_bytes) VALUES(?, 20, 10)`, recent)

	if err := store.Cleanup(recent); err != nil {
		t.Fatal(err)
	}

	var hourlyCount, adjCount int
	store.db.QueryRow(`SELECT COUNT(*) FROM hourly`).Scan(&hourlyCount)
	store.db.QueryRow(`SELECT COUNT(*) FROM adjustments`).Scan(&adjCount)
	if hourlyCount != 1 {
		t.Fatalf("hourly rows = %d, want 1", hourlyCount)
	}
	if adjCount != 1 {
		t.Fatalf("adjustment rows = %d, want 1", adjCount)
	}
}

// ---------------------------------------------------------------------------
// Store: LatestCounters
// ---------------------------------------------------------------------------

func TestLatestCountersEmpty(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	_, _, ok, err := store.LatestCounters("eth0")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok should be false for empty store")
	}
}

func TestLatestCountersReturnsNewest(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	base := time.Now().Unix()
	store.InsertSample(base, "eth0", 100, 50, 100, 50)
	store.InsertSample(base+60, "eth0", 300, 200, 200, 150)

	rx, tx, ok, err := store.LatestCounters("eth0")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if rx != 300 || tx != 200 {
		t.Fatalf("latest counters rx=%d tx=%d, want 300/200", rx, tx)
	}
}

// ---------------------------------------------------------------------------
// Store: TrafficRawSamples
// ---------------------------------------------------------------------------

func TestTrafficRawSamples(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	base := int64(1717200000)
	store.InsertSample(base, "eth0", 100, 50, 100, 50)
	store.InsertSample(base+60, "eth0", 200, 100, 100, 50)

	points, err := store.TrafficRawSamples(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2", len(points))
	}
	if points[0].InBytes != 100 || points[0].OutBytes != 50 || points[0].TotalBytes != 150 {
		t.Fatalf("points[0] = %+v", points[0])
	}
}

// ---------------------------------------------------------------------------
// Store: Resource samples + aggregation
// ---------------------------------------------------------------------------

func TestInsertResourceSampleAndQuery(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	base := int64(1717200000)
	if err := store.InsertResourceSample(base, 45.5, 60.0, 70.0, 1024, 512); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertResourceSample(base+10, 50.0, 65.0, 70.5, 2048, 1024); err != nil {
		t.Fatal(err)
	}

	points, err := store.ResourceRawSamples(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("resource points = %d, want 2", len(points))
	}
	if points[0].CPUPct != 45.5 || points[0].DIORead != 1024 {
		t.Fatalf("points[0] = %+v", points[0])
	}
}

func TestAggregateResourceHourly(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	hour := int64(1717200000)
	store.InsertResourceSample(hour+10, 40.0, 60.0, 70.0, 1000, 500)
	store.InsertResourceSample(hour+20, 60.0, 80.0, 72.0, 3000, 1500)

	if err := store.AggregateResourceHourly(hour + 3600); err != nil {
		t.Fatal(err)
	}

	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM resource_samples`).Scan(&count)
	if count != 0 {
		t.Fatalf("raw resource samples remaining = %d, want 0", count)
	}

	trend, err := store.ResourceTrendHourly(hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 {
		t.Fatalf("resource trend points = %d, want 1", len(trend))
	}
	if trend[0].CPUMax != 60.0 || trend[0].MemMax != 80.0 {
		t.Fatalf("resource trend[0] = %+v", trend[0])
	}
}

func TestAggregateResourceHourlyMergesWeightedBySampleCount(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	hour := int64(1717200000)
	// Fold 1: a single sample at cpu 100%.
	store.InsertResourceSample(hour+10, 100.0, 0, 0, 0, 0)
	if err := store.AggregateResourceHourly(hour + 60); err != nil {
		t.Fatal(err)
	}
	// Fold 2: three samples at cpu 0% in the same hour bucket.
	store.InsertResourceSample(hour+100, 0, 0, 0, 0, 0)
	store.InsertResourceSample(hour+110, 0, 0, 0, 0, 0)
	store.InsertResourceSample(hour+120, 0, 0, 0, 0, 0)
	if err := store.AggregateResourceHourly(hour + 3600); err != nil {
		t.Fatal(err)
	}

	trend, err := store.ResourceTrendHourly(hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 {
		t.Fatalf("resource trend points = %d, want 1", len(trend))
	}
	// Weighted mean of 1×100% and 3×0% is 25%; the old unweighted merge
	// produced 50%.
	if trend[0].CPUAvg != 25.0 {
		t.Fatalf("cpu avg = %v, want weighted mean 25.0", trend[0].CPUAvg)
	}
}

// ---------------------------------------------------------------------------
// Store: LatestSampleTime
// ---------------------------------------------------------------------------

func TestLatestSampleTimeEmpty(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	_, ok := store.LatestSampleTime()
	if ok {
		t.Fatal("ok should be false for empty store")
	}
}

func TestLatestSampleTime(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	store.InsertSample(1000, "eth0", 0, 0, 0, 0)
	store.InsertSample(2000, "eth0", 0, 0, 0, 0)

	ts, ok := store.LatestSampleTime()
	if !ok || ts != 2000 {
		t.Fatalf("latest sample time = %d, ok = %v", ts, ok)
	}
}

// ---------------------------------------------------------------------------
// API: /api/traffic-trend
// ---------------------------------------------------------------------------

func TestTrafficTrendAPI(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sampleTS := now.Add(-30 * time.Minute).Unix()
	store.InsertSample(sampleTS, "eth0", 100, 50, 100, 50)

	m := New(store, Config{
		Alias:    "local",
		ResetDay: 1,
		Now:      func() time.Time { return now },
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-trend", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result struct {
		Trend []HourlyPoint `json:"trend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Trend) == 0 {
		t.Fatal("expected at least one trend point")
	}
}

func TestTrafficTrendAPIRemoteSourceNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := New(store, Config{Alias: "local", ResetDay: 1, Now: func() time.Time { return time.Now() }}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-trend?source=nonexistent", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// API: /api/traffic-recent
// ---------------------------------------------------------------------------

func TestTrafficRecentAPI(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store.InsertSample(now.Add(-10*time.Minute).Unix(), "eth0", 100, 50, 100, 50)

	m := New(store, Config{
		Alias:    "local",
		ResetDay: 1,
		Now:      func() time.Time { return now },
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-recent", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result struct {
		Points []TrafficRawPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(result.Points))
	}
}

// ---------------------------------------------------------------------------
// API: /api/resource-trend
// ---------------------------------------------------------------------------

func TestResourceTrendAPI(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store.InsertResourceSample(now.Add(-30*time.Minute).Unix(), 50.0, 60.0, 70.0, 1024, 512)

	m := New(store, Config{
		Alias:    "local",
		ResetDay: 1,
		Now:      func() time.Time { return now },
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/resource-trend", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result struct {
		Trend []ResourceHourlyPoint `json:"trend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Trend) == 0 {
		t.Fatal("expected at least one resource trend point")
	}
	// Stored values are 10-second byte deltas; the API serves bytes/sec.
	if result.Trend[0].DIOReadAvg != 102 || result.Trend[0].DIOWriteAvg != 51 {
		t.Fatalf("disk IO should be served as bytes/sec, got read=%d write=%d", result.Trend[0].DIOReadAvg, result.Trend[0].DIOWriteAvg)
	}
}

// ---------------------------------------------------------------------------
// API: /api/resource-recent
// ---------------------------------------------------------------------------

func TestResourceRecentAPI(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store.InsertResourceSample(now.Add(-10*time.Minute).Unix(), 50.0, 60.0, 70.0, 1024, 512)

	m := New(store, Config{
		Alias:    "local",
		ResetDay: 1,
		Now:      func() time.Time { return now },
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/resource-recent", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result struct {
		Points []ResourceRawPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(result.Points))
	}
	// Stored values are 10-second byte deltas; the API serves bytes/sec.
	if result.Points[0].DIORead != 102 || result.Points[0].DIOWrite != 51 {
		t.Fatalf("disk IO should be served as bytes/sec, got read=%d write=%d", result.Points[0].DIORead, result.Points[0].DIOWrite)
	}
}

// ---------------------------------------------------------------------------
// API: remote source with embedded trend data (no proxy)
// ---------------------------------------------------------------------------

func TestTrafficTrendAPIRemoteEmbeddedTrend(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		Name:  "JP-remote",
		Trend: []HourlyPoint{{HourTS: 1000, InBytes: 10, OutBytes: 20, TotalBytes: 30}},
	}}); err != nil {
		t.Fatal(err)
	}

	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		ResetDay:          1,
		Now:               func() time.Time { return time.Now() },
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-trend?source=JP-remote", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Trend []HourlyPoint `json:"trend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Trend) != 1 || result.Trend[0].InBytes != 10 {
		t.Fatalf("trend = %+v", result.Trend)
	}
}

func TestRemoteDrillDownUsesStableIDFetcherInsteadOfStoredURL(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		ID: "stable-node-id", Name: "mutable alias", MonitorURL: "http://127.0.0.1:1/should-not-be-used",
	}}); err != nil {
		t.Fatal(err)
	}
	var gotID, gotPath string
	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		FetchRemoteData: func(_ context.Context, sourceID, path string, _ url.Values) ([]byte, error) {
			gotID, gotPath = sourceID, path
			return []byte(`{"trend":[{"hourTs":1000,"inBytes":7}]}`), nil
		},
	}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-trend?source=stable-node-id", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotID != "stable-node-id" || gotPath != "/api/traffic-trend" {
		t.Fatalf("fetcher got id=%q path=%q", gotID, gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"inBytes":7`) {
		t.Fatalf("unexpected proxied body: %s", rec.Body.String())
	}
}

func TestManagedHubDoesNotFallBackToLegacyPublicMonitorURL(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	remotePath := filepath.Join(dir, "remote_monitor.json")
	if err := WriteRemoteSources(remotePath, []SourceSummary{{
		Name: "legacy", MonitorURL: "http://127.0.0.1:1/public-legacy",
	}}); err != nil {
		t.Fatal(err)
	}
	m := New(store, Config{
		RemoteMonitorPath: remotePath,
		FetchRemoteData: func(context.Context, string, string, url.Values) ([]byte, error) {
			t.Fatal("legacy source without stable ID must not reach the spoke fetcher")
			return nil, nil
		},
	}, nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/traffic-trend?source=legacy", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tempStore(t *testing.T) (*Store, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store, func() { store.Close() }
}
