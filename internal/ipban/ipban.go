// Package ipban is the node-local data plane of the IP ban feature, plus the
// hub's registry of which address is banned from which node.
//
// A ban is enforced where the client's own address is still visible. That is
// the landing node itself for a direct connection, but for a relayed one it is
// the relay: the relay masquerades what it forwards, so the landing node only
// ever sees the relay's address. The hub therefore turns every ban on node A
// into two kinds of rule — A drops the address on its own protocol ports, and
// whichever relay fronts A drops it on the ports it forwards to A — and pushes
// each node its complete rule set. The relay's own protocol ports are untouched,
// so an address banned from A alone can still use the relay as a node in its
// own right.
//
// Only proxy ports are matched. The cover site, the subscription and monitor
// endpoints, SSH and the WireGuard overlay all stay reachable.
package ipban

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	// Table is the nftables table the drop rules live in. It is this
	// deployment's own, so replacing it never disturbs the rules ufw, firewalld
	// or the relay data plane manage.
	Table = "singbox_deploy_ipban"

	// stateFile holds this node's rule set. It is what the boot-time unit
	// reapplies from, so it is the single node-local record of the bans.
	stateFile = "ipban.json"
)

// Rule drops every listed address on every listed port.
type Rule struct {
	// Label says what the ports belong to — this node's own protocols, or the
	// relay ports fronting one landing node. It is for display only.
	Label string `json:"label"`
	// Addresses are canonical single addresses or CIDR prefixes, IPv4 or IPv6.
	Addresses []string `json:"addresses"`
	TCP       []int    `json:"tcp,omitempty"`
	UDP       []int    `json:"udp,omitempty"`
}

// Config is this node's complete ban rule set.
type Config struct {
	Rules []Rule `json:"rules"`
}

// Empty reports whether the configuration drops nothing, in which case the
// table and the boot-time unit are removed rather than installed empty.
func (c Config) Empty() bool {
	for _, rule := range c.Rules {
		if rule.active() {
			return false
		}
	}
	return true
}

func (r Rule) active() bool {
	return len(r.Addresses) > 0 && (len(r.TCP) > 0 || len(r.UDP) > 0)
}

// Validate rejects a configuration that could not be rendered into a ruleset.
func (c Config) Validate() error {
	for _, rule := range c.Rules {
		for _, address := range rule.Addresses {
			if _, err := ParseAddress(address); err != nil {
				return err
			}
		}
		for _, port := range append(append([]int(nil), rule.TCP...), rule.UDP...) {
			if port < 1 || port > 65535 {
				return fmt.Errorf("banned port %d is out of range", port)
			}
		}
	}
	return nil
}

// ParseAddress accepts a single IPv4/IPv6 address or a CIDR prefix and returns
// it in canonical form: host bits masked off, an IPv4-mapped IPv6 address
// reduced to IPv4. A prefix of length zero is refused, because it would ban
// every client at once.
func ParseAddress(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Prefix{}, fmt.Errorf("an IP address is required")
	}
	var prefix netip.Prefix
	if strings.Contains(value, "/") {
		parsed, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or CIDR prefix", value)
		}
		addr := parsed.Addr()
		bits := parsed.Bits()
		if addr.Is4In6() {
			if bits < 96 {
				return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or CIDR prefix", value)
			}
			addr, bits = addr.Unmap(), bits-96
		}
		prefix = netip.PrefixFrom(addr, bits)
	} else {
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or CIDR prefix", value)
		}
		if addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("%q carries an interface zone, which a ban cannot match", value)
		}
		addr = addr.Unmap()
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if prefix.Bits() == 0 {
		return netip.Prefix{}, fmt.Errorf("%q would ban every client", value)
	}
	return prefix.Masked(), nil
}

// CanonicalAddress returns the canonical spelling of a ban address: a single
// host without its prefix length, anything wider as CIDR.
func CanonicalAddress(value string) (string, error) {
	prefix, err := ParseAddress(value)
	if err != nil {
		return "", err
	}
	return formatPrefix(prefix), nil
}

func formatPrefix(prefix netip.Prefix) string {
	if prefix.IsSingleIP() {
		return prefix.Addr().String()
	}
	return prefix.String()
}

