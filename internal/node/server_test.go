package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// fakeRunner records commands without executing them.
type fakeRunner struct {
	cmds []system.Command
	err  error
}

func (f *fakeRunner) Run(c system.Command) error {
	f.cmds = append(f.cmds, c)
	return f.err
}

func newTestServer(t *testing.T) (*Server, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	runner := &fakeRunner{}
	state := AgentState{APIToken: "test-token", WGIP: "127.0.0.1"}
	return NewServer(layout, state, runner, "v1.0.0"), runner
}

func TestServerRequiresBearerToken(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestServerAcceptsValidToken(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerRejectsWrongToken(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestServerStatusPayload(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var status cluster.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.NodeVersion != "v1.0.0" {
		t.Errorf("NodeVersion = %q want v1.0.0", status.NodeVersion)
	}
	if status.WGIP != "127.0.0.1" {
		t.Errorf("WGIP = %q want 127.0.0.1", status.WGIP)
	}
}

func TestServerConfigReloadRestartsSingBox(t *testing.T) {
	s, runner := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/config/reload", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(runner.cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(runner.cmds))
	}
	if runner.cmds[0].Name != "systemctl" || len(runner.cmds[0].Args) < 2 || runner.cmds[0].Args[0] != "restart" {
		t.Errorf("unexpected command: %v", runner.cmds[0])
	}
}

func TestServerCertDeployRequiresFields(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(cluster.CertDeploy{}) // empty
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/cert/deploy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing cert/key, got %d", resp.StatusCode)
	}
}

func TestServerRejectsGetOnPostEndpoints(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	endpoints := []string{
		"/api/config/update",
		"/api/config/reload",
		"/api/monitor/config",
		"/api/upgrade",
		"/api/cert/deploy",
		"/api/teardown",
	}
	for _, ep := range endpoints {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+ep, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", ep, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", ep, resp.StatusCode)
		}
	}
}

