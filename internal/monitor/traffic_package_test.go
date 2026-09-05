package monitor

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficLimitsWithPackageExtendsOnlyLimitedDirections(t *testing.T) {
	limits := TrafficLimits{InBytes: 100, TotalBytes: 300}
	got := limits.WithPackage(TrafficPackage{InBytes: 50, OutBytes: 70, TotalBytes: 20})
	if got != (TrafficLimits{InBytes: 150, OutBytes: 0, TotalBytes: 320}) {
		t.Fatalf("WithPackage = %+v", got)
	}
	// A grant to an unlimited direction must not turn it into a limit.
	if (TrafficLimits{InBytes: 100}).WithPackage(TrafficPackage{OutBytes: 70}).Exceeded(TrafficTotals{OutBytes: 1 << 40}) {
		t.Fatal("an unlimited direction must stay unlimited under a package")
	}
	saturated := TrafficLimits{InBytes: math.MaxUint64 - 1}.WithPackage(TrafficPackage{InBytes: 10})
	if saturated.InBytes != math.MaxUint64 {
		t.Fatalf("saturating extend = %d", saturated.InBytes)
	}
}

func TestStorePackageGrantsAndCorrectionsSumFromTheCycleStart(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cycle := CycleStart(now, 1).Unix()

	pkg, err := store.PackageSince(cycle)
	if err != nil || !pkg.IsZero() {
		t.Fatalf("empty package = %+v, err=%v", pkg, err)
	}
	granted, err := store.AddPackageSince(cycle, now.Unix(), TrafficPackage{InBytes: 100, TotalBytes: 500})
	if err != nil || granted != (TrafficPackage{InBytes: 100, TotalBytes: 500}) {
		t.Fatalf("first grant = %+v, err=%v", granted, err)
	}
	granted, err = store.AddPackageSince(cycle, now.Unix()+60, TrafficPackage{InBytes: 50, OutBytes: 30})
	if err != nil || granted != (TrafficPackage{InBytes: 150, OutBytes: 30, TotalBytes: 500}) {
		t.Fatalf("second grant stacks on the first: %+v, err=%v", granted, err)
	}
	previous, err := store.ReplacePackageSince(cycle, now.Unix()+120, TrafficPackage{InBytes: 10, OutBytes: 30, TotalBytes: 700})
	if err != nil || previous != (TrafficPackage{InBytes: 150, OutBytes: 30, TotalBytes: 500}) {
		t.Fatalf("replace reports the previous package: %+v, err=%v", previous, err)
	}
	pkg, err = store.PackageSince(cycle)
	if err != nil || pkg != (TrafficPackage{InBytes: 10, OutBytes: 30, TotalBytes: 700}) {
		t.Fatalf("package after replace = %+v, err=%v", pkg, err)
	}

	// The next cycle starts from nothing: that is what makes a package
	// temporary without anyone having to take it away.
	next := NextCycleReset(now, 1).Unix()
	pkg, err = store.PackageSince(next)
	if err != nil || !pkg.IsZero() {
		t.Fatalf("next cycle package = %+v, err=%v", pkg, err)
	}

	// Cleanup drops grants past retention like every other adjustment row.
	if err := store.Cleanup(now.Unix() + 1000); err != nil {
		t.Fatal(err)
	}
	pkg, err = store.PackageSince(cycle)
	if err != nil || !pkg.IsZero() {
		t.Fatalf("package after cleanup = %+v, err=%v", pkg, err)
	}
}

func TestStorePackageSurvivesReopeningAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the table to stand in for a database written by a release that had
	// no packages; reopening must add it back without touching the rest.
	if _, err := store.db.Exec(`DROP TABLE packages`); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSample(100, "eth0", 10, 20, 10, 20); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen a pre-package database: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.AddPackageSince(0, 200, TrafficPackage{TotalBytes: 5}); err != nil {
		t.Fatalf("grant on a migrated database: %v", err)
	}
	totals, err := reopened.TotalsSince(0)
	if err != nil || totals != (TrafficTotals{InBytes: 10, OutBytes: 20}) {
		t.Fatalf("usage after migration = %+v, err=%v", totals, err)
	}
}

// A package extends the quota: usage that stopped sing-box is under quota
// again once the package lands, and the stop is released at once.
func TestGrantTrafficPackageReleasesAQuotaStopImmediately(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	cycleStart := CycleStart(now, 1)
	if err := store.InsertSample(cycleStart.Unix()+100, "eth0", 80, 40, 80, 40); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{active: true}
	m := New(store, Config{
		TotalLimitBytes: 100, ResetDay: 1, Now: func() time.Time { return now },
	}, ctrl)

	m.enforceQuota(now)
	if ctrl.stops != 1 || !m.stoppedByQuota {
		t.Fatalf("precondition: 120 used of 100 should stop sing-box; stops=%d stopped=%v", ctrl.stops, m.stoppedByQuota)
	}

	update, err := m.GrantTrafficPackage(cycleStart.Unix(), TrafficPackage{TotalBytes: 50})
	if err != nil {
		t.Fatalf("GrantTrafficPackage: %v", err)
	}
	if update.Previous.Package != (TrafficPackage{}) || update.Applied.Package != (TrafficPackage{TotalBytes: 50}) ||
		update.Applied.Totals != (TrafficTotals{InBytes: 80, OutBytes: 40}) || update.Applied.CycleStart != cycleStart {
		t.Fatalf("grant update = %+v", update)
	}
	if ctrl.starts != 1 || m.stoppedByQuota {
		t.Fatalf("a package that covers the overrun must restart sing-box: starts=%d stopped=%v", ctrl.starts, m.stoppedByQuota)
	}
	if stopped, err := store.QuotaStopped(); err != nil || stopped {
		t.Fatalf("quota marker after grant = %v, err=%v", stopped, err)
	}

	// The next sample keeps enforcing against the extended limit.
	if err := store.InsertSample(now.Unix()+60, "eth0", 120, 50, 40, 10); err != nil {
		t.Fatal(err)
	}
	m.enforceQuota(now)
	if ctrl.stops != 2 {
		t.Fatalf("170 used of 150 should stop again; stops=%d", ctrl.stops)
	}

	// A grant that names the wrong cycle changes nothing.
	if _, err := m.GrantTrafficPackage(cycleStart.AddDate(0, -1, 0).Unix(), TrafficPackage{TotalBytes: 1}); !errors.Is(err, ErrTrafficCycleChanged) {
		t.Fatalf("stale cycle error = %v", err)
	}
	usage, err := m.CurrentTrafficUsage()
	if err != nil || usage.Package != (TrafficPackage{TotalBytes: 50}) {
		t.Fatalf("stale grant changed the package: %+v err=%v", usage, err)
	}
}

