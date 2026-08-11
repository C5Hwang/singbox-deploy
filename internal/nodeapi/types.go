// Package nodeapi defines the hub↔agent control protocol carried over the
// WireGuard overlay. The hub is the client; each spoke's agent is the server.
// Transport is plain HTTP because WireGuard already encrypts and authenticates
// the link; a per-node bearer token guards against a stray process on the
// overlay. Long-running operations (install, apply, cert push, uninstall)
// stream their log output as chunked text terminated by a status sentinel.
package nodeapi

import (
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	protocolRevisionConflictMarker = "protocol-state-revision-conflict"
	trafficCycleConflictMarker     = "traffic-usage-cycle-conflict"
	maxTrafficUsageBytes           = uint64(1<<63 - 1)
)

// PortSet is the protocol listen-port assignment for a spoke.
type PortSet struct {
	RealityVision int `json:"realityVision"`
	RealityGRPC   int `json:"realityGRPC"`
	Hysteria2     int `json:"hysteria2"`
	TUIC          int `json:"tuic"`
	AnyTLS        int `json:"anytls"`
}

// ProtocolCredentials is the complete secret material for one managed spoke.
// It is returned and accepted only by the authenticated Agent API over the
// WireGuard overlay. A complete value is required for an override so a partial
// request can never accidentally erase credentials for another protocol.
type ProtocolCredentials struct {
	RealityVisionUUID string `json:"realityVisionUUID"`
	RealityGRPCUUID   string `json:"realityGRPCUUID"`
	HysteriaPassword  string `json:"hysteriaPassword"`
	TUICUUID          string `json:"tuicUUID"`
	TUICPassword      string `json:"tuicPassword"`
	AnyTLSPassword    string `json:"anyTLSPassword"`
	RealityPrivateKey string `json:"realityPrivateKey"`
	RealityPublicKey  string `json:"realityPublicKey"`
	RealityShortID    string `json:"realityShortID"`
}

// ProtocolPatch changes one installed protocol while leaving every other
// Agent-owned setting untouched. Credentials is a complete authenticated
// snapshot, but the Agent applies only the fields owned by Protocol.
type ProtocolPatch struct {
	Protocol    string              `json:"protocol"`
	Port        int                 `json:"port"`
	Credentials ProtocolCredentials `json:"credentials"`
}

// ProtocolStateResponse is the Agent's current editable protocol state. The
// response is deliberately separate from health so routine probes never move
// credential material.
type ProtocolStateResponse struct {
	// Revision is the SHA-256 digest of every editable field and credential in
	// this response. A credential edit sends it back as a compare-and-swap
	// precondition, closing the gap between the Hub's final read and the
	// Agent's mutation lock.
	Revision             string              `json:"revision"`
	Domain               string              `json:"domain"`
	RealityServerName    string              `json:"realityServerName"`
	RealityHandshakePort int                 `json:"realityHandshakePort"`
	EnabledProtocols     []string            `json:"enabledProtocols"`
	Ports                PortSet             `json:"ports"`
	Credentials          ProtocolCredentials `json:"credentials"`
}

// TrafficUsage is the current absolute inbound/outbound usage in the Agent's
// active quota cycle. CycleStart is a Unix timestamp and lets the Hub detect a
// form that crossed a monthly reset boundary before it writes anything.
type TrafficUsage struct {
	InBytes    uint64 `json:"inBytes"`
	OutBytes   uint64 `json:"outBytes"`
	CycleStart int64  `json:"cycleStart"`
}

// TrafficUsageRequest replaces the absolute usage totals for one quota cycle.
// Traffic counters are sampled continuously, so individual totals deliberately
// do not use compare-and-swap. The cycle boundary is stable and is checked to
// prevent an old form from seeding a newly-reset month with previous-cycle
// values.
type TrafficUsageRequest struct {
	InBytes            uint64 `json:"inBytes"`
	OutBytes           uint64 `json:"outBytes"`
	ExpectedCycleStart int64  `json:"expectedCycleStart"`
}

