package deploy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

func groupTestConfig(t *testing.T) deploy.Config {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	return deploy.Config{
		Domain:            "hub.example.com",
		Enabled:           []config.Protocol{config.ProtocolHysteria2},
		DisplayName:       "HUB",
		Salt:              "hubsalt",
		SubscribePort:     deploy.DefaultSubscribePort,
		RealityServerName: "www.microsoft.com",
		Ports:             config.Ports{Hysteria2: 9443},
		Creds:             creds,
	}
}

// spokeSource renders a standalone node's outputs and packages them the way the
// hub receives them from an agent over the overlay.
func spokeSource(t *testing.T, alias, domain, salt string, port int) deploy.SubscriptionSource {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	cfg.Domain = domain
	cfg.DisplayName = alias
	cfg.Salt = salt
	cfg.Ports = config.Ports{Hysteria2: port}
	if err := deploy.WriteSubscriptions(layout, cfg); err != nil {
		t.Fatalf("WriteSubscriptions for %s: %v", alias, err)
	}
	read := func(dir string) []byte {
		body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken(salt)))
		if err != nil {
			t.Fatalf("read %s for %s: %v", dir, alias, err)
		}
		return body
	}
	return deploy.SubscriptionSource{
		Alias:       alias,
		DefaultBody: read("default"),
		ClashBody:   read("clashMeta"),
		SingBoxBody: read("singboxProfiles"),
		SurgeBody:   read("surge"),
	}
}

func groupDefaultBody(t *testing.T, layout paths.Layout, salt string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", deploy.SubscriptionToken(salt)))
	if err != nil {
		t.Fatalf("read published default for salt %q: %v", salt, err)
	}
	decoded, err := subscription.DecodeBase64(string(body))
	if err != nil {
		t.Fatalf("decode published default: %v", err)
	}
	return decoded
}

func TestWriteSubscriptionGroupsPublishesOneTokenPerGroup(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	tokyo := spokeSource(t, "tokyo", "tokyo.example.com", "tokyosalt", 8443)
	london := spokeSource(t, "london", "london.example.com", "londonsalt", 8444)

	err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "everything", Sources: []deploy.SubscriptionSource{tokyo, london}, IncludeLocal: true},
		{Salt: "asiaonly", Sources: []deploy.SubscriptionSource{tokyo}},
	})
	if err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}

	all := groupDefaultBody(t, layout, "everything")
	if n := strings.Count(all, "hysteria2://"); n != 3 {
		t.Fatalf("expected hub + 2 spokes, got %d links:\n%s", n, all)
	}
	for _, want := range []string{"HUB", "tokyo", "london"} {
		if !strings.Contains(all, want) {
			t.Fatalf("combined group is missing %q:\n%s", want, all)
		}
	}

	// A group that does not name the hub publishes only its spokes.
	asia := groupDefaultBody(t, layout, "asiaonly")
	if n := strings.Count(asia, "hysteria2://"); n != 1 {
		t.Fatalf("expected the single selected spoke, got %d links:\n%s", n, asia)
	}
	if !strings.Contains(asia, "tokyo") || strings.Contains(asia, "london") || strings.Contains(asia, "HUB") {
		t.Fatalf("narrowed group published the wrong nodes:\n%s", asia)
	}
}

// Each group's Clash and Surge profiles must point at that group's own provider
// URL; borrowing another group's token would serve the wrong node list.
func TestWriteSubscriptionGroupsUsesPerGroupProviderURLs(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "first", IncludeLocal: true},
		{Salt: "second", IncludeLocal: true},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}
	for _, salt := range []string{"first", "second"} {
		token := deploy.SubscriptionToken(salt)
		body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "clashMetaProfiles", token))
		if err != nil {
			t.Fatalf("read clash profile for %s: %v", salt, err)
		}
		if !strings.Contains(string(body), "/s/clashMeta/"+token) {
			t.Fatalf("clash profile for %s does not reference its own token:\n%s", salt, body)
		}
	}
}

// Publishing must sweep tokens that no longer belong to a group while leaving
// every live group's files in place.
func TestWriteSubscriptionGroupsPrunesOnlyRetiredTokens(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "keep", IncludeLocal: true},
		{Salt: "retire", IncludeLocal: true},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "keep", IncludeLocal: true},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups after deletion: %v", err)
	}
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "singboxProfiles", "surge", "surgeProfiles"} {
		if _, err := os.Stat(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken("keep"))); err != nil {
			t.Fatalf("live group lost its %s file: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken("retire"))); !os.IsNotExist(err) {
			t.Fatalf("deleted group kept its %s file: %v", dir, err)
		}
	}
}

// A group left with no members — every spoke it named has since been removed —
// must publish nothing. Rendering it anyway emits a sing-box profile whose node
// list is a bare JSON null and whose selectors have no members, which the client
// rejects with "unknown outbound type" before it ever reaches the node list.
func TestWriteSubscriptionGroupsSkipsAGroupWithNoNodes(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "populated", IncludeLocal: true},
		{Salt: "emptied"},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "singboxProfiles", "surge", "surgeProfiles"} {
		if _, err := os.Stat(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken("populated"))); err != nil {
			t.Fatalf("populated group lost its %s file: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken("emptied"))); !os.IsNotExist(err) {
			t.Fatalf("group with no nodes published a %s file: %v", dir, err)
		}
	}
}

// Losing its last member must also retire a group's already-published files,
// rather than leaving the previous node list served under the same URL.
func TestWriteSubscriptionGroupsRetiresAGroupThatLosesItsLastNode(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	cfg := groupTestConfig(t)
	tokyo := spokeSource(t, "tokyo", "tokyo.example.com", "tokyosalt", 8443)
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "spokeonly", Sources: []deploy.SubscriptionSource{tokyo}},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups: %v", err)
	}
	if body := groupDefaultBody(t, layout, "spokeonly"); !strings.Contains(body, "tokyo") {
		t.Fatalf("spoke-only group did not publish its spoke:\n%s", body)
	}
	if err := deploy.WriteSubscriptionGroups(layout, cfg, []deploy.SubscriptionGroupSpec{
		{Salt: "spokeonly"},
	}); err != nil {
		t.Fatalf("WriteSubscriptionGroups after the spoke left: %v", err)
	}
	for _, dir := range []string{"default", "singboxProfiles"} {
		if _, err := os.Stat(filepath.Join(layout.SubscribeDir, dir, deploy.SubscriptionToken("spokeonly"))); !os.IsNotExist(err) {
			t.Fatalf("emptied group kept serving its %s file: %v", dir, err)
		}
	}
}

// The hub's own nodes are enough to publish a group; only a group with neither
// the hub nor a reachable spoke is skipped.
func TestSubscriptionGroupSpecPublishesNodes(t *testing.T) {
	tokyo := deploy.SubscriptionSource{Alias: "tokyo"}
	for _, tc := range []struct {
		name string
		spec deploy.SubscriptionGroupSpec
		want bool
	}{
		{"hub only", deploy.SubscriptionGroupSpec{IncludeLocal: true}, true},
		{"spoke only", deploy.SubscriptionGroupSpec{Sources: []deploy.SubscriptionSource{tokyo}}, true},
		{"hub and spoke", deploy.SubscriptionGroupSpec{IncludeLocal: true, Sources: []deploy.SubscriptionSource{tokyo}}, true},
		{"nothing", deploy.SubscriptionGroupSpec{}, false},
	} {
		if got := tc.spec.PublishesNodes(); got != tc.want {
			t.Errorf("%s: PublishesNodes() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
