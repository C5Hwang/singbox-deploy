package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNFT records the commands issued and answers set listings from a scripted
// sequence of per-set byte counters.
type fakeNFT struct {
	commands []string
	// rulesets holds the stdin of every ruleset load, so a test can assert what
	// the chains ended up containing.
	rulesets []string
	// rounds[i] maps set name to address -> cumulative bytes for the i-th read
	// of each set; the last round repeats once exhausted.
	rounds []map[string]map[string]uint64
	reads  int
	// failSet makes every listing fail, standing in for a flushed table.
	failSet bool
}

func (f *fakeNFT) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, strings.Join(args, " "))
	if len(args) >= 2 && args[0] == "-f" {
		if !strings.Contains(stdin, "table inet "+ipAcctTable) {
			return nil, fmt.Errorf("ruleset does not define the accounting table")
		}
		f.rulesets = append(f.rulesets, stdin)
		return nil, nil
	}
	if len(args) < 5 || args[1] != "list" {
		return nil, nil
	}
	if f.failSet {
		return nil, fmt.Errorf("No such file or directory")
	}
	name := args[len(args)-1]
	round := f.rounds[min(f.reads/len(ipAcctSets), len(f.rounds)-1)]
	f.reads++
	return marshalNFTSet(name, round[name]), nil
}

func marshalNFTSet(name string, counters map[string]uint64) []byte {
	type counter struct {
		Bytes uint64 `json:"bytes"`
	}
	type elemBody struct {
		Val     string  `json:"val"`
		Counter counter `json:"counter"`
	}
	type elem struct {
		Elem elemBody `json:"elem"`
	}
	elems := make([]elem, 0, len(counters))
	for address, bytes := range counters {
		elems = append(elems, elem{Elem: elemBody{Val: address, Counter: counter{Bytes: bytes}}})
	}
	payload := map[string]any{
		"nftables": []any{
			map[string]any{"metainfo": map[string]any{"version": "1.0.9"}},
			map[string]any{"set": map[string]any{"name": name, "elem": elems}},
		},
	}
	out, _ := json.Marshal(payload)
	return out
}

func newFakeIPAccounting(nft *fakeNFT) *IPAccounting {
	return &IPAccounting{run: nft.run, previous: map[string]uint64{}}
}

func TestIPAccountingReportsDeltasPerDirection(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{
		{
			"peer_in4":  {"203.0.113.7": 1000},
			"peer_out4": {"203.0.113.7": 500},
		},
		{
			"peer_in4":  {"203.0.113.7": 1800, "198.51.100.4": 60},
			"peer_out4": {"203.0.113.7": 500},
			"peer_in6":  {"2001:db8::1": 40},
		},
	}}
	acct := newFakeIPAccounting(nft)

	first, err := acct.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// The table is created empty, so the first read is itself the delta.
	if got := first["203.0.113.7"]; got.InBytes != 1000 || got.OutBytes != 500 {
		t.Fatalf("first round = %#v", got)
	}

	second, err := acct.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := second["203.0.113.7"]; got.InBytes != 800 || got.OutBytes != 0 {
		t.Fatalf("second round = %#v, want only the 800 bytes since the first read", got)
	}
	if got := second["198.51.100.4"]; got.InBytes != 60 {
		t.Fatalf("new address = %#v", got)
	}
	if got := second["2001:db8::1"]; got.InBytes != 40 {
		t.Fatalf("IPv6 address = %#v", got)
	}
}

// nftables evicts an idle element and re-adds it from zero on the next packet,
// which must read as new traffic rather than as a negative delta.
func TestIPAccountingTreatsCounterRestartAsNewTraffic(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{
		{"peer_in4": {"203.0.113.7": 5000}},
		{"peer_in4": {"203.0.113.7": 120}},
	}}
	acct := newFakeIPAccounting(nft)
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	second, err := acct.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := second["203.0.113.7"]; got.InBytes != 120 {
		t.Fatalf("restarted counter = %#v, want the full 120 bytes", got)
	}
}

