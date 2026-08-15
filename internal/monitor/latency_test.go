package monitor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A probe that cannot run at all is dropped rather than recorded, so a broken
// target never masquerades as a reachable one with no data.
func TestCollectDropsFailedProbes(t *testing.T) {
	avg := 12.5
	c := &PingCollector{
		targets: []PingTarget{
			{ID: "ok", Address: "192.0.2.1"},
			{ID: "broken", Address: "192.0.2.2"},
		},
		probe: func(_ context.Context, address string) (PingSample, error) {
			if address == "192.0.2.2" {
				return PingSample{}, context.DeadlineExceeded
			}
			return PingSample{AvgMS: &avg, LossPct: 0}, nil
		},
	}
	samples := c.Collect(context.Background())
	if len(samples) != 1 {
		t.Fatalf("samples = %#v, want only the reachable target", samples)
	}
	if got, ok := samples["ok"]; !ok || got.AvgMS == nil || *got.AvgMS != avg {
		t.Fatalf("samples[ok] = %#v", got)
	}
}

// A relay contributes one probe per landing node it fronts, and the list is
// read per round so a link added or withdrawn takes effect without a restart.
func TestCollectSamplesRuntimeTargetsToo(t *testing.T) {
	avg := 7.5
	extra := []PingTarget{{ID: "relay:aa11", Kind: "relay", Name: "HK", Address: "192.0.2.9:443"}}
	c := &PingCollector{
		targets: []PingTarget{{ID: "telecom-beijing", Address: "192.0.2.1:80"}},
		extra:   func() []PingTarget { return extra },
		probe: func(context.Context, string) (PingSample, error) {
			return PingSample{AvgMS: &avg}, nil
		},
	}
	if got := c.Collect(context.Background()); len(got) != 2 || got["relay:aa11"].AvgMS == nil {
		t.Fatalf("samples = %#v", got)
	}
	if targets := c.Targets(); len(targets) != 2 || targets[1].Kind != "relay" || targets[1].Name != "HK" {
		t.Fatalf("targets = %#v", targets)
	}

	extra = nil
	if got := c.Collect(context.Background()); len(got) != 1 {
		t.Fatalf("a withdrawn relay link should stop being probed: %#v", got)
	}
}

// Two entries for one target would each overwrite the other's sample, so a
// runtime target that repeats a fixed one is dropped rather than sampled twice.
func TestTargetsDropDuplicatesAndIncompleteEntries(t *testing.T) {
	c := &PingCollector{
		targets: []PingTarget{{ID: "telecom-beijing", Address: "192.0.2.1:80"}},
		extra: func() []PingTarget {
			return []PingTarget{
				{ID: "telecom-beijing", Address: "192.0.2.9:443"},
				{ID: "", Address: "192.0.2.9:443"},
				{ID: "relay:aa11", Address: ""},
				{ID: "relay:bb22", Address: "192.0.2.8:443"},
			}
		},
	}
	targets := c.Targets()
	if len(targets) != 2 || targets[0].Address != "192.0.2.1:80" || targets[1].ID != "relay:bb22" {
		t.Fatalf("targets = %#v", targets)
	}
}

func newLatencyTestMonitor(t *testing.T, now time.Time) *Monitor {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, Config{Alias: "local", Now: func() time.Time { return now }}, nil)
	m.pingCollector = &PingCollector{targets: DefaultPingTargets}
	return m
}

func TestPingTrendServesTargetsAndLatest(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC)
	m := newLatencyTestMonitor(t, now)

	hour := time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC)
	for i, avg := range []float64{100, 200} {
		value := avg
		if err := m.store.InsertPingSamples(hour.Add(time.Duration(i)*5*time.Minute).Unix(), map[string]PingSample{
			"telecom-beijing": {AvgMS: &value, LossPct: 0},
		}); err != nil {
			t.Fatalf("InsertPingSamples: %v", err)
		}
	}
	// A newer, fully-lost round: it must win "latest" and carry no latency.
	if err := m.store.InsertPingSamples(now.Add(-time.Minute).Unix(), map[string]PingSample{
		"telecom-beijing": {LossPct: 100},
	}); err != nil {
		t.Fatalf("InsertPingSamples: %v", err)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ping-trend", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Latency LatencySnapshot `json:"latency"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Latency.Targets) != len(DefaultPingTargets) {
		t.Fatalf("targets = %d, want %d", len(payload.Latency.Targets), len(DefaultPingTargets))
	}
	if len(payload.Latency.Latest) != 1 {
		t.Fatalf("latest = %#v, want one entry per probed target", payload.Latency.Latest)
	}
	if latest := payload.Latency.Latest[0]; latest.AvgMS != nil || latest.LossPct != 100 {
		t.Fatalf("latest = %#v, want the newest fully-lost round", latest)
	}
}

// Latency history is capped at a week, so a run of maintenance drops anything
// older and keeps everything inside the window.
func TestMaintenanceDropsPingSamplesOlderThanAWeek(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	m := newLatencyTestMonitor(t, now)
	avg := 42.0
	for _, age := range []time.Duration{8 * 24 * time.Hour, 6 * 24 * time.Hour} {
		value := avg
		if err := m.store.InsertPingSamples(now.Add(-age).Unix(), map[string]PingSample{
			"telecom-beijing": {AvgMS: &value},
		}); err != nil {
			t.Fatalf("InsertPingSamples: %v", err)
		}
	}
	m.maintenance(now)

	var rows int
	if err := m.store.db.QueryRow(`SELECT COUNT(*) FROM ping_samples`).Scan(&rows); err != nil {
		t.Fatalf("count ping samples: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want only the sample inside the retention window", rows)
	}
}

