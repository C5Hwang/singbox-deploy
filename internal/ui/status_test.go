package ui

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/state"
)

func TestLoadStatusPublicIPUsesPersistedValueWithoutLookup(t *testing.T) {
	store := state.NewStore(t.TempDir())
	if err := store.WriteString("public_ip", "203.0.113.10\n", 0o600); err != nil {
		t.Fatalf("write public IP: %v", err)
	}
	oldResolve := resolveStatusIPs
	t.Cleanup(func() { resolveStatusIPs = oldResolve })
	resolveStatusIPs = func(context.Context, string) ([]net.IP, error) {
		t.Fatal("persisted public IP triggered a DNS lookup")
		return nil, nil
	}

	if got := loadStatusPublicIP(store, "vpn.example.com"); got != "203.0.113.10" {
		t.Fatalf("public IP = %q", got)
	}
}

func TestLoadStatusPublicIPBackfillsLegacyStateOnce(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)
	calls := 0
	oldResolve := resolveStatusIPs
	t.Cleanup(func() { resolveStatusIPs = oldResolve })
	resolveStatusIPs = func(ctx context.Context, domain string) ([]net.IP, error) {
		calls++
		if domain != "vpn.example.com" {
			t.Fatalf("lookup domain = %q", domain)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatalf("status DNS lookup is not tightly bounded: deadline=%v ok=%v", deadline, ok)
		}
		return []net.IP{
			net.ParseIP("10.0.0.1"),
			net.ParseIP("2001:db8::10"),
			net.ParseIP("203.0.113.10"),
		}, nil
	}

	if got := loadStatusPublicIP(store, "vpn.example.com"); got != "203.0.113.10" {
		t.Fatalf("backfilled public IP = %q", got)
	}
	if got := loadStatusPublicIP(store, "vpn.example.com"); got != "203.0.113.10" {
		t.Fatalf("cached public IP = %q", got)
	}
	if calls != 1 {
		t.Fatalf("DNS lookups = %d, want 1", calls)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "public_ip"))
	if err != nil {
		t.Fatalf("read cached public IP: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "203.0.113.10" {
		t.Fatalf("cached state = %q", raw)
	}
	if info, err := os.Stat(filepath.Join(dir, "public_ip")); err != nil {
		t.Fatalf("stat cached public IP: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("cached public IP mode = %#o", info.Mode().Perm())
	}
}

func TestLoadStatusPublicIPIgnoresPrivateOnlyDNS(t *testing.T) {
	store := state.NewStore(t.TempDir())
	oldResolve := resolveStatusIPs
	t.Cleanup(func() { resolveStatusIPs = oldResolve })
	resolveStatusIPs = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.1")}, nil
	}

	if got := loadStatusPublicIP(store, "vpn.example.com"); got != "" {
		t.Fatalf("private DNS address displayed as public: %q", got)
	}
	if _, err := store.ReadString("public_ip"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("private DNS address was cached: %v", err)
	}
}

func TestLoadStatusPublicIPReturnsUnknownPromptlyOnLookupFailure(t *testing.T) {
	store := state.NewStore(t.TempDir())
	oldResolve := resolveStatusIPs
	t.Cleanup(func() { resolveStatusIPs = oldResolve })
	resolveStatusIPs = func(context.Context, string) ([]net.IP, error) {
		return nil, fmt.Errorf("resolver unavailable")
	}

	if got := loadStatusPublicIP(store, "vpn.example.com"); got != "" {
		t.Fatalf("lookup failure public IP = %q", got)
	}
}