// A firewall reload can flush the table. The next round must rebuild it rather
// than reporting against counters that no longer exist.
func TestIPAccountingReinstallsRulesetAfterTheTableDisappears(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}, failSet: true}
	acct := newFakeIPAccounting(nft)
	if _, err := acct.Collect(context.Background()); err == nil {
		t.Fatal("Collect succeeded against a flushed table")
	}
	nft.failSet = false
	nft.reads = 0
	deltas, err := acct.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after reinstall: %v", err)
	}
	if got := deltas["203.0.113.7"]; got.InBytes != 700 {
		t.Fatalf("after reinstall = %#v", got)
	}
	loads := 0
	for _, command := range nft.commands {
		if strings.HasPrefix(command, "-f ") {
			loads++
		}
	}
	if loads != 2 {
		t.Fatalf("ruleset loads = %d, want the initial install plus the repair", loads)
	}
}

// A set with no counters lists bare address strings; that shape must be skipped
// rather than failing the whole listing.
func TestParseNFTCounterSetSkipsElementsWithoutCounters(t *testing.T) {
	raw := []byte(`{"nftables":[{"set":{"name":"peer_in4","elem":["203.0.113.7",
        {"elem":{"val":"198.51.100.4","counter":{"packets":3,"bytes":900}}}]}}]}`)
	counters, err := parseNFTCounterSet(raw)
	if err != nil {
		t.Fatalf("parseNFTCounterSet: %v", err)
	}
	if len(counters) != 1 || counters["198.51.100.4"] != 900 {
		t.Fatalf("counters = %#v", counters)
	}
}

func newIPTrafficTestMonitor(t *testing.T, now time.Time, nft *fakeNFT) *Monitor {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, Config{
		Alias:     "local",
		ResetDay:  15,
		ResetHour: 5,
		Now:       func() time.Time { return now },
	}, nil)
	m.pingCollector = nil
	m.ipAccounting = newFakeIPAccounting(nft)
	return m
}

func TestIPTrafficServesTopAddressesWithEveryWindow(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{
		"peer_in4":  {"203.0.113.7": 3000, "198.51.100.4": 100},
		"peer_out4": {"203.0.113.7": 1000},
	}}}
	m := newIPTrafficTestMonitor(t, now, nft)
	m.ipSampleOnce(context.Background(), now)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ip-traffic", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		IPTraffic IPTrafficSnapshot `json:"ipTraffic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snapshot := payload.IPTraffic
	if !snapshot.Enabled {
		t.Fatal("snapshot reports per-IP accounting as unavailable")
	}
	if want := CycleStart(now, 15, 5).Unix(); snapshot.CycleStart != want {
		t.Fatalf("CycleStart = %d, want %d", snapshot.CycleStart, want)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
	top := snapshot.Entries[0]
	if top.IP != "203.0.113.7" {
		t.Fatalf("top entry = %#v", top)
	}
	// One round inside today sits inside all three windows, so each of them
	// reports the same bytes broken out by direction.
	for name, window := range map[string]IPTrafficWindow{"cycle": top.Cycle, "today": top.Today, "last7": top.Last7} {
		if window.InBytes != 3000 || window.OutBytes != 1000 || window.TotalBytes != 4000 {
			t.Fatalf("%s window = %#v", name, window)
		}
	}
}

