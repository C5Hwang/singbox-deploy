package hubctl

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

type fleetCoreHandler struct {
	mu           sync.Mutex
	version      string
	agentVersion string
	active       bool
	installed    bool
	failTarget   string
	changeCalls  []string
}

func (h *fleetCoreHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return nodeapi.HealthResponse{
		OK: true, Version: h.agentVersion, Installed: h.installed,
		SingBoxVersion: h.version, SingBoxActive: h.active,
	}
}

func (*fleetCoreHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}
func (*fleetCoreHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}
func (*fleetCoreHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (*fleetCoreHandler) Subscription(string) ([]byte, error) { return nil, nil }

func (h *fleetCoreHandler) ChangeCore(_ context.Context, req nodeapi.CoreRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.changeCalls = append(h.changeCalls, req.SingBoxVersion)
	if req.SingBoxVersion == h.failTarget {
		return errors.New("injected core change failure")
	}
	h.version = req.SingBoxVersion
	h.active = true
	return nil
}

func (h *fleetCoreHandler) snapshot() (string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.version, append([]string(nil), h.changeCalls...)
}

type fleetCoreTestNode struct {
	node    nodes.Node
	handler *fleetCoreHandler
	server  *httptest.Server
}

func newFleetCoreTestNode(t *testing.T, id, alias, version string) fleetCoreTestNode {
	t.Helper()
	handler := &fleetCoreHandler{
		version: version, agentVersion: "v2.0.0", active: true, installed: true,
	}
	server := httptest.NewServer((&nodeapi.Server{Token: "token-" + id, Handler: handler}).Mux())
	t.Cleanup(server.Close)
	return fleetCoreTestNode{
		node: nodes.Node{
			ID: id, Alias: alias, WGIP: "10.90.0.2", Token: "token-" + id,
			Installed: true, AgentVersion: "v2.0.0", SingBoxVersion: version,
		},
		handler: handler,
		server:  server,
	}
}

func fleetCoreController(t *testing.T, localVersion *string, testNodes ...fleetCoreTestNode) *Controller {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	registry := make([]nodes.Node, 0, len(testNodes))
	urls := make(map[string]string, len(testNodes))
	clients := make(map[string]*httptest.Server, len(testNodes))
	for _, testNode := range testNodes {
		registry = append(registry, testNode.node)
		urls[testNode.node.ID] = testNode.server.URL
		clients[testNode.node.ID] = testNode.server
	}
	if err := nodes.Save(layout, registry); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	return &Controller{
		Layout:          layout,
		ExpectedVersion: "v2.0.0",
		NewClient: func(node nodes.Node) *nodeapi.Client {
			server := clients[node.ID]
			return &nodeapi.Client{
				BaseURL: urls[node.ID], Token: node.Token, HTTP: server.Client(),
			}
		},
		CurrentCoreVersion: func(context.Context) (string, error) {
			return *localVersion, nil
		},
		LocalCoreActive: func() error { return nil },
	}
}

func TestChangeFleetCoreConvergesExactVersionsAndPersistsHealth(t *testing.T) {
	first := newFleetCoreTestNode(t, "11111111111111111111111111111111", "London", "v1.12.2")
	second := newFleetCoreTestNode(t, "22222222222222222222222222222222", "Tokyo", "v1.12.4")
	localVersion := "v1.12.3"
	controller := fleetCoreController(t, &localVersion, first, second)
	var localChanges []string
	controller.ChangeLocalCore = func(_ context.Context, tag string, _ io.Writer) error {
		localChanges = append(localChanges, tag)
		localVersion = tag
		return nil
	}

	if err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard); err != nil {
		t.Fatalf("ChangeFleetCore: %v", err)
	}
	if localVersion != "v1.12.4" || strings.Join(localChanges, ",") != "v1.12.4" {
		t.Fatalf("local core = %q, changes=%v", localVersion, localChanges)
	}
	if version, calls := first.handler.snapshot(); version != "v1.12.4" ||
		strings.Join(calls, ",") != "v1.12.4" {
		t.Fatalf("first spoke version=%q calls=%v", version, calls)
	}
	if version, calls := second.handler.snapshot(); version != "v1.12.4" || len(calls) != 0 {
		t.Fatalf("already-converged spoke version=%q calls=%v", version, calls)
	}
	registry, err := nodes.Load(controller.Layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range registry {
		if node.SingBoxVersion != "v1.12.4" {
			t.Fatalf("registry did not persist exact core health: %+v", registry)
		}
	}
}

func TestChangeFleetCoreRollsBackChangedSpokesBeforeHubCommit(t *testing.T) {
	first := newFleetCoreTestNode(t, "11111111111111111111111111111111", "London", "v1.12.2")
	second := newFleetCoreTestNode(t, "22222222222222222222222222222222", "Tokyo", "v1.12.3")
	second.handler.failTarget = "v1.12.4"
	localVersion := "v1.12.1"
	controller := fleetCoreController(t, &localVersion, first, second)
	var localCalls int
	controller.ChangeLocalCore = func(context.Context, string, io.Writer) error {
		localCalls++
		return nil
	}

	err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "injected core change failure") {
		t.Fatalf("fleet error = %v", err)
	}
	if localCalls != 0 || localVersion != "v1.12.1" {
		t.Fatalf("Hub committed before spokes: calls=%d version=%q", localCalls, localVersion)
	}
	if version, calls := first.handler.snapshot(); version != "v1.12.2" ||
		strings.Join(calls, ",") != "v1.12.4,v1.12.2" {
		t.Fatalf("first spoke rollback version=%q calls=%v", version, calls)
	}
	if version, calls := second.handler.snapshot(); version != "v1.12.3" ||
		strings.Join(calls, ",") != "v1.12.4,v1.12.3" {
		t.Fatalf("possibly-committed failing spoke was not restored: version=%q calls=%v", version, calls)
	}
}

