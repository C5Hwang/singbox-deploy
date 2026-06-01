// Package acme issues and renews Let's Encrypt certificates using a built-in
// ACME client. Only Let's Encrypt is supported, with HTTP-01 and DNS-01
// (Cloudflare and Aliyun) challenges. acme.sh is not used.
package acme

import (
	"context"
	"fmt"
)

// Challenge is an ACME challenge type.
type Challenge string

const (
	ChallengeHTTP01 Challenge = "http-01"
	ChallengeDNS01  Challenge = "dns-01"
)

// String renders the challenge identifier.
func (c Challenge) String() string { return string(c) }

// SupportedDNSProvider reports whether a DNS-01 provider is supported in the
// MVP. Only Cloudflare and Aliyun are.
func SupportedDNSProvider(name string) bool {
	switch name {
	case "cloudflare", "aliyun":
		return true
	default:
		return false
	}
}

// Request describes a certificate request.
type Request struct {
	Domain      string
	Email       string
	Challenge   Challenge
	DNSProvider string
	// Credentials carries provider-specific secrets (e.g. CF_API_TOKEN,
	// ALICLOUD_ACCESS_KEY/ALICLOUD_SECRET_KEY) for DNS-01.
	Credentials map[string]string
}

// Validate checks that the request is internally consistent before issuance.
func (r Request) Validate() error {
	if r.Domain == "" {
		return fmt.Errorf("domain is required")
	}
	if r.Email == "" {
		return fmt.Errorf("account email is required")
	}
	switch r.Challenge {
	case ChallengeHTTP01:
		// no extra requirements
	case ChallengeDNS01:
		if r.DNSProvider == "" {
			return fmt.Errorf("dns-01 requires a DNS provider")
		}
		if !SupportedDNSProvider(r.DNSProvider) {
			return fmt.Errorf("unsupported DNS provider %q", r.DNSProvider)
		}
	default:
		return fmt.Errorf("unsupported challenge %q", r.Challenge)
	}
	return nil
}

// Certificate is an issued certificate and its private key, in PEM form.
type Certificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

// Issuer performs the actual ACME issuance. The production implementation wraps
// lego; tests use a fake.
type Issuer interface {
	Issue(ctx context.Context, r Request) (Certificate, error)
}

// Manager validates requests and delegates issuance to an Issuer.
type Manager struct {
	issuer Issuer
}

// NewManager returns a Manager backed by the given Issuer.
func NewManager(issuer Issuer) *Manager {
	return &Manager{issuer: issuer}
}

// Obtain validates the request and issues a certificate.
func (m *Manager) Obtain(ctx context.Context, r Request) (Certificate, error) {
	if err := r.Validate(); err != nil {
		return Certificate{}, err
	}
	return m.issuer.Issue(ctx, r)
}
