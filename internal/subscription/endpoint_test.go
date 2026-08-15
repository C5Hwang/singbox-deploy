package subscription_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

func sampleRewrite() subscription.EndpointRewrite {
	return subscription.EndpointRewrite{
		Host:  "land.example.com",
		To:    "relay.example.com",
		Ports: map[int]int{41234: 34567, 41235: 34568},
	}
}

func TestRewriteDefaultLinkEndpointMovesOnlyTheAuthority(t *testing.T) {
	link := "hysteria2://pw@land.example.com:41235?alpn=h3&sni=land.example.com#%F0%9F%87%AD%F0%9F%87%B0%20HK-Hysteria2"
	got := subscription.RewriteDefaultLinkEndpoint(link, sampleRewrite())
	if !strings.Contains(got, "@relay.example.com:34568?") {
		t.Fatalf("server address was not redirected: %s", got)
	}
	if !strings.Contains(got, "sni=land.example.com") {
		t.Fatalf("the SNI must keep naming the landing node: %s", got)
	}
	if !strings.HasSuffix(got, "#%F0%9F%87%AD%F0%9F%87%B0%20HK-Hysteria2") {
		t.Fatalf("the node name must not change: %s", got)
	}
	if !strings.HasPrefix(got, "hysteria2://pw@") {
		t.Fatalf("credentials must not change: %s", got)
	}
}

func TestRewriteDefaultLinkEndpointLeavesUnfrontedNodesAlone(t *testing.T) {
	for name, link := range map[string]string{
		"another host":  "anytls://pw@other.example.com:41234?sni=other.example.com#Other",
		"unmapped port": "anytls://pw@land.example.com:49999?sni=land.example.com#HK",
	} {
		if got := subscription.RewriteDefaultLinkEndpoint(link, sampleRewrite()); got != link {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

// The generator emits a Reality proxy with a nested reality-opts block that has
// no server/port of its own; the rewrite must find the proxy's own fields and
// leave the camouflage host in servername untouched.
func TestRewriteClashEndpointsHandlesNestedBlocks(t *testing.T) {
	fragment := `proxies:
  - name: "🇭🇰 HK-VLESS-Reality-Vision"
    type: vless
    server: land.example.com
    port: 41234
    uuid: 0f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8
    network: tcp
    tls: true
    udp: true
    flow: xtls-rprx-vision
    servername: www.microsoft.com
    client-fingerprint: chrome
    reality-opts:
      public-key: abc
      short-id: 6ba85179
  - name: "🇭🇰 HK-Hysteria2"
    type: hysteria2
    server: land.example.com
    port: 41235
    password: pw
    sni: land.example.com
    alpn:
      - h3
`
	got := subscription.RewriteClashEndpoints(fragment, sampleRewrite())
	if strings.Count(got, "server: relay.example.com") != 2 {
		t.Fatalf("both proxies should point at the relay:\n%s", got)
	}
	if !strings.Contains(got, "port: 34567") || !strings.Contains(got, "port: 34568") {
		t.Fatalf("ports were not redirected:\n%s", got)
	}
	if !strings.Contains(got, "sni: land.example.com") || !strings.Contains(got, "servername: www.microsoft.com") {
		t.Fatalf("TLS names must not change:\n%s", got)
	}
	if !strings.Contains(got, "short-id: 6ba85179") || !strings.Contains(got, `name: "🇭🇰 HK-Hysteria2"`) {
		t.Fatalf("nested fields and names must survive:\n%s", got)
	}
}

func TestRewriteClashEndpointsPreservesQuoting(t *testing.T) {
	fragment := "proxies:\n  - name: \"HK\"\n    type: anytls\n    server: \"land.example.com\"\n    port: 41234\n"
	got := subscription.RewriteClashEndpoints(fragment, sampleRewrite())
	if !strings.Contains(got, `server: "relay.example.com"`) {
		t.Fatalf("a quoted value should stay quoted:\n%s", got)
	}
}

func TestRewriteSurgeEndpointsMovesTheServerFields(t *testing.T) {
	fragment := "🇭🇰 HK-Hysteria2 = hysteria2, land.example.com, 41235, password=pw, sni=land.example.com, download-bandwidth=200\n" +
		"🇭🇰 HK-Other = anytls, other.example.com, 41235, password=pw, sni=other.example.com"
	got := subscription.RewriteSurgeEndpoints(fragment, sampleRewrite())
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "🇭🇰 HK-Hysteria2 = hysteria2, relay.example.com, 34568, password=pw, sni=land.example.com") {
		t.Fatalf("first line = %q", lines[0])
	}
	if lines[1] != "🇭🇰 HK-Other = anytls, other.example.com, 41235, password=pw, sni=other.example.com" {
		t.Fatalf("another node's line must not change: %q", lines[1])
	}
}

func TestRewriteSingBoxEndpointsLeavesTLSAlone(t *testing.T) {
	outbounds := []byte(`[
	  {"type":"hysteria2","tag":"🇭🇰 HK-Hysteria2","server":"land.example.com","server_port":41235,
	   "password":"pw","tls":{"enabled":true,"server_name":"land.example.com","alpn":["h3"]}},
	  {"type":"anytls","tag":"Other","server":"other.example.com","server_port":41235,"password":"pw"}
	]`)
	rewritten, err := subscription.RewriteSingBoxEndpoints(outbounds, sampleRewrite())
	if err != nil {
		t.Fatalf("RewriteSingBoxEndpoints: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded[0]["server"] != "relay.example.com" || decoded[0]["server_port"].(float64) != 34568 {
		t.Fatalf("outbound was not redirected: %#v", decoded[0])
	}
	if decoded[0]["tag"] != "🇭🇰 HK-Hysteria2" {
		t.Fatalf("the tag must not change: %#v", decoded[0])
	}
	tls := decoded[0]["tls"].(map[string]any)
	if tls["server_name"] != "land.example.com" {
		t.Fatalf("the SNI must keep naming the landing node: %#v", tls)
	}
	if decoded[1]["server"] != "other.example.com" {
		t.Fatalf("another node's outbound must not change: %#v", decoded[1])
	}
}

func TestAnIncompleteRewriteChangesNothing(t *testing.T) {
	link := "anytls://pw@land.example.com:41234?sni=land.example.com#HK"
	for name, r := range map[string]subscription.EndpointRewrite{
		"no host":  {To: "relay.example.com", Ports: map[int]int{41234: 34567}},
		"no relay": {Host: "land.example.com", Ports: map[int]int{41234: 34567}},
		"no ports": {Host: "land.example.com", To: "relay.example.com"},
	} {
		if r.Valid() {
			t.Fatalf("%s should not be a usable rewrite", name)
		}
		if got := subscription.RewriteDefaultLinkEndpoint(link, r); got != link {
			t.Fatalf("%s: %s", name, got)
		}
	}
}
