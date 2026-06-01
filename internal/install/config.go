// Package install orchestrates the real, end-to-end sing-box deployment: system
// preparation, Nginx, certificates, sing-box core, config generation, services,
// subscriptions, and the traffic monitor. System mutations go through a
// system.Runner and filesystem writes go under a paths.Layout, so the whole
// flow is exercisable with a recording runner and a temporary root.
package install

import (
	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/system"
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

	DisplayName string
	Salt        string

	RealityServerName    string
	RealityHandshakePort int

	SubscribePort int
	MonitorPort   int

	TrafficLimitBytes uint64
	ResetDay          int
	MonitorInterface  string

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
		RealityPort:       c.RealityHandshakePort,
		RealityShortID:    c.Creds.RealityShortID,
		User:              c.userCredentials(),
		Ports:             c.Ports,
		Enabled:           c.enabled(),
	}
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
	// Subscriptions and ACME HTTP-01 need the web port(s).
	ports = append(ports, system.Port{Number: c.SubscribePort, Proto: "tcp"})
	ports = append(ports, system.Port{Number: 80, Proto: "tcp"})
	return ports
}
