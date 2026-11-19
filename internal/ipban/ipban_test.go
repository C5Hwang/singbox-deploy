package ipban_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestCanonicalAddress(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":             "203.0.113.7",
		" 203.0.113.7 ":           "203.0.113.7",
		"203.0.113.7/32":          "203.0.113.7",
		"203.0.113.77/24":         "203.0.113.0/24",
		"::ffff:203.0.113.7":      "203.0.113.7",
		"2001:DB8::1":             "2001:db8::1",
		"2001:db8::1/48":          "2001:db8::/48",
		"::ffff:198.51.100.0/120": "198.51.100.0/24",
	}
	for in, want := range cases {
		got, err := ipban.CanonicalAddress(in)
		if err != nil || got != want {
			t.Errorf("CanonicalAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nope", "203.0.113.256", "0.0.0.0/0", "::/0", "203.0.113.0/33", "fe80::1%eth0"} {
		if got, err := ipban.CanonicalAddress(bad); err == nil {
			t.Errorf("CanonicalAddress(%q) = %q, want an error", bad, got)
		}
	}
}

func TestRulesetDropsEachFamilyOnEachTransport(t *testing.T) {
	cfg := ipban.Config{Rules: []ipban.Rule{
		{
			Label:     "own protocols",
			Addresses: []string{"203.0.113.7", "203.0.113.0/24", "2001:db8::1", "198.51.100.9"},
			TCP:       []int{443, 8443, 443},
			UDP:       []int{9443},
		},
		{Label: "relay to tokyo", Addresses: []string{"192.0.2.1"}, TCP: []int{34567}},
		{Label: "nothing to drop", Addresses: nil, TCP: []int{1}},
	}}
	got := ipban.Ruleset(cfg)
	for _, want := range []string{
		"table inet singbox_deploy_ipban {}\ndelete table inet singbox_deploy_ipban\n",
		"type filter hook prerouting priority raw; policy accept;",
		// The /24 swallows the address inside it: nftables refuses an
		// anonymous interval set whose elements overlap.
		"ip saddr { 198.51.100.9, 203.0.113.0/24 } tcp dport { 443, 8443 } drop",
		"ip saddr { 198.51.100.9, 203.0.113.0/24 } udp dport { 9443 } drop",
		"ip6 saddr { 2001:db8::1 } tcp dport { 443, 8443 } drop",
		"ip6 saddr { 2001:db8::1 } udp dport { 9443 } drop",
		"# relay to tokyo\n\t\tip saddr { 192.0.2.1 } tcp dport { 34567 } drop",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "203.0.113.7") {
		t.Errorf("an address covered by a banned prefix should be collapsed away:\n%s", got)
	}
	if strings.Contains(got, "nothing to drop") || strings.Contains(got, "ip6 saddr { 192.0.2.1") {
		t.Errorf("rules with nothing to match should render nothing:\n%s", got)
	}
}

func TestEmptyConfig(t *testing.T) {
	if !(ipban.Config{}).Empty() {
		t.Fatal("zero config should be empty")
	}
	if !(ipban.Config{Rules: []ipban.Rule{{Addresses: []string{"192.0.2.1"}}}}).Empty() {
		t.Fatal("a rule without ports drops nothing")
	}
	if (ipban.Config{Rules: []ipban.Rule{{Addresses: []string{"192.0.2.1"}, UDP: []int{1}}}}).Empty() {
		t.Fatal("an address on a port is a ban")
	}
}

func TestRegistryAddEditRemoveAndDropNode(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := ipban.AddBan(layout, ipban.Ban{Address: "203.0.113.7/32", Nodes: []string{"HUB", "aa11", "aa11"}}); err != nil {
		t.Fatalf("AddBan: %v", err)
	}
	if err := ipban.AddBan(layout, ipban.Ban{Address: "203.0.113.7", Nodes: []string{"bb22"}}); err == nil {
		t.Fatal("adding the same address twice should be refused")
	}
	if err := ipban.AddBan(layout, ipban.Ban{Address: "198.51.100.1"}); err == nil {
		t.Fatal("a ban naming no node should be refused")
	}
	if err := ipban.AddBan(layout, ipban.Ban{Address: "2001:db8::/32", Nodes: []string{"aa11"}}); err != nil {
		t.Fatalf("AddBan v6: %v", err)
	}
	bans, err := ipban.LoadBans(layout)
	if err != nil || len(bans) != 2 {
		t.Fatalf("LoadBans = %#v, %v", bans, err)
	}
	if bans[0].Address != "203.0.113.7" || !bans[0].Covers("hub") || !bans[0].Covers("AA11") || len(bans[0].Nodes) != 2 {
		t.Fatalf("first ban = %#v", bans[0])
	}

	if err := ipban.SetBan(layout, ipban.Ban{Address: "203.0.113.7", Nodes: []string{"bb22"}}); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	bans, _ = ipban.LoadBans(layout)
	if got := ipban.AddressesFor(bans, "bb22"); len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("AddressesFor(bb22) = %v", got)
	}
	if got := ipban.AddressesFor(bans, "hub"); len(got) != 0 {
		t.Fatalf("an edit replaces the node list, but hub still has %v", got)
	}

	// Leaving the fleet drops the node, and a ban left naming nobody goes.
	if err := ipban.DropNode(layout, "aa11"); err != nil {
		t.Fatalf("DropNode: %v", err)
	}
	bans, _ = ipban.LoadBans(layout)
	if len(bans) != 1 || bans[0].Address != "203.0.113.7" {
		t.Fatalf("after DropNode = %#v", bans)
	}

	if err := ipban.RemoveBan(layout, "203.0.113.7/32"); err != nil {
		t.Fatalf("RemoveBan: %v", err)
	}
	if bans, _ = ipban.LoadBans(layout); len(bans) != 0 {
		t.Fatalf("after RemoveBan = %#v", bans)
	}
}

