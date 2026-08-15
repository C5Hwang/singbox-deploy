// Package relay is the node-local data plane of a relay node. A relay listens
// on a generated port per fronted protocol and hands the packets to the landing
// node's own listen port with an nftables DNAT, so nothing is ever unwrapped:
// TLS still terminates on the landing node and the client's credentials are
// unchanged.
//
// The hub decides which node relays for which — see internal/relaylinks — and
// pushes the resulting Config here. This package only installs it, so the same
// code runs whether the relay is the hub or a spoke.
package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const (
	// Table is the nftables table the forwarding rules live in. It is a table
	// of this deployment's own, so applying it never disturbs the rules ufw or
	// firewalld manage.
	Table = "singbox_deploy_relay"

	// stateFile holds this node's relay configuration. It is what the boot-time
	// unit reapplies from, and what the monitor reads its relay probe targets
	// from, so it is the single node-local record of the relay's job.
	stateFile = "relay.json"
)

// Forward is one fronted protocol's port mapping.
type Forward struct {
	// Protocol names the landing node's protocol this mapping fronts. It is
	// carried for display and for the subscription rewrite that pairs with it.
	Protocol string `json:"protocol"`
	// Network is the transport to match: "tcp" or "udp".
	Network string `json:"network"`
	// ListenPort is the generated port this relay answers on.
	ListenPort int `json:"listenPort"`
	// TargetPort is the landing node's own listen port for that protocol.
	TargetPort int `json:"targetPort"`
}

// Landing is one node this relay fronts, and every port mapping to it.
type Landing struct {
	// NodeID is the landing node's stable hub registry identity. It ties the
	// forwards and the latency probes back to one node in the dashboard.
	NodeID string `json:"nodeID"`
	// Name is the landing node's display alias, shown on the relay latency
	// panel. It is a label only; nothing is keyed on it.
	Name string `json:"name"`
	// Host is the landing node's public hostname. It is resolved on every
	// apply, so a landing node that changes address is followed by reapplying
	// rather than by re-provisioning the link.
	Host string `json:"host"`
	// Address is the IPv4 address the hub resolved when the link was created.
	// It is the fallback used when the relay itself cannot resolve Host, so a
	// resolver outage at boot does not take the forwarding down.
	Address  string    `json:"address,omitempty"`
	Forwards []Forward `json:"forwards"`
}

// Config is everything this node forwards.
type Config struct {
	Landings []Landing `json:"landings"`
}

// Empty reports whether this node forwards nothing, in which case the ruleset
// and the systemd unit are removed rather than installed empty.
func (c Config) Empty() bool {
	for _, landing := range c.Landings {
		if len(landing.Forwards) > 0 {
			return false
		}
	}
	return true
}

// ListenPorts returns every port this relay answers on, for the firewall.
func (c Config) ListenPorts() []system.Port {
	var ports []system.Port
	for _, landing := range c.Landings {
		for _, forward := range landing.Forwards {
			ports = append(ports, system.Port{
				Number: forward.ListenPort,
				Proto:  forward.Network,
				Label:  "relay " + forward.Protocol + " to " + landing.displayName(),
			})
		}
	}
	return ports
}

