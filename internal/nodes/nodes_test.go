package nodes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

func TestNodeRoundTrip(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	n := Node{
		Alias:              "tokyo",
		SSHHost:            "203.0.113.9",
		SSHPort:            22,
		SSHUser:            "root",
		WGPublicKey:        "pub",
		WGIP:               "10.90.0.2",
		Token:              "tok",
		AgentPort:          19091,
		Arch:               "arm64",
		Installed:          true,
		AgentVersion:       "v2.0.0",
		LastSeen:           time.Date(2026, 7, 10, 1, 2, 3, 4, time.UTC),
		Domain:             "spoke.example.com",
		EnabledProtocols:   []string{"hysteria2", "tuic"},
		Hysteria2Port:      8443,
		Monitor:            true,
		MonitorAlias:       "Tokyo",
		PendingCertificate: true,
	}
	if err := Add(layout, n); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 node, got %d", len(list))
	}
	got := list[0]
	if got.ID == "" || got.Alias != "tokyo" || got.WGIP != "10.90.0.2" || !got.Installed {
		t.Fatalf("node round-trip mismatch: %+v", got)
	}
	if !got.IncludeInSubscription || got.AgentVersion != "v2.0.0" || !got.LastSeen.Equal(n.LastSeen) || !got.PendingCertificate {
		t.Fatalf("node status round-trip mismatch: %+v", got)
	}
	if len(got.EnabledProtocols) != 2 || got.EnabledProtocols[0] != "hysteria2" {
		t.Fatalf("protocols mismatch: %+v", got.EnabledProtocols)
	}
	if got.AgentAddr() != "10.90.0.2:19091" {
		t.Fatalf("AgentAddr = %q", got.AgentAddr())
	}
}

