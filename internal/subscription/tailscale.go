package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// TailscaleRole selects how a full profile coexists with Tailscale.
type TailscaleRole string

const (
	// TailscaleServer keeps the tailnet out of the proxy core: the device also
	// runs the official Tailscale app, which owns 100.64.0.0/10 and MagicDNS.
	TailscaleServer TailscaleRole = "server"
	// TailscaleClient embeds Tailscale in the proxy core itself, logging in
	// with an auth key under the given hostname.
	TailscaleClient TailscaleRole = "client"
)

// TailscaleOptions selects a Tailscale mode. Hostname and the auth key are set
// only for TailscaleClient. A parsed query carries the key only as
// EncryptedAuthKey; the caller opens it with DecryptAuthKey and sets the
// plaintext AuthKey that ApplyTailscale embeds.
type TailscaleOptions struct {
	Role             TailscaleRole
	Hostname         string
	AuthKey          string
	EncryptedAuthKey string
}

// ProfileFormat names a full-profile subscription directory that supports
// Tailscale injection.
type ProfileFormat string

const (
	ProfileSingBox   ProfileFormat = "singboxProfiles"
	ProfileClashMeta ProfileFormat = "clashMetaProfiles"
	ProfileSurge     ProfileFormat = "surgeProfiles"
)

const (
	tailnetIPv4    = "100.64.0.0/10"
	tailnetIPv6    = "fd7a:115c:a1e0::/48"
	magicDNSSuffix = "ts.net"
	// tailscaleTag names the embedded Tailscale outbound in every format.
	tailscaleTag = "Tailscale"
	// tailscaleDNSTag names sing-box's Tailscale DNS server. DNS server tags
	// and outbound/endpoint tags live in separate namespaces, but a distinct
	// spelling keeps the profile unambiguous to read.
	tailscaleDNSTag = "tailscale"
	// surgeTailscaleSection is the section-name of Surge's Tailscale policy.
	surgeTailscaleSection = "tailnet"
)

var (
	tailscaleHostnameRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	tailscaleAuthKeyRE  = regexp.MustCompile(`^tskey-[A-Za-z0-9_-]{1,200}$`)
)

// ParseTailscaleQuery reads the Tailscale mode from a subscription request's
// query. It returns ok=false with a nil error when the query has no
// "tailscale" key, meaning the base profile is served unchanged. Keys other
// than tailscale, hostname and authkey are ignored. A client's authkey must be
// the encrypted value (see EncryptAuthKey); only its shape is checked here, and
// a plaintext key is refused outright.
func ParseTailscaleQuery(q url.Values) (opts TailscaleOptions, ok bool, err error) {
	if _, present := q["tailscale"]; !present {
		return TailscaleOptions{}, false, nil
	}
	for _, key := range []string{"tailscale", "hostname", "authkey"} {
		if len(q[key]) > 1 {
			return TailscaleOptions{}, false, fmt.Errorf("query key %q is repeated", key)
		}
	}
	_, hasHostname := q["hostname"]
	_, hasAuthKey := q["authkey"]
	switch role := TailscaleRole(q.Get("tailscale")); role {
	case TailscaleServer:
		if hasHostname || hasAuthKey {
			return TailscaleOptions{}, false, errors.New("tailscale=server takes no hostname or authkey")
		}
		opts = TailscaleOptions{Role: role}
	case TailscaleClient:
		opts = TailscaleOptions{Role: role, Hostname: q.Get("hostname"), EncryptedAuthKey: q.Get("authkey")}
		if !tailscaleHostnameRE.MatchString(opts.Hostname) {
			return TailscaleOptions{}, false, fmt.Errorf("invalid tailscale hostname %q", opts.Hostname)
		}
		if !hasAuthKey {
			return TailscaleOptions{}, false, errors.New("tailscale=client requires an authkey")
		}
		if !encryptedAuthKeyRE.MatchString(opts.EncryptedAuthKey) {
			return TailscaleOptions{}, false, errAuthKeyNotEncrypted
		}
	default:
		return TailscaleOptions{}, false, fmt.Errorf("unknown tailscale role %q", role)
	}
	return opts, true, nil
}