// TrafficUsageUpdate records both sides of one linearized absolute
// replacement. Previous lets callers later remove the exact artificial
// adjustment without discarding traffic sampled between form load and commit.
type TrafficUsageUpdate struct {
	Previous TrafficUsage `json:"previous"`
	Applied  TrafficUsage `json:"applied"`
	// Warning is non-empty only when the absolute counters committed but
	// immediate quota service reconciliation could not be confirmed.
	Warning string `json:"warning,omitempty"`
}

// ValidateTrafficUsage validates an Agent usage snapshot.
func ValidateTrafficUsage(usage TrafficUsage) error {
	if usage.InBytes > maxTrafficUsageBytes || usage.OutBytes > maxTrafficUsageBytes {
		return fmt.Errorf("traffic usage must not exceed %d bytes per direction", maxTrafficUsageBytes)
	}
	if usage.CycleStart <= 0 {
		return fmt.Errorf("traffic usage cycle start must be a positive Unix timestamp")
	}
	return nil
}

// ValidateTrafficUsageRequest validates an absolute usage replacement.
func ValidateTrafficUsageRequest(req TrafficUsageRequest) error {
	if req.InBytes > maxTrafficUsageBytes || req.OutBytes > maxTrafficUsageBytes {
		return fmt.Errorf("traffic usage must not exceed %d bytes per direction", maxTrafficUsageBytes)
	}
	if req.ExpectedCycleStart <= 0 {
		return fmt.Errorf("expected traffic usage cycle start must be a positive Unix timestamp")
	}
	return nil
}

// ValidateTrafficUsageUpdate verifies both snapshots and their shared quota
// cycle.
func ValidateTrafficUsageUpdate(update TrafficUsageUpdate) error {
	if err := ValidateTrafficUsage(update.Previous); err != nil {
		return fmt.Errorf("previous traffic usage: %w", err)
	}
	if err := ValidateTrafficUsage(update.Applied); err != nil {
		return fmt.Errorf("applied traffic usage: %w", err)
	}
	if update.Previous.CycleStart != update.Applied.CycleStart {
		return fmt.Errorf("traffic usage update crossed quota cycles")
	}
	if len(update.Warning) > 2048 || strings.ContainsAny(update.Warning, "\r\n") {
		return fmt.Errorf("traffic usage update warning is invalid")
	}
	return nil
}

// TrafficCycleConflict reports that a usage form crossed a quota reset
// boundary before the Agent acquired its mutation gate.
func TrafficCycleConflict() error {
	return fmt.Errorf("%s: spoke traffic quota cycle changed before the update acquired the Agent mutation lock", trafficCycleConflictMarker)
}

// IsTrafficCycleConflict recognizes the stable conflict marker after it crosses
// the Agent API.
func IsTrafficCycleConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), trafficCycleConflictMarker)
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
	// ProtocolPatch is used only by the installed-protocol editor. Unlike a
	// general reconfigure it reads the Agent's current config and changes only
	// one protocol's credential fields and listen port.
	ProtocolPatch *ProtocolPatch `json:"protocolPatch,omitempty"`
	SiteTemplate  string         `json:"siteTemplate"`
	// ReplaceProtocolState authorizes a config-only request to replace the
	// complete protocol selection, ports, and shared Reality settings from
	// this request. Ordinary config-only requests preserve those Agent-owned
	// fields so a stale monitor/display edit cannot undo a newer protocol edit.
	ReplaceProtocolState bool `json:"replaceProtocolState,omitempty"`
	// ExpectedProtocolRevision is optional for ordinary reconfiguration. A
	// protocol credential edit supplies the revision returned by
	// /api/protocol-state; the Agent verifies it while holding its mutation
	// gate before applying any change.
	ExpectedProtocolRevision string `json:"expectedProtocolRevision,omitempty"`

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

	// SingBoxVersion is the exact stable upstream release tag selected by the
	// hub, for example v1.12.4. It is mandatory for a full install. Config-only
	// requests do not download or replace the core and may omit it.
	SingBoxVersion string `json:"singBoxVersion,omitempty"`

	CertificatePEM string `json:"certificatePEM"`
	PrivateKeyPEM  string `json:"privateKeyPEM"`
}

