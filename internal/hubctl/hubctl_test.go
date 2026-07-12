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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
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
	c := &Controller{Layout: layout}
	c.defaults()
	// Place a cert pair the request should embed.
	certPEM, keyPEM := writeCertificatePair(t, layout, "spoke.example.com")

	node := nodes.Node{
		Alias:            "tokyo",
		Domain:           "spoke.example.com",
		EnabledProtocols: []string{"hysteria2"},
		Hysteria2Port:    9443,
		Monitor:          true,
		MonitorAlias:     "Tokyo",
	}
	req, err := c.buildInstallRequest(node)
	if err != nil {
		t.Fatalf("buildInstallRequest: %v", err)
	}
	if req.Domain != "spoke.example.com" || req.DisplayName != "tokyo" {
		t.Fatalf("unexpected request identity: %+v", req)
	}
	if req.CertificatePEM != string(certPEM) || req.PrivateKeyPEM != string(keyPEM) {
		t.Fatalf("certificate not embedded: %+v", req)
	}
	if req.Ports.Hysteria2 != 9443 || !req.Monitor {
		t.Fatalf("ports/monitor not mapped: %+v", req)
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

	err := ctrl.DistributeCertificate(context.Background(), domain, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "spoke reload failed") {
		t.Fatalf("expected delivery failure, got %v", err)
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
	wgDir := filepath.Join(dir, "wireguard")
	ctrl := &Controller{
		Layout:          layout,
		Runner:          hubRunner,
		WGConfDir:       wgDir,
		ExpectedVersion: "v-test",
		Bootstrapper: &bootstrap.Bootstrapper{Dial: func(_ context.Context, target bootstrap.Target) (bootstrap.Runner, error) {
			dialFingerprints = append(dialFingerprints, target.HostKeyFingerprint)
			return sshRunner, nil
		}},
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
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

type upgradeHealthHandler struct {
	mu         sync.Mutex
	version    string
	upgradeReq nodeapi.UpgradeRequest
	certReq    nodeapi.CertRequest
	applyCount int
	certErr    error
}

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
}

func (r *hubCommandRunner) Run(cmd system.Command) error {
	r.commands = append(r.commands, cmd)
	if cmd.Name == "wg" && len(cmd.Args) > 0 {
		joined := strings.Join(cmd.Args, " ")
		r.sawPeerAdd = r.sawPeerAdd || strings.Contains(joined, "allowed-ips")
		r.sawPeerRemove = r.sawPeerRemove || strings.HasSuffix(joined, " remove")
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
		NotAfter:     now.Add(24 * time.Hour),
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