func TestUpgradeHookOverride(t *testing.T) {
	s, _ := newTestServer(t)
	called := false
	s.Upgrader = func(ctx context.Context, srv *Server, req cluster.UpgradeRequest) error {
		called = true
		if req.Version != "v9.9.9" {
			t.Errorf("expected version v9.9.9, got %q", req.Version)
		}
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(cluster.UpgradeRequest{Version: "v9.9.9"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/upgrade", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !called {
		t.Errorf("Upgrader hook was not invoked")
	}
}

func TestAgentStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	in := AgentState{
		APIToken:        "secret",
		WGIP:            "10.10.0.5",
		MasterPublicKey: "pubkey",
		MasterEndpoint:  "1.2.3.4:51820",
	}
	if err := SaveAgentState(layout, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := LoadAgentState(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out != in {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", in, out)
	}
}

func TestLoadAgentStateRequiresToken(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	if _, err := LoadAgentState(layout); err == nil {
		t.Errorf("expected error for missing token")
	}
}

// postCertDeploy posts a cluster.CertDeploy to the given httptest server with
// the canonical Bearer token used by newTestServer. Returns the HTTP response
// for the caller to inspect.
func postCertDeploy(t *testing.T, ts *httptest.Server, payload cluster.CertDeploy) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/cert/deploy", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

// TestCertDeployFirstTimeUsesPayloadDomain covers the add-node provisioning
// path: no install state exists yet, and the master sends Domain inline. The
// agent must write the PEM files under tlsPaths(layout, payloadDomain),
// return 200, and — because UnitActive defaults to false here — skip both the
// sing-box restart and the nginx reload (these will start later for the first
// time once "Initial config" lands).
func TestCertDeployFirstTimeUsesPayloadDomain(t *testing.T) {
	s, runner := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	certPath := filepath.Join(s.Layout.TLSDir, "jp.example.com.crt")
	keyPath := filepath.Join(s.Layout.TLSDir, "jp.example.com.key")
	if got, err := os.ReadFile(certPath); err != nil || string(got) != "PEM-CERT" {
		t.Fatalf("cert file: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(keyPath); err != nil || string(got) != "PEM-KEY" {
		t.Fatalf("key file: got=%q err=%v", got, err)
	}
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file perm: want 0600, got %v (err=%v)", info.Mode().Perm(), err)
	}
	if len(runner.cmds) != 0 {
		t.Errorf("expected zero systemctl commands when no services are active, got %#v", runner.cmds)
	}
}

// TestCertDeployFallsBackToInstallStateDomain covers the legacy/renewal path
// where the master did not put Domain in the payload. The agent falls back to
// the domain recorded in its install state.
func TestCertDeployFallsBackToInstallStateDomain(t *testing.T) {
	s, _ := newTestServer(t)
	seedInstallState(t, s.Layout, "us.example.com")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Cert: "RENEW-CERT",
		Key:  "RENEW-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	certPath := filepath.Join(s.Layout.TLSDir, "us.example.com.crt")
	if got, err := os.ReadFile(certPath); err != nil || string(got) != "RENEW-CERT" {
		t.Fatalf("cert file: got=%q err=%v", got, err)
	}
}

// TestCertDeployRejectsWhenDomainMissing covers the "neither payload nor
// install state has a domain" case. The agent must refuse with 400 rather
// than silently writing PEMs to an unreasonable path.
func TestCertDeployRejectsWhenDomainMissing(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Cert: "PEM-CERT",
		Key:  "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "domain is required") {
		t.Errorf("expected domain-required message, got %q", body)
	}
}

// TestCertDeployRestartsSingBoxWhenActive proves the renewal path still
// restarts sing-box once the unit is running.
func TestCertDeployRestartsSingBoxWhenActive(t *testing.T) {
	s, runner := newTestServer(t)
	s.UnitActive = func(unit string) bool { return unit == system.SingBoxService }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if len(runner.cmds) != 1 {
		t.Fatalf("expected exactly 1 systemctl command, got %d: %#v", len(runner.cmds), runner.cmds)
	}
	got := runner.cmds[0]
	if got.Name != "systemctl" || len(got.Args) < 2 || got.Args[0] != "restart" || got.Args[1] != system.SingBoxService {
		t.Errorf("expected systemctl restart %s, got %#v", system.SingBoxService, got)
	}
}

// TestCertDeployReloadsNginxWhenActive proves nginx is reloaded (not
// restarted) when only it is active — sing-box must stay untouched.
func TestCertDeployReloadsNginxWhenActive(t *testing.T) {
	s, runner := newTestServer(t)
	s.UnitActive = func(unit string) bool { return unit == "nginx.service" }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if len(runner.cmds) != 1 {
		t.Fatalf("expected exactly 1 systemctl command, got %d: %#v", len(runner.cmds), runner.cmds)
	}
	got := runner.cmds[0]
	if got.Name != "systemctl" || got.Args[0] != "reload" || got.Args[1] != "nginx.service" {
		t.Errorf("expected systemctl reload nginx.service, got %#v", got)
	}
}

// TestCertDeployBothServicesActive verifies the full restart+reload pair
// when both sing-box and nginx are running (renewal on a TLS-protocol node).
func TestCertDeployBothServicesActive(t *testing.T) {
	s, runner := newTestServer(t)
	s.UnitActive = func(unit string) bool { return true }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if len(runner.cmds) != 2 {
		t.Fatalf("expected 2 systemctl commands (restart sing-box + reload nginx), got %d: %#v", len(runner.cmds), runner.cmds)
	}
	if runner.cmds[0].Args[0] != "restart" || runner.cmds[0].Args[1] != system.SingBoxService {
		t.Errorf("first command should be sing-box restart, got %#v", runner.cmds[0])
	}
	if runner.cmds[1].Args[0] != "reload" || runner.cmds[1].Args[1] != "nginx.service" {
		t.Errorf("second command should be nginx reload, got %#v", runner.cmds[1])
	}
}

// TestCertDeploySingBoxRestartFailurePropagates makes sure a real failure
// from the runner during the renewal-style restart turns into 500, not a
// silent success.
func TestCertDeploySingBoxRestartFailurePropagates(t *testing.T) {
	s, runner := newTestServer(t)
	runner.err = io.ErrUnexpectedEOF
	s.UnitActive = func(unit string) bool { return unit == system.SingBoxService }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postCertDeploy(t, ts, cluster.CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

// seedInstallState writes the minimal state files LoadProtocolConfig requires
// so the agent has a usable cfg.Domain to fall back on.
func seedInstallState(t *testing.T, layout paths.Layout, domain string) {
	t.Helper()
	store := state.NewStore(layout.StateDir)
	if err := store.WriteString("domain", domain, 0o600); err != nil {
		t.Fatalf("write domain: %v", err)
	}
	if err := store.WriteString("subscribe_salt", "salt", 0o600); err != nil {
		t.Fatalf("write subscribe_salt: %v", err)
	}
	if err := store.WriteString("enabled_protocols", "vless-reality-vision", 0o600); err != nil {
		t.Fatalf("write enabled_protocols: %v", err)
	}
}
