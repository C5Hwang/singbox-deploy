package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// The token is md5(salt + newline) and is recomputed wherever it is needed, so
// an install must not persist a second copy that goes stale as soon as a group
// rotates its salt.
func TestWriteInstallStateDoesNotPersistTheSubscriptionToken(t *testing.T) {
	stateDir := t.TempDir()
	cfg := testConfig(t)
	if err := WriteInstallState(stateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "subscribe_token")); !os.IsNotExist(err) {
		t.Fatalf("install state still writes subscribe_token: %v", err)
	}
	// The salt it was derived from is still the thing that must survive.
	if _, err := os.Lstat(filepath.Join(stateDir, "subscribe_salt")); err != nil {
		t.Fatalf("install state lost the subscription salt: %v", err)
	}
}

// Upgraded installations carry the file on disk; startup drops it, and doing so
// twice must stay quiet.
func TestRemoveLegacySubscribeTokenIsIdempotent(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	token := filepath.Join(layout.StateDir, "subscribe_token")
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := os.WriteFile(token, []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatalf("seed legacy token: %v", err)
	}

	if err := RemoveLegacySubscribeToken(layout); err != nil {
		t.Fatalf("RemoveLegacySubscribeToken: %v", err)
	}
	if _, err := os.Lstat(token); !os.IsNotExist(err) {
		t.Fatalf("legacy subscription token survived: %v", err)
	}
	if err := RemoveLegacySubscribeToken(layout); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
}
