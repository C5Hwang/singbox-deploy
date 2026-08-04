package submigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func installedLayout(t *testing.T) paths.Layout {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(layout.StateDir).WriteString("domain", "example.com\n", 0o600); err != nil {
		t.Fatalf("write managed domain: %v", err)
	}
	return layout
}

func TestEnsureCurrentRegeneratesOnceAndMarksSchema(t *testing.T) {
	layout := installedLayout(t)
	calls := 0
	regenerate := func(context.Context) error {
		calls++
		return os.WriteFile(filepath.Join(layout.Root, "regenerated"), []byte("yes"), 0o600)
	}

	migrated, err := EnsureCurrent(context.Background(), layout, regenerate)
	if err != nil || !migrated {
		t.Fatalf("first migration = %v, %v", migrated, err)
	}
	migrated, err = EnsureCurrent(context.Background(), layout, regenerate)
	if err != nil || migrated {
		t.Fatalf("second migration = %v, %v", migrated, err)
	}
	if calls != 1 {
		t.Fatalf("regenerator calls = %d, want 1", calls)
	}
	marker, err := state.NewStore(layout.StateDir).ReadValue(markerName, true)
	if err != nil || marker != "2" {
		t.Fatalf("schema marker = %q, %v", marker, err)
	}
}

func TestEnsureCurrentMigratesPreviousSchema(t *testing.T) {
	layout := installedLayout(t)
	store := state.NewStore(layout.StateDir)
	if err := store.WriteString(markerName, "1\n", 0o600); err != nil {
		t.Fatalf("write previous schema marker: %v", err)
	}
	calls := 0
	migrated, err := EnsureCurrent(context.Background(), layout, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil || !migrated {
		t.Fatalf("previous schema migration = %v, %v", migrated, err)
	}
	if calls != 1 {
		t.Fatalf("regenerator calls = %d, want 1", calls)
	}
	marker, err := store.ReadValue(markerName, true)
	if err != nil || marker != "2" {
		t.Fatalf("schema marker = %q, %v", marker, err)
	}
}

func TestEnsureCurrentRetriesAfterRegenerationFailure(t *testing.T) {
	layout := installedLayout(t)
	wantErr := errors.New("render failed")
	if migrated, err := EnsureCurrent(context.Background(), layout, func(context.Context) error { return wantErr }); migrated || !errors.Is(err, wantErr) {
		t.Fatalf("failed migration = %v, %v", migrated, err)
	}
	if _, err := os.Stat(filepath.Join(layout.StateDir, markerName)); !os.IsNotExist(err) {
		t.Fatalf("marker written after failure: %v", err)
	}
	if migrated, err := EnsureCurrent(context.Background(), layout, func(context.Context) error { return nil }); err != nil || !migrated {
		t.Fatalf("retry migration = %v, %v", migrated, err)
	}
}

func TestEnsureCurrentSkipsUninstalledLayout(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	called := false
	migrated, err := EnsureCurrent(context.Background(), layout, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || migrated || called {
		t.Fatalf("uninstalled migration = %v, %v, called=%v", migrated, err, called)
	}
}

func TestEnsureCurrentSerializesConcurrentProcesses(t *testing.T) {
	layout := installedLayout(t)
	start := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	regenerate := func(context.Context) error {
		if calls.Add(1) == 1 {
			close(start)
			<-release
		}
		return nil
	}
	type result struct {
		migrated bool
		err      error
	}
	results := make(chan result, 2)
	go func() {
		migrated, err := EnsureCurrent(context.Background(), layout, regenerate)
		results <- result{migrated: migrated, err: err}
	}()
	<-start
	go func() {
		migrated, err := EnsureCurrent(context.Background(), layout, regenerate)
		results <- result{migrated: migrated, err: err}
	}()
	close(release)

	migratedCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent migration: %v", result.err)
		}
		if result.migrated {
			migratedCount++
		}
	}
	if calls.Load() != 1 || migratedCount != 1 {
		t.Fatalf("regenerator calls=%d migrated results=%d, want 1/1", calls.Load(), migratedCount)
	}
}
