package install

import (
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
)

func (o *Orchestrator) writeCertificateRenewalState(cfg Config) error {
	state := map[string]string{
		"acme_challenge": string(cfg.Challenge),
		"domain":         cfg.Domain,
		"dns_credential": dnsCredentialForState(cfg),
		"dns_provider":   cfg.DNSProvider,
		"email":          cfg.Email,
	}
	for name, value := range state {
		if err := writeFile(filepath.Join(o.Layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
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
