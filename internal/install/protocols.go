package install

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// ProtocolUpdateOptions describes a protocol enable/disable operation against
// an existing managed installation.
type ProtocolUpdateOptions struct {
	Layout paths.Layout
	Runner system.Runner

	Firewall system.Firewall
	Selected []config.Protocol
	Ports    config.Ports
	Creds    Credentials

	// RealityServerName overrides the stored Reality camouflage host when Reality
	// is newly enabled and no stored value exists yet.
	RealityServerName string

	CheckPorts func(context.Context, Config, []config.Protocol) error
	Progress   func(Event)
}

// LoadProtocolConfig reconstructs the managed install config from small state
// files. It is used by the protocol management UI so config/subscriptions can be
// regenerated without asking for unchanged credentials again.
func LoadProtocolConfig(layout paths.Layout) (Config, error) {
	layout = defaultProtocolLayout(layout)
	store := state.NewStore(layout.StateDir)
	domain, err := readProtocolState(store, "domain", true)
	if err != nil {
		return Config{}, fmt.Errorf("no managed installation state found; run install first: %w", err)
	}
	salt, err := readProtocolState(store, "subscribe_salt", true)
	if err != nil {
		return Config{}, err
	}
	enabledRaw, err := readProtocolState(store, "enabled_protocols", true)
	if err != nil {
		return Config{}, err
	}
	enabled, err := parseProtocolState(enabledRaw)
	if err != nil {
		return Config{}, err
	}
	subscribePort := readProtocolStateIntDefault(store, "subscribe_port", 2096)
	trafficPort := readProtocolStateIntDefault(store, "traffic_port", 0)
	if trafficPort == 0 {
		trafficPort = subscribePort
	}

	cfg := Config{
		Domain:                 domain,
		Email:                  readProtocolStateDefault(store, "email", ""),
		Challenge:              "http-01",
		DNSProvider:            readProtocolStateDefault(store, "dns_provider", ""),
		DNSCredentials:         map[string]string{},
		Enabled:                enabled,
		DisplayName:            readProtocolStateDefault(store, "display_name", "Node"),
		Salt:                   salt,
		RealityServerName:      readProtocolStateDefault(store, "reality_server_name", ""),
		RealityHandshakePort:   readProtocolStateIntDefault(store, "reality_handshake_port", 443),
		SubscribePort:          subscribePort,
		TrafficPort:            trafficPort,
		MonitorPort:            readProtocolStateIntDefault(store, "monitor_port", 19090),
		DeployMonitor:          readProtocolStateDefault(store, "traffic_monitor", "yes") != "no",
		TrafficInLimitBytes:    readProtocolStateUintDefault(store, "traffic_in_limit_bytes", 0),
		TrafficOutLimitBytes:   readProtocolStateUintDefault(store, "traffic_out_limit_bytes", 0),
		TrafficTotalLimitBytes: readProtocolStateUintDefault(store, "traffic_total_limit_bytes", 0),
		ResetDay:               readProtocolStateIntDefault(store, "reset_day", 1),
		MonitorInterface:       readProtocolStateDefault(store, "monitor_interface", ""),
		Ports: config.Ports{
			RealityVision: readProtocolStateIntDefault(store, "reality_vision_port", 0),
			RealityGRPC:   readProtocolStateIntDefault(store, "reality_grpc_port", 0),
			Hysteria2:     readProtocolStateIntDefault(store, "hysteria2_port", 0),
			TUIC:          readProtocolStateIntDefault(store, "tuic_port", 0),
			AnyTLS:        readProtocolStateIntDefault(store, "anytls_port", 0),
		},
		Creds: Credentials{
			RealityVisionUUID: readProtocolStateDefault(store, "reality_vision_uuid", ""),
			RealityGRPCUUID:   readProtocolStateDefault(store, "reality_grpc_uuid", ""),
			HysteriaPassword:  readProtocolStateDefault(store, "hysteria2_password", ""),
			TUICUUID:          readProtocolStateDefault(store, "tuic_uuid", ""),
			TUICPassword:      readProtocolStateDefault(store, "tuic_password", ""),
			AnyTLSPassword:    readProtocolStateDefault(store, "anytls_password", ""),
			RealityPrivateKey: readProtocolStateDefault(store, "reality_private_key", ""),
			RealityPublicKey:  readProtocolStateDefault(store, "reality_public_key", ""),
			RealityShortID:    readProtocolStateDefault(store, "reality_short_id", ""),
		},
	}
	if challenge := readProtocolStateDefault(store, "acme_challenge", ""); challenge != "" {
		cfg.Challenge = acmeChallenge(challenge)
	}
	return cfg, nil
}

