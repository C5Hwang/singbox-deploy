package deploy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

var (
	tailscaleServerOpts = subscription.TailscaleOptions{Role: subscription.TailscaleServer}
	tailscaleClientOpts = subscription.TailscaleOptions{
		Role: subscription.TailscaleClient, Hostname: "laptop", AuthKey: "tskey-auth-kAbC123-XyZ_789",
	}
)

// tailscaleBaseProfiles renders the real profile templates with a couple of
// nodes, keyed by the profile format they are served under.
func tailscaleBaseProfiles(t *testing.T) map[subscription.ProfileFormat][]byte {
	t.Helper()
	outbounds := []map[string]any{
		{"type": "hysteria2", "tag": "🇺🇸 US-vps1-Hysteria2", "server": "us.example.com", "server_port": 9443, "password": "p&<>"},
		{"type": "anytls", "tag": "🇯🇵 JP-vps2-AnyTLS", "server": "jp.example.com", "server_port": 8443, "password": "pw"},
	}
	var out subscriptionOutputs
	if err := fillProfiles(&out, Config{Domain: "example.com", SubscribePort: 2096, Salt: "salt"}, outbounds); err != nil {
		t.Fatalf("fillProfiles: %v", err)
	}
	return map[subscription.ProfileFormat][]byte{
		subscription.ProfileSingBox:   []byte(out.SingBoxProfile),
		subscription.ProfileClashMeta: []byte(out.ClashProfile),
		subscription.ProfileSurge:     []byte(out.SurgeProfile),
	}
}

func applyTailscale(t *testing.T, format subscription.ProfileFormat, base []byte, opts subscription.TailscaleOptions) string {
	t.Helper()
	got, err := subscription.ApplyTailscale(format, base, opts)
	if err != nil {
		t.Fatalf("ApplyTailscale(%s, %s): %v", format, opts.Role, err)
	}
	return string(got)
}