// CoreRequest asks an agent to replace its local sing-box core with one exact
// stable upstream release.
type CoreRequest struct {
	SingBoxVersion string `json:"singBoxVersion"`
}

// ValidateStableSingBoxTag accepts only a canonical, v-prefixed, three-part
// stable semantic version. Aliases such as "latest", abbreviated versions,
// prereleases, build metadata, and surrounding whitespace are rejected.
func ValidateStableSingBoxTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("sing-box version tag is required")
	}
	normalized, err := NormalizeSingBoxVersion(tag)
	if err != nil || normalized != tag {
		return fmt.Errorf("sing-box version %q must be an exact stable tag such as v1.12.4", tag)
	}
	return nil
}

// NormalizeSingBoxVersion converts the stable version token printed by the
// sing-box binary into the canonical upstream release-tag form.
func NormalizeSingBoxVersion(version string) (string, error) {
	if version == "" || strings.TrimSpace(version) != version {
		return "", fmt.Errorf("sing-box version is empty or has surrounding whitespace")
	}
	candidate := version
	if !strings.HasPrefix(candidate, "v") {
		candidate = "v" + candidate
	}
	if !semver.IsValid(candidate) || semver.Canonical(candidate) != candidate ||
		semver.Prerelease(candidate) != "" || semver.Build(candidate) != "" {
		return "", fmt.Errorf("sing-box version %q is not an exact stable semantic version", version)
	}
	return candidate, nil
}

// ValidateInstallSingBoxVersion enforces a pinned core for full installs.
// Config-only requests never touch the core, but a supplied tag must still be
// well formed so malformed protocol data is not silently ignored.
func ValidateInstallSingBoxVersion(req InstallRequest) error {
	if req.ProtocolPatch != nil && req.ReplaceProtocolState {
		return fmt.Errorf("protocol patch and complete protocol replacement are mutually exclusive")
	}
	if req.ProtocolPatch != nil {
		if !req.ConfigOnly {
			return fmt.Errorf("protocol patches require a config-only request")
		}
		if req.ExpectedProtocolRevision == "" {
			return fmt.Errorf("protocol patches require an expected protocol revision")
		}
		if err := ValidateProtocolRevision(req.ExpectedProtocolRevision); err != nil {
			return err
		}
		if err := ValidateProtocolPatch(*req.ProtocolPatch); err != nil {
			return err
		}
	} else if req.ReplaceProtocolState {
		if !req.ConfigOnly {
			return fmt.Errorf("complete protocol replacement requires a config-only request")
		}
		if req.ExpectedProtocolRevision == "" {
			return fmt.Errorf("complete protocol replacement requires an expected protocol revision")
		}
		if err := ValidateProtocolRevision(req.ExpectedProtocolRevision); err != nil {
			return err
		}
		if err := ValidateProtocolStateReplacement(req); err != nil {
			return err
		}
	} else if req.ExpectedProtocolRevision != "" {
		return fmt.Errorf("protocol revision preconditions require a protocol patch or complete protocol replacement")
	}
	if req.ConfigOnly && req.SingBoxVersion == "" {
		return nil
	}
	if req.SingBoxVersion == "" {
		return fmt.Errorf("sing-box version is required for full install")
	}
	return ValidateStableSingBoxTag(req.SingBoxVersion)
}

