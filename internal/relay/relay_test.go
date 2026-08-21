package relay_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// recorder captures the commands an Applier issues instead of running them.
type recorder struct {
	cmds []system.Command
	fail map[string]error
}

func (r *recorder) Run(c system.Command) error {
	r.cmds = append(r.cmds, c)
	return r.fail[c.String()]
}

func (r *recorder) lines() []string {
	out := make([]string, 0, len(r.cmds))
	for _, c := range r.cmds {
		out = append(out, c.String())
	}
	return out
}

func (r *recorder) ran(substr string) bool {
	for _, line := range r.lines() {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// nftLog records the rulesets loaded through nft.
type nftLog struct {
	stdin []string
	err   error
}

func (n *nftLog) run(_ context.Context, stdin string, _ ...string) ([]byte, error) {
	n.stdin = append(n.stdin, stdin)
	return nil, n.err
}

func testApplier(t *testing.T, run *recorder, nft *nftLog) *relay.Applier {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	return &relay.Applier{
		Layout:     layout,
		Bin:        "/usr/bin/singbox-deploy",
		SystemdDir: t.TempDir(),
		Firewall:   system.FirewallUFW,
		Runner:     run,
		NFT:        nft.run,
		Resolve: func(_ context.Context, host string) (string, error) {
			switch host {
			case "land.example.com":
				return "203.0.113.9", nil
			case "second.example.com":
				return "198.51.100.4", nil
			default:
				return "", errors.New("no such host")
			}
		},
	}
}

func sampleConfig() relay.Config {
	return relay.Config{Landings: []relay.Landing{{
		NodeID:  "aa11",
		Name:    "HK",
		Host:    "land.example.com",
		Address: "203.0.113.9",
		Forwards: []relay.Forward{
			{Protocol: "hysteria2", Network: "udp", ListenPort: 34568, TargetPort: 41235},
			{Protocol: "anytls", Network: "tcp", ListenPort: 34567, TargetPort: 41234},
		},
	}}}
}

func TestRulesetDNATsAndMasqueradesEachForward(t *testing.T) {
	got := relay.Ruleset([]relay.ResolvedLanding{{Landing: sampleConfig().Landings[0], IP: "203.0.113.9"}})
	for _, want := range []string{
		"table ip " + relay.Table + " {}\ndelete table ip " + relay.Table,
		"type nat hook prerouting priority dstnat",
		"tcp dport 34567 dnat to 203.0.113.9:41234",
		"udp dport 34568 dnat to 203.0.113.9:41235",
		"type nat hook postrouting priority srcnat",
		"ip daddr 203.0.113.9 tcp dport 41234 masquerade",
		"ip daddr 203.0.113.9 udp dport 41235 masquerade",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ruleset is missing %q:\n%s", want, got)
		}
	}
	// Ordered by listen port so a reapply produces a byte-identical program.
	if strings.Index(got, "dport 34567") > strings.Index(got, "dport 34568") {
		t.Fatalf("forwards should be rendered in listen-port order:\n%s", got)
	}
}

func TestApplyPersistsInstallsAndEnablesTheBootUnit(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	if err := a.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "dnat to 203.0.113.9:41234") {
		t.Fatalf("nft rulesets = %#v", nft.stdin)
	}
	stored, err := relay.Load(a.Layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored.Landings) != 1 || stored.Landings[0].NodeID != "aa11" {
		t.Fatalf("stored configuration = %#v", stored)
	}
	unit, err := os.ReadFile(filepath.Join(a.SystemdDir, system.RelayService))
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if !strings.Contains(string(unit), "/usr/bin/singbox-deploy relay apply") {
		t.Fatalf("unit should reapply with the node's own binary:\n%s", unit)
	}
	for _, want := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"ufw allow 34567/tcp",
		"ufw route allow proto tcp from any to 203.0.113.9 port 41234",
		"ufw route allow proto udp from any to 203.0.113.9 port 41235",
		"systemctl enable " + system.RelayService,
	} {
		if !run.ran(want) {
			t.Fatalf("missing command %q in:\n%s", want, strings.Join(run.lines(), "\n"))
		}
	}
}