// Clicking an address asks for its own history, which is read at the same three
// granularities the node's traffic modal offers.
func TestIPDetailServesRecentHourlyAndDaily(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 3000}}}}
	m := newIPTrafficTestMonitor(t, now, nft)
	m.ipSampleOnce(context.Background(), now)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ip-detail?ip=203.0.113.7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		IPDetail IPTrafficDetail `json:"ipDetail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	detail := payload.IPDetail
	if detail.IP != "203.0.113.7" {
		t.Fatalf("detail = %#v", detail)
	}
	if len(detail.Recent) != 1 || detail.Recent[0].TS != now.Unix() || detail.Recent[0].InBytes != 3000 {
		t.Fatalf("recent = %#v", detail.Recent)
	}
	if len(detail.Hourly) != 1 || detail.Hourly[0].TS != now.Truncate(time.Hour).Unix() {
		t.Fatalf("hourly = %#v", detail.Hourly)
	}
	if len(detail.Daily) != 1 || detail.Daily[0].TS != now.Truncate(24*time.Hour).Unix() {
		t.Fatalf("daily = %#v", detail.Daily)
	}

	// Anything that is not an address is refused before it reaches the store.
	bad := httptest.NewRecorder()
	m.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/ip-detail?ip=not-an-address", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("status = %d for a non-address, want 400", bad.Code)
	}
}

// A client whose traffic this node forwards for a landing node is listed apart
// from the same address's direct traffic — marked as relayed in the table, and
// drilled into through the marked key.
func TestIPTrafficDistinguishesRelayedClients(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{
		"peer_in4":   {"203.0.113.7": 1000},
		"relay_in4":  {"203.0.113.7": 400},
		"relay_out4": {"203.0.113.7": 90},
	}}}
	m := newIPTrafficTestMonitor(t, now, nft)
	m.ipSampleOnce(context.Background(), now)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ip-traffic", nil))
	var listPayload struct {
		IPTraffic IPTrafficSnapshot `json:"ipTraffic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries := listPayload.IPTraffic.Entries
	if len(entries) != 2 {
		t.Fatalf("entries = %#v, want the direct and the relayed row", entries)
	}
	byKind := map[bool]IPTrafficEntry{}
	for _, entry := range entries {
		if entry.IP != "203.0.113.7" {
			t.Fatalf("entry address = %q, want the bare address on both rows", entry.IP)
		}
		byKind[entry.Relayed] = entry
	}
	if got := byKind[false].Cycle; got.InBytes != 1000 || got.OutBytes != 0 {
		t.Fatalf("direct cycle = %#v", got)
	}
	if got := byKind[true].Cycle; got.InBytes != 400 || got.OutBytes != 90 {
		t.Fatalf("relayed cycle = %#v", got)
	}

	detail := httptest.NewRecorder()
	m.Handler().ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/ip-detail?ip=relay:203.0.113.7", nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", detail.Code, detail.Body.String())
	}
	var detailPayload struct {
		IPDetail IPTrafficDetail `json:"ipDetail"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	series := detailPayload.IPDetail
	if series.IP != "203.0.113.7" || !series.Relayed {
		t.Fatalf("detail = %#v, want the bare address marked as relayed", series)
	}
	if len(series.Recent) != 1 || series.Recent[0].InBytes != 400 || series.Recent[0].OutBytes != 90 {
		t.Fatalf("recent = %#v, want only the relayed bytes", series.Recent)
	}
}

// Raw samples fold into hourly buckets on the same cutoff the node's own
// samples do, and a window total spans both tables so nothing is lost or
// counted twice across the fold.
func TestIPTrafficFoldsIntoHourlyWithoutChangingTotals(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 3000}}}}
	m := newIPTrafficTestMonitor(t, now, nft)
	old := now.Add(-3 * time.Hour)
	if err := m.store.AddIPTraffic(old.Unix(), map[string]IPTrafficDelta{"203.0.113.7": {InBytes: 500, OutBytes: 100}}); err != nil {
		t.Fatalf("AddIPTraffic: %v", err)
	}
	m.ipSampleOnce(context.Background(), now)

	before := topEntry(t, m, now)
	m.maintenance(now)
	after := topEntry(t, m, now)
	if before.Cycle != after.Cycle {
		t.Fatalf("cycle total changed across the fold: %#v -> %#v", before.Cycle, after.Cycle)
	}
	if after.Cycle.TotalBytes != 3600 {
		t.Fatalf("cycle total = %#v, want both rounds", after.Cycle)
	}
	// Today's window excludes nothing here, but the folded row now lives in the
	// hourly table, which is what proves the window reads both.
	var rawRows int
	if err := m.store.db.QueryRow(`SELECT COUNT(*) FROM ip_samples`).Scan(&rawRows); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if rawRows != 1 {
		t.Fatalf("raw rows = %d, want only the sample inside the retention window", rawRows)
	}
}

// Hours older than a week fold again into days. The fold is the thing that
// keeps the ranking query flat over a long quota cycle, so what it must not do
// is change any number the dashboard reads: the cycle total stays put, the
// shorter windows keep their own totals, and the hours that survive are exactly
// the ones inside the retention window.
func TestIPTrafficFoldsIntoDailyWithoutChangingTotals(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	today := now.Truncate(24 * time.Hour)
	cycle := today.AddDate(0, 0, -20).Unix()
	const address = "203.0.113.7"

	// One sample a day for three weeks, so the history straddles the cutoff.
	for day := 20; day >= 0; day-- {
		ts := today.AddDate(0, 0, -day).Add(9 * time.Hour).Unix()
		if err := store.AddIPTraffic(ts, map[string]IPTrafficDelta{address: {InBytes: 100, OutBytes: 20}}); err != nil {
			t.Fatalf("AddIPTraffic: %v", err)
		}
	}
	if err := store.AggregateIPHourly(now.Add(-rawRetention).Unix()); err != nil {
		t.Fatalf("AggregateIPHourly: %v", err)
	}
	before := ipEntry(t, store, address, cycle, today.Unix())

	cutoff := today.Add(-ipHourlyRetention).Unix()
	if err := store.AggregateIPDaily(cutoff); err != nil {
		t.Fatalf("AggregateIPDaily: %v", err)
	}
	after := ipEntry(t, store, address, cycle, today.Unix())
	if before != after {
		t.Fatalf("totals changed across the daily fold: %#v -> %#v", before, after)
	}
	if after.Cycle.TotalBytes != 21*120 {
		t.Fatalf("cycle total = %#v, want every day of the cycle", after.Cycle)
	}
	if after.Last7.TotalBytes != 7*120 {
		t.Fatalf("week total = %#v, want the seven days inside the window", after.Last7)
	}

	var oldestHour, hours, days int64
	if err := store.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MIN(ts_hour), 0) FROM ip_hourly`).Scan(&hours, &oldestHour); err != nil {
		t.Fatalf("count hourly: %v", err)
	}
	if oldestHour < cutoff {
		t.Fatalf("oldest surviving hour %d is older than the cutoff %d", oldestHour, cutoff)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ip_daily`).Scan(&days); err != nil {
		t.Fatalf("count daily: %v", err)
	}
	// The cutoff falls on midnight seven days back and the samples sit at 09:00,
	// so eight days of hours survive it and the other thirteen fold. What matters
	// is that every day is in exactly one tier.
	if hours != 8 || days != 13 {
		t.Fatalf("split = %d hours + %d days, want 8 + 13 covering all 21", hours, days)
	}

	// A day bucket carries a whole day of bytes, so letting one into the hourly
	// view would draw a spike at midnight that never happened.
	detail, err := store.IPTrafficSeries(address, now.Add(-rawRetention).Unix(), cycle)
	if err != nil {
		t.Fatalf("IPTrafficSeries: %v", err)
	}
	for _, point := range detail.Hourly {
		if point.TS < cutoff {
			t.Fatalf("hourly series reaches %d, older than the folded cutoff %d", point.TS, cutoff)
		}
	}
	if len(detail.Daily) != 21 {
		t.Fatalf("daily series = %d points, want every day of the cycle", len(detail.Daily))
	}
}

func ipEntry(t *testing.T, store *Store, address string, cycle, today int64) IPTrafficEntry {
	t.Helper()
	entries, err := store.TopIPTraffic(topIPTrafficEntries, cycle, today, today-6*86400)
	if err != nil {
		t.Fatalf("TopIPTraffic: %v", err)
	}
	for _, entry := range entries {
		if entry.IP == address {
			return entry
		}
	}
	t.Fatalf("entries = %#v, want %s", entries, address)
	return IPTrafficEntry{}
}

func topEntry(t *testing.T, m *Monitor, now time.Time) IPTrafficEntry {
	t.Helper()
	cycle := CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour).Unix()
	today := now.Truncate(24 * time.Hour).Unix()
	entries, err := m.store.TopIPTraffic(topIPTrafficEntries, cycle, today, today-6*86400)
	if err != nil || len(entries) == 0 {
		t.Fatalf("TopIPTraffic: %#v (%v)", entries, err)
	}
	return entries[0]
}

// The per-IP table describes one quota cycle, so crossing the reset boundary
// clears it exactly the way the traffic quota resets.
func TestIPTrafficClearsWhenTheQuotaCycleRolls(t *testing.T) {
	beforeReset := time.Date(2026, 6, 15, 4, 0, 0, 0, time.UTC)
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{
		{"peer_in4": {"203.0.113.7": 3000}},
		{"peer_in4": {"203.0.113.7": 3400}},
	}}
	m := newIPTrafficTestMonitor(t, beforeReset, nft)
	m.ipSampleOnce(context.Background(), beforeReset)
	if entry := topEntry(t, m, beforeReset); entry.Cycle.TotalBytes != 3000 {
		t.Fatalf("before reset: entry = %#v", entry)
	}

	// 05:00 on the 15th is this configuration's reset boundary.
	afterReset := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	m.ipSampleOnce(context.Background(), afterReset)
	if entry := topEntry(t, m, afterReset); entry.Cycle.TotalBytes != 400 {
		t.Fatalf("after reset: entry = %#v, want only the 400 bytes since the boundary", entry)
	}
}

// A host without nftables reports so, which lets the dashboard explain an empty
// list instead of implying the node saw no traffic.
func TestIPTrafficReportsDisabledWithoutNFT(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	m := newIPTrafficTestMonitor(t, now, &fakeNFT{})
	m.ipAccounting = nil

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ip-traffic", nil))
	var payload struct {
		IPTraffic IPTrafficSnapshot `json:"ipTraffic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.IPTraffic.Enabled {
		t.Fatal("snapshot reports per-IP accounting as available with no nft utility")
	}
}

