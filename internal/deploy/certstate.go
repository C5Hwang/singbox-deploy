package deploy

import (
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
)

// certificateRenewalState returns the state keys the cert-renew timer reads. It
// is the single definition of those keys; both the early renewal-state write
// (stepServices) and the final full-state write (WriteInstallState) derive the
// renewal portion from here so the two can never drift.
func certificateRenewalState(cfg Config) map[string]string {
	return map[string]string{
		"acme_challenge": string(cfg.Challenge),
		"domain":         cfg.Domain,
		"dns_credential": dnsCredentialForState(cfg),
		"dns_provider":   cfg.DNSProvider,
		"email":          cfg.Email,
	}
}

func (o *Orchestrator) writeCertificateRenewalState(cfg Config) error {
	for name, value := range certificateRenewalState(cfg) {
		if err := WriteFile(filepath.Join(o.Layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// DNSCredentialsForProvider decodes the single stored/entered credential string
// into the lego provider environment map. It is the inverse of
// dnsCredentialForState and the one place this "single string ↔ map" convention
// lives (used by install, renewal, and the install UI).
func DNSCredentialsForProvider(provider, credential string) map[string]string {
	creds := map[string]string{}
	if credential == "" {
		return creds
	}
	switch provider {
	case "cloudflare":
		creds["CF_API_TOKEN"] = credential
	case "aliyun":
		if key, secret, ok := strings.Cut(credential, ":"); ok {
			creds["ALICLOUD_ACCESS_KEY"] = key
			creds["ALICLOUD_SECRET_KEY"] = secret
		}
	}
	return creds
}

func dnsCredentialForState(cfg Config) string {
	if cfg.Challenge != acme.ChallengeDNS01 {
		return ""
	}
	switch cfg.DNSProvider {
	case "cloudflare":
		return cfg.DNSCredentials["CF_API_TOKEN"]
	case "aliyun":
		key := cfg.DNSCredentials["ALICLOUD_ACCESS_KEY"]
		secret := cfg.DNSCredentials["ALICLOUD_SECRET_KEY"]
		if key == "" || secret == "" {
			return ""
		}
		return key + ":" + secret
	default:
		return ""
	}
}
