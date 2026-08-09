// Package deploy orchestrates the sing-box deployment lifecycle: fresh
// installation, configuration, subscriptions, and shared types used by the
// per-domain management packages (protocol, subscription, monitor, account,
// uninstall). System mutations go through a system.Runner and filesystem
// writes go under a paths.Layout, so the whole flow is exercisable with a
// recording runner and a temporary root.
package deploy

import (
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const (
	DefaultDisplayName            = "Node"
	DefaultSubscribePort          = 2096
	DefaultMonitorPublicPort      = 2097
	DefaultMonitorPort            = 19090
	DefaultResetDay               = 1
	DefaultResetHour              = 0
	DefaultMonitorAlias           = "Local Server"
	DefaultMonitorIntervalSeconds = 60
)

// Credentials holds every generated secret for the single user.
type Credentials struct {
	RealityVisionUUID string
	RealityGRPCUUID   string
	HysteriaPassword  string
	TUICUUID          string
	TUICPassword      string
	AnyTLSPassword    string
	RealityPrivateKey string
	RealityPublicKey  string
	RealityShortID    string
}

// GenerateCredentials produces a fresh set of user credentials.
func GenerateCredentials() (Credentials, error) {
	var c Credentials
	var err error
	pick := func(fn func() (string, error), dst *string) {
		if err != nil {
			return
		}
		*dst, err = fn()
	}
	pick(credentials.UUID, &c.RealityVisionUUID)
	pick(credentials.UUID, &c.RealityGRPCUUID)
	pick(credentials.Password, &c.HysteriaPassword)
	pick(credentials.UUID, &c.TUICUUID)
	pick(credentials.Password, &c.TUICPassword)
	pick(credentials.Password, &c.AnyTLSPassword)
	pick(credentials.ShortID, &c.RealityShortID)
	if err != nil {
		return Credentials{}, err
	}
	kp, kerr := credentials.RealityKeypair()
	if kerr != nil {
		return Credentials{}, kerr
	}
	c.RealityPrivateKey = kp.PrivateKey
	c.RealityPublicKey = kp.PublicKey
	return c, nil
}

// Config is the complete input to an installation. Certificates are issued
// centrally via DNS-01 (see internal/certmgr), so no per-install challenge,
// account-email, or DNS-provider fields live here.
type Config struct {
	Domain   string
	PublicIP string // public address verified during the interactive domain check

	Ports   config.Ports
	Enabled []config.Protocol

	DisplayName  string
	Salt         string
	SiteTemplate string

	RealityServerName    string
	RealityHandshakePort int

	SubscribePort     int
	MonitorPublicPort int
	MonitorPort       int

	// MonitorDomain is the hostname Nginx serves the monitor under. It is kept
	// separate from Domain so the monitor is not reachable through the
	// masquerade site's name; an empty value means "the install domain", which
	// is what installations made before the split recorded.
	MonitorDomain string

	DeployMonitor          bool
	DeployMonitorFrontend  bool
	MonitorAlias           string
	TrafficInLimitBytes    uint64
	TrafficOutLimitBytes   uint64
	TrafficTotalLimitBytes uint64
	ResetDay               int
	ResetHour              int
	MonitorInterface       string
	MonitorIntervalSeconds int

	OS       system.OSRelease
	Firewall system.Firewall

	// SpokeMode installs a spoke managed by the hub: the certificate is pushed
	// in (no local ACME), no public subscription/monitor ports are opened, no
	// cert-renew timer or monitor unit is installed (the hub renews and the
	// agent runs the monitor in-process), and Nginx serves only the camouflage
	// site. False installs a hub with the full public surface.
	SpokeMode bool

	// WGListenPort, when > 0, is the hub's WireGuard UDP port to open in the
	// firewall during install so spokes can dial in.
	WGListenPort int

	Creds Credentials
}

// MonitorHost returns the hostname the monitor is published under, falling back
// to the install domain when no separate monitor domain is configured.
func (c Config) MonitorHost() string {
	if domain := strings.TrimSpace(c.MonitorDomain); domain != "" {
		return domain
	}
	return strings.TrimSpace(c.Domain)
}

// MonitorCertificateDomain returns the extra hostname that needs its own
// managed certificate because the monitor is published under a name of its
// own. It is empty when the monitor shares the install domain's certificate,
// when the monitor is disabled, or on a spoke (which publishes no monitor).
func (c Config) MonitorCertificateDomain() (string, error) {
	if c.SpokeMode || !c.DeployMonitor {
		return "", nil
	}
	monitorDomain, err := certmgr.NormalizeDomain(c.MonitorHost())
	if err != nil {
		return "", fmt.Errorf("monitor domain: %w", err)
	}
	domain, err := certmgr.NormalizeDomain(c.Domain)
	if err != nil {
		return "", err
	}
	if monitorDomain == domain {
		return "", nil
	}
	return monitorDomain, nil
}

// EnabledProtocols returns the protocols to install, defaulting to all supported.
func (c Config) EnabledProtocols() []config.Protocol {
	if len(c.Enabled) == 0 {
		return config.AllProtocols
	}
	return c.Enabled
}

// userCredentials maps install credentials to config.UserCredentials.
func (c Config) userCredentials() config.UserCredentials {
	return config.UserCredentials{
		DisplayName:       c.DisplayName,
		RealityVisionUUID: c.Creds.RealityVisionUUID,
		RealityGRPCUUID:   c.Creds.RealityGRPCUUID,
		HysteriaPassword:  c.Creds.HysteriaPassword,
		TUICUUID:          c.Creds.TUICUUID,
		TUICPassword:      c.Creds.TUICPassword,
		AnyTLSPassword:    c.Creds.AnyTLSPassword,
	}
}

// serverOptions builds the sing-box config inputs from the install config.
func (c Config) serverOptions(tlsCert, tlsKey string) config.ServerOptions {
	return config.ServerOptions{
		Domain:            c.Domain,
		TLSCert:           tlsCert,
		TLSKey:            tlsKey,
		RealityPrivateKey: c.Creds.RealityPrivateKey,
		RealityServerName: c.RealityServerName,
		RealityPort:       c.realityHandshakePort(),
		RealityShortID:    c.Creds.RealityShortID,
		SubscribePort:     c.SubscribePort,
		User:              c.userCredentials(),
		Ports:             c.Ports,
		Enabled:           c.EnabledProtocols(),
	}
}

func (c Config) realityHandshakePort() int {
	if c.RealityHandshakePort > 0 {
		return c.RealityHandshakePort
	}
	return config.DefaultRealityHandshakePort
}

// ManagedFirewallPorts returns every port the deployment opens in the firewall,
// used by uninstall to close them again.
func ManagedFirewallPorts(c Config) []system.Port { return c.firewallPorts() }

// firewallPorts returns the TCP/UDP ports to open for the enabled protocols.
func (c Config) firewallPorts() []system.Port {
	want := map[config.Protocol]struct {
		port  int
		proto string
	}{
		config.ProtocolRealityVision: {c.Ports.RealityVision, "tcp"},
		config.ProtocolRealityGRPC:   {c.Ports.RealityGRPC, "tcp"},
		config.ProtocolHysteria2:     {c.Ports.Hysteria2, "udp"},
		config.ProtocolTUIC:          {c.Ports.TUIC, "udp"},
		config.ProtocolAnyTLS:        {c.Ports.AnyTLS, "tcp"},
	}
	var ports []system.Port
	for _, p := range c.EnabledProtocols() {
		if spec, ok := want[p]; ok {
			ports = append(ports, system.Port{Number: spec.port, Proto: spec.proto})
		}
	}
	// A spoke serves only the camouflage site publicly; its subscription and
	// monitor data reach the hub over the WireGuard overlay, so no public
	// subscription/monitor ports are opened. A hub opens both.
	if !c.SpokeMode {
		ports = append(ports, system.Port{Number: c.SubscribePort, Proto: "tcp"})
		if c.DeployMonitor {
			ports = append(ports, system.Port{Number: c.MonitorPublicPort, Proto: "tcp"})
		}
	}
	ports = append(ports,
		system.Port{Number: 80, Proto: "tcp"},
		system.Port{Number: 443, Proto: "tcp"},
	)
	if c.WGListenPort > 0 {
		ports = append(ports, system.Port{Number: c.WGListenPort, Proto: "udp"})
	}
	return ports
}

// portChecks returns the ports that must be available before installation. The
// public protocol, subscription, and monitor UI ports are probed through the
// configured domain; the monitor service port only needs to be free locally
// because it binds to 127.0.0.1 behind Nginx.
func (c Config) portChecks() []system.Port {
	checks := make([]system.Port, 0, len(c.EnabledProtocols())+4)
	for _, p := range c.EnabledProtocols() {
		switch p {
		case config.ProtocolRealityVision:
			checks = append(checks, system.Port{Number: c.Ports.RealityVision, Proto: "tcp", Label: "VLESS Reality Vision", Public: true})
		case config.ProtocolRealityGRPC:
			checks = append(checks, system.Port{Number: c.Ports.RealityGRPC, Proto: "tcp", Label: "VLESS Reality gRPC", Public: true})
		case config.ProtocolHysteria2:
			checks = append(checks, system.Port{Number: c.Ports.Hysteria2, Proto: "udp", Label: "Hysteria2", Public: true})
		case config.ProtocolTUIC:
			checks = append(checks, system.Port{Number: c.Ports.TUIC, Proto: "udp", Label: "TUIC", Public: true})
		case config.ProtocolAnyTLS:
			checks = append(checks, system.Port{Number: c.Ports.AnyTLS, Proto: "tcp", Label: "AnyTLS", Public: true})
		}
	}
	// A spoke exposes neither the subscription nor the monitor port publicly.
	if !c.SpokeMode {
		checks = append(checks, system.Port{Number: c.SubscribePort, Proto: "tcp", Label: "subscription/Nginx", Public: true})
		if c.DeployMonitor {
			checks = append(checks, system.Port{Number: c.MonitorPublicPort, Proto: "tcp", Label: "monitor/Nginx", Public: true})
		}
	}
	// Nginx always listens on 80 (HTTP redirect) and 443 (camouflage site), so
	// both must be free regardless of the ACME challenge. Skip the duplicates
	// already added above when the subscription/monitor ports are 80/443.
	seen := map[int]bool{}
	for _, p := range checks {
		seen[p.Number] = true
	}
	if !seen[80] {
		checks = append(checks, system.Port{Number: 80, Proto: "tcp", Label: "Nginx HTTP redirect", Public: true})
	}
	if !seen[443] {
		checks = append(checks, system.Port{Number: 443, Proto: "tcp", Label: "Nginx HTTPS camouflage", Public: true})
	}
	if c.DeployMonitor {
		checks = append(checks, system.Port{Number: c.MonitorPort, Proto: "tcp", Label: "monitor service", Public: false})
	}
	return checks
}

// ValidatePorts rejects configurations whose listen ports collide. Nginx always
// owns 80 (HTTP redirect) and 443 (camouflage site); the subscription and
// monitor endpoints may fold onto 443 (Nginx merges them into that server
// block), but protocol ports must never take 80/443 and no non-443 port may be
// claimed twice. This is the configuration-level backstop for CheckPorts, which
// only sees whatever is free on the host at install time.
func (c Config) ValidatePorts() error {
	owner := map[int]string{80: "Nginx HTTP redirect", 443: "Nginx HTTPS camouflage"}
	claim := func(port int, label string, mayFold443 bool) error {
		if port <= 0 {
			return nil
		}
		if port == 443 && mayFold443 {
			return nil // folded into the Nginx camouflage server block
		}
		if prev, ok := owner[port]; ok {
			return fmt.Errorf("port %d is used by both %s and %s", port, prev, label)
		}
		owner[port] = label
		return nil
	}
	if !c.SpokeMode {
		if err := claim(c.SubscribePort, "subscription", true); err != nil {
			return err
		}
	}
	if c.DeployMonitor {
		if !c.SpokeMode {
			if err := claim(c.MonitorPublicPort, "monitor public", true); err != nil {
				return err
			}
		}
		if err := claim(c.MonitorPort, "monitor service", false); err != nil {
			return err
		}
	}
	protoLabels := map[config.Protocol]string{
		config.ProtocolRealityVision: "VLESS Reality Vision",
		config.ProtocolRealityGRPC:   "VLESS Reality gRPC",
		config.ProtocolHysteria2:     "Hysteria2",
		config.ProtocolTUIC:          "TUIC",
		config.ProtocolAnyTLS:        "AnyTLS",
	}
	protoPorts := map[config.Protocol]int{
		config.ProtocolRealityVision: c.Ports.RealityVision,
		config.ProtocolRealityGRPC:   c.Ports.RealityGRPC,
		config.ProtocolHysteria2:     c.Ports.Hysteria2,
		config.ProtocolTUIC:          c.Ports.TUIC,
		config.ProtocolAnyTLS:        c.Ports.AnyTLS,
	}
	for _, p := range c.EnabledProtocols() {
		if err := claim(protoPorts[p], protoLabels[p], false); err != nil {
			return err
		}
	}
	return nil
}
