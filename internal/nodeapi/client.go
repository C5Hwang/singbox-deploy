package nodeapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the hub's connection to one spoke agent over the overlay.
type Client struct {
	// BaseURL is the agent API root, e.g. http://10.90.0.2:19091.
	BaseURL string
	Token   string
	// HTTP is the underlying client; nil uses a default with sane timeouts.
	HTTP *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// Streaming operations can run for minutes (package install, core download),
	// so there is no overall client timeout; per-request timeouts come from the
	// caller's context instead.
	return &http.Client{}
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

// Health probes the agent, returning its reported state.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/api/health", nil)
	if err != nil {
		return HealthResponse{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return HealthResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthResponse{}, statusError(resp)
	}
	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return HealthResponse{}, err
	}
	return health, nil
}

// Install runs a full or config-only install on the agent, forwarding streamed
// log lines to log until the agent reports completion or failure.
func (c *Client) Install(ctx context.Context, req InstallRequest, log io.Writer) error {
	if err := ValidateInstallSingBoxVersion(req); err != nil {
		return err
	}
	return c.stream(ctx, "/api/install", req, log)
}

// ApplyCert pushes a refreshed certificate pair to the agent.
func (c *Client) ApplyCert(ctx context.Context, req CertRequest, log io.Writer) error {
	return c.stream(ctx, "/api/cert", req, log)
}

// Uninstall tears down the agent's sing-box deployment.
func (c *Client) Uninstall(ctx context.Context, req UninstallRequest, log io.Writer) error {
	return c.stream(ctx, "/api/uninstall", req, log)
}

// Upgrade atomically replaces the spoke agent with the supplied verified
// binary. The agent schedules its own service restart after acknowledging the
// streamed operation, so callers should poll Health for the requested version.
func (c *Client) Upgrade(ctx context.Context, req UpgradeRequest, log io.Writer) error {
	if err := ValidateUpgradeRequest(req); err != nil {
		return err
	}
	return c.stream(ctx, "/api/upgrade", req, log)
}

// ChangeCore replaces the spoke's local sing-box core with the exact requested
// stable upstream release.
func (c *Client) ChangeCore(ctx context.Context, req CoreRequest, log io.Writer) error {
	if err := ValidateCoreRequest(req); err != nil {
		return err
	}
	return c.stream(ctx, "/api/core", req, log)
}

// Subscription fetches one subscription format body from the agent.
func (c *Client) Subscription(ctx context.Context, format string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/api/subscription?format="+format, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// Monitor reads one fixed monitor resource through the authenticated agent
// API. endpoint is a typed allow-list value, not a caller-supplied path or URL.
func (c *Client) Monitor(ctx context.Context, endpoint MonitorEndpoint) ([]byte, error) {
	apiPath, _, ok := endpoint.paths()
	if !ok {
		return nil, fmt.Errorf("unsupported agent monitor endpoint %q", endpoint)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	const maxMonitorResponse = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMonitorResponse+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMonitorResponse {
		return nil, fmt.Errorf("agent monitor response exceeds %d bytes", maxMonitorResponse)
	}
	return body, nil
}

// stream posts a JSON body and forwards the streamed log to log, returning the
// terminal status parsed from the sentinel line.
func (c *Client) stream(ctx context.Context, path string, payload any, log io.Writer) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	sawTerminal := false
	var opErr error
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == doneSentinel:
			sawTerminal = true
		case strings.HasPrefix(line, errorSentinelPrefix):
			sawTerminal = true
			opErr = fmt.Errorf("%s", strings.TrimPrefix(line, errorSentinelPrefix))
		default:
			if log != nil {
				fmt.Fprintln(log, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !sawTerminal {
		return fmt.Errorf("agent stream ended without a status (connection lost?)")
	}
	return opErr
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("agent request failed (%d): %s", resp.StatusCode, msg)
}
