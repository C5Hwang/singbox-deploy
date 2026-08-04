package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func TestHubStartupMigratesPersistedSubscriptionTemplate(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	for name, value := range map[string]string{
		"domain":             "hub.example.com\n",
		"subscribe_salt":     "migration-salt\n",
		"subscribe_port":     "2096\n",
		"display_name":       "Hub\n",
		"enabled_protocols":  "hysteria2\n",
		"hysteria2_port":     "9443\n",
		"hysteria2_password": "test-password\n",
		"site_template":      "massively\n",
		"monitor":            "no\n",
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

	migrated, err := migrateHubSubscriptions(context.Background(), layout, "v-test")
	if err != nil || !migrated {
		t.Fatalf("migrateHubSubscriptions = %v, %v", migrated, err)
	}
	body, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"idle_timeout": "10h"`) {
		t.Fatalf("migrated sing-box profile did not use the current template")
	}
}
