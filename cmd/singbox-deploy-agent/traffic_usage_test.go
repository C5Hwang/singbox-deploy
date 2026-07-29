package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestAgentTrafficUsageRoundTripWhileMonitorDisabled(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName: "Spoke", SiteTemplate: deploy.DefaultSiteTemplate,
		Salt:          "traffic-usage-test",
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
	req := nodeapi.TrafficUsageRequest{
		InBytes: 123, OutBytes: 456, ExpectedCycleStart: current.CycleStart,
	}
	updated, err := h.SetTrafficUsage(context.Background(), req)
	if err != nil {
		t.Fatalf("SetTrafficUsage: %v", err)
	}
	if updated.Previous != current ||
		updated.Applied.InBytes != req.InBytes || updated.Applied.OutBytes != req.OutBytes ||
		updated.Applied.CycleStart != current.CycleStart {
		t.Fatalf("updated usage = %+v", updated)
	}
	got, err := h.TrafficUsage(context.Background())
	if err != nil || got != updated.Applied {
		t.Fatalf("round-trip usage = %+v, err=%v", got, err)
	}

	stale := req
	stale.InBytes = 999
	stale.ExpectedCycleStart--
	if _, err := h.SetTrafficUsage(context.Background(), stale); !nodeapi.IsTrafficCycleConflict(err) {
		t.Fatalf("stale cycle error = %v", err)
	}
	got, err = h.TrafficUsage(context.Background())
	if err != nil || got != updated.Applied {
		t.Fatalf("stale cycle changed usage: %+v, err=%v", got, err)
	}
}

func TestAgentTrafficUsageImmediatelyReconcilesActiveMonitorQuota(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName: "Spoke", SiteTemplate: deploy.DefaultSiteTemplate,
		Salt:          "traffic-usage-test",
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
	supervisor := &monitorSupervisor{active: active, store: store, cfg: monitorCfg}
	h := &agentHandler{layout: layout, monitor: supervisor}
	cycleStart := monitor.CycleStart(now, 1, 0).Unix()

	high, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 60, OutBytes: 50, ExpectedCycleStart: cycleStart,
	})
	if err != nil {
		t.Fatalf("set usage above quota: %v", err)
	}
	if high.Applied.InBytes != 60 || high.Applied.OutBytes != 50 ||
		control.stops != 1 || control.active {
		t.Fatalf("high usage=%+v control=%+v", high, control)
	}
	stopped, err := store.QuotaStopped()
	if err != nil || !stopped {
		t.Fatalf("quota marker after stop=%v err=%v", stopped, err)
	}

	low, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 10, OutBytes: 20, ExpectedCycleStart: cycleStart,
	})
	if err != nil {
		t.Fatalf("set usage below quota: %v", err)
	}
	if low.Previous != high.Applied || low.Applied.InBytes != 10 ||
		low.Applied.OutBytes != 20 || control.starts != 1 || !control.active {
		t.Fatalf("low usage=%+v control=%+v", low, control)
	}
	stopped, err = store.QuotaStopped()
	if err != nil || stopped {
		t.Fatalf("quota marker after release=%v err=%v", stopped, err)
	}
}

func TestAgentTrafficUsagePropagatesQuotaInspectionFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName: "Spoke", SiteTemplate: deploy.DefaultSiteTemplate,
		Salt:          "traffic-usage-test",
		DeployMonitor: true, MonitorPort: deploy.DefaultMonitorPort,
		MonitorInterface: "lo", MonitorIntervalSeconds: 60,
		TrafficTotalLimitBytes: 1, ResetDay: 1,
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
	controlErr := errors.New("systemd unavailable")
	control := &trafficUsageServiceController{active: true, inspectErr: controlErr}
	monitorCfg := monitor.Config{
		TotalLimitBytes: 1, ResetDay: 1, Now: func() time.Time { return now },
	}
	active := monitor.New(store, monitorCfg, control)
	h := &agentHandler{
		layout:  layout,
		monitor: &monitorSupervisor{active: active, store: store, cfg: monitorCfg},
	}
	update, err := h.SetTrafficUsage(context.Background(), nodeapi.TrafficUsageRequest{
		InBytes: 2, ExpectedCycleStart: monitor.CycleStart(now, 1, 0).Unix(),
	})
	if err != nil || !strings.Contains(update.Warning, controlErr.Error()) ||
		update.Applied.InBytes != 2 {
		t.Fatalf("quota inspection update=%+v error=%v", update, err)
	}
}

func TestSystemdSingBoxIsActiveUsesExplicitStates(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		runErr     error
		wantActive bool
		wantErr    string
	}{
		{name: "active", output: "LoadState=loaded\nActiveState=active\n", wantActive: true},
		{name: "activating", output: "LoadState=loaded\nActiveState=activating\n", wantActive: true},
		{name: "deactivating", output: "LoadState=loaded\nActiveState=deactivating\n", wantActive: true},
		{name: "inactive", output: "LoadState=loaded\nActiveState=inactive\n"},
		{name: "failed", output: "LoadState=loaded\nActiveState=failed\n"},
		{name: "missing unit", output: "LoadState=not-found\nActiveState=inactive\n", wantErr: "LoadState"},
		{name: "missing state", output: "LoadState=loaded\n", wantErr: "ActiveState is missing"},
		{name: "unknown state", output: "LoadState=loaded\nActiveState=frozen\n", wantErr: "unknown"},
		{name: "systemctl failure", runErr: errors.New("D-Bus unavailable"), wantErr: "D-Bus unavailable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotArgs []string
			control := systemdSingBox{
				systemctlOutput: func(args ...string) ([]byte, error) {
					gotArgs = append([]string(nil), args...)
					return []byte(tc.output), tc.runErr
				},
			}
			active, err := control.IsActive()
			if active != tc.wantActive {
				t.Fatalf("IsActive = %v, want %v", active, tc.wantActive)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("IsActive error = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("IsActive error = %v, want substring %q", err, tc.wantErr)
			}
			if len(gotArgs) != 4 || gotArgs[0] != "show" ||
				gotArgs[1] != "--property=LoadState" ||
				gotArgs[2] != "--property=ActiveState" ||
				gotArgs[3] != "sing-box.service" {
				t.Fatalf("systemctl args = %q", gotArgs)
			}
		})
	}
}

type trafficUsageServiceController struct {
	active     bool
	starts     int
	stops      int
	inspectErr error
}

func (c *trafficUsageServiceController) Start() error {
	c.starts++
	c.active = true
	return nil
}

func (c *trafficUsageServiceController) Stop() error {
	c.stops++
	c.active = false
	return nil
}

func (c *trafficUsageServiceController) IsActive() (bool, error) {
	return c.active, c.inspectErr
}
