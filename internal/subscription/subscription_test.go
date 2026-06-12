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

func TestRenameDefaultLinksFiltersAndRenames(t *testing.T) {
	body := strings.Join([]string{
		"vless://11111111-1111-4111-8111-111111111111@example.com:443?security=reality#JP-01",
		"vmess://unsupported",
		"hysteria2://pass@example.com:8443#HK-02",
	}, "\n")
	out := RenameDefaultLinks(body, "remote.example.com")
	if !strings.Contains(out, "#remote.example.com-01") || !strings.Contains(out, "#remote.example.com-02") {
		t.Fatalf("renamed links missing domain prefix:\n%s", out)
	}
	if strings.Contains(out, "vmess") {
		t.Fatalf("unsupported vmess link should be filtered:\n%s", out)
	}
}

func TestExtractSingBoxNodeOutboundsFiltersProfile(t *testing.T) {
	profile := []byte(`{"outbounds":[{"type":"selector","tag":"PROXY"},{"type":"vless","tag":"JP-01"},{"type":"direct","tag":"direct"}]}`)
	extracted, err := ExtractSingBoxNodeOutbounds(profile)
	if err != nil {
		t.Fatalf("ExtractSingBoxNodeOutbounds error: %v", err)
	}
	renamed, err := RenameSingBoxOutbounds(extracted, "remote.example.com")
	if err != nil {
		t.Fatalf("RenameSingBoxOutbounds error: %v", err)
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(renamed, &outbounds); err != nil {
		t.Fatalf("invalid outbounds json: %v", err)
	}
	if len(outbounds) != 1 || outbounds[0]["tag"] != "remote.example.com-01" {
		t.Fatalf("outbounds = %#v", outbounds)
	}
}

func TestRenameSurgeFragment(t *testing.T) {
	fragment := "🇯🇵 JP-01 = hysteria2, server, 443, password=abc\n🇯🇵 JP-02 = tuic-v5, server, 8443, uuid=def\n"
	out := RenameSurgeFragment(fragment, "US-vps1")
	if !strings.Contains(out, "🇺🇸 US-vps1-01 = hysteria2") {
		t.Fatalf("renamed surge fragment = %q", out)
	}
	if !strings.Contains(out, "🇺🇸 US-vps1-02 = tuic-v5") {
		t.Fatalf("renamed surge fragment = %q", out)
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
	if !strings.HasSuffix(e.SingBoxProfilesURL(), "/s/singboxProfiles/0bee89b07a248e27c83fc3d5951213c1") {
		t.Fatalf("SingBoxProfilesURL = %q", e.SingBoxProfilesURL())
	}
	if !strings.HasSuffix(e.SurgeURL(), "/s/surge/0bee89b07a248e27c83fc3d5951213c1") {
		t.Fatalf("SurgeURL = %q", e.SurgeURL())
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
