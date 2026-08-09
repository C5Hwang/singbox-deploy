package hubctl

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
)

// spokeFixture registers a spoke backed by a fake agent that serves the
// subscription files of a standalone installation.
func spokeFixture(t *testing.T, hubLayout paths.Layout, alias, domain, wgIP, salt string, port int) nodes.Node {
	t.Helper()
	spokeLayout := paths.LayoutForRoot(t.TempDir())
	spokeCfg := hysteriaConfig(t, domain, strings.ToUpper(alias), salt, port)
	if err := deploy.WriteSubscriptions(spokeLayout, spokeCfg); err != nil {
		t.Fatalf("spoke %s WriteSubscriptions: %v", alias, err)
	}
	srv := httptest.NewServer((&nodeapi.Server{
		Token:   "tok-" + alias,
		Handler: &subHandler{layout: spokeLayout, salt: salt},
	}).Mux())
	t.Cleanup(srv.Close)
	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: alias, SubscriptionAlias: alias, Domain: domain, WGIP: wgIP,
		Token: srv.URL, AgentPort: 19091, Installed: true,
	}); err != nil {
		t.Fatalf("register %s: %v", alias, err)
	}
	list, err := nodes.Load(hubLayout)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return list[len(list)-1]
}

// groupTestController dials the agent whose base URL was stashed in the node
// token, so each spoke fixture keeps its own server.
func groupTestController(layout paths.Layout) *Controller {
	return &Controller{
		Layout: layout,
		NewClient: func(n nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: n.Token, Token: "tok-" + n.Alias}
		},
	}
}

func installedHub(t *testing.T) (paths.Layout, deploy.Config) {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := hysteriaConfig(t, "hub.example.com", "HUB", "hubsalt", 9443)
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	if err := deploy.WriteSubscriptions(layout, cfg); err != nil {
		t.Fatalf("WriteSubscriptions: %v", err)
	}
	return layout, cfg
}

