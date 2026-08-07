package hubctl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

func TestEnsureOverlayRejectsRouteConflictBeforePersistingIdentity(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	ctrl := &Controller{
		Layout: layout,
		CheckOverlaySubnet: func(subnet string) error {
			if subnet != wgnet.DefaultSubnet {
				t.Fatalf("checked subnet = %q", subnet)
			}
			return errors.New("existing route 10.90.0.0/24 on eth0")
		},
	}
	if _, err := ctrl.EnsureOverlay("hub.example.com"); err == nil || !strings.Contains(err.Error(), "existing route") {
		t.Fatalf("expected route conflict, got %v", err)
	}
	if _, ok, err := nodes.LoadHubIdentity(layout); err != nil || ok {
		t.Fatalf("identity was persisted despite conflict: ok=%v err=%v", ok, err)
	}
}

func TestWriteHubConfigRendersPeers(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	c := &Controller{Layout: layout, WGConfDir: filepath.Join(dir, "wireguard")}
	c.defaults()
	identity := nodes.HubIdentity{
		PrivateKey:   "HUBPRIV",
		PublicKey:    "HUBPUB",
		EndpointHost: "hub.example.com",
		ListenPort:   wgnet.DefaultListenPort,
		Subnet:       wgnet.DefaultSubnet,
	}
	list := []nodes.Node{
		{WGPublicKey: "PEER1", WGIP: "10.90.0.2"},
		{WGPublicKey: "PEER2", WGIP: "10.90.0.3"},
	}
	if err := c.writeHubConfig(identity, list); err != nil {
		t.Fatalf("writeHubConfig: %v", err)
	}
	confBytes, err := os.ReadFile(c.wgManager().ConfigPath(wgnet.InterfaceName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	conf := string(confBytes)
	for _, want := range []string{
		"Address = 10.90.0.1/24",
		"ListenPort = 51820",
		"PublicKey = PEER1",
		"AllowedIPs = 10.90.0.2/32",
		"PublicKey = PEER2",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("hub config missing %q:\n%s", want, conf)
		}
	}
}

func TestBuildInstallRequestIncludesCert(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	c := &Controller{Layout: layout, ExpectedCoreVersion: "v1.12.4"}
	c.defaults()
	// Place a cert pair the request should embed.
	certPEM, keyPEM := writeCertificatePair(t, layout, "spoke.example.com")

	node := nodes.Node{
		ID:                "0123456789abcdef0123456789abcdef",
		Alias:             "tokyo-server",
		SubscriptionAlias: "tokyo",
		Domain:            "spoke.example.com",
		EnabledProtocols:  []string{"hysteria2"},
		Hysteria2Port:     9443,
		Monitor:           true,
		MonitorAlias:      "Tokyo",
	}
	req, err := c.buildInstallRequest(node)
	if err != nil {
		t.Fatalf("buildInstallRequest: %v", err)
	}
	if req.Domain != "spoke.example.com" || req.DisplayName != "tokyo" {
		t.Fatalf("unexpected request identity: %+v", req)
	}
	if req.InstallTransactionID != node.ID {
		t.Fatalf("install transaction ID = %q, want %q", req.InstallTransactionID, node.ID)
	}
	if req.SingBoxVersion != "v1.12.4" {
		t.Fatalf("sing-box pin = %q, want v1.12.4", req.SingBoxVersion)
	}
	if req.CertificatePEM != string(certPEM) || req.PrivateKeyPEM != string(keyPEM) {
		t.Fatalf("certificate not embedded: %+v", req)
	}
	if req.Ports.Hysteria2 != 9443 || !req.Monitor {
		t.Fatalf("ports/monitor not mapped: %+v", req)
	}
}

func TestFullSpokeInstallPinsAndVerifiesHubCoreVersion(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID: "0123456789abcdef0123456789abcdef", Alias: "tokyo",
		Domain: "spoke.example.com", WGIP: "10.90.0.2", Token: "token",
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	writeCertificatePair(t, layout, node.Domain)

	handler := &lifecycleHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v-test",
	}}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	defer server.Close()
	controller := &Controller{
		Layout:              layout,
		ExpectedCoreVersion: "v1.12.4",
		NewClient: func(nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: node.Token, HTTP: server.Client()}
		},
	}
	controller.defaults()
	if err := controller.installNode(context.Background(), node, io.Discard, false); err != nil {
		t.Fatalf("installNode: %v", err)
	}

	handler.mu.Lock()
	request := handler.installReq
	handler.mu.Unlock()
	if request.SingBoxVersion != "v1.12.4" {
		t.Fatalf("full install pin = %q", request.SingBoxVersion)
	}
	registry, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry) != 1 || registry[0].SingBoxVersion != "v1.12.4" {
		t.Fatalf("verified core version was not persisted: %+v", registry)
	}
}

