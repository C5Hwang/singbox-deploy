// Package relaylinks is the hub's registry of relay links. One link fronts a
// single landing node with a single relay node: the relay listens on a
// generated port per protocol and forwards packets to the landing node's own
// listen port without unwrapping them, so TLS still terminates on the landing
// node and its credentials are unchanged.
//
// A landing node is the registry key, so it has at most one relay. One relay
// may front several landing nodes. Chaining is refused in both directions: a
// node that already relays for someone cannot itself be relayed, and a node
// that is already fronted cannot relay for anyone. That keeps every published
// endpoint exactly one hop from its landing node, which is what lets the
// subscription rewrite and the quota fallback stay a single lookup.
//
// Each link is one numbered directory of small state files under
// state/relay_links/, using the same entry-tree machinery as the spoke and
// subscription-group registries.
package relaylinks

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	linksDir = "relay_links"

	// HubNodeID names the hub's own deployment where a link references it as a
	// landing or a relay. Spokes are stored as their stable registry IDs, which
	// are hex and can therefore never collide with this literal. It matches the
	// convention subgroups uses for hub membership.
	HubNodeID = "hub"

	// minRelayPort and maxRelayPort bound generated relay listen ports. The
	// range matches the one protocol ports are drawn from, so a relay port is
	// indistinguishable from a directly served one.
	minRelayPort = 20000
	maxRelayPort = 59999
)

// Forward is one protocol's port mapping on the relay node.
type Forward struct {
	// Protocol names the landing node's protocol this mapping fronts. The
	// subscription rewrite matches on it, so it survives a port change on
	// either side.
	Protocol config.Protocol
	// Network is the transport the protocol listens on: "tcp" or "udp". It
	// decides which nftables rule and which firewall port is opened.
	Network string
	// RelayPort is the generated port the relay listens on.
	RelayPort int
	// TargetPort is the landing node's own listen port for this protocol.
	TargetPort int
}

// Link fronts one landing node with one relay node.
type Link struct {
	// LandingID is the fronted node: HubNodeID or a stable spoke registry ID.
	LandingID string
	// RelayID is the node that forwards to it.
	RelayID string
	// Forwards is one mapping per protocol the landing node serves.
	Forwards []Forward
}

// Target is one landing-node protocol a caller wants relay ports generated for.
type Target struct {
	Protocol config.Protocol
	Port     int
}

// NetworkFor returns the transport a protocol listens on. It mirrors the
// firewall port list the installer opens, so a relay opens exactly the
// protocols the landing node serves.
func NetworkFor(protocol config.Protocol) (string, bool) {
	switch protocol {
	case config.ProtocolRealityVision, config.ProtocolRealityGRPC, config.ProtocolAnyTLS:
		return "tcp", true
	case config.ProtocolHysteria2, config.ProtocolTUIC:
		return "udp", true
	default:
		return "", false
	}
}

func linksPath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, linksDir)
}

// Load reads every relay link in saved order.
func Load(layout paths.Layout) ([]Link, error) {
	return state.LoadEntryDirs(linksPath(layout), decodeLink)
}

// Save persists the link list, one directory per link.
func Save(layout paths.Layout, list []Link) error {
	return state.SaveEntryDirs(linksPath(layout), list, encodeLink)
}

// Set stores link, replacing whatever relay the landing node had before. The
// whole list is validated under the registry transaction lock, so two operators
// cannot each pass validation against a registry the other is about to change.
func Set(layout paths.Layout, link Link) error {
	link = link.normalized()
	_, err := transact(layout, func(list []Link) ([]Link, error) {
		match := indexOf(list, link.LandingID)
		if err := validate(list, link, match); err != nil {
			return nil, err
		}
		if match < 0 {
			return append(list, link), nil
		}
		list[match] = link
		return list, nil
	})
	return err
}

// Remove deletes the link fronting landingID. It is idempotent when the landing
// node has no relay.
func Remove(layout paths.Layout, landingID string) error {
	landingID = normalize(landingID)
	_, err := transact(layout, func(list []Link) ([]Link, error) {
		match := indexOf(list, landingID)
		if match < 0 {
			return list, nil
		}
		return append(list[:match], list[match+1:]...), nil
	})
	return err
}

