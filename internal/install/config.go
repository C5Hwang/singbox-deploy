// Package install orchestrates the real, end-to-end sing-box deployment: system
// preparation, Nginx, certificates, sing-box core, config generation, services,
// subscriptions, and the monitor. System mutations go through a
// system.Runner and filesystem writes go under a paths.Layout, so the whole
// flow is exercisable with a recording runner and a temporary root.
package install

import (
	"github.com/C5Hwang/singbox-deploy/internal/acme"
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

// Config is the complete input to an installation.
type Config struct {
	Domain string
	Email  string

	Challenge      acme.Challenge
	DNSProvider    string
	DNSCredentials map[string]string

	Ports   config.Ports
	Enabled []config.Protocol

	DisplayName  string
	Salt         string
	SiteTemplate string

	RealityServerName    string
	RealityHandshakePort int
	Hysteria2UpMbps      int
	Hysteria2DownMbps    int

	SubscribePort     int
	MonitorPublicPort int
	MonitorPort       int

	DeployMonitor          bool
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

	Creds Credentials
}

// enabled returns the protocols to install, defaulting to all supported.
func (c Config) enabled() []config.Protocol {
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
		Hysteria2UpMbps:   c.hysteria2UpMbps(),
		Hysteria2DownMbps: c.hysteria2DownMbps(),
		User:              c.userCredentials(),
		Ports:             c.Ports,
		Enabled:           c.enabled(),
	}
}

func (c Config) realityHandshakePort() int {
	if c.RealityHandshakePort > 0 {
		return c.RealityHandshakePort
	}
	return config.DefaultRealityHandshakePort
}

func (c Config) hysteria2UpMbps() int {
	if c.Hysteria2UpMbps > 0 {
		return c.Hysteria2UpMbps
	}
	return config.DefaultHysteria2UpMbps
}

func (c Config) hysteria2DownMbps() int {
	if c.Hysteria2DownMbps > 0 {
		return c.Hysteria2DownMbps
	}
	return config.DefaultHysteria2DownMbps
}

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
	for _, p := range c.enabled() {
		if spec, ok := want[p]; ok {
			ports = append(ports, system.Port{Number: spec.port, Proto: spec.proto})
		}
	}
	// Subscriptions, the monitor UI, and ACME HTTP-01 need the public web ports.
	ports = append(ports, system.Port{Number: c.SubscribePort, Proto: "tcp"})
	if c.DeployMonitor {
		ports = append(ports, system.Port{Number: c.MonitorPublicPort, Proto: "tcp"})
	}
	ports = append(ports, system.Port{Number: 80, Proto: "tcp"})
	return ports
}

// portChecks returns the ports that must be available before installation. The
// public protocol, subscription, and monitor UI ports are probed through the
// configured domain; the monitor service port only needs to be free locally
// because it binds to 127.0.0.1 behind Nginx.
func (c Config) portChecks() []system.Port {
	checks := make([]system.Port, 0, len(c.enabled())+4)
	for _, p := range c.enabled() {
		switch p {
		case config.ProtocolRealityVision:
			checks = append(checks, system.Port{Number: c.Ports.RealityVision, Proto: "tcp", Label: "Reality Vision", Public: true})
		case config.ProtocolRealityGRPC:
			checks = append(checks, system.Port{Number: c.Ports.RealityGRPC, Proto: "tcp", Label: "Reality gRPC", Public: true})
		case config.ProtocolHysteria2:
			checks = append(checks, system.Port{Number: c.Ports.Hysteria2, Proto: "udp", Label: "Hysteria2", Public: true})
		case config.ProtocolTUIC:
			checks = append(checks, system.Port{Number: c.Ports.TUIC, Proto: "udp", Label: "TUIC", Public: true})
		case config.ProtocolAnyTLS:
			checks = append(checks, system.Port{Number: c.Ports.AnyTLS, Proto: "tcp", Label: "AnyTLS", Public: true})
		}
	}
	checks = append(checks, system.Port{Number: c.SubscribePort, Proto: "tcp", Label: "subscription/Nginx", Public: true})
	if c.DeployMonitor {
		checks = append(checks, system.Port{Number: c.MonitorPublicPort, Proto: "tcp", Label: "monitor/Nginx", Public: true})
	}
	if c.Challenge == acme.ChallengeHTTP01 {
		checks = append(checks, system.Port{Number: 80, Proto: "tcp", Label: "ACME HTTP-01", Public: true})
	}
	if c.DeployMonitor {
		checks = append(checks, system.Port{Number: c.MonitorPort, Proto: "tcp", Label: "monitor service", Public: false})
	}
	return checks
}