// ValidateProtocolStateReplacement applies strict replacement-only semantics.
// Full installs retain their historical empty-means-all behavior; an explicit
// replacement must name each desired protocol exactly once with a valid port.
func ValidateProtocolStateReplacement(req InstallRequest) error {
	if len(req.EnabledProtocols) == 0 {
		return fmt.Errorf("complete protocol replacement requires at least one enabled protocol")
	}
	seen := make(map[string]bool, len(req.EnabledProtocols))
	for _, protocol := range req.EnabledProtocols {
		if seen[protocol] {
			return fmt.Errorf("complete protocol replacement contains duplicate protocol %q", protocol)
		}
		seen[protocol] = true
		port := 0
		switch protocol {
		case "vless-reality-vision":
			port = req.Ports.RealityVision
		case "vless-reality-grpc":
			port = req.Ports.RealityGRPC
		case "hysteria2":
			port = req.Ports.Hysteria2
		case "tuic":
			port = req.Ports.TUIC
		case "anytls":
			port = req.Ports.AnyTLS
		default:
			return fmt.Errorf("complete protocol replacement contains unsupported protocol %q", protocol)
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s replacement port must be between 1 and 65535", protocol)
		}
	}
	if req.RealityHandshakePort < 0 || req.RealityHandshakePort > 65535 {
		return fmt.Errorf("Reality handshake port must be 0 or between 1 and 65535")
	}
	return nil
}

// ValidateProtocolPatch validates the transport-level shape. Whether the
// target protocol is installed is checked against Agent state under the
// mutation gate.
func ValidateProtocolPatch(patch ProtocolPatch) error {
	switch patch.Protocol {
	case "vless-reality-vision", "vless-reality-grpc", "hysteria2", "tuic", "anytls":
	default:
		return fmt.Errorf("unsupported protocol patch target %q", patch.Protocol)
	}
	if patch.Port < 1 || patch.Port > 65535 {
		return fmt.Errorf("protocol patch port must be between 1 and 65535")
	}
	return validateProtocolCredentialsFor([]string{patch.Protocol}, &patch.Credentials)
}

// ProtocolStateRevision returns the canonical digest used for an Agent-side
// compare-and-swap. Revision itself is excluded from the digest.
func ProtocolStateRevision(state ProtocolStateResponse) (string, error) {
	state.Revision = ""
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateProtocolStateResponse verifies the credential material and that the
// response revision describes exactly the returned state.
func ValidateProtocolStateResponse(state ProtocolStateResponse) error {
	if err := validateProtocolCredentialsFor(state.EnabledProtocols, &state.Credentials); err != nil {
		return err
	}
	if err := ValidateProtocolRevision(state.Revision); err != nil {
		return err
	}
	want, err := ProtocolStateRevision(state)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(state.Revision), []byte(want)) != 1 {
		return fmt.Errorf("protocol state revision does not match the returned settings")
	}
	return nil
}

// ValidateProtocolRevision accepts only a lowercase SHA-256 digest.
func ValidateProtocolRevision(revision string) error {
	if len(revision) != sha256.Size*2 || strings.ToLower(revision) != revision {
		return fmt.Errorf("protocol state revision must be a lowercase SHA-256 digest")
	}
	raw, err := hex.DecodeString(revision)
	if err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("protocol state revision must be a lowercase SHA-256 digest")
	}
	return nil
}

// ProtocolRevisionConflict reports that a compare-and-swap precondition no
// longer matches. The stable marker survives the Agent's streamed error
// transport so the Hub can restore its registry without pushing a stale remote
// rollback over the concurrent winner.
func ProtocolRevisionConflict() error {
	return fmt.Errorf("%s: spoke protocol settings changed before the update acquired the Agent mutation lock", protocolRevisionConflictMarker)
}

// IsProtocolRevisionConflict recognizes the stable conflict marker after it
// crosses the streamed Agent API.
func IsProtocolRevisionConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), protocolRevisionConflictMarker)
}

// ValidateProtocolCredentials validates a complete modern credential set.
// Protocol state and patch validation use the protocol-scoped helper below so
// legacy installs may leave disabled-protocol credentials empty.
func ValidateProtocolCredentials(creds *ProtocolCredentials) error {
	return validateProtocolCredentialsFor([]string{
		"vless-reality-vision",
		"vless-reality-grpc",
		"hysteria2",
		"tuic",
		"anytls",
	}, creds)
}

