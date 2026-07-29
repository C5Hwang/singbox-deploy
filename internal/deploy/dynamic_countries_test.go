package deploy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDynamicCountryDetection(t *testing.T) {
	// US-prefixed nodes should only produce a US country group
	usTags := []string{"🇺🇸 US-vps1-VLESS", "🇺🇸 US-vps1-Hysteria2"}
	countries := detectCountries(usTags)
	if len(countries) != 1 {
		t.Fatalf("expected 1 country for US nodes, got %d: %+v", len(countries), countries)
	}
	if countries[0].Tag != "🇺🇸 美国节点" {
		t.Fatalf("expected US tag, got %q", countries[0].Tag)
	}

	// JP + HK nodes should produce 2 country groups in order (HK first per knownCountries)
	mixedTags := []string{"🇯🇵 JP-vps1-VLESS", "🇭🇰 HK-vps2-Hysteria2"}
	countries = detectCountries(mixedTags)
	if len(countries) != 2 {
		t.Fatalf("expected 2 countries, got %d", len(countries))
	}
	if countries[0].Tag != "🇭🇰 香港节点" {
		t.Fatalf("expected HK first, got %q", countries[0].Tag)
	}
	if countries[1].Tag != "🇯🇵 日本节点" {
		t.Fatalf("expected JP second, got %q", countries[1].Tag)
	}

	// Empty tags should produce no countries
	countries = detectCountries(nil)
	if len(countries) != 0 {
		t.Fatalf("expected 0 countries for nil tags, got %d", len(countries))
	}

	// Taiwan uses 🇼🇸 flag
	twTags := []string{"🇼🇸 TW-vps-VLESS"}
	countries = detectCountries(twTags)
	if len(countries) != 1 || countries[0].Tag != "🇼🇸 台湾节点" {
		t.Fatalf("expected TW with 🇼🇸, got %+v", countries)
	}
}

func TestFillProfilesProducesValidOutput(t *testing.T) {
	outbounds := []map[string]any{
		{"type": "vless", "tag": "🇺🇸 US-vps1-VLESS"},
		{"type": "hysteria2", "tag": "🇺🇸 US-vps1-Hysteria2"},
	}
	var out subscriptionOutputs
	err := fillProfiles(&out, Config{Domain: "example.com", SubscribePort: 2096, Salt: "salt"}, outbounds)
	if err != nil {
		t.Fatalf("fillProfiles error: %v", err)
	}

	// Sing-box profile should be valid JSON
	var parsed any
	if err := json.Unmarshal([]byte(out.SingBoxProfile), &parsed); err != nil {
		t.Fatalf("sing-box profile not valid JSON: %v\n%s", err, out.SingBoxProfile)
	}

	// Should have US country group but NOT JP/TW/HK
	if !strings.Contains(out.SingBoxProfile, `"🇺🇸 美国节点"`) {
		t.Fatal("sing-box profile missing US country group")
	}
	for _, absent := range []string{`"🇯🇵 日本节点"`, `"🇼🇸 台湾节点"`, `"🇭🇰 香港节点"`} {
		if strings.Contains(out.SingBoxProfile, absent) {
			t.Fatalf("sing-box profile should not have %s (no matching nodes)", absent)
		}
	}

	// Clash profile should have US group but not others
	if !strings.Contains(out.ClashProfile, "🇺🇸 美国节点") {
		t.Fatal("clash profile missing US country group")
	}
	for _, absent := range []string{"🇯🇵 日本节点", "🇼🇸 台湾节点", "🇭🇰 香港节点"} {
		if strings.Contains(out.ClashProfile, absent) {
			t.Fatalf("clash profile should not have %s (no matching nodes)", absent)
		}
	}

	for _, want := range []string{
		"allow-lan: false",
		`bind-address: "127.0.0.1"`,
		"external-controller: 127.0.0.1:9090",
		"listen: 127.0.0.1:1053",
	} {
		if !strings.Contains(out.ClashProfile, want) {
			t.Fatalf("Clash profile missing safe local binding %q", want)
		}
	}
	if strings.Contains(out.ClashProfile, "0.0.0.0") {
		t.Fatalf("Clash profile exposes a client listener on all interfaces:\n%s", out.ClashProfile)
	}

	var singBoxProfile struct {
		Inbounds []struct {
			Type   string `json:"type"`
			Listen string `json:"listen"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(out.SingBoxProfile), &singBoxProfile); err != nil {
		t.Fatalf("sing-box profile not valid JSON: %v\n%s", err, out.SingBoxProfile)
	}
	for _, inbound := range singBoxProfile.Inbounds {
		if inbound.Type == "mixed" {
			if inbound.Listen != "127.0.0.1" {
				t.Fatalf("sing-box mixed inbound listen = %q, want loopback", inbound.Listen)
			}
			return
		}
	}
	t.Fatal("sing-box profile is missing its mixed inbound")
}

func TestFillProfilesRoutesAddressQueriesToFakeIP(t *testing.T) {
	outbounds := []map[string]any{
		{"type": "vless", "tag": "test-node"},
	}
	var out subscriptionOutputs
	if err := fillProfiles(&out, Config{Domain: "example.com", SubscribePort: 2096, Salt: "salt"}, outbounds); err != nil {
		t.Fatalf("fillProfiles error: %v", err)
	}

	var profile struct {
		DNS struct {
			Final string `json:"final"`
			Rules []struct {
				QueryType []string `json:"query_type"`
				Server    string   `json:"server"`
			} `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal([]byte(out.SingBoxProfile), &profile); err != nil {
		t.Fatalf("sing-box profile not valid JSON: %v\n%s", err, out.SingBoxProfile)
	}
	if profile.DNS.Final == "fakeip" {
		t.Fatal("fakeip DNS server cannot be used as the default server")
	}
	if profile.DNS.Final != "cloudflare" {
		t.Fatalf("dns.final = %q, want cloudflare", profile.DNS.Final)
	}
	for _, rule := range profile.DNS.Rules {
		if rule.Server == "fakeip" &&
			len(rule.QueryType) == 2 &&
			rule.QueryType[0] == "A" &&
			rule.QueryType[1] == "AAAA" {
			return
		}
	}
	t.Fatal("sing-box profile missing A/AAAA rule routed to fakeip")
}