// collapse drops every prefix already covered by a wider one of the same
// family. nftables refuses an anonymous interval set whose elements overlap,
// and two bans on one node may well overlap.
func collapse(prefixes []netip.Prefix) []netip.Prefix {
	sorted := append([]netip.Prefix(nil), prefixes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Bits() != sorted[j].Bits() {
			return sorted[i].Bits() < sorted[j].Bits()
		}
		return sorted[i].Addr().Less(sorted[j].Addr())
	})
	var kept []netip.Prefix
	for _, candidate := range sorted {
		covered := false
		for _, wider := range kept {
			if wider.Addr().Is4() == candidate.Addr().Is4() && wider.Contains(candidate.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, candidate)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Addr() != kept[j].Addr() {
			return kept[i].Addr().Less(kept[j].Addr())
		}
		return kept[i].Bits() < kept[j].Bits()
	})
	return kept
}

// Ruleset renders the complete nftables program for cfg. The empty table
// declaration before the delete is the atomic-replace idiom, so one load always
// fully replaces the previous rule set.
//
// The chain hooks prerouting at raw priority. That runs before connection
// tracking and before the relay's DNAT, so it matches a relay port while the
// packet still carries it, and it cuts an established connection off at its
// next packet rather than only refusing new ones.
func Ruleset(cfg Config) string {
	var b strings.Builder
	b.WriteString("table inet " + Table + " {}\n")
	b.WriteString("delete table inet " + Table + "\n\n")
	b.WriteString("table inet " + Table + " {\n")
	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype filter hook prerouting priority raw; policy accept;\n")
	for _, rule := range cfg.Rules {
		if !rule.active() {
			continue
		}
		var v4, v6 []string
		var prefixes []netip.Prefix
		for _, address := range rule.Addresses {
			if prefix, err := ParseAddress(address); err == nil {
				prefixes = append(prefixes, prefix)
			}
		}
		for _, prefix := range collapse(prefixes) {
			if prefix.Addr().Is4() {
				v4 = append(v4, formatPrefix(prefix))
			} else {
				v6 = append(v6, formatPrefix(prefix))
			}
		}
		if label := sanitizeComment(rule.Label); label != "" {
			fmt.Fprintf(&b, "\t\t# %s\n", label)
		}
		for _, family := range []struct {
			match string
			addrs []string
		}{{"ip saddr", v4}, {"ip6 saddr", v6}} {
			if len(family.addrs) == 0 {
				continue
			}
			for _, transport := range []struct {
				proto string
				ports []int
			}{{"tcp", rule.TCP}, {"udp", rule.UDP}} {
				ports := portList(transport.ports)
				if ports == "" {
					continue
				}
				fmt.Fprintf(&b, "\t\t%s { %s } %s dport { %s } drop\n",
					family.match, strings.Join(family.addrs, ", "), transport.proto, ports)
			}
		}
	}
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}

func portList(ports []int) string {
	seen := make(map[int]struct{}, len(ports))
	var unique []int
	for _, port := range ports {
		if port < 1 || port > 65535 {
			continue
		}
		if _, dup := seen[port]; dup {
			continue
		}
		seen[port] = struct{}{}
		unique = append(unique, port)
	}
	sort.Ints(unique)
	parts := make([]string, len(unique))
	for i, port := range unique {
		parts[i] = fmt.Sprint(port)
	}
	return strings.Join(parts, ", ")
}

// sanitizeComment keeps a label from breaking out of its comment line.
func sanitizeComment(label string) string {
	label = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 {
			return ' '
		}
		return r
	}, label)
	return strings.TrimSpace(label)
}

func statePath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, stateFile)
}

// Load reads this node's ban rule set. A node that has never been told about a
// ban reads as an empty configuration rather than an error.
func Load(layout paths.Layout) (Config, error) {
	raw, err := os.ReadFile(statePath(layout))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read IP ban configuration: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse IP ban configuration: %w", err)
	}
	return cfg, nil
}

// Save persists this node's ban rule set, removing the file entirely when
// nothing is banned.
func Save(layout paths.Layout, cfg Config) error {
	path := statePath(layout)
	if cfg.Empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove IP ban configuration: %w", err)
		}
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create IP ban state directory: %w", err)
	}
	return state.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}
