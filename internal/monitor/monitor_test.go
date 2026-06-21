package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestSummaryRefreshesRemoteSourcesBeforeRead(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore error: %v", err)
	}
	defer store.Close()

	remotePath := filepath.Join(dir, "state", "remote_monitor.json")
	calls := 0
	m := New(store, Config{
		Alias:             "local",
		RemoteMonitorPath: remotePath,
		RefreshRemoteSources: func(_ context.Context) error {
			calls++
			return WriteRemoteSources(remotePath, []SourceSummary{{
				Name:           "JP-remote",
				TotalUsedBytes: 900,
				ResetTime:      "2026-06-01T00:00:00Z",
			}})
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
	if calls != 1 {
		t.Fatalf("refresh calls = %d", calls)
	}
	var got summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[1].Name != "JP-remote" || got.Sources[1].TotalUsedBytes != 900 {
		t.Fatalf("sources = %#v", got.Sources)
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
	if err := store.SetTotalsSince(base-1, base+1, TrafficTotals{InBytes: 40, OutBytes: 200}); err != nil {
		t.Fatalf("SetTotalsSince error: %v", err)
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
	active  bool
	starts  int
	stops   int
	startFn func() error
	stopFn  func() error
}

func (c *fakeController) IsActive() (bool, error) { return c.active, nil }
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
	c.active = false
	if c.stopFn != nil {
		return c.stopFn()
	}
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
