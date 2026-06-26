package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// NodesDirName is the subdirectory under StateDir that holds per-node state.
const NodesDirName = "nodes"

// MasterPrivateKeyFile holds the master's WireGuard private key, written
// alongside the node registry under StateDir.
const MasterPrivateKeyFile = "wg_private_key"

// MasterPublicKeyFile holds the master's WireGuard public key.
const MasterPublicKeyFile = "wg_public_key"

// Registry persists and retrieves the master's set of registered nodes.
type Registry struct {
	layout paths.Layout
}

// NewRegistry returns a Registry rooted at layout.StateDir/nodes.
func NewRegistry(layout paths.Layout) Registry {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return Registry{layout: layout}
}

// Dir returns the absolute path of the nodes registry directory.
func (r Registry) Dir() string {
	return filepath.Join(r.layout.StateDir, NodesDirName)
}

// nodeDir returns the path of one node's state directory.
func (r Registry) nodeDir(id string) string {
	return filepath.Join(r.Dir(), id)
}

// List returns every registered node, ordered by ID ascending.
func (r Registry) List() ([]Node, error) {
	entries, err := os.ReadDir(r.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var nodes []Node
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		node, err := r.Load(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("load node %s: %w", entry.Name(), err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// Load returns the node with the given ID.
func (r Registry) Load(id string) (Node, error) {
	root := r.nodeDir(id)
	if _, err := os.Stat(root); err != nil {
		return Node{}, err
	}
	node := Node{
		ID:                     id,
		Alias:                  readString(root, "alias"),
		PublicIP:               readString(root, "public_ip"),
		Domain:                 readString(root, "domain"),
		WGIP:                   readString(root, "wg_ip"),
		WGPublicKey:            readString(root, "wg_public_key"),
		APIToken:               readString(root, "api_token"),
		RealityServerName:      readString(root, "reality_server_name"),
		RealityHandshakePort:   readInt(root, "reality_handshake_port", config.DefaultRealityHandshakePort),
		MonitorEnabled:         readBool(root, "monitor_enabled", true),
		MonitorAlias:           readString(root, "monitor_alias"),
		MonitorInterface:       readString(root, "monitor_interface"),
		MonitorIntervalSeconds: readInt(root, "monitor_interval_seconds", deploy.DefaultMonitorIntervalSeconds),
		TrafficInLimitBytes:    readUint(root, "traffic_in_limit_bytes"),
		TrafficOutLimitBytes:   readUint(root, "traffic_out_limit_bytes"),
		TrafficTotalLimitBytes: readUint(root, "traffic_total_limit_bytes"),
		ResetDay:               readInt(root, "reset_day", deploy.DefaultResetDay),
		ResetHour:              readInt(root, "reset_hour", deploy.DefaultResetHour),
		Ports: config.Ports{
			RealityVision: readInt(root, "reality_vision_port", 0),
			RealityGRPC:   readInt(root, "reality_grpc_port", 0),
			Hysteria2:     readInt(root, "hysteria2_port", 0),
			TUIC:          readInt(root, "tuic_port", 0),
			AnyTLS:        readInt(root, "anytls_port", 0),
		},
		Creds: deploy.Credentials{
			RealityVisionUUID: readString(root, "reality_vision_uuid"),
			RealityGRPCUUID:   readString(root, "reality_grpc_uuid"),
			HysteriaPassword:  readString(root, "hysteria2_password"),
			TUICUUID:          readString(root, "tuic_uuid"),
			TUICPassword:      readString(root, "tuic_password"),
			AnyTLSPassword:    readString(root, "anytls_password"),
			RealityPrivateKey: readString(root, "reality_private_key"),
			RealityPublicKey:  readString(root, "reality_public_key"),
			RealityShortID:    readString(root, "reality_short_id"),
		},
	}
	node.EnabledProtocols = parseProtocols(readString(root, "enabled_protocols"))
	return node, nil
}

// Save writes every field of the node as an individual state file. Existing
// files are overwritten; fields with empty values write empty files (the
// caller is expected to set required fields before saving).
func (r Registry) Save(node Node) error {
	if strings.TrimSpace(node.ID) == "" {
		return fmt.Errorf("node ID is empty")
	}
	dir := r.nodeDir(node.ID)
	values := map[string]string{
		"alias":                     node.Alias,
		"public_ip":                 node.PublicIP,
		"domain":                    node.Domain,
		"wg_ip":                     node.WGIP,
		"wg_public_key":             node.WGPublicKey,
		"api_token":                 node.APIToken,
		"enabled_protocols":         protocolsToString(node.EnabledProtocols),
		"reality_server_name":       node.RealityServerName,
		"reality_handshake_port":    strconv.Itoa(node.RealityHandshakePort),
		"reality_vision_uuid":       node.Creds.RealityVisionUUID,
		"reality_grpc_uuid":         node.Creds.RealityGRPCUUID,
		"hysteria2_password":        node.Creds.HysteriaPassword,
		"tuic_uuid":                 node.Creds.TUICUUID,
		"tuic_password":             node.Creds.TUICPassword,
		"anytls_password":           node.Creds.AnyTLSPassword,
		"reality_private_key":       node.Creds.RealityPrivateKey,
		"reality_public_key":        node.Creds.RealityPublicKey,
		"reality_short_id":          node.Creds.RealityShortID,
		"reality_vision_port":       strconv.Itoa(node.Ports.RealityVision),
		"reality_grpc_port":         strconv.Itoa(node.Ports.RealityGRPC),
		"hysteria2_port":            strconv.Itoa(node.Ports.Hysteria2),
		"tuic_port":                 strconv.Itoa(node.Ports.TUIC),
		"anytls_port":               strconv.Itoa(node.Ports.AnyTLS),
		"monitor_enabled":           strconv.FormatBool(node.MonitorEnabled),
		"monitor_alias":             node.MonitorAlias,
		"monitor_interface":         node.MonitorInterface,
		"monitor_interval_seconds":  strconv.Itoa(node.MonitorIntervalSeconds),
		"traffic_in_limit_bytes":    strconv.FormatUint(node.TrafficInLimitBytes, 10),
		"traffic_out_limit_bytes":   strconv.FormatUint(node.TrafficOutLimitBytes, 10),
		"traffic_total_limit_bytes": strconv.FormatUint(node.TrafficTotalLimitBytes, 10),
		"reset_day":                 strconv.Itoa(node.ResetDay),
		"reset_hour":                strconv.Itoa(node.ResetHour),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(dir, filepath.Clean(name)), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the node directory completely.
func (r Registry) Delete(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("node ID is empty")
	}
	return os.RemoveAll(r.nodeDir(id))
}

// AllocateNextID returns the smallest unused 3-digit ID in the registry.
// Recycled IDs from deleted nodes are reused.
func (r Registry) AllocateNextID() (string, error) {
	taken := map[int]bool{}
	entries, err := os.ReadDir(r.Dir())
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		n, err := strconv.Atoi(entry.Name())
		if err != nil || n <= 0 {
			continue
		}
		taken[n] = true
	}
	for i := 1; i <= 999; i++ {
		if !taken[i] {
			return fmt.Sprintf("%03d", i), nil
		}
	}
	return "", fmt.Errorf("node registry full (999 IDs in use)")
}

// AssignedWGIPs returns every WireGuard IP currently allocated to a node.
// Used by the IP allocator to pick the next free address.
func (r Registry) AssignedWGIPs() ([]string, error) {
	nodes, err := r.List()
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, n := range nodes {
		if ip := strings.TrimSpace(n.WGIP); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}

func readString(root, name string) string {
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(root, name string, fallback int) int {
	value := readString(root, name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func readBool(root, name string, fallback bool) bool {
	value := readString(root, name)
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return b
}

func readUint(root, name string) uint64 {
	value := readString(root, name)
	if value == "" {
		return 0
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseProtocols(value string) []config.Protocol {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	allowed := map[config.Protocol]bool{}
	for _, p := range config.AllProtocols {
		allowed[p] = true
	}
	var out []config.Protocol
	seen := map[config.Protocol]bool{}
	for _, part := range strings.Split(value, ",") {
		p := config.Protocol(strings.TrimSpace(part))
		if !allowed[p] || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	// Re-order by canonical order from config.AllProtocols.
	ordered := make([]config.Protocol, 0, len(out))
	for _, p := range config.AllProtocols {
		if seen[p] {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

func protocolsToString(protocols []config.Protocol) string {
	parts := make([]string, 0, len(protocols))
	for _, p := range protocols {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, ",")
}