// A DNATed packet is routed, never delivered locally, so the forward rules are
// what actually admit it. Losing them silently breaks every relayed node.
func TestApplyFailsWhenAFirewallRuleCannotBeInstalled(t *testing.T) {
	run := &recorder{fail: map[string]error{
		"ufw route allow proto tcp from any to 203.0.113.9 port 41234": errors.New("ufw refused"),
	}}
	a := testApplier(t, run, &nftLog{})
	err := a.Apply(context.Background(), sampleConfig())
	if err == nil || !strings.Contains(err.Error(), "ufw refused") {
		t.Fatalf("Apply = %v, want the firewall failure", err)
	}
}

func TestApplyWithdrawsTheRulesOfThePreviousConfiguration(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	if err := a.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	run.cmds = nil

	next := relay.Config{Landings: []relay.Landing{{
		NodeID: "bb22", Name: "SG", Host: "second.example.com", Address: "198.51.100.4",
		Forwards: []relay.Forward{{Protocol: "anytls", Network: "tcp", ListenPort: 39000, TargetPort: 42000}},
	}}}
	if err := a.Apply(context.Background(), next); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	for _, want := range []string{
		"ufw delete allow 34567/tcp",
		"ufw route delete allow proto tcp from any to 203.0.113.9 port 41234",
		"ufw route allow proto tcp from any to 198.51.100.4 port 42000",
	} {
		if !run.ran(want) {
			t.Fatalf("missing command %q in:\n%s", want, strings.Join(run.lines(), "\n"))
		}
	}
	stored, err := relay.Load(a.Layout)
	if err != nil || len(stored.Landings) != 1 || stored.Landings[0].NodeID != "bb22" {
		t.Fatalf("stored configuration = %#v (%v)", stored, err)
	}
}

// A landing node whose name no longer resolves falls back to the address the
// hub recorded, so a resolver outage at boot does not take forwarding down.
func TestApplyFallsBackToTheRecordedAddress(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	cfg := sampleConfig()
	cfg.Landings[0].Host = "gone.example.com"
	if err := a.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "dnat to 203.0.113.9:41234") {
		t.Fatalf("ruleset should use the recorded address:\n%#v", nft.stdin)
	}
}

func TestApplyReportsALandingItCouldNotAddressAtAll(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	cfg := sampleConfig()
	cfg.Landings[0].Host = "gone.example.com"
	cfg.Landings[0].Address = ""
	err := a.Apply(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "no relay landing node could be resolved") {
		t.Fatalf("Apply = %v", err)
	}
	if len(nft.stdin) != 0 {
		t.Fatalf("nothing should be loaded when no landing node resolved: %#v", nft.stdin)
	}
}

// One unreachable landing node must not withdraw the mappings of the others.
func TestApplyKeepsTheLandingsItCouldResolve(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	cfg := sampleConfig()
	cfg.Landings = append(cfg.Landings, relay.Landing{
		NodeID: "bb22", Name: "SG", Host: "gone.example.com",
		Forwards: []relay.Forward{{Protocol: "anytls", Network: "tcp", ListenPort: 39000, TargetPort: 42000}},
	})
	err := a.Apply(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "gone.example.com") {
		t.Fatalf("Apply should report the landing it lost: %v", err)
	}
	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "dnat to 203.0.113.9:41234") {
		t.Fatalf("the resolvable landing should still be forwarded:\n%#v", nft.stdin)
	}
	if strings.Contains(nft.stdin[0], "39000") {
		t.Fatalf("the unresolvable landing must not be rendered:\n%s", nft.stdin[0])
	}
}

func TestApplyWithNothingToForwardWithdrawsEverything(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	if err := a.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	run.cmds, nft.stdin = nil, nil

	if err := a.Apply(context.Background(), relay.Config{}); err != nil {
		t.Fatalf("empty Apply: %v", err)
	}
	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "delete table ip "+relay.Table) {
		t.Fatalf("the table should be deleted: %#v", nft.stdin)
	}
	if strings.Contains(nft.stdin[0], "dnat") {
		t.Fatalf("no rule should survive: %s", nft.stdin[0])
	}
	if !run.ran("systemctl disable --now " + system.RelayService) {
		t.Fatalf("the boot unit should be disabled:\n%s", strings.Join(run.lines(), "\n"))
	}
	if _, err := os.Stat(filepath.Join(a.SystemdDir, system.RelayService)); !os.IsNotExist(err) {
		t.Fatalf("the unit file should be gone: %v", err)
	}
	stored, err := relay.Load(a.Layout)
	if err != nil || !stored.Empty() {
		t.Fatalf("stored configuration = %#v (%v)", stored, err)
	}
}