// An installation upgraded from the single-salt layout must keep serving the
// URL it already handed out, so the seeded group carries the previous salt.
func TestRefreshSubscriptionsSeedsTheFirstGroupFromTheInstalledSalt(t *testing.T) {
	hubLayout, hubCfg := installedHub(t)
	spokeFixture(t, hubLayout, "tokyo", "tokyo.example.com", "10.90.0.2", "tokyosalt", 8443)

	if err := groupTestController(hubLayout).RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	groups, err := subgroups.Load(hubLayout)
	if err != nil {
		t.Fatalf("load groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Salt != hubCfg.Salt || groups[0].Alias != subgroups.DefaultAlias {
		t.Fatalf("seeded groups = %#v", groups)
	}
	if !groups[0].HasMember(subgroups.HubMemberID) {
		t.Fatalf("seeded group should publish the hub: %#v", groups[0].Members)
	}
	if got := combinedNodeCount(t, hubLayout, hubCfg.Salt); got != 2 {
		t.Fatalf("seeded group should publish hub + spoke, got %d links", got)
	}
}

func TestRefreshSubscriptionsPublishesEachGroupSeparately(t *testing.T) {
	hubLayout, _ := installedHub(t)
	tokyo := spokeFixture(t, hubLayout, "tokyo", "tokyo.example.com", "10.90.0.2", "tokyosalt", 8443)
	london := spokeFixture(t, hubLayout, "london", "london.example.com", "10.90.0.3", "londonsalt", 8444)

	if err := subgroups.Save(hubLayout, []subgroups.Group{
		{ID: "aa11", Alias: "Everything", Salt: "allsalt",
			Members: []string{subgroups.HubMemberID, tokyo.ID, london.ID}},
		{ID: "bb22", Alias: "Asia", Salt: "asiasalt", Members: []string{tokyo.ID}},
	}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	if err := groupTestController(hubLayout).RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}

	all := combinedDefault(t, hubLayout, "allsalt")
	if n := strings.Count(all, "hysteria2://"); n != 3 {
		t.Fatalf("Everything group = %d links:\n%s", n, all)
	}
	asia := combinedDefault(t, hubLayout, "asiasalt")
	if n := strings.Count(asia, "hysteria2://"); n != 1 {
		t.Fatalf("Asia group = %d links:\n%s", n, asia)
	}
	if !strings.Contains(asia, "tokyo") || strings.Contains(asia, "london") || strings.Contains(asia, "HUB") {
		t.Fatalf("Asia group published the wrong nodes:\n%s", asia)
	}
	// The pre-group salt is no longer published by any group and must be swept.
	if _, err := os.Stat(filepath.Join(hubLayout.SubscribeDir, "default", deploy.SubscriptionToken("hubsalt"))); !os.IsNotExist(err) {
		t.Fatalf("retired token survived: %v", err)
	}
}

// A spoke published by two groups must be contacted once, not once per group.
func TestRefreshSubscriptionsFetchesEachSpokeOnce(t *testing.T) {
	hubLayout, _ := installedHub(t)
	tokyo := spokeFixture(t, hubLayout, "tokyo", "tokyo.example.com", "10.90.0.2", "tokyosalt", 8443)
	if err := subgroups.Save(hubLayout, []subgroups.Group{
		{ID: "aa11", Alias: "One", Salt: "onesalt", Members: []string{subgroups.HubMemberID, tokyo.ID}},
		{ID: "bb22", Alias: "Two", Salt: "twosalt", Members: []string{tokyo.ID}},
		{ID: "cc33", Alias: "Three", Salt: "threesalt", Members: []string{tokyo.ID}},
	}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	ctrl := groupTestController(hubLayout)
	dials := 0
	inner := ctrl.NewClient
	ctrl.NewClient = func(n nodes.Node) *nodeapi.Client {
		dials++
		return inner(n)
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	// One health probe plus one subscription client for the single spoke.
	if dials > 2 {
		t.Fatalf("spoke was contacted %d times for 3 groups", dials)
	}
	if n := strings.Count(combinedDefault(t, hubLayout, "twosalt"), "hysteria2://"); n != 1 {
		t.Fatalf("second group = %d links", n)
	}
}

// A spoke that belongs to no group is registered and managed, but nothing
// published, so the hub must not contact it at all.
func TestRefreshSubscriptionsSkipsSpokesNoGroupPublishes(t *testing.T) {
	hubLayout, _ := installedHub(t)
	if err := nodes.Add(hubLayout, nodes.Node{
		Alias: "unpublished", Domain: "unpublished.example.com", WGIP: "10.90.0.2",
		Token: "tok", AgentPort: 19091, Installed: true,
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := subgroups.Save(hubLayout, []subgroups.Group{
		{ID: "aa11", Alias: "Hub only", Salt: "hubonly", Members: []string{subgroups.HubMemberID}},
	}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	ctrl := &Controller{
		Layout: hubLayout,
		NewClient: func(nodes.Node) *nodeapi.Client {
			t.Fatal("a spoke no group publishes must not be contacted")
			return nil
		},
	}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	if got := combinedNodeCount(t, hubLayout, "hubonly"); got != 1 {
		t.Fatalf("expected hub-only group, got %d links", got)
	}
}

// Removing a group's last spoke leaves it with nothing to aggregate. The refresh
// must retire that group's published files and say so, rather than serving a
// profile whose selectors have no members.
func TestRefreshSubscriptionsRetiresAGroupLeftWithoutNodes(t *testing.T) {
	hubLayout, _ := installedHub(t)
	tokyo := spokeFixture(t, hubLayout, "tokyo", "tokyo.example.com", "10.90.0.2", "tokyosalt", 8443)
	if _, err := subgroups.Add(hubLayout, subgroups.Group{
		Alias: "Spoke only", Salt: "spokeonly", Members: []string{tokyo.ID},
	}); err != nil {
		t.Fatalf("add spoke-only group: %v", err)
	}
	ctrl := groupTestController(hubLayout)
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		t.Fatalf("RefreshSubscriptions: %v", err)
	}
	published := filepath.Join(hubLayout.SubscribeDir, "singboxProfiles", deploy.SubscriptionToken("spokeonly"))
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("spoke-only group was not published: %v", err)
	}

	if err := subgroups.DropMember(hubLayout, tokyo.ID); err != nil {
		t.Fatalf("drop the spoke from every group: %v", err)
	}
	err := ctrl.RefreshSubscriptions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no nodes to publish") {
		t.Fatalf("emptied group was not reported: %v", err)
	}
	if _, statErr := os.Stat(published); !os.IsNotExist(statErr) {
		t.Fatalf("emptied group kept serving its sing-box profile: %v", statErr)
	}
}