func TestCheckHealthUpgradesMismatchedAgentAndPersistsStatus(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node := list[0]
	h := &upgradeHealthHandler{version: "v1.0.0"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	requestedArch := ""
	ctrl := &Controller{
		Layout:          layout,
		ExpectedVersion: "v2.0.0",
		AgentBinary: func(arch string) ([]byte, error) {
			requestedArch = arch
			return []byte("embedded-agent-v2"), nil
		},
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.CheckHealth(context.Background(), node, io.Discard)
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if requestedArch != "arm64" || updated.AgentVersion != "v2.0.0" || updated.LastSeen.IsZero() {
		t.Fatalf("upgrade/status mismatch: arch=%q node=%+v", requestedArch, updated)
	}
	h.mu.Lock()
	upgradeReq := h.upgradeReq
	h.mu.Unlock()
	if upgradeReq.Version != "v2.0.0" || upgradeReq.SHA256 != nodeapi.UpgradeDigest([]byte("embedded-agent-v2")) {
		t.Fatalf("wrong upgrade request: %+v", upgradeReq)
	}
	persisted, _ := nodes.Load(layout)
	if len(persisted) != 1 || persisted[0].AgentVersion != "v2.0.0" || persisted[0].LastSeen.IsZero() {
		t.Fatalf("health status not persisted: %+v", persisted)
	}
}

func TestProbeHealthRecordsStatusWithoutMutatingSpoke(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
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
			t.Fatal("a read-only probe must not load an agent binary")
			return nil, nil
		},
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.ProbeHealth(context.Background(), node)
	if err != nil {
		t.Fatalf("ProbeHealth: %v", err)
	}
	if updated.AgentVersion != "v1.0.0" || updated.LastSeen.IsZero() {
		t.Fatalf("observed status not returned: %+v", updated)
	}
	h.mu.Lock()
	upgradeVersion, applyCount := h.upgradeReq.Version, h.applyCount
	h.mu.Unlock()
	if upgradeVersion != "" || applyCount != 0 {
		t.Fatalf("probe mutated the spoke: upgrade=%q applyCert=%d", upgradeVersion, applyCount)
	}
	persisted, _ := nodes.Load(layout)
	if persisted[0].AgentVersion != "v1.0.0" || !persisted[0].PendingCertificate {
		t.Fatalf("probe changed more than the observed status: %+v", persisted[0])
	}
}

func TestCheckHealthDoesNotDowngradeNewerAgent(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node := list[0]
	h := &upgradeHealthHandler{version: "v3.0.0"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:          layout,
		ExpectedVersion: "v2.0.0",
		AgentBinary: func(string) ([]byte, error) {
			t.Fatal("newer agent must not be replaced by a stale hub")
			return nil, nil
		},
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.CheckHealth(context.Background(), node, io.Discard)
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if updated.AgentVersion != "v3.0.0" || updated.LastSeen.IsZero() {
		t.Fatalf("newer status not preserved: %+v", updated)
	}
	h.mu.Lock()
	upgradeVersion := h.upgradeReq.Version
	h.mu.Unlock()
	if upgradeVersion != "" {
		t.Fatalf("newer agent was downgraded to %q", upgradeVersion)
	}
}

func TestCheckHealthRejectsNewerAgentWhenExactVersionIsRequired(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node := list[0]
	h := &upgradeHealthHandler{version: "v3.0.0"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:                   layout,
		ExpectedVersion:          "v2.0.0",
		RequireExactAgentVersion: true,
		AgentBinary: func(string) ([]byte, error) {
			t.Fatal("exact-version gate must not downgrade a newer agent")
			return nil, nil
		},
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.CheckHealth(context.Background(), node, io.Discard)
	if err == nil ||
		!strings.Contains(err.Error(), `reports version "v3.0.0"`) ||
		!strings.Contains(err.Error(), `requires exact version "v2.0.0"`) {
		t.Fatalf("exact-version error = %v", err)
	}
	if updated.AgentVersion != "v3.0.0" || updated.LastSeen.IsZero() {
		t.Fatalf("authenticated observed status was not retained: %+v", updated)
	}
	h.mu.Lock()
	upgradeVersion := h.upgradeReq.Version
	h.mu.Unlock()
	if upgradeVersion != "" {
		t.Fatalf("exact-version gate downgraded the agent to %q", upgradeVersion)
	}
}

func TestCoordinatedHealthRejectsMatchingAgentWithInactiveCore(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}
	if err := nodes.Add(layout, node); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node = list[0]
	h := &fixedHealthHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v2.0.0", Installed: true,
		SingBoxVersion: "v1.13.16", SingBoxActive: false, Domain: node.Domain,
	}}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:                   layout,
		ExpectedVersion:          "v2.0.0",
		RequireExactAgentVersion: true,
		RequireOperationalAgent:  true,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if _, err := ctrl.CheckHealth(context.Background(), node, io.Discard); err == nil || !strings.Contains(err.Error(), "sing-box is inactive") {
		t.Fatalf("coordinated health error = %v", err)
	}
}

