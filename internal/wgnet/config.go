package wgnet

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const (
	// InterfaceName is the WireGuard interface name for the overlay. wg-quick
	// derives the config path /etc/wireguard/<InterfaceName>.conf from it.
	InterfaceName = "sbwg0"
	// DefaultSubnet is the overlay address range. The hub takes the first host.
	DefaultSubnet = "10.90.0.0/24"
	// DefaultListenPort is the UDP port the hub's WireGuard endpoint listens on.
	DefaultListenPort = 51820
	// HubAddress is the hub's fixed overlay address (first host in DefaultSubnet).
	HubAddress = "10.90.0.1"
	// KeepaliveSeconds keeps spoke→hub NAT/firewall state alive so the hub can
	// reach a spoke that sits behind NAT.
	KeepaliveSeconds = 25
)

// Peer is one WireGuard peer entry rendered into a hub config or applied live.
type Peer struct {
	PublicKey string // base64 peer public key
	AllowedIP string // the peer's /32 overlay address, e.g. 10.90.0.2/32
	Endpoint  string // optional host:port; empty for spokes reached over the tunnel
}

// HubConfig is the input to the hub's wg-quick configuration.
type HubConfig struct {
	PrivateKey string
	Address    string // hub overlay address with prefix, e.g. 10.90.0.1/24
	ListenPort int
	Peers      []Peer
}

// SpokeConfig is the input to a spoke's wg-quick configuration.
type SpokeConfig struct {
	PrivateKey   string
	Address      string // spoke overlay address with prefix, e.g. 10.90.0.2/24
	HubPublicKey string
	HubEndpoint  string // hub public host:port the spoke dials
	Subnet       string // overlay CIDR routed to the hub, e.g. 10.90.0.0/24
}

// RenderHubConfig renders the hub's /etc/wireguard/sbwg0.conf. Peers are sorted
// by allowed IP so the file is stable across renders (clean diffs on change).
func RenderHubConfig(c HubConfig) string {
	port := c.ListenPort
	if port <= 0 {
		port = DefaultListenPort
	}
	var b strings.Builder
	b.WriteString("# Managed by singbox-deploy. Do not edit by hand.\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", c.Address)
	fmt.Fprintf(&b, "ListenPort = %d\n", port)
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)

	peers := append([]Peer(nil), c.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].AllowedIP < peers[j].AllowedIP })
	for _, p := range peers {
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		fmt.Fprintf(&b, "AllowedIPs = %s\n", p.AllowedIP)
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
	}
	return b.String()
}

// RenderSpokeConfig renders a spoke's /etc/wireguard/sbwg0.conf. The single
// peer is the hub, reachable at its public endpoint; the spoke routes the whole
// overlay subnet through it and uses a keepalive so the hub can dial back.
func RenderSpokeConfig(c SpokeConfig) string {
	subnet := c.Subnet
	if subnet == "" {
		subnet = DefaultSubnet
	}
	var b strings.Builder
	b.WriteString("# Managed by singbox-deploy. Do not edit by hand.\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", c.Address)
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", c.HubPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", c.HubEndpoint)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", subnet)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", KeepaliveSeconds)
	return b.String()
}

// AllocateSpokeIP returns the lowest unused host address in subnet, skipping the
// network address, the hub's first host, and any address in used. The returned
// value is a bare address (no prefix).
func AllocateSpokeIP(subnet string, used []string) (string, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet %q: %w", subnet, err)
	}
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("subnet %q must be IPv4", subnet)
	}
	taken := map[netip.Addr]bool{}
	for _, u := range used {
		if a, err := netip.ParseAddr(strings.TrimSpace(u)); err == nil {
			taken[a] = true
		}
	}
	// The first host is the hub; the last host is reserved as broadcast.
	network := prefix.Addr()
	addr := network.Next() // .1, the hub
	taken[addr] = true     // never hand out the hub address
	for addr = addr.Next(); prefix.Contains(addr); addr = addr.Next() {
		next := addr.Next()
		if !prefix.Contains(next) {
			// addr is the broadcast address; stop before it.
			break
		}
		if !taken[addr] {
			return addr.String(), nil
		}
	}
	return "", fmt.Errorf("no free address in subnet %s", subnet)
}

// WithPrefix appends the subnet's prefix length to a bare address, e.g.
// WithPrefix("10.90.0.2", "10.90.0.0/24") -> "10.90.0.2/24".
func WithPrefix(addr, subnet string) (string, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet %q: %w", subnet, err)
	}
	if _, err := netip.ParseAddr(addr); err != nil {
		return "", fmt.Errorf("parse address %q: %w", addr, err)
	}
	return fmt.Sprintf("%s/%d", addr, prefix.Bits()), nil
}
