package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/install"
)

func realitySNIField() field {
	return field{
		key:   "reality_sni",
		label: "Reality URL/SNI (camouflage server)",
		def:   "www.microsoft.com",
		note:  "You may enter a URL or host; the host is used for the Reality handshake.",
	}
}

func realitySNIEditField(current string) field {
	f := realitySNIField()
	f.label = "Reality URL/SNI (camouflage server)"
	f.def = current
	if f.def == "" {
		f.def = "www.microsoft.com"
	}
	f.note = "Updates the shared Reality handshake SNI for Reality Vision and Reality gRPC."
	return f
}

func protocolInstallFieldsForProtocol(proto config.Protocol) []field {
	switch proto {
	case config.ProtocolRealityVision:
		return []field{
			{key: "reality_vision_uuid", label: "Reality Vision UUID (optional)", note: "Blank generates a random UUID."},
			{key: "reality_vision_port", label: "Reality Vision port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolRealityGRPC:
		return []field{
			{key: "reality_grpc_uuid", label: "Reality gRPC UUID (optional)", note: "Blank generates a random UUID."},
			{key: "reality_grpc_port", label: "Reality gRPC port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolHysteria2:
		return []field{
			{key: "hysteria2_password", label: "Hysteria2 password (optional)", note: "Blank generates a random password."},
			{key: "hysteria2_port", label: "Hysteria2 port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolTUIC:
		return []field{
			{key: "tuic_uuid", label: "TUIC UUID (optional)", note: "Blank generates a random UUID."},
			{key: "tuic_password", label: "TUIC password (optional)", note: "Blank generates a random password."},
			{key: "tuic_port", label: "TUIC port (optional)", note: "Blank chooses a random listen port."},
		}
	case config.ProtocolAnyTLS:
		return []field{
			{key: "anytls_password", label: "AnyTLS password (optional)", note: "Blank generates a random password."},
			{key: "anytls_port", label: "AnyTLS port (optional)", note: "Blank chooses a random listen port."},
		}
	default:
		return nil
	}
}

func protocolEditFieldsForProtocol(cfg install.Config, proto config.Protocol) []field {
	switch proto {
	case config.ProtocolRealityVision:
		return []field{
			{key: "reality_vision_uuid", label: "Reality Vision UUID", def: cfg.Creds.RealityVisionUUID},
			{key: "reality_vision_port", label: "Reality Vision port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolRealityGRPC:
		return []field{
			{key: "reality_grpc_uuid", label: "Reality gRPC UUID", def: cfg.Creds.RealityGRPCUUID},
			{key: "reality_grpc_port", label: "Reality gRPC port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolHysteria2:
		return []field{
			{key: "hysteria2_password", label: "Hysteria2 password", def: cfg.Creds.HysteriaPassword},
			{key: "hysteria2_port", label: "Hysteria2 port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolTUIC:
		return []field{
			{key: "tuic_uuid", label: "TUIC UUID", def: cfg.Creds.TUICUUID},
			{key: "tuic_password", label: "TUIC password", def: cfg.Creds.TUICPassword},
			{key: "tuic_port", label: "TUIC port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	case config.ProtocolAnyTLS:
		return []field{
			{key: "anytls_password", label: "AnyTLS password", def: cfg.Creds.AnyTLSPassword},
			{key: "anytls_port", label: "AnyTLS port", def: portDefault(installedPort(proto, cfg.Ports))},
		}
	default:
		return nil
	}
}

func portDefault(port int) string {
	if port <= 0 {
		return ""
	}
	return strconv.Itoa(port)
}

func validateProtocolParameterField(f field, val string, _ map[string]string) error {
	return validateSharedParameterValue(f.key, val)
}

func validateSharedParameterValue(key, val string) error {
	switch {
	case key == "reality_sni":
		_, err := normalizeRealityServerName(val)
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
		if val != "" && !validUUID(val) {
			return fmt.Errorf("uuid must be an RFC 4122 value")
		}
	}
	return nil
}
