package relay

import (
	"net"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

const (
	// PingTargetKind marks the relay-to-landing probes apart from the fixed
	// carrier list, so the dashboard can give them their own panel.
	PingTargetKind = "relay"

	// pingTargetIDPrefix namespaces a landing node's probe. Registry IDs are
	// hex, so a prefixed one can never collide with a carrier target ID.
	pingTargetIDPrefix = "relay:"

	// pingProbePort is the port the probe connects to on the landing node.
	// Every managed node serves its masquerade site over HTTPS there, so it is
	// always answering, and the connection travels exactly the route the
	// forwarded packets take. The forwarded ports themselves are deliberately
	// not probed: most of them are UDP, and opening a bare TCP connection to a
	// protocol port would only show up as a failed handshake in its logs.
	pingProbePort = 443
)

// PingTargetID returns the probe identity for one landing node.
func PingTargetID(nodeID string) string {
	return pingTargetIDPrefix + strings.ToLower(strings.TrimSpace(nodeID))
}

// PingTargets returns the latency probe destinations for the relay at layout:
// one per landing node it fronts. The stored job is read on every call, so a
// link the hub adds or withdraws is reflected on the next round rather than at
// the next monitor restart. A node that relays for nobody contributes none.
func PingTargets(layout paths.Layout) func() []monitor.PingTarget {
	return func() []monitor.PingTarget {
		cfg, err := Load(layout)
		if err != nil {
			return nil
		}
		targets := make([]monitor.PingTarget, 0, len(cfg.Landings))
		for _, landing := range cfg.Landings {
			host := strings.TrimSpace(landing.Host)
			if host == "" || strings.TrimSpace(landing.NodeID) == "" {
				continue
			}
			targets = append(targets, monitor.PingTarget{
				ID:      PingTargetID(landing.NodeID),
				Kind:    PingTargetKind,
				Name:    landing.DisplayName(),
				Address: net.JoinHostPort(host, strconv.Itoa(pingProbePort)),
			})
		}
		return targets
	}
}
