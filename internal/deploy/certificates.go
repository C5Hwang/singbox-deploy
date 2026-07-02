package deploy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

var (
	letsEncryptLiveDir = "/etc/letsencrypt/live"
	now                = time.Now
)

type certificatePair struct {
	cert string
	key  string
}

func (o *Orchestrator) importExistingCertificate(cfg Config, certPath, keyPath string) (bool, error) {
	for _, candidate := range existingCertificateCandidates(cfg.Domain) {
		if candidate.cert == certPath && candidate.key == keyPath {
			continue
		}
		ok, err := certificatePairUsable(candidate.cert, candidate.key, cfg.Domain, now())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, fmt.Errorf("check existing certificate %s: %w", candidate.cert, err)
		}
		if !ok {
			continue
		}
		certPEM, err := os.ReadFile(candidate.cert)
		if err != nil {
			return false, err
		}
		keyPEM, err := os.ReadFile(candidate.key)
		if err != nil {
			return false, err
		}
		if err := state.WriteFilePair(keyPath, keyPEM, 0o600, certPath, certPEM, 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// CertificatePaths returns the managed certificate and key paths for a domain.
// It is the single source of truth for the TLSDir/<domain>.crt/.key naming used
// across install, renewal, and nginx config.
func CertificatePaths(layout paths.Layout, domain string) (cert, key string) {
	return filepath.Join(layout.TLSDir, domain+".crt"), filepath.Join(layout.TLSDir, domain+".key")
}

func existingCertificateCandidates(domain string) []certificatePair {
	return []certificatePair{{
		cert: filepath.Join(letsEncryptLiveDir, domain, "fullchain.pem"),
		key:  filepath.Join(letsEncryptLiveDir, domain, "privkey.pem"),
	}}
}

func certificatePairUsable(certPath, keyPath, domain string, t time.Time) (bool, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return false, err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return false, nil
	}
	cert, err := FirstCertificate(certPEM)
	if err != nil {
		return false, nil
	}
	if t.Before(cert.NotBefore) || !t.Before(cert.NotAfter) {
		return false, nil
	}
	if err := cert.VerifyHostname(domain); err != nil {
		return false, nil
	}
	return true, nil
}

// FirstCertificate parses the leaf certificate from a PEM bundle.
func FirstCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}
