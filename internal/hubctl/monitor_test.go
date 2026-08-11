package hubctl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

type monitorRoundTripper func(*http.Request) (*http.Response, error)

func (fn monitorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestRefreshMonitorAndDrillDownUseAuthenticatedAgentAPI(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "Tokyo", SSHHost: "tokyo.example.com", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "node-secret", Installed: true, Monitor: true, MonitorAlias: "JP-monitor",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := list[0].ID
	var requested []string
	httpClient := &http.Client{Transport: monitorRoundTripper(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer node-secret" {
			t.Fatalf("authorization = %q", got)
		}
		requested = append(requested, req.URL.Path)
		body := ""
		switch req.URL.Path {
		case "/api/health":
			body = `{"ok":true,"version":"v1","installed":true,"singBoxActive":true}`
		case "/api/monitor/summary":
			body = `{"inUsedBytes":10,"outUsedBytes":20,"totalUsedBytes":30,"sources":[{"sampledAt":"2026-07-10T00:00:00Z"}]}`
		case "/api/monitor/traffic-trend":
			body = `{"trend":[{"hourTs":1,"inBytes":7}]}`
		default:
			t.Fatalf("unexpected agent path %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	ctrl := &Controller{
		Layout: layout,
		NewClient: func(node nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: "http://agent.invalid", Token: node.Token, HTTP: httpClient}
		},
	}
	if err := ctrl.RefreshMonitor(context.Background()); err != nil {
		t.Fatalf("RefreshMonitor: %v", err)
	}
	snapshot, err := monitor.ReadRemoteSources(deploy.RemoteMonitorPath(layout))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].ID != nodeID || snapshot[0].Name != "🇯🇵 JP-monitor" || snapshot[0].TotalUsedBytes != 30 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot[0].MonitorURL != "" {
		t.Fatalf("snapshot leaked direct monitor URL %q", snapshot[0].MonitorURL)
	}
	body, err := ctrl.MonitorData(context.Background(), nodeID, nodeapi.MonitorTrafficTrend, "")
	if err != nil {
		t.Fatalf("MonitorData: %v", err)
	}
	if !strings.Contains(string(body), `"inBytes":7`) {
		t.Fatalf("drill-down body = %s", body)
	}
	wantPaths := []string{"/api/health", "/api/monitor/summary", "/api/monitor/traffic-trend"}
	if strings.Join(requested, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("agent paths = %v, want %v", requested, wantPaths)
	}
}

func TestTrafficUsageUsesAuthenticatedAgentAPIWithoutMutatingRegistry(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID: "0123456789abcdef0123456789abcdef", Alias: "London",
		Domain: "uk.example.com", WGIP: "10.90.0.2", Token: "node-secret",
		Installed: true, Monitor: true, TrafficTotalLimitBytes: 10 << 30,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	var methods []string
	httpClient := &http.Client{Transport: monitorRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "10.90.0.2:19091" ||
			(req.URL.Path != "/api/health" && req.URL.Path != "/api/monitor/usage") {
			t.Fatalf("traffic usage URL = %s", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "Bearer node-secret" {
			t.Fatalf("authorization = %q", got)
		}
		methods = append(methods, req.Method+" "+req.URL.Path)
		body := `{"inBytes":100,"outBytes":200,"cycleStart":1782864000}`
		if req.URL.Path == "/api/health" {
			body = `{"ok":true,"version":"v1","installed":true,"singBoxActive":true,"domain":"uk.example.com"}`
		} else if req.Method == http.MethodPut {
			body = `{"previous":{"inBytes":100,"outBytes":200,"cycleStart":1782864000},"applied":{"inBytes":300,"outBytes":400,"cycleStart":1782864000}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: req,
		}, nil
	})}
	ctrl := &Controller{
		Layout: layout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{
				BaseURL: "http://" + n.WGIP + ":19091", Token: n.Token, HTTP: httpClient,
			}
		},
	}
	current, err := ctrl.TrafficUsage(context.Background(), node)
	if err != nil || current.InBytes != 100 || current.OutBytes != 200 {
		t.Fatalf("TrafficUsage = %+v, err=%v", current, err)
	}
	updated, err := ctrl.SetTrafficUsage(context.Background(), node, nodeapi.TrafficUsageRequest{
		InBytes: 300, OutBytes: 400, ExpectedCycleStart: current.CycleStart,
	})
	if err != nil || updated.Previous != current ||
		updated.Applied.InBytes != 300 || updated.Applied.OutBytes != 400 {
		t.Fatalf("SetTrafficUsage = %+v, err=%v", updated, err)
	}
	wantMethods := []string{
		"GET /api/health", "GET /api/monitor/usage",
		"GET /api/health", "PUT /api/monitor/usage",
	}
	if strings.Join(methods, ",") != strings.Join(wantMethods, ",") {
		t.Fatalf("methods = %v", methods)
	}
	persisted, err := nodes.Load(layout)
	if err != nil || len(persisted) != 1 {
		t.Fatalf("load registry after usage update: %+v err=%v", persisted, err)
	}
	if persisted[0].ID != node.ID || persisted[0].Domain != node.Domain ||
		persisted[0].TrafficTotalLimitBytes != node.TrafficTotalLimitBytes ||
		!persisted[0].Monitor || persisted[0].AgentVersion != "v1" {
		t.Fatalf("dynamic usage mutated registry settings: %+v", persisted[0])
	}
}

// The hub's monitor service refreshes on a short timer. A spoke that is behind
// on agent version or still owed a certificate must not turn every tick into a
// binary push or a remote service restart.
func TestRefreshMonitorNeverMutatesTheSpoke(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "Tokyo", SSHHost: "tokyo.example.com", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "node-secret", Arch: "amd64", Installed: true, Monitor: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	node := list[0]
	node.PendingCertificate = true
	if err := nodes.Update(layout, node); err != nil {
		t.Fatal(err)
	}
	writeCertificatePair(t, layout, node.Domain)

	h := &upgradeHealthHandler{version: "v1.0.0"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:          layout,
		ExpectedVersion: "v2.0.0",
		AgentBinary: func(string) ([]byte, error) {
			t.Fatal("monitor aggregation must not load an agent binary")
			return nil, nil
		},
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if err := ctrl.RefreshMonitor(context.Background()); err == nil || !strings.Contains(err.Error(), "monitor") {
		t.Fatalf("RefreshMonitor should report the unsupported monitor without mutating it: %v", err)
	}

	h.mu.Lock()
	upgradeVersion, applyCount := h.upgradeReq.Version, h.applyCount
	h.mu.Unlock()
	if upgradeVersion != "" || applyCount != 0 {
		t.Fatalf("aggregation mutated the spoke: upgrade=%q applyCert=%d", upgradeVersion, applyCount)
	}
	persisted, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if persisted[0].AgentVersion != "v1.0.0" || persisted[0].LastSeen.IsZero() {
		t.Fatalf("observed status not recorded: %+v", persisted[0])
	}
	if !persisted[0].PendingCertificate {
		t.Fatal("aggregation cleared the pending certificate marker")
	}
}

func TestRefreshMonitorReportsFailureAndKeepsPreviousSnapshot(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "Tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "node-secret", Installed: true, Monitor: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	previous := monitor.SourceSummary{
		ID: list[0].ID, Name: "Tokyo", FetchedAt: "2026-07-01T00:00:00Z", TotalUsedBytes: 123,
	}
	if err := monitor.WriteRemoteSources(deploy.RemoteMonitorPath(layout), []monitor.SourceSummary{previous}); err != nil {
		t.Fatal(err)
	}
	ctrl := &Controller{
		Layout: layout,
		NewClient: func(node nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{
				BaseURL: "http://offline.invalid", Token: node.Token,
				HTTP: &http.Client{Transport: monitorRoundTripper(func(*http.Request) (*http.Response, error) {
					return nil, fmt.Errorf("overlay unreachable")
				})},
			}
		},
	}
	err = ctrl.RefreshMonitor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Tokyo") || !strings.Contains(err.Error(), "kept previous snapshot") {
		t.Fatalf("RefreshMonitor error = %v", err)
	}
	snapshot, readErr := monitor.ReadRemoteSources(deploy.RemoteMonitorPath(layout))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(snapshot) != 1 || snapshot[0].ID != previous.ID || snapshot[0].TotalUsedBytes != previous.TotalUsedBytes {
		t.Fatalf("previous snapshot was not retained: %+v", snapshot)
	}
}
