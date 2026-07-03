// Package protocol manages the protocol enable/disable lifecycle for an
// existing managed sing-box installation.
package protocol

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// UpdateOptions describes a protocol enable/disable operation against
// an existing managed installation.
type UpdateOptions struct {
	Layout paths.Layout
	Runner system.Runner

	Firewall system.Firewall
	Selected []config.Protocol
	Ports    config.Ports
	Creds    deploy.Credentials

	// RealityServerName overrides the stored Reality camouflage host when Reality
	// is newly enabled and no stored value exists yet.
	RealityServerName string

	// Fetch retrieves remote subscription payloads when aggregating remote
	// nodes into the regenerated subscription outputs.
	Fetch deploy.SubscriptionFetcher

	CheckPorts func(context.Context, deploy.Config, []config.Protocol) error
	Progress   func(deploy.Event)
}

// Update applies the selected protocol set to an existing managed
// installation: generate missing material for newly enabled protocols, validate
// the new config, refresh subscriptions, persist state, and restart sing-box.
func Update(ctx context.Context, opts UpdateOptions) (deploy.Config, error) {
	opts.Layout = deploy.DefaultProtocolLayout(opts.Layout)
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.CheckPorts == nil {
		opts.CheckPorts = func(ctx context.Context, cfg deploy.Config, added []config.Protocol) error {
			return system.CheckPorts(ctx, cfg.Domain, addedLocalPortChecks(cfg, added))
		}
	}
	if opts.Fetch == nil {
		opts.Fetch = deploy.DefaultSubscriptionFetch
	}
	if len(opts.Selected) == 0 {
		return deploy.Config{}, fmt.Errorf("select at least one protocol")
	}

	cfg, err := deploy.LoadProtocolConfig(opts.Layout)
	if err != nil {
		return deploy.Config{}, err
	}
	remotes, err := deploy.LoadRemoteSubscriptions(opts.Layout)
	if err != nil {
		return deploy.Config{}, err
	}
	old := cfg.EnabledProtocols()
	oldCfg := cfg
	cfg.Enabled = deploy.CanonicalProtocols(opts.Selected)
	if len(cfg.Enabled) == 0 {
		return deploy.Config{}, fmt.Errorf("select at least one supported protocol")
	}
	cfg.Firewall = opts.Firewall
	if strings.TrimSpace(opts.RealityServerName) != "" {
		cfg.RealityServerName = strings.TrimSpace(opts.RealityServerName)
	}
	applyProtocolOverrides(&cfg, opts)
	if err := ensureProtocolMaterial(&cfg, old, cfg.Enabled); err != nil {
		return deploy.Config{}, err
	}
	changedPorts := protocolsNeedingPortChanges(oldCfg, cfg)
	stalePorts := stalePortsToClose(oldCfg, cfg)

	if err := deploy.RunSteps(ctx, opts.Progress, protocolUpdateSteps(opts, cfg, changedPorts, stalePorts, remotes)); err != nil {
		return deploy.Config{}, err
	}
	return cfg, nil
}