// validate checks the role and, for a client, that hostname and plaintext
// auth key are safe to paste unquoted into every profile format.
func (o TailscaleOptions) validate() error {
	switch o.Role {
	case TailscaleServer:
		if o.Hostname != "" || o.AuthKey != "" || o.EncryptedAuthKey != "" {
			return errors.New("tailscale=server takes no hostname or authkey")
		}
	case TailscaleClient:
		if !tailscaleHostnameRE.MatchString(o.Hostname) {
			return fmt.Errorf("invalid tailscale hostname %q", o.Hostname)
		}
		if !tailscaleAuthKeyRE.MatchString(o.AuthKey) {
			return errInvalidAuthKey
		}
	default:
		return fmt.Errorf("unknown tailscale role %q", o.Role)
	}
	return nil
}

// ProfileFormatForDir maps a /s/<dir> directory to its profile format. Node
// fragments (default, clashMeta, surge) and unknown names report ok=false.
func ProfileFormatForDir(dir string) (ProfileFormat, bool) {
	switch f := ProfileFormat(dir); f {
	case ProfileSingBox, ProfileClashMeta, ProfileSurge:
		return f, true
	}
	return "", false
}

// ApplyTailscale injects the role's Tailscale configuration into a rendered
// base profile. It either returns a fully transformed profile or an error;
// a profile missing an expected anchor is never half transformed.
func ApplyTailscale(format ProfileFormat, body []byte, opts TailscaleOptions) ([]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	switch format {
	case ProfileSingBox:
		return applySingBoxTailscale(body, opts)
	case ProfileClashMeta:
		return applyClashTailscale(body, opts)
	case ProfileSurge:
		return applySurgeTailscale(body, opts)
	}
	return nil, fmt.Errorf("unsupported profile format %q", format)
}

// tailnetRules lists the classical rules (shared by mihomo and Surge) that
// send MagicDNS names and tailnet addresses to target. Everything else,
// Tailscale's own control plane included, stays on the profile's normal rules.
func tailnetRules(opts TailscaleOptions) []string {
	target := "DIRECT"
	if opts.Role == TailscaleClient {
		target = tailscaleTag
	}
	return []string{
		"DOMAIN-SUFFIX," + magicDNSSuffix + "," + target,
		"IP-CIDR," + tailnetIPv4 + "," + target + ",no-resolve",
		"IP-CIDR6," + tailnetIPv6 + "," + target + ",no-resolve",
	}
}

func insertLines(lines []string, at int, added ...string) []string {
	return slices.Insert(slices.Clone(lines), at, added...)
}

// --- sing-box ---

