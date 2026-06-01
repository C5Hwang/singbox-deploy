package subscription

import (
	"encoding/base64"
	"strings"
)

// Node is one server node in a subscription, identified by its protocol and a
// ready-to-use universal link.
type Node struct {
	Name     string
	Protocol string
	Link     string
}

// supportedProtocols lists the protocols representable in subscription output.
// sing-box's Reality inbounds are vless; Naive and other excluded protocols are
// absent by construction.
var supportedProtocols = map[string]bool{
	"vless":     true,
	"hysteria2": true,
	"tuic":      true,
	"anytls":    true,
}

// Supported reports whether a protocol can appear in subscription output.
func Supported(protocol string) bool { return supportedProtocols[protocol] }

// GenerateDefault joins the universal links of supported nodes, one per line.
// Unsupported protocols are skipped. The caller base64-encodes the result for
// the /s/default endpoint.
func GenerateDefault(nodes []Node) string {
	var lines []string
	for _, n := range nodes {
		if supportedProtocols[n.Protocol] && n.Link != "" {
			lines = append(lines, n.Link)
		}
	}
	return strings.Join(lines, "\n")
}

// EncodeBase64 standard-base64-encodes a subscription body.
func EncodeBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// DecodeBase64 decodes a base64 subscription body, tolerating both standard and
// raw (unpadded) encodings used by various clients.
func DecodeBase64(s string) (string, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(b), nil
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
