package cluster

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dnsCredentialsDir is the subdirectory under StateDir that holds the
// per-root-domain DNS API credentials used for ACME DNS-01 challenges.
const dnsCredentialsDir = "dns_credentials"

// DNSCredentials describes the secrets needed to publish DNS-01 challenge
// records for one root domain. Each root domain owns one credential set
// regardless of how many subdomains (master, nodes) use it.
type DNSCredentials struct {
	RootDomain string // e.g. "example.com"
	Provider   string // "cloudflare" or "aliyun"
	APIToken   string // Cloudflare API Token / Aliyun AccessKey ID
	APISecret  string // Aliyun AccessKey Secret (Cloudflare ignores this)
}

// EnvMap returns the credential set in the env-var form lego expects.
func (c DNSCredentials) EnvMap() map[string]string {
	switch c.Provider {
	case "cloudflare":
		return map[string]string{"CF_API_TOKEN": c.APIToken}
	case "aliyun":
		return map[string]string{
			"ALICLOUD_ACCESS_KEY": c.APIToken,
			"ALICLOUD_SECRET_KEY": c.APISecret,
		}
	default:
		return map[string]string{}
	}
}

// DNSStore reads and writes per-root-domain DNS credentials.
type DNSStore struct {
	dir string
}

// NewDNSStore returns a store rooted at the registry's state directory.
func (r Registry) DNS() DNSStore {
	return DNSStore{dir: filepath.Join(r.layout.StateDir, dnsCredentialsDir)}
}

// Save persists creds for one root domain, overwriting any existing entry.
func (s DNSStore) Save(creds DNSCredentials) error {
	root := normalizeHost(creds.RootDomain)
	if root == "" {
		return fmt.Errorf("root domain is empty")
	}
	if creds.Provider == "" {
		return fmt.Errorf("dns provider is empty")
	}
	if creds.APIToken == "" {
		return fmt.Errorf("api token is empty")
	}
	if creds.Provider == "aliyun" && creds.APISecret == "" {
		return fmt.Errorf("aliyun requires api_secret")
	}
	dir := filepath.Join(s.dir, root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	values := map[string]string{
		"provider":   creds.Provider,
		"api_token":  creds.APIToken,
		"api_secret": creds.APISecret,
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Load returns the credentials for one root domain, or os.ErrNotExist if no
// credentials are stored for it.
func (s DNSStore) Load(root string) (DNSCredentials, error) {
	root = normalizeHost(root)
	if root == "" {
		return DNSCredentials{}, fmt.Errorf("root domain is empty")
	}
	dir := filepath.Join(s.dir, root)
	if _, err := os.Stat(dir); err != nil {
		return DNSCredentials{}, err
	}
	return DNSCredentials{
		RootDomain: root,
		Provider:   readFileTrim(dir, "provider"),
		APIToken:   readFileTrim(dir, "api_token"),
		APISecret:  readFileTrim(dir, "api_secret"),
	}, nil
}

// Delete removes the credential set for one root domain.
func (s DNSStore) Delete(root string) error {
	root = normalizeHost(root)
	if root == "" {
		return fmt.Errorf("root domain is empty")
	}
	return os.RemoveAll(filepath.Join(s.dir, root))
}

// List returns every stored credential set ordered by root domain.
func (s DNSStore) List() ([]DNSCredentials, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []DNSCredentials
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		creds, err := s.Load(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", entry.Name(), err)
		}
		out = append(out, creds)
	}
	return out, nil
}

// FindForDomain returns the longest-matching root domain credentials for a
// given (possibly subdomain) hostname. Returns os.ErrNotExist if no stored
// root domain is a suffix of host.
func (s DNSStore) FindForDomain(host string) (DNSCredentials, error) {
	host = normalizeHost(host)
	if host == "" {
		return DNSCredentials{}, fmt.Errorf("host is empty")
	}
	// Reject literal IPs (lego only does DNS-01 for FQDNs).
	if net.ParseIP(host) != nil {
		return DNSCredentials{}, fmt.Errorf("host %s is an IP literal", host)
	}
	all, err := s.List()
	if err != nil {
		return DNSCredentials{}, err
	}
	var best DNSCredentials
	bestLen := -1
	for _, creds := range all {
		root := normalizeHost(creds.RootDomain)
		if host == root || strings.HasSuffix(host, "."+root) {
			if len(root) > bestLen {
				best = creds
				bestLen = len(root)
			}
		}
	}
	if bestLen < 0 {
		return DNSCredentials{}, os.ErrNotExist
	}
	return best, nil
}

// normalizeHost canonicalises a hostname for store key lookup: lowercase,
// trimmed of whitespace, and with any trailing FQDN dot removed so
// "example.com." and "example.com" compare equal.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

func readFileTrim(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
