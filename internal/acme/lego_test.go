package acme

import (
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
