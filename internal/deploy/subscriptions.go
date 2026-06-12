package deploy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// node is one generated server node across all output formats.
type node struct {
	Name            string
	DefaultLink     string
	ClashYAML       string         // block-style list item, 2-space indent
	SingBoxOutbound map[string]any // sing-box client outbound object
	SurgeLine       string         // Surge proxy line: "name = type, server, port, ..."
}

// buildNodes generates a node per enabled protocol.
func (c Config) buildNodes() []node {
	addr := c.Domain
	name := func(label string) string {
		return subscription.AddNodePrefixFlag(c.DisplayName + "-" + label)
	}
	var nodes []node
	for _, p := range c.EnabledProtocols() {
		switch p {
		case config.ProtocolRealityVision:
			n := name("VLESS-Reality-Vision")
			nodes = append(nodes, node{
				Name: n,
				DefaultLink: realityLink("vless", c.Creds.RealityVisionUUID, addr, c.Ports.RealityVision, n, url.Values{
					"encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "security": {"reality"},
					"sni": {c.RealityServerName}, "fp": {"chrome"}, "pbk": {c.Creds.RealityPublicKey},
					"sid": {c.Creds.RealityShortID}, "type": {"tcp"},
				}),
				ClashYAML: clashBlock(map[string]any{
					"name": n, "type": "vless", "server": addr, "port": c.Ports.RealityVision,
					"uuid": c.Creds.RealityVisionUUID, "network": "tcp", "tls": true, "udp": true,
					"flow": "xtls-rprx-vision", "servername": c.RealityServerName, "client-fingerprint": "chrome",
					"reality-opts": map[string]any{"public-key": c.Creds.RealityPublicKey, "short-id": c.Creds.RealityShortID},
				}),
				SingBoxOutbound: map[string]any{
					"type": "vless", "tag": n, "server": addr, "server_port": c.Ports.RealityVision,
					"uuid": c.Creds.RealityVisionUUID, "flow": "xtls-rprx-vision",
					"tls": realityClientTLS(c.RealityServerName, c.Creds.RealityPublicKey, c.Creds.RealityShortID),
				},
			})
		case config.ProtocolRealityGRPC:
			n := name("VLESS-Reality-gRPC")
			nodes = append(nodes, node{
				Name: n,
				DefaultLink: realityLink("vless", c.Creds.RealityGRPCUUID, addr, c.Ports.RealityGRPC, n, url.Values{
					"encryption": {"none"}, "security": {"reality"}, "sni": {c.RealityServerName},
					"fp": {"chrome"}, "pbk": {c.Creds.RealityPublicKey}, "sid": {c.Creds.RealityShortID},
					"type": {"grpc"}, "serviceName": {"grpc"},
				}),
				ClashYAML: clashBlock(map[string]any{
					"name": n, "type": "vless", "server": addr, "port": c.Ports.RealityGRPC,
					"uuid": c.Creds.RealityGRPCUUID, "network": "grpc", "tls": true, "udp": true,
					"servername": c.RealityServerName, "client-fingerprint": "chrome",
					"grpc-opts":    map[string]any{"grpc-service-name": "grpc"},
					"reality-opts": map[string]any{"public-key": c.Creds.RealityPublicKey, "short-id": c.Creds.RealityShortID},
				}),
				SingBoxOutbound: map[string]any{
					"type": "vless", "tag": n, "server": addr, "server_port": c.Ports.RealityGRPC,
					"uuid":      c.Creds.RealityGRPCUUID,
					"tls":       realityClientTLS(c.RealityServerName, c.Creds.RealityPublicKey, c.Creds.RealityShortID),
					"transport": map[string]any{"type": "grpc", "service_name": "grpc"},
				},
			})
		case config.ProtocolHysteria2:
			n := name("Hysteria2")
			nodes = append(nodes, node{
				Name: n,
				DefaultLink: scheme("hysteria2", c.Creds.HysteriaPassword, "", addr, c.Ports.Hysteria2, n, url.Values{
					"sni": {c.Domain}, "alpn": {"h3"},
				}),
				ClashYAML: clashBlock(map[string]any{
					"name": n, "type": "hysteria2", "server": addr, "port": c.Ports.Hysteria2,
					"password": c.Creds.HysteriaPassword,
					"sni":      c.Domain, "alpn": []any{"h3"},
				}),
				SingBoxOutbound: map[string]any{
					"type": "hysteria2", "tag": n, "server": addr, "server_port": c.Ports.Hysteria2,
					"password": c.Creds.HysteriaPassword,
					"tls":      map[string]any{"enabled": true, "server_name": c.Domain, "alpn": []any{"h3"}},
				},
				SurgeLine: surgeLine(n, "hysteria2", addr, c.Ports.Hysteria2,
					"password="+c.Creds.HysteriaPassword,
					"sni="+c.Domain, "download-bandwidth=200"),
			})
		case config.ProtocolTUIC:
			n := name("TUIC")
			nodes = append(nodes, node{
				Name: n,
				DefaultLink: scheme("tuic", c.Creds.TUICUUID, c.Creds.TUICPassword, addr, c.Ports.TUIC, n, url.Values{
					"congestion_control": {"bbr"}, "alpn": {"h3"}, "sni": {c.Domain},
				}),
				ClashYAML: clashBlock(map[string]any{
					"name": n, "type": "tuic", "server": addr, "port": c.Ports.TUIC,
					"uuid": c.Creds.TUICUUID, "password": c.Creds.TUICPassword,
					"alpn": []any{"h3"}, "congestion-controller": "bbr", "sni": c.Domain,
				}),
				SingBoxOutbound: map[string]any{
					"type": "tuic", "tag": n, "server": addr, "server_port": c.Ports.TUIC,
					"uuid": c.Creds.TUICUUID, "password": c.Creds.TUICPassword, "congestion_control": "bbr",
					"tls": map[string]any{"enabled": true, "server_name": c.Domain, "alpn": []any{"h3"}},
				},
				SurgeLine: surgeLine(n, "tuic-v5", addr, c.Ports.TUIC,
					"uuid="+c.Creds.TUICUUID,
					"password="+c.Creds.TUICPassword,
					"alpn=h3", "sni="+c.Domain),
			})
		case config.ProtocolAnyTLS:
			n := name("AnyTLS")
			nodes = append(nodes, node{
				Name: n,
				DefaultLink: scheme("anytls", c.Creds.AnyTLSPassword, "", addr, c.Ports.AnyTLS, n, url.Values{
					"sni": {c.Domain},
				}),
				ClashYAML: clashBlock(map[string]any{
					"name": n, "type": "anytls", "server": addr, "port": c.Ports.AnyTLS,
					"password": c.Creds.AnyTLSPassword, "sni": c.Domain,
				}),
				SingBoxOutbound: map[string]any{
					"type": "anytls", "tag": n, "server": addr, "server_port": c.Ports.AnyTLS,
					"password": c.Creds.AnyTLSPassword,
					"tls":      map[string]any{"enabled": true, "server_name": c.Domain},
				},
				SurgeLine: surgeLine(n, "anytls", addr, c.Ports.AnyTLS,
					"password="+c.Creds.AnyTLSPassword,
					"sni="+c.Domain),
			})
		}
	}
	return nodes
}