func TestSetCurrentTrafficUsageReplacesThePackageOnlyWhenGiven(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()

	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	cycleStart := CycleStart(now, 1).Unix()
	m := New(store, Config{TotalLimitBytes: 100, ResetDay: 1, Now: func() time.Time { return now }}, &fakeController{active: true})

	update, err := m.SetCurrentTrafficUsage(cycleStart, TrafficTotals{InBytes: 1}, &TrafficPackage{InBytes: 5, TotalBytes: 40})
	if err != nil {
		t.Fatal(err)
	}
	if update.Previous.Package != (TrafficPackage{}) || update.Applied.Package != (TrafficPackage{InBytes: 5, TotalBytes: 40}) {
		t.Fatalf("package replacement = %+v", update)
	}

	// A Hub built before packages sends no package, and must not take the
	// one it does not know about away.
	update, err = m.SetCurrentTrafficUsage(cycleStart, TrafficTotals{InBytes: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if update.Previous.Package != (TrafficPackage{InBytes: 5, TotalBytes: 40}) || update.Applied.Package != update.Previous.Package ||
		update.Applied.Totals != (TrafficTotals{InBytes: 2}) {
		t.Fatalf("usage-only replacement = %+v", update)
	}
}

// The summary reports the limit in force — configured plus package — in the
// limit fields every reader already compares usage against, and the package's
// share beside it for readers that draw the two apart.
func TestSummaryReportsTheLimitInForceAndThePackageShare(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cycle := CycleStart(now, 1).Unix()
	if err := store.InsertSample(cycle+100, "eth0", 250, 10, 250, 10); err != nil {
		t.Fatal(err)
	}
	// Granted on every direction, but outbound is unlimited here, so that
	// share is neither reported nor counted.
	if _, err := store.AddPackageSince(cycle, cycle+200, TrafficPackage{InBytes: 100, OutBytes: 100, TotalBytes: 60}); err != nil {
		t.Fatal(err)
	}
	m := New(store, Config{
		Alias: "local", InLimitBytes: 200, TotalLimitBytes: 300, ResetDay: 1,
		Now: func() time.Time { return now },
	}, nil)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	local := got.Sources[0]
	if local.InLimitBytes != 300 || local.InPackageBytes != 100 || local.InRemainingBytes != 50 ||
		local.OutLimitBytes != 0 || local.OutPackageBytes != 0 || local.OutRemainingBytes != 0 ||
		local.TotalLimitBytes != 360 || local.TotalPackageBytes != 60 || local.TotalRemainingBytes != 100 {
		t.Fatalf("local source = %+v", local)
	}
	if got.InLimitBytes != 300 || got.InPackageBytes != 100 || got.TotalLimitBytes != 360 || got.TotalPackageBytes != 60 {
		t.Fatalf("top-level summary = %+v", got)
	}
	// The wire keeps the package out of a node that has none, so an older
	// hub's decoder sees exactly the document it always saw.
	if _, err := store.ReplacePackageSince(cycle, cycle+300, TrafficPackage{}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["inPackageBytes"]; present {
		t.Fatalf("an empty package must be omitted from the wire: %s", rec.Body.String())
	}
}

func TestReplaceAndGrantCurrentTrafficUsageRefuseAStaleCycle(t *testing.T) {
	store, cleanup := tempStore(t)
	defer cleanup()
	now := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	stale := CycleStart(now, 1).AddDate(0, -1, 0).Unix()
	if _, err := ReplaceCurrentTrafficUsage(store, now, 1, 0, stale, TrafficTotals{InBytes: 1}, nil); !errors.Is(err, ErrTrafficCycleChanged) {
		t.Fatalf("replace with a stale cycle = %v", err)
	}
	if _, err := GrantCurrentTrafficPackage(store, now, 1, 0, stale, TrafficPackage{InBytes: 1}); !errors.Is(err, ErrTrafficCycleChanged) {
		t.Fatalf("grant with a stale cycle = %v", err)
	}
	usage, err := ReadCurrentTrafficUsage(store, now, 1, 0)
	if err != nil || usage.Totals != (TrafficTotals{}) || !usage.Package.IsZero() {
		t.Fatalf("stale requests changed the cycle: %+v err=%v", usage, err)
	}
}
