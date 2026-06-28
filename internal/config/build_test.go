package config

import (
	"encoding/json"
	"testing"
)

func sampleOptions() ServerOptions {
	return ServerOptions{
		Domain:            "example.com",
		TLSCert:           "/etc/singbox-deploy/tls/example.com.crt",
		TLSKey:            "/etc/singbox-deploy/tls/example.com.key",
		RealityPrivateKey: "priv",
		RealityServerName: "www.python.org",
		RealityPort:       443,
		SubscribePort:     2096,
		User: UserCredentials{
			DisplayName:       "US-vps1",
			RealityVisionUUID: "11111111-1111-1111-1111-111111111111",
			RealityGRPCUUID:   "22222222-2222-2222-2222-222222222222",
			HysteriaPassword:  "hy-pass",
			TUICUUID:          "33333333-3333-3333-3333-333333333333",
			TUICPassword:      "tuic-pass",
			AnyTLSPassword:    "any-pass",
		},
		Ports: Ports{RealityVision: 443, RealityGRPC: 8443, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
	}
}

func TestBuildConfigIncludesSupportedProtocolsOnly(t *testing.T) {
	cfg, err := Build(sampleOptions())
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	var decoded struct {
		Inbounds []struct {
			Type string `json:"type"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, cfg)
	}
	got := map[string]int{}
	for _, in := range decoded.Inbounds {
		got[in.Type]++
	}
	for _, typ := range []string{"vless", "hysteria2", "tuic", "anytls"} {
		if got[typ] == 0 {
			t.Fatalf("missing inbound type %s in %s", typ, cfg)
		}
	}
	if got["vless"] != 2 {
		t.Fatalf("expected 2 vless inbounds (vision + grpc), got %d", got["vless"])
	}
	for _, forbidden := range []string{"trojan", "vmess", "naive"} {
		if got[forbidden] != 0 {
			t.Fatalf("forbidden inbound %s present", forbidden)
		}
	}
}

func TestBuildConfigEnabledSubset(t *testing.T) {
	o := sampleOptions()
	o.Enabled = []Protocol{ProtocolHysteria2, ProtocolTUIC}
	cfg, err := Build(o)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	var decoded struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(decoded.Inbounds) != 2 {
		t.Fatalf("expected 2 inbounds, got %d", len(decoded.Inbounds))
	}
}

func TestBuildConfigCredentialsRendered(t *testing.T) {
	cfg, err := Build(sampleOptions())
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	s := string(cfg)
	for _, want := range []string{
		"11111111-1111-1111-1111-111111111111", // reality vision uuid
		"xtls-rprx-vision",
		`"ignore_client_bandwidth": true`,
		"hy-pass",
		"tuic-pass",
		"any-pass",
		"www.python.org",
	} {
		if !contains(s, want) {
			t.Fatalf("config missing %q", want)
		}
	}
}

func TestBuildConfigSetsDefaultDomainResolver(t *testing.T) {
	cfg, err := Build(sampleOptions())
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	var decoded struct {
		Route struct {
			DefaultDomainResolver struct {
				Server   string `json:"server"`
				Strategy string `json:"strategy"`
			} `json:"default_domain_resolver"`
		} `json:"route"`
	}
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if decoded.Route.DefaultDomainResolver.Server != "google" {
		t.Fatalf("missing route.default_domain_resolver server in %s", cfg)
	}
	if decoded.Route.DefaultDomainResolver.Strategy != "prefer_ipv4" {
		t.Fatalf("missing route.default_domain_resolver strategy in %s", cfg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
