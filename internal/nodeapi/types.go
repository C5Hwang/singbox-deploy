// Package nodeapi defines the hub↔agent control protocol carried over the
// WireGuard overlay. The hub is the client; each spoke's agent is the server.
// Transport is plain HTTP because WireGuard already encrypts and authenticates
// the link; a per-node bearer token guards against a stray process on the
// overlay. Long-running operations (install, apply, cert push, uninstall)
// stream their log output as chunked text terminated by a status sentinel.
package nodeapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// PortSet is the protocol listen-port assignment for a spoke.
type PortSet struct {
	RealityVision int `json:"realityVision"`
	RealityGRPC   int `json:"realityGRPC"`
	Hysteria2     int `json:"hysteria2"`
	TUIC          int `json:"tuic"`
	AnyTLS        int `json:"anytls"`
}

// InstallRequest is the full parameter set the hub pushes to install or
// reconfigure a spoke. The certificate is issued by the hub (DNS-01) and shipped
// inline so the agent never runs ACME itself.
type InstallRequest struct {
	// InstallTransactionID identifies the Hub add-node transaction that owns a
	// full install. The Agent persists it before the first mutation so rollback
	// can prove it is deleting only runtime created by that transaction.
	InstallTransactionID string `json:"installTransactionID,omitempty"`

	Domain               string   `json:"domain"`
	DisplayName          string   `json:"displayName"`
	RealityServerName    string   `json:"realityServerName"`
	RealityHandshakePort int      `json:"realityHandshakePort"`
	EnabledProtocols     []string `json:"enabledProtocols"`
	Ports                PortSet  `json:"ports"`
	SiteTemplate         string   `json:"siteTemplate"`

	Monitor                bool   `json:"monitor"`
	MonitorAlias           string `json:"monitorAlias"`
	MonitorInterface       string `json:"monitorInterface"`
	MonitorPort            int    `json:"monitorPort"`
	MonitorIntervalSeconds int    `json:"monitorIntervalSeconds"`
	TrafficInLimitBytes    uint64 `json:"trafficInLimitBytes"`
	TrafficOutLimitBytes   uint64 `json:"trafficOutLimitBytes"`
	TrafficTotalLimitBytes uint64 `json:"trafficTotalLimitBytes"`
	ResetDay               int    `json:"resetDay"`
	ResetHour              int    `json:"resetHour"`

	// ConfigOnly requests a lightweight reconfigure (regenerate config,
	// subscription, monitor, nginx) without reinstalling packages or the
	// sing-box core. Used when the operator edits an already-installed spoke.
	ConfigOnly bool `json:"configOnly"`

	CertificatePEM string `json:"certificatePEM"`
	PrivateKeyPEM  string `json:"privateKeyPEM"`
}

// CertRequest ships a refreshed certificate pair to a spoke (e.g. after the hub
// renews it). The agent writes the pair and restarts TLS-dependent services.
type CertRequest struct {
	Domain         string `json:"domain"`
	CertificatePEM string `json:"certificatePEM"`
	PrivateKeyPEM  string `json:"privateKeyPEM"`
}

// UninstallRequest asks the agent to tear down its sing-box deployment.
type UninstallRequest struct {
	// KeepOverlay leaves the WireGuard interface up so the hub can still reach
	// the agent to confirm teardown; the hub removes the overlay afterwards.
	KeepOverlay bool `json:"keepOverlay"`
	// RollbackTransactionID is required with KeepOverlay and must match the
	// full-install owner persisted by the Agent. This prevents a failed add-node
	// attempt from uninstalling an unrelated standalone deployment.
	RollbackTransactionID string `json:"rollbackTransactionID,omitempty"`
}

// ValidateInstallTransactionID accepts the random 128-bit lowercase hex IDs
// generated for new Hub registry entries.
func ValidateInstallTransactionID(value string) error {
	if len(value) != 32 || strings.ToLower(value) != value {
		return fmt.Errorf("invalid install transaction ID")
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("invalid install transaction ID")
	}
	return nil
}

