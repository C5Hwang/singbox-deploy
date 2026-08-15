package hubctl

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// RelayProtocolPort is one protocol a node serves and the port it listens on.
type RelayProtocolPort struct {
	Protocol config.Protocol
	Port     int
}

// RelayEndpoint is one fleet member as the relay screens see it: the ID a relay
// link references it by, its display name, the public hostname a relay forwards
// to, and the listen port of every protocol it serves.
type RelayEndpoint struct {
	// ID is relaylinks.HubNodeID for the hub, or a stable spoke registry ID.
	ID   string
	Name string
	// Domain is the public hostname. A relay resolves it to know where to send
	// packets, and the landing latency probes measure the route to it.
	Domain    string
	Installed bool
	Protocols []RelayProtocolPort
	// ReservedPorts are the ports this node already answers on. A generated
	// relay port avoids them so forwarding never shadows a local service.
	ReservedPorts []int
}

// RelayEndpoints returns every fleet member a relay link can name, the hub
// first and then the spokes in registry order. A hub that is not installed yet
// has no fleet and returns none.
func (c *Controller) RelayEndpoints() ([]RelayEndpoint, error) {
	c.defaults()
	localCfg, err := deploy.LoadProtocolConfig(c.Layout)
	if err != nil {
		return nil, nil
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nil, err
	}
	out := make([]RelayEndpoint, 0, len(list)+1)
	out = append(out, hubRelayEndpoint(c.Layout, localCfg))
	for _, n := range list {
		out = append(out, spokeRelayEndpoint(n))
	}
	return out, nil
}

// RelayEndpointByID finds one endpoint in a list returned by RelayEndpoints.
func RelayEndpointByID(endpoints []RelayEndpoint, id string) (RelayEndpoint, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, endpoint := range endpoints {
		if strings.ToLower(endpoint.ID) == id {
			return endpoint, true
		}
	}
	return RelayEndpoint{}, false
}

func hubRelayEndpoint(layout paths.Layout, cfg deploy.Config) RelayEndpoint {
	reserved := []int{80, 443, cfg.SubscribePort, cfg.MonitorPublicPort, cfg.MonitorPort, nodes.DefaultAgentPort}
	if identity, ok, err := nodes.LoadHubIdentity(layout); err == nil && ok {
		reserved = append(reserved, identity.ListenPort)
	}
	endpoint := RelayEndpoint{
		ID:        relaylinks.HubNodeID,
		Name:      strings.TrimSpace(cfg.DisplayName),
		Domain:    strings.TrimSpace(cfg.Domain),
		Installed: nodes.HubInstalled(layout),
	}
	for _, protocol := range cfg.EnabledProtocols() {
		if port := hubProtocolPort(cfg, protocol); port > 0 {
			endpoint.Protocols = append(endpoint.Protocols, RelayProtocolPort{Protocol: protocol, Port: port})
			reserved = append(reserved, port)
		}
	}
	endpoint.ReservedPorts = reserved
	return endpoint
}

func hubProtocolPort(cfg deploy.Config, protocol config.Protocol) int {
	switch protocol {
	case config.ProtocolRealityVision:
		return cfg.Ports.RealityVision
	case config.ProtocolRealityGRPC:
		return cfg.Ports.RealityGRPC
	case config.ProtocolHysteria2:
		return cfg.Ports.Hysteria2
	case config.ProtocolTUIC:
		return cfg.Ports.TUIC
	case config.ProtocolAnyTLS:
		return cfg.Ports.AnyTLS
	default:
		return 0
	}
}

func spokeRelayEndpoint(n nodes.Node) RelayEndpoint {
	agentPort := n.AgentPort
	if agentPort <= 0 {
		agentPort = nodes.DefaultAgentPort
	}
	reserved := []int{80, 443, deploy.DefaultSubscribePort, n.MonitorPort, agentPort}
	endpoint := RelayEndpoint{
		ID:        n.ID,
		Name:      n.EffectiveAlias(),
		Domain:    strings.TrimSpace(n.Domain),
		Installed: n.Installed,
	}
	for _, protocol := range n.EnabledProtocols {
		port := spokeProtocolPort(n, config.Protocol(protocol))
		if port <= 0 {
			continue
		}
		endpoint.Protocols = append(endpoint.Protocols, RelayProtocolPort{Protocol: config.Protocol(protocol), Port: port})
		reserved = append(reserved, port)
	}
	endpoint.ReservedPorts = reserved
	return endpoint
}

func spokeProtocolPort(n nodes.Node, protocol config.Protocol) int {
	switch protocol {
	case config.ProtocolRealityVision:
		return n.RealityVisionPort
	case config.ProtocolRealityGRPC:
		return n.RealityGRPCPort
	case config.ProtocolHysteria2:
		return n.Hysteria2Port
	case config.ProtocolTUIC:
		return n.TUICPort
	case config.ProtocolAnyTLS:
		return n.AnyTLSPort
	default:
		return 0
	}
}