// UpdateProtocols applies the selected protocol set to an existing managed
// installation: generate missing material for newly enabled protocols, validate
// the new config, refresh subscriptions, persist state, and restart sing-box.
func UpdateProtocols(ctx context.Context, opts ProtocolUpdateOptions) (Config, error) {
	opts.Layout = defaultProtocolLayout(opts.Layout)
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.CheckPorts == nil {
		opts.CheckPorts = func(ctx context.Context, cfg Config, added []config.Protocol) error {
			return system.CheckPorts(ctx, cfg.Domain, addedLocalPortChecks(cfg, added))
		}
	}
	if len(opts.Selected) == 0 {
		return Config{}, fmt.Errorf("select at least one protocol")
	}

	cfg, err := LoadProtocolConfig(opts.Layout)
	if err != nil {
		return Config{}, err
	}
	old := cfg.enabled()
	oldCfg := cfg
	cfg.Enabled = canonicalProtocols(opts.Selected)
	if len(cfg.Enabled) == 0 {
		return Config{}, fmt.Errorf("select at least one supported protocol")
	}
	cfg.Firewall = opts.Firewall
	if strings.TrimSpace(opts.RealityServerName) != "" {
		cfg.RealityServerName = strings.TrimSpace(opts.RealityServerName)
	}
	applyProtocolOverrides(&cfg, opts)
	if err := ensureProtocolMaterial(&cfg, old, cfg.Enabled); err != nil {
		return Config{}, err
	}
	changedPorts := protocolsNeedingPortChanges(oldCfg, cfg)

	steps := protocolUpdateSteps(opts, changedPorts)
	for i, s := range steps {
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

type protocolUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, Config) error
}

func protocolUpdateSteps(opts ProtocolUpdateOptions, changedPorts []config.Protocol) []protocolUpdateStep {
	steps := []protocolUpdateStep{
		{label: "Port check", detail: "check new or changed protocol ports", run: func(ctx context.Context, cfg Config) error {
			return opts.CheckPorts(ctx, cfg, changedPorts)
		}},
	}
	if opts.Firewall != system.FirewallNone && len(changedPorts) > 0 {
		steps = append(steps, protocolUpdateStep{label: "Firewall", detail: "open new or changed protocol ports", run: func(_ context.Context, cfg Config) error {
			cmds := system.FirewallCommands(opts.Firewall, firewallPortsForProtocols(cfg, changedPorts))
			if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
				cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
			}
			return runProtocolCommands(opts.Runner, cmds...)
		}})
	}
	steps = append(steps,
		protocolUpdateStep{label: "Config", detail: "render candidate config.json", run: func(_ context.Context, cfg Config) error {
			return writeProtocolConfigCandidate(opts.Layout, cfg)
		}},
		protocolUpdateStep{label: "Validate", detail: "validate candidate config with sing-box", run: func(_ context.Context, _ Config) error {
			return opts.Runner.Run(system.Command{Name: opts.Layout.SingBoxBin, Args: []string{"check", "-c", protocolConfigCandidate(opts.Layout)}})
		}},
		protocolUpdateStep{label: "Activate config", detail: "replace config.json after validation", run: func(_ context.Context, _ Config) error {
			return os.Rename(protocolConfigCandidate(opts.Layout), opts.Layout.ConfigJSON)
		}},
		protocolUpdateStep{label: "Subscriptions", detail: "regenerate subscription files", run: func(_ context.Context, cfg Config) error {
			return writeSubscriptions(opts.Layout, cfg)
		}},
		protocolUpdateStep{label: "State", detail: "persist protocol selection and generated material", run: func(_ context.Context, cfg Config) error {
			return writeInstallState(opts.Layout.StateDir, cfg)
		}},
		protocolUpdateStep{label: "Restart", detail: "restart sing-box.service", run: func(_ context.Context, _ Config) error {
			return opts.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}},
	)
	return steps
}

func defaultProtocolLayout(layout paths.Layout) paths.Layout {
	if layout.Root == "" {
		return paths.DefaultLayout()
	}
	return layout
}

