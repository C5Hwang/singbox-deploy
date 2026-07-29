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