// applySingBoxTailscale edits the sing-box profile as an order-preserving JSON
// tree so the result stays diffable against the base profile.
//
// Field references (sing-box >= 1.12): tun route_exclude_address
// (configuration/inbound/tun), tailscale endpoint (configuration/endpoint/
// tailscale), whose control-plane connection dials directly without a detour,
// tailscale DNS server with endpoint/accept_default_resolvers
// (configuration/dns/server/tailscale).
func applySingBoxTailscale(body []byte, opts TailscaleOptions) ([]byte, error) {
	root, err := decodeOrderedJSON(body)
	if err != nil {
		return nil, fmt.Errorf("parse sing-box profile: %w", err)
	}
	if root.kind != jsonObject {
		return nil, errors.New("sing-box profile: top level is not an object")
	}
	dns, err := root.child("dns", jsonObject)
	if err != nil {
		return nil, err
	}
	dnsServers, err := dns.child("servers", jsonArray)
	if err != nil {
		return nil, err
	}
	dnsRules, err := dns.child("rules", jsonArray)
	if err != nil {
		return nil, err
	}
	route, err := root.child("route", jsonObject)
	if err != nil {
		return nil, err
	}
	routeRules, err := route.child("rules", jsonArray)
	if err != nil {
		return nil, err
	}
	outbounds, err := root.child("outbounds", jsonArray)
	if err != nil {
		return nil, err
	}
	hijack := slices.IndexFunc(routeRules.vals, func(r *jsonNode) bool {
		return r.str("protocol") == "dns" && r.str("action") == "hijack-dns"
	})
	if hijack < 0 {
		return nil, errors.New("sing-box profile: route.rules has no hijack-dns rule")
	}

	// The rules below are inserted, not merged, so a second application
	// would duplicate them; the other formats refuse it the same way.
	if slices.ContainsFunc(dnsRules.vals, func(r *jsonNode) bool {
		return jsonHasString(r.get("domain_suffix"), magicDNSSuffix)
	}) {
		return nil, errors.New("sing-box profile: dns.rules already routes " + magicDNSSuffix)
	}

	dnsServer, outbound := "local", "DIRECT"
	switch opts.Role {
	case TailscaleServer:
		if !hasTag(dnsServers, dnsServer) {
			return nil, errors.New(`sing-box profile: dns.servers has no "local" server`)
		}
		if !hasTag(outbounds, outbound) {
			return nil, errors.New(`sing-box profile: outbounds has no "DIRECT" outbound`)
		}
		inbounds, err := root.child("inbounds", jsonArray)
		if err != nil {
			return nil, err
		}
		tunAt := slices.IndexFunc(inbounds.vals, func(in *jsonNode) bool { return in.str("type") == "tun" })
		if tunAt < 0 {
			return nil, errors.New("sing-box profile: inbounds has no tun inbound")
		}
		// Keep tailnet packets off the TUN so the official app's interface
		// carries them.
		if err := inbounds.vals[tunAt].mergeStrings("route_exclude_address", tailnetIPv4, tailnetIPv6); err != nil {
			return nil, err
		}
	case TailscaleClient:
		dnsServer, outbound = tailscaleDNSTag, tailscaleTag
		endpoints := root.get("endpoints")
		if endpoints != nil && endpoints.kind != jsonArray {
			return nil, errors.New("sing-box profile: endpoints is not an array")
		}
		if hasTag(outbounds, tailscaleTag) || (endpoints != nil && hasTag(endpoints, tailscaleTag)) {
			return nil, fmt.Errorf("sing-box profile: tag %q is already in use", tailscaleTag)
		}
		if hasTag(dnsServers, tailscaleDNSTag) {
			return nil, fmt.Errorf("sing-box profile: dns server tag %q is already in use", tailscaleDNSTag)
		}
		endpoint := jsonObj(
			"type", "tailscale",
			"tag", tailscaleTag,
			"state_directory", "tailscale",
			"auth_key", opts.AuthKey,
			"hostname", opts.Hostname,
		)
		if endpoints == nil {
			// The documented top-level order is endpoints, inbounds, outbounds.
			root.insertBefore("inbounds", "endpoints", jsonArr(endpoint))
		} else {
			endpoints.vals = append(endpoints.vals, endpoint)
		}
		// Without default resolvers the server answers MagicDNS names only.
		dnsServers.vals = append(dnsServers.vals, jsonObj(
			"type", "tailscale",
			"tag", tailscaleDNSTag,
			"endpoint", tailscaleTag,
			"accept_default_resolvers", false,
		))
	}

	// Ahead of the fakeip A/AAAA rule, so MagicDNS names get real addresses.
	dnsRules.vals = slices.Insert(dnsRules.vals, 0, jsonObj(
		"domain_suffix", []string{magicDNSSuffix},
		"action", "route",
		"server", dnsServer,
	))
	// Right after hijack-dns and ahead of the Private rule set, which also
	// covers 100.64.0.0/10.
	routeRules.vals = slices.Insert(routeRules.vals, hijack+1, jsonObj(
		"domain_suffix", []string{magicDNSSuffix},
		"ip_cidr", []string{tailnetIPv4, tailnetIPv6},
		"outbound", outbound,
	))

	var b bytes.Buffer
	if err := root.encode(&b, 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

type jsonKind int

const (
	jsonScalar jsonKind = iota
	jsonObject
	jsonArray
)

// jsonNode is a JSON value that remembers object key order. Objects keep
// parallel keys/vals; arrays keep their items in vals.
type jsonNode struct {
	kind   jsonKind
	keys   []string
	vals   []*jsonNode
	scalar any
}

func decodeOrderedJSON(body []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	root, err := decodeJSONNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the top-level value")
	}
	return root, nil
}

func decodeJSONNode(dec *json.Decoder) (*jsonNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return &jsonNode{kind: jsonScalar, scalar: tok}, nil
	}
	n := &jsonNode{kind: jsonArray}
	if delim == '{' {
		n.kind = jsonObject
	}
	for dec.More() {
		if n.kind == jsonObject {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			if slices.Contains(n.keys, key) {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			n.keys = append(n.keys, key)
		}
		v, err := decodeJSONNode(dec)
		if err != nil {
			return nil, err
		}
		n.vals = append(n.vals, v)
	}
	if _, err := dec.Token(); err != nil { // closing delimiter
		return nil, err
	}
	return n, nil
}

// jsonObj builds an object from alternating key/value arguments.
func jsonObj(kv ...any) *jsonNode {
	n := &jsonNode{kind: jsonObject}
	for i := 0; i+1 < len(kv); i += 2 {
		n.keys = append(n.keys, kv[i].(string))
		n.vals = append(n.vals, jsonVal(kv[i+1]))
	}
	return n
}

func jsonArr(items ...*jsonNode) *jsonNode {
	return &jsonNode{kind: jsonArray, vals: items}
}

func jsonVal(v any) *jsonNode {
	switch t := v.(type) {
	case *jsonNode:
		return t
	case []string:
		arr := jsonArr()
		for _, s := range t {
			arr.vals = append(arr.vals, jsonVal(s))
		}
		return arr
	}
	return &jsonNode{kind: jsonScalar, scalar: v}
}

func (n *jsonNode) get(key string) *jsonNode {
	if n.kind != jsonObject {
		return nil
	}
	if i := slices.Index(n.keys, key); i >= 0 {
		return n.vals[i]
	}
	return nil
}

// child returns the object member key, which must exist with the given kind.
func (n *jsonNode) child(key string, kind jsonKind) (*jsonNode, error) {
	c := n.get(key)
	if c == nil {
		return nil, fmt.Errorf("sing-box profile: missing %q", key)
	}
	if c.kind != kind {
		return nil, fmt.Errorf("sing-box profile: %q has an unexpected type", key)
	}
	return c, nil
}

// str returns the string member key, or "" when absent or not a string.
func (n *jsonNode) str(key string) string {
	if c := n.get(key); c != nil && c.kind == jsonScalar {
		s, _ := c.scalar.(string)
		return s
	}
	return ""
}

// insertBefore adds key ahead of the member before, or at the end when before
// is absent.
func (n *jsonNode) insertBefore(before, key string, v *jsonNode) {
	at := slices.Index(n.keys, before)
	if at < 0 {
		at = len(n.keys)
	}
	n.keys = slices.Insert(n.keys, at, key)
	n.vals = slices.Insert(n.vals, at, v)
}

// mergeStrings appends the values missing from the string array key,
// creating it when absent.
func (n *jsonNode) mergeStrings(key string, values ...string) error {
	arr := n.get(key)
	if arr == nil {
		n.keys = append(n.keys, key)
		n.vals = append(n.vals, jsonVal(values))
		return nil
	}
	if arr.kind != jsonArray {
		return fmt.Errorf("sing-box profile: %q is not an array", key)
	}
	for _, v := range values {
		if !slices.ContainsFunc(arr.vals, func(e *jsonNode) bool { return e.scalar == v }) {
			arr.vals = append(arr.vals, jsonVal(v))
		}
	}
	return nil
}

// jsonHasString reports whether n is the string value or an array holding it.
func jsonHasString(n *jsonNode, value string) bool {
	if n == nil {
		return false
	}
	if n.kind == jsonArray {
		return slices.ContainsFunc(n.vals, func(e *jsonNode) bool { return e.scalar == value })
	}
	return n.kind == jsonScalar && n.scalar == value
}

func hasTag(arr *jsonNode, tag string) bool {
	return slices.ContainsFunc(arr.vals, func(e *jsonNode) bool { return e.str("tag") == tag })
}

// encode writes n with two-space indentation, matching json.MarshalIndent
// but without HTML escaping so emoji, CJK tags and URLs stay readable.
func (n *jsonNode) encode(b *bytes.Buffer, depth int) error {
	if n.kind == jsonScalar {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(n.scalar); err != nil {
			return err
		}
		b.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
		return nil
	}
	opening, closing := byte('['), byte(']')
	if n.kind == jsonObject {
		opening, closing = '{', '}'
	}
	b.WriteByte(opening)
	if len(n.vals) == 0 {
		b.WriteByte(closing)
		return nil
	}
	b.WriteByte('\n')
	for i, v := range n.vals {
		b.WriteString(strings.Repeat("  ", depth+1))
		if n.kind == jsonObject {
			if err := (&jsonNode{scalar: n.keys[i]}).encode(b, 0); err != nil {
				return err
			}
			b.WriteString(": ")
		}
		if err := v.encode(b, depth+1); err != nil {
			return err
		}
		if i < len(n.vals)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte(closing)
	return nil
}

// --- mihomo (Clash Meta) ---

// applyClashTailscale edits the mihomo profile line by line. The repository has
// no YAML library and the base profile comes from our own template, so blocks
// are located by key and indentation; anything unexpected is an error.
//
// Field references (MetaCubeX Meta-Docs): tun.route-exclude-address
// (docs/config/inbound/tun.md), the tailscale proxy's hostname/auth-key/
// state-dir/udp fields (docs/config/proxies/tailscale.md). Nameserver schemes
// come from mihomo's config.parseNameServer: "system" (bare form expanded to
// system://) queries the OS-configured resolvers, and "ts://<proxy name>"
// resolves through the named tailscale proxy.
func applyClashTailscale(body []byte, opts TailscaleOptions) ([]byte, error) {
	lines := strings.Split(string(body), "\n")
	var err error

	policy := `"system"`
	if opts.Role == TailscaleClient {
		policy = strconv.Quote("ts://" + tailscaleTag)
		if lines, err = clashAddTailscaleProxy(lines, opts); err != nil {
			return nil, err
		}
	}
	if lines, err = clashEditDNS(lines, policy); err != nil {
		return nil, err
	}
	if opts.Role == TailscaleServer {
		if lines, err = clashExcludeTunRoutes(lines); err != nil {
			return nil, err
		}
	}
	rulesAt, _, err := yamlFind(lines, 0, len(lines), 0, "rules")
	if err != nil {
		return nil, err
	}
	if rulesAt < 0 {
		return nil, errors.New("clash profile: missing rules")
	}
	prefix := yamlItemPrefix(lines, rulesAt, 2)
	var added []string
	for _, r := range tailnetRules(opts) {
		added = append(added, prefix+r)
	}
	lines = insertLines(lines, rulesAt+1, added...)
	return []byte(strings.Join(lines, "\n")), nil
}

// clashEditDNS keeps MagicDNS names out of fake-ip and resolves them with the
// given nameserver-policy value.
func clashEditDNS(lines []string, policy string) ([]string, error) {
	find := func() (int, int, int, error) {
		at, end, err := yamlFind(lines, 0, len(lines), 0, "dns")
		if err != nil {
			return 0, 0, 0, err
		}
		if at < 0 {
			return 0, 0, 0, errors.New("clash profile: missing dns")
		}
		indent := yamlChildIndent(lines, at, end)
		if indent < 0 {
			return 0, 0, 0, errors.New("clash profile: dns is empty")
		}
		return at, end, indent, nil
	}
	at, end, indent, err := find()
	if err != nil {
		return nil, err
	}
	mode, err := yamlKeyLine(lines, at+1, end, indent, "fake-ip-filter-mode")
	if err != nil {
		return nil, err
	}
	// Under the other modes a fake-ip-filter entry is not a plain exclusion.
	if mode >= 0 && yamlScalarValue(lines[mode]) != "blacklist" {
		return nil, errors.New("clash profile: dns.fake-ip-filter-mode is not blacklist")
	}

	if lines, err = yamlAppendItem(lines, at+1, end, indent, "fake-ip-filter", `"+.`+magicDNSSuffix+`"`); err != nil {
		return nil, err
	}
	if at, end, indent, err = find(); err != nil {
		return nil, err
	}
	key := `"+.` + magicDNSSuffix + `"`
	pAt, pEnd, err := yamlFind(lines, at+1, end, indent, "nameserver-policy")
	if err != nil {
		return nil, err
	}
	if pAt < 0 {
		pad := strings.Repeat(" ", indent)
		return insertLines(lines, end, pad+"nameserver-policy:", pad+"  "+key+": "+policy), nil
	}
	for _, l := range lines[pAt+1 : pEnd] {
		if k, _, _ := strings.Cut(strings.TrimSpace(l), ":"); strings.Trim(k, `"'`) == "+."+magicDNSSuffix {
			return nil, errors.New("clash profile: dns.nameserver-policy already maps +." + magicDNSSuffix)
		}
	}
	childIndent := yamlChildIndent(lines, pAt, pEnd)
	if childIndent < 0 {
		childIndent = indent + 2
	}
	return insertLines(lines, pEnd, strings.Repeat(" ", childIndent)+key+": "+policy), nil
}

// clashExcludeTunRoutes keeps tailnet routes off mihomo's TUN, merging into an
// existing top-level tun block or adding one ahead of dns.
func clashExcludeTunRoutes(lines []string) ([]string, error) {
	at, end, err := yamlFind(lines, 0, len(lines), 0, "tun")
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		indent := yamlChildIndent(lines, at, end)
		if indent < 0 {
			indent = 2
		}
		for _, cidr := range []string{tailnetIPv4, tailnetIPv6} {
			if lines, err = yamlAppendItem(lines, at+1, end, indent, "route-exclude-address", cidr); err != nil {
				return nil, err
			}
			if at, end, err = yamlFind(lines, 0, len(lines), 0, "tun"); err != nil {
				return nil, err
			}
		}
		return lines, nil
	}
	dnsAt, _, err := yamlFind(lines, 0, len(lines), 0, "dns")
	if err != nil {
		return nil, err
	}
	if dnsAt < 0 {
		return nil, errors.New("clash profile: missing dns")
	}
	return insertLines(lines, dnsAt,
		"tun:",
		"  route-exclude-address:",
		"    - "+tailnetIPv4,
		"    - "+tailnetIPv6,
		"",
	), nil
}

// clashAddTailscaleProxy adds the embedded tailscale proxy, merging into an
// existing top-level proxies list or adding one ahead of proxy-groups.
func clashAddTailscaleProxy(lines []string, opts TailscaleOptions) ([]string, error) {
	groupsAt, _, err := yamlFind(lines, 0, len(lines), 0, "proxy-groups")
	if err != nil {
		return nil, err
	}
	if groupsAt < 0 {
		return nil, errors.New("clash profile: missing proxy-groups")
	}
	item := func(prefix string) []string {
		pad := strings.Repeat(" ", len(prefix))
		// udp: true lets UDP to tailnet peers through the proxy; the fields
		// are already charset-validated, quoting only keeps YAML from typing
		// a numeric or boolean-looking hostname.
		return []string{
			prefix + "name: " + tailscaleTag,
			pad + "type: tailscale",
			pad + "hostname: " + strconv.Quote(opts.Hostname),
			pad + "auth-key: " + strconv.Quote(opts.AuthKey),
			pad + "state-dir: ./tailscale",
			pad + "udp: true",
		}
	}
	at, end, err := yamlFind(lines, 0, len(lines), 0, "proxies")
	if err != nil {
		return nil, err
	}
	if at < 0 {
		return insertLines(lines, groupsAt, append(append([]string{"proxies:"}, item("  - ")...), "")...), nil
	}
	if slices.ContainsFunc(lines[at+1:end], func(l string) bool { return yamlNameItem(l) == tailscaleTag }) {
		return nil, fmt.Errorf("clash profile: proxies already has %q", tailscaleTag)
	}
	return insertLines(lines, end, item(yamlItemPrefix(lines, at, 2))...), nil
}

// yamlKeyLine returns the line holding key at exactly indent spaces within
// lines[from:to], or -1 when absent. A repeated key is an error.
func yamlKeyLine(lines []string, from, to, indent int, key string) (int, error) {
	prefix := strings.Repeat(" ", indent) + key + ":"
	at := -1
	for i := from; i < to; i++ {
		if !strings.HasPrefix(lines[i], prefix) {
			continue
		}
		if at >= 0 {
			return 0, fmt.Errorf("profile key %q is repeated", key)
		}
		at = i
	}
	return at, nil
}

// yamlFind locates the block opened by key at exactly indent spaces within
// lines[from:to]. It returns the key line and the end of its block (exclusive,
// trailing blank lines left out), or -1 when absent. A key with an inline
// value instead of a block is an error.
func yamlFind(lines []string, from, to, indent int, key string) (at, end int, err error) {
	if at, err = yamlKeyLine(lines, from, to, indent, key); err != nil || at < 0 {
		return -1, -1, err
	}
	rest := strings.TrimSpace(lines[at][indent+len(key)+1:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return 0, 0, fmt.Errorf("profile key %q has an inline value; expected a block", key)
	}
	end = at + 1
	for end < to {
		l := lines[end]
		t := strings.TrimSpace(l)
		lead := len(l) - len(strings.TrimLeft(l, " "))
		if t == "" || strings.HasPrefix(t, "#") || lead > indent || (lead == indent && strings.HasPrefix(t, "- ")) {
			end++
			continue
		}
		break
	}
	for end > at+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return at, end, nil
}

// yamlChildIndent reports the indentation of the first content line inside the
// block (at, end), or -1 for an empty block.
func yamlChildIndent(lines []string, at, end int) int {
	for _, l := range lines[at+1 : end] {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return len(l) - len(strings.TrimLeft(l, " "))
		}
	}
	return -1
}

// yamlItemPrefix returns the "<indent>- " prefix used by the list under the key
// at line at, falling back to fallback spaces for an empty list.
func yamlItemPrefix(lines []string, at, fallback int) string {
	for _, l := range lines[at+1:] {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "- ") {
			return l[:len(l)-len(t)] + "- "
		}
		break
	}
	return strings.Repeat(" ", fallback) + "- "
}