func readProtocolState(store state.Store, name string, required bool) (string, error) {
	value, err := store.ReadString(name)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read state %s: %w", name, err)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("state %s is empty", name)
	}
	return value, nil
}

func readProtocolStateDefault(store state.Store, name, fallback string) string {
	value, err := readProtocolState(store, name, false)
	if err != nil || value == "" {
		return fallback
	}
	return value
}

func readProtocolStateIntDefault(store state.Store, name string, fallback int) int {
	value := readProtocolStateDefault(store, name, "")
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func readProtocolStateUintDefault(store state.Store, name string, fallback uint64) uint64 {
	value := readProtocolStateDefault(store, name, "")
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func acmeChallenge(value string) acme.Challenge { return acme.Challenge(value) }

func parseProtocolState(value string) ([]config.Protocol, error) {
	selected := selectedProtocolSet(canonicalProtocolsFromString(value))
	var out []config.Protocol
	for _, p := range config.AllProtocols {
		if selected[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("state enabled_protocols has no supported protocols")
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !protocolKnown(config.Protocol(part)) {
			return nil, fmt.Errorf("state enabled_protocols contains unsupported protocol %q", part)
		}
	}
	return out, nil
}

func canonicalProtocolsFromString(value string) []config.Protocol {
	var out []config.Protocol
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, config.Protocol(part))
		}
	}
	return out
}

func canonicalProtocols(protocols []config.Protocol) []config.Protocol {
	selected := selectedProtocolSet(protocols)
	out := make([]config.Protocol, 0, len(selected))
	for _, p := range config.AllProtocols {
		if selected[p] {
			out = append(out, p)
		}
	}
	return out
}

func selectedProtocolSet(protocols []config.Protocol) map[config.Protocol]bool {
	selected := map[config.Protocol]bool{}
	for _, p := range protocols {
		if protocolKnown(p) {
			selected[p] = true
		}
	}
	return selected
}

func protocolKnown(proto config.Protocol) bool {
	for _, p := range config.AllProtocols {
		if p == proto {
			return true
		}
	}
	return false
}

func applyProtocolOverrides(cfg *Config, opts ProtocolUpdateOptions) {
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

func protocolsNeedingPortChanges(oldCfg, newCfg Config) []config.Protocol {
	oldSet := selectedProtocolSet(oldCfg.enabled())
	var changed []config.Protocol
	for _, p := range newCfg.enabled() {
		if !oldSet[p] || protocolPort(oldCfg, p) != protocolPort(newCfg, p) {
			changed = append(changed, p)
		}
	}
	return changed
}

func ensureProtocolMaterial(cfg *Config, old, selected []config.Protocol) error {
	oldSet := selectedProtocolSet(old)
	used := map[int]bool{80: true, cfg.SubscribePort: true}
	if cfg.DeployMonitor {
		used[cfg.TrafficPort] = true
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

func ensureProtocolCredentials(cfg *Config, proto config.Protocol, alreadyInstalled bool) error {
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

func ensureRealityKeys(cfg *Config, alreadyInstalled bool) error {
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

func protocolPort(cfg Config, proto config.Protocol) int {
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

func setProtocolPort(cfg *Config, proto config.Protocol, port int) {
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

func addedLocalPortChecks(cfg Config, added []config.Protocol) []system.Port {
	ports := firewallPortsForProtocols(cfg, added)
	for i := range ports {
		ports[i].Public = false
	}
	return ports
}

func firewallPortsForProtocols(cfg Config, protocols []config.Protocol) []system.Port {
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

func writeProtocolConfigCandidate(layout paths.Layout, cfg Config) error {
	certPath, keyPath := filepath.Join(layout.TLSDir, cfg.Domain+".crt"), filepath.Join(layout.TLSDir, cfg.Domain+".key")
	cfgBytes, err := config.Build(cfg.serverOptions(certPath, keyPath))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.FragmentsDir, 0o755); err != nil {
		return err
	}
	return writeFile(protocolConfigCandidate(layout), cfgBytes, 0o644)
}

func protocolConfigCandidate(layout paths.Layout) string {
	return layout.ConfigJSON + ".candidate"
}

func runProtocolCommands(r system.Runner, cmds ...system.Command) error {
	for _, c := range cmds {
		if err := r.Run(c); err != nil {
			return fmt.Errorf("command %q: %w", c.String(), err)
		}
	}
	return nil
}

func emitProtocolProgress(progress func(Event), e Event) {
	if progress != nil {
		progress(e)
	}
}