func TestPruneIPTrafficKeepsOnlyTheBusiestAddresses(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	day := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC).Unix()
	for i := range 10 {
		address := fmt.Sprintf("203.0.113.%d", i+1)
		if err := store.AddIPTraffic(day, map[string]IPTrafficDelta{address: {InBytes: uint64(i+1) * 100}}); err != nil {
			t.Fatalf("AddIPTraffic: %v", err)
		}
	}
	// Half the history is folded, so the ranking has to span both tables.
	if err := store.AggregateIPHourly(day + 1); err != nil {
		t.Fatalf("AggregateIPHourly: %v", err)
	}
	if err := store.AddIPTraffic(day+3600, map[string]IPTrafficDelta{"203.0.113.10": {InBytes: 50}}); err != nil {
		t.Fatalf("AddIPTraffic: %v", err)
	}
	if err := store.PruneIPTraffic(3); err != nil {
		t.Fatalf("PruneIPTraffic: %v", err)
	}
	entries, err := store.TopIPTraffic(topIPTrafficEntries, day, day, day)
	if err != nil {
		t.Fatalf("TopIPTraffic: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want the three busiest", entries)
	}
	for i, want := range []string{"203.0.113.10", "203.0.113.9", "203.0.113.8"} {
		if entries[i].IP != want {
			t.Fatalf("entries[%d] = %q, want %q", i, entries[i].IP, want)
		}
	}
}

