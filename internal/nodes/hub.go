package nodes

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

// HubIdentity is the hub's own overlay identity: the WireGuard key pair, the
// public endpoint spokes dial, the overlay subnet, and the listen port.
type HubIdentity struct {
	PrivateKey   string
	PublicKey    string
	EndpointHost string // public host or IP spokes use to reach the hub
	ListenPort   int
	Subnet       string
}

// Endpoint returns the host:port a spoke dials to reach the hub.
func (h HubIdentity) Endpoint() string {
	port := h.ListenPort
	if port <= 0 {
		port = wgnet.DefaultListenPort
	}
	host := strings.TrimSpace(h.EndpointHost)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// LoadHubIdentity reads the hub identity, returning ok=false when it has not
// been initialized yet.
func LoadHubIdentity(layout paths.Layout) (HubIdentity, bool, error) {
	store := stateStore(layout)
	priv, err := store.ReadValue(hubPrivateKeyFile, false)
	if err != nil {
		return HubIdentity{}, false, err
	}
	if priv == "" {
		return HubIdentity{}, false, nil
	}
	if !wgnet.ValidKey(priv) {
		return HubIdentity{}, false, fmt.Errorf("stored hub WireGuard private key is invalid")
	}
	pub, err := store.ReadValue(hubPublicKeyFile, false)
	if err != nil {
		return HubIdentity{}, false, err
	}
	host, err := store.ReadValue(hubEndpointHostFile, false)
	if err != nil {
		return HubIdentity{}, false, err
	}
	portStr, err := store.ReadValue(hubListenPortFile, false)
	if err != nil {
		return HubIdentity{}, false, err
	}
	subnet, err := store.ReadValue(hubSubnetFile, false)
	if err != nil {
		return HubIdentity{}, false, err
	}
	derivedPublic, err := wgnet.PublicKeyFromPrivate(priv)
	if err != nil {
		return HubIdentity{}, false, fmt.Errorf("derive hub WireGuard public key: %w", err)
	}
	// The private key is authoritative. Deriving the public half recovers from
	// a crash between the individual atomic state-file writes without rotating
	// the Hub identity.
	if pub == "" || pub != derivedPublic {
		pub = derivedPublic
	}
	port, _ := strconv.Atoi(portStr)
	if port <= 0 {
		port = wgnet.DefaultListenPort
	}
	if subnet == "" {
		subnet = wgnet.DefaultSubnet
	}
	return HubIdentity{PrivateKey: priv, PublicKey: pub, EndpointHost: host, ListenPort: port, Subnet: subnet}, true, nil
}

// EnsureHubIdentity loads the hub identity, generating and persisting a fresh
// WireGuard key pair on first use. endpointHost is the public host/IP spokes
// dial; it is recorded (and updated if it changed) so later spoke configs use
// the current value.
func EnsureHubIdentity(layout paths.Layout, endpointHost string) (HubIdentity, error) {
	identity, ok, err := LoadHubIdentity(layout)
	if err != nil {
		return HubIdentity{}, err
	}
	if !ok {
		kp, err := wgnet.GenerateKeyPair()
		if err != nil {
			return HubIdentity{}, err
		}
		identity = HubIdentity{
			PrivateKey:   kp.PrivateKey,
			PublicKey:    kp.PublicKey,
			EndpointHost: endpointHost,
			ListenPort:   wgnet.DefaultListenPort,
			Subnet:       wgnet.DefaultSubnet,
		}
	} else if endpointHost != "" && endpointHost != identity.EndpointHost {
		identity.EndpointHost = endpointHost
	}
	if err := SaveHubIdentity(layout, identity); err != nil {
		return HubIdentity{}, err
	}
	return identity, nil
}

// SaveHubIdentity persists the hub identity fields.
func SaveHubIdentity(layout paths.Layout, h HubIdentity) error {
	store := stateStore(layout)
	writes := map[string]string{
		hubPrivateKeyFile:   h.PrivateKey,
		hubPublicKeyFile:    h.PublicKey,
		hubEndpointHostFile: h.EndpointHost,
		hubListenPortFile:   strconv.Itoa(h.ListenPort),
		hubSubnetFile:       h.Subnet,
	}
	for name, value := range writes {
		if err := store.WriteString(name, value+"\n", 0o600); err != nil {
			return err
		}
	}
	return nil
}

// HubInstalled reports whether the hub's own sing-box install has completed.
// Spokes cannot be added before the hub is installed.
func HubInstalled(layout paths.Layout) bool {
	v, _ := stateStore(layout).ReadValue(hubInstalledFile, false)
	return v == "yes"
}

// SetHubInstalled records the hub install completion flag.
func SetHubInstalled(layout paths.Layout, installed bool) error {
	value := "no"
	if installed {
		value = "yes"
	}
	return stateStore(layout).WriteString(hubInstalledFile, value+"\n", 0o600)
}

func stateStore(layout paths.Layout) state.Store {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return state.NewStore(layout.StateDir)
}
