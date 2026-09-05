package hubctl

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

// The snapshot carries a spoke's package share beside the limit in force, so
// the dashboard can draw the two apart for spokes exactly as for the hub.
func TestRefreshMonitorCarriesTheSpokePackageShare(t *testing.T) {
	layout, _ := relayTestHub(t)
	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "Tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "node-secret", Installed: true, Monitor: true,
	}); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: monitorRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.Path {
		case "/api/health":
			body = `{"ok":true,"version":"v1","installed":true,"singBoxActive":true}`
		case "/api/monitor/summary":
			body = `{"inUsedBytes":10,"outUsedBytes":20,"totalUsedBytes":30,` +
				`"inLimitBytes":300,"inRemainingBytes":290,"inPackageBytes":100,` +
				`"totalLimitBytes":1000,"totalRemainingBytes":970,"totalPackageBytes":400,` +
				`"sources":[{"sampledAt":"2026-07-10T00:00:00Z"}]}`
		default:
			t.Fatalf("unexpected agent path %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: req,
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
	if len(snapshot) != 1 || snapshot[0].InLimitBytes != 300 || snapshot[0].InPackageBytes != 100 ||
		snapshot[0].TotalLimitBytes != 1000 || snapshot[0].TotalPackageBytes != 400 || snapshot[0].OutPackageBytes != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// A grant goes through the Agent's grant route with the Hub's health check in
// front of it, and never touches the registry.
func TestGrantTrafficPackageUsesTheAgentAndLeavesTheRegistryAlone(t *testing.T) {
	layout, _ := relayTestHub(t)
	if err := nodes.Add(layout, nodes.Node{
		ID: "aa11", Alias: "Tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "node-secret", Installed: true, Monitor: true, AgentVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	httpClient := &http.Client{Transport: monitorRoundTripper(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		body := ""
		switch req.URL.Path {
		case "/api/health":
			body = `{"ok":true,"version":"v1","installed":true,"singBoxActive":true}`
		case "/api/monitor/package":
			raw, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(raw), `"totalBytes":40`) || !strings.Contains(string(raw), `"expectedCycleStart":1782864000`) {
				t.Fatalf("grant body = %s", raw)
			}
			body = `{"previous":{"inBytes":1,"outBytes":2,"cycleStart":1782864000,"package":{"inBytes":0,"outBytes":0,"totalBytes":10}},` +
				`"applied":{"inBytes":1,"outBytes":2,"cycleStart":1782864000,"package":{"inBytes":0,"outBytes":0,"totalBytes":50}}}`
		default:
			t.Fatalf("unexpected agent path %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: req,
		}, nil
	})}
	ctrl := &Controller{
		Layout: layout, ExpectedVersion: "v1",
		NewClient: func(node nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: "http://agent.invalid", Token: node.Token, HTTP: httpClient}
		},
	}
	update, err := ctrl.GrantTrafficPackage(context.Background(), before[0], nodeapi.TrafficPackageGrant{
		TotalBytes: 40, ExpectedCycleStart: 1_782_864_000,
	})
	if err != nil {
		t.Fatalf("GrantTrafficPackage: %v", err)
	}
	if update.Applied.Package.TotalBytes != 50 || update.Previous.Package.TotalBytes != 10 {
		t.Fatalf("update = %+v", update)
	}
	if strings.Join(paths, ",") != "GET /api/health,POST /api/monitor/package" {
		t.Fatalf("agent calls = %v", paths)
	}
	after, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	// LastSeen moves with the health probe; nothing else may.
	after[0].LastSeen, before[0].LastSeen = time.Time{}, time.Time{}
	if len(after) != 1 || after[0].TrafficTotalLimitBytes != before[0].TrafficTotalLimitBytes || after[0].Alias != before[0].Alias {
		t.Fatalf("registry changed by a grant:\nbefore=%+v\nafter=%+v", before[0], after[0])
	}
}

// The hub's own relay availability reads the package it was granted: a hub
// that ran out of traffic carries relayed traffic again once topped up.
func TestRelayAvailableHonoursTheHubTrafficPackage(t *testing.T) {
	layout, cfg := relayTestHub(t)
	cfg.DeployMonitor = true
	cfg.MonitorAlias = "HUB"
	cfg.TrafficTotalLimitBytes = 100
	cfg.ResetDay, cfg.ResetHour = 1, 0
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.MonitorDB), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := monitor.OpenStore(layout.MonitorDB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.InsertSample(now.Unix(), "eth0", 80, 40, 80, 40); err != nil {
		t.Fatal(err)
	}
	store.Close()
	writeSpokeUsage(t, layout)

	available, err := (&Controller{Layout: layout}).RelayAvailable()
	if err != nil {
		t.Fatalf("RelayAvailable: %v", err)
	}
	if available(relaylinks.HubNodeID) {
		t.Fatal("a hub at 120 of 100 must not carry relayed traffic")
	}

	if _, err := monitor.AddTrafficPackage(layout, 1, 0, now, monitor.TrafficPackage{TotalBytes: 50}); err != nil {
		t.Fatalf("AddTrafficPackage: %v", err)
	}
	available, err = (&Controller{Layout: layout}).RelayAvailable()
	if err != nil {
		t.Fatalf("RelayAvailable after grant: %v", err)
	}
	if !available(relaylinks.HubNodeID) {
		t.Fatal("a package that covers the overrun must make the hub available again")
	}
}