// yamlAppendItem appends value to the list key found at indent within
// lines[from:to], creating the key at the end of that range when absent. A
// value already listed is left alone.
func yamlAppendItem(lines []string, from, to, indent int, key, value string) ([]string, error) {
	at, end, err := yamlFind(lines, from, to, indent, key)
	if err != nil {
		return nil, err
	}
	pad := strings.Repeat(" ", indent)
	if at < 0 {
		insertAt := to
		for insertAt > from && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		return insertLines(lines, insertAt, pad+key+":", pad+"  - "+value), nil
	}
	want := strings.Trim(value, `"'`)
	for _, l := range lines[at+1 : end] {
		if item, ok := strings.CutPrefix(strings.TrimSpace(l), "- "); ok && strings.Trim(strings.TrimSpace(item), `"'`) == want {
			return lines, nil
		}
	}
	return insertLines(lines, end, yamlItemPrefix(lines, at, indent+2)+value), nil
}

// yamlScalarValue returns the unquoted value of a "key: value" line.
func yamlScalarValue(line string) string {
	_, v, _ := strings.Cut(line, ":")
	v, _, _ = strings.Cut(v, " #")
	return strings.Trim(strings.TrimSpace(v), `"'`)
}

// yamlNameItem returns the name of a "- name: X" list item line, or "".
func yamlNameItem(line string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- name:")
	if !ok {
		return ""
	}
	return strings.Trim(strings.TrimSpace(rest), `"'`)
}

