package subscription

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenFromSalt(t *testing.T) {
	got := TokenFromSalt("abc")
	want := "0bee89b07a248e27c83fc3d5951213c1"
	if got != want {
		t.Fatalf("TokenFromSalt = %s, want %s", got, want)
	}
}

func TestRewriteRemoteNodeName(t *testing.T) {
	got := RewriteRemoteNodeName("JP-01 Tokyo", "US-vps1")
	want := "🇺🇸 US-vps1-01 Tokyo"
	if got != want {
		t.Fatalf("RewriteRemoteNodeName = %q, want %q", got, want)
	}
}

func TestAddNodePrefixFlagLeavesExistingFlag(t *testing.T) {
	got := AddNodePrefixFlag("🇯🇵 JP-01")
	if got != "🇯🇵 JP-01" {
		t.Fatalf("got %q", got)
	}
}

func TestGenerateDefaultFiltersUnsupportedProtocols(t *testing.T) {
	nodes := []Node{
		{Name: "US-vps1-Reality", Protocol: "vless", Link: "vless://abc#US-vps1-Reality"},
		{Name: "bad-vmess", Protocol: "vmess", Link: "vmess://bad"},
	}
	out := GenerateDefault(nodes)
	if out == "" || !contains(out, "vless://abc") {
		t.Fatalf("default output missing vless link: %q", out)
	}
	if contains(out, "vmess://bad") {
		t.Fatalf("default output contains unsupported vmess: %q", out)
	}
}

func TestRemoteClashUsesNodeFragment(t *testing.T) {
	fragment := "proxies:\n  - name: \"JP-01\"\n    type: vless\n"
	out := RenameClashFragment(fragment, "US-vps1")
	if !contains(out, "🇺🇸 US-vps1-01") {
		t.Fatalf("renamed clash fragment = %q", out)
	}
}

func TestEncodeDecodeBase64RoundTrip(t *testing.T) {
	enc := EncodeBase64("vless://abc\nhysteria2://def")
	dec, err := DecodeBase64(enc)
	if err != nil {
		t.Fatalf("DecodeBase64 error: %v", err)
	}
	if dec != "vless://abc\nhysteria2://def" {
		t.Fatalf("round trip = %q", dec)
	}
}

func TestMergeSingBoxOutboundsRenamesRemoteTags(t *testing.T) {
	local := []byte(`[{"type":"vless","tag":"🇺🇸 US-vps1-Reality-Vision"}]`)
	remote := []byte(`[{"type":"hysteria2","tag":"JP-01 Hysteria2"}]`)
	merged, err := MergeSingBoxOutbounds(local, remote, "US-vps1")
	if err != nil {
		t.Fatalf("MergeSingBoxOutbounds error: %v", err)
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(merged, &outbounds); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, merged)
	}
	if len(outbounds) != 2 {
		t.Fatalf("expected 2 outbounds, got %d", len(outbounds))
	}
	tags := []string{outbounds[0]["tag"].(string), outbounds[1]["tag"].(string)}
	if tags[0] != "🇺🇸 US-vps1-Reality-Vision" {
		t.Fatalf("local tag changed: %q", tags[0])
	}
	if tags[1] != "🇺🇸 US-vps1-01 Hysteria2" {
		t.Fatalf("remote tag = %q, want renamed", tags[1])
	}
}

func TestRemoteEntryTokenAndURLs(t *testing.T) {
	e := RemoteEntry{Domain: "node.example.com", Port: 443, Alias: "US-vps1", Salt: "abc"}
	if e.Token() != "0bee89b07a248e27c83fc3d5951213c1" {
		t.Fatalf("Token = %s", e.Token())
	}
	got := e.DefaultURL()
	want := "https://node.example.com:443/s/default/0bee89b07a248e27c83fc3d5951213c1"
	if got != want {
		t.Fatalf("DefaultURL = %q, want %q", got, want)
	}
	if !strings.HasSuffix(e.ClashURL(), "/s/clashMeta/0bee89b07a248e27c83fc3d5951213c1") {
		t.Fatalf("ClashURL = %q", e.ClashURL())
	}
	if !strings.HasSuffix(e.SingBoxURL(), "/s/sing-box/0bee89b07a248e27c83fc3d5951213c1") {
		t.Fatalf("SingBoxURL = %q", e.SingBoxURL())
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
