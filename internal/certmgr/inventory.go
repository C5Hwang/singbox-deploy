package certmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// CertInfo describes one managed certificate: its registry metadata plus the
// live state parsed from disk.
type CertInfo struct {
	Domain string
	Email  string
	// NeedsDNSCredential is true when no configured DNS-01 credential covers
	// Domain. Legacy HTTP-01 certificates therefore remain visible in the
	// inventory while clearly indicating what is needed for their next renewal.
	NeedsDNSCredential bool
	// Present is true when a certificate file exists on disk for the domain.
	Present bool
	// Valid is true when the on-disk pair parses, matches the domain, and is
	// within its validity window.
	Valid     bool
	NotBefore time.Time
	NotAfter  time.Time
	IssuerCN  string
}

// UnmanagedDomainError reports that a valid domain is not present in the
// managed certificate registry or inventory. Callers can use errors.As to
// redirect the operator to certificate management without conflating this
// state with a missing DNS credential.
type UnmanagedDomainError struct{ Domain string }

func (e *UnmanagedDomainError) Error() string {
	return fmt.Sprintf("domain %s is not in certificate management", e.Domain)
}

// RemainingDays returns whole days until expiry relative to now. It is negative
// for an expired certificate and zero when no certificate is present.
func (c CertInfo) RemainingDays(now time.Time) int {
	if !c.Present || c.NotAfter.IsZero() {
		return 0
	}
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

// registeredCert is the persisted registry entry for a managed certificate.
type registeredCert struct {
	Domain string
	Email  string
}

func certsPath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, certsDir)
}

func loadRegistered(layout paths.Layout) ([]registeredCert, error) {
	certs, err := state.LoadEntryDirs(certsPath(layout), func(root string) registeredCert {
		return registeredCert{
			Domain: state.ReadEntryValue(root, "domain", ""),
			Email:  state.ReadEntryValue(root, "email", ""),
		}
	})
	if err != nil {
		return nil, err
	}
	for i := range certs {
		domain, err := NormalizeDomain(certs[i].Domain)
		if err != nil {
			return nil, fmt.Errorf("load registered certificate %d: %w", i+1, err)
		}
		certs[i].Domain = domain
	}
	return certs, nil
}

// Register adds or updates a managed certificate's registry entry.
func Register(layout paths.Layout, domain, email string) error {
	var err error
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return err
	}
	_, err = state.TransactEntryDirs(certsPath(layout), func(root string) registeredCert {
		return registeredCert{
			Domain: state.ReadEntryValue(root, "domain", ""),
			Email:  state.ReadEntryValue(root, "email", ""),
		}
	}, func(c registeredCert) map[string]string {
		return map[string]string{"domain": c.Domain, "email": c.Email}
	}, func(certs []registeredCert) ([]registeredCert, error) {
		for i := range certs {
			normalized, normalizeErr := NormalizeDomain(certs[i].Domain)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			certs[i].Domain = normalized
			if normalized == domain {
				certs[i].Email = strings.TrimSpace(email)
				return certs, nil
			}
		}
		return append(certs, registeredCert{Domain: domain, Email: strings.TrimSpace(email)}), nil
	})
	return err
}

