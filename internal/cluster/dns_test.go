package cluster

import (
	"errors"
	"os"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestDNSStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewRegistry(paths.LayoutForRoot(dir)).DNS()
	in := DNSCredentials{
		RootDomain: "example.com",
		Provider:   "cloudflare",
		APIToken:   "secrettoken",
	}
	if err := store.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := store.Load("example.com")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip: got %+v want %+v", out, in)
	}
}

func TestDNSStoreSaveRequiresFields(t *testing.T) {
	dir := t.TempDir()
	store := NewRegistry(paths.LayoutForRoot(dir)).DNS()
	cases := []DNSCredentials{
		{},                                                                 // missing root
		{RootDomain: "x"},                                                  // missing provider
		{RootDomain: "x", Provider: "cloudflare"},                          // missing token
		{RootDomain: "x", Provider: "aliyun", APIToken: "k"},               // missing secret
	}
	for i, c := range cases {
		if err := store.Save(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestDNSStoreListSorted(t *testing.T) {
	store := NewRegistry(paths.LayoutForRoot(t.TempDir())).DNS()
	for _, root := range []string{"zeta.com", "alpha.com", "beta.org"} {
		if err := store.Save(DNSCredentials{RootDomain: root, Provider: "cloudflare", APIToken: "t"}); err != nil {
			t.Fatalf("Save %s: %v", root, err)
		}
	}
	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	want := []string{"alpha.com", "beta.org", "zeta.com"}
	for i, c := range got {
		if c.RootDomain != want[i] {
			t.Errorf("[%d] = %q want %q", i, c.RootDomain, want[i])
		}
	}
}

func TestDNSStoreFindForDomainLongestMatch(t *testing.T) {
	store := NewRegistry(paths.LayoutForRoot(t.TempDir())).DNS()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	must(store.Save(DNSCredentials{RootDomain: "example.com", Provider: "cloudflare", APIToken: "outer"}))
	must(store.Save(DNSCredentials{RootDomain: "sub.example.com", Provider: "cloudflare", APIToken: "inner"}))

	tests := map[string]string{
		"jp.example.com":      "outer",
		"hk.example.com":      "outer",
		"example.com":         "outer",
		"tokyo.sub.example.com": "inner",
		"sub.example.com":     "inner",
	}
	for host, wantToken := range tests {
		got, err := store.FindForDomain(host)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got.APIToken != wantToken {
			t.Errorf("%s: got %s want %s", host, got.APIToken, wantToken)
		}
	}
}

func TestDNSStoreFindForDomainReturnsNotExist(t *testing.T) {
	store := NewRegistry(paths.LayoutForRoot(t.TempDir())).DNS()
	if _, err := store.FindForDomain("noproperty.io"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestDNSStoreFindForDomainRejectsIP(t *testing.T) {
	store := NewRegistry(paths.LayoutForRoot(t.TempDir())).DNS()
	if _, err := store.FindForDomain("192.0.2.1"); err == nil {
		t.Errorf("expected error for IP literal")
	}
}

func TestDNSCredentialsEnvMap(t *testing.T) {
	cf := DNSCredentials{Provider: "cloudflare", APIToken: "tok"}
	got := cf.EnvMap()
	if got["CF_API_TOKEN"] != "tok" {
		t.Errorf("cloudflare env = %v", got)
	}
	ali := DNSCredentials{Provider: "aliyun", APIToken: "k", APISecret: "s"}
	got = ali.EnvMap()
	if got["ALICLOUD_ACCESS_KEY"] != "k" || got["ALICLOUD_SECRET_KEY"] != "s" {
		t.Errorf("aliyun env = %v", got)
	}
}
