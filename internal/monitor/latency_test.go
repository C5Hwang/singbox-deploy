package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePingOutput(t *testing.T) {
	const iputils = `PING 219.141.136.10 (219.141.136.10) 56(84) bytes of data.

--- 219.141.136.10 ping statistics ---
10 packets transmitted, 8 received, 20% packet loss, time 2712ms
rtt min/avg/max/mdev = 59.302/63.257/69.333/4.360 ms
`
	sample, err := parsePingOutput(iputils)
	if err != nil {
		t.Fatalf("parsePingOutput: %v", err)
	}
	if sample.AvgMS == nil || *sample.AvgMS != 63.257 {
		t.Fatalf("AvgMS = %v, want 63.257", sample.AvgMS)
	}
	if sample.LossPct != 20 {
		t.Fatalf("LossPct = %v, want 20", sample.LossPct)
	}
}

// BusyBox spells the second count differently and labels the summary
// "round-trip", so both spellings have to parse.
func TestParsePingOutputBusyBoxSpelling(t *testing.T) {
	const busybox = `--- 1.1.1.1 ping statistics ---
10 packets transmitted, 9 packets received, 10% packet loss
round-trip min/avg/max = 11.1/22.5/44.0 ms
`
	sample, err := parsePingOutput(busybox)
	if err != nil {
		t.Fatalf("parsePingOutput: %v", err)
	}
	if sample.AvgMS == nil || *sample.AvgMS != 22.5 {
		t.Fatalf("AvgMS = %v, want 22.5", sample.AvgMS)
	}
	if sample.LossPct != 10 {
		t.Fatalf("LossPct = %v, want 10", sample.LossPct)
	}
}

// A target that answered nothing has no latency at all. Reporting zero would
// draw as a perfect link, so the sample carries no value instead.
func TestParsePingOutputFullLossHasNoLatency(t *testing.T) {
	const lost = `--- 10.0.0.1 ping statistics ---
10 packets transmitted, 0 received, 100% packet loss, time 9204ms
`
	sample, err := parsePingOutput(lost)
	if err != nil {
		t.Fatalf("parsePingOutput: %v", err)
	}
	if sample.AvgMS != nil {
		t.Fatalf("AvgMS = %v, want nil", *sample.AvgMS)
	}
	if sample.LossPct != 100 {
		t.Fatalf("LossPct = %v, want 100", sample.LossPct)
	}
}

func TestParsePingOutputRejectsUnparsableSummary(t *testing.T) {
	if _, err := parsePingOutput("ping: connect: Network is unreachable\n"); err == nil {
		t.Fatal("parsePingOutput accepted output with no statistics")
	}
}

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

// iputils treats -w as "keep probing until the deadline expires or count
// probes are answered", so a deadline flag makes -c stop bounding the run: an
// unanswered target is probed for the whole deadline, and a target slower than
// the send interval gets an extra probe that reads as 1/11 = 9.09% loss on a
// route that lost nothing. The count and the per-reply timeout are what bound
// a probe; pingDeadline bounds it from the outside.
func TestPingArgsCarryNoDeadlineFlag(t *testing.T) {
	args := pingArgs("203.0.113.9")
	for i, arg := range args {
		if arg == "-w" {
			t.Fatalf("pingArgs = %q, want no -w deadline flag", args)
		}
		if arg == "-c" && (i+1 >= len(args) || args[i+1] != "10") {
			t.Fatalf("pingArgs = %q, want -c %d", args, pingCount)
		}
	}
	if args[len(args)-1] != "203.0.113.9" {
		t.Fatalf("pingArgs = %q, want the address last", args)
	}
}
