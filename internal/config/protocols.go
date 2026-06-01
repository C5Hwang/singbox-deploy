package config

// Protocol identifies one supported sing-box inbound protocol.
type Protocol string

const (
	ProtocolRealityVision Protocol = "reality-vision"
	ProtocolRealityGRPC   Protocol = "reality-grpc"
	ProtocolHysteria2     Protocol = "hysteria2"
	ProtocolTUIC          Protocol = "tuic"
	ProtocolAnyTLS        Protocol = "anytls"
)

// AllProtocols is the full, ordered set of supported protocols. "All install"
// enables every entry; custom install selects a subset.
var AllProtocols = []Protocol{
	ProtocolRealityVision,
	ProtocolRealityGRPC,
	ProtocolHysteria2,
	ProtocolTUIC,
	ProtocolAnyTLS,
}

// UserCredentials holds the single managed user's per-protocol secrets.
type UserCredentials struct {
	DisplayName       string
	RealityVisionUUID string
	RealityGRPCUUID   string
	HysteriaPassword  string
	TUICUUID          string
	TUICPassword      string
	AnyTLSPassword    string
}

// Ports holds the listen port for each protocol.
type Ports struct {
	RealityVision int
	RealityGRPC   int
	Hysteria2     int
	TUIC          int
	AnyTLS        int
}

// ServerOptions is the full input needed to render a sing-box server config.
type ServerOptions struct {
	Domain            string
	TLSCert           string
	TLSKey            string
	RealityPrivateKey string
	RealityServerName string
	RealityPort       int
	RealityShortID    string
	User              UserCredentials
	Ports             Ports
	// Enabled selects which protocols to render. Empty means all supported
	// protocols (the "all install" default).
	Enabled []Protocol
}

// enabledSet returns the protocols to render, defaulting to all when unset.
func (o ServerOptions) enabledSet() []Protocol {
	if len(o.Enabled) == 0 {
		return AllProtocols
	}
	return o.Enabled
}
