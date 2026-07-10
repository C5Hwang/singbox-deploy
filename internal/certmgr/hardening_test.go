package certmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func TestNormalizeDomainStrict(t *testing.T) {
	valid := map[string]string{
		" EXAMPLE.COM. ":  "example.com",
		"one.example.com": "one.example.com",
		"bücher.example":  "xn--bcher-kva.example",
	}
	for input, want := range valid {
		got, err := NormalizeDomain(input)
		if err != nil {
			t.Errorf("NormalizeDomain(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{
		"", ".", "localhost", "127.0.0.1", "*.example.com",
		"../example.com", "a..example.com", "-a.example.com",
		"a-.example.com", "a_example.com", "example.com/../../etc",
		strings.Repeat("a", 64) + ".example.com", "example.com..",
	}
	for _, input := range invalid {
		if got, err := NormalizeDomain(input); err == nil {
			t.Errorf("NormalizeDomain(%q) unexpectedly succeeded as %q", input, got)
		}
	}
}

func TestCertPathsRejectTraversal(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cert, key := CertPaths(layout, "../../etc/passwd")
	if cert != "" || key != "" {
		t.Fatalf("invalid domain produced paths: cert=%q key=%q", cert, key)
	}
	cert, key = CertPaths(layout, "One.Example.com.")
	if cert != filepath.Join(layout.TLSDir, "one.example.com.crt") || key != filepath.Join(layout.TLSDir, "one.example.com.key") {
		t.Fatalf("unexpected normalized paths: cert=%q key=%q", cert, key)
	}
}

func TestDomainCoversRequiresLabelBoundaryAndValidNames(t *testing.T) {
	tests := []struct {
		base, domain string
		want         bool
	}{
		{"example.com", "deep.one.example.com", true},
		{"example.com", "notexample.com", false},
		{"example.com", "example.com.evil", false},
		{"../example.com", "one.example.com", false},
		{"example.com", "../example.com", false},
	}
	for _, tt := range tests {
		if got := DomainCovers(tt.base, tt.domain); got != tt.want {
			t.Errorf("DomainCovers(%q, %q) = %v, want %v", tt.base, tt.domain, got, tt.want)
		}
	}
}

func TestValidateCertificatePair(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	certPEM, keyPEM := makeCertificatePair(t, []string{"one.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := ValidateCertificatePair(certPEM, keyPEM, "ONE.EXAMPLE.COM.", now); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}

	_, otherKey := makeCertificatePair(t, []string{"one.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	tests := []struct {
		name   string
		cert   []byte
		key    []byte
		domain string
		now    time.Time
	}{
		{"mismatched key", certPEM, otherKey, "one.example.com", now},
		{"wrong hostname", certPEM, keyPEM, "two.example.com", now},
		{"not yet valid", certPEM, keyPEM, "one.example.com", now.Add(-2 * time.Hour)},
		{"expired", certPEM, keyPEM, "one.example.com", now.Add(2 * time.Hour)},
		{"invalid domain", certPEM, keyPEM, "../../etc/passwd", now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ValidateCertificatePair(tt.cert, tt.key, tt.domain, tt.now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestManagerRejectsUnusableACMEResultBeforeSaving(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	validCert, validKey := makeCertificatePair(t, []string{"example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	wrongCert, wrongKey := makeCertificatePair(t, []string{"other.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	_, mismatchedKey := makeCertificatePair(t, []string{"example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	expiredCert, expiredKey := makeCertificatePair(t, []string{"example.com"}, now.Add(-2*time.Hour), now.Add(-time.Hour))

	tests := []struct {
		name string
		cert []byte
		key  []byte
	}{
		{"malformed", []byte("not pem"), []byte("not pem")},
		{"mismatched key", validCert, mismatchedKey},
		{"wrong hostname", wrongCert, wrongKey},
		{"expired", expiredCert, expiredKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			if err := SaveCredentials(layout, []DNSCredential{{Domain: "example.com", Provider: ProviderCloudflare, Credential: "token"}}); err != nil {
				t.Fatal(err)
			}
			certPath, keyPath := CertPaths(layout, "example.com")
			if err := state.WriteFilePair(keyPath, []byte("old key"), 0o600, certPath, []byte("old cert"), 0o644); err != nil {
				t.Fatal(err)
			}
			issuer := &staticIssuer{certificate: acme.Certificate{CertificatePEM: tt.cert, PrivateKeyPEM: tt.key}}
			manager := &Manager{Layout: layout, ACME: acme.NewManager(issuer), Now: func() time.Time { return now }}
			if _, err := manager.Issue(context.Background(), "example.com", ""); err == nil {
				t.Fatal("expected issuance validation error")
			}
			gotCert, _ := os.ReadFile(certPath)
			gotKey, _ := os.ReadFile(keyPath)
			if string(gotCert) != "old cert" || string(gotKey) != "old key" {
				t.Fatalf("existing pair was changed: cert=%q key=%q", gotCert, gotKey)
			}
			registered, err := loadRegistered(layout)
			if err != nil {
				t.Fatal(err)
			}
			if len(registered) != 0 {
				t.Fatalf("invalid result was registered: %+v", registered)
			}
		})
	}

	// Sanity-check the same path with a valid result.
	layout := paths.LayoutForRoot(t.TempDir())
	if err := SaveCredentials(layout, []DNSCredential{{Domain: "example.com", Provider: ProviderCloudflare, Credential: "token"}}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		Layout: layout,
		ACME:   acme.NewManager(&staticIssuer{certificate: acme.Certificate{CertificatePEM: validCert, PrivateKeyPEM: validKey}}),
		Now:    func() time.Time { return now },
	}
	info, err := manager.Issue(context.Background(), "EXAMPLE.COM.", "admin@example.com")
	if err != nil {
		t.Fatalf("valid issuance failed: %v", err)
	}
	if !info.Present || !info.Valid || info.Domain != "example.com" || info.NeedsDNSCredential {
		t.Fatalf("unexpected issued info: %+v", info)
	}
}

func TestEnsureIssuedSerializesConcurrentHubProcesses(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	certPEM, keyPEM := makeCertificatePair(t, []string{"example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	layout := paths.LayoutForRoot(t.TempDir())
	if err := SaveCredentials(layout, []DNSCredential{{
		Domain: "example.com", Provider: ProviderCloudflare, Credential: "token",
	}}); err != nil {
		t.Fatal(err)
	}
	issuer := &blockingIssuer{
		certificate: acme.Certificate{CertificatePEM: certPEM, PrivateKeyPEM: keyPEM},
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	manager := &Manager{
		Layout: layout,
		ACME:   acme.NewManager(issuer),
		Now:    func() time.Time { return now },
	}
	type result struct {
		issued bool
		err    error
	}
	results := make(chan result, 2)
	go func() {
		_, issued, err := manager.EnsureIssued(context.Background(), "example.com", "", DefaultRenewBefore)
		results <- result{issued: issued, err: err}
	}()
	<-issuer.started
	go func() {
		_, issued, err := manager.EnsureIssued(context.Background(), "example.com", "", DefaultRenewBefore)
		results <- result{issued: issued, err: err}
	}()
	close(issuer.release)

	issuedCount := 0
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.issued {
			issuedCount++
		}
	}
	issuer.mu.Lock()
	orders := issuer.orders
	issuer.mu.Unlock()
	if orders != 1 || issuedCount != 1 {
		t.Fatalf("ACME orders=%d issued results=%d, want one serialized issuance", orders, issuedCount)
	}
}

func TestLegacyMigrationMergesAndUsesSchemaMarker(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := SaveCredentials(layout, []DNSCredential{{
		Domain: "other.net", Provider: ProviderCloudflare, Credential: "existing",
	}}); err != nil {
		t.Fatal(err)
	}
	writeState(t, layout, "acme_challenge", "dns-01")
	writeState(t, layout, "dns_provider", ProviderCloudflare)
	writeState(t, layout, "dns_credential", "legacy-token")
	writeState(t, layout, "domain", "legacy.example.com")
	writeState(t, layout, "email", "op@example.com")

	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatalf("SeedLegacyCredentials: %v", err)
	}
	creds, err := LoadCredentials(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 || creds[0].Domain != "other.net" || creds[1].Domain != "legacy.example.com" {
		t.Fatalf("legacy credential was not merged: %+v", creds)
	}
	if managed, err := IsManaged(layout, "legacy.example.com"); err != nil || !managed {
		t.Fatalf("legacy hub certificate was not registered: managed=%v err=%v", managed, err)
	}
	marker, err := state.NewStore(layout.StateDir).ReadValue(legacyMigrationMarker, true)
	if err != nil || marker != legacyMigrationVersion {
		t.Fatalf("migration marker = %q, err=%v", marker, err)
	}

	// The marker makes subsequent runs a no-op even if legacy flat state is
	// later edited; it must neither duplicate nor overwrite merged credentials.
	writeState(t, layout, "dns_credential", "changed-token")
	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatal(err)
	}
	creds, _ = LoadCredentials(layout)
	if len(creds) != 2 || creds[1].Credential != "legacy-token" {
		t.Fatalf("migration was not idempotent: %+v", creds)
	}
}

func TestLegacyMigrationPreservesExistingCredentialForSameDomain(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := SaveCredentials(layout, []DNSCredential{{
		Domain: "legacy.example.com", Provider: ProviderCloudflare, Credential: "current-token",
	}}); err != nil {
		t.Fatal(err)
	}
	writeState(t, layout, "acme_challenge", "dns-01")
	writeState(t, layout, "dns_provider", ProviderCloudflare)
	writeState(t, layout, "dns_credential", "stale-flat-token")
	writeState(t, layout, "domain", "legacy.example.com")

	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].Credential != "current-token" {
		t.Fatalf("existing same-domain credential was overwritten or duplicated: %+v", creds)
	}
}

func TestLegacyHTTP01CertificateIsRegisteredAsNeedingDNSCredential(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeState(t, layout, "acme_challenge", "http-01")
	writeState(t, layout, "domain", "legacy.example.com")
	writeState(t, layout, "email", "op@example.com")

	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatalf("SeedLegacyCredentials: %v", err)
	}
	inv, err := Inventory(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 1 || inv[0].Domain != "legacy.example.com" || inv[0].Email != "op@example.com" || !inv[0].NeedsDNSCredential {
		t.Fatalf("unexpected migrated HTTP-01 inventory: %+v", inv)
	}
	if err := SaveCredentials(layout, []DNSCredential{{Domain: "example.com", Provider: ProviderCloudflare, Credential: "token"}}); err != nil {
		t.Fatal(err)
	}
	inv, err = Inventory(layout)
	if err != nil {
		t.Fatal(err)
	}
	if inv[0].NeedsDNSCredential {
		t.Fatalf("status did not clear after adding a covering credential: %+v", inv[0])
	}
}

func TestIsManagedUsesCertificateInventoryNotCredentialCoverage(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := SaveCredentials(layout, []DNSCredential{{
		Domain: "example.com", Provider: ProviderCloudflare, Credential: "token",
	}}); err != nil {
		t.Fatal(err)
	}
	managed, err := IsManaged(layout, "covered.example.com")
	if managed {
		t.Fatal("credential coverage alone must not make a domain managed")
	}
	var unmanaged *UnmanagedDomainError
	if !errors.As(err, &unmanaged) || unmanaged.Domain != "covered.example.com" {
		t.Fatalf("expected UnmanagedDomainError, got %T %v", err, err)
	}

	if err := Register(layout, "managed.example.com", ""); err != nil {
		t.Fatal(err)
	}
	managed, err = IsManaged(layout, " MANAGED.EXAMPLE.COM. ")
	if err != nil || !managed {
		t.Fatalf("registered domain: managed=%v err=%v", managed, err)
	}

	if err := os.MkdirAll(layout.TLSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.TLSDir, "imported.example.com.crt"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	managed, err = IsManaged(layout, "imported.example.com")
	if err != nil || !managed {
		t.Fatalf("inventory-discovered domain: managed=%v err=%v", managed, err)
	}

	managed, err = IsManaged(layout, "../../etc/passwd")
	if managed || err == nil || errors.As(err, &unmanaged) {
		t.Fatalf("invalid domain should return validation error, managed=%v err=%T %v", managed, err, err)
	}
}

type staticIssuer struct {
	certificate acme.Certificate
	err         error
}

type blockingIssuer struct {
	mu          sync.Mutex
	orders      int
	certificate acme.Certificate
	started     chan struct{}
	release     chan struct{}
}

func (i *blockingIssuer) Issue(context.Context, acme.Request) (acme.Certificate, error) {
	i.mu.Lock()
	i.orders++
	first := i.orders == 1
	i.mu.Unlock()
	if first {
		close(i.started)
	}
	<-i.release
	return i.certificate, nil
}

func (i *staticIssuer) Issue(context.Context, acme.Request) (acme.Certificate, error) {
	return i.certificate, i.err
}

func makeCertificatePair(t *testing.T, dnsNames []string, notBefore, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
