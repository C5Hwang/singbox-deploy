// Package submigration coordinates one-time regeneration of persisted
// subscription outputs after an embedded template schema changes.
package submigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	markerName = "subscription_schema_version"
	lockName   = ".subscription-schema.lock"
	// currentVersion must be incremented whenever an embedded subscription
	// template change requires already-persisted outputs to be regenerated.
	currentVersion = 1
)

// EnsureCurrent runs regenerate exactly once for an installed layout whose
// persisted subscription schema predates currentVersion. The marker is written
// only after regeneration succeeds, so a later process can retry a partial or
// failed migration. A cross-process lock prevents the Hub TUI and monitor (or
// two concurrent Agent starts) from rewriting the same public files together.
func EnsureCurrent(ctx context.Context, layout paths.Layout, regenerate func(context.Context) error) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if regenerate == nil {
		return false, fmt.Errorf("subscription migration regenerator is required")
	}
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}

	store := state.NewStore(layout.StateDir)
	domain, err := store.ReadValue("domain", false)
	if err != nil {
		return false, fmt.Errorf("read managed domain before subscription migration: %w", err)
	}
	if strings.TrimSpace(domain) == "" {
		// Bootstrap starts both binaries before a managed deployment exists. The
		// ordinary install path already renders current outputs, and a later
		// process start can record the marker once state is complete.
		return false, nil
	}

	unlock, err := lock(ctx, layout.StateDir)
	if err != nil {
		return false, err
	}
	defer unlock()

	version, err := readVersion(store)
	if err != nil {
		return false, err
	}
	if version >= currentVersion {
		return false, nil
	}
	if err := regenerate(ctx); err != nil {
		return false, fmt.Errorf("regenerate persisted subscriptions for schema %d: %w", currentVersion, err)
	}
	if err := store.WriteString(markerName, strconv.Itoa(currentVersion)+"\n", 0o600); err != nil {
		return false, fmt.Errorf("write subscription schema marker: %w", err)
	}
	return true, nil
}

func readVersion(store state.Store) (int, error) {
	raw, err := store.ReadValue(markerName, false)
	if err != nil {
		return 0, fmt.Errorf("read subscription schema marker: %w", err)
	}
	if raw == "" {
		return 0, nil
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		return 0, fmt.Errorf("invalid subscription schema version %q", raw)
	}
	return version, nil
}

func lock(ctx context.Context, stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory for subscription migration: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure state directory for subscription migration: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open subscription migration lock: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("secure subscription migration lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("lock subscription migration: %w", err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
