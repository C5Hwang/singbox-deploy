package node

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
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