// Reapply is what the boot-time unit runs, so it must install the rules
// without trying to manage the unit that invoked it.
func TestReapplyRestoresTheStoredRulesWithoutTouchingSystemd(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	if err := a.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	run.cmds, nft.stdin = nil, nil

	if err := a.Reapply(context.Background()); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "dnat to 203.0.113.9:41234") {
		t.Fatalf("Reapply should reinstall the stored rules: %#v", nft.stdin)
	}
	if run.ran("systemctl") {
		t.Fatalf("Reapply must not manage its own unit:\n%s", strings.Join(run.lines(), "\n"))
	}
}

func TestReapplyOnANodeThatForwardsNothingClearsTheTable(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	a := testApplier(t, run, nft)
	if err := a.Reapply(context.Background()); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if len(nft.stdin) != 1 || strings.Contains(nft.stdin[0], "dnat") {
		t.Fatalf("nft rulesets = %#v", nft.stdin)
	}
}

// fakeSingBox records the service half of quota enforcement.
type fakeSingBox struct {
	started, stopped int
	active           bool
}

func (f *fakeSingBox) Start() error            { f.started++; f.active = true; return nil }
func (f *fakeSingBox) Stop() error             { f.stopped++; f.active = false; return nil }
func (f *fakeSingBox) IsActive() (bool, error) { return f.active, nil }

// A relay that ran out of traffic must stop forwarding as well as stop serving:
// the rules are in the kernel and would otherwise keep spending its allowance
// on other nodes' clients.
func TestQuotaControllerWithdrawsAndRestoresForwarding(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	applier := testApplier(t, run, nft)
	if err := applier.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	nft.stdin = nil

	singBox := &fakeSingBox{active: true}
	controller := relay.QuotaController{SingBox: singBox, Applier: applier, Logf: func(string, ...any) {}}

	if err := controller.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if singBox.stopped != 1 {
		t.Fatalf("sing-box stops = %d", singBox.stopped)
	}
	if len(nft.stdin) != 1 || strings.Contains(nft.stdin[0], "dnat") {
		t.Fatalf("the forwarding rules should be withdrawn: %#v", nft.stdin)
	}
	if stored, err := relay.Load(applier.Layout); err != nil || stored.Empty() {
		t.Fatalf("a suspension must keep the stored job: %#v (%v)", stored, err)
	}
	if _, err := os.Stat(filepath.Join(applier.SystemdDir, system.RelayService)); err != nil {
		t.Fatalf("a suspension must keep the boot unit: %v", err)
	}

	nft.stdin = nil
	if err := controller.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if singBox.started != 1 {
		t.Fatalf("sing-box starts = %d", singBox.started)
	}
	if len(nft.stdin) != 1 || !strings.Contains(nft.stdin[0], "dnat to 203.0.113.9:41234") {
		t.Fatalf("the forwarding rules should be restored: %#v", nft.stdin)
	}
}

