package parameters

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// Field describes one UI parameter without depending on the ui package.
type Field struct {
	Key       string
	Label     string
	Def       string
	Note      string
	Options   []string
	Multi     bool
	Skip      func(vals map[string]string) bool
	NoteFunc  func(vals map[string]string) string
	BadgeFunc func(vals map[string]string) string
}

func RealitySNIField() Field {
	return Field{
		Key:   "reality_sni",
		Label: "Reality URL/SNI (camouflage server)",
		Def:   "www.microsoft.com",
		Note:  "You may enter a URL or host; the host is used for the Reality handshake.",
	}
}

func RealitySNIEditField(current string) Field {
	f := RealitySNIField()
	f.Label = "Reality URL/SNI (camouflage server)"
	f.Def = current
	if f.Def == "" {
		f.Def = "www.microsoft.com"
	}
	f.Note = "Updates the shared Reality handshake SNI for Reality Vision and Reality gRPC."
	return f
}

func ProtocolInstallFieldsForProtocol(proto config.Protocol) []Field {
	switch proto {
	case config.ProtocolRealityVision:
		return []Field{
			{Key: "reality_vision_uuid", Label: "Reality Vision UUID (optional)", Note: "Blank generates a random UUID."},
			{Key: "reality_vision_port", Label: "Reality Vision port (optional)", Note: "Blank chooses a random listen port."},
		}
	case config.ProtocolRealityGRPC:
		return []Field{
			{Key: "reality_grpc_uuid", Label: "Reality gRPC UUID (optional)", Note: "Blank generates a random UUID."},
			{Key: "reality_grpc_port", Label: "Reality gRPC port (optional)", Note: "Blank chooses a random listen port."},
		}
	case config.ProtocolHysteria2:
		return []Field{
			{Key: "hysteria2_password", Label: "Hysteria2 password (optional)", Note: "Blank generates a random password."},
			{Key: "hysteria2_port", Label: "Hysteria2 port (optional)", Note: "Blank chooses a random listen port."},
			{Key: "hysteria2_up_mbps", Label: "Hysteria2 up limit", Def: strconv.Itoa(config.DefaultHysteria2UpMbps), Note: "Sets the Hysteria2 upload bandwidth limit in Mbps."},
			{Key: "hysteria2_down_mbps", Label: "Hysteria2 down limit", Def: strconv.Itoa(config.DefaultHysteria2DownMbps), Note: "Sets the Hysteria2 download bandwidth limit in Mbps."},
		}
	case config.ProtocolTUIC:
		return []Field{
			{Key: "tuic_uuid", Label: "TUIC UUID (optional)", Note: "Blank generates a random UUID."},
			{Key: "tuic_password", Label: "TUIC password (optional)", Note: "Blank generates a random password."},
			{Key: "tuic_port", Label: "TUIC port (optional)", Note: "Blank chooses a random listen port."},
		}
	case config.ProtocolAnyTLS:
		return []Field{
			{Key: "anytls_password", Label: "AnyTLS password (optional)", Note: "Blank generates a random password."},
			{Key: "anytls_port", Label: "AnyTLS port (optional)", Note: "Blank chooses a random listen port."},
		}
	default:
		return nil
	}
}

func ProtocolEditFieldsForProtocol(cfg deploy.Config, proto config.Protocol) []Field {
	switch proto {
	case config.ProtocolRealityVision:
		return []Field{
			{Key: "reality_vision_uuid", Label: "Reality Vision UUID", Def: cfg.Creds.RealityVisionUUID},
			{Key: "reality_vision_port", Label: "Reality Vision port", Def: PortDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolRealityGRPC:
		return []Field{
			{Key: "reality_grpc_uuid", Label: "Reality gRPC UUID", Def: cfg.Creds.RealityGRPCUUID},
			{Key: "reality_grpc_port", Label: "Reality gRPC port", Def: PortDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolHysteria2:
		return []Field{
			{Key: "hysteria2_password", Label: "Hysteria2 password", Def: cfg.Creds.HysteriaPassword},
			{Key: "hysteria2_port", Label: "Hysteria2 port", Def: PortDefault(installedPort(proto, cfg.Ports))},
			{Key: "hysteria2_up_mbps", Label: "Hysteria2 up limit", Def: MbpsDefault(cfg.Hysteria2UpMbps, config.DefaultHysteria2UpMbps), Note: "Sets the Hysteria2 upload bandwidth limit in Mbps."},
			{Key: "hysteria2_down_mbps", Label: "Hysteria2 down limit", Def: MbpsDefault(cfg.Hysteria2DownMbps, config.DefaultHysteria2DownMbps), Note: "Sets the Hysteria2 download bandwidth limit in Mbps."},
		}
	case config.ProtocolTUIC:
		return []Field{
			{Key: "tuic_uuid", Label: "TUIC UUID", Def: cfg.Creds.TUICUUID},
			{Key: "tuic_password", Label: "TUIC password", Def: cfg.Creds.TUICPassword},
			{Key: "tuic_port", Label: "TUIC port", Def: PortDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolAnyTLS:
		return []Field{
			{Key: "anytls_password", Label: "AnyTLS password", Def: cfg.Creds.AnyTLSPassword},
			{Key: "anytls_port", Label: "AnyTLS port", Def: PortDefault(installedPort(proto, cfg.Ports))},
		}
	default:
		return nil
	}
}

func PortDefault(port int) string {
	if port <= 0 {
		return ""
	}
	return strconv.Itoa(port)
}

func MbpsDefault(value, fallback int) string {
	if value <= 0 {
		value = fallback
	}
	return strconv.Itoa(value)
}

func ValidateProtocolParameterField(f Field, val string, _ map[string]string) error {
	return ValidateSharedParameterValue(f.Key, val)
}

func ValidateSharedParameterValue(key, val string) error {
	switch {
	case key == "reality_sni":
		_, err := NormalizeRealityServerName(val)
		return err
	case key == "hysteria2_up_mbps" || key == "hysteria2_down_mbps":
		if val == "" {
			return nil
		}
		mbps, err := strconv.Atoi(val)
		if err != nil || mbps <= 0 {
			return fmt.Errorf("bandwidth must be a positive integer Mbps value")
		}
	case strings.HasSuffix(key, "_port"):
		if val == "" {
			return nil
		}
		port, err := strconv.Atoi(val)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	case strings.HasSuffix(key, "_uuid"):
		if val != "" && !ValidUUID(val) {
			return fmt.Errorf("uuid must be an RFC 4122 value")
		}
	}
	return nil
}

func NormalizeRealityServerName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("reality URL/SNI is required")
	}
	if !strings.Contains(raw, "://") && strings.Contains(raw, "/") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("reality URL/SNI host is required")
		}
		return host, nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	if raw == "" || strings.ContainsAny(raw, "/?#") {
		return "", fmt.Errorf("reality URL/SNI must be a URL or host")
	}
	return raw, nil
}

func ValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func installedPort(proto config.Protocol, ports config.Ports) int {
	switch proto {
	case config.ProtocolRealityVision:
		return ports.RealityVision
	case config.ProtocolRealityGRPC:
		return ports.RealityGRPC
	case config.ProtocolHysteria2:
		return ports.Hysteria2
	case config.ProtocolTUIC:
		return ports.TUIC
	case config.ProtocolAnyTLS:
		return ports.AnyTLS
	default:
		return 0
	}
}
