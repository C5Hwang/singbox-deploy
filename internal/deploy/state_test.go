package deploy

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// Management flows reload config via LoadProtocolConfig and persist it again
// with WriteInstallState; the DNS credential must survive that round trip or
// dns-01 renewals break after any management operation.
func TestLoadProtocolConfigRestoresDNSCredentials(t *testing.T) {
	tests := []struct {
		provider   string
		credential map[string]string
	}{
		{"cloudflare", map[string]string{"CF_API_TOKEN": "cf-token"}},
		{"aliyun", map[string]string{"ALICLOUD_ACCESS_KEY": "ak", "ALICLOUD_SECRET_KEY": "sk"}},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			cfg := testConfig(t)
			cfg.Challenge = acme.ChallengeDNS01
			cfg.DNSProvider = tc.provider
			cfg.DNSCredentials = tc.credential
			if err := WriteInstallState(layout.StateDir, cfg); err != nil {
				t.Fatalf("WriteInstallState: %v", err)
			}
			for i := 0; i < 2; i++ {
				loaded, err := LoadProtocolConfig(layout)
				if err != nil {
					t.Fatalf("LoadProtocolConfig (pass %d): %v", i, err)
				}
				for k, v := range tc.credential {
					if loaded.DNSCredentials[k] != v {
						t.Fatalf("DNSCredentials[%s] = %q after pass %d, want %q", k, loaded.DNSCredentials[k], i, v)
					}
				}
				if err := WriteInstallState(layout.StateDir, loaded); err != nil {
					t.Fatalf("WriteInstallState (pass %d): %v", i, err)
				}
			}
		})
	}
}