func TestUpdateAndRemove(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	_ = Add(layout, Node{Alias: "a", WGIP: "10.90.0.2"})
	_ = Add(layout, Node{Alias: "b", WGIP: "10.90.0.3"})

	if err := Update(layout, Node{Alias: "a2", WGIP: "10.90.0.2"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, _ := Load(layout)
	if list[0].Alias != "a2" {
		t.Fatalf("update did not take: %+v", list[0])
	}
	if err := Remove(layout, "10.90.0.2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	list, _ = Load(layout)
	if len(list) != 1 || list[0].WGIP != "10.90.0.3" {
		t.Fatalf("remove mismatch: %+v", list)
	}
}

func TestUpdateAndRemovePreferStableID(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := Add(layout, Node{Alias: "a", SSHHost: "one.example", WGIP: "10.90.0.2"}); err != nil {
		t.Fatal(err)
	}
	if err := Add(layout, Node{Alias: "b", SSHHost: "two.example", WGIP: "10.90.0.3"}); err != nil {
		t.Fatal(err)
	}
	list, _ := Load(layout)
	firstID := list[0].ID
	updated := list[0]
	updated.Alias = "moved"
	updated.WGIP = "10.90.0.44"
	if err := Update(layout, updated); err != nil {
		t.Fatalf("Update by ID: %v", err)
	}
	list, _ = Load(layout)
	if list[0].ID != firstID || list[0].WGIP != "10.90.0.44" {
		t.Fatalf("stable-ID update mismatch: %+v", list)
	}
	// A supplied (but unknown) ID must not fall back to a coincidentally reused
	// overlay address and overwrite a different node.
	wrong := updated
	wrong.ID = "ffffffffffffffffffffffffffffffff"
	if err := Update(layout, wrong); err == nil {
		t.Fatal("Update unexpectedly fell back from an unknown ID")
	}
	if err := Remove(layout, firstID); err != nil {
		t.Fatalf("Remove by ID: %v", err)
	}
	list, _ = Load(layout)
	if len(list) != 1 || list[0].Alias != "b" {
		t.Fatalf("stable-ID remove mismatch: %+v", list)
	}
}

func TestMutatePreservesConcurrentIndependentFieldUpdates(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const id = "11111111111111111111111111111111"
	if err := Add(layout, Node{
		ID:      id,
		Alias:   "original",
		WGIP:    "10.90.0.2",
		Domain:  "spoke.example.com",
		Monitor: false,
	}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		errs <- Mutate(layout, id, func(n *Node) error {
			n.AgentVersion = "v2.0.0"
			n.LastSeen = time.Date(2026, 7, 10, 2, 3, 4, 0, time.UTC)
			return nil
		})
	}()
	go func() {
		<-start
		errs <- Mutate(layout, id, func(n *Node) error {
			n.PendingCertificate = true
			return nil
		})
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Mutate: %v", err)
		}
	}

	list, err := Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].AgentVersion != "v2.0.0" || list[0].LastSeen.IsZero() || !list[0].PendingCertificate {
		t.Fatalf("independent mutations were not preserved: %+v", list)
	}
}

func TestConcurrentAddsDoNotDiscardNodes(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const count = 16
	start := make(chan struct{})
	errs := make(chan error, count)
	var ready sync.WaitGroup
	ready.Add(count)
	for i := range count {
		go func(i int) {
			ready.Done()
			<-start
			errs <- Add(layout, Node{
				Alias:   fmt.Sprintf("node-%02d", i),
				SSHHost: fmt.Sprintf("192.0.2.%d", i+1),
				WGIP:    fmt.Sprintf("10.90.0.%d", i+2),
				Domain:  fmt.Sprintf("node-%02d.example.com", i),
			})
		}(i)
	}
	ready.Wait()
	close(start)
	for range count {
		if err := <-errs; err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	list, err := Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != count {
		t.Fatalf("concurrent Add retained %d nodes, want %d", len(list), count)
	}
}

func TestReorderUsesCurrentNodesAndAppendsUnlistedInPlace(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	ids := []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
		"44444444444444444444444444444444",
	}
	for i, id := range ids {
		if err := Add(layout, Node{ID: id, Alias: string(rune('a' + i)), WGIP: fmt.Sprintf("10.90.0.%d", i+2)}); err != nil {
			t.Fatal(err)
		}
	}

	// Model a TUI retaining only its old ID order while another process updates
	// a status field. Reorder must read the current objects inside its own
	// transaction rather than write back the stale Node snapshot.
	stale, err := Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := Mutate(layout, ids[1], func(n *Node) error {
		n.AgentVersion = "v2.1.0"
		n.PendingCertificate = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Reorder(layout, []string{stale[2].ID, strings.ToUpper(stale[0].ID)}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	list, err := Load(layout)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{ids[2], ids[0], ids[1], ids[3]}
	for i, want := range wantIDs {
		if list[i].ID != want {
			t.Fatalf("node order = %+v, want IDs %+v", list, wantIDs)
		}
	}
	if list[2].AgentVersion != "v2.1.0" || !list[2].PendingCertificate {
		t.Fatalf("reorder discarded current node fields: %+v", list[2])
	}
}

func TestReorderRejectsDuplicateAndMissingIDsWithoutChangingOrder(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const firstID = "1111111111111111111111111111111a"
	const secondID = "2222222222222222222222222222222b"
	if err := Add(layout, Node{ID: firstID, Alias: "first", WGIP: "10.90.0.2"}); err != nil {
		t.Fatal(err)
	}
	if err := Add(layout, Node{ID: secondID, Alias: "second", WGIP: "10.90.0.3"}); err != nil {
		t.Fatal(err)
	}

	for name, order := range map[string][]string{
		"duplicate": {firstID, strings.ToUpper(firstID)},
		"missing":   {"ffffffffffffffffffffffffffffffff"},
		"empty":     {" "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Reorder(layout, order); err == nil {
				t.Fatalf("Reorder(%v) unexpectedly succeeded", order)
			}
			list, loadErr := Load(layout)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(list) != 2 || list[0].ID != firstID || list[1].ID != secondID {
				t.Fatalf("failed reorder changed registry: %+v", list)
			}
		})
	}
}

func TestLoadMigratesLegacyIDAndSubscriptionDefault(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	entry := filepath.Join(layout.StateDir, nodesDir, "001")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"alias":    "legacy",
		"ssh_host": "old.example.com",
		"wg_ip":    "10.90.0.2",
		"domain":   "spoke.example.com",
	} {
		if err := os.WriteFile(filepath.Join(entry, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Load(layout)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if len(first) != 1 || first[0].ID == "" || !first[0].IncludeInSubscription {
		t.Fatalf("legacy defaults not migrated: %+v", first)
	}
	persisted, err := os.ReadFile(filepath.Join(entry, "id"))
	if err != nil {
		t.Fatalf("migrated ID was not persisted: %v", err)
	}
	if strings.TrimSpace(string(persisted)) != first[0].ID {
		t.Fatalf("persisted ID = %q, want %q", persisted, first[0].ID)
	}
	second, err := Load(layout)
	if err != nil || second[0].ID != first[0].ID {
		t.Fatalf("migrated ID is not stable: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestDuplicateNodeIdentityValidation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := Add(layout, Node{ID: "11111111111111111111111111111111", SSHHost: "Host.Example", Domain: "one.example.com", WGIP: "10.90.0.2", WGPublicKey: "peer-key"}); err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]Node{
		"id":     {ID: "11111111111111111111111111111111", SSHHost: "other.example", Domain: "two.example.com"},
		"host":   {ID: "22222222222222222222222222222222", SSHHost: "host.example", Domain: "two.example.com"},
		"domain": {ID: "33333333333333333333333333333333", SSHHost: "other.example", Domain: "ONE.EXAMPLE.COM."},
		"wg ip":  {ID: "44444444444444444444444444444444", SSHHost: "ip.example", Domain: "ip.example.com", WGIP: "10.90.0.2"},
		"wg key": {ID: "55555555555555555555555555555555", SSHHost: "key.example", Domain: "key.example.com", WGPublicKey: "peer-key"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Add(layout, candidate); err == nil {
				t.Fatalf("duplicate %s was accepted", name)
			}
		})
	}
}

func TestUsedIPsIncludesHubAndAllocation(t *testing.T) {
	list := []Node{{WGIP: "10.90.0.2"}, {WGIP: "10.90.0.3"}}
	used := UsedIPs(list)
	next, err := wgnet.AllocateSpokeIP(wgnet.DefaultSubnet, used)
	if err != nil {
		t.Fatalf("AllocateSpokeIP: %v", err)
	}
	if next != "10.90.0.4" {
		t.Fatalf("next IP = %q, want 10.90.0.4", next)
	}
}

func TestHubIdentityLifecycle(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if _, ok, _ := LoadHubIdentity(layout); ok {
		t.Fatalf("expected no identity initially")
	}
	id, err := EnsureHubIdentity(layout, "hub.example.com")
	if err != nil {
		t.Fatalf("EnsureHubIdentity: %v", err)
	}
	if !wgnet.ValidKey(id.PrivateKey) || !wgnet.ValidKey(id.PublicKey) {
		t.Fatalf("invalid keys: %+v", id)
	}
	if id.Endpoint() != "hub.example.com:51820" {
		t.Fatalf("endpoint = %q", id.Endpoint())
	}
	// Stable across reloads; endpoint update is honored.
	id2, err := EnsureHubIdentity(layout, "new.example.com")
	if err != nil {
		t.Fatalf("EnsureHubIdentity 2: %v", err)
	}
	if id2.PrivateKey != id.PrivateKey {
		t.Fatalf("key changed across reload")
	}
	if id2.EndpointHost != "new.example.com" {
		t.Fatalf("endpoint not updated: %q", id2.EndpointHost)
	}

	if HubInstalled(layout) {
		t.Fatalf("hub should not be installed yet")
	}
	if err := SetHubInstalled(layout, true); err != nil {
		t.Fatalf("SetHubInstalled: %v", err)
	}
	if !HubInstalled(layout) {
		t.Fatalf("hub install flag not set")
	}
}

func TestHubEndpointJoinsIPv4AndIPv6(t *testing.T) {
	for _, tc := range []struct {
		host string
		want string
	}{
		{host: "203.0.113.10", want: "203.0.113.10:51820"},
		{host: "2001:db8::10", want: "[2001:db8::10]:51820"},
		{host: "[2001:db8::10]", want: "[2001:db8::10]:51820"},
	} {
		identity := HubIdentity{EndpointHost: tc.host, ListenPort: 51820}
		if got := identity.Endpoint(); got != tc.want {
			t.Errorf("Endpoint(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestLoadHubIdentityRecoversMissingPublicKeyFromPrivate(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	keyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.NewStore(layout.StateDir).WriteString(hubPrivateKeyFile, keyPair.PrivateKey+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	identity, ok, err := LoadHubIdentity(layout)
	if err != nil || !ok {
		t.Fatalf("LoadHubIdentity: ok=%v err=%v", ok, err)
	}
	if identity.PublicKey != keyPair.PublicKey || identity.Subnet != wgnet.DefaultSubnet || identity.ListenPort != wgnet.DefaultListenPort {
		t.Fatalf("recovered identity = %+v", identity)
	}
}