// Validate rejects a configuration that could not be turned into a ruleset.
func (c Config) Validate() error {
	claimed := make(map[string]string, 8)
	seen := make(map[string]struct{}, len(c.Landings))
	for _, landing := range c.Landings {
		if strings.TrimSpace(landing.NodeID) == "" {
			return fmt.Errorf("a relay landing needs a node ID")
		}
		if _, duplicate := seen[landing.NodeID]; duplicate {
			return fmt.Errorf("landing node %s appears twice in the relay configuration", landing.NodeID)
		}
		seen[landing.NodeID] = struct{}{}
		if strings.TrimSpace(landing.Host) == "" {
			return fmt.Errorf("landing node %s needs a hostname to forward to", landing.displayName())
		}
		if len(landing.Forwards) == 0 {
			return fmt.Errorf("landing node %s has no forwarded protocol", landing.displayName())
		}
		for _, forward := range landing.Forwards {
			if forward.Network != "tcp" && forward.Network != "udp" {
				return fmt.Errorf("%s forward for %s names transport %q, which is neither tcp nor udp",
					forward.Protocol, landing.displayName(), forward.Network)
			}
			if forward.ListenPort < 1 || forward.ListenPort > 65535 {
				return fmt.Errorf("relay listen port %d for %s is out of range", forward.ListenPort, landing.displayName())
			}
			if forward.TargetPort < 1 || forward.TargetPort > 65535 {
				return fmt.Errorf("landing port %d for %s is out of range", forward.TargetPort, landing.displayName())
			}
			// One socket cannot serve two landing nodes, and nftables would
			// silently let the first matching rule win.
			key := forward.Network + "/" + fmt.Sprint(forward.ListenPort)
			if owner, clash := claimed[key]; clash {
				return fmt.Errorf("relay port %d/%s is claimed by both %s and %s",
					forward.ListenPort, forward.Network, owner, landing.displayName())
			}
			claimed[key] = landing.displayName()
		}
	}
	return nil
}

func (l Landing) displayName() string {
	if name := strings.TrimSpace(l.Name); name != "" {
		return name
	}
	if host := strings.TrimSpace(l.Host); host != "" {
		return host
	}
	return strings.TrimSpace(l.NodeID)
}

// DisplayName returns the landing node's label, falling back to its hostname
// then its node ID.
func (l Landing) DisplayName() string { return l.displayName() }

func statePath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, stateFile)
}

// Load reads this node's relay configuration. A node that has never relayed
// reads as an empty configuration rather than an error.
func Load(layout paths.Layout) (Config, error) {
	raw, err := os.ReadFile(statePath(layout))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read relay configuration: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse relay configuration: %w", err)
	}
	return cfg, nil
}

// Save persists this node's relay configuration, removing the file entirely
// when there is nothing left to forward.
func Save(layout paths.Layout, cfg Config) error {
	path := statePath(layout)
	if cfg.Empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove relay configuration: %w", err)
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
		return fmt.Errorf("create relay state directory: %w", err)
	}
	return state.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}

// ResolvedLanding is one landing node with the address its rules will point at.
type ResolvedLanding struct {
	Landing
	// IP is the landing node's IPv4 address. nftables needs a literal address,
	// so the name is resolved before the ruleset is rendered.
	IP string
}

// Ruleset renders the complete nftables program that installs the forwarding
// rules. The empty table declaration before the delete is the standard
// atomic-replace idiom: it makes the delete succeed whether or not the table
// already exists, so one apply always fully replaces the previous one.
//
// Only the DNATed flows are masqueraded, matched on the address and port they
// were rewritten to. Without the source rewrite the landing node would answer
// the client's own address directly and the reply would never come back
// through this relay.
func Ruleset(landings []ResolvedLanding) string {
	var b strings.Builder
	b.WriteString("table ip " + Table + " {}\n")
	b.WriteString("delete table ip " + Table + "\n\n")
	b.WriteString("table ip " + Table + " {\n")
	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, landing := range landings {
		for _, f := range sortedForwards(landing.Forwards) {
			fmt.Fprintf(&b, "\t\t%s dport %d dnat to %s:%d\n", f.Network, f.ListenPort, landing.IP, f.TargetPort)
		}
	}
	b.WriteString("\t}\n\n")
	b.WriteString("\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, landing := range landings {
		for _, f := range sortedForwards(landing.Forwards) {
			fmt.Fprintf(&b, "\t\tip daddr %s %s dport %d masquerade\n", landing.IP, f.Network, f.TargetPort)
		}
	}
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}

// sortedForwards orders one landing node's mappings by listen port so the
// rendered ruleset is stable across applies and reviewable in a diff.
func sortedForwards(forwards []Forward) []Forward {
	out := append([]Forward(nil), forwards...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ListenPort != out[j].ListenPort {
			return out[i].ListenPort < out[j].ListenPort
		}
		return out[i].Network < out[j].Network
	})
	return out
}