// A flow the relay DNATs is routed, not delivered, so the input/output chains
// never see it. The forward chain is what meters it, pinned to the relay's own
// flows by the DNAT status and the pre-rewrite listen port that survives in the
// conntrack original tuple, and attributed to the client: the source of an
// original packet, the destination of a reply.
func TestIPAccountingRulesetCountsRelayedFlowsByClientAddress(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	acct.relayPorts = func() []int { return []int{30001, 30002} }
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, want := range []string{
		"type filter hook forward priority 0; policy accept;",
		"ct status dnat ct original proto-dst { 30001, 30002 } ct direction original update @relay_in4 { ip saddr }",
		"ct status dnat ct original proto-dst { 30001, 30002 } ct direction reply update @relay_out4 { ip daddr }",
	} {
		if !strings.Contains(nft.rulesets[0], want) {
			t.Fatalf("ruleset is missing %q:\n%s", want, nft.rulesets[0])
		}
	}
}

// A node that relays for nobody renders no forward chain, but the relay sets
// are still declared so the collector can read all of them either way.
func TestIPAccountingRulesetOmitsTheForwardChainWithoutRelayPorts(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if strings.Contains(nft.rulesets[0], "hook forward") {
		t.Fatalf("ruleset carries a forward chain with no relay job:\n%s", nft.rulesets[0])
	}
	for _, want := range []string{"set relay_in4", "set relay_out4"} {
		if !strings.Contains(nft.rulesets[0], want) {
			t.Fatalf("ruleset is missing %q:\n%s", want, nft.rulesets[0])
		}
	}
}

// The same address can be a direct client and sit behind the relay at once, and
// the two histories must not blend: the relay sets report under the marked key.
func TestIPAccountingKeysRelayedTrafficApartFromDirect(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{
		"peer_in4":   {"203.0.113.7": 1000},
		"relay_in4":  {"203.0.113.7": 400},
		"relay_out4": {"203.0.113.7": 90},
	}}}
	acct := newFakeIPAccounting(nft)
	deltas, err := acct.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := deltas["203.0.113.7"]; got.InBytes != 1000 || got.OutBytes != 0 {
		t.Fatalf("direct delta = %#v", got)
	}
	if got := deltas["relay:203.0.113.7"]; got.InBytes != 400 || got.OutBytes != 90 {
		t.Fatalf("relayed delta = %#v", got)
	}
}