// Deregister removes a domain from the registry and deletes its cert/key files.
func Deregister(layout paths.Layout, domain string) error {
	var err error
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return err
	}
	manager := &Manager{Layout: layout}
	manager.defaults()
	unlock, err := manager.lockIssuance(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	_, err = state.TransactEntryDirs(certsPath(layout), func(root string) registeredCert {
		return registeredCert{
			Domain: state.ReadEntryValue(root, "domain", ""),
			Email:  state.ReadEntryValue(root, "email", ""),
		}
	}, func(c registeredCert) map[string]string {
		return map[string]string{"domain": c.Domain, "email": c.Email}
	}, func(certs []registeredCert) ([]registeredCert, error) {
		kept := make([]registeredCert, 0, len(certs))
		for _, cert := range certs {
			normalized, normalizeErr := NormalizeDomain(cert.Domain)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			if normalized != domain {
				cert.Domain = normalized
				kept = append(kept, cert)
			}
		}
		return kept, nil
	})
	if err != nil {
		return err
	}
	certPath, keyPath, err := certPaths(layout, domain)
	if err != nil {
		return err
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Inventory returns every managed certificate: the registry entries merged with
// any additional certificate files found on disk, each annotated with the live
// state parsed from its .crt/.key pair. Entries are sorted by domain.
func Inventory(layout paths.Layout) ([]CertInfo, error) {
	registered, err := loadRegistered(layout)
	if err != nil {
		return nil, err
	}
	credentials, err := LoadCredentials(layout)
	if err != nil {
		return nil, err
	}
	byDomain := map[string]*CertInfo{}
	order := []string{}
	add := func(domain, email string) *CertInfo {
		domain = normalizeDomain(domain)
		if info, ok := byDomain[domain]; ok {
			if info.Email == "" {
				info.Email = email
			}
			return info
		}
		info := &CertInfo{Domain: domain, Email: email}
		byDomain[domain] = info
		order = append(order, domain)
		return info
	}
	for _, r := range registered {
		add(r.Domain, r.Email)
	}
	// Fold in any on-disk certs not tracked in the registry (imported/legacy).
	for _, domain := range certFileDomains(layout) {
		add(domain, "")
	}
	for _, domain := range order {
		info := byDomain[domain]
		info.NeedsDNSCredential = !CredentialCovers(credentials, domain)
		inspectInto(layout, info, time.Now())
	}
	out := make([]CertInfo, 0, len(order))
	for _, domain := range order {
		out = append(out, *byDomain[domain])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

// IsManaged reports whether domain belongs to certificate management. An
// explicit registry entry or a certificate file discovered by Inventory is
// sufficient; DNS credential coverage alone is deliberately not. A valid but
// unmanaged domain returns both false and an *UnmanagedDomainError so UI flows
// can distinguish it with errors.As.
func IsManaged(layout paths.Layout, domain string) (bool, error) {
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return false, err
	}
	registered, err := loadRegistered(layout)
	if err != nil {
		return false, err
	}
	for _, cert := range registered {
		if cert.Domain == domain {
			return true, nil
		}
	}
	for _, discovered := range certFileDomains(layout) {
		if discovered == domain {
			return true, nil
		}
	}
	return false, &UnmanagedDomainError{Domain: domain}
}

// Inspect returns the live state of a single managed certificate.
func Inspect(layout paths.Layout, domain string) CertInfo {
	return inspectAt(layout, domain, time.Now())
}

func inspectAt(layout paths.Layout, domain string, now time.Time) CertInfo {
	rawDomain := domain
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return CertInfo{Domain: strings.TrimSpace(rawDomain), NeedsDNSCredential: true}
	}
	info := CertInfo{Domain: domain}
	credentials, err := LoadCredentials(layout)
	info.NeedsDNSCredential = err != nil || !CredentialCovers(credentials, domain)
	inspectInto(layout, &info, now)
	return info
}

func inspectInto(layout paths.Layout, info *CertInfo, now time.Time) {
	certPath, keyPath, err := certPaths(layout, info.Domain)
	if err != nil {
		return
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return
	}
	info.Present = true
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return
	}
	leaf, err := firstCertificate(certPEM)
	if err != nil {
		return
	}
	info.NotBefore = leaf.NotBefore
	info.NotAfter = leaf.NotAfter
	info.IssuerCN = leaf.Issuer.CommonName
	if _, err := ValidateCertificatePair(certPEM, keyPEM, info.Domain, now); err == nil {
		info.Valid = true
	}
}

// certFileDomains lists the domains that have a .crt file in TLSDir.
func certFileDomains(layout paths.Layout) []string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	entries, err := os.ReadDir(layout.TLSDir)
	if err != nil {
		return nil
	}
	var domains []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".crt") {
			continue
		}
		domain, err := NormalizeDomain(strings.TrimSuffix(e.Name(), ".crt"))
		if err != nil {
			continue
		}
		domains = append(domains, domain)
	}
	return domains
}

// ValidateCertificatePair verifies that certPEM and keyPEM form a matching
// pair, that the leaf certificate covers domain, and that it is valid at now.
// Callers such as the spoke agent should use this before replacing an existing
// on-disk pair. The parsed leaf is returned on certificate-policy failures to
// make its validity metadata available to diagnostics.
func ValidateCertificatePair(certPEM, keyPEM []byte, domain string, now time.Time) (*x509.Certificate, error) {
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("certificate and private key do not form a pair: %w", err)
	}
	leaf, err := firstCertificate(certPEM)
	if err != nil {
		return nil, err
	}
	if now.Before(leaf.NotBefore) {
		return leaf, fmt.Errorf("certificate for %s is not valid before %s", domain, leaf.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(leaf.NotAfter) {
		return leaf, fmt.Errorf("certificate for %s expired at %s", domain, leaf.NotAfter.Format(time.RFC3339))
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		return leaf, fmt.Errorf("certificate does not cover %s: %w", domain, err)
	}
	return leaf, nil
}

// firstCertificate parses the leaf certificate from a PEM bundle.
func firstCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}
