// Package cluster manages the master's registry of remote nodes that join the
// internal WireGuard network. Each registered node has a directory under
// state/nodes/<ID>/ holding its identity, key material, protocol credentials,
// per-node monitor config, and shared-secret API token. Persisted as one file
// per field so an operator can inspect or edit individual values.
package cluster

import (
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// Node captures everything the master knows about a remote node. It carries
// enough information to (a) generate subscription entries pointing at the node
// and (b) drive the node agent via the internal HTTP API.
type Node struct {
	ID                   string // 3-digit zero-padded dir name, e.g. "001"
	Alias                string // human label shown in TUI and subscription
	PublicIP             string // public IP or hostname (for WireGuard endpoint, if applicable)
	Domain               string // public TLS domain, used as subscription server address
	WGIP                 string // assigned internal WireGuard IP
	WGPublicKey          string // node's WireGuard public key (master keeps only the public key)
	APIToken             string // shared bearer secret for the node agent HTTP API
	EnabledProtocols     []config.Protocol
	Ports                config.Ports
	Creds                deploy.Credentials
	RealityServerName    string
	RealityHandshakePort int

	// Per-node monitor configuration. Quotas are enforced locally by the
	// node's monitor; the master only configures them and aggregates samples.
	// MonitorEnabled gates whether the node's monitor agent samples and
	// reports at all — when false the master pushes Disabled=true on every
	// reconfigure so the node tears its monitor service down. MonitorAlias is
	// the master-side display name shown on the dashboard; it is independent
	// of Alias (the management label) so operators can rename the dashboard
	// entry without renaming the node.
	MonitorEnabled         bool
	MonitorAlias           string
	MonitorInterface       string
	MonitorIntervalSeconds int
	TrafficInLimitBytes    uint64
	TrafficOutLimitBytes   uint64
	TrafficTotalLimitBytes uint64
	ResetDay               int
	ResetHour              int
}

// MonitorDisplayName returns the label the master's monitor dashboard should
// use for this node — the explicit MonitorAlias when set, otherwise the
// management Alias (and Domain as a final fallback so the entry is never
// nameless).
func (n Node) MonitorDisplayName() string {
	if v := strings.TrimSpace(n.MonitorAlias); v != "" {
		return v
	}
	if n.Alias != "" {
		return n.Alias
	}
	return n.Domain
}

// SubscriptionDisplayName returns the alias shown on subscription entries.
// Empty alias falls back to the domain so entries always carry a readable
// label.
func (n Node) SubscriptionDisplayName() string {
	if n.Alias != "" {
		return n.Alias
	}
	return n.Domain
}

// APIBaseURL returns the agent HTTP base reachable over the WireGuard subnet.
// The node agent always binds to its WG IP on the well-known agent port.
func (n Node) APIBaseURL() string {
	return "http://" + n.WGIP + ":19091"
}

// MonitorAPIURL returns the node monitor's summary endpoint reachable over
// the WireGuard subnet (port 19090 on the WG interface).
func (n Node) MonitorAPIURL() string {
	return "http://" + n.WGIP + ":19090/api/summary"
}

// HasTLSProtocol reports whether the node needs a TLS certificate (any of
// Hysteria2, TUIC, AnyTLS — protocols that terminate TLS via the certificate
// instead of Reality).
func (n Node) HasTLSProtocol() bool {
	for _, p := range n.EnabledProtocols {
		switch p {
		case config.ProtocolHysteria2, config.ProtocolTUIC, config.ProtocolAnyTLS:
			return true
		}
	}
	return false
}
