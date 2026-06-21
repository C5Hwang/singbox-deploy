// Package wireguard manages the internal hub-spoke WireGuard network that
// connects the master (10.10.0.1) to its remote nodes (10.10.0.2+). The master
// owns all key material: it generates each node's key pair at add-time and
// pushes the private key to the node over SSH during initial provisioning.
package wireguard

import "fmt"

// InterfaceName is the WireGuard interface name shared by master and nodes.
const InterfaceName = "wg-sdeploy"

// SubnetCIDR is the internal /24 subnet allocated to the cluster.
const SubnetCIDR = "10.10.0.0/24"

// MasterIP is the fixed master address inside the subnet.
const MasterIP = "10.10.0.1"

// MasterCIDR is the master address with subnet mask, used in [Interface].
const MasterCIDR = MasterIP + "/24"

// NodeFirstIP is the first IP allocatable to a node.
const NodeFirstIP = "10.10.0.2"

// NodeLastIP is the last IP allocatable to a node (10.10.0.254).
const NodeLastIP = "10.10.0.254"

// DefaultListenPort is the master WireGuard UDP listen port.
const DefaultListenPort = 51820

// ConfigPath is the on-disk path of the wg-quick config for the cluster.
// Exposed as a variable so tests can redirect it to a temp directory.
var ConfigPath = "/etc/wireguard/" + InterfaceName + ".conf"

// PersistentKeepalive is the keepalive interval nodes use to keep the NAT
// pinhole open back to the master.
const PersistentKeepalive = 25

// Peer describes one remote node inside the master's WireGuard config.
type Peer struct {
	Alias     string // human label, written as a comment in the config
	PublicKey string // base64-encoded Curve25519 public key
	IP        string // assigned internal IP, e.g. "10.10.0.2"
}

// Validate checks the peer fields are well-formed.
func (p Peer) Validate() error {
	if p.PublicKey == "" {
		return fmt.Errorf("peer public key is empty")
	}
	if p.IP == "" {
		return fmt.Errorf("peer ip is empty")
	}
	if !ValidNodeIP(p.IP) {
		return fmt.Errorf("peer ip %q outside node range", p.IP)
	}
	return nil
}