// --- Surge ---

// applySurgeTailscale edits the Surge profile section by section.
//
// Syntax references (manual.nssurge.com): the tailscale policy line and its
// [Tailscale <section-name>] section with auth-key/hostname
// (policies/tailscale.html); tun-excluded-routes, always-real-ip and skip-proxy
// in [General] (profile/general.html); "server:system" in [Host]
// (dns/local-dns-mapping.html).
func applySurgeTailscale(body []byte, opts TailscaleOptions) ([]byte, error) {
	lines := strings.Split(string(body), "\n")
	var err error
	general := func(edit func(start, end int) []string) error {
		start, end, err := iniSection(lines, "General")
		if err != nil {
			return err
		}
		if start < 0 {
			return errors.New("surge profile: missing [General]")
		}
		lines = edit(start, end)
		return nil
	}
	mergeGeneral := func(key string, values ...string) error {
		return general(func(start, end int) []string { return iniMergeList(lines, start, end, key, values...) })
	}

	if err = mergeGeneral("always-real-ip", "*."+magicDNSSuffix); err != nil {
		return nil, err
	}
	switch opts.Role {
	case TailscaleServer:
		// tun-excluded-routes keeps the tailnet off Surge VIF; skip-proxy
		// keeps system-proxy requests for tailnet addresses and MagicDNS
		// names off Surge as well. The base profile lists neither.
		if err = mergeGeneral("tun-excluded-routes", tailnetIPv4, tailnetIPv6); err != nil {
			return nil, err
		}
		if err = mergeGeneral("skip-proxy", tailnetIPv4, "*."+magicDNSSuffix); err != nil {
			return nil, err
		}
		if lines, err = surgeAddHost(lines); err != nil {
			return nil, err
		}
	case TailscaleClient:
		// skip-proxy would hand system-proxy requests for the tailnet to the
		// system (macOS) or to VIF (iOS) instead of the Tailscale policy.
		if err = general(func(start, end int) []string {
			return iniRemoveFromList(lines, start, end, "skip-proxy", tailnetIPv4)
		}); err != nil {
			return nil, err
		}
		if lines, err = surgeAddTailscalePolicy(lines, opts); err != nil {
			return nil, err
		}
	}

	ruleAt, _, err := iniSection(lines, "Rule")
	if err != nil {
		return nil, err
	}
	if ruleAt < 0 {
		return nil, errors.New("surge profile: missing [Rule]")
	}
	lines = insertLines(lines, ruleAt+1, tailnetRules(opts)...)
	return []byte(strings.Join(lines, "\n")), nil
}

