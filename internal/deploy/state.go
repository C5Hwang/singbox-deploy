package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// LoadProtocolConfig reconstructs the managed install config from small state
// files. It is used by the protocol management UI so config/subscriptions can be
// regenerated without asking for unchanged credentials again.
func LoadProtocolConfig(layout paths.Layout) (Config, error) {
	layout = DefaultProtocolLayout(layout)
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
	subscribePort := readProtocolStateIntDefault(store, "subscribe_port", DefaultSubscribePort)
	monitorPublicPort := readProtocolStateIntDefault(store, "monitor_public_port", 0)
	if monitorPublicPort == 0 {
		monitorPublicPort = readProtocolStateIntDefault(store, "traffic_port", 0)
	}
	if monitorPublicPort == 0 {
		monitorPublicPort = subscribePort
	}
	monitorStateValue := readProtocolStateDefault(store, "monitor", "")
	if monitorStateValue == "" {
		monitorStateValue = readProtocolStateDefault(store, "traffic_monitor", "yes")
	}
	monitorAlias := readProtocolStateDefault(store, "monitor_alias", "")
	if monitorAlias == "" {
		monitorAlias = readProtocolStateDefault(store, "traffic_alias", DefaultMonitorAlias)
	}

	cfg := Config{
		Domain:                 domain,
		Email:                  readProtocolStateDefault(store, "email", ""),
		Challenge:              "http-01",
		DNSProvider:            readProtocolStateDefault(store, "dns_provider", ""),
		DNSCredentials:         map[string]string{},
		Enabled:                enabled,
		DisplayName:            readProtocolStateDefault(store, "display_name", DefaultDisplayName),
		Salt:                   salt,
		SiteTemplate:           readProtocolStateDefault(store, "site_template", DefaultSiteTemplate),
		RealityServerName:      readProtocolStateDefault(store, "reality_server_name", ""),
		RealityHandshakePort:   readProtocolStateIntDefault(store, "reality_handshake_port", config.DefaultRealityHandshakePort),
		Hysteria2UpMbps:        readProtocolStateIntDefault(store, "hysteria2_up_mbps", config.DefaultHysteria2UpMbps),
		Hysteria2DownMbps:      readProtocolStateIntDefault(store, "hysteria2_down_mbps", config.DefaultHysteria2DownMbps),
		SubscribePort:          subscribePort,
		MonitorPublicPort:      monitorPublicPort,
		MonitorPort:            readProtocolStateIntDefault(store, "monitor_port", DefaultMonitorPort),
		DeployMonitor:          monitorStateValue != "no",
		MonitorAlias:           monitorAlias,
		TrafficInLimitBytes:    readProtocolStateUintDefault(store, "traffic_in_limit_bytes", 0),
		TrafficOutLimitBytes:   readProtocolStateUintDefault(store, "traffic_out_limit_bytes", 0),
		TrafficTotalLimitBytes: readProtocolStateUintDefault(store, "traffic_total_limit_bytes", 0),
		ResetDay:               readProtocolStateIntDefault(store, "reset_day", DefaultResetDay),
		ResetHour:              readProtocolStateIntDefault(store, "reset_hour", DefaultResetHour),
		MonitorInterface:       readProtocolStateDefault(store, "monitor_interface", ""),
		MonitorIntervalSeconds: readProtocolStateIntDefault(store, "monitor_interval_seconds", DefaultMonitorIntervalSeconds),
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

// DefaultProtocolLayout returns the given layout, falling back to the system
// default when the layout root is empty.
func DefaultProtocolLayout(layout paths.Layout) paths.Layout {
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
	selected := SelectedProtocolSet(canonicalProtocolsFromString(value))
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

// SelectedProtocolSet returns a set of known protocols from the given slice.
func SelectedProtocolSet(protocols []config.Protocol) map[config.Protocol]bool {
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

// WriteProtocolConfigCandidate renders a candidate config.json for validation.
func WriteProtocolConfigCandidate(layout paths.Layout, cfg Config) error {
	certPath, keyPath := filepath.Join(layout.TLSDir, cfg.Domain+".crt"), filepath.Join(layout.TLSDir, cfg.Domain+".key")
	cfgBytes, err := config.Build(cfg.serverOptions(certPath, keyPath))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.FragmentsDir, 0o755); err != nil {
		return err
	}
	return WriteFile(ProtocolConfigCandidate(layout), cfgBytes, 0o644)
}

// ProtocolConfigCandidate returns the path to the candidate config.json.
func ProtocolConfigCandidate(layout paths.Layout) string {
	return layout.ConfigJSON + ".candidate"
}
