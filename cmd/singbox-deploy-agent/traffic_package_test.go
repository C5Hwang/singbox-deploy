package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// With the monitor switched off the Agent keeps the package in the store on
// its own, and a later usage replacement that names no package leaves it be.
func TestAgentGrantsTrafficPackagesWhileMonitorDisabled(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName: "Spoke", SiteTemplate: deploy.DefaultSiteTemplate,
		Salt:          "traffic-package-test",
		DeployMonitor: false, ResetDay: 1, ResetHour: 0,
	}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{layout: layout}
	current, err := h.TrafficUsage(context.Background())
	if err != nil {
		t.Fatalf("TrafficUsage: %v", err)
	}
	if current.Package != (nodeapi.TrafficPackage{}) {
		t.Fatalf("fresh spoke reports a package: %+v", current)
	}

	granted, err := h.GrantTrafficPackage(context.Background(), nodeapi.TrafficPackageGrant{
		InBytes: 100, TotalBytes: 300, ExpectedCycleStart: current.CycleStart,
	})
	if err != nil {
		t.Fatalf("GrantTrafficPackage: %v", err)
	}
	if granted.Previous != current || granted.Applied.Package != (nodeapi.TrafficPackage{InBytes: 100, TotalBytes: 300}) ||
		granted.Applied.InBytes != current.InBytes || granted.Applied.CycleStart != current.CycleStart {
		t.Fatalf("grant = %+v", granted)
	}
	again, err := h.GrantTrafficPackage(context.Background(), nodeapi.TrafficPackageGrant{
		OutBytes: 5, ExpectedCycleStart: current.CycleStart,
	})
	if err != nil || again.Applied.Package != (nodeapi.TrafficPackage{InBytes: 100, OutBytes: 5, TotalBytes: 300}) {
		t.Fatalf("second grant stacks: %+v err=%v", again, err)
	}

	stale := nodeapi.TrafficPackageGrant{InBytes: 1, ExpectedCycleStart: current.CycleStart - 1}
	if _, err := h.GrantTrafficPackage(context.Background(), stale); !nodeapi.IsTrafficCycleConflict(err) {
		t.Fatalf("stale cycle error = %v", err)
	}

	replaced, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 7, OutBytes: 8, ExpectedCycleStart: current.CycleStart,
	})
	if err != nil || replaced.Applied.Package != again.Applied.Package || replaced.Applied.InBytes != 7 {
		t.Fatalf("usage replacement without a package: %+v err=%v", replaced, err)
	}
	corrected, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 7, OutBytes: 8, ExpectedCycleStart: current.CycleStart,
		Package: &nodeapi.TrafficPackage{TotalBytes: 1},
	})
	if err != nil || corrected.Previous.Package != again.Applied.Package ||
		corrected.Applied.Package != (nodeapi.TrafficPackage{TotalBytes: 1}) {
		t.Fatalf("usage replacement with a package: %+v err=%v", corrected, err)
	}
	got, err := h.TrafficUsage(context.Background())
	if err != nil || got != corrected.Applied {
		t.Fatalf("round-trip = %+v, err=%v", got, err)
	}
}

// With the monitor running, a grant that covers the overrun restarts sing-box
// before the Hub's call returns.
func TestAgentTrafficPackageImmediatelyReleasesAQuotaStop(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName: "Spoke", SiteTemplate: deploy.DefaultSiteTemplate,
		Salt:          "traffic-package-test",
		DeployMonitor: true, MonitorPort: deploy.DefaultMonitorPort,
		MonitorInterface: "lo", MonitorIntervalSeconds: 60,
		TrafficTotalLimitBytes: 100, ResetDay: 1, ResetHour: 0,
	}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.MonitorDB), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := monitor.OpenStore(layout.MonitorDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC)
	control := &trafficUsageServiceController{active: true}
	monitorCfg := monitor.Config{
		TotalLimitBytes: 100, ResetDay: 1, ResetHour: 0,
		Now: func() time.Time { return now },
	}
	active := monitor.New(store, monitorCfg, control)
	h := &agentHandler{layout: layout, monitor: &monitorSupervisor{active: active, store: store, cfg: monitorCfg}}
	cycleStart := monitor.CycleStart(now, 1, 0).Unix()

	if _, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 90, OutBytes: 30, ExpectedCycleStart: cycleStart,
	}); err != nil {
		t.Fatal(err)
	}
	if control.stops != 1 || control.active {
		t.Fatalf("precondition: 120 of 100 should stop sing-box; control=%+v", control)
	}

	update, err := h.GrantTrafficPackage(context.Background(), nodeapi.TrafficPackageGrant{
		TotalBytes: 50, ExpectedCycleStart: cycleStart,
	})
	if err != nil {
		t.Fatalf("GrantTrafficPackage: %v", err)
	}
	if update.Applied.Package != (nodeapi.TrafficPackage{TotalBytes: 50}) || update.Applied.InBytes != 90 ||
		update.Warning != "" || control.starts != 1 || !control.active {
		t.Fatalf("grant=%+v control=%+v", update, control)
	}
	if stopped, err := store.QuotaStopped(); err != nil || stopped {
		t.Fatalf("quota marker after grant=%v err=%v", stopped, err)
	}
	if got, err := h.TrafficUsage(context.Background()); err != nil || got != update.Applied {
		t.Fatalf("usage after grant = %+v, err=%v", got, err)
	}
}
