// Package config renders sing-box server configuration from embedded template
// fragments and assembles them into a single validated config.json. Each
// protocol is a separate JSON fragment; Go reads the fragments and builds the
// final document (sing-box's own merge is not used).
package config

import (
	"encoding/json"
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// defaultShortID is used when no Reality short_id is supplied.
const defaultShortID = "6ba85179e30d4fc2"

// Build renders the base config plus one fragment per enabled protocol and
// returns the indented final config.json. Each rendered fragment is unmarshaled
// (validating it as JSON) before being assembled.
func Build(o ServerOptions) ([]byte, error) {
	baseStr, err := templatefs.Render("sing-box/base.json.tmpl", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("render base: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(baseStr), &root); err != nil {
		return nil, fmt.Errorf("parse base config: %w", err)
	}

	shortID := o.RealityShortID
	if shortID == "" {
		shortID = defaultShortID
	}

	inbounds := make([]any, 0, len(o.enabledSet()))
	for _, proto := range o.enabledSet() {
		tmpl, data := o.fragmentFor(proto, shortID)
		frag, err := templatefs.Render(tmpl, data)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", proto, err)
		}
		var inbound any
		if err := json.Unmarshal([]byte(frag), &inbound); err != nil {
			return nil, fmt.Errorf("parse %s fragment: %w\n%s", proto, err, frag)
		}
		inbounds = append(inbounds, inbound)
	}
	root["inbounds"] = inbounds

	return json.MarshalIndent(root, "", "  ")
}

// fragmentFor returns the template path and data map for one protocol.
func (o ServerOptions) fragmentFor(proto Protocol, shortID string) (string, map[string]any) {
	name := func(suffix string) string { return o.User.DisplayName + suffix }
	switch proto {
	case ProtocolRealityVision:
		return "sing-box/reality-vision.json.tmpl", map[string]any{
			"Port":          o.Ports.RealityVision,
			"UUID":          o.User.RealityVisionUUID,
			"Name":          name("-Reality-Vision"),
			"ServerName":    o.RealityServerName,
			"HandshakePort": positiveOrDefault(o.RealityPort, DefaultRealityHandshakePort),
			"PrivateKey":    o.RealityPrivateKey,
			"ShortID":       shortID,
		}
	case ProtocolRealityGRPC:
		return "sing-box/reality-grpc.json.tmpl", map[string]any{
			"Port":          o.Ports.RealityGRPC,
			"UUID":          o.User.RealityGRPCUUID,
			"Name":          name("-Reality-gRPC"),
			"ServerName":    o.RealityServerName,
			"HandshakePort": positiveOrDefault(o.RealityPort, DefaultRealityHandshakePort),
			"PrivateKey":    o.RealityPrivateKey,
			"ShortID":       shortID,
		}
	case ProtocolHysteria2:
		return "sing-box/hysteria2.json.tmpl", map[string]any{
			"Port":     o.Ports.Hysteria2,
			"UpMbps":   positiveOrDefault(o.Hysteria2UpMbps, DefaultHysteria2UpMbps),
			"DownMbps": positiveOrDefault(o.Hysteria2DownMbps, DefaultHysteria2DownMbps),
			"Password": o.User.HysteriaPassword,
			"Name":     name("-Hysteria2"),
			"Domain":   o.Domain,
			"CertPath": o.TLSCert,
			"KeyPath":  o.TLSKey,
		}
	case ProtocolTUIC:
		return "sing-box/tuic.json.tmpl", map[string]any{
			"Port":     o.Ports.TUIC,
			"UUID":     o.User.TUICUUID,
			"Password": o.User.TUICPassword,
			"Name":     name("-TUIC"),
			"Domain":   o.Domain,
			"CertPath": o.TLSCert,
			"KeyPath":  o.TLSKey,
		}
	case ProtocolAnyTLS:
		return "sing-box/anytls.json.tmpl", map[string]any{
			"Port":     o.Ports.AnyTLS,
			"Password": o.User.AnyTLSPassword,
			"Name":     name("-AnyTLS"),
			"Domain":   o.Domain,
			"CertPath": o.TLSCert,
			"KeyPath":  o.TLSKey,
		}
	default:
		return "", nil
	}
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