type recorder struct{ cmds []string }

func (r *recorder) Run(c system.Command) error {
	r.cmds = append(r.cmds, c.String())
	return nil
}

func TestApplierInstallsPersistsAndClears(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	systemdDir := t.TempDir()
	run := &recorder{}
	var loaded []string
	applier := &ipban.Applier{
		Layout:     layout,
		Bin:        "/usr/bin/singbox-deploy-agent",
		SystemdDir: systemdDir,
		Runner:     run,
		NFT: func(_ context.Context, stdin string, _ ...string) ([]byte, error) {
			loaded = append(loaded, stdin)
			return nil, nil
		},
	}
	cfg := ipban.Config{Rules: []ipban.Rule{{Label: "own protocols", Addresses: []string{"203.0.113.7"}, TCP: []int{443}}}}
	if err := applier.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(loaded) != 1 || !strings.Contains(loaded[0], "ip saddr { 203.0.113.7 } tcp dport { 443 } drop") {
		t.Fatalf("loaded = %q", loaded)
	}
	unit, err := os.ReadFile(filepath.Join(systemdDir, system.IPBanService))
	if err != nil || !strings.Contains(string(unit), "ExecStart=/usr/bin/singbox-deploy-agent ipban apply") {
		t.Fatalf("unit = %q, %v", unit, err)
	}
	if stored, err := ipban.Load(layout); err != nil || stored.Empty() {
		t.Fatalf("stored = %#v, %v", stored, err)
	}

	// The boot-time path reinstalls the stored rules without touching systemd.
	run.cmds = nil
	if err := applier.Reapply(context.Background()); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if len(loaded) != 2 || len(run.cmds) != 0 {
		t.Fatalf("Reapply loaded %d rulesets and ran %v", len(loaded), run.cmds)
	}

	// An empty configuration lifts everything.
	if err := applier.Apply(context.Background(), ipban.Config{}); err != nil {
		t.Fatalf("Apply empty: %v", err)
	}
	if last := loaded[len(loaded)-1]; !strings.Contains(last, "delete table inet singbox_deploy_ipban") || strings.Contains(last, "drop") {
		t.Fatalf("clear loaded %q", last)
	}
	if _, err := os.Stat(filepath.Join(systemdDir, system.IPBanService)); !os.IsNotExist(err) {
		t.Fatalf("unit should be removed, stat err = %v", err)
	}
	if stored, _ := ipban.Load(layout); !stored.Empty() {
		t.Fatalf("stored config should be gone: %#v", stored)
	}
}
