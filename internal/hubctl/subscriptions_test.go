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

// A real agent installs relay forwarding as well as serving subscriptions, and
// the reconcile pass pushes both.
func (h *subHandler) ApplyRelay(context.Context, nodeapi.RelayRequest, io.Writer) error { return nil }

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
		Alias: "tokyo-server", SubscriptionAlias: "tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
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

// A spoke that answered once keeps contributing its nodes while it is offline;
// otherwise a transient outage would silently shrink every published
// subscription. Once the node leaves aggregation the cache must go with it.
func TestRefreshSubscriptionsReusesCachedSpokeAndPrunesRemoved(t *testing.T) {
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := deploy.WriteSubscriptions(hubLayout, hubCfg); err != nil {
		t.Fatalf("hub WriteSubscriptions: %v", err)
	}
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

	reachable := true
	ctrl := &Controller{
		Layout: hubLayout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			if !reachable {
				return &nodeapi.Client{BaseURL: "http://127.0.0.1:1", Token: n.Token}
			}
			return &nodeapi.Client{BaseURL: srv.URL, Token: n.Token, HTTP: srv.Client()}
		},
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	if got := combinedNodeCount(t, hubLayout, hubCfg.Salt); got != 2 {
		t.Fatalf("expected hub + spoke while reachable, got %d links", got)
	}

	// The spoke goes offline: its nodes must survive, relabeled with the
	// subscription alias the operator changed in the meantime.
	list, _ := nodes.Load(hubLayout)
	nodeID := list[0].ID
	if err := nodes.Mutate(hubLayout, nodeID, func(current *nodes.Node) error {
		current.SubscriptionAlias = "osaka"
		return nil
	}); err != nil {
		t.Fatalf("rename subscription source: %v", err)
	}
	reachable = false
	err := ctrl.RefreshSubscriptions(context.Background())
	if err == nil {
		t.Fatal("expected a soft error while the spoke is unreachable")
	}
	if !strings.Contains(err.Error(), "reused the last subscription cached") {
		t.Fatalf("error should report the fallback: %v", err)
	}
	decoded := combinedDefault(t, hubLayout, hubCfg.Salt)
	if n := strings.Count(decoded, "hysteria2://"); n != 2 {
		t.Fatalf("cached spoke nodes were dropped, got %d links:\n%s", n, decoded)
	}
	if !strings.Contains(decoded, "osaka") || strings.Contains(decoded, "tokyo") {
		t.Fatalf("cached bodies were not relabeled with the current alias:\n%s", decoded)
	}

	// Removing the node must drop its cache so it cannot reappear later.
	if err := nodes.Remove(hubLayout, nodeID); err != nil {
		t.Fatalf("remove node: %v", err)
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions after removal: %v", err)
	}
	if got := combinedNodeCount(t, hubLayout, hubCfg.Salt); got != 1 {
		t.Fatalf("expected hub-only after removal, got %d links", got)
	}
	if _, err := os.Stat(filepath.Join(hubLayout.StateDir, spokeSubscriptionCacheDir, nodeID)); !os.IsNotExist(err) {
		t.Fatalf("cache for the removed node survived: %v", err)
	}
}

// The registry refuses duplicate spoke aliases, but it cannot see the hub's own
// display name and cannot retroactively fix a registry written before that
// rule. Aggregation must still emit distinct node names, because duplicate
// Clash proxy names and duplicate sing-box outbound tags break clients.
func TestRefreshSubscriptionsDisambiguatesCollidingSourceAliases(t *testing.T) {
	hubLayout := paths.LayoutForRoot(t.TempDir())
	hubCfg := hysteriaConfig(t, "hub.example.com", "tokyo", "hubsalt", 9443)
	if err := deploy.WriteInstallState(hubLayout.StateDir, hubCfg); err != nil {
		t.Fatalf("hub WriteInstallState: %v", err)
	}
	if err := deploy.WriteSubscriptions(hubLayout, hubCfg); err != nil {
		t.Fatalf("hub WriteSubscriptions: %v", err)
	}
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

	// The spoke alias collides with the hub's display name, which no registry
	// constraint can prevent.
	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: "Tokyo", Domain: "spoke.example.com", WGIP: "10.90.0.2",
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
	err := ctrl.RefreshSubscriptions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("collision should be reported: %v", err)
	}

	decoded := combinedDefault(t, hubLayout, hubCfg.Salt)
	if n := strings.Count(decoded, "hysteria2://"); n != 2 {
		t.Fatalf("expected hub + spoke, got %d links:\n%s", n, decoded)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(decoded), "\n") {
		_, name, found := strings.Cut(line, "#")
		if !found {
			t.Fatalf("share link has no name fragment: %s", line)
		}
		if names[name] {
			t.Fatalf("duplicate node name %q in combined output:\n%s", name, decoded)
		}
		names[name] = true
	}
	if !strings.Contains(decoded, "Tokyo-2") {
		t.Fatalf("colliding spoke was not renumbered:\n%s", decoded)
	}
}

func combinedDefault(t *testing.T, layout paths.Layout, salt string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", deploy.SubscriptionToken(salt)))
	if err != nil {
		t.Fatalf("read combined default: %v", err)
	}
	decoded, err := subscription.DecodeBase64(string(body))
	if err != nil {
		t.Fatalf("decode combined default: %v", err)
	}
	return decoded
}

func combinedNodeCount(t *testing.T, layout paths.Layout, salt string) int {
	t.Helper()
	return strings.Count(combinedDefault(t, layout, salt), "hysteria2://")
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

// A spoke excluded under the pre-groups inclusion flag must stay out of the
// group seeded from that installation, so upgrading publishes exactly what the
// single-salt layout published.
func TestSeededGroupHonorsLegacyNodeInclusionFlag(t *testing.T) {
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
