package ui

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

type committedUpgradeErrorHandler struct {
	mu           sync.Mutex
	version      string
	upgradeCalls int
	upgradeErr   error
}

type transientRestoreHealthHandler struct {
	mu           sync.Mutex
	calls        int
	upgradeCalls int
	version      string
	domain       string
}

type delayedRestartRollbackHandler struct {
	mu                 sync.Mutex
	healthCalls        int
	upgradeCalls       int
	runningVersion     string
	pendingVersion     string
	rollbackVersion    string
	firstRestoreFailed bool
}

func (h *transientRestoreHealthHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.calls == 1 {
		return nodeapi.HealthResponse{OK: false, Version: h.version, Error: "Agent is restarting"}
	}
	return nodeapi.HealthResponse{
		OK: true, Version: h.version, Installed: true,
		SingBoxVersion: "v1.13.16", SingBoxActive: true, Domain: h.domain,
	}
}

func (*transientRestoreHealthHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}
func (*transientRestoreHealthHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}
func (*transientRestoreHealthHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (*transientRestoreHealthHandler) Subscription(string) ([]byte, error) { return nil, nil }

func (h *transientRestoreHealthHandler) Upgrade(_ context.Context, req nodeapi.UpgradeRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upgradeCalls++
	h.version = req.Version
	return nil
}

func (h *transientRestoreHealthHandler) snapshot() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls, h.upgradeCalls
}

func (h *delayedRestartRollbackHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthCalls++
	version := h.runningVersion
	if h.upgradeCalls == 0 {
		// Model the dangerous window: the old process answers one healthy
		// request, then the already queued restart boots the candidate that is
		// still on disk.
		h.runningVersion = h.pendingVersion
	}
	return nodeapi.HealthResponse{OK: true, Version: version, Installed: true, SingBoxActive: true}
}

func (*delayedRestartRollbackHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}
func (*delayedRestartRollbackHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}
func (*delayedRestartRollbackHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (*delayedRestartRollbackHandler) Subscription(string) ([]byte, error) { return nil, nil }

func (h *delayedRestartRollbackHandler) Upgrade(_ context.Context, req nodeapi.UpgradeRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upgradeCalls++
	h.runningVersion = req.Version
	h.pendingVersion = req.Version
	if h.upgradeCalls == 1 && h.firstRestoreFailed {
		return errors.New("an agent upgrade has already committed; restart is pending")
	}
	return nil
}

func (h *delayedRestartRollbackHandler) snapshot() (string, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runningVersion, h.healthCalls, h.upgradeCalls
}

func (h *committedUpgradeErrorHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return nodeapi.HealthResponse{
		OK: true, Version: h.version, Installed: true, SingBoxActive: true,
	}
}

func (*committedUpgradeErrorHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}

func (*committedUpgradeErrorHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}

func (*committedUpgradeErrorHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}

func (*committedUpgradeErrorHandler) Subscription(string) ([]byte, error) { return nil, nil }

func (h *committedUpgradeErrorHandler) Upgrade(_ context.Context, req nodeapi.UpgradeRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upgradeCalls++
	// Model the ambiguous failure this rollback guard exists for: the remote
	// executable has committed, but the Hub receives an operation error.
	h.version = req.Version
	return h.upgradeErr
}

func (h *committedUpgradeErrorHandler) snapshot() (string, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.version, h.upgradeCalls
}

func TestUpgradeSelectedSpokeAgentsRollsBackCurrentAfterCommittedRequestError(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID:           "11111111111111111111111111111111",
		Alias:        "Tokyo",
		Domain:       "jp.example.com",
		WGIP:         "10.90.0.2",
		Token:        "token",
		Arch:         "amd64",
		Installed:    true,
		AgentVersion: "v1.0.0",
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}

	commitErr := errors.New("response lost after Agent commit")
	handler := &committedUpgradeErrorHandler{version: "v1.0.0", upgradeErr: commitErr}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	t.Cleanup(server.Close)

	ctrl := &hubctl.Controller{
		Layout:                   layout,
		ExpectedVersion:          "v2.0.0",
		RequireExactAgentVersion: true,
		AgentBinary: func(string) ([]byte, error) {
			return []byte("candidate-agent"), nil
		},
		NewClient: func(current nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{
				BaseURL: server.URL,
				Token:   current.Token,
				HTTP:    server.Client(),
			}
		},
	}

	var rollbackNodes []nodes.Node
	restore := func(_ context.Context, selected []nodes.Node, _ *logWriter) error {
		rollbackNodes = append([]nodes.Node(nil), selected...)
		return nil
	}
	logs := &logWriter{ch: make(chan runMsg, 16)}
	err := upgradeSelectedSpokeAgents(
		context.Background(), []nodes.Node{node}, ctrl, logs, restore,
	)
	if err == nil || !strings.Contains(err.Error(), commitErr.Error()) {
		t.Fatalf("upgrade error = %v, want committed request error", err)
	}
	if version, calls := handler.snapshot(); version != "v2.0.0" || calls != 1 {
		t.Fatalf("remote Agent version=%q upgrade calls=%d, want committed v2.0.0 once", version, calls)
	}
	if len(rollbackNodes) != 1 || rollbackNodes[0].ID != node.ID {
		t.Fatalf("rollback nodes = %+v, want current possibly-committed node", rollbackNodes)
	}
}

