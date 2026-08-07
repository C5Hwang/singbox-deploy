package certmgr

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// Supported DNS-01 providers. Kept in sync with the lego providers wired in
// internal/acme.
const (
	ProviderCloudflare = "cloudflare"
	ProviderAliyun     = "aliyun"
)

// SupportedProvider reports whether name is a supported DNS-01 provider.
func SupportedProvider(name string) bool {
	switch name {
	case ProviderCloudflare, ProviderAliyun:
		return true
	default:
		return false
	}
}

// DNSCredential is a DNS-01 provider credential scoped to a base domain. It can
// issue certificates for that base domain and any subdomain (suffix match).
type DNSCredential struct {
	// Domain is the base domain the credential is authoritative for, e.g.
	// example.com. Stored normalized (lowercase, no trailing dot).
	Domain string
	// Provider is the DNS provider name (cloudflare, aliyun).
	Provider string
	// Credential is the flattened secret: a Cloudflare API token, or
	// "accessKey:secretKey" for Aliyun.
	Credential string
}

// Validate checks a credential is well-formed.
func (c DNSCredential) Validate() error {
	if _, err := NormalizeDomain(c.Domain); err != nil {
		return fmt.Errorf("invalid credential domain: %w", err)
	}
	provider := strings.TrimSpace(c.Provider)
	if !SupportedProvider(provider) {
		return fmt.Errorf("unsupported DNS provider %q", c.Provider)
	}
	if strings.TrimSpace(c.Credential) == "" {
		return fmt.Errorf("credential secret is required for %s", c.Domain)
	}
	if provider == ProviderAliyun {
		key, secret, ok := strings.Cut(c.Credential, ":")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(secret) == "" {
			return fmt.Errorf("aliyun credential for %s must be accessKey:secretKey", c.Domain)
		}
	}
	return nil
}

// Env inflates the flattened credential into the lego provider environment map.
func (c DNSCredential) Env() map[string]string {
	return providerEnv(strings.TrimSpace(c.Provider), c.Credential)
}

// providerEnv maps a flattened credential string to lego's provider env vars.
func providerEnv(provider, credential string) map[string]string {
	env := map[string]string{}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return env
	}
	switch provider {
	case ProviderCloudflare:
		env["CF_API_TOKEN"] = credential
	case ProviderAliyun:
		if key, secret, ok := strings.Cut(credential, ":"); ok {
			env["ALICLOUD_ACCESS_KEY"] = strings.TrimSpace(key)
			env["ALICLOUD_SECRET_KEY"] = strings.TrimSpace(secret)
		}
	}
	return env
}

func dnsCredentialsPath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, dnsCredentialsDir)
}

func decodeDNSCredential(root string) DNSCredential {
	return DNSCredential{
		Domain:     state.ReadEntryValue(root, "domain", ""),
		Provider:   strings.TrimSpace(state.ReadEntryValue(root, "provider", "")),
		Credential: state.ReadEntryValue(root, "credential", ""),
	}
}

func encodeDNSCredential(c DNSCredential) map[string]string {
	return map[string]string{
		"domain":     c.Domain,
		"provider":   c.Provider,
		"credential": c.Credential,
	}
}

// LoadCredentials reads the stored DNS credentials in saved order.
func LoadCredentials(layout paths.Layout) ([]DNSCredential, error) {
	creds, err := state.LoadEntryDirs(dnsCredentialsPath(layout), decodeDNSCredential)
	if err != nil {
		return nil, err
	}
	for i := range creds {
		domain, err := NormalizeDomain(creds[i].Domain)
		if err != nil {
			return nil, fmt.Errorf("load DNS credential %d: %w", i+1, err)
		}
		creds[i].Domain = domain
		if err := creds[i].Validate(); err != nil {
			return nil, fmt.Errorf("load DNS credential %d: %w", i+1, err)
		}
	}
	return creds, nil
}

