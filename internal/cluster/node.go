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
//
// Three labels coexist so each consumer can be renamed independently:
//   - Alias              — the primary/TUI label, required at add time
//   - SubscriptionAlias  — what subscription entries display; falls back to Alias
//   - MonitorAlias       — what the master monitor dashboard shows; falls back to Alias
//
// The two override fields are optional. Empty means "follow Alias dynamically" —
// renaming the node later automatically retags subscriptions / dashboard rows
// unless the operator has materialised a static override.
type Node struct {
	ID                   string // 3-digit zero-padded dir name, e.g. "001"
	Alias                string // label shown in the TUI; default for subscription / monitor when no override
	SubscriptionAlias    string // override shown on subscription entries; empty falls back to Alias
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

// SubscriptionDisplayName returns the label subscription entries should carry
// for this node — the explicit SubscriptionAlias when set, otherwise the
// management Alias (Domain as final fallback so entries always have a label).
func (n Node) SubscriptionDisplayName() string {
	if v := strings.TrimSpace(n.SubscriptionAlias); v != "" {
		return v
	}
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