func TestCoordinatedHealthValidatesInstalledDomainAndCoreVersion(t *testing.T) {
	node := nodes.Node{Alias: "tokyo", Domain: "spoke.example.com"}
	base := nodeapi.HealthResponse{
		OK: true, Version: "v2.0.0", Installed: true,
		SingBoxVersion: "v1.13.16", SingBoxActive: true, Domain: node.Domain,
	}
	tests := []struct {
		name   string
		mutate func(*nodeapi.HealthResponse)
		want   string
	}{
		{name: "not installed", mutate: func(h *nodeapi.HealthResponse) { h.Installed = false }, want: "not installed"},
		{name: "missing core version", mutate: func(h *nodeapi.HealthResponse) { h.SingBoxVersion = "" }, want: "sing-box version"},
		{name: "missing domain", mutate: func(h *nodeapi.HealthResponse) { h.Domain = "" }, want: "managed domain"},
		{name: "wrong domain", mutate: func(h *nodeapi.HealthResponse) { h.Domain = "other.example.com" }, want: `expected "spoke.example.com"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := base
			tt.mutate(&health)
			ctrl := &Controller{RequireOperationalAgent: true}
			err := ctrl.validateOperationalAgent(context.Background(), node, health, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("operational validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCoordinatedHealthRequiresWorkingEnabledMonitor(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", Installed: true, Monitor: true,
	}
	if err := nodes.Add(layout, node); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node = list[0]
	h := &fixedHealthHandler{
		health: nodeapi.HealthResponse{
			OK: true, Version: "v2.0.0", Installed: true,
			SingBoxVersion: "v1.13.16", SingBoxActive: true, Domain: node.Domain,
		},
		monitor: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "not-json")
		}),
	}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:                  layout,
		ExpectedVersion:         "v2.0.0",
		RequireOperationalAgent: true,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if _, err := ctrl.CheckHealth(context.Background(), node, io.Discard); err == nil || !strings.Contains(err.Error(), "monitor summary is not valid JSON") {
		t.Fatalf("coordinated monitor health error = %v", err)
	}
}

func TestCheckHealthAllowsExplicitRecoveryDowngrade(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "arm64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node := list[0]
	h := &upgradeHealthHandler{version: "v3.0.0"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:              layout,
		ExpectedVersion:     "v2.0.0",
		AllowAgentDowngrade: true,
		AgentBinary:         func(string) ([]byte, error) { return []byte("embedded-agent-v2"), nil },
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.CheckHealth(context.Background(), node, io.Discard)
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if updated.AgentVersion != "v2.0.0" {
		t.Fatalf("explicit recovery left version %q", updated.AgentVersion)
	}
	h.mu.Lock()
	upgradeVersion := h.upgradeReq.Version
	h.mu.Unlock()
	if upgradeVersion != "v2.0.0" {
		t.Fatalf("explicit recovery requested version %q", upgradeVersion)
	}
}

func TestShouldReplaceAgentVersion(t *testing.T) {
	tests := []struct {
		name, reported, expected string
		allowDowngrade           bool
		want                     bool
	}{
		{name: "older", reported: "v1.9.0", expected: "v2.0.0", want: true},
		{name: "newer", reported: "v2.1.0", expected: "v2.0.0", want: false},
		{name: "without v prefix", reported: "1.9.0", expected: "2.0.0", want: true},
		{name: "release repairs legacy", reported: "dev", expected: "v2.0.0", want: true},
		{name: "unknown hub does not overwrite", reported: "v2.0.0", expected: "dev", want: false},
		{name: "explicit recovery", reported: "v3.0.0", expected: "v2.0.0", allowDowngrade: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReplaceAgentVersion(tt.reported, tt.expected, tt.allowDowngrade); got != tt.want {
				t.Fatalf("shouldReplaceAgentVersion(%q, %q, %v) = %v, want %v", tt.reported, tt.expected, tt.allowDowngrade, got, tt.want)
			}
		})
	}
}

func TestCheckHealthRetriesPendingCertificate(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: "spoke.example.com",
		WGIP: "10.90.0.2", Token: "tok", Arch: "amd64", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	node := list[0]
	node.PendingCertificate = true
	if err := nodes.Update(layout, node); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := writeCertificatePair(t, layout, node.Domain)
	h := &upgradeHealthHandler{version: "v2"}
	srv := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout:          layout,
		ExpectedVersion: "v2",
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	updated, err := ctrl.CheckHealth(context.Background(), node, io.Discard)
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	h.mu.Lock()
	certReq, applyCount := h.certReq, h.applyCount
	h.mu.Unlock()
	if updated.PendingCertificate || applyCount != 1 || certReq.CertificatePEM != string(certPEM) || certReq.PrivateKeyPEM != string(keyPEM) {
		t.Fatalf("pending certificate was not retried: node=%+v count=%d req=%+v", updated, applyCount, certReq)
	}
	persisted, _ := nodes.Load(layout)
	if persisted[0].PendingCertificate {
		t.Fatal("pending certificate flag was not cleared")
	}
}

func TestReconfigureReportsEveryHighLevelPhase(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hub-salt", 9443)
	if err := deploy.WriteInstallState(layout.StateDir, hubCfg); err != nil {
		t.Fatal(err)
	}
	if err := deploy.WriteSubscriptions(layout, hubCfg); err != nil {
		t.Fatal(err)
	}
	node := nodes.Node{
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Arch: "amd64", Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 8443,
		Monitor: true, MonitorAlias: "tokyo", MonitorIntervalSeconds: 60, ResetDay: 1,
		PendingCertificate: true,
	}
	if err := nodes.Add(layout, node); err != nil {
		t.Fatal(err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	node = list[0]
	writeCertificatePair(t, layout, node.Domain)

	handler := &lifecycleHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v1.0.0", Installed: true, SingBoxActive: true, Domain: node.Domain,
	}}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	defer server.Close()

	var events []deploy.Event
	ctrl := &Controller{
		Layout: layout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: n.Token, HTTP: server.Client()}
		},
		Progress: func(event deploy.Event) { events = append(events, event) },
	}
	if err := ctrl.Reconfigure(context.Background(), node, io.Discard); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	handler.mu.Lock()
	reconfigureReq := handler.installReq
	handler.mu.Unlock()
	if reconfigureReq.CertificatePEM != "" || reconfigureReq.PrivateKeyPEM != "" {
		t.Fatal("settings reconfigure carried certificate material")
	}
	updatedNodes, err := nodes.Load(layout)
	if err != nil || len(updatedNodes) != 1 {
		t.Fatalf("load reconfigured registry: nodes=%+v err=%v", updatedNodes, err)
	}
	if updatedNodes[0].PendingCertificate {
		t.Fatal("health reconciliation did not complete the separate pending certificate delivery")
	}
	wantLabels := []string{"Agent health", "Spoke configuration", "Registry status", "Subscriptions"}
	if len(events) != 2*len(wantLabels) {
		t.Fatalf("progress event count = %d, want %d: %+v", len(events), 2*len(wantLabels), events)
	}
	for i, label := range wantLabels {
		running, complete := events[2*i], events[2*i+1]
		if running.Index != i+1 || running.Total != len(wantLabels) || running.Label != label || running.Status != "running" {
			t.Errorf("running event %d = %+v", i, running)
		}
		if complete.Index != i+1 || complete.Total != len(wantLabels) || complete.Label != label || complete.Status != "ok" {
			t.Errorf("complete event %d = %+v", i, complete)
		}
	}
}

func TestPatchProtocolPropagatesOnlyProtocolPatch(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID:    "0123456789abcdef0123456789abcdef",
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
		EnabledProtocols: []string{"tuic"}, TUICPort: 10443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	handler := &lifecycleHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v-test", Installed: true, SingBoxActive: true, Domain: node.Domain,
	}}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	defer server.Close()
	ctrl := &Controller{
		Layout: layout, ExpectedVersion: "v-test",
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: n.Token, HTTP: server.Client()}
		},
	}
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	override := nodeapi.ProtocolCredentials{
		RealityVisionUUID: creds.RealityVisionUUID,
		RealityGRPCUUID:   creds.RealityGRPCUUID,
		HysteriaPassword:  creds.HysteriaPassword,
		TUICUUID:          creds.TUICUUID,
		TUICPassword:      creds.TUICPassword,
		AnyTLSPassword:    creds.AnyTLSPassword,
		RealityPrivateKey: creds.RealityPrivateKey,
		RealityPublicKey:  creds.RealityPublicKey,
		RealityShortID:    creds.RealityShortID,
	}
	revision := strings.Repeat("a", 64)
	patch := nodeapi.ProtocolPatch{
		Protocol:    "tuic",
		Port:        node.TUICPort,
		Credentials: override,
	}
	if err := ctrl.PatchProtocolRevision(context.Background(), node, patch, revision, io.Discard); err != nil {
		t.Fatalf("PatchProtocolRevision: %v", err)
	}
	handler.mu.Lock()
	req := handler.installReq
	handler.mu.Unlock()
	wantReq := nodeapi.InstallRequest{
		ConfigOnly:               true,
		ProtocolPatch:            &patch,
		ExpectedProtocolRevision: revision,
	}
	if !reflect.DeepEqual(req, wantReq) {
		t.Fatalf("protocol patch request = %+v, want only %+v", req, wantReq)
	}
}

func TestReplaceProtocolStateUsesExplicitCASRequest(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	node := nodes.Node{
		ID:    "abababababababababababababababab",
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 10443,
		RealityServerName: "www.example.com", RealityHandshakePort: 443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	handler := &lifecycleHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v-test", Installed: true, SingBoxActive: true, Domain: node.Domain,
	}}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	defer server.Close()
	ctrl := &Controller{
		Layout: layout, ExpectedVersion: "v-test",
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: n.Token, HTTP: server.Client()}
		},
	}
	// Simulate a later protocol transaction reaching the registry before this
	// transaction leaves its health phase. Its intent must remain in the
	// registry, but must not be folded into this transaction's Agent request.
	concurrent := node
	concurrent.EnabledProtocols = []string{"anytls"}
	concurrent.AnyTLSPort = 21443
	concurrent.RealityServerName = "concurrent.example.com"
	if err := nodes.Update(layout, concurrent); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("b", 64)
	if err := ctrl.ReplaceProtocolStateRevision(context.Background(), node, revision, io.Discard); err != nil {
		t.Fatalf("ReplaceProtocolStateRevision: %v", err)
	}
	handler.mu.Lock()
	req := handler.installReq
	handler.mu.Unlock()
	wantReq := ctrl.buildNodeRequest(node)
	wantReq.ConfigOnly = true
	wantReq.ReplaceProtocolState = true
	wantReq.ExpectedProtocolRevision = revision
	if !reflect.DeepEqual(req, wantReq) {
		t.Fatalf("complete protocol replacement request = %+v, want %+v", req, wantReq)
	}
	if req.CertificatePEM != "" || req.PrivateKeyPEM != "" {
		t.Fatal("complete protocol replacement carried certificate material")
	}
	stored, err := nodes.Load(layout)
	if err != nil || len(stored) != 1 {
		t.Fatalf("load concurrent registry state: nodes=%+v err=%v", stored, err)
	}
	if strings.Join(stored[0].EnabledProtocols, ",") != "anytls" ||
		stored[0].AnyTLSPort != concurrent.AnyTLSPort ||
		stored[0].RealityServerName != concurrent.RealityServerName {
		t.Fatalf("older replacement overwrote later registry intent: %+v", stored[0])
	}
}

func TestReconfigureProtocolTreatsSubscriptionRefreshAsTransactional(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hub-salt", 9443)
	if err := deploy.WriteInstallState(layout.StateDir, hubCfg); err != nil {
		t.Fatal(err)
	}
	if err := deploy.WriteSubscriptions(layout, hubCfg); err != nil {
		t.Fatal(err)
	}
	node := nodes.Node{
		ID:    "0123456789abcdef0123456789abcdef",
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true, IncludeInSubscription: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 10443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	writeCertificatePair(t, layout, node.Domain)
	handler := &lifecycleHandler{
		health: nodeapi.HealthResponse{
			OK: true, Version: "v-test", Installed: true, SingBoxActive: true, Domain: node.Domain,
		},
		subscriptionErr: errors.New("injected stale subscription failure"),
	}
	server := httptest.NewServer((&nodeapi.Server{Token: node.Token, Handler: handler}).Mux())
	defer server.Close()
	ctrl := &Controller{
		Layout: layout, ExpectedVersion: "v-test",
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: server.URL, Token: n.Token, HTTP: server.Client()}
		},
	}
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	override := nodeapi.ProtocolCredentials{
		RealityVisionUUID: creds.RealityVisionUUID, RealityGRPCUUID: creds.RealityGRPCUUID,
		HysteriaPassword: creds.HysteriaPassword, TUICUUID: creds.TUICUUID,
		TUICPassword: creds.TUICPassword, AnyTLSPassword: creds.AnyTLSPassword,
		RealityPrivateKey: creds.RealityPrivateKey, RealityPublicKey: creds.RealityPublicKey,
		RealityShortID: creds.RealityShortID,
	}
	err = ctrl.PatchProtocolRevision(context.Background(), node, nodeapi.ProtocolPatch{
		Protocol: "hysteria2", Port: node.Hysteria2Port, Credentials: override,
	}, strings.Repeat("a", 64), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "refresh subscriptions after protocol change") {
		t.Fatalf("credential reconfigure refresh error = %v", err)
	}

	var normalLog strings.Builder
	if err := ctrl.Reconfigure(context.Background(), node, &normalLog); err != nil {
		t.Fatalf("ordinary reconfigure should keep warning semantics: %v", err)
	}
	if !strings.Contains(normalLog.String(), "warning: subscription refresh") {
		t.Fatalf("ordinary reconfigure did not report refresh warning: %q", normalLog.String())
	}
}

func TestDistributeCertificateLeavesFailurePendingAndRetryClearsIt(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const domain = "spoke.example.com"
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example", Domain: domain,
		WGIP: "10.90.0.2", Token: "tok", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := writeCertificatePair(t, layout, domain)
	h := &upgradeHealthHandler{version: "v2", certErr: errors.New("spoke reload failed")}
	srv := httptest.NewServer((&nodeapi.Server{Token: "tok", Handler: h}).Mux())
	defer srv.Close()
	ctrl := &Controller{
		Layout: layout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}

	var events []deploy.Event
	err := ctrl.DistributeCertificate(context.Background(), domain, io.Discard, func(e deploy.Event) {
		events = append(events, e)
	})
	if err == nil || !strings.Contains(err.Error(), "spoke reload failed") {
		t.Fatalf("expected delivery failure, got %v", err)
	}
	// The single spoke is the only activation target, and the caller's bar must
	// reach it: a run reporting no events would sit at 0% throughout.
	wantSteps := []deploy.Event{
		{Index: 1, Total: 1, Label: "Deliver to tokyo", Detail: domain, Status: "running"},
		{Index: 1, Total: 1, Label: "Deliver to tokyo", Detail: domain, Status: "fail"},
	}
	if len(events) != len(wantSteps) {
		t.Fatalf("distribution progress = %+v, want %d steps", events, len(wantSteps))
	}
	for i, want := range wantSteps {
		got := events[i]
		got.Err = nil
		if got != want {
			t.Fatalf("distribution progress[%d] = %+v, want %+v", i, got, want)
		}
	}
	if events[1].Err == nil {
		t.Fatalf("failed step did not carry its error: %+v", events[1])
	}
	persisted, err := nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || !persisted[0].PendingCertificate {
		t.Fatalf("failed delivery was not left pending: %+v", persisted)
	}
	h.mu.Lock()
	firstReq, firstCount := h.certReq, h.applyCount
	h.certErr = nil
	h.mu.Unlock()
	if firstCount != 1 || firstReq.CertificatePEM != string(certPEM) || firstReq.PrivateKeyPEM != string(keyPEM) {
		t.Fatalf("unexpected first delivery: count=%d req=%+v", firstCount, firstReq)
	}

	if err := ctrl.RetryPendingCertificates(context.Background(), io.Discard); err != nil {
		t.Fatalf("RetryPendingCertificates: %v", err)
	}
	persisted, err = nodes.Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if persisted[0].PendingCertificate {
		t.Fatal("successful retry did not clear pending state")
	}
	h.mu.Lock()
	applyCount := h.applyCount
	h.mu.Unlock()
	if applyCount != 2 {
		t.Fatalf("certificate apply count = %d, want 2", applyCount)
	}
}

func TestAddNodeRollsBackRegistryPeerAndConfigOnFailure(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	hubKeyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	identity := nodes.HubIdentity{
		PrivateKey: hubKeyPair.PrivateKey, PublicKey: hubKeyPair.PublicKey, EndpointHost: "hub.example.com",
		ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}
	if err := nodes.SaveHubIdentity(layout, identity); err != nil {
		t.Fatal(err)
	}
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		t.Fatal(err)
	}
	hubRunner := &hubCommandRunner{}
	sshRunner := &bootstrapTestRunner{}
	var dialFingerprints []string
	var progressEvents []deploy.Event
	wgDir := filepath.Join(dir, "wireguard")
	ctrl := &Controller{
		Layout:              layout,
		Runner:              hubRunner,
		WGConfDir:           wgDir,
		ExpectedVersion:     "v-test",
		ExpectedCoreVersion: "v1.12.4",
		Bootstrapper: &bootstrap.Bootstrapper{Dial: func(_ context.Context, target bootstrap.Target) (bootstrap.Runner, error) {
			dialFingerprints = append(dialFingerprints, target.HostKeyFingerprint)
			return sshRunner, nil
		}},
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
		Progress:    func(event deploy.Event) { progressEvents = append(progressEvents, event) },
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: "http://127.0.0.1:1", Token: n.Token}
		},
	}
	// Cancellation is observed at the first overlay health probe, after the
	// registry, live peer, and durable config have all been modified.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ctrl.AddNode(ctx, AddNodeParams{
		Node:     bootstrap.Target{Host: "203.0.113.20", Port: 22, User: "root", HostKeyFingerprint: "SHA256:test"},
		Registry: nodes.Node{Alias: "failed", Domain: "failed.example.com"},
	}, io.Discard)
	if err == nil {
		t.Fatal("expected canceled health check to fail AddNode")
	}
	if len(progressEvents) == 0 {
		t.Fatal("AddNode emitted no progress")
	}
	lastProgress := progressEvents[len(progressEvents)-1]
	if lastProgress.Label != "Agent health" || lastProgress.Status != "fail" || !errors.Is(lastProgress.Err, context.Canceled) {
		t.Fatalf("last AddNode progress = %+v, want failed agent health step", lastProgress)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 0 {
		t.Fatalf("registry not rolled back: %+v err=%v", list, loadErr)
	}
	conf, readErr := os.ReadFile(filepath.Join(wgDir, wgnet.InterfaceName+".conf"))
	if readErr != nil {
		t.Fatalf("read restored hub config: %v", readErr)
	}
	if strings.Contains(string(conf), "[Peer]") {
		t.Fatalf("failed peer remained in durable config:\n%s", conf)
	}
	if !hubRunner.sawPeerAdd || !hubRunner.sawPeerRemove {
		t.Fatalf("live peer rollback missing: add=%v remove=%v commands=%+v", hubRunner.sawPeerAdd, hubRunner.sawPeerRemove, hubRunner.commands)
	}
	if len(dialFingerprints) != 3 {
		t.Fatalf("expected DetectArch, Provision, and failed-transaction Cleanup SSH connections: %v", dialFingerprints)
	}
	for _, fingerprint := range dialFingerprints {
		if fingerprint != "SHA256:test" {
			t.Fatalf("bootstrap connection did not pin the confirmed host key: %v", dialFingerprints)
		}
	}
}

func TestAddNodeRefusesExistingStandaloneWithoutInstallOrUninstall(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	hubKeyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	identity := nodes.HubIdentity{
		PrivateKey: hubKeyPair.PrivateKey, PublicKey: hubKeyPair.PublicKey, EndpointHost: "hub.example.com",
		ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}
	if err := nodes.SaveHubIdentity(layout, identity); err != nil {
		t.Fatal(err)
	}
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		t.Fatal(err)
	}
	// Keep the rest of the install path viable so the test would observe an
	// Install call if the existing-deployment guard were removed.
	writeCertificatePair(t, layout, "new.example.com")

	agent := &lifecycleHandler{health: nodeapi.HealthResponse{
		OK: true, Version: "v-test", Installed: true, SingBoxActive: true, Domain: "old.example.com",
	}}
	srv := httptest.NewServer((&nodeapi.Server{Token: "api-token", Handler: agent}).Mux())
	defer srv.Close()

	hubRunner := &hubCommandRunner{}
	sshRunner := &bootstrapTestRunner{}
	var dialCount int
	wgDir := filepath.Join(dir, "wireguard")
	ctrl := &Controller{
		Layout:              layout,
		Runner:              hubRunner,
		WGConfDir:           wgDir,
		ExpectedVersion:     "v-test",
		ExpectedCoreVersion: "v1.12.4",
		Bootstrapper: &bootstrap.Bootstrapper{Dial: func(_ context.Context, _ bootstrap.Target) (bootstrap.Runner, error) {
			dialCount++
			return sshRunner, nil
		}},
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
		NewClient: func(nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: "api-token", HTTP: srv.Client()}
		},
	}

	_, err = ctrl.AddNode(context.Background(), AddNodeParams{
		Node: bootstrap.Target{
			Host: "203.0.113.21", Port: 22, User: "root", HostKeyFingerprint: "SHA256:test",
		},
		Registry: nodes.Node{Alias: "existing", Domain: "new.example.com"},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "automatic standalone-to-spoke migration is disabled") {
		t.Fatalf("expected safe migration refusal, got %v", err)
	}
	agent.mu.Lock()
	healthCount, installCount, uninstallCount := agent.healthCount, agent.installCount, agent.uninstallCount
	agent.mu.Unlock()
	if healthCount < 2 {
		t.Fatalf("health probes = %d, want initial readiness plus pre-install check", healthCount)
	}
	if installCount != 0 || uninstallCount != 0 {
		t.Fatalf("existing runtime was mutated: install=%d uninstall=%d", installCount, uninstallCount)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 0 {
		t.Fatalf("temporary registry entry was not rolled back: %+v err=%v", list, loadErr)
	}
	conf, readErr := os.ReadFile(filepath.Join(wgDir, wgnet.InterfaceName+".conf"))
	if readErr != nil {
		t.Fatalf("read restored hub config: %v", readErr)
	}
	if strings.Contains(string(conf), "[Peer]") {
		t.Fatalf("temporary peer remained in durable config:\n%s", conf)
	}
	if !hubRunner.sawPeerAdd || !hubRunner.sawPeerRemove {
		t.Fatalf("temporary live peer was not rolled back: add=%v remove=%v", hubRunner.sawPeerAdd, hubRunner.sawPeerRemove)
	}
	if dialCount != 3 {
		t.Fatalf("SSH dial count = %d, want DetectArch, Provision, and bootstrap Cleanup", dialCount)
	}
}

func TestAddNodeRollbackUsesMatchingInstallTransaction(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	hubKeyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	identity := nodes.HubIdentity{
		PrivateKey: hubKeyPair.PrivateKey, PublicKey: hubKeyPair.PublicKey, EndpointHost: "hub.example.com",
		ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}
	if err := nodes.SaveHubIdentity(layout, identity); err != nil {
		t.Fatal(err)
	}
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		t.Fatal(err)
	}
	writeCertificatePair(t, layout, "failed.example.com")

	agent := &lifecycleHandler{
		health:     nodeapi.HealthResponse{OK: true, Version: "v-test"},
		installErr: errors.New("injected install failure"),
	}
	srv := httptest.NewServer((&nodeapi.Server{Token: "api-token", Handler: agent}).Mux())
	defer srv.Close()
	var dialCount int
	ctrl := &Controller{
		Layout:              layout,
		Runner:              &hubCommandRunner{},
		WGConfDir:           filepath.Join(dir, "wireguard"),
		ExpectedVersion:     "v-test",
		ExpectedCoreVersion: "v1.12.4",
		Bootstrapper: &bootstrap.Bootstrapper{Dial: func(_ context.Context, _ bootstrap.Target) (bootstrap.Runner, error) {
			dialCount++
			return &bootstrapTestRunner{}, nil
		}},
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
		NewClient: func(nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: "api-token", HTTP: srv.Client()}
		},
	}
	_, err = ctrl.AddNode(context.Background(), AddNodeParams{
		Node: bootstrap.Target{
			Host: "203.0.113.22", Port: 22, User: "root", HostKeyFingerprint: "SHA256:test",
		},
		Registry: nodes.Node{Alias: "failed", Domain: "failed.example.com"},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "injected install failure") {
		t.Fatalf("AddNode error = %v", err)
	}
	agent.mu.Lock()
	installReq, uninstallReq := agent.installReq, agent.uninstallReq
	installCount, uninstallCount := agent.installCount, agent.uninstallCount
	agent.mu.Unlock()
	if installCount != 1 || uninstallCount != 1 {
		t.Fatalf("install=%d uninstall=%d, want one owned rollback", installCount, uninstallCount)
	}
	if validateErr := nodeapi.ValidateInstallTransactionID(installReq.InstallTransactionID); validateErr != nil {
		t.Fatalf("invalid install transaction: %q: %v", installReq.InstallTransactionID, validateErr)
	}
	if !uninstallReq.KeepOverlay || uninstallReq.RollbackTransactionID != installReq.InstallTransactionID {
		t.Fatalf("rollback ownership mismatch: install=%+v uninstall=%+v", installReq, uninstallReq)
	}
	if list, loadErr := nodes.Load(layout); loadErr != nil || len(list) != 0 {
		t.Fatalf("failed node registry not rolled back: %+v err=%v", list, loadErr)
	}
	if dialCount != 3 {
		t.Fatalf("SSH dial count = %d, want DetectArch, Provision, Cleanup", dialCount)
	}
}

func TestForceDetachNodeDoesNotContactUnreachableAgent(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	keyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.SaveHubIdentity(layout, nodes.HubIdentity{
		PrivateKey: keyPair.PrivateKey, PublicKey: keyPair.PublicKey,
		EndpointHost: "hub.example.com", ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}); err != nil {
		t.Fatal(err)
	}
	peerKey, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.Add(layout, nodes.Node{
		Alias: "lost", SSHHost: "lost.example.com", Domain: "lost.example.com",
		WGIP: "10.90.0.2", WGPublicKey: peerKey.PublicKey, Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)
	runner := &hubCommandRunner{}
	ctrl := &Controller{
		Layout: layout, Runner: runner, WGConfDir: filepath.Join(root, "wireguard"),
		NewClient: func(nodes.Node) *nodeapi.Client {
			t.Fatal("force detach must not contact the agent")
			return nil
		},
	}
	if err := ctrl.ForceDetachNode(context.Background(), list[0], io.Discard); err != nil {
		t.Fatalf("ForceDetachNode: %v", err)
	}
	remaining, err := nodes.Load(layout)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("registry after detach = %+v err=%v", remaining, err)
	}
	if !runner.sawPeerRemove {
		t.Fatalf("force detach did not remove the live WireGuard peer: %+v", runner.commands)
	}
}

func TestForceDetachRetainsRegistryWhenDurableConfigWriteFails(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	hubKey, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.SaveHubIdentity(layout, nodes.HubIdentity{
		PrivateKey: hubKey.PrivateKey, PublicKey: hubKey.PublicKey,
		EndpointHost: "hub.example.com", ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}); err != nil {
		t.Fatal(err)
	}
	peerKey, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.Add(layout, nodes.Node{
		Alias: "write-failure", SSHHost: "write-failure.example.com", Domain: "write-failure.example.com",
		WGIP: "10.90.0.2", WGPublicKey: peerKey.PublicKey, Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)

	// Make the configured WireGuard directory impossible to create by placing a
	// regular file at one of its parent path components.
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &hubCommandRunner{}
	ctrl := &Controller{
		Layout: layout, Runner: runner, WGConfDir: filepath.Join(blocker, "wireguard"),
		NewClient: func(nodes.Node) *nodeapi.Client {
			t.Fatal("force detach must not contact the agent")
			return nil
		},
	}
	err = ctrl.ForceDetachNode(context.Background(), list[0], io.Discard)
	if err == nil || !strings.Contains(err.Error(), "persist overlay config") || !strings.Contains(err.Error(), "registry retained") {
		t.Fatalf("expected durable-config failure, got %v", err)
	}
	remaining, loadErr := nodes.Load(layout)
	if loadErr != nil || len(remaining) != 1 {
		t.Fatalf("registry was lost after config failure: %+v err=%v", remaining, loadErr)
	}
	if runner.sawPeerRemove {
		t.Fatalf("live peer was removed despite durable-config failure: %+v", runner.commands)
	}
}

func TestRemoveNodeRetainsRegistryWhenLivePeerRemovalFails(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	hubKey, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.SaveHubIdentity(layout, nodes.HubIdentity{
		PrivateKey: hubKey.PrivateKey, PublicKey: hubKey.PublicKey,
		EndpointHost: "hub.example.com", ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}); err != nil {
		t.Fatal(err)
	}
	peerKey, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.Add(layout, nodes.Node{
		Alias: "peer-failure", SSHHost: "peer-failure.example.com", Domain: "peer-failure.example.com",
		WGIP: "10.90.0.2", WGPublicKey: peerKey.PublicKey, Token: "node-token", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := nodes.Load(layout)

	agent := &lifecycleHandler{health: nodeapi.HealthResponse{OK: true, Version: "v-test", Installed: true}}
	srv := httptest.NewServer((&nodeapi.Server{Token: "api-token", Handler: agent}).Mux())
	defer srv.Close()
	runner := &hubCommandRunner{peerRemoveErr: errors.New("wg remove failed")}
	var progressEvents []deploy.Event
	wgDir := filepath.Join(root, "wireguard")
	ctrl := &Controller{
		Layout: layout, Runner: runner, WGConfDir: wgDir,
		Progress: func(event deploy.Event) { progressEvents = append(progressEvents, event) },
		NewClient: func(nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: "api-token", HTTP: srv.Client()}
		},
	}
	err = ctrl.RemoveNode(context.Background(), list[0], io.Discard)
	if err == nil ||
		!strings.Contains(err.Error(), "spoke teardown was acknowledged") ||
		!strings.Contains(err.Error(), "force-detach retry") ||
		!strings.Contains(err.Error(), "remove live overlay peer") {
		t.Fatalf("expected acknowledged teardown/local detach error, got %v", err)
	}
	if len(progressEvents) != 4 ||
		progressEvents[0].Label != "Remote uninstall" || progressEvents[0].Status != "running" ||
		progressEvents[1].Label != "Remote uninstall" || progressEvents[1].Status != "ok" ||
		progressEvents[2].Label != "Hub detach" || progressEvents[2].Status != "running" ||
		progressEvents[3].Label != "Hub detach" || progressEvents[3].Status != "fail" {
		t.Fatalf("remove progress events = %+v", progressEvents)
	}
	agent.mu.Lock()
	uninstallCount := agent.uninstallCount
	agent.mu.Unlock()
	if uninstallCount != 1 {
		t.Fatalf("remote uninstall calls = %d, want 1", uninstallCount)
	}
	remaining, loadErr := nodes.Load(layout)
	if loadErr != nil || len(remaining) != 1 {
		t.Fatalf("registry was lost after peer failure: %+v err=%v", remaining, loadErr)
	}
	if !runner.sawPeerRemove {
		t.Fatal("live peer removal was not attempted")
	}
	conf, readErr := os.ReadFile(filepath.Join(wgDir, wgnet.InterfaceName+".conf"))
	if readErr != nil {
		t.Fatalf("read fail-closed hub config: %v", readErr)
	}
	if strings.Contains(string(conf), peerKey.PublicKey) {
		t.Fatalf("failed peer remained in durable config:\n%s", conf)
	}
}

func TestTeardownAllRetainsOverlayWhenSpokeDoesNotAcknowledge(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.Add(layout, nodes.Node{
		Alias: "offline", SSHHost: "offline.example.com", Domain: "offline.example.com",
		WGIP: "10.90.0.2", Token: "token", Installed: true,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &hubCommandRunner{}
	ctrl := &Controller{
		Layout: layout,
		Runner: runner,
		NewClient: func(node nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{
				BaseURL: "http://offline.invalid", Token: node.Token,
				HTTP: &http.Client{Transport: monitorRoundTripper(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("node unreachable")
				})},
			}
		},
	}
	err := ctrl.TeardownAll(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "overlay retained") {
		t.Fatalf("expected fail-closed teardown error, got %v", err)
	}
	remaining, loadErr := nodes.Load(layout)
	if loadErr != nil || len(remaining) != 1 {
		t.Fatalf("offline node registry was lost: %+v err=%v", remaining, loadErr)
	}
	for _, command := range runner.commands {
		if strings.Contains(command.String(), "disable --now wg-quick") {
			t.Fatalf("hub overlay was stopped despite failed spoke teardown: %s", command.String())
		}
	}
}

type lifecycleHandler struct {
	mu              sync.Mutex
	health          nodeapi.HealthResponse
	healthCount     int
	installCount    int
	installReq      nodeapi.InstallRequest
	installErr      error
	uninstallCount  int
	uninstallReq    nodeapi.UninstallRequest
	subscriptionErr error
}

func (h *lifecycleHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthCount++
	return h.health
}

func (h *lifecycleHandler) Install(_ context.Context, req nodeapi.InstallRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.installCount++
	h.installReq = req
	if h.installErr == nil && !req.ConfigOnly {
		h.health.OK = true
		h.health.Installed = true
		h.health.SingBoxActive = true
		h.health.SingBoxVersion = req.SingBoxVersion
		h.health.Domain = req.Domain
	}
	return h.installErr
}

func (h *lifecycleHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}

func (h *lifecycleHandler) Uninstall(_ context.Context, req nodeapi.UninstallRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.uninstallCount++
	h.uninstallReq = req
	return nil
}

func (h *lifecycleHandler) Subscription(string) ([]byte, error) {
	if h.subscriptionErr != nil {
		return nil, h.subscriptionErr
	}
	return nil, nil
}

type upgradeHealthHandler struct {
	mu         sync.Mutex
	version    string
	upgradeReq nodeapi.UpgradeRequest
	certReq    nodeapi.CertRequest
	applyCount int
	certErr    error
}

type fixedHealthHandler struct {
	health  nodeapi.HealthResponse
	monitor http.Handler
}

func (h *fixedHealthHandler) Health() nodeapi.HealthResponse { return h.health }
func (*fixedHealthHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}
func (*fixedHealthHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error {
	return nil
}
func (*fixedHealthHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (*fixedHealthHandler) Subscription(string) ([]byte, error) { return nil, nil }
func (h *fixedHealthHandler) MonitorHandler() http.Handler      { return h.monitor }

func (h *upgradeHealthHandler) Health() nodeapi.HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return nodeapi.HealthResponse{OK: true, Version: h.version, Installed: true, SingBoxActive: true}
}

func (h *upgradeHealthHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}

func (h *upgradeHealthHandler) ApplyCert(_ context.Context, req nodeapi.CertRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.certReq = req
	h.applyCount++
	return h.certErr
}

func (h *upgradeHealthHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}

func (h *upgradeHealthHandler) Subscription(string) ([]byte, error) { return nil, nil }

func (h *upgradeHealthHandler) Upgrade(_ context.Context, req nodeapi.UpgradeRequest, _ io.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upgradeReq = req
	h.version = req.Version
	return nil
}

type hubCommandRunner struct {
	commands      []system.Command
	sawPeerAdd    bool
	sawPeerRemove bool
	peerRemoveErr error
}

func (r *hubCommandRunner) Run(cmd system.Command) error {
	r.commands = append(r.commands, cmd)
	if cmd.Name == "wg" && len(cmd.Args) > 0 {
		joined := strings.Join(cmd.Args, " ")
		r.sawPeerAdd = r.sawPeerAdd || strings.Contains(joined, "allowed-ips")
		r.sawPeerRemove = r.sawPeerRemove || strings.HasSuffix(joined, " remove")
		if strings.HasSuffix(joined, " remove") && r.peerRemoveErr != nil {
			return r.peerRemoveErr
		}
	}
	return nil
}

type bootstrapTestRunner struct{}

func (r *bootstrapTestRunner) Run(_ context.Context, cmd string, _ []byte) (string, error) {
	switch cmd {
	case "uname -m":
		return "aarch64\n", nil
	case "cat /etc/os-release":
		return "ID=ubuntu\nID_LIKE=debian\n", nil
	case "'/usr/bin/singbox-deploy-agent' --version":
		return "v-test\n", nil
	default:
		if strings.Contains(cmd, "wg pubkey") {
			kp, err := wgnet.GenerateKeyPair()
			if err != nil {
				return "", err
			}
			return kp.PublicKey + "\n", nil
		}
		return "", nil
	}
}

func (r *bootstrapTestRunner) Close() error { return nil }

func writeCertificatePair(t *testing.T, layout paths.Layout, domain string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate certificate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate certificate serial: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal certificate key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPath, keyPath := certmgr.CertPaths(layout, domain)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		t.Fatalf("mkdir certificate directory: %v", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write certificate key: %v", err)
	}
	return certPEM, keyPEM
}
