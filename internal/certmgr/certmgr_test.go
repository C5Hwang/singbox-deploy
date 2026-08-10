package certmgr

import (
	"sync"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func TestDomainCovers(t *testing.T) {
	tests := []struct {
		base, domain string
		want         bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "one.example.com", true},
		{"example.com", "a.b.example.com", true},
		{"example.com", "notexample.com", false},
		{"example.com", "example.com.evil.com", false},
		{"EXAMPLE.COM", "One.Example.Com", true},
		{"example.com.", "one.example.com", true},
		{"", "example.com", false},
	}
	for _, tt := range tests {
		if got := DomainCovers(tt.base, tt.domain); got != tt.want {
			t.Errorf("DomainCovers(%q,%q)=%v want %v", tt.base, tt.domain, got, tt.want)
		}
	}
}

func TestSelectCredentialLongestSuffixWins(t *testing.T) {
	creds := []DNSCredential{
		{Domain: "example.com", Provider: "cloudflare", Credential: "apex"},
		{Domain: "sub.example.com", Provider: "aliyun", Credential: "k:s"},
	}
	got, ok := SelectCredential(creds, "a.sub.example.com")
	if !ok {
		t.Fatalf("expected a match")
	}
	if got.Domain != "sub.example.com" {
		t.Fatalf("expected most-specific match, got %q", got.Domain)
	}

	got, ok = SelectCredential(creds, "other.example.com")
	if !ok || got.Domain != "example.com" {
		t.Fatalf("expected apex match, got %q ok=%v", got.Domain, ok)
	}

	if _, ok := SelectCredential(creds, "example.org"); ok {
		t.Fatalf("unexpected match for unrelated domain")
	}
}

func TestCredentialValidate(t *testing.T) {
	valid := []DNSCredential{
		{Domain: "example.com", Provider: "cloudflare", Credential: "token"},
		{Domain: "example.com", Provider: "aliyun", Credential: "key:secret"},
	}
	for _, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("expected valid, got %v for %+v", err, c)
		}
	}
	invalid := []DNSCredential{
		{Domain: "", Provider: "cloudflare", Credential: "t"},
		{Domain: "example.com", Provider: "route53", Credential: "t"},
		{Domain: "example.com", Provider: "cloudflare", Credential: ""},
		{Domain: "example.com", Provider: "aliyun", Credential: "no-colon"},
	}
	for _, c := range invalid {
		if err := c.Validate(); err == nil {
			t.Errorf("expected invalid: %+v", c)
		}
	}
}

func TestCredentialEnv(t *testing.T) {
	cf := DNSCredential{Provider: "cloudflare", Credential: "tok"}.Env()
	if cf["CF_API_TOKEN"] != "tok" {
		t.Fatalf("cloudflare env = %v", cf)
	}
	ali := DNSCredential{Provider: "aliyun", Credential: "key:secret"}.Env()
	if ali["ALICLOUD_ACCESS_KEY"] != "key" || ali["ALICLOUD_SECRET_KEY"] != "secret" {
		t.Fatalf("aliyun env = %v", ali)
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	want := []DNSCredential{
		{Domain: "example.com", Provider: "cloudflare", Credential: "tok"},
		{Domain: "other.net", Provider: "aliyun", Credential: "k:s"},
	}
	if err := SaveCredentials(layout, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	got, err := LoadCredentials(layout)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d creds want %d", len(got), len(want))
	}
	if got[0].Domain != "example.com" || got[0].Provider != "cloudflare" || got[0].Credential != "tok" {
		t.Fatalf("first credential mismatch: %+v", got[0])
	}
}

func TestConcurrentCredentialUpsertsDoNotLoseDomains(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	credentials := []DNSCredential{
		{Domain: "example.com", Provider: ProviderCloudflare, Credential: "one"},
		{Domain: "example.net", Provider: ProviderCloudflare, Credential: "two"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(credentials))
	for _, credential := range credentials {
		credential := credential
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- UpsertCredential(layout, credential)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadCredentials(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("concurrent credentials = %+v", got)
	}
}

func TestRegisterAndInventory(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := Register(layout, "example.com"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Re-registering a managed domain must not create a second entry.
	if err := Register(layout, "EXAMPLE.COM."); err != nil {
		t.Fatalf("Register repeat: %v", err)
	}
	inv, err := Inventory(layout)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(inv) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(inv))
	}
	if inv[0].Domain != "example.com" {
		t.Fatalf("unexpected inventory entry: %+v", inv[0])
	}
	if inv[0].Present {
		t.Fatalf("no cert file exists yet, Present should be false")
	}
	if err := Deregister(layout, "example.com"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	inv, _ = Inventory(layout)
	if len(inv) != 0 {
		t.Fatalf("expected empty inventory after deregister, got %d", len(inv))
	}
}

func TestSeedLegacyCredentials(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeState(t, layout, "acme_challenge", "dns-01")
	writeState(t, layout, "dns_provider", "cloudflare")
	writeState(t, layout, "dns_credential", "legacy-token")
	writeState(t, layout, "domain", "legacy.example.com")

	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatalf("SeedLegacyCredentials: %v", err)
	}
	creds, _ := LoadCredentials(layout)
	if len(creds) != 1 || creds[0].Domain != "legacy.example.com" || creds[0].Credential != "legacy-token" {
		t.Fatalf("legacy credential not seeded: %+v", creds)
	}
	if !CredentialCovers(creds, "legacy.example.com") {
		t.Fatalf("seeded credential should cover its own domain")
	}
	// Idempotent: a second run must not duplicate.
	if err := SeedLegacyCredentials(layout); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	creds, _ = LoadCredentials(layout)
	if len(creds) != 1 {
		t.Fatalf("seed not idempotent, got %d creds", len(creds))
	}
}

func writeState(t *testing.T, layout paths.Layout, name, value string) {
	t.Helper()
	s := state.NewStore(layout.StateDir)
	if err := s.WriteString(name, value+"\n", 0o600); err != nil {
		t.Fatalf("write state %s: %v", name, err)
	}
}

func TestZoneOfReturnsTheRegistrableParent(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"us1.example.com", "example.com"},
		{"a.b.example.com", "example.com"},
		{"example.com", "example.com"},
		// A multi-label public suffix must not be mistaken for the zone.
		{"vpn.example.co.uk", "example.co.uk"},
		{"EXAMPLE.COM.", "example.com"},
		// An unlisted TLD is treated as the whole suffix, so the label under it
		// is the zone.
		{"host.internal.invalidtld", "internal.invalidtld"},
		// A bare public suffix cannot be split; it is its own best answer.
		{"co.uk", "co.uk"},
	}
	for _, tc := range cases {
		got, err := ZoneOf(tc.domain)
		if err != nil {
			t.Fatalf("ZoneOf(%q) error: %v", tc.domain, err)
		}
		if got != tc.want {
			t.Fatalf("ZoneOf(%q) = %q, want %q", tc.domain, got, tc.want)
		}
	}
	if _, err := ZoneOf("not a domain"); err == nil {
		t.Fatal("ZoneOf accepted an invalid domain")
	}
}
