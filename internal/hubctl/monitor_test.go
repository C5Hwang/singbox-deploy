package hubctl

import (
	"context"
	"io"
	"net/http"
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
		WGIP: "10.90.0.2", Token: "node-secret", Installed: true, Monitor: true,
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
	if len(snapshot) != 1 || snapshot[0].ID != nodeID || snapshot[0].Name != "Tokyo" || snapshot[0].TotalUsedBytes != 30 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot[0].MonitorURL != "" {
		t.Fatalf("snapshot leaked direct monitor URL %q", snapshot[0].MonitorURL)
	}
	body, err := ctrl.MonitorData(context.Background(), nodeID, nodeapi.MonitorTrafficTrend)
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
