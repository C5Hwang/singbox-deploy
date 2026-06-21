package wireguard

import (
	"fmt"
	"strings"
)

// MasterConfig describes the master's [Interface] section and all node peers.
type MasterConfig struct {
	PrivateKey string
	ListenPort int
	Peers      []Peer
}

// NodeConfig describes a node's [Interface] section and its single master peer.
type NodeConfig struct {
	PrivateKey      string
	IP              string
	MasterPublicKey string
	MasterEndpoint  string // host:port reachable from the node, e.g. "1.2.3.4:51820"
}

// RenderMaster returns the wg-quick config body for the master.
func RenderMaster(cfg MasterConfig) (string, error) {
	if cfg.PrivateKey == "" {
		return "", fmt.Errorf("master private key is empty")
	}
	port := cfg.ListenPort
	if port == 0 {
		port = DefaultListenPort
	}
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", cfg.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", MasterCIDR)
	fmt.Fprintf(&b, "ListenPort = %d\n", port)
	for _, peer := range SortPeersByIP(cfg.Peers) {
		if err := peer.Validate(); err != nil {
			return "", fmt.Errorf("peer %s: %w", peer.Alias, err)
		}
		b.WriteString("\n[Peer]")
		if alias := strings.TrimSpace(peer.Alias); alias != "" {
			fmt.Fprintf(&b, " # %s", alias)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", peer.PublicKey)
		fmt.Fprintf(&b, "AllowedIPs = %s/32\n", peer.IP)
	}
	return b.String(), nil
}

// RenderNode returns the wg-quick config body for a node connecting back to
// the master.
func RenderNode(cfg NodeConfig) (string, error) {
	if cfg.PrivateKey == "" {
		return "", fmt.Errorf("node private key is empty")
	}
	if !ValidNodeIP(cfg.IP) {
		return "", fmt.Errorf("node ip %q outside node range", cfg.IP)
	}
	if cfg.MasterPublicKey == "" {
		return "", fmt.Errorf("master public key is empty")
	}
	if strings.TrimSpace(cfg.MasterEndpoint) == "" {
		return "", fmt.Errorf("master endpoint is empty")
	}
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", cfg.PrivateKey)
	fmt.Fprintf(&b, "Address = %s/24\n", cfg.IP)
	b.WriteString("\n[Peer] # master\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", cfg.MasterPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", cfg.MasterEndpoint)
	fmt.Fprintf(&b, "AllowedIPs = %s/32\n", MasterIP)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", PersistentKeepalive)
	return b.String(), nil
}
