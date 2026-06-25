package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AgentClient calls a single node's agent HTTP API over the WireGuard subnet.
// All requests carry an Authorization: Bearer <APIToken> header.
type AgentClient struct {
	Node       Node
	HTTPClient *http.Client
}

// NewAgentClient returns a client targeting node. A default HTTP client with
// a 30s timeout is used unless one was assigned to the returned struct.
func NewAgentClient(node Node) *AgentClient {
	return &AgentClient{
		Node:       node,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Status is the response payload from GET /api/status.
type Status struct {
	NodeVersion      string   `json:"nodeVersion"`
	CoreVersion      string   `json:"coreVersion"`
	MonitorVersion   string   `json:"monitorVersion"`
	SingBoxActive    bool     `json:"singboxActive"`
	MonitorActive    bool     `json:"monitorActive"`
	EnabledProtocols []string `json:"enabledProtocols"`
	Uptime           string   `json:"uptime"`
	WGIP             string   `json:"wgIP"`
	CertExpiry       string   `json:"certExpiry,omitempty"`
}

// ConfigUpdate is the payload accepted by POST /api/config/update.
type ConfigUpdate struct {
	EnabledProtocols     []string          `json:"enabledProtocols"`
	ProtocolPorts        map[string]int    `json:"protocolPorts"`
	Domain               string            `json:"domain"`
	Credentials          map[string]string `json:"credentials"`
	RealityServerName    string            `json:"realityServerName,omitempty"`
	RealityHandshakePort int               `json:"realityHandshakePort,omitempty"`
}

// MonitorUpdate is the payload accepted by POST /api/monitor/config. When
// Disabled is true the node tears down singbox-deploy-monitor (stops/disables
// the unit, removes the unit file and binary) and the other fields are
// ignored.
type MonitorUpdate struct {
	Disabled         bool   `json:"disabled,omitempty"`
	Interface        string `json:"interface"`
	SamplingInterval string `json:"samplingInterval"`
	InLimitBytes     uint64 `json:"inLimitBytes"`
	OutLimitBytes    uint64 `json:"outLimitBytes"`
	TotalLimitBytes  uint64 `json:"totalLimitBytes"`
	ResetDay         int    `json:"resetDay"`
	ResetHour        int    `json:"resetHour"`
}

// UpgradeRequest is the payload accepted by POST /api/upgrade.
type UpgradeRequest struct {
	Version     string `json:"version,omitempty"`
	CoreVersion string `json:"coreVersion,omitempty"`
}

// CertDeploy is the payload accepted by POST /api/cert/deploy.
type CertDeploy struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// SiteDeploy is the payload accepted by POST /api/site/deploy. Triggers the
// agent to install Nginx (if missing), deploy the named masquerade site
// template, write the node Nginx config, and start Nginx.
type SiteDeploy struct {
	Domain       string `json:"domain"`
	SiteTemplate string `json:"siteTemplate"`
}

// Status fetches GET /api/status.
func (c *AgentClient) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := c.do(ctx, http.MethodGet, "/api/status", nil, &status); err != nil {
		return Status{}, err
	}
	return status, nil
}

// UpdateConfig applies a new protocol/credential config to the node.
func (c *AgentClient) UpdateConfig(ctx context.Context, req ConfigUpdate) error {
	return c.do(ctx, http.MethodPost, "/api/config/update", req, nil)
}

// ReloadConfig restarts the node's sing-box service.
func (c *AgentClient) ReloadConfig(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/config/reload", nil, nil)
}

// UpdateMonitor reconfigures the node's monitor (quota + sampling).
func (c *AgentClient) UpdateMonitor(ctx context.Context, req MonitorUpdate) error {
	return c.do(ctx, http.MethodPost, "/api/monitor/config", req, nil)
}

// Upgrade asks the node to download replacement binaries from GitHub Release.
func (c *AgentClient) Upgrade(ctx context.Context, req UpgradeRequest) error {
	return c.do(ctx, http.MethodPost, "/api/upgrade", req, nil)
}

// DeployCert pushes a renewed certificate (PEM) to the node.
func (c *AgentClient) DeployCert(ctx context.Context, req CertDeploy) error {
	return c.do(ctx, http.MethodPost, "/api/cert/deploy", req, nil)
}

// DeploySite asks the node to install Nginx and the masquerade site.
func (c *AgentClient) DeploySite(ctx context.Context, req SiteDeploy) error {
	return c.do(ctx, http.MethodPost, "/api/site/deploy", req, nil)
}

// Teardown asks the node to fully uninstall itself.
func (c *AgentClient) Teardown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/teardown", nil, nil)
}

// MonitorSummary fetches the raw monitor summary JSON from the node monitor.
// The body is returned as-is so callers can decode it into the monitor
// package's existing types without a cross-package dependency from cluster
// into monitor.
func (c *AgentClient) MonitorSummary(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Node.MonitorAPIURL(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("monitor summary %s: %d: %s", c.Node.WGIP, resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func (c *AgentClient) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Node.APIBaseURL()+path, body)
	if err != nil {
		return err
	}
	if c.Node.APIToken == "" {
		return fmt.Errorf("node %s has no API token", c.Node.Alias)
	}
	req.Header.Set("Authorization", "Bearer "+c.Node.APIToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, string(buf))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
