package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

func TestWriteSubscriptionsWithSourcesMergesLocalAndRemote(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())

	hub := testConfig(t)
	hub.DisplayName = "HUB"
	hub.Enabled = []config.Protocol{config.ProtocolHysteria2}

	// A spoke's own outputs stand in for what the hub fetches over the overlay.
	spoke := testConfig(t)
	spoke.DisplayName = "SPOKE"
	spoke.Domain = "spoke.example.com"
	spoke.Enabled = []config.Protocol{config.ProtocolHysteria2}
	spokeOut, err := spoke.buildSubscriptions()
	if err != nil {
		t.Fatalf("build spoke subscriptions: %v", err)
	}

	src := SubscriptionSource{
		Alias:       "tokyo",
		DefaultBody: []byte(spokeOut.DefaultBase64),
		ClashBody:   []byte(spokeOut.ClashFragment),
		SingBoxBody: []byte(spokeOut.SingBoxProfile),
		SurgeBody:   []byte(spokeOut.SurgeFragment),
	}
	if err := WriteSubscriptionsWithSources(layout, hub, []SubscriptionSource{src}, 0); err != nil {
		t.Fatalf("WriteSubscriptionsWithSources: %v", err)
	}

	token := SubscriptionToken(hub.Salt)
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read combined default: %v", err)
	}
	decoded, err := subscription.DecodeBase64(string(body))
	if err != nil {
		t.Fatalf("decode combined default: %v", err)
	}
	// Combined output must carry both the hub's node and the spoke's node.
	if n := strings.Count(decoded, "hysteria2://"); n != 2 {
		t.Fatalf("expected 2 hysteria2 links (hub + spoke), got %d:\n%s", n, decoded)
	}
	if !strings.Contains(decoded, "tokyo") {
		t.Fatalf("combined output should carry the spoke's alias:\n%s", decoded)
	}

	// The clash fragment must also include both nodes.
	clash, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "clashMeta", token))
	if err != nil {
		t.Fatalf("read combined clash: %v", err)
	}
	if n := strings.Count(string(clash), "type: hysteria2"); n != 2 {
		t.Fatalf("expected 2 clash proxies, got %d:\n%s", n, clash)
	}
}

func TestWriteSubscriptionsWithSourcesLocalOnly(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	hub := testConfig(t)
	hub.Enabled = []config.Protocol{config.ProtocolHysteria2}
	if err := WriteSubscriptionsWithSources(layout, hub, nil, 0); err != nil {
		t.Fatalf("WriteSubscriptionsWithSources (no sources): %v", err)
	}
	token := SubscriptionToken(hub.Salt)
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read default: %v", err)
	}
	decoded, _ := subscription.DecodeBase64(string(body))
	if n := strings.Count(decoded, "hysteria2://"); n != 1 {
		t.Fatalf("expected 1 hysteria2 link with no sources, got %d", n)
	}
}
