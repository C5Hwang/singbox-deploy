package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func TestAgentStartupMigratesPersistedSubscriptionTemplate(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	for name, value := range map[string]string{
		"domain":                      "spoke.example.com\n",
		"subscribe_salt":              "migration-salt\n",
		"subscribe_port":              "2096\n",
		"display_name":                "Spoke\n",
		"enabled_protocols":           "hysteria2\n",
		"hysteria2_port":              "9443\n",
		"hysteria2_password":          "test-password\n",
		"site_template":               "massively\n",
		"monitor":                     "no\n",
		"subscription_schema_version": "1\n",
	} {
		if err := store.WriteString(name, value, 0o600); err != nil {
			t.Fatalf("write state %s: %v", name, err)
		}
	}
	token := deploy.SubscriptionToken("migration-salt")
	profile := filepath.Join(layout.SubscribeDir, "singboxProfiles", token)
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("stale profile without migrated field\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	migrated, err := migrateAgentSubscriptions(context.Background(), layout)
	if err != nil || !migrated {
		t.Fatalf("migrateAgentSubscriptions = %v, %v", migrated, err)
	}
	body, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"idle_timeout": "10h"`) {
		t.Fatalf("migrated sing-box profile did not use the current template")
	}
	assertAgentDomesticDNSUsesNativeDirectDialer(t, body)
	marker, err := store.ReadValue("subscription_schema_version", true)
	if err != nil || marker != "2" {
		t.Fatalf("subscription schema marker = %q, %v", marker, err)
	}
}

func TestAgentStartupRemovesLegacyACMEEmail(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	emailPath := filepath.Join(layout.StateDir, "email")
	if err := state.WriteFileAtomic(emailPath, []byte("op@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyAgentACMEEmail(layout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(emailPath); !os.IsNotExist(err) {
		t.Fatalf("legacy ACME email was not removed: %v", err)
	}
	if err := removeLegacyAgentACMEEmail(layout); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
}

func assertAgentDomesticDNSUsesNativeDirectDialer(t *testing.T, body []byte) {
	t.Helper()
	var profile struct {
		DNS struct {
			Servers []struct {
				Tag    string  `json:"tag"`
				Detour *string `json:"detour"`
			} `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatalf("decode migrated sing-box profile: %v", err)
	}
	found := map[string]bool{"dnspod": false, "alidns": false}
	for _, server := range profile.DNS.Servers {
		if _, ok := found[server.Tag]; ok {
			found[server.Tag] = true
			if server.Detour != nil {
				t.Errorf("migrated DNS server %q contains detour %q, want field absent for native direct dialer", server.Tag, *server.Detour)
			}
		}
	}
	for tag, ok := range found {
		if !ok {
			t.Errorf("migrated profile is missing DNS server %q", tag)
		}
	}
}

// A spoke upgraded from an older release carries the derived token too, and the
// Agent has to clear it without the Hub being involved.
func TestAgentStartupRemovesLegacySubscribeToken(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	tokenPath := filepath.Join(layout.StateDir, "subscribe_token")
	if err := state.WriteFileAtomic(tokenPath, []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deploy.RemoveLegacySubscribeToken(layout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("legacy subscription token was not removed: %v", err)
	}
}
