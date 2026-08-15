package subscription

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// EndpointRewrite redirects a node's published address to the relay that fronts
// it. Only the address a client dials is changed — never the SNI, the
// certificate name, or any credential — because the relay forwards the packets
// without unwrapping them and TLS still terminates on the landing node. That is
// what keeps the rewrite invisible: the node keeps its name, its tag, and every
// property a client profile groups or filters on.
type EndpointRewrite struct {
	// Host is the landing node's own hostname, as it appears in the links the
	// landing node published.
	Host string
	// To is the relay's hostname, which replaces it.
	To string
	// Ports maps each of the landing node's listen ports to the port the relay
	// answers on for it. A port that is not listed is left alone, so a protocol
	// the relay does not front keeps pointing at the landing node.
	Ports map[int]int
}

// Valid reports whether this rewrite would change anything.
func (r EndpointRewrite) Valid() bool {
	return strings.TrimSpace(r.Host) != "" && strings.TrimSpace(r.To) != "" && len(r.Ports) > 0
}

// apply returns the relay address for one published endpoint, and whether it
// was fronted at all.
func (r EndpointRewrite) apply(host string, port int) (string, int, bool) {
	if !r.Valid() || !sameHost(host, r.Host) {
		return host, port, false
	}
	relayPort, ok := r.Ports[port]
	if !ok {
		return host, port, false
	}
	return strings.TrimSpace(r.To), relayPort, true
}

func sameHost(a, b string) bool {
	a = strings.TrimSuffix(strings.TrimSpace(a), ".")
	b = strings.TrimSuffix(strings.TrimSpace(b), ".")
	return a != "" && strings.EqualFold(a, b)
}

// RewriteDefaultLinkEndpoint redirects one universal link's server address. The
// query string carries the SNI and is left exactly as it was.
func RewriteDefaultLinkEndpoint(link string, r EndpointRewrite) string {
	if !r.Valid() {
		return link
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" {
		return link
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return link
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return link
	}
	newHost, newPort, rewritten := r.apply(host, port)
	if !rewritten {
		return link
	}
	u.Host = net.JoinHostPort(newHost, strconv.Itoa(newPort))
	return u.String()
}

// RewriteClashEndpoints redirects the server address of every proxy in a Clash
// proxies fragment. The `sni`/`servername` fields are deliberately untouched.
func RewriteClashEndpoints(fragment string, r EndpointRewrite) string {
	if !r.Valid() {
		return fragment
	}
	lines := strings.Split(fragment, "\n")
	for _, item := range clashItemRanges(lines) {
		serverLine, hasServer := clashFieldLine(lines, item, "server")
		portLine, hasPort := clashFieldLine(lines, item, "port")
		if !hasServer || !hasPort {
			continue
		}
		port, err := strconv.Atoi(clashValue(lines[portLine]))
		if err != nil {
			continue
		}
		newHost, newPort, rewritten := r.apply(clashValue(lines[serverLine]), port)
		if !rewritten {
			continue
		}
		lines[serverLine] = setClashValue(lines[serverLine], newHost)
		lines[portLine] = setClashValue(lines[portLine], strconv.Itoa(newPort))
	}
	return strings.Join(lines, "\n")
}

// clashItemRanges returns the [start, end) line span of every proxy in the
// fragment. An item begins at its "- " marker and runs to the next one.
func clashItemRanges(lines []string) [][2]int {
	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "- ") {
			starts = append(starts, i)
		}
	}
	ranges := make([][2]int, 0, len(starts))
	for i, start := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges
}

// clashFieldLine finds one top-level field of a proxy item. Nested blocks such
// as reality-opts are indented further and are skipped, so a nested key can
// never be mistaken for the proxy's own.
func clashFieldLine(lines []string, item [2]int, field string) (int, bool) {
	depth := -1
	for i := item[0]; i < item[1]; i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if trimmed == "" {
			continue
		}
		indent := len(lines[i]) - len(trimmed)
		if marker := strings.TrimPrefix(trimmed, "- "); marker != trimmed {
			// The first field shares the marker's line and sits two columns in.
			indent += 2
			trimmed = marker
		}
		if depth < 0 {
			depth = indent
		}
		if indent != depth {
			continue
		}
		if key, _, found := strings.Cut(trimmed, ":"); found && strings.TrimSpace(key) == field {
			return i, true
		}
	}
	return 0, false
}

// clashValue reads a field line's value, dropping the quotes the generator adds
// around values that need them.
func clashValue(line string) string {
	_, value, found := strings.Cut(line, ":")
	if !found {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return value
}

// setClashValue replaces a field line's value, quoting the replacement exactly
// when the original was quoted so the fragment's style is preserved.
func setClashValue(line, value string) string {
	key, old, found := strings.Cut(line, ":")
	if !found {
		return line
	}
	if strings.HasPrefix(strings.TrimSpace(old), `"`) {
		value = strconv.Quote(value)
	}
	return key + ": " + value
}

// RewriteSurgeEndpoints redirects the server address of every proxy line in a
// Surge fragment: "name = type, server, port, key=value, ...".
func RewriteSurgeEndpoints(fragment string, r EndpointRewrite) string {
	if !r.Valid() {
		return fragment
	}
	lines := strings.Split(fragment, "\n")
	for i, line := range lines {
		_, definition, found := strings.Cut(line, " = ")
		if !found {
			continue
		}
		fields := strings.Split(definition, ",")
		if len(fields) < 3 {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			continue
		}
		newHost, newPort, rewritten := r.apply(strings.TrimSpace(fields[1]), port)
		if !rewritten {
			continue
		}
		fields[1] = " " + newHost
		fields[2] = " " + strconv.Itoa(newPort)
		lines[i] = strings.SplitN(line, " = ", 2)[0] + " = " + strings.Join(fields, ",")
	}
	return strings.Join(lines, "\n")
}

// RewriteSingBoxEndpoints redirects the server address of every outbound in a
// JSON outbound array. tls.server_name carries the SNI and is left alone.
func RewriteSingBoxEndpoints(outbounds []byte, r EndpointRewrite) ([]byte, error) {
	if !r.Valid() {
		return outbounds, nil
	}
	outs, err := decodeOutbounds(outbounds)
	if err != nil {
		return nil, err
	}
	for _, ob := range outs {
		RewriteOutboundEndpoint(ob, r)
	}
	return json.Marshal(outs)
}

// RewriteOutboundEndpoint redirects one decoded sing-box outbound in place.
func RewriteOutboundEndpoint(outbound map[string]any, r EndpointRewrite) {
	if !r.Valid() {
		return
	}
	host, ok := outbound["server"].(string)
	if !ok {
		return
	}
	port, ok := numericField(outbound["server_port"])
	if !ok {
		return
	}
	newHost, newPort, rewritten := r.apply(host, port)
	if !rewritten {
		return
	}
	outbound["server"] = newHost
	outbound["server_port"] = newPort
}

// numericField reads a port that may have come from a JSON decode (float64), a
// json.Number, or a value this process built itself (int).
func numericField(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}