// Quota enforcement runs on every node, so an ordinary one must not have its
// nftables touched at all.
func TestQuotaControllerLeavesANodeThatForwardsNothingAlone(t *testing.T) {
	run, nft := &recorder{}, &nftLog{}
	singBox := &fakeSingBox{active: true}
	controller := relay.QuotaController{
		SingBox: singBox,
		Applier: testApplier(t, run, nft),
		Logf:    func(string, ...any) {},
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(nft.stdin) != 0 {
		t.Fatalf("nft should not run on a node that forwards nothing: %#v", nft.stdin)
	}
	if singBox.stopped != 1 || singBox.started != 1 {
		t.Fatalf("sing-box should still be governed: %+v", singBox)
	}
}

// Forwarding that cannot be withdrawn must never keep sing-box running: the
// hub republishes the landing node's own address either way.
func TestQuotaControllerStopsSingBoxEvenIfTheRulesetFails(t *testing.T) {
	run, nft := &recorder{}, &nftLog{err: errors.New("nft refused")}
	applier := testApplier(t, run, nft)
	applier.NFT = (&nftLog{}).run
	if err := applier.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	applier.NFT = nft.run

	singBox := &fakeSingBox{active: true}
	var logged []string
	controller := relay.QuotaController{
		SingBox: singBox,
		Applier: applier,
		Logf:    func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if singBox.stopped != 1 {
		t.Fatal("sing-box must still be stopped")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "nft refused") {
		t.Fatalf("the failure should be reported: %#v", logged)
	}
}

func TestValidateRejectsTwoLandingsOnOneListenPort(t *testing.T) {
	cfg := relay.Config{Landings: []relay.Landing{
		{NodeID: "aa11", Name: "HK", Host: "a.example.com", Forwards: []relay.Forward{
			{Protocol: "anytls", Network: "tcp", ListenPort: 34567, TargetPort: 41234},
		}},
		{NodeID: "bb22", Name: "SG", Host: "b.example.com", Forwards: []relay.Forward{
			{Protocol: "anytls", Network: "tcp", ListenPort: 34567, TargetPort: 42234},
		}},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "claimed by both HK and SG") {
		t.Fatalf("Validate = %v", err)
	}
	// The same number on the other transport is a different socket.
	cfg.Landings[1].Forwards[0].Network = "udp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("tcp and udp on one port number: %v", err)
	}
}

func TestValidateRejectsAnUnknownTransport(t *testing.T) {
	cfg := relay.Config{Landings: []relay.Landing{{
		NodeID: "aa11", Name: "HK", Host: "a.example.com",
		Forwards: []relay.Forward{{Protocol: "anytls", Network: "sctp", ListenPort: 34567, TargetPort: 41234}},
	}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "neither tcp nor udp") {
		t.Fatalf("Validate = %v", err)
	}
}

func TestListenPortsCoverEveryForward(t *testing.T) {
	ports := sampleConfig().ListenPorts()
	if len(ports) != 2 {
		t.Fatalf("ListenPorts = %#v", ports)
	}
	seen := map[int]string{}
	for _, p := range ports {
		seen[p.Number] = p.Proto
	}
	if seen[34567] != "tcp" || seen[34568] != "udp" {
		t.Fatalf("ListenPorts = %#v", ports)
	}
}

func TestLoadOnANodeThatNeverRelayedIsEmpty(t *testing.T) {
	cfg, err := relay.Load(paths.LayoutForRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Empty() {
		t.Fatalf("Load = %#v", cfg)
	}
}

// MonitorForwards reads the stored job on every call, so the monitor's forward
// counters follow a pushed or withdrawn link without a restart. Every mapping
// carries the landing node it fronts, which is what lets a forwarded byte be
// attributed to a destination rather than only to the relay.
func TestMonitorForwardsFollowTheStoredJob(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	forwards := relay.MonitorForwards(layout)
	if got := forwards(); got != nil {
		t.Fatalf("forwards = %v on a node that never relayed, want none", got)
	}
	cfg := relay.Config{Landings: []relay.Landing{
		{NodeID: "bb22", Name: "SG", Host: "b.example.com", Forwards: []relay.Forward{
			{Protocol: "anytls", Network: "tcp", ListenPort: 34570, TargetPort: 41240},
		}},
		{NodeID: "aa11", Name: "HK", Host: "a.example.com", Forwards: []relay.Forward{
			{Protocol: "anytls", Network: "tcp", ListenPort: 34568, TargetPort: 41234},
			// One port carrying both transports is one port on two mappings; the
			// ruleset dedupes them and the landing behind them is the same.
			{Protocol: "hysteria2", Network: "udp", ListenPort: 34567, TargetPort: 41235},
			{Protocol: "tuic", Network: "tcp", ListenPort: 34567, TargetPort: 41236},
		}},
	}}
	if err := relay.Save(layout, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := forwards()
	if len(got) != 4 {
		t.Fatalf("forwards = %v, want one per mapping", got)
	}
	for i, want := range []monitor.RelayForward{
		{ListenPort: 34567, LandingID: "aa11", LandingName: "HK"},
		{ListenPort: 34567, LandingID: "aa11", LandingName: "HK"},
		{ListenPort: 34568, LandingID: "aa11", LandingName: "HK"},
		{ListenPort: 34570, LandingID: "bb22", LandingName: "SG"},
	} {
		if got[i] != want {
			t.Fatalf("forwards[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	if err := relay.Save(layout, relay.Config{}); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	if got := forwards(); got != nil {
		t.Fatalf("forwards = %v after the job was withdrawn, want none", got)
	}
}