// surgeAddHost resolves MagicDNS names with the system resolver, which the
// official Tailscale app configures.
func surgeAddHost(lines []string) ([]string, error) {
	const key = "*." + magicDNSSuffix
	entry := key + " = server:system"
	start, end, err := iniSection(lines, "Host")
	if err != nil {
		return nil, err
	}
	if start >= 0 {
		if iniKey(lines, start, end, key) >= 0 {
			return nil, errors.New("surge profile: [Host] already maps " + key)
		}
		return insertLines(lines, iniContentEnd(lines, start, end), entry), nil
	}
	ruleAt, _, err := iniSection(lines, "Rule")
	if err != nil {
		return nil, err
	}
	if ruleAt < 0 {
		return nil, errors.New("surge profile: missing [Rule]")
	}
	return insertLines(lines, ruleAt, "[Host]", entry, ""), nil
}

// surgeAddTailscalePolicy adds the Tailscale policy to [Proxy] and its
// settings section ahead of [Rule].
func surgeAddTailscalePolicy(lines []string, opts TailscaleOptions) ([]string, error) {
	settings := "Tailscale " + surgeTailscaleSection
	if at, _, err := iniSection(lines, settings); err != nil {
		return nil, err
	} else if at >= 0 {
		return nil, fmt.Errorf("surge profile: [%s] already exists", settings)
	}
	start, end, err := iniSection(lines, "Proxy")
	if err != nil {
		return nil, err
	}
	if start < 0 {
		return nil, errors.New("surge profile: missing [Proxy]")
	}
	if iniKey(lines, start, end, tailscaleTag) >= 0 {
		return nil, fmt.Errorf("surge profile: [Proxy] already has %q", tailscaleTag)
	}
	lines = insertLines(lines, iniContentEnd(lines, start, end),
		tailscaleTag+" = tailscale, section-name="+surgeTailscaleSection)

	ruleAt, _, err := iniSection(lines, "Rule")
	if err != nil {
		return nil, err
	}
	if ruleAt < 0 {
		return nil, errors.New("surge profile: missing [Rule]")
	}
	return insertLines(lines, ruleAt,
		"["+settings+"]",
		"auth-key = "+opts.AuthKey,
		"hostname = "+opts.Hostname,
		"",
	), nil
}

