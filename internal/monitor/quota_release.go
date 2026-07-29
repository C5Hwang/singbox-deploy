package monitor

import (
	"errors"
	"fmt"
	"os"
)

// ReleaseQuotaStop restores sing-box when the monitor owns its stopped state.
//
// A missing database or an unset marker is a no-op. The ownership marker is
// deliberately cleared only after start succeeds, so a later disable/stop can
// retry recovery after a transient service failure.
func ReleaseQuotaStop(dbPath string, start func() error) error {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat monitor store: %w", err)
	}

	store, err := OpenStore(dbPath)
	if err != nil {
		return fmt.Errorf("open monitor store: %w", err)
	}
	defer store.Close()

	stopped, err := store.QuotaStopped()
	if err != nil {
		return fmt.Errorf("read quota stop marker: %w", err)
	}
	if !stopped {
		return nil
	}
	if start == nil {
		return fmt.Errorf("start sing-box: no service starter configured")
	}
	if err := start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}
	if err := store.SetQuotaStopped(false); err != nil {
		return fmt.Errorf("clear quota stop marker: %w", err)
	}
	return nil
}