func protocolUpdateSteps(opts UpdateOptions, cfg deploy.Config, changedPorts []config.Protocol, stalePorts []system.Port, remotes []deploy.RemoteSubscription) []deploy.Step {
	steps := []deploy.Step{
		{Label: "Port check", Detail: "check new or changed protocol ports", Run: func(ctx context.Context) error {
			return opts.CheckPorts(ctx, cfg, changedPorts)
		}},
	}
	if opts.Firewall != system.FirewallNone && (len(changedPorts) > 0 || len(stalePorts) > 0) {
		steps = append(steps, deploy.Step{Label: "Firewall", Detail: "open new ports and close removed ones", Run: func(context.Context) error {
			// Open added/changed ports first, then close ports no longer used so
			// disabling a protocol or moving its port does not leave the old
			// port open forever.
			if err := deploy.RunCommands(opts.Runner, system.FirewallCommands(opts.Firewall, firewallPortsForProtocols(cfg, changedPorts))...); err != nil {
				return err
			}
			return deploy.RunCommands(opts.Runner, system.FirewallRemoveCommands(opts.Firewall, stalePorts)...)
		}})
	}
	steps = append(steps,
		deploy.Step{Label: "Config", Detail: "render candidate config.json", Run: func(context.Context) error {
			return deploy.WriteProtocolConfigCandidate(opts.Layout, cfg)
		}},
		deploy.Step{Label: "Validate", Detail: "validate candidate config with sing-box", Run: func(context.Context) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", deploy.ProtocolConfigCandidate(opts.Layout)}})
		}},
		deploy.Step{Label: "Activate config", Detail: "replace config.json after validation", Run: func(context.Context) error {
			return os.Rename(deploy.ProtocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		deploy.Step{Label: "Subscriptions", Detail: "regenerate subscription files", Run: func(ctx context.Context) error {
			return deploy.WriteSubscriptionsWithRemotes(ctx, opts.Layout, cfg, remotes, opts.Fetch, deploy.LoadLocalSubscriptionPosition(opts.Layout))
		}},
		deploy.Step{Label: "State", Detail: "persist protocol selection and generated material", Run: func(context.Context) error {
			return deploy.WriteInstallState(opts.Layout.StateDir, cfg)
		}},
		deploy.Step{Label: "Restart", Detail: "restart sing-box.service", Run: func(context.Context) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	)
	return steps
}

func applyProtocolOverrides(cfg *deploy.Config, opts UpdateOptions) {
	for _, p := range cfg.Enabled {
		switch p {
		case config.ProtocolRealityVision:
			if opts.Ports.RealityVision > 0 {
				cfg.Ports.RealityVision = opts.Ports.RealityVision
			}
			if strings.TrimSpace(opts.Creds.RealityVisionUUID) != "" {
				cfg.Creds.RealityVisionUUID = strings.TrimSpace(opts.Creds.RealityVisionUUID)
			}
		case config.ProtocolRealityGRPC:
			if opts.Ports.RealityGRPC > 0 {
				cfg.Ports.RealityGRPC = opts.Ports.RealityGRPC
			}
			if strings.TrimSpace(opts.Creds.RealityGRPCUUID) != "" {
				cfg.Creds.RealityGRPCUUID = strings.TrimSpace(opts.Creds.RealityGRPCUUID)
			}
		case config.ProtocolHysteria2:
			if opts.Ports.Hysteria2 > 0 {
				cfg.Ports.Hysteria2 = opts.Ports.Hysteria2
			}
			if strings.TrimSpace(opts.Creds.HysteriaPassword) != "" {
				cfg.Creds.HysteriaPassword = strings.TrimSpace(opts.Creds.HysteriaPassword)
			}
		case config.ProtocolTUIC:
			if opts.Ports.TUIC > 0 {
				cfg.Ports.TUIC = opts.Ports.TUIC
			}
			if strings.TrimSpace(opts.Creds.TUICUUID) != "" {
				cfg.Creds.TUICUUID = strings.TrimSpace(opts.Creds.TUICUUID)
			}
			if strings.TrimSpace(opts.Creds.TUICPassword) != "" {
				cfg.Creds.TUICPassword = strings.TrimSpace(opts.Creds.TUICPassword)
			}
		case config.ProtocolAnyTLS:
			if opts.Ports.AnyTLS > 0 {
				cfg.Ports.AnyTLS = opts.Ports.AnyTLS
			}
			if strings.TrimSpace(opts.Creds.AnyTLSPassword) != "" {
				cfg.Creds.AnyTLSPassword = strings.TrimSpace(opts.Creds.AnyTLSPassword)
			}
		}
	}
}

// stalePortsToClose returns firewall ports the old config opened that the new
// config no longer uses (protocol disabled or moved to a different port). Ports
// still in use by the new config — including a coincidental match with another
// protocol — are kept open.
func stalePortsToClose(oldCfg, newCfg deploy.Config) []system.Port {
	inUse := map[int]bool{}
	for _, p := range newCfg.EnabledProtocols() {
		if port := protocolPort(newCfg, p); port > 0 {
			inUse[port] = true
		}
	}
	var stale []system.Port
	seen := map[int]bool{}
	for _, p := range firewallPortsForProtocols(oldCfg, oldCfg.EnabledProtocols()) {
		if p.Number <= 0 || inUse[p.Number] || seen[p.Number] {
			continue
		}
		seen[p.Number] = true
		stale = append(stale, p)
	}
	return stale
}

func protocolsNeedingPortChanges(oldCfg, newCfg deploy.Config) []config.Protocol {
	oldSet := deploy.SelectedProtocolSet(oldCfg.EnabledProtocols())
	var changed []config.Protocol
	for _, p := range newCfg.EnabledProtocols() {
		if !oldSet[p] || protocolPort(oldCfg, p) != protocolPort(newCfg, p) {
			changed = append(changed, p)
		}
	}
	return changed
}

func ensureProtocolMaterial(cfg *deploy.Config, old, selected []config.Protocol) error {
	oldSet := deploy.SelectedProtocolSet(old)
	used := map[int]bool{80: true, cfg.SubscribePort: true}
	if cfg.DeployMonitor {
		used[cfg.MonitorPublicPort] = true
		used[cfg.MonitorPort] = true
	}
	for _, p := range selected {
		port := protocolPort(*cfg, p)
		if port <= 0 {
			continue
		}
		if used[port] {
			return fmt.Errorf("%s port %d conflicts with another managed port", p, port)
		}
		used[port] = true
	}
	for _, p := range selected {
		if err := ensureProtocolCredentials(cfg, p, oldSet[p]); err != nil {
			return err
		}
		if protocolPort(*cfg, p) == 0 {
			if oldSet[p] {
				return fmt.Errorf("missing stored port for installed protocol %s", p)
			}
			port, err := randomManagedPort(used)
			if err != nil {
				return err
			}
			setProtocolPort(cfg, p, port)
		}
	}
	if needsReality(selected) && strings.TrimSpace(cfg.RealityServerName) == "" {
		return fmt.Errorf("Reality URL/SNI is required before enabling Reality protocols")
	}
	return nil
}

func ensureProtocolCredentials(cfg *deploy.Config, proto config.Protocol, alreadyInstalled bool) error {
	missingInstalled := func(name string) error {
		if alreadyInstalled {
			return fmt.Errorf("missing stored credential %s for installed protocol %s", name, proto)
		}
		return nil
	}
	switch proto {
	case config.ProtocolRealityVision:
		if err := ensureRealityKeys(cfg, alreadyInstalled); err != nil {
			return err
		}
		if cfg.Creds.RealityVisionUUID == "" {
			if err := missingInstalled("reality_vision_uuid"); err != nil {
				return err
			}
			uuid, err := credentials.UUID()
			if err != nil {
				return err
			}
			cfg.Creds.RealityVisionUUID = uuid
		}
	case config.ProtocolRealityGRPC:
		if err := ensureRealityKeys(cfg, alreadyInstalled); err != nil {
			return err
		}
		if cfg.Creds.RealityGRPCUUID == "" {
			if err := missingInstalled("reality_grpc_uuid"); err != nil {
				return err
			}
			uuid, err := credentials.UUID()
			if err != nil {
				return err
			}
			cfg.Creds.RealityGRPCUUID = uuid
		}
	case config.ProtocolHysteria2:
		if cfg.Creds.HysteriaPassword == "" {
			if err := missingInstalled("hysteria2_password"); err != nil {
				return err
			}
			password, err := credentials.Password()
			if err != nil {
				return err
			}
			cfg.Creds.HysteriaPassword = password
		}
	case config.ProtocolTUIC:
		if cfg.Creds.TUICUUID == "" {
			if err := missingInstalled("tuic_uuid"); err != nil {
				return err
			}
			uuid, err := credentials.UUID()
			if err != nil {
				return err
			}
			cfg.Creds.TUICUUID = uuid
		}
		if cfg.Creds.TUICPassword == "" {
			if err := missingInstalled("tuic_password"); err != nil {
				return err
			}
			password, err := credentials.Password()
			if err != nil {
				return err
			}
			cfg.Creds.TUICPassword = password
		}
	case config.ProtocolAnyTLS:
		if cfg.Creds.AnyTLSPassword == "" {
			if err := missingInstalled("anytls_password"); err != nil {
				return err
			}
			password, err := credentials.Password()
			if err != nil {
				return err
			}
			cfg.Creds.AnyTLSPassword = password
		}
	}
	return nil
}

func ensureRealityKeys(cfg *deploy.Config, alreadyInstalled bool) error {
	if cfg.Creds.RealityPrivateKey != "" && cfg.Creds.RealityPublicKey != "" && cfg.Creds.RealityShortID != "" {
		return nil
	}
	if alreadyInstalled {
		return fmt.Errorf("missing stored Reality key material for installed Reality protocol")
	}
	kp, err := credentials.RealityKeypair()
	if err != nil {
		return err
	}
	shortID, err := credentials.ShortID()
	if err != nil {
		return err
	}
	cfg.Creds.RealityPrivateKey = kp.PrivateKey
	cfg.Creds.RealityPublicKey = kp.PublicKey
	cfg.Creds.RealityShortID = shortID
	return nil
}

func needsReality(protocols []config.Protocol) bool {
	for _, p := range protocols {
		if p == config.ProtocolRealityVision || p == config.ProtocolRealityGRPC {
			return true
		}
	}
	return false
}

func protocolPort(cfg deploy.Config, proto config.Protocol) int {
	switch proto {
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

func setProtocolPort(cfg *deploy.Config, proto config.Protocol, port int) {
	switch proto {
	case config.ProtocolRealityVision:
		cfg.Ports.RealityVision = port
	case config.ProtocolRealityGRPC:
		cfg.Ports.RealityGRPC = port
	case config.ProtocolHysteria2:
		cfg.Ports.Hysteria2 = port
	case config.ProtocolTUIC:
		cfg.Ports.TUIC = port
	case config.ProtocolAnyTLS:
		cfg.Ports.AnyTLS = port
	}
}

func randomManagedPort(used map[int]bool) (int, error) {
	const minPort = 20000
	const maxPort = 59999
	span := big.NewInt(maxPort - minPort + 1)
	for range 1000 {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			return 0, err
		}
		port := int(n.Int64()) + minPort
		if !used[port] {
			used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("could not choose an unused random port")
}

func addedLocalPortChecks(cfg deploy.Config, added []config.Protocol) []system.Port {
	ports := firewallPortsForProtocols(cfg, added)
	for i := range ports {
		ports[i].Public = false
	}
	return ports
}

func firewallPortsForProtocols(cfg deploy.Config, protocols []config.Protocol) []system.Port {
	var ports []system.Port
	for _, p := range protocols {
		switch p {
		case config.ProtocolRealityVision:
			ports = append(ports, system.Port{Number: cfg.Ports.RealityVision, Proto: "tcp", Label: "VLESS Reality Vision"})
		case config.ProtocolRealityGRPC:
			ports = append(ports, system.Port{Number: cfg.Ports.RealityGRPC, Proto: "tcp", Label: "VLESS Reality gRPC"})
		case config.ProtocolHysteria2:
			ports = append(ports, system.Port{Number: cfg.Ports.Hysteria2, Proto: "udp", Label: "Hysteria2"})
		case config.ProtocolTUIC:
			ports = append(ports, system.Port{Number: cfg.Ports.TUIC, Proto: "udp", Label: "TUIC"})
		case config.ProtocolAnyTLS:
			ports = append(ports, system.Port{Number: cfg.Ports.AnyTLS, Proto: "tcp", Label: "AnyTLS"})
		}
	}
	return ports
}
