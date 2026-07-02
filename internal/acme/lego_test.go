package acme

import (
	"crypto/ecdsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	legolog "github.com/go-acme/lego/v4/log"
)

func TestLegoIssuerRedirectsLegoLogs(t *testing.T) {
	previous := legolog.Logger
	var buf strings.Builder
	issuer := &LegoIssuer{Output: &buf}

	_, err := issuer.withLegoLogger(func() (Certificate, error) {
		legolog.Infof("acme: Registering account for %s", "admin@example.com")
		return Certificate{}, nil
	})
	if err != nil {
		t.Fatalf("withLegoLogger error: %v", err)
	}

	if !strings.Contains(buf.String(), "[INFO] acme: Registering account for admin@example.com") {
		t.Fatalf("lego log was not redirected: %q", buf.String())
	}
	if legolog.Logger != previous {
		t.Fatalf("lego logger was not restored")
	}
}

func TestAccountKeyPersistsAcrossIssuances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acme_account_key")
	issuer := &LegoIssuer{AccountKeyPath: path}

	first, err := issuer.accountKey()
	if err != nil {
		t.Fatalf("accountKey (create): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("account key not persisted: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("account key mode = %v, want 0600", info.Mode().Perm())
	}

	second, err := issuer.accountKey()
	if err != nil {
		t.Fatalf("accountKey (reload): %v", err)
	}
	if !first.(*ecdsa.PrivateKey).Equal(second.(*ecdsa.PrivateKey)) {
		t.Fatal("reloaded account key differs from the persisted one")
	}
}

func TestAccountKeyEphemeralWithoutPath(t *testing.T) {
	issuer := &LegoIssuer{}
	first, err := issuer.accountKey()
	if err != nil {
		t.Fatalf("accountKey: %v", err)
	}
	second, err := issuer.accountKey()
	if err != nil {
		t.Fatalf("accountKey: %v", err)
	}
	if first.(*ecdsa.PrivateKey).Equal(second.(*ecdsa.PrivateKey)) {
		t.Fatal("ephemeral keys should differ per call")
	}
}