// DropNode removes every link a node takes part in, on either side. It is
// called when a node leaves the fleet so a later node cannot inherit a relay
// through a recycled ID, and so no link ever names a node that is gone. The
// relays that lose a landing node are returned, since each of them still has
// forwarding rules installed that the caller has to withdraw.
func DropNode(layout paths.Layout, nodeID string) ([]string, error) {
	nodeID = normalize(nodeID)
	if nodeID == "" {
		return nil, nil
	}
	var orphaned []string
	_, err := transact(layout, func(list []Link) ([]Link, error) {
		orphaned = nil
		kept := make([]Link, 0, len(list))
		seen := make(map[string]struct{}, len(list))
		for _, link := range list {
			if link.LandingID == nodeID {
				if _, dup := seen[link.RelayID]; !dup && link.RelayID != nodeID {
					seen[link.RelayID] = struct{}{}
					orphaned = append(orphaned, link.RelayID)
				}
				continue
			}
			if link.RelayID == nodeID {
				continue
			}
			kept = append(kept, link)
		}
		return kept, nil
	})
	if err != nil {
		return nil, err
	}
	return orphaned, nil
}

// Find returns the link fronting landingID.
func Find(list []Link, landingID string) (Link, bool) {
	if match := indexOf(list, normalize(landingID)); match >= 0 {
		return list[match], true
	}
	return Link{}, false
}

// ServedBy returns every link relayID forwards, in registry order.
func ServedBy(list []Link, relayID string) []Link {
	relayID = normalize(relayID)
	if relayID == "" {
		return nil
	}
	var out []Link
	for _, link := range list {
		if link.RelayID == relayID {
			out = append(out, link)
		}
	}
	return out
}

// IsLanding reports whether nodeID is fronted by a relay.
func IsLanding(list []Link, nodeID string) bool {
	return indexOf(list, normalize(nodeID)) >= 0
}

// IsRelay reports whether nodeID forwards for at least one landing node.
func IsRelay(list []Link, nodeID string) bool {
	return len(ServedBy(list, nodeID)) > 0
}

// AllocateForwards generates a relay listen port for each target. Ports already
// used by another landing node on the same relay, and the reserved ports the
// caller names (the relay's own protocol, subscription, monitor and overlay
// ports), are avoided, so a generated port never collides with something the
// relay already answers on.
func AllocateForwards(list []Link, relayID string, reserved []int, targets []Target) ([]Forward, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("a relay link needs at least one protocol to forward")
	}
	used := make(map[int]bool, len(reserved)+4*len(list))
	for _, port := range reserved {
		if port > 0 {
			used[port] = true
		}
	}
	for _, link := range ServedBy(list, relayID) {
		for _, forward := range link.Forwards {
			used[forward.RelayPort] = true
		}
	}
	forwards := make([]Forward, 0, len(targets))
	for _, target := range targets {
		network, ok := NetworkFor(target.Protocol)
		if !ok {
			return nil, fmt.Errorf("cannot relay unsupported protocol %q", target.Protocol)
		}
		if target.Port < 1 || target.Port > 65535 {
			return nil, fmt.Errorf("%s listen port %d is out of range on the landing node", target.Protocol, target.Port)
		}
		port, err := randomRelayPort(used)
		if err != nil {
			return nil, err
		}
		forwards = append(forwards, Forward{
			Protocol:   target.Protocol,
			Network:    network,
			RelayPort:  port,
			TargetPort: target.Port,
		})
	}
	return forwards, nil
}

// Validate reports whether link could be stored against the current registry.
// Forms use it to refuse a selection before anything is provisioned.
func Validate(list []Link, link Link) error {
	link = link.normalized()
	return validate(list, link, indexOf(list, link.LandingID))
}