// TestTailscaleProfilesWriteSamples writes base and transformed profiles to
// $TS_SAMPLES_DIR for validation against the real clients.
func TestTailscaleProfilesWriteSamples(t *testing.T) {
	dir := os.Getenv("TS_SAMPLES_DIR")
	if dir == "" {
		t.Skip("TS_SAMPLES_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := map[subscription.ProfileFormat]string{
		subscription.ProfileSingBox:   "json",
		subscription.ProfileClashMeta: "yaml",
		subscription.ProfileSurge:     "conf",
	}
	for format, base := range tailscaleBaseProfiles(t) {
		files := map[string][]byte{"base": base}
		for _, opts := range []subscription.TailscaleOptions{tailscaleServerOpts, tailscaleClientOpts} {
			files[string(opts.Role)] = []byte(applyTailscale(t, format, base, opts))
		}
		for name, body := range files {
			path := filepath.Join(dir, string(format)+"-"+name+"."+ext[format])
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTailscaleSingBoxServerProfile(t *testing.T) {
	base := tailscaleBaseProfiles(t)[subscription.ProfileSingBox]
	got := applyTailscale(t, subscription.ProfileSingBox, base, tailscaleServerOpts)
	profile := decodeSingBoxProfile(t, got)

	tun := profile.inbound(t, "tun")
	if !slices.Equal(tun.RouteExcludeAddress, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}) {
		t.Fatalf("tun route_exclude_address = %v", tun.RouteExcludeAddress)
	}
	first := profile.DNS.Rules[0]
	if !slices.Equal(first.DomainSuffix, []string{"ts.net"}) || first.Server != "local" || first.Action != "route" {
		t.Fatalf("first dns rule = %+v, want ts.net routed to local", first)
	}
	profile.assertTailnetRouteRule(t, "DIRECT")
	if len(profile.Endpoints) != 0 {
		t.Fatalf("server role must not embed tailscale, got endpoints %+v", profile.Endpoints)
	}
	if strings.Contains(got, `"tailscale"`) {
		t.Fatalf("server role must not add a tailscale endpoint or dns server:\n%s", got)
	}
}

func TestTailscaleSingBoxClientProfile(t *testing.T) {
	base := tailscaleBaseProfiles(t)[subscription.ProfileSingBox]
	got := applyTailscale(t, subscription.ProfileSingBox, base, tailscaleClientOpts)
	profile := decodeSingBoxProfile(t, got)

	if tun := profile.inbound(t, "tun"); len(tun.RouteExcludeAddress) != 0 {
		t.Fatalf("client role must keep tailnet traffic on the tun, got route_exclude_address %v", tun.RouteExcludeAddress)
	}
	if len(profile.Endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want one tailscale endpoint", profile.Endpoints)
	}
	ep := profile.Endpoints[0]
	if ep.Type != "tailscale" || ep.Tag != "Tailscale" || ep.Detour != "" ||
		ep.AuthKey != tailscaleClientOpts.AuthKey || ep.Hostname != "laptop" || ep.StateDirectory != "tailscale" {
		t.Fatalf("endpoint = %+v", ep)
	}
	server := profile.DNS.Servers[len(profile.DNS.Servers)-1]
	if server.Type != "tailscale" || server.Tag != "tailscale" || server.Endpoint != "Tailscale" ||
		server.AcceptDefaultResolvers == nil || *server.AcceptDefaultResolvers {
		t.Fatalf("last dns server = %+v, want MagicDNS-only tailscale server", server)
	}
	first := profile.DNS.Rules[0]
	if !slices.Equal(first.DomainSuffix, []string{"ts.net"}) || first.Server != "tailscale" {
		t.Fatalf("first dns rule = %+v, want ts.net routed to tailscale", first)
	}
	profile.assertTailnetRouteRule(t, "Tailscale")

	// Endpoints follow the documented top-level order: ahead of inbounds.
	if e, i := strings.Index(got, `"endpoints"`), strings.Index(got, `"inbounds"`); e < 0 || e > i {
		t.Fatalf("endpoints should precede inbounds:\n%s", got)
	}
}

// TestTailscaleSingBoxPreservesTheBaseProfile checks that apart from the
// injected entries the profile round-trips unchanged and stays readable.
func TestTailscaleSingBoxPreservesTheBaseProfile(t *testing.T) {
	base := tailscaleBaseProfiles(t)[subscription.ProfileSingBox]
	for _, opts := range []subscription.TailscaleOptions{tailscaleServerOpts, tailscaleClientOpts} {
		got := applyTailscale(t, subscription.ProfileSingBox, base, opts)
		for _, want := range []string{`"🚀 手动切换"`, `"p&<>"`, `"全球流量"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: output lost or escaped %s", opts.Role, want)
			}
		}

		var baseTree, gotTree map[string]any
		if err := json.Unmarshal(base, &baseTree); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(got), &gotTree); err != nil {
			t.Fatalf("%s: output is not valid JSON: %v", opts.Role, err)
		}
		dropInjected(gotTree)
		dropInjected(baseTree)
		baseJSON, _ := json.Marshal(baseTree)
		gotJSON, _ := json.Marshal(gotTree)
		if !bytes.Equal(baseJSON, gotJSON) {
			t.Fatalf("%s: profile changed beyond the tailscale entries:\nbase %s\n got %s", opts.Role, baseJSON, gotJSON)
		}

		// Key order survives: the top level still reads as in the template.
		var keys []string
		dec := json.NewDecoder(strings.NewReader(got))
		dec.Token()
		for dec.More() {
			tok, _ := dec.Token()
			keys = append(keys, tok.(string))
			var skip json.RawMessage
			dec.Decode(&skip)
		}
		want := []string{"log", "dns", "inbounds", "outbounds", "route"}
		if opts.Role == subscription.TailscaleClient {
			want = []string{"log", "dns", "endpoints", "inbounds", "outbounds", "route"}
		}
		if !slices.Equal(keys, want) {
			t.Fatalf("%s: top-level keys = %v, want %v", opts.Role, keys, want)
		}
	}
}

// dropInjected removes everything ApplyTailscale adds so the rest of the tree
// can be compared with the base profile.
func dropInjected(tree map[string]any) {
	delete(tree, "endpoints")
	isTailnet := func(v any) bool {
		m, _ := v.(map[string]any)
		suffix, _ := m["domain_suffix"].([]any)
		return m["type"] == "tailscale" || (len(suffix) == 1 && suffix[0] == "ts.net")
	}
	dns := tree["dns"].(map[string]any)
	dns["servers"] = slices.DeleteFunc(dns["servers"].([]any), isTailnet)
	dns["rules"] = slices.DeleteFunc(dns["rules"].([]any), isTailnet)
	route := tree["route"].(map[string]any)
	route["rules"] = slices.DeleteFunc(route["rules"].([]any), isTailnet)
	for _, in := range tree["inbounds"].([]any) {
		delete(in.(map[string]any), "route_exclude_address")
	}
}

type singBoxTailscaleProfile struct {
	Endpoints []struct {
		Type           string `json:"type"`
		Tag            string `json:"tag"`
		Detour         string `json:"detour"`
		StateDirectory string `json:"state_directory"`
		AuthKey        string `json:"auth_key"`
		Hostname       string `json:"hostname"`
	} `json:"endpoints"`
	Inbounds []singBoxTailscaleInbound `json:"inbounds"`
	DNS      struct {
		Servers []struct {
			Type                   string `json:"type"`
			Tag                    string `json:"tag"`
			Endpoint               string `json:"endpoint"`
			AcceptDefaultResolvers *bool  `json:"accept_default_resolvers"`
		} `json:"servers"`
		Rules []singBoxTailscaleDNSRule `json:"rules"`
	} `json:"dns"`
	Route struct {
		Rules []singBoxTailscaleRouteRule `json:"rules"`
	} `json:"route"`
}

type singBoxTailscaleDNSRule struct {
	DomainSuffix []string `json:"domain_suffix"`
	QueryType    []string `json:"query_type"`
	Action       string   `json:"action"`
	Server       string   `json:"server"`
}

type singBoxTailscaleRouteRule struct {
	Protocol     string   `json:"protocol"`
	Action       string   `json:"action"`
	DomainSuffix []string `json:"domain_suffix"`
	IPCIDR       []string `json:"ip_cidr"`
	Outbound     string   `json:"outbound"`
}

type singBoxTailscaleInbound struct {
	Type                string   `json:"type"`
	RouteExcludeAddress []string `json:"route_exclude_address"`
}

func decodeSingBoxProfile(t *testing.T, body string) singBoxTailscaleProfile {
	t.Helper()
	var p singBoxTailscaleProfile
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("sing-box profile is not valid JSON: %v\n%s", err, body)
	}
	return p
}

func (p singBoxTailscaleProfile) inbound(t *testing.T, typ string) singBoxTailscaleInbound {
	t.Helper()
	for _, in := range p.Inbounds {
		if in.Type == typ {
			return in
		}
	}
	t.Fatalf("no %s inbound", typ)
	return singBoxTailscaleInbound{}
}

// assertTailnetRouteRule checks the tailnet route rule sits right after
// hijack-dns, ahead of the Private rule set that also covers 100.64.0.0/10.
func (p singBoxTailscaleProfile) assertTailnetRouteRule(t *testing.T, outbound string) {
	t.Helper()
	rules := p.Route.Rules
	hijack := slices.IndexFunc(rules, func(r singBoxTailscaleRouteRule) bool {
		return r.Protocol == "dns" && r.Action == "hijack-dns"
	})
	if hijack < 0 || hijack+1 >= len(rules) {
		t.Fatalf("no rule after hijack-dns: %+v", rules)
	}
	r := rules[hijack+1]
	if !slices.Equal(r.DomainSuffix, []string{"ts.net"}) ||
		!slices.Equal(r.IPCIDR, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}) || r.Outbound != outbound {
		t.Fatalf("rule after hijack-dns = %+v, want tailnet to %s", r, outbound)
	}
	// The ts.net DNS rule must win over the fakeip A/AAAA rule.
	fakeip := slices.IndexFunc(p.DNS.Rules, func(r singBoxTailscaleDNSRule) bool {
		return r.Server == "fakeip"
	})
	if fakeip <= 0 {
		t.Fatalf("fakeip dns rule at %d, want after the ts.net rule", fakeip)
	}
}

func TestTailscaleClashProfiles(t *testing.T) {
	base := tailscaleBaseProfiles(t)[subscription.ProfileClashMeta]

	server := applyTailscale(t, subscription.ProfileClashMeta, base, tailscaleServerOpts)
	assertContainsInOrder(t, server,
		"tun:\n  route-exclude-address:\n    - 100.64.0.0/10\n    - fd7a:115c:a1e0::/48\n\ndns:\n",
		`    - "localhost.ptlogin2.qq.com"`+"\n"+`    - "+.ts.net"`+"\n",
		"  nameserver-policy:\n    \"+.ts.net\": \"system\"\n\nproxy-providers:",
		"rules:\n  - DOMAIN-SUFFIX,ts.net,DIRECT\n  - IP-CIDR,100.64.0.0/10,DIRECT,no-resolve\n"+
			"  - IP-CIDR6,fd7a:115c:a1e0::/48,DIRECT,no-resolve\n  - RULE-SET,Private,DIRECT\n",
	)
	for _, absent := range []string{"type: tailscale", "Tailscale", "tailscale.com"} {
		if strings.Contains(server, absent) {
			t.Fatalf("server profile should not contain %q", absent)
		}
	}

	client := applyTailscale(t, subscription.ProfileClashMeta, base, tailscaleClientOpts)
	assertContainsInOrder(t, client,
		`    - "+.ts.net"`+"\n",
		"  nameserver-policy:\n    \"+.ts.net\": \"ts://Tailscale\"\n",
		"proxies:\n  - name: Tailscale\n    type: tailscale\n    hostname: \"laptop\"\n"+
			"    auth-key: \"tskey-auth-kAbC123-XyZ_789\"\n    state-dir: ./tailscale\n    udp: true\n\nproxy-groups:\n",
		"rules:\n  - DOMAIN-SUFFIX,ts.net,Tailscale\n  - IP-CIDR,100.64.0.0/10,Tailscale,no-resolve\n"+
			"  - IP-CIDR6,fd7a:115c:a1e0::/48,Tailscale,no-resolve\n  - RULE-SET,Private,DIRECT\n",
	)
	if strings.Contains(client, "route-exclude-address") || strings.Contains(client, "dialer-proxy") ||
		strings.Contains(client, "tailscale.com") {
		t.Fatal("client profile must keep tailnet traffic on the tun and everything else on the normal rules")
	}
}

func TestTailscaleSurgeProfiles(t *testing.T) {
	base := tailscaleBaseProfiles(t)[subscription.ProfileSurge]
	for _, absent := range []string{"100.64.0.0/10", "ts.net", "crashlytics"} {
		if bytes.Contains(base, []byte(absent)) {
			t.Fatalf("base profile should not contain %q", absent)
		}
	}

	server := applyTailscale(t, subscription.ProfileSurge, base, tailscaleServerOpts)
	assertContainsInOrder(t, server,
		"skip-proxy = 127.0.0.1, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, localhost, *.local, 100.64.0.0/10, *.ts.net\n",
		"always-real-ip = *.ts.net\ntun-excluded-routes = 100.64.0.0/10, fd7a:115c:a1e0::/48\n\n[Proxy]\n",
		"[Host]\n*.ts.net = server:system\n\n[Rule]\n"+
			"DOMAIN-SUFFIX,ts.net,DIRECT\nIP-CIDR,100.64.0.0/10,DIRECT,no-resolve\nIP-CIDR6,fd7a:115c:a1e0::/48,DIRECT,no-resolve\nRULE-SET,",
	)
	if strings.Contains(server, "tailscale") {
		t.Fatal("server profile must not embed a tailscale policy")
	}

	client := applyTailscale(t, subscription.ProfileSurge, base, tailscaleClientOpts)
	assertContainsInOrder(t, client,
		"skip-proxy = 127.0.0.1, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, localhost, *.local\n",
		"always-real-ip = *.ts.net\n\n[Proxy]\nDIRECT = direct\nTailscale = tailscale, section-name=tailnet\n\n[Proxy Group]\n",
		"[Tailscale tailnet]\nauth-key = tskey-auth-kAbC123-XyZ_789\nhostname = laptop\n\n[Rule]\n"+
			"DOMAIN-SUFFIX,ts.net,Tailscale\nIP-CIDR,100.64.0.0/10,Tailscale,no-resolve\nIP-CIDR6,fd7a:115c:a1e0::/48,Tailscale,no-resolve\nRULE-SET,",
	)
	for _, absent := range []string{"tun-excluded-routes", "[Host]", "underlying-proxy", "tailscale.com"} {
		if strings.Contains(client, absent) {
			t.Fatalf("client profile should not contain %q", absent)
		}
	}
}

// TestTailscaleProfilesRejectSecondApplication guards against stacking a
// client transform on an already transformed profile.
func TestTailscaleProfilesRejectSecondApplication(t *testing.T) {
	for format, base := range tailscaleBaseProfiles(t) {
		once := applyTailscale(t, format, base, tailscaleClientOpts)
		if _, err := subscription.ApplyTailscale(format, []byte(once), tailscaleClientOpts); err == nil {
			t.Errorf("%s: applying the client role twice succeeded", format)
		}
	}
}

// assertContainsInOrder checks each snippet appears after the previous one.
func assertContainsInOrder(t *testing.T, body string, snippets ...string) {
	t.Helper()
	rest := body
	for _, s := range snippets {
		i := strings.Index(rest, s)
		if i < 0 {
			t.Fatalf("missing (or out of order) snippet:\n%s\n--- in ---\n%s", s, body)
		}
		rest = rest[i+len(s):]
	}
}