// RelayConfigFor assembles the complete data-plane job relayNodeID performs,
// from the relay registry and each landing node's current address. It is the
// full job rather than one link, because a relay's ruleset is replaced whole.
func (c *Controller) RelayConfigFor(relayNodeID string, links []relaylinks.Link, endpoints []RelayEndpoint) (relay.Config, error) {
	c.defaults()
	var cfg relay.Config
	for _, link := range relaylinks.ServedBy(links, relayNodeID) {
		landing, ok := RelayEndpointByID(endpoints, link.LandingID)
		if !ok {
			return relay.Config{}, fmt.Errorf("relay link names landing node %s, which is no longer in the fleet", link.LandingID)
		}
		if landing.Domain == "" {
			return relay.Config{}, fmt.Errorf("landing node %s has no domain to forward to", landing.Name)
		}
		forwards := make([]relay.Forward, 0, len(link.Forwards))
		for _, f := range link.Forwards {
			forwards = append(forwards, relay.Forward{
				Protocol:   string(f.Protocol),
				Network:    f.Network,
				ListenPort: f.RelayPort,
				TargetPort: f.TargetPort,
			})
		}
		cfg.Landings = append(cfg.Landings, relay.Landing{
			NodeID:   link.LandingID,
			Name:     landing.Name,
			Host:     landing.Domain,
			Address:  c.ResolveHostIPv4(landing.Domain),
			Forwards: forwards,
		})
	}
	return cfg, nil
}

// resolveHostIPv4 records the landing node's address at the moment the link is
// provisioned. The relay resolves the name itself on every apply and only falls
// back to this, so an unresolvable name here is not fatal.
func resolveHostIPv4(domain string) string {
	if ip := net.ParseIP(domain); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ""
	}
	addrs, err := net.LookupIP(domain)
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if v4 := addr.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// ApplyRelay installs relayNodeID's complete relay job. The hub applies its own
// locally; a spoke's is pushed over the authenticated overlay API. Passing an
// empty configuration retires the node's data plane, which is how a relay that
// lost its last landing node stops forwarding.
func (c *Controller) ApplyRelay(ctx context.Context, relayNodeID string, cfg relay.Config, log io.Writer) error {
	c.defaults()
	if strings.EqualFold(strings.TrimSpace(relayNodeID), relaylinks.HubNodeID) {
		applier := &relay.Applier{
			Layout:   c.Layout,
			Firewall: system.DetectFirewall(),
			Runner:   c.Runner,
		}
		return applier.Apply(ctx, cfg)
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	for _, n := range list {
		if !strings.EqualFold(n.ID, relayNodeID) {
			continue
		}
		if !n.Installed {
			return fmt.Errorf("node %s is not installed and cannot relay", n.EffectiveAlias())
		}
		checked, err := c.ProbeHealth(ctx, n)
		if err != nil {
			return fmt.Errorf("check agent %s before applying its relay: %w", n.EffectiveAlias(), err)
		}
		return c.NewClient(checked).ApplyRelay(ctx, relayRequest(cfg), log)
	}
	return fmt.Errorf("relay node %s not found", relayNodeID)
}

// ApplyRelayFor installs the job relayNodeID currently owes according to the
// registries. It is the entry point every caller that changed a relay link
// uses, so the node's ruleset always describes the whole registry rather than
// the one link that was edited.
func (c *Controller) ApplyRelayFor(ctx context.Context, relayNodeID string, log io.Writer) error {
	c.defaults()
	links, err := relaylinks.Load(c.Layout)
	if err != nil {
		return err
	}
	endpoints, err := c.RelayEndpoints()
	if err != nil {
		return err
	}
	cfg, err := c.RelayConfigFor(relayNodeID, links, endpoints)
	if err != nil {
		return err
	}
	if log == nil {
		log = io.Discard
	}
	return c.ApplyRelay(ctx, relayNodeID, cfg, log)
}

func relayRequest(cfg relay.Config) nodeapi.RelayRequest {
	req := nodeapi.RelayRequest{Landings: make([]nodeapi.RelayLanding, 0, len(cfg.Landings))}
	for _, landing := range cfg.Landings {
		forwards := make([]nodeapi.RelayForward, 0, len(landing.Forwards))
		for _, f := range landing.Forwards {
			forwards = append(forwards, nodeapi.RelayForward{
				Protocol:   f.Protocol,
				Network:    f.Network,
				ListenPort: f.ListenPort,
				TargetPort: f.TargetPort,
			})
		}
		req.Landings = append(req.Landings, nodeapi.RelayLanding{
			NodeID:   landing.NodeID,
			Name:     landing.Name,
			Host:     landing.Host,
			Address:  landing.Address,
			Forwards: forwards,
		})
	}
	return req
}
