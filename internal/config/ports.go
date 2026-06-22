package config

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// ProtocolPortMin and ProtocolPortMax bound the random port range for
// sing-box inbound listen_port. The range is unprivileged and steers clear
// of the common ephemeral range used by clients on the same host.
const (
	ProtocolPortMin = 20000
	ProtocolPortMax = 59999
)

// MasqueradeSitePort is the TCP port reserved for the nginx masquerade
// site. No protocol inbound may bind to it.
const MasqueradeSitePort = 443

// ValidateProtocolPort returns nil if port is a legal sing-box inbound
// listen port given the currently-used port set. 443 is rejected
// unconditionally because it is the masquerade site. used may be nil.
func ValidateProtocolPort(port int, used map[int]bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if port == MasqueradeSitePort {
		return fmt.Errorf("protocol port 443 is reserved for the masquerade site; choose another")
	}
	if used[port] {
		return fmt.Errorf("port %d conflicts with another selected port", port)
	}
	return nil
}

// RandomProtocolPort picks an unused port in [ProtocolPortMin, ProtocolPortMax],
// skipping 443 and anything already in used. On success it marks the chosen
// port as used so callers can keep calling it in a loop.
func RandomProtocolPort(used map[int]bool) (int, error) {
	span := big.NewInt(int64(ProtocolPortMax - ProtocolPortMin + 1))
	for range 1000 {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			return 0, err
		}
		port := int(n.Int64()) + ProtocolPortMin
		if port == MasqueradeSitePort {
			continue
		}
		if used[port] {
			continue
		}
		used[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("could not choose an unused random port")
}
