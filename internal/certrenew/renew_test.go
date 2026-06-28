package certrenew

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

type fakeIssuer struct {
	calls int
	got   acme.Request
}

func (i *fakeIssuer) Issue(_ context.Context, r acme.Request) (acme.Certificate, error) {
	i.calls++
	i.got = r
	return acme.Certificate{CertificatePEM: []byte("NEWCERT"), PrivateKeyPEM: []byte("NEWKEY")}, nil
}

func TestRunSkipsCertificateNotNearExpiry(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	domain := "example.com"
	writeRenewalState(t, layout, map[string]string{"domain": domain, "email": "", "acme_challenge": "http-01"})
	writeTestCertificatePair(t, filepath.Join(layout.TLSDir, domain+".crt"), filepath.Join(layout.TLSDir, domain+".key"), domain, time.Now().Add(90*24*time.Hour))
	issuer := &fakeIssuer{}
	runner := &recordingRunner{}

	r := Renewer{
		Layout:      layout,
		ACME:        acme.NewManager(issuer),
		Runner:      runner,
		Now:         time.Now,
		RenewBefore: 30 * 24 * time.Hour,
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if issuer.calls != 0 {
		t.Fatalf("expected no ACME call, got %d", issuer.calls)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("expected no service commands, got %#v", runner.commands)
	}
}

func TestRunRenewsNearExpiryCertificate(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	domain := "example.com"
	writeRenewalState(t, layout, map[string]string{
		"domain":         domain,
		"email":          "",
		"acme_challenge": "dns-01",
		"dns_provider":   "cloudflare",
		"dns_credential": "cf-token",
	})
	certPath := filepath.Join(layout.TLSDir, domain+".crt")
	keyPath := filepath.Join(layout.TLSDir, domain+".key")
	writeTestCertificatePair(t, certPath, keyPath, domain, time.Now().Add(5*24*time.Hour))
	issuer := &fakeIssuer{}
	runner := &recordingRunner{}

	r := Renewer{
		Layout:      layout,
		ACME:        acme.NewManager(issuer),
		Runner:      runner,
		Now:         time.Now,
		RenewBefore: 30 * 24 * time.Hour,
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if issuer.calls != 1 {
		t.Fatalf("expected one ACME call, got %d", issuer.calls)
	}
	if issuer.got.Email != "" {
		t.Fatalf("email = %q, want empty", issuer.got.Email)
	}
	if issuer.got.Challenge != acme.ChallengeDNS01 || issuer.got.DNSProvider != "cloudflare" {
		t.Fatalf("bad ACME request: %#v", issuer.got)
	}
	if issuer.got.Credentials["CF_API_TOKEN"] != "cf-token" {
		t.Fatalf("missing Cloudflare token in request: %#v", issuer.got.Credentials)
	}
	if got := strings.Join(runner.commands, "\n"); !strings.Contains(got, "systemctl restart sing-box.service") || !strings.Contains(got, "systemctl restart nginx") {
		t.Fatalf("missing restart commands: %#v", runner.commands)
	}
	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if string(gotCert) != "NEWCERT" || string(gotKey) != "NEWKEY" {
		t.Fatalf("renewed certificate pair not written")
	}
}

func writeRenewalState(t *testing.T, layout paths.Layout, values map[string]string) {
	t.Helper()
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write state %s: %v", name, err)
		}
	}
}

func writeTestCertificatePair(t *testing.T, certPath, keyPath, domain string, notAfter time.Time) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          x509Serial(t),
		Subject:               pkix.Name{CommonName: domain},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

func x509Serial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}