// MaxAgentBinarySize bounds an authenticated upgrade request. The limit is
// intentionally well above current static agent binaries while preventing a
// compromised overlay peer from exhausting the agent's memory with JSON.
const MaxAgentBinarySize = 64 << 20

// UpgradeRequest carries the hub-versioned, architecture-matched agent binary
// over WireGuard. SHA256 is the lowercase hexadecimal digest of Binary.
type UpgradeRequest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Binary  []byte `json:"binary"`
}

// NewUpgradeRequest constructs a self-consistent upgrade payload.
func NewUpgradeRequest(version string, binary []byte) UpgradeRequest {
	return UpgradeRequest{
		Version: version,
		SHA256:  UpgradeDigest(binary),
		Binary:  binary,
	}
}

// UpgradeDigest returns the canonical digest representation used on the wire.
func UpgradeDigest(binary []byte) string {
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:])
}

// ValidateUpgradeRequest verifies the bounded payload, version label, and
// digest before any executable file is staged.
func ValidateUpgradeRequest(req UpgradeRequest) error {
	if !validVersion(req.Version) {
		return fmt.Errorf("invalid agent version %q", req.Version)
	}
	if len(req.Binary) == 0 {
		return fmt.Errorf("agent binary is empty")
	}
	if len(req.Binary) > MaxAgentBinarySize {
		return fmt.Errorf("agent binary is too large (%d bytes)", len(req.Binary))
	}
	want, err := hex.DecodeString(req.SHA256)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("invalid agent SHA-256")
	}
	got := sha256.Sum256(req.Binary)
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return fmt.Errorf("agent SHA-256 mismatch")
	}
	return nil
}

func validVersion(version string) bool {
	if version == "" || len(version) > 128 || strings.TrimSpace(version) != version {
		return false
	}
	for _, r := range version {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// HealthResponse reports agent liveness and deployment state.
type HealthResponse struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version"`
	Installed     bool   `json:"installed"`
	SingBoxActive bool   `json:"singBoxActive"`
	Domain        string `json:"domain,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Subscription formats the agent can return over the overlay. These mirror the
// public /s/<format> paths but are fetched by the hub for aggregation.
const (
	FormatDefault         = "default"
	FormatClashMeta       = "clashMeta"
	FormatSingBoxProfiles = "singboxProfiles"
	FormatSurge           = "surge"
)

// MonitorEndpoint identifies one read-only monitor resource exposed through
// the authenticated agent API. It is deliberately an enum rather than a URL:
// neither callers nor the agent server can turn it into an arbitrary proxy.
type MonitorEndpoint string

const (
	MonitorSummary        MonitorEndpoint = "summary"
	MonitorTrafficTrend   MonitorEndpoint = "traffic-trend"
	MonitorTrafficRecent  MonitorEndpoint = "traffic-recent"
	MonitorResourceTrend  MonitorEndpoint = "resource-trend"
	MonitorResourceRecent MonitorEndpoint = "resource-recent"
)

func (e MonitorEndpoint) paths() (apiPath, handlerPath string, ok bool) {
	switch e {
	case MonitorSummary:
		return "/api/monitor/summary", "/api/summary", true
	case MonitorTrafficTrend:
		return "/api/monitor/traffic-trend", "/api/traffic-trend", true
	case MonitorTrafficRecent:
		return "/api/monitor/traffic-recent", "/api/traffic-recent", true
	case MonitorResourceTrend:
		return "/api/monitor/resource-trend", "/api/resource-trend", true
	case MonitorResourceRecent:
		return "/api/monitor/resource-recent", "/api/resource-recent", true
	default:
		return "", "", false
	}
}

var monitorEndpoints = [...]MonitorEndpoint{
	MonitorSummary,
	MonitorTrafficTrend,
	MonitorTrafficRecent,
	MonitorResourceTrend,
	MonitorResourceRecent,
}

// Stream status sentinels. A streamed operation ends with exactly one of these
// as its final line; everything before them is log output.
const (
	doneSentinel        = "\x00SINGBOX-DEPLOY-DONE\x00"
	errorSentinelPrefix = "\x00SINGBOX-DEPLOY-ERROR\x00 "
)