func TestRestoreSelectedSpokeAgentsWaitsThroughAgentRestart(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID: "11111111111111111111111111111111", Alias: "Tokyo",
		Domain: "jp.example.com", WGIP: "10.90.0.2", Token: "token",
		Arch: "amd64", Installed: true, AgentVersion: "v1.0.0",
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	handler := &transientRestoreHealthHandler{version: "v1.0.0", domain: node.Domain}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	t.Cleanup(server.Close)
	ctrl := &hubctl.Controller{
		Layout:              layout,
		ExpectedVersion:     "v1.0.0",
		AllowAgentDowngrade: true,
		AgentBinary: func(string) ([]byte, error) {
			return []byte("rollback-agent"), nil
		},
		NewClient: func(current nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: current.Token, HTTP: server.Client()}
		},
	}
	logs := &logWriter{ch: make(chan runMsg, 16)}
	if err := restoreSelectedSpokeAgentsWithController(context.Background(), []nodes.Node{node}, ctrl, logs); err != nil {
		t.Fatalf("restore after transient restart: %v", err)
	}
	if healthCalls, upgradeCalls := handler.snapshot(); healthCalls < 2 || upgradeCalls != 1 {
		t.Fatalf("health calls = %d, upgrade calls = %d; want one forced restore and a health retry", healthCalls, upgradeCalls)
	}
}

func TestRestoreSelectedSpokeAgentsDoesNotTrustOldProcessBeforeDelayedRestart(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID: "11111111111111111111111111111111", Alias: "Tokyo",
		Domain: "jp.example.com", WGIP: "10.90.0.2", Token: "token",
		Arch: "amd64", Installed: true, AgentVersion: "v1.0.0",
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	handler := &delayedRestartRollbackHandler{
		runningVersion: "v1.0.0", pendingVersion: "v2.0.0",
		rollbackVersion: "v1.0.0", firstRestoreFailed: true,
	}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	t.Cleanup(server.Close)
	ctrl := &hubctl.Controller{
		Layout:              layout,
		ExpectedVersion:     handler.rollbackVersion,
		AllowAgentDowngrade: true,
		AgentBinary: func(string) ([]byte, error) {
			return []byte("rollback-agent"), nil
		},
		NewClient: func(current nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: current.Token, HTTP: server.Client()}
		},
	}
	logs := &logWriter{ch: make(chan runMsg, 32)}
	if err := restoreSelectedSpokeAgentsWithController(context.Background(), []nodes.Node{node}, ctrl, logs); err != nil {
		t.Fatalf("restore across delayed restart window: %v", err)
	}
	version, healthCalls, upgradeCalls := handler.snapshot()
	if version != handler.rollbackVersion {
		t.Fatalf("running Agent version = %q, want rollback %q", version, handler.rollbackVersion)
	}
	if healthCalls == 0 || upgradeCalls != 2 {
		t.Fatalf("health calls = %d, upgrade calls = %d; want health only after an acknowledged forced retry", healthCalls, upgradeCalls)
	}
}