func realityClientTLS(sni, pbk, sid string) map[string]any {
	return map[string]any{
		"enabled": true, "server_name": sni,
		"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
		"reality": map[string]any{"enabled": true, "public_key": pbk, "short_id": sid},
	}
}

// realityLink builds a vless:// reality link.
func realityLink(scheme, uuid, host string, port int, name string, q url.Values) string {
	return fmt.Sprintf("%s://%s@%s:%d?%s#%s", scheme, uuid, host, port, q.Encode(), url.PathEscape(name))
}

// scheme builds a generic user[:pass]@host:port URI with query and fragment.
func scheme(proto, user, pass, host string, port int, name string, q url.Values) string {
	auth := user
	if pass != "" {
		auth = user + ":" + pass
	}
	return fmt.Sprintf("%s://%s@%s:%d?%s#%s", proto, auth, host, port, q.Encode(), url.PathEscape(name))
}

// clashBlock renders a Clash proxy as a 2-space-indented block-style list item.
// Keys are emitted in a stable order for deterministic output.
func clashBlock(m map[string]any) string {
	order := []string{
		"name", "type", "server", "port", "uuid", "password", "network", "tls", "udp",
		"flow", "servername", "sni", "alpn", "congestion-controller", "client-fingerprint",
		"grpc-opts", "reality-opts",
	}
	var b strings.Builder
	first := true
	for _, k := range order {
		v, ok := m[k]
		if !ok {
			continue
		}
		if first {
			b.WriteString("  - ")
			first = false
		} else {
			b.WriteString("    ")
		}
		writeYAMLField(&b, 4, k, v)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeYAMLField(b *strings.Builder, indent int, key string, v any) {
	b.WriteString(key)
	switch t := v.(type) {
	case []any:
		b.WriteString(":\n")
		for _, e := range t {
			b.WriteString(strings.Repeat(" ", indent+2))
			b.WriteString("- ")
			b.WriteString(yamlScalar(e))
			b.WriteString("\n")
		}
	case map[string]any:
		b.WriteString(":\n")
		for _, nestedKey := range sortedKeys(t) {
			b.WriteString(strings.Repeat(" ", indent+2))
			writeYAMLField(b, indent+2, nestedKey, t[nestedKey])
		}
	default:
		b.WriteString(": ")
		b.WriteString(yamlScalar(v))
		b.WriteString("\n")
	}
}

func yamlScalar(v any) string {
	switch t := v.(type) {
	case string:
		if yamlNeedsQuotes(t) {
			return strconv.Quote(t)
		}
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func yamlNeedsQuotes(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return true
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off":
		return true
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '.', '_', '/', '-':
			continue
		default:
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// small, fixed maps; insertion via simple sort
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// countryDef maps a country code to its display info and filter regex.
type countryDef struct {
	Code   string
	Flag   string
	Name   string
	Filter string
}

// knownCountries lists recognized countries in display order (Asia first, then West).
var knownCountries = []countryDef{
	{Code: "HK", Flag: "🇭🇰", Name: "香港节点", Filter: `(🇭🇰)|(港)|(Hong)|(HK)`},
	{Code: "TW", Flag: "🇼🇸", Name: "台湾节点", Filter: `(🇹🇼)|(🇼🇸)|(台)|(Tai)|(TW)`},
	{Code: "JP", Flag: "🇯🇵", Name: "日本节点", Filter: `(🇯🇵)|(日)|(Japan)|(JP)`},
	{Code: "KR", Flag: "🇰🇷", Name: "韩国节点", Filter: `(🇰🇷)|(韩)|(Korea)|(KR)`},
	{Code: "SG", Flag: "🇸🇬", Name: "新加坡节点", Filter: `(🇸🇬)|(新)|(Singapore)|(SG)`},
	{Code: "US", Flag: "🇺🇸", Name: "美国节点", Filter: `(🇺🇸)|(🇺🇲)|(美)|(States)|(US)`},
	{Code: "CA", Flag: "🇨🇦", Name: "加拿大节点", Filter: `(🇨🇦)|(加)|(Canada)|(CA)`},
	{Code: "UK", Flag: "🇬🇧", Name: "英国节点", Filter: `(🇬🇧)|(英)|(United Kingdom)|(UK)`},
	{Code: "DE", Flag: "🇩🇪", Name: "德国节点", Filter: `(🇩🇪)|(德)|(Germany)|(DE)`},
	{Code: "FR", Flag: "🇫🇷", Name: "法国节点", Filter: `(🇫🇷)|(法)|(France)|(FR)`},
	{Code: "NL", Flag: "🇳🇱", Name: "荷兰节点", Filter: `(🇳🇱)|(荷)|(Netherlands)|(NL)`},
	{Code: "AU", Flag: "🇦🇺", Name: "澳大利亚节点", Filter: `(🇦🇺)|(澳)|(Australia)|(AU)`},
}

// detectedCountry is a country group detected from node tags.
type detectedCountry struct {
	Tag      string
	Filter   string
	TagsJSON string
}

func detectCountries(tags []string) []detectedCountry {
	var result []detectedCountry
	for _, def := range knownCountries {
		re := regexp.MustCompile(def.Filter)
		var matched []string
		for _, tag := range tags {
			if re.MatchString(tag) {
				matched = append(matched, tag)
			}
		}
		if len(matched) > 0 {
			result = append(result, detectedCountry{
				Tag:      def.Flag + " " + def.Name,
				Filter:   def.Filter,
				TagsJSON: marshalTags(matched),
			})
		}
	}
	return result
}

// subscriptionOutputs holds the rendered bodies for each subscription endpoint.
type subscriptionOutputs struct {
	DefaultBase64    string
	ClashFragment    string
	ClashProfile     string
	SingBoxOutbounds string
	SingBoxProfile   string
	SurgeFragment    string
	SurgeProfile     string
}

// buildSubscriptions renders every subscription output for the install config.
func (c Config) buildSubscriptions() (subscriptionOutputs, error) {
	nodes := c.buildNodes()

	var links []subscription.Node
	var clashItems []string
	var surgeItems []string
	var outbounds []map[string]any
	for _, n := range nodes {
		links = append(links, subscription.Node{Name: n.Name, Protocol: protoOf(n.SingBoxOutbound), Link: n.DefaultLink})
		clashItems = append(clashItems, n.ClashYAML)
		if n.SurgeLine != "" {
			surgeItems = append(surgeItems, n.SurgeLine)
		}
		outbounds = append(outbounds, n.SingBoxOutbound)
	}

	out := subscriptionOutputs{
		DefaultBase64: subscription.EncodeBase64(subscription.GenerateDefault(links)),
		ClashFragment: "proxies:\n" + strings.Join(clashItems, "\n") + "\n",
		SurgeFragment: strings.Join(surgeItems, "\n") + "\n",
	}

	clashProviderURL := fmt.Sprintf("https://%s:%d/s/clashMeta/%s", c.Domain, c.SubscribePort, subscriptionToken(c.Salt))
	surgeProviderURL := fmt.Sprintf("https://%s:%d/s/surge/%s", c.Domain, c.SubscribePort, subscriptionToken(c.Salt))
	if err := fillProfiles(&out, outbounds, clashProviderURL, surgeProviderURL); err != nil {
		return subscriptionOutputs{}, err
	}
	return out, nil
}

func fillProfiles(out *subscriptionOutputs, outbounds []map[string]any, clashProviderURL, surgeProviderURL string) error {
	obJSON, err := json.MarshalIndent(outbounds, "", "  ")
	if err != nil {
		return err
	}
	out.SingBoxOutbounds = string(obJSON)
	tagsList := outboundTags(outbounds)

	tagsJSON, err := json.Marshal(tagsList)
	if err != nil {
		return err
	}
	inner := strings.TrimSpace(string(obJSON))
	inner = strings.TrimPrefix(inner, "[")
	inner = strings.TrimSuffix(inner, "]")
	defaultTag := ""
	if len(tagsList) > 0 {
		defaultTag = tagsList[0]
	}

	countries := detectCountries(tagsList)

	singboxProfile, err := templatefs.Render("subscription/sing-box.json.tmpl", map[string]any{
		"ProxyTagsJSON":   string(tagsJSON),
		"DefaultProxyTag": defaultTag,
		"OutboundsJSON":   strings.TrimSpace(inner),
		"Countries":       countries,
	})
	if err != nil {
		return err
	}
	out.SingBoxProfile = singboxProfile

	clashProfile, err := templatefs.Render("subscription/clash-meta.yaml.tmpl", map[string]any{
		"ClashProviderURL": clashProviderURL,
		"Countries":        countries,
	})
	if err != nil {
		return err
	}
	out.ClashProfile = clashProfile

	surgeProfile, err := templatefs.Render("subscription/surge.conf.tmpl", map[string]any{
		"SurgeProviderURL": surgeProviderURL,
		"Countries":        countries,
	})
	if err != nil {
		return err
	}
	out.SurgeProfile = surgeProfile
	return nil
}

func marshalTags(tags []string) string {
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func outboundTags(outbounds []map[string]any) []string {
	tags := make([]string, 0, len(outbounds))
	for _, ob := range outbounds {
		if tag, ok := ob["tag"].(string); ok && tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// WriteSubscriptions renders and writes all subscription output files.
func WriteSubscriptions(layout paths.Layout, cfg Config) error {
	out, err := cfg.buildSubscriptions()
	if err != nil {
		return err
	}
	return writeSubscriptionOutputs(layout, cfg, out)
}

// surgeLine builds a Surge proxy line: "name = type, server, port, key=val, ..."
func surgeLine(name, protoType, server string, port int, params ...string) string {
	line := fmt.Sprintf("%s = %s, %s, %d", name, protoType, server, port)
	for _, p := range params {
		line += ", " + p
	}
	return line
}

// protoOf reports the subscription protocol key for a sing-box outbound.
func protoOf(ob map[string]any) string {
	if t, ok := ob["type"].(string); ok {
		return t
	}
	return ""
}
