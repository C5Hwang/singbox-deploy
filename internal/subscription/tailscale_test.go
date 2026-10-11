package subscription

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

var testClientOpts = TailscaleOptions{Role: TailscaleClient, Hostname: "nas-1", AuthKey: "tskey-auth-abc_DEF-123"}

func TestParseTailscaleQuery(t *testing.T) {
	enc := testEncryptedAuthKey(t, "salt", "nas-1", testClientOpts.AuthKey)
	tests := []struct {
		name    string
		query   string
		want    TailscaleOptions
		wantOK  bool
		wantErr bool
	}{
		{name: "no query", query: ""},
		{name: "unrelated keys only", query: "hostname=a&authkey=tskey-x&utm=1"},
		{name: "server", query: "tailscale=server", want: TailscaleOptions{Role: TailscaleServer}, wantOK: true},
		{name: "server ignores unknown keys", query: "tailscale=server&flag=1", want: TailscaleOptions{Role: TailscaleServer}, wantOK: true},
		{
			name: "client", query: "tailscale=client&hostname=nas-1&authkey=" + enc,
			want: TailscaleOptions{Role: TailscaleClient, Hostname: "nas-1", EncryptedAuthKey: enc}, wantOK: true,
		},
		{name: "empty role", query: "tailscale=", wantErr: true},
		{name: "unknown role", query: "tailscale=peer", wantErr: true},
		{name: "role is case sensitive", query: "tailscale=Server", wantErr: true},
		{name: "server with authkey", query: "tailscale=server&authkey=" + enc, wantErr: true},
		{name: "server with empty hostname", query: "tailscale=server&hostname=", wantErr: true},
		{name: "client missing hostname", query: "tailscale=client&authkey=" + enc, wantErr: true},
		{name: "client missing authkey", query: "tailscale=client&hostname=nas", wantErr: true},
		{name: "client plaintext authkey", query: "tailscale=client&hostname=nas&authkey=" + testClientOpts.AuthKey, wantErr: true},
		{name: "client long plaintext authkey", query: "tailscale=client&hostname=nas&authkey=tskey-auth-" + strings.Repeat("a", 80), wantErr: true},
		{name: "client authkey without version", query: "tailscale=client&hostname=nas&authkey=" + strings.TrimPrefix(enc, "v1."), wantErr: true},
		{name: "client authkey unknown version", query: "tailscale=client&hostname=nas&authkey=v2." + strings.TrimPrefix(enc, "v1."), wantErr: true},
		{name: "client authkey too short", query: "tailscale=client&hostname=nas&authkey=" + "v1." + strings.Repeat("A", 46), wantErr: true},
		{name: "client authkey too long", query: "tailscale=client&hostname=nas&authkey=" + "v1." + strings.Repeat("A", 313), wantErr: true},
		{name: "client authkey padded", query: "tailscale=client&hostname=nas&authkey=" + enc + "%3D", wantErr: true},
		{name: "client authkey std base64", query: "tailscale=client&hostname=nas&authkey=v1." + strings.Repeat("A", 60) + "%2B%2F", wantErr: true},
		{name: "hostname with newline", query: "tailscale=client&hostname=nas%0Ax&authkey=" + enc, wantErr: true},
		{name: "hostname with comma", query: "tailscale=client&hostname=nas,x&authkey=" + enc, wantErr: true},
		{name: "hostname with quote", query: "tailscale=client&hostname=nas%22x&authkey=" + enc, wantErr: true},
		{name: "hostname with dot", query: "tailscale=client&hostname=nas.lan&authkey=" + enc, wantErr: true},
		{name: "hostname leading hyphen", query: "tailscale=client&hostname=-nas&authkey=" + enc, wantErr: true},
		{name: "hostname too long", query: "tailscale=client&hostname=" + strings.Repeat("a", 64) + "&authkey=" + enc, wantErr: true},
		{name: "authkey with newline", query: "tailscale=client&hostname=nas&authkey=" + enc + "%0Ax", wantErr: true},
		{name: "authkey with comma", query: "tailscale=client&hostname=nas&authkey=" + enc + ",x", wantErr: true},
		{name: "authkey with quote", query: "tailscale=client&hostname=nas&authkey=" + enc + "%22x", wantErr: true},
		{name: "authkey with space", query: "tailscale=client&hostname=nas&authkey=" + enc + "+x", wantErr: true},
		{name: "repeated role", query: "tailscale=server&tailscale=server", wantErr: true},
		{name: "repeated hostname", query: "tailscale=client&hostname=a&hostname=b&authkey=" + enc, wantErr: true},
		{name: "repeated authkey", query: "tailscale=client&hostname=a&authkey=" + enc + "&authkey=" + enc, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			got, ok, err := ParseTailscaleQuery(q)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("got (%+v, %v), want (%+v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestProfileFormatForDir(t *testing.T) {
	for dir, want := range map[string]ProfileFormat{
		"singboxProfiles":   ProfileSingBox,
		"clashMetaProfiles": ProfileClashMeta,
		"surgeProfiles":     ProfileSurge,
	} {
		if got, ok := ProfileFormatForDir(dir); !ok || got != want {
			t.Errorf("ProfileFormatForDir(%q) = %q, %v", dir, got, ok)
		}
	}
	for _, dir := range []string{"default", "clashMeta", "surge", "sing-box", "sing-boxProfiles", "SurgeProfiles", ""} {
		if got, ok := ProfileFormatForDir(dir); ok {
			t.Errorf("ProfileFormatForDir(%q) = %q, want not ok", dir, got)
		}
	}
}

func TestApplyTailscaleRejectsInvalidOptions(t *testing.T) {
	body := []byte(singBoxFixture)
	for _, opts := range []TailscaleOptions{
		{},
		{Role: "peer"},
		{Role: TailscaleServer, AuthKey: "tskey-auth-x"},
		{Role: TailscaleServer, EncryptedAuthKey: "x"},
		{Role: TailscaleClient, Hostname: "nas"},
		// A parsed but undecrypted client must never reach a profile.
		{Role: TailscaleClient, Hostname: "nas", EncryptedAuthKey: testEncryptedAuthKey(t, "salt", "nas", "tskey-auth-x")},
		{Role: TailscaleClient, Hostname: "a\nb", AuthKey: "tskey-auth-x"},
	} {
		if _, err := ApplyTailscale(ProfileSingBox, body, opts); err == nil {
			t.Errorf("ApplyTailscale accepted %+v", opts)
		}
	}
	if _, err := ApplyTailscale("surge", body, TailscaleOptions{Role: TailscaleServer}); err == nil {
		t.Error("ApplyTailscale accepted a node fragment format")
	}
}

const singBoxFixture = `{
  "dns": {
    "servers": [{"type": "local", "tag": "local"}, {"type": "fakeip", "tag": "fakeip"}],
    "rules": [{"query_type": ["A", "AAAA"], "action": "route", "server": "fakeip"}]
  },
  "inbounds": [{"type": "tun", "tag": "tun-in", "route_exclude_address": ["192.168.0.0/16", "100.64.0.0/10"], "mtu": 9000}],
  "endpoints": [{"type": "wireguard", "tag": "wg"}],
  "outbounds": [
    {"type": "selector", "tag": "全球流量", "outbounds": ["🇺🇸 a&b<c>"]},
    {"type": "direct", "tag": "DIRECT"}
  ],
  "route": {
    "rules": [{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}, {"ip_is_private": true, "outbound": "DIRECT"}],
    "final": "全球流量"
  },
  "experimental": {"ratio": 1.50, "empty": {}, "list": []}
}`

func TestApplyTailscaleSingBoxMergesExistingEntries(t *testing.T) {
	server, err := ApplyTailscale(ProfileSingBox, []byte(singBoxFixture), TailscaleOptions{Role: TailscaleServer})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	s := string(server)
	for _, want := range []string{
		// Existing excludes are kept and the duplicate is not repeated.
		"\"route_exclude_address\": [\n        \"192.168.0.0/16\",\n        \"100.64.0.0/10\",\n        \"fd7a:115c:a1e0::/48\"\n      ],\n      \"mtu\": 9000",
		`"outbounds": [` + "\n" + `        "🇺🇸 a&b<c>"`,
		`"ratio": 1.50`,
		`"empty": {}`,
		`"list": []`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("server output missing %q:\n%s", want, s)
		}
	}

	client, err := ApplyTailscale(ProfileSingBox, []byte(singBoxFixture), testClientOpts)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	var got struct {
		Endpoints []struct {
			Tag string `json:"tag"`
		} `json:"endpoints"`
		Inbounds []struct {
			RouteExcludeAddress []string `json:"route_exclude_address"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(client, &got); err != nil {
		t.Fatalf("client output is not valid JSON: %v", err)
	}
	if len(got.Endpoints) != 2 || got.Endpoints[0].Tag != "wg" || got.Endpoints[1].Tag != "Tailscale" {
		t.Fatalf("endpoints = %+v, want the tailscale endpoint appended", got.Endpoints)
	}
	if len(got.Inbounds[0].RouteExcludeAddress) != 2 {
		t.Fatalf("client role changed route_exclude_address: %v", got.Inbounds[0].RouteExcludeAddress)
	}
}

func TestApplyTailscaleSingBoxRequiresAnchors(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		role TailscaleRole
	}{
		{"not json", func(string) string { return "{" }, TailscaleServer},
		{"trailing data", func(s string) string { return s + "{}" }, TailscaleServer},
		{"duplicate key", func(s string) string {
			return strings.Replace(s, `"final": "全球流量"`, `"final": "a", "final": "b"`, 1)
		}, TailscaleServer},
		{"no hijack-dns", func(s string) string { return strings.Replace(s, `"hijack-dns"`, `"reject"`, 1) }, TailscaleServer},
		{"no local server", func(s string) string { return strings.Replace(s, `"tag": "local"`, `"tag": "sys"`, 1) }, TailscaleServer},
		{"no tun", func(s string) string { return strings.Replace(s, `"type": "tun"`, `"type": "mixed"`, 1) }, TailscaleServer},
		{"no DIRECT", func(s string) string { return strings.Replace(s, `"tag": "DIRECT"`, `"tag": "direct"`, 1) }, TailscaleServer},
		{"dns rules not array", func(s string) string {
			return strings.Replace(s, `"rules": [{"query_type"`, `"rules": {}, "x": [{"query_type"`, 1)
		}, TailscaleServer},
		{"tag taken", func(s string) string { return strings.Replace(s, `"tag": "wg"`, `"tag": "Tailscale"`, 1) }, TailscaleClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := TailscaleOptions{Role: tt.role}
			if tt.role == TailscaleClient {
				opts = testClientOpts
			}
			if out, err := ApplyTailscale(ProfileSingBox, []byte(tt.edit(singBoxFixture)), opts); err == nil {
				t.Fatalf("expected an error, got:\n%s", out)
			}
		})
	}
}

const clashFixture = `mixed-port: 7890
tun:
  enable: true
  route-exclude-address:
  - 192.168.0.0/16
dns:
  enable: true
  fake-ip-filter:
    - "+.lan"
  nameserver-policy:
    "+.corp": 10.0.0.1

proxies:
  - name: a
    type: ss

proxy-groups:
  - name: 全球流量
    type: select
    proxies:
      - a

rules:
- MATCH,全球流量
`

func TestApplyTailscaleClashMergesExistingBlocks(t *testing.T) {
	server, err := ApplyTailscale(ProfileClashMeta, []byte(clashFixture), TailscaleOptions{Role: TailscaleServer})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	wantServer := `mixed-port: 7890
tun:
  enable: true
  route-exclude-address:
  - 192.168.0.0/16
  - 100.64.0.0/10
  - fd7a:115c:a1e0::/48
dns:
  enable: true
  fake-ip-filter:
    - "+.lan"
    - "+.ts.net"
  nameserver-policy:
    "+.corp": 10.0.0.1
    "+.ts.net": "system"

proxies:
  - name: a
    type: ss

proxy-groups:
  - name: 全球流量
    type: select
    proxies:
      - a

rules:
- DOMAIN-SUFFIX,ts.net,DIRECT
- IP-CIDR,100.64.0.0/10,DIRECT,no-resolve
- IP-CIDR6,fd7a:115c:a1e0::/48,DIRECT,no-resolve
- MATCH,全球流量
`
	if string(server) != wantServer {
		t.Fatalf("server output:\n%s\nwant:\n%s", server, wantServer)
	}

	client, err := ApplyTailscale(ProfileClashMeta, []byte(clashFixture), testClientOpts)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	for _, want := range []string{
		"proxies:\n  - name: a\n    type: ss\n  - name: Tailscale\n    type: tailscale\n    hostname: \"nas-1\"\n",
		"  route-exclude-address:\n  - 192.168.0.0/16\ndns:",
		"    \"+.ts.net\": \"ts://Tailscale\"\n",
		"rules:\n- DOMAIN-SUFFIX,ts.net,Tailscale\n",
		"- IP-CIDR6,fd7a:115c:a1e0::/48,Tailscale,no-resolve\n- MATCH,全球流量\n",
	} {
		if !strings.Contains(string(client), want) {
			t.Fatalf("client output missing %q:\n%s", want, client)
		}
	}
}

func TestApplyTailscaleClashRequiresAnchors(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		role TailscaleRole
	}{
		{"no dns", func(s string) string { return strings.Replace(s, "dns:", "dnsx:", 1) }, TailscaleServer},
		{"no rules", func(s string) string { return strings.Replace(s, "rules:", "rulez:", 1) }, TailscaleServer},
		{"inline dns", func(s string) string { return strings.Replace(s, "dns:\n", "dns: {}\nx:\n", 1) }, TailscaleServer},
		{"repeated dns", func(s string) string { return s + "dns:\n  enable: false\n" }, TailscaleServer},
		{"fake-ip rule mode", func(s string) string {
			return strings.Replace(s, "  enable: true\n  fake-ip", "  enable: true\n  fake-ip-filter-mode: rule\n  fake-ip", 1)
		}, TailscaleServer},
		{"policy already maps ts.net", func(s string) string { return strings.Replace(s, `"+.corp"`, `"+.ts.net"`, 1) }, TailscaleServer},
		{"no proxy-groups", func(s string) string { return strings.Replace(s, "proxy-groups:", "groups:", 1) }, TailscaleClient},
		{"proxy name taken", func(s string) string { return strings.Replace(s, "- name: a", "- name: Tailscale", 1) }, TailscaleClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := TailscaleOptions{Role: tt.role}
			if tt.role == TailscaleClient {
				opts = testClientOpts
			}
			if out, err := ApplyTailscale(ProfileClashMeta, []byte(tt.edit(clashFixture)), opts); err == nil {
				t.Fatalf("expected an error, got:\n%s", out)
			}
		})
	}
}

const surgeFixture = `[General]
# > skip proxy
skip-proxy = 100.64.0.0/10
always-real-ip = *.lan
tun-excluded-routes = 239.255.255.250/32

[Proxy]
DIRECT = direct

[Proxy Group]
全球流量 = select, DIRECT

[Host]
router.lan = 192.168.1.1

[Rule]
FINAL,全球流量
`

func TestApplyTailscaleSurgeMergesExistingKeys(t *testing.T) {
	server, err := ApplyTailscale(ProfileSurge, []byte(surgeFixture), TailscaleOptions{Role: TailscaleServer})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	wantServer := `[General]
# > skip proxy
skip-proxy = 100.64.0.0/10, *.ts.net
always-real-ip = *.lan, *.ts.net
tun-excluded-routes = 239.255.255.250/32, 100.64.0.0/10, fd7a:115c:a1e0::/48

[Proxy]
DIRECT = direct

[Proxy Group]
全球流量 = select, DIRECT

[Host]
router.lan = 192.168.1.1
*.ts.net = server:system

[Rule]
DOMAIN-SUFFIX,ts.net,DIRECT
IP-CIDR,100.64.0.0/10,DIRECT,no-resolve
IP-CIDR6,fd7a:115c:a1e0::/48,DIRECT,no-resolve
FINAL,全球流量
`
	if string(server) != wantServer {
		t.Fatalf("server output:\n%s\nwant:\n%s", server, wantServer)
	}

	client, err := ApplyTailscale(ProfileSurge, []byte(surgeFixture), testClientOpts)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	wantClient := `[General]
# > skip proxy
always-real-ip = *.lan, *.ts.net
tun-excluded-routes = 239.255.255.250/32

[Proxy]
DIRECT = direct
Tailscale = tailscale, section-name=tailnet

[Proxy Group]
全球流量 = select, DIRECT

[Host]
router.lan = 192.168.1.1

[Tailscale tailnet]
auth-key = tskey-auth-abc_DEF-123
hostname = nas-1

[Rule]
DOMAIN-SUFFIX,ts.net,Tailscale
IP-CIDR,100.64.0.0/10,Tailscale,no-resolve
IP-CIDR6,fd7a:115c:a1e0::/48,Tailscale,no-resolve
FINAL,全球流量
`
	if string(client) != wantClient {
		t.Fatalf("client output:\n%s\nwant:\n%s", client, wantClient)
	}
}

func TestApplyTailscaleSurgeRequiresAnchors(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		role TailscaleRole
	}{
		{"no general", func(s string) string { return strings.Replace(s, "[General]", "[Generic]", 1) }, TailscaleServer},
		{"no rule", func(s string) string { return strings.Replace(s, "[Rule]", "[Rules]", 1) }, TailscaleServer},
		{"repeated rule", func(s string) string { return s + "[Rule]\n" }, TailscaleServer},
		{"host maps ts.net", func(s string) string { return strings.Replace(s, "router.lan", "*.ts.net", 1) }, TailscaleServer},
		{"no proxy", func(s string) string { return strings.Replace(s, "[Proxy]\n", "[Proxies]\n", 1) }, TailscaleClient},
		{"policy name taken", func(s string) string { return strings.Replace(s, "DIRECT = direct", "Tailscale = direct", 1) }, TailscaleClient},
		{"section taken", func(s string) string { return strings.Replace(s, "[Host]", "[Tailscale tailnet]", 1) }, TailscaleClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := TailscaleOptions{Role: tt.role}
			if tt.role == TailscaleClient {
				opts = testClientOpts
			}
			if out, err := ApplyTailscale(ProfileSurge, []byte(tt.edit(surgeFixture)), opts); err == nil {
				t.Fatalf("expected an error, got:\n%s", out)
			}
		})
	}
}

// Every format refuses a profile that already carries Tailscale routing, so a
// second application never duplicates rules.
func TestApplyTailscaleRefusesASecondApplication(t *testing.T) {
	fixtures := map[ProfileFormat]string{
		ProfileSingBox:   singBoxFixture,
		ProfileClashMeta: clashFixture,
		ProfileSurge:     surgeFixture,
	}
	for format, fixture := range fixtures {
		for _, opts := range []TailscaleOptions{{Role: TailscaleServer}, testClientOpts} {
			once, err := ApplyTailscale(format, []byte(fixture), opts)
			if err != nil {
				t.Fatalf("%s %s: %v", format, opts.Role, err)
			}
			if out, err := ApplyTailscale(format, once, opts); err == nil {
				t.Fatalf("%s %s: second application succeeded:\n%s", format, opts.Role, out)
			}
		}
	}
}
