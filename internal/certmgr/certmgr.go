// Package certmgr owns TLS certificate management for the hub. It keeps an
// inventory of issued certificates and a set of DNS-01 provider credentials
// scoped to base domains. Because the hub issues every certificate (its own and
// each spoke's) and there is no per-node HTTP-01 server, only the DNS-01
// challenge is supported.
//
// A DNS credential added for a base domain (e.g. example.com) can issue
// certificates for that domain and any subdomain via suffix match, so
// one.example.com and a.b.example.com are both covered. When several
// credentials match, the most specific (longest base domain) wins.
package certmgr

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"golang.org/x/net/idna"
)

const (
	dnsCredentialsDir = "dns_credentials"
	certsDir          = "certs"
)

// CertPaths returns the managed certificate and key paths for a domain. This is
// the single source of truth for the TLSDir/<domain>.crt/.key naming used
// across issuance, renewal, nginx, and sing-box config. Invalid domains return
// two empty paths; mutating callers should first use NormalizeDomain so they can
// report the validation error.
func CertPaths(layout paths.Layout, domain string) (cert, key string) {
	cert, key, _ = certPaths(layout, domain)
	return cert, key
}

// NormalizeDomain converts a fully-qualified DNS name to its canonical ASCII
// form. It accepts the usual presentation conveniences (surrounding space,
// mixed case, Unicode IDNs, and one trailing root dot), but rejects IP
// addresses, wildcards, relative/single-label names, empty labels, and labels
// that cannot appear in a hostname. Returning only validated DNS labels keeps
// the result safe for use as a certificate filename.
func NormalizeDomain(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("invalid domain %q: %w", domain, err)
	}
	ascii = strings.ToLower(ascii)
	if len(ascii) > 253 {
		return "", fmt.Errorf("invalid domain %q: name is longer than 253 bytes", domain)
	}
	if net.ParseIP(ascii) != nil {
		return "", fmt.Errorf("invalid domain %q: IP addresses are not allowed", domain)
	}
	labels := strings.Split(ascii, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("invalid domain %q: a fully-qualified name needs at least two labels", domain)
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return "", fmt.Errorf("invalid domain %q: each label must contain 1 to 63 bytes", domain)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid domain %q: labels cannot start or end with a hyphen", domain)
		}
		for _, c := range []byte(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", fmt.Errorf("invalid domain %q: label contains %q", domain, c)
			}
		}
	}
	return ascii, nil
}

// normalizeDomain is used only in places that already handle an invalid name
// by omitting the entry. Mutating operations must call NormalizeDomain and
// return its error to the caller instead of relying on this helper.
func normalizeDomain(domain string) string {
	normalized, _ := NormalizeDomain(domain)
	return normalized
}

func certPaths(layout paths.Layout, domain string) (cert, key string, err error) {
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return "", "", err
	}
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	cert = filepath.Join(layout.TLSDir, domain+".crt")
	key = filepath.Join(layout.TLSDir, domain+".key")
	for _, path := range []string{cert, key} {
		rel, relErr := filepath.Rel(layout.TLSDir, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("certificate path escapes TLS directory")
		}
	}
	return cert, key, nil
}

// DomainCovers reports whether base (a credential's base domain) covers domain
// by suffix match: an exact match or a proper subdomain of base.
func DomainCovers(base, domain string) bool {
	var err error
	base, err = NormalizeDomain(base)
	if err != nil {
		return false
	}
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return false
	}
	return domain == base || strings.HasSuffix(domain, "."+base)
}
