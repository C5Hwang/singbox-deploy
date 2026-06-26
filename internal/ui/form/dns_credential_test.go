package form

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
)

func TestGuessRootDomain(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"example.com":            "example.com",
		"sub.example.com":        "example.com",
		"a.b.c.example.com":      "example.com",
		"example.com.":           "example.com",
		"  example.com  ":        "example.com",
		"single":                 "single",
		"tokyo.sub.example.com.": "example.com",
	}
	for in, want := range cases {
		if got := GuessRootDomain(in); got != want {
			t.Errorf("GuessRootDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeDNSSaver records the credentials it was asked to save.
type fakeDNSSaver struct {
	last cluster.DNSCredentials
	err  error
}

func (s *fakeDNSSaver) Save(c cluster.DNSCredentials) error {
	if s.err != nil {
		return s.err
	}
	s.last = c
	return nil
}

func TestDNSCredentialFormCloudflarePath(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := NewDNSCredentialForm("tokyo.example.com", saver)
	if got := f.Fields[0].Def; got != "example.com" {
		t.Fatalf("root_domain pre-fill default = %q, want example.com", got)
	}
	// Cloudflare path is three steps: root_domain (default), provider
	// (cloudflare default), cf_token (typed). The aliyun fields are skipped.
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default provider=cloudflare
	f.Input.SetValue("tok")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit cf_token → form completes

	saved, cancelled, creds := f.State()
	if !saved || cancelled {
		t.Fatalf("expected saved=true cancelled=false, got saved=%v cancelled=%v", saved, cancelled)
	}
	if creds.RootDomain != "example.com" || creds.Provider != "cloudflare" || creds.APIToken != "tok" || creds.APISecret != "" {
		t.Fatalf("saved credentials wrong: %#v", creds)
	}
	if saver.last.APIToken != "tok" || saver.last.APISecret != "" {
		t.Fatalf("saver did not receive cloudflare token only: %#v", saver.last)
	}
}

func TestDNSCredentialFormAliyunPath(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := NewDNSCredentialForm("tokyo.example.com", saver)
	// Switch the provider selection to aliyun before committing it.
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit default root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyDown})  // move provider option to aliyun
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit provider=aliyun
	f.Input.SetValue("LTAI-key")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit aliyun_access_key_id
	f.Input.SetValue("LTAI-secret")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit aliyun_access_key_secret → form completes

	saved, cancelled, creds := f.State()
	if !saved || cancelled {
		t.Fatalf("expected saved=true cancelled=false, got saved=%v cancelled=%v", saved, cancelled)
	}
	if creds.Provider != "aliyun" || creds.APIToken != "LTAI-key" || creds.APISecret != "LTAI-secret" {
		t.Fatalf("saved aliyun credentials wrong: %#v", creds)
	}
	if saver.last.APIToken != "LTAI-key" || saver.last.APISecret != "LTAI-secret" {
		t.Fatalf("saver did not receive aliyun access key + secret: %#v", saver.last)
	}
}

func TestDNSCredentialFormAliyunRequiresSecret(t *testing.T) {
	saver := &fakeDNSSaver{}
	f := NewDNSCredentialForm("example.com", saver)
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // root_domain
	f.Update(tea.KeyMsg{Type: tea.KeyDown})  // provider → aliyun
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit provider
	f.Input.SetValue("LTAI-key")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit access key id
	// Leave access key secret blank — validation should reject and form must
	// remain on the aliyun_access_key_secret field, not saved.
	f.Input.SetValue("")
	f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	saved, cancelled, _ := f.State()
	if saved || cancelled {
		t.Fatalf("blank aliyun secret should not save: saved=%v cancelled=%v", saved, cancelled)
	}
	if got := f.ParameterForm.CurrentFieldKey(); got != "aliyun_access_key_secret" {
		t.Fatalf("cursor should remain on aliyun_access_key_secret, got %q", got)
	}
}