// The hub pushes and withdraws relay jobs between samples, so a changed port
// list has to rebuild the table the same way a changed overlay port does.
func TestIPAccountingRebuildsTheTableWhenTheRelayJobChanges(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	var ports []int
	acct.relayPorts = func() []int { return ports }
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(nft.rulesets) != 1 {
		t.Fatalf("ruleset loads = %d, want the empty job to be reused", len(nft.rulesets))
	}
	ports = []int{30001}
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(nft.rulesets) != 2 {
		t.Fatalf("ruleset loads = %d, want a rebuild for the new relay job", len(nft.rulesets))
	}
	if !strings.Contains(nft.rulesets[1], "ct original proto-dst { 30001 }") {
		t.Fatalf("rebuilt ruleset does not meter the new port:\n%s", nft.rulesets[1])
	}
}

// The overlay's tunnelled addresses are private, but the packets carrying them
// travel between public ones, so the private-range filters cannot keep the
// fleet's own control traffic out of a list of client addresses. The local
// WireGuard listen port is what does.
func TestIPAccountingRulesetSkipsTheOverlayTransport(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	acct.overlayPorts = func(context.Context) []int { return []int{51820} }
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(nft.rulesets) != 1 {
		t.Fatalf("ruleset loads = %d, want one", len(nft.rulesets))
	}
	for _, want := range []string{"udp dport { 51820 } return", "udp sport { 51820 } return"} {
		if !strings.Contains(nft.rulesets[0], want) {
			t.Fatalf("ruleset is missing %q:\n%s", want, nft.rulesets[0])
		}
	}
}

// A host with no WireGuard interface has nothing to exclude, and the rule is
// left out entirely rather than rendered against an empty set.
func TestIPAccountingRulesetOmitsTheOverlayRuleWithoutWireGuard(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if strings.Contains(nft.rulesets[0], "udp dport") {
		t.Fatalf("ruleset carries an overlay rule with no interface:\n%s", nft.rulesets[0])
	}
}

// wg-quick hands a restarted interface a new ephemeral port, so the table has
// to be rebuilt around it instead of counting the tunnel until the next
// monitor restart.
func TestIPAccountingRebuildsTheTableWhenTheOverlayPortChanges(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{{"peer_in4": {"203.0.113.7": 700}}}}
	acct := newFakeIPAccounting(nft)
	port := 45517
	acct.overlayPorts = func(context.Context) []int { return []int{port} }
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(nft.rulesets) != 1 {
		t.Fatalf("ruleset loads = %d, want the port to be reused", len(nft.rulesets))
	}
	port = 51821
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(nft.rulesets) != 2 {
		t.Fatalf("ruleset loads = %d, want a rebuild for the new port", len(nft.rulesets))
	}
	if !strings.Contains(nft.rulesets[1], "udp dport { 51821 } return") {
		t.Fatalf("rebuilt ruleset kept the old port:\n%s", nft.rulesets[1])
	}
}

// nftables evicts a set element after its idle timeout. The remembered counter
// has to go with it, or a node facing a long tail of one-off client addresses
// grows this map for as long as it runs.
func TestIPAccountingForgetsAddressesTheSetEvicted(t *testing.T) {
	nft := &fakeNFT{rounds: []map[string]map[string]uint64{
		{"peer_in4": {"203.0.113.7": 700, "203.0.113.8": 900}},
		{"peer_in4": {"203.0.113.8": 900}},
	}}
	acct := newFakeIPAccounting(nft)
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := acct.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, ok := acct.previous["peer_in4|203.0.113.7"]; ok {
		t.Fatalf("previous still remembers an evicted address: %#v", acct.previous)
	}
	if got := acct.previous["peer_in4|203.0.113.8"]; got != 900 {
		t.Fatalf("previous for a live address = %d, want 900", got)
	}
}

func TestParseWireGuardListenPorts(t *testing.T) {
	ports := parseWireGuardListenPorts("sbwg0\t51820\nwg1\t45517\nsbwg0\t51820\nbroken\n")
	if len(ports) != 2 || ports[0] != 45517 || ports[1] != 51820 {
		t.Fatalf("ports = %v, want the two distinct ports sorted", ports)
	}
	if got := parseWireGuardListenPorts(""); got != nil {
		t.Fatalf("ports = %v, want none for a host with no interface", got)
	}
}
