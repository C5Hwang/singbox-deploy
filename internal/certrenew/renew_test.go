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
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

type fakeIssuer struct {
	calls       int
	got         acme.Request
	certificate acme.Certificate
}

func (i *fakeIssuer) Issue(_ context.Context, r acme.Request) (acme.Certificate, error) {
	i.calls++
	i.got = r
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return acme.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return acme.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: r.Domain},
		DNSNames:     []string{r.Domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return acme.Certificate{}, err
	}
	i.certificate = acme.Certificate{
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	}
	return i.certificate, nil
}

func renewer(layout paths.Layout, issuer acme.Issuer, runner system.Runner) Renewer {
	return Renewer{
		Layout:      layout,
		Manager:     &certmgr.Manager{Layout: layout, ACME: acme.NewManager(issuer), Now: time.Now},
		Runner:      runner,
		Now:         time.Now,
		RenewBefore: 30 * 24 * time.Hour,
	}
}

func TestRunSkipsCertificateNotNearExpiry(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	domain := "example.com"
	writeTestCertificatePair(t, filepath.Join(layout.TLSDir, domain+".crt"), filepath.Join(layout.TLSDir, domain+".key"), domain, time.Now().Add(90*24*time.Hour))
	issuer := &fakeIssuer{}
	runner := &recordingRunner{}

	if err := renewer(layout, issuer, runner).Run(context.Background()); err != nil {
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
	domain := "vpn.example.com"
	// A credential for the apex covers the subdomain via suffix match.
	if err := certmgr.SaveCredentials(layout, []certmgr.DNSCredential{{
		Domain: "example.com", Provider: "cloudflare", Credential: "cf-token",
	}}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	certPath := filepath.Join(layout.TLSDir, domain+".crt")
	keyPath := filepath.Join(layout.TLSDir, domain+".key")
	writeTestCertificatePair(t, certPath, keyPath, domain, time.Now().Add(5*24*time.Hour))
	issuer := &fakeIssuer{}
	runner := &recordingRunner{}

	if err := renewer(layout, issuer, runner).Run(context.Background()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if issuer.calls != 1 {
		t.Fatalf("expected one ACME call, got %d", issuer.calls)
	}
	if issuer.got.Domain != domain {
		t.Fatalf("issued for %q, want %q", issuer.got.Domain, domain)
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
	if string(gotCert) != string(issuer.certificate.CertificatePEM) || string(gotKey) != string(issuer.certificate.PrivateKeyPEM) {
		t.Fatalf("renewed certificate pair not written")
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
	if err := state.WriteFilePair(keyPath, keyPEM, 0o600, certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write certificate pair: %v", err)
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
