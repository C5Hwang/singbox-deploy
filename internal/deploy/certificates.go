package deploy

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

var letsEncryptLiveDir = "/etc/letsencrypt/live"

type certificatePair struct {
	cert string
	key  string
}

func (o *Orchestrator) importExistingCertificate(domain, certPath, keyPath string) (bool, error) {
	for _, candidate := range existingCertificateCandidates(domain) {
		if candidate.cert == certPath && candidate.key == keyPath {
			continue
		}
		ok, err := certificatePairUsable(candidate.cert, candidate.key, domain, time.Now())
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
	return certmgr.CertPaths(layout, domain)
}

func existingCertificateCandidates(domain string) []certificatePair {
	domain, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return nil
	}
	return []certificatePair{{
		cert: filepath.Join(letsEncryptLiveDir, domain, "fullchain.pem"),
		key:  filepath.Join(letsEncryptLiveDir, domain, "privkey.pem"),
	}}
}

func certificatePairUsable(certPath, keyPath, domain string, t time.Time) (bool, error) {
	normalized, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return false, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return false, err
	}
	if _, err := certmgr.ValidateCertificatePair(certPEM, keyPEM, normalized, t); err != nil {
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