func validateProtocolCredentialsFor(protocols []string, creds *ProtocolCredentials) error {
	if creds == nil {
		return nil
	}
	reality := false
	for _, protocol := range protocols {
		switch protocol {
		case "vless-reality-vision":
			reality = true
			if !validUUID(creds.RealityVisionUUID) {
				return fmt.Errorf("Reality Vision UUID must be an RFC 4122 UUID")
			}
		case "vless-reality-grpc":
			reality = true
			if !validUUID(creds.RealityGRPCUUID) {
				return fmt.Errorf("Reality gRPC UUID must be an RFC 4122 UUID")
			}
		case "hysteria2":
			if strings.TrimSpace(creds.HysteriaPassword) == "" {
				return fmt.Errorf("Hysteria2 password is required")
			}
		case "tuic":
			if !validUUID(creds.TUICUUID) {
				return fmt.Errorf("TUIC UUID must be an RFC 4122 UUID")
			}
			if strings.TrimSpace(creds.TUICPassword) == "" {
				return fmt.Errorf("TUIC password is required")
			}
		case "anytls":
			if strings.TrimSpace(creds.AnyTLSPassword) == "" {
				return fmt.Errorf("AnyTLS password is required")
			}
		default:
			return fmt.Errorf("unsupported enabled protocol %q", protocol)
		}
	}
	if !reality {
		return nil
	}
	return validateRealityCredentials(creds)
}

func validateRealityCredentials(creds *ProtocolCredentials) error {
	shortID, err := hex.DecodeString(creds.RealityShortID)
	if err != nil || len(shortID) == 0 || len(shortID) > 8 {
		return fmt.Errorf("Reality short ID must be 2-16 hexadecimal characters")
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(creds.RealityPrivateKey)
	if err != nil {
		return fmt.Errorf("Reality private key must be raw URL-safe base64")
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return fmt.Errorf("Reality private key is not a valid X25519 key")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(creds.RealityPublicKey)
	if err != nil {
		return fmt.Errorf("Reality public key must be raw URL-safe base64")
	}
	if subtle.ConstantTimeCompare(privateKey.PublicKey().Bytes(), publicBytes) != 1 {
		return fmt.Errorf("Reality public key does not match the private key")
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range value {
		switch i {
		case 8, 13, 18, 23:
			if value[i] != '-' {
				return false
			}
		default:
			b := value[i]
			if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
				return false
			}
		}
	}
	return true
}

// ValidateCoreRequest validates the exact target of a core mutation.
func ValidateCoreRequest(req CoreRequest) error {
	return ValidateStableSingBoxTag(req.SingBoxVersion)
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
	OK             bool   `json:"ok"`
	Version        string `json:"version"`
	Installed      bool   `json:"installed"`
	SingBoxVersion string `json:"singBoxVersion,omitempty"`
	SingBoxActive  bool   `json:"singBoxActive"`
	Domain         string `json:"domain,omitempty"`
	Error          string `json:"error,omitempty"`
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
	MonitorPingTrend      MonitorEndpoint = "ping-trend"
	MonitorIPTraffic      MonitorEndpoint = "ip-traffic"
	MonitorIPDetail       MonitorEndpoint = "ip-detail"
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
	case MonitorPingTrend:
		return "/api/monitor/ping-trend", "/api/ping-trend", true
	case MonitorIPTraffic:
		return "/api/monitor/ip-traffic", "/api/ip-traffic", true
	case MonitorIPDetail:
		return "/api/monitor/ip-detail", "/api/ip-detail", true
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
	MonitorPingTrend,
	MonitorIPTraffic,
	MonitorIPDetail,
}

// Stream status sentinels. A streamed operation ends with exactly one of these
// as its final line; everything before them is log output.
const (
	doneSentinel        = "\x00SINGBOX-DEPLOY-DONE\x00"
	errorSentinelPrefix = "\x00SINGBOX-DEPLOY-ERROR\x00 "
)