// The series is a fixed grid rather than a list of points, so what has to hold
// is that a round lands in the slot its timestamp names, a round that answered
// nothing is told apart from a minute that was never probed, and the week is
// covered end to end.
func TestPingSeriesPlacesRoundsOnTheMinuteGrid(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	since := now.Add(-pingRetention)

	value := 123.456
	if err := store.InsertPingSamples(since.Add(90*time.Second).Unix(), map[string]PingSample{
		"telecom-beijing": {AvgMS: &value, LossPct: 20},
	}); err != nil {
		t.Fatalf("InsertPingSamples: %v", err)
	}
	// A round that lost everything: recorded, but with no latency to report.
	if err := store.InsertPingSamples(since.Add(3*time.Minute).Unix(), map[string]PingSample{
		"telecom-beijing": {LossPct: 100},
	}); err != nil {
		t.Fatalf("InsertPingSamples: %v", err)
	}

	series, err := store.PingSeriesData(since.Unix(), now.Unix(), 60)
	if err != nil {
		t.Fatalf("PingSeriesData: %v", err)
	}
	if series.Step != 60 || series.Count != 7*24*60+1 {
		t.Fatalf("grid = %d slots of %ds, want a week of minutes", series.Count, series.Step)
	}
	track := series.Series["telecom-beijing"]
	if track == nil {
		t.Fatal("no track for the probed target")
	}
	if len(track.MS) != series.Count || len(track.Loss) != series.Count {
		t.Fatalf("track = %d/%d slots, want %d", len(track.MS), len(track.Loss), series.Count)
	}
	// 90 seconds in is the second minute, and the value is rounded to the one
	// decimal a ping measurement means.
	if track.MS[1] == nil || *track.MS[1] != 123.5 || track.Loss[1] != 20 {
		t.Fatalf("slot 1 = %v / %v, want the rounded round", track.MS[1], track.Loss[1])
	}
	if track.MS[3] != nil || track.Loss[3] != 100 {
		t.Fatalf("slot 3 = %v / %v, want a recorded round that answered nothing", track.MS[3], track.Loss[3])
	}
	if track.MS[2] != nil || track.Loss[2] != -1 {
		t.Fatalf("slot 2 = %v / %v, want an unprobed minute", track.MS[2], track.Loss[2])
	}
}

// The probe list is what the dashboard groups its cards by, so it has to stay a
// full three-by-three of carriers and cities addressed as host:port.
func TestDefaultPingTargetsAreThreeCarriersByThreeCities(t *testing.T) {
	carriers, cities := map[string]int{}, map[string]int{}
	for _, target := range DefaultPingTargets {
		carriers[target.Carrier]++
		cities[target.City]++
		host, port, err := net.SplitHostPort(target.Address)
		if err != nil {
			t.Fatalf("target %s address %q is not host:port: %v", target.ID, target.Address, err)
		}
		if port != "80" || !strings.HasSuffix(host, ".ip.zstaticcdn.com") {
			t.Fatalf("target %s = %q, want a zstaticcdn node on port 80", target.ID, target.Address)
		}
	}
	if len(carriers) != 3 || len(cities) != 3 || len(DefaultPingTargets) != 9 {
		t.Fatalf("targets = %d across %d carriers and %d cities", len(DefaultPingTargets), len(carriers), len(cities))
	}
	for carrier, count := range carriers {
		if count != 3 {
			t.Fatalf("carrier %s has %d cities, want 3", carrier, count)
		}
	}
}

// A listener that accepts every connect is a route with no loss, and the
// average is a real measurement rather than a placeholder.
func TestTCPPingMeasuresAnAcceptingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	sample, err := tcpPing(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatalf("tcpPing: %v", err)
	}
	if sample.LossPct != 0 {
		t.Fatalf("LossPct = %v, want 0 against a listener that accepts", sample.LossPct)
	}
	if sample.AvgMS == nil || *sample.AvgMS < 0 {
		t.Fatalf("AvgMS = %v, want a measured average", sample.AvgMS)
	}
}

// A refused port is a fully lost round: 100% loss and no latency at all, which
// the dashboard draws as a gap instead of as a perfect link.
func TestTCPPingReportsFullLossOnARefusedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()

	sample, err := tcpPing(context.Background(), address)
	if err != nil {
		t.Fatalf("tcpPing: %v", err)
	}
	if sample.LossPct != 100 {
		t.Fatalf("LossPct = %v, want 100 against a closed port", sample.LossPct)
	}
	if sample.AvgMS != nil {
		t.Fatalf("AvgMS = %v, want no latency for a fully lost round", *sample.AvgMS)
	}
}

// A target that cannot even be addressed is an error, not a sample: recording
// it as loss would blame the route for a configuration mistake. Collect drops
// errored probes, so the target keeps its previous history instead.
func TestTCPPingErrorsOnAnUnusableAddress(t *testing.T) {
	if _, err := tcpPing(context.Background(), "missing-port.example.com"); err == nil {
		t.Fatal("tcpPing accepted an address with no port")
	}
}

func TestPingSampleFromCountsLossAgainstAttempts(t *testing.T) {
	sample := pingSampleFrom(300*time.Millisecond, 3, 5)
	if sample.LossPct != 40 {
		t.Fatalf("LossPct = %v, want 40 for three of five", sample.LossPct)
	}
	if sample.AvgMS == nil || *sample.AvgMS != 100 {
		t.Fatalf("AvgMS = %v, want the average over the answered connects", sample.AvgMS)
	}
}
