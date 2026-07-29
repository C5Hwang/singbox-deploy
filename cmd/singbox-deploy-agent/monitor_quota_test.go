package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func setAgentQuotaMarker(t *testing.T, dbPath string, stopped bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("create monitor store directory: %v", err)
	}
	store, err := monitor.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open monitor store: %v", err)
	}
	if err := store.SetQuotaStopped(stopped); err != nil {
		store.Close()
		t.Fatalf("set quota stop marker: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close monitor store: %v", err)
	}
}

func agentQuotaMarker(t *testing.T, dbPath string) bool {
	t.Helper()
	store, err := monitor.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open monitor store: %v", err)
	}
	defer store.Close()
	stopped, err := store.QuotaStopped()
	if err != nil {
		t.Fatalf("read quota stop marker: %v", err)
	}
	return stopped
}

func TestSpokeMonitorDisableReleasesQuotaOwnership(t *testing.T) {
	startErr := errors.New("sing-box start failed")
	tests := []struct {
		name       string
		marker     bool
		startErr   error
		wantStarts int
		wantMarker bool
		wantRetry  bool
	}{
		{name: "marker unset"},
		{name: "owned stop restored", marker: true, wantStarts: 1},
		{name: "failed restore retains ownership", marker: true, startErr: startErr, wantStarts: 1, wantMarker: true, wantRetry: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout := installedSpokeLayout(t)
			if err := state.NewStore(layout.StateDir).WriteString("monitor", "no\n", 0o600); err != nil {
				t.Fatalf("disable monitor state: %v", err)
			}
			setAgentQuotaMarker(t, layout.MonitorDB, tt.marker)

			starts := 0
			supervisor := newMonitorSupervisor(context.Background(), layout)
			t.Cleanup(supervisor.stop)
			supervisor.startSingBox = func() error {
				starts++
				return tt.startErr
			}
			supervisor.reload()

			if starts != tt.wantStarts {
				t.Fatalf("sing-box starts = %d, want %d", starts, tt.wantStarts)
			}
			if got := agentQuotaMarker(t, layout.MonitorDB); got != tt.wantMarker {
				t.Fatalf("quota stop marker = %v, want %v", got, tt.wantMarker)
			}
			supervisor.mu.RLock()
			handler := supervisor.handler
			supervisor.mu.RUnlock()
			if handler != nil {
				t.Fatal("disabled spoke monitor is still serving")
			}
			supervisor.lifecycle.Lock()
			hasRetry := supervisor.retryTimer != nil
			supervisor.lifecycle.Unlock()
			if hasRetry != tt.wantRetry {
				t.Fatalf("quota release retry scheduled = %v, want %v", hasRetry, tt.wantRetry)
			}
		})
	}
}