// iniSection returns the header line of [name] and the index of the next
// header (or len(lines)), or -1 when the section is absent.
func iniSection(lines []string, name string) (start, end int, err error) {
	start = -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "["+name+"]" {
			continue
		}
		if start >= 0 {
			return 0, 0, fmt.Errorf("surge profile: [%s] is repeated", name)
		}
		start = i
	}
	if start < 0 {
		return -1, -1, nil
	}
	end = start + 1
	for end < len(lines) && !iniIsHeader(lines[end]) {
		end++
	}
	return start, end, nil
}

func iniIsHeader(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")
}

// iniContentEnd is the insertion point for appending to a section: after its
// last non-blank line.
func iniContentEnd(lines []string, start, end int) int {
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}

// iniKey returns the line of key inside the section, or -1.
func iniKey(lines []string, start, end int, key string) int {
	for i := start + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "//") {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			return i
		}
	}
	return -1
}

func iniListValues(line string) []string {
	_, v, _ := strings.Cut(line, "=")
	var values []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

// iniMergeList appends the missing values to the comma-separated key, adding
// the key at the end of the section when absent.
func iniMergeList(lines []string, start, end int, key string, values ...string) []string {
	at := iniKey(lines, start, end, key)
	if at < 0 {
		return insertLines(lines, iniContentEnd(lines, start, end), key+" = "+strings.Join(values, ", "))
	}
	merged := iniListValues(lines[at])
	for _, v := range values {
		if !slices.Contains(merged, v) {
			merged = append(merged, v)
		}
	}
	lines = slices.Clone(lines)
	lines[at] = key + " = " + strings.Join(merged, ", ")
	return lines
}

// iniRemoveFromList drops value from the comma-separated key, removing the key
// when nothing is left.
func iniRemoveFromList(lines []string, start, end int, key, value string) []string {
	at := iniKey(lines, start, end, key)
	if at < 0 {
		return lines
	}
	kept := slices.DeleteFunc(iniListValues(lines[at]), func(v string) bool { return v == value })
	if len(kept) == 0 {
		return slices.Delete(slices.Clone(lines), at, at+1)
	}
	lines = slices.Clone(lines)
	lines[at] = key + " = " + strings.Join(kept, ", ")
	return lines
}
