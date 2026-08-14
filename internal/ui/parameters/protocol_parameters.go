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
	Key   string
	Label string
	Def   string
	// DefFunc derives the default from the values already collected, for a
	// parameter whose sensible default is another answer in the same form. It
	// takes precedence over Def.
	DefFunc   func(vals map[string]string) string
	Note      string
	Options   []string
	Multi     bool
	Secret    bool
	Skip      func(vals map[string]string) bool
	NoteFunc  func(vals map[string]string) string
	BadgeFunc func(vals map[string]string) string
}

const DefaultRealityServerName = "www.google.com"

// LabelRealitySNI names the same input everywhere it appears: the setup form,
// both edit forms, and the confirmation summaries.
const LabelRealitySNI = "Reality SNI"

// NotePortListen describes a protocol's public listen port wherever it is
// collected, on the hub or on a spoke.
const NotePortListen = "The port your clients connect to for this protocol."

// The credential notes say what each secret is for before offering to generate
// it, so an empty answer reads as a choice rather than an unfinished field.
const (
	noteUUID     = "The UUID your clients authenticate with."
	notePassword = "The password your clients authenticate with."
	notePort     = "The port your clients connect to."

	NoteInstallUUID     = noteUUID + "\nBlank generates one."
	NoteInstallPassword = notePassword + "\nBlank generates one."
	NoteInstallPort     = notePort + "\nBlank picks a random port."

	// The edit forms already show the current value as the default, so they say
	// what changing it costs instead of what leaving it blank does.
	noteEditRefresh  = "\nAfter a change, clients must refresh their subscription."
	noteEditUUID     = noteUUID + noteEditRefresh
	noteEditPassword = notePassword + noteEditRefresh
	noteEditPort     = notePort + noteEditRefresh
)

func RealitySNIField() Field {
	return Field{
		Key:   "reality_sni",
		Label: LabelRealitySNI,
		Def:   DefaultRealityServerName,
		Note: "The site name presented in the Reality handshake.\n" +
			"Accepts a URL or a hostname.",
	}
}

func RealitySNIEditField(current string) Field {
	f := RealitySNIField()
	f.Def = current
	if f.Def == "" {
		f.Def = DefaultRealityServerName
	}
	f.Note += "\nShared by VLESS Reality Vision and VLESS Reality gRPC."
	return f
}

func ProtocolInstallFieldsForProtocol(proto config.Protocol) []Field {
	switch proto {
	case config.ProtocolRealityVision:
		return []Field{
			{Key: "reality_vision_uuid", Label: "VLESS Reality Vision UUID (optional)", Note: NoteInstallUUID, Secret: true},
			{Key: "reality_vision_port", Label: "VLESS Reality Vision port (optional)", Note: NoteInstallPort},
		}
	case config.ProtocolRealityGRPC:
		return []Field{
			{Key: "reality_grpc_uuid", Label: "VLESS Reality gRPC UUID (optional)", Note: NoteInstallUUID, Secret: true},
			{Key: "reality_grpc_port", Label: "VLESS Reality gRPC port (optional)", Note: NoteInstallPort},
		}
	case config.ProtocolHysteria2:
		return []Field{
			{Key: "hysteria2_password", Label: "Hysteria2 password (optional)", Note: NoteInstallPassword, Secret: true},
			{Key: "hysteria2_port", Label: "Hysteria2 port (optional)", Note: NoteInstallPort},
		}
	case config.ProtocolTUIC:
		return []Field{
			{Key: "tuic_uuid", Label: "TUIC UUID (optional)", Note: NoteInstallUUID, Secret: true},
			{Key: "tuic_password", Label: "TUIC password (optional)", Note: NoteInstallPassword, Secret: true},
			{Key: "tuic_port", Label: "TUIC port (optional)", Note: NoteInstallPort},
		}
	case config.ProtocolAnyTLS:
		return []Field{
			{Key: "anytls_password", Label: "AnyTLS password (optional)", Note: NoteInstallPassword, Secret: true},
			{Key: "anytls_port", Label: "AnyTLS port (optional)", Note: NoteInstallPort},
		}
	default:
		return nil
	}
}

func ProtocolEditFieldsForProtocol(cfg deploy.Config, proto config.Protocol) []Field {
	switch proto {
	case config.ProtocolRealityVision:
		return []Field{
			{Key: "reality_vision_uuid", Label: "VLESS Reality Vision UUID", Def: cfg.Creds.RealityVisionUUID, Note: noteEditUUID, Secret: true},
			{Key: "reality_vision_port", Label: "VLESS Reality Vision port", Def: PortDefault(PortForProtocol(proto, cfg.Ports)), Note: noteEditPort},
		}
	case config.ProtocolRealityGRPC:
		return []Field{
			{Key: "reality_grpc_uuid", Label: "VLESS Reality gRPC UUID", Def: cfg.Creds.RealityGRPCUUID, Note: noteEditUUID, Secret: true},
			{Key: "reality_grpc_port", Label: "VLESS Reality gRPC port", Def: PortDefault(PortForProtocol(proto, cfg.Ports)), Note: noteEditPort},
		}
	case config.ProtocolHysteria2:
		return []Field{
			{Key: "hysteria2_password", Label: "Hysteria2 password", Def: cfg.Creds.HysteriaPassword, Note: noteEditPassword, Secret: true},
			{Key: "hysteria2_port", Label: "Hysteria2 port", Def: PortDefault(PortForProtocol(proto, cfg.Ports)), Note: noteEditPort},
		}
	case config.ProtocolTUIC:
		return []Field{
			{Key: "tuic_uuid", Label: "TUIC UUID", Def: cfg.Creds.TUICUUID, Note: noteEditUUID, Secret: true},
			{Key: "tuic_password", Label: "TUIC password", Def: cfg.Creds.TUICPassword, Note: noteEditPassword, Secret: true},
			{Key: "tuic_port", Label: "TUIC port", Def: PortDefault(PortForProtocol(proto, cfg.Ports)), Note: noteEditPort},
		}
	case config.ProtocolAnyTLS:
		return []Field{
			{Key: "anytls_password", Label: "AnyTLS password", Def: cfg.Creds.AnyTLSPassword, Note: noteEditPassword, Secret: true},
			{Key: "anytls_port", Label: "AnyTLS port", Def: PortDefault(PortForProtocol(proto, cfg.Ports)), Note: noteEditPort},
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

func ValidateProtocolParameterField(f Field, val string, _ map[string]string) error {
	return ValidateSharedParameterValue(f.Key, val)
}

func ValidateSharedParameterValue(key, val string) error {
	switch {
	case key == "reality_sni":
		_, err := NormalizeRealityServerName(val)
		return err
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

// PortForProtocol returns the listen port configured for proto, or 0 if unset.
func PortForProtocol(proto config.Protocol, ports config.Ports) int {
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
