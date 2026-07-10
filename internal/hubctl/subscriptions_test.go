package hubctl

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// subHandler is a fake agent that serves a spoke's subscription files.
type subHandler struct {
	layout paths.Layout
	salt   string
}

func (h *subHandler) Health() nodeapi.HealthResponse { return nodeapi.HealthResponse{OK: true} }
func (h *subHandler) Install(context.Context, nodeapi.InstallRequest, io.Writer) error {
	return nil
}
func (h *subHandler) ApplyCert(context.Context, nodeapi.CertRequest, io.Writer) error { return nil }
func (h *subHandler) Uninstall(context.Context, nodeapi.UninstallRequest, io.Writer) error {
	return nil
}
func (h *subHandler) Subscription(format string) ([]byte, error) {
	dir := map[string]string{
		nodeapi.FormatDefault:         "default",
		nodeapi.FormatClashMeta:       "clashMeta",
		nodeapi.FormatSingBoxProfiles: "singboxProfiles",
		nodeapi.FormatSurge:           "surge",
	}[format]
	token := deploy.SubscriptionToken(h.salt)
	return os.ReadFile(filepath.Join(h.layout.SubscribeDir, dir, token))
}

func hysteriaConfig(t *testing.T, domain, name, salt string, port int) deploy.Config {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	return deploy.Config{
		Domain:            domain,
		Enabled:           []config.Protocol{config.ProtocolHysteria2},
		DisplayName:       name,
		Salt:              salt,
		SubscribePort:     deploy.DefaultSubscribePort,
		RealityServerName: "www.microsoft.com",
		Ports:             config.Ports{Hysteria2: port},
		Creds:             creds,
	}
}

func TestRefreshSubscriptionsAggregatesOverWG(t *testing.T) {
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := deploy.WriteSubscriptions(hubLayout, hubCfg); err != nil {
		t.Fatalf("hub WriteSubscriptions: %v", err)
	}

	// A spoke with its own subscription files, served by a fake agent.
	spokeLayout := paths.LayoutForRoot(t.TempDir())
	spokeCfg := hysteriaConfig(t, "spoke.example.com", "SPOKE", "spokesalt", 8443)
	if err := deploy.WriteSubscriptions(spokeLayout, spokeCfg); err != nil {
		t.Fatalf("spoke WriteSubscriptions: %v", err)
	}
	srv := httptest.NewServer((&nodeapi.Server{
		Token:   "tok",
		Handler: &subHandler{layout: spokeLayout, salt: spokeCfg.Salt},
	}).Mux())
	defer srv.Close()

	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}

	ctrl := &Controller{
		Layout: hubLayout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}

	token := deploy.SubscriptionToken(hubCfg.Salt)
	body, err := os.ReadFile(filepath.Join(hubLayout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read combined default: %v", err)
	}
	decoded, err := subscription.DecodeBase64(string(body))
	if err != nil {
		t.Fatalf("decode combined default: %v", err)
	}
	if n := strings.Count(decoded, "hysteria2://"); n != 2 {
		t.Fatalf("expected hub + spoke (2 links), got %d:\n%s", n, decoded)
	}
	if !strings.Contains(decoded, "tokyo") {
		t.Fatalf("combined output should include the spoke alias:\n%s", decoded)
	}
}

func TestRefreshSubscriptionsSkipsUnreachableSpoke(t *testing.T) {
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: "dead", Domain: "dead.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	ctrl := &Controller{
		Layout: hubLayout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			// Points nowhere; the fetch fails.
			return &nodeapi.Client{BaseURL: "http://127.0.0.1:1", Token: n.Token}
		},
	}
	// The unreachable spoke is skipped; a soft error is returned but the hub's
	// own (local-only) subscription is still published.
	err := ctrl.RefreshSubscriptions(context.Background())
	if err == nil {
		t.Fatalf("expected a soft error for the unreachable spoke")
	}
	token := deploy.SubscriptionToken(hubCfg.Salt)
	body, rerr := os.ReadFile(filepath.Join(hubLayout.SubscribeDir, "default", token))
	if rerr != nil {
		t.Fatalf("local subscription should still be written: %v", rerr)
	}
	decoded, _ := subscription.DecodeBase64(string(body))
	if n := strings.Count(decoded, "hysteria2://"); n != 1 {
		t.Fatalf("expected hub-only (1 link) when spoke unreachable, got %d", n)
	}
}

func TestRefreshSubscriptionsHonorsNodeInclusionFlag(t *testing.T) {
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: "excluded", SSHHost: "excluded.example.com", Domain: "excluded.example.com",
		WGIP: "10.90.0.2", Token: "tok", Installed: true,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	list, _ := nodes.Load(hubLayout)
	list[0].IncludeInSubscription = false
	if err := nodes.Update(hubLayout, list[0]); err != nil {
		t.Fatalf("exclude node: %v", err)
	}
	ctrl := &Controller{
		Layout: hubLayout,
		NewClient: func(nodes.Node) *nodeapi.Client {
			t.Fatal("excluded node should not be contacted")
			return nil
		},
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	token := deploy.SubscriptionToken(hubCfg.Salt)
	body, err := os.ReadFile(filepath.Join(hubLayout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := subscription.DecodeBase64(string(body))
	if n := strings.Count(decoded, "hysteria2://"); n != 1 {
		t.Fatalf("expected hub-only subscription, got %d links:\n%s", n, decoded)
	}
}
