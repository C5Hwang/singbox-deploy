package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestMonitorSourceRoundTrip(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	sources := []MonitorSource{
		{Domain: "a.example.com", Alias: "US-a", MonitorPublicPort: 6002},
		{Domain: "b.example.com", Alias: "JP-b", MonitorPublicPort: 6003},
	}
	if err := SaveMonitorSources(layout, sources); err != nil {
		t.Fatalf("SaveMonitorSources: %v", err)
	}
	loaded, err := LoadMonitorSources(layout)
	if err != nil {
		t.Fatalf("LoadMonitorSources: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d sources, want 2", len(loaded))
	}
	for i, want := range sources {
		got := loaded[i]
		if got.Domain != want.Domain || got.Alias != want.Alias || got.MonitorPublicPort != want.MonitorPublicPort {
			t.Fatalf("source[%d] = %+v, want %+v", i, got, want)
		}
	}
}

func TestMigrateMonitorSources(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)

	remotes := []RemoteSubscription{
		{Domain: "a.example.com", Port: 9443, Alias: "US-a", Salt: "salt-a"},
		{Domain: "b.example.com", Port: 9444, Alias: "JP-b", Salt: "salt-b"},
	}
	if err := SaveRemoteSubscriptions(layout, remotes); err != nil {
		t.Fatalf("SaveRemoteSubscriptions: %v", err)
	}

	remotesDir := filepath.Join(layout.StateDir, "remotes")
	writeStateFile := func(dir, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
	}
	entries, _ := os.ReadDir(remotesDir)
	writeStateFile(filepath.Join(remotesDir, entries[1].Name()), "monitor", "yes")
	writeStateFile(filepath.Join(remotesDir, entries[1].Name()), "monitor_public_port", "6003")

	if err := MigrateMonitorSources(layout); err != nil {
		t.Fatalf("MigrateMonitorSources: %v", err)
	}
	sources, err := LoadMonitorSources(layout)
	if err != nil {
		t.Fatalf("LoadMonitorSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("migrated %d sources, want 1", len(sources))
	}
	if sources[0].Domain != "b.example.com" || sources[0].Alias != "JP-b" || sources[0].MonitorPublicPort != 6003 {
		t.Fatalf("migrated source = %+v", sources[0])
	}
}

func TestMigrateMonitorSourcesIdempotent(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)

	if err := SaveMonitorSources(layout, []MonitorSource{{Domain: "a.example.com", Alias: "US-a", MonitorPublicPort: 6002}}); err != nil {
		t.Fatalf("SaveMonitorSources: %v", err)
	}

	remotes := []RemoteSubscription{{Domain: "b.example.com", Port: 9443, Alias: "JP-b", Salt: "salt"}}
	if err := SaveRemoteSubscriptions(layout, remotes); err != nil {
		t.Fatalf("SaveRemoteSubscriptions: %v", err)
	}
	remotesDir := filepath.Join(layout.StateDir, "remotes")
	entries, _ := os.ReadDir(remotesDir)
	os.WriteFile(filepath.Join(remotesDir, entries[0].Name(), "monitor"), []byte("yes\n"), 0o600)
	os.WriteFile(filepath.Join(remotesDir, entries[0].Name(), "monitor_public_port"), []byte("7000\n"), 0o600)

	if err := MigrateMonitorSources(layout); err != nil {
		t.Fatalf("MigrateMonitorSources: %v", err)
	}
	sources, err := LoadMonitorSources(layout)
	if err != nil {
		t.Fatalf("LoadMonitorSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Domain != "a.example.com" {
		t.Fatalf("migration should be no-op, got %+v", sources)
	}
}

func TestRefreshRemoteMonitorToToleratesDeadSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote_monitor.json")
	sources := []MonitorSource{
		{Domain: "a.example.com", Alias: "US-a", MonitorPublicPort: 6002},
		{Domain: "b.example.com", Alias: "JP-b", MonitorPublicPort: 6003},
	}
	fetchFor := func(alive string) SubscriptionFetcher {
		return func(_ context.Context, url string) ([]byte, error) {
			if !strings.Contains(url, alive) {
				return nil, errors.New("connection refused")
			}
			payload := `{"totalUsedBytes": 111, "sources": [{"sampledAt": "2026-06-01T00:00:00Z"}]}`
			if alive == "b.example.com" {
				payload = `{"totalUsedBytes": 222, "sources": [{"sampledAt": "2026-06-01T01:00:00Z"}]}`
			}
			return []byte(payload), nil
		}
	}

	// Round 1: only A responds. The dead peer must not blank out A's data.
	if err := RefreshRemoteMonitorTo(context.Background(), path, sources, fetchFor("a.example.com")); err == nil {
		t.Fatal("expected aggregated fetch error for the dead source")
	}
	got, err := monitor.ReadRemoteSources(path)
	if err != nil {
		t.Fatalf("ReadRemoteSources: %v", err)
	}
	if len(got) != 1 || got[0].TotalUsedBytes != 111 || !strings.Contains(got[0].Name, "US-a") {
		t.Fatalf("round 1 snapshot = %#v", got)
	}

	// Round 2: B responds and A fails; A keeps its previous snapshot entry.
	if err := RefreshRemoteMonitorTo(context.Background(), path, sources, fetchFor("b.example.com")); err == nil {
		t.Fatal("expected aggregated fetch error for the dead source")
	}
	got, err = monitor.ReadRemoteSources(path)
	if err != nil {
		t.Fatalf("ReadRemoteSources: %v", err)
	}
	if len(got) != 2 || got[0].TotalUsedBytes != 111 || got[1].TotalUsedBytes != 222 {
		t.Fatalf("round 2 snapshot = %#v", got)
	}
}

func TestValidateMonitorSources(t *testing.T) {
	tests := []struct {
		name    string
		sources []MonitorSource
		wantErr bool
	}{
		{"valid", []MonitorSource{{Domain: "a.com", Alias: "A", MonitorPublicPort: 443}}, false},
		{"empty domain", []MonitorSource{{Domain: "", Alias: "A", MonitorPublicPort: 443}}, true},
		{"empty alias", []MonitorSource{{Domain: "a.com", Alias: "", MonitorPublicPort: 443}}, false},
		{"zero port", []MonitorSource{{Domain: "a.com", Alias: "A", MonitorPublicPort: 0}}, true},
		{"port too high", []MonitorSource{{Domain: "a.com", Alias: "A", MonitorPublicPort: 70000}}, true},
		{"duplicate", []MonitorSource{
			{Domain: "a.com", Alias: "A", MonitorPublicPort: 443},
			{Domain: "a.com", Alias: "B", MonitorPublicPort: 443},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMonitorSources(tt.sources)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateMonitorSources() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
