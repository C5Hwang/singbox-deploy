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

func TestPingTrendServesHourlyAveragesAndLatest(t *testing.T) {
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
	var hourly *PingHourlyPoint
	for i, p := range payload.Latency.Points {
		if p.HourTS == hour.Unix() && p.Target == "telecom-beijing" {
			hourly = &payload.Latency.Points[i]
		}
	}
	if hourly == nil || hourly.AvgMS == nil || *hourly.AvgMS != 150 {
		t.Fatalf("hourly point = %#v, want the 100/200 average", hourly)
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

	points, err := m.store.PingTrendHourly(0)
	if err != nil {
		t.Fatalf("PingTrendHourly: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %#v, want only the sample inside the retention window", points)
	}
	if points[0].HourTS != now.Add(-6*24*time.Hour).Truncate(time.Hour).Unix() {
		t.Fatalf("surviving point = %#v", points[0])
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
