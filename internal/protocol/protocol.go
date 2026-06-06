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

	Hysteria2UpMbps   int
	Hysteria2DownMbps int

	// RealityServerName overrides the stored Reality camouflage host when Reality
	// is newly enabled and no stored value exists yet.
	RealityServerName string

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
	if len(opts.Selected) == 0 {
		return deploy.Config{}, fmt.Errorf("select at least one protocol")
	}

	cfg, err := deploy.LoadProtocolConfig(opts.Layout)
	if err != nil {
		return deploy.Config{}, err
	}
	old := cfg.EnabledProtocols()
	oldCfg := cfg
	cfg.Enabled = canonicalProtocols(opts.Selected)
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

	steps := protocolUpdateSteps(opts, changedPorts)
	for i, s := range steps {
		deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return deploy.Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		deploy.EmitProgress(opts.Progress, deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

type protocolUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, deploy.Config) error
}

func protocolUpdateSteps(opts UpdateOptions, changedPorts []config.Protocol) []protocolUpdateStep {
	steps := []protocolUpdateStep{
		{label: "Port check", detail: "check new or changed protocol ports", run: func(ctx context.Context, cfg deploy.Config) error {
			return opts.CheckPorts(ctx, cfg, changedPorts)
		}},
	}
	if opts.Firewall != system.FirewallNone && len(changedPorts) > 0 {
		steps = append(steps, protocolUpdateStep{label: "Firewall", detail: "open new or changed protocol ports", run: func(_ context.Context, cfg deploy.Config) error {
			cmds := system.FirewallCommands(opts.Firewall, firewallPortsForProtocols(cfg, changedPorts))
			if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
				cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
			}
			return deploy.RunCommands(opts.Runner, cmds...)
		}})
	}
	steps = append(steps,
		protocolUpdateStep{label: "Config", detail: "render candidate config.json", run: func(_ context.Context, cfg deploy.Config) error {
			return deploy.WriteProtocolConfigCandidate(opts.Layout, cfg)
		}},
		protocolUpdateStep{label: "Validate", detail: "validate candidate config with sing-box", run: func(_ context.Context, _ deploy.Config) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", deploy.ProtocolConfigCandidate(opts.Layout)}})
		}},
		protocolUpdateStep{label: "Activate config", detail: "replace config.json after validation", run: func(_ context.Context, _ deploy.Config) error {
			return os.Rename(deploy.ProtocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		protocolUpdateStep{label: "Subscriptions", detail: "regenerate subscription files", run: func(_ context.Context, cfg deploy.Config) error {
			return deploy.WriteSubscriptions(opts.Layout, cfg)
		}},
		protocolUpdateStep{label: "State", detail: "persist protocol selection and generated material", run: func(_ context.Context, cfg deploy.Config) error {
			return deploy.WriteInstallState(opts.Layout.StateDir, cfg)
		}},
		protocolUpdateStep{label: "Restart", detail: "restart sing-box.service", run: func(_ context.Context, _ deploy.Config) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	)
	return steps
}

func canonicalProtocols(protocols []config.Protocol) []config.Protocol {
	selected := deploy.SelectedProtocolSet(protocols)
	out := make([]config.Protocol, 0, len(selected))
	for _, p := range config.AllProtocols {
		if selected[p] {
			out = append(out, p)
		}
	}
	return out
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
			if opts.Hysteria2UpMbps > 0 {
				cfg.Hysteria2UpMbps = opts.Hysteria2UpMbps
			}
			if opts.Hysteria2DownMbps > 0 {
				cfg.Hysteria2DownMbps = opts.Hysteria2DownMbps
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
			ports = append(ports, system.Port{Number: cfg.Ports.RealityVision, Proto: "tcp", Label: "Reality Vision"})
		case config.ProtocolRealityGRPC:
			ports = append(ports, system.Port{Number: cfg.Ports.RealityGRPC, Proto: "tcp", Label: "Reality gRPC"})
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
