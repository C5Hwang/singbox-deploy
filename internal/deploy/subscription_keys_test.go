package deploy_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// assertAuthKeyCipherKey checks that salt's token has its key file, readable
// by the sandboxed subscription service, outside the directory Nginx serves.
func assertAuthKeyCipherKey(t *testing.T, layout paths.Layout, salt string) {
	t.Helper()
	path := filepath.Join(layout.SubscribeKeysDir, deploy.SubscriptionToken(salt))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file for %q: %v", salt, err)
	}
	if want := subscription.AuthKeyCipherKeyFile(salt); !bytes.Equal(got, want) {
		t.Fatalf("key file for %q = %q, want %q", salt, got, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("key file mode = %v, %v; want 0644", info.Mode(), err)
	}
	if info, err := os.Stat(layout.SubscribeKeysDir); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("keys dir mode = %v, %v; want 0755", info.Mode(), err)
	}
	if rel, err := filepath.Rel(layout.SubscribeDir, path); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("key file %s is inside the published directory %s", path, layout.SubscribeDir)
	}
}

func assertNoAuthKeyCipherKey(t *testing.T, layout paths.Layout, salt string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(layout.SubscribeKeysDir, deploy.SubscriptionToken(salt))); !os.IsNotExist(err) {
		t.Fatalf("key file for retired salt %q survived: %v", salt, err)
	}
}

// Every published group gets its key file, and deleting a group, emptying it
// or changing its salt sweeps the old token's key with its files, so a leaked
// client link of a destroyed subscription has nothing left to open it with.
func TestWriteSubscriptionGroupsPublishesAndSweepsAuthKeyCipherKeys(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "keep", IncludeLocal: true},
		{Salt: "delete", IncludeLocal: true},
		{Salt: "resalt", IncludeLocal: true},
		{Salt: "empty", IncludeLocal: true},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}
	for _, salt := range []string{"keep", "delete", "resalt", "empty"} {
		assertAuthKeyCipherKey(t, layout, salt)
	}
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "keep", IncludeLocal: true},
		{Salt: "resalted", IncludeLocal: true},
		{Salt: "empty"},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups after the changes: %v", err)
	}
	assertAuthKeyCipherKey(t, layout, "keep")
	assertAuthKeyCipherKey(t, layout, "resalted")
	for _, salt := range []string{"delete", "resalt", "empty"} {
		assertNoAuthKeyCipherKey(t, layout, salt)
	}
	entries, err := os.ReadDir(layout.SubscribeKeysDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("keys dir = %v, %v; want the two live tokens", entries, err)
	}
}

// The single-salt path (a standalone node, a spoke) keeps exactly the current
// salt's key, sweeping a previous salt's and anything else left in the dir.
func TestWriteSubscriptionsKeepsOnlyTheCurrentAuthKeyCipherKey(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	if err := deploy.WriteSubscriptions(layout, cfg); err != nil {
		t.Fatalf("WriteSubscriptions: %v", err)
	}
	assertAuthKeyCipherKey(t, layout, cfg.Salt)
	stray := filepath.Join(layout.SubscribeKeysDir, deploy.SubscriptionToken("stray")+".tmp-123")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	crashed := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stray, crashed, crashed); err != nil {
		t.Fatal(err)
	}
	// Another process's atomic write still in flight must survive the sweep,
	// or that write's rename fails.
	inFlight := filepath.Join(layout.SubscribeKeysDir, deploy.SubscriptionToken("other")+".tmp-456")
	if err := os.WriteFile(inFlight, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := cfg.Salt
	cfg.Salt = "rotated"
	if err := deploy.WriteSubscriptions(layout, cfg); err != nil {
		t.Fatalf("WriteSubscriptions after rotation: %v", err)
	}
	assertAuthKeyCipherKey(t, layout, "rotated")
	assertNoAuthKeyCipherKey(t, layout, old)
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray key dir entry survived: %v", err)
	}
	if _, err := os.Stat(inFlight); err != nil {
		t.Fatalf("in-flight temp file was swept: %v", err)
	}
}

// Nginx must never serve the key files: no root or alias in the managed config
// may cover the keys directory. It sits beside the subscription directory, so
// only an alias of the layout root itself could reach it.
func TestManagedNginxConfigNeverServesAuthKeyCipherKeys(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	cfg.DeployMonitor = true
	confPath := filepath.Join(t.TempDir(), "singbox-deploy.conf")
	if err := deploy.WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	served := regexp.MustCompile(`(?m)^\s*(?:root|alias)\s+"?([^";]+)"?;`).FindAllStringSubmatch(string(conf), -1)
	if len(served) == 0 {
		t.Fatalf("found no root or alias directive in:\n%s", conf)
	}
	for _, m := range served {
		rel, err := filepath.Rel(filepath.Clean(m[1]), layout.SubscribeKeysDir)
		if err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("Nginx serves %s, which covers the keys directory %s", m[1], layout.SubscribeKeysDir)
		}
	}
}