// validate checks link against every other entry. skip is the index link
// replaces, or -1 when it is new.
func validate(list []Link, link Link, skip int) error {
	if link.LandingID == "" {
		return fmt.Errorf("a relay link needs a landing node")
	}
	if link.RelayID == "" {
		return fmt.Errorf("a relay link needs a relay node")
	}
	if link.LandingID == link.RelayID {
		return fmt.Errorf("a node cannot relay for itself")
	}
	if len(link.Forwards) == 0 {
		return fmt.Errorf("a relay link needs at least one forwarded protocol")
	}
	claimed := make(map[int]struct{}, len(link.Forwards))
	seenProtocol := make(map[config.Protocol]struct{}, len(link.Forwards))
	for _, forward := range link.Forwards {
		network, ok := NetworkFor(forward.Protocol)
		if !ok {
			return fmt.Errorf("cannot relay unsupported protocol %q", forward.Protocol)
		}
		if forward.Network != network {
			return fmt.Errorf("%s is a %s protocol but its forward names %q", forward.Protocol, network, forward.Network)
		}
		if _, duplicate := seenProtocol[forward.Protocol]; duplicate {
			return fmt.Errorf("protocol %s is forwarded twice in one relay link", forward.Protocol)
		}
		seenProtocol[forward.Protocol] = struct{}{}
		if forward.RelayPort < 1 || forward.RelayPort > 65535 {
			return fmt.Errorf("%s relay port must be between 1 and 65535", forward.Protocol)
		}
		if forward.TargetPort < 1 || forward.TargetPort > 65535 {
			return fmt.Errorf("%s landing port must be between 1 and 65535", forward.Protocol)
		}
		if _, duplicate := claimed[forward.RelayPort]; duplicate {
			return fmt.Errorf("relay port %d is claimed twice in one relay link", forward.RelayPort)
		}
		claimed[forward.RelayPort] = struct{}{}
	}

	for i, existing := range list {
		if i == skip {
			continue
		}
		if existing.LandingID == link.LandingID {
			return fmt.Errorf("node %s already has a relay", link.LandingID)
		}
		// Chaining is refused in both directions, so an endpoint is always
		// exactly one hop from the node that terminates its TLS.
		if existing.RelayID == link.LandingID {
			return fmt.Errorf("node %s already relays for another node, so it cannot be relayed itself", link.LandingID)
		}
		if existing.LandingID == link.RelayID {
			return fmt.Errorf("node %s is already relayed, so it cannot relay for another node", link.RelayID)
		}
		if existing.RelayID != link.RelayID {
			continue
		}
		for _, forward := range existing.Forwards {
			if _, clash := claimed[forward.RelayPort]; clash {
				return fmt.Errorf("relay port %d is already forwarding for node %s", forward.RelayPort, existing.LandingID)
			}
		}
	}
	return nil
}

func transact(layout paths.Layout, mutate func([]Link) ([]Link, error)) ([]Link, error) {
	return state.TransactEntryDirs(linksPath(layout), decodeLink, encodeLink, mutate)
}

func indexOf(list []Link, landingID string) int {
	if landingID == "" {
		return -1
	}
	for i := range list {
		if list[i].LandingID == landingID {
			return i
		}
	}
	return -1
}

func (l Link) normalized() Link {
	l.LandingID = normalize(l.LandingID)
	l.RelayID = normalize(l.RelayID)
	return l
}

func normalize(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// randomRelayPort claims an unused port and records it in used. It starts at a
// random offset and walks the range from there, so the choice is unpredictable
// but the search still finds the last free port on a relay that is already
// forwarding a large fleet.
func randomRelayPort(used map[int]bool) (int, error) {
	const span = maxRelayPort - minRelayPort + 1
	offset, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, err
	}
	start := int(offset.Int64())
	for i := range span {
		port := minRelayPort + (start+i)%span
		if !used[port] {
			used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("every port between %d and %d is already in use on this relay", minRelayPort, maxRelayPort)
}

func decodeLink(root string) Link {
	get := func(name string) string { return state.ReadEntryValue(root, name, "") }
	return Link{
		LandingID: normalize(get("landing_id")),
		RelayID:   normalize(get("relay_id")),
		Forwards:  decodeForwards(get("forwards")),
	}
}

func encodeLink(l Link) map[string]string {
	return map[string]string{
		"landing_id": normalize(l.LandingID),
		"relay_id":   normalize(l.RelayID),
		"forwards":   encodeForwards(l.Forwards),
	}
}

// decodeForwards parses the compact "protocol:network:relayPort:targetPort"
// records the registry stores one per comma. A malformed record is dropped
// rather than failing the whole read: validation refuses a link with no usable
// forward, which is a clearer failure than a registry that will not load.
func decodeForwards(value string) []Forward {
	var out []Forward
	for _, record := range strings.Split(value, ",") {
		fields := strings.Split(strings.TrimSpace(record), ":")
		if len(fields) != 4 {
			continue
		}
		relayPort, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		targetPort, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}
		out = append(out, Forward{
			Protocol:   config.Protocol(fields[0]),
			Network:    fields[1],
			RelayPort:  relayPort,
			TargetPort: targetPort,
		})
	}
	return out
}

func encodeForwards(forwards []Forward) string {
	records := make([]string, 0, len(forwards))
	for _, f := range forwards {
		records = append(records, fmt.Sprintf("%s:%s:%d:%d", f.Protocol, f.Network, f.RelayPort, f.TargetPort))
	}
	return strings.Join(records, ",")
}
