package cluster

import (
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/config"
)

// EnsureNodeProtocolPorts brings node.Ports into a state where every protocol
// in target has a valid, non-zero listen port. User-supplied overrides take
// precedence over the stored value; for any protocol in target that still has
// no port, a fresh random port is picked. The updated node is persisted via
// reg.Save before returning so the registry never lags behind the config the
// master is about to push to the agent.
//
// 80, plus all currently stored protocol ports, are treated as "used" so the
// new allocation never collides with another listener on the node. 443 is
// rejected by config.ValidateProtocolPort because it is owned by the
// masquerade site.
func EnsureNodeProtocolPorts(reg Registry, node Node, target []config.Protocol, overrides map[config.Protocol]int) (Node, error) {
	used := map[int]bool{80: true}
	for _, p := range config.AllProtocols {
		if port := portForProtocol(node.Ports, p); port > 0 {
			used[port] = true
		}
	}

	// Apply user overrides first; an override on a protocol with an existing
	// stored port replaces it, so free the old slot before validating.
	for _, p := range target {
		port, ok := overrides[p]
		if !ok || port == 0 {
			continue
		}
		existing := portForProtocol(node.Ports, p)
		if existing > 0 {
			delete(used, existing)
		}
		if err := config.ValidateProtocolPort(port, used); err != nil {
			return Node{}, fmt.Errorf("%s: %w", p, err)
		}
		used[port] = true
		node.Ports = withProtocolPort(node.Ports, p, port)
	}

	// Random allocate for any selected protocol without a stored port.
	for _, p := range target {
		if portForProtocol(node.Ports, p) > 0 {
			continue
		}
		port, err := config.RandomProtocolPort(used)
		if err != nil {
			return Node{}, fmt.Errorf("%s: %w", p, err)
		}
		node.Ports = withProtocolPort(node.Ports, p, port)
	}

	if err := reg.Save(node); err != nil {
		return Node{}, fmt.Errorf("save node: %w", err)
	}
	return node, nil
}

func portForProtocol(p config.Ports, proto config.Protocol) int {
	switch proto {
	case config.ProtocolRealityVision:
		return p.RealityVision
	case config.ProtocolRealityGRPC:
		return p.RealityGRPC
	case config.ProtocolHysteria2:
		return p.Hysteria2
	case config.ProtocolTUIC:
		return p.TUIC
	case config.ProtocolAnyTLS:
		return p.AnyTLS
	}
	return 0
}

func withProtocolPort(p config.Ports, proto config.Protocol, port int) config.Ports {
	switch proto {
	case config.ProtocolRealityVision:
		p.RealityVision = port
	case config.ProtocolRealityGRPC:
		p.RealityGRPC = port
	case config.ProtocolHysteria2:
		p.Hysteria2 = port
	case config.ProtocolTUIC:
		p.TUIC = port
	case config.ProtocolAnyTLS:
		p.AnyTLS = port
	}
	return p
}