// SaveCredentials persists DNS credentials, one directory per entry.
func SaveCredentials(layout paths.Layout, creds []DNSCredential) error {
	normalized := make([]DNSCredential, len(creds))
	for i, c := range creds {
		if err := c.Validate(); err != nil {
			return err
		}
		domain, _ := NormalizeDomain(c.Domain)
		c.Domain = domain
		c.Provider = strings.TrimSpace(c.Provider)
		c.Credential = strings.TrimSpace(c.Credential)
		normalized[i] = c
	}
	return state.SaveEntryDirs(dnsCredentialsPath(layout), normalized, encodeDNSCredential)
}

// UpsertCredential adds or replaces one base-domain credential under the
// credential registry's cross-process transaction lock.
func UpsertCredential(layout paths.Layout, credential DNSCredential) error {
	normalized, err := normalizeCredential(credential)
	if err != nil {
		return err
	}
	_, err = transactCredentials(layout, func(current []DNSCredential) ([]DNSCredential, error) {
		for i := range current {
			if current[i].Domain == normalized.Domain {
				current[i] = normalized
				return current, nil
			}
		}
		return append(current, normalized), nil
	})
	return err
}

// AddCredentialIfAbsent adds credential only when its normalized base domain
// has no entry. It reports whether an entry was added and is used by legacy
// migration so a concurrent operator edit can never be overwritten.
func AddCredentialIfAbsent(layout paths.Layout, credential DNSCredential) (bool, error) {
	normalized, err := normalizeCredential(credential)
	if err != nil {
		return false, err
	}
	added := false
	_, err = transactCredentials(layout, func(current []DNSCredential) ([]DNSCredential, error) {
		for i := range current {
			if current[i].Domain == normalized.Domain {
				return current, nil
			}
		}
		added = true
		return append(current, normalized), nil
	})
	return added, err
}

// DeleteCredential removes one normalized base-domain credential. It is
// idempotent and preserves credentials concurrently added for other domains.
func DeleteCredential(layout paths.Layout, domain string) error {
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return err
	}
	_, err = transactCredentials(layout, func(current []DNSCredential) ([]DNSCredential, error) {
		kept := make([]DNSCredential, 0, len(current))
		for _, credential := range current {
			if credential.Domain != domain {
				kept = append(kept, credential)
			}
		}
		return kept, nil
	})
	return err
}

func transactCredentials(layout paths.Layout, mutate func([]DNSCredential) ([]DNSCredential, error)) ([]DNSCredential, error) {
	return state.TransactEntryDirs(dnsCredentialsPath(layout), decodeDNSCredential, encodeDNSCredential, func(current []DNSCredential) ([]DNSCredential, error) {
		for i := range current {
			normalized, err := normalizeCredential(current[i])
			if err != nil {
				return nil, fmt.Errorf("load DNS credential %d: %w", i+1, err)
			}
			current[i] = normalized
		}
		return mutate(current)
	})
}

func normalizeCredential(c DNSCredential) (DNSCredential, error) {
	if err := c.Validate(); err != nil {
		return DNSCredential{}, err
	}
	domain, _ := NormalizeDomain(c.Domain)
	c.Domain = domain
	c.Provider = strings.TrimSpace(c.Provider)
	c.Credential = strings.TrimSpace(c.Credential)
	return c, nil
}

// SelectCredential returns the credential whose base domain covers domain by
// suffix match, preferring the most specific (longest base domain). The second
// result is false when no credential covers the domain.
func SelectCredential(creds []DNSCredential, domain string) (DNSCredential, bool) {
	var err error
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return DNSCredential{}, false
	}
	best := -1
	bestLen := -1
	for i, c := range creds {
		base, err := NormalizeDomain(c.Domain)
		if err != nil {
			continue
		}
		if DomainCovers(base, domain) && len(base) > bestLen {
			bestLen = len(base)
			best = i
		}
	}
	if best < 0 {
		return DNSCredential{}, false
	}
	return creds[best], true
}

// CredentialCovers reports whether any stored credential covers domain.
func CredentialCovers(creds []DNSCredential, domain string) bool {
	_, ok := SelectCredential(creds, domain)
	return ok
}