func TestChangeFleetCoreRollsBackSpokesAndPossiblyCommittedHub(t *testing.T) {
	spoke := newFleetCoreTestNode(t, "11111111111111111111111111111111", "London", "v1.12.2")
	localVersion := "v1.12.2"
	controller := fleetCoreController(t, &localVersion, spoke)
	var localChanges []string
	controller.ChangeLocalCore = func(_ context.Context, tag string, _ io.Writer) error {
		localChanges = append(localChanges, tag)
		localVersion = tag
		if tag == "v1.12.4" {
			return errors.New("lost response after local commit")
		}
		return nil
	}

	err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "lost response after local commit") {
		t.Fatalf("fleet error = %v", err)
	}
	if localVersion != "v1.12.2" || strings.Join(localChanges, ",") != "v1.12.4,v1.12.2" {
		t.Fatalf("Hub rollback version=%q changes=%v", localVersion, localChanges)
	}
	if version, calls := spoke.handler.snapshot(); version != "v1.12.2" ||
		strings.Join(calls, ",") != "v1.12.4,v1.12.2" {
		t.Fatalf("spoke rollback version=%q calls=%v", version, calls)
	}
}

func TestChangeFleetCorePreflightRejectsUnknownVersionWithoutMutation(t *testing.T) {
	spoke := newFleetCoreTestNode(t, "11111111111111111111111111111111", "London", "")
	localVersion := "v1.12.2"
	controller := fleetCoreController(t, &localVersion, spoke)
	var localCalls int
	controller.ChangeLocalCore = func(context.Context, string, io.Writer) error {
		localCalls++
		return nil
	}

	err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid sing-box version") {
		t.Fatalf("preflight error = %v", err)
	}
	if localCalls != 0 {
		t.Fatalf("local core changed after failed preflight: %d calls", localCalls)
	}
	if _, calls := spoke.handler.snapshot(); len(calls) != 0 {
		t.Fatalf("spoke core changed after failed preflight: %v", calls)
	}
}
