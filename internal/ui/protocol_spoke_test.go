package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
)

func TestProtocolManagementEditsSpokeByStableID(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	list := []nodes.Node{
		{
			ID: "11111111111111111111111111111111", Alias: "London UI", SubscriptionAlias: "United Kingdom",
			Domain: "uk.example.com", WGIP: "10.90.0.2", Installed: true,
			IncludeInSubscription: true,
			EnabledProtocols:      []string{"vless-reality-vision", "hysteria2"},
			RealityServerName:     "www.cloudflare.com",
			RealityVisionPort:     8443,
			RealityGRPCPort:       8444,
			Hysteria2Port:         9443,
			TUICPort:              10443,
			AnyTLSPort:            11443,
			Monitor:               true,
			MonitorPort:           deploy.DefaultMonitorPort,
		},
		{
			ID: "22222222222222222222222222222222", Alias: "Tokyo UI",
			Domain: "jp.example.com", WGIP: "10.90.0.3", Installed: true,
			EnabledProtocols: []string{"tuic"}, TUICPort: 20443,
		},
	}
	if err := nodes.Save(layout, list); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	withProtocolManagerDeps(t, layout)

	pm := newProtocolManager()
	pm.setSize(110, 32)
	if pm.loadErr != nil {
		t.Fatalf("load protocol manager: %v", pm.loadErr)
	}
	actionView := pm.View()
	for _, want := range []string{"Hub", "Spokes (WireGuard)", "Spoke · Edit protocols / SNI / ports", "Registered spokes: 2"} {
		if !strings.Contains(actionView, want) {
			t.Fatalf("protocol action page missing %q:\n%s", want, actionView)
		}
	}

	pm.cursor = pm.actionCursor(protocolActionEditSpoke)
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm || pm.editNodeID != "" {
		t.Fatalf("open spoke selector: done=%v phase=%v id=%q", done, pm.phase, pm.editNodeID)
	}
	selector := pm.View()
	for _, want := range []string{"Choose Spoke", "Spoke protocol settings to edit", "London UI", "10.90.0.2", "11111111", "stable node ID"} {
		if !strings.Contains(selector, want) {
			t.Fatalf("spoke selector missing %q:\n%s", want, selector)
		}
	}

	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm || pm.editNodeID != list[0].ID {
		t.Fatalf("select spoke: done=%v phase=%v id=%q", done, pm.phase, pm.editNodeID)
	}
	if got := pm.values["protocols"]; got != "vless-reality-vision,hysteria2" {
		t.Fatalf("seeded protocols = %q", got)
	}
	keys := map[string]bool{}
	for _, f := range pm.fields {
		keys[f.key] = true
		if strings.Contains(f.key, "uuid") || strings.Contains(f.key, "password") {
			t.Fatalf("spoke form must not expose credential field %q", f.key)
		}
	}
	for _, want := range []string{
		"protocols", "reality_sni", "reality_vision_port", "reality_grpc_port",
		"hysteria2_port", "tuic_port", "anytls_port",
	} {
		if !keys[want] {
			t.Errorf("spoke protocol form missing %q", want)
		}
	}
	if !strings.Contains(pm.fields[0].note, "credentials are preserved") {
		t.Fatalf("credential-preservation note missing: %q", pm.fields[0].note)
	}

	// Reordering or renaming the in-memory registry after selection must not
	// retarget the form: editNodeID, rather than alias or slice position, owns
	// the selection.
	list[0].Alias = "Renamed London"
	pm.nodes = []nodes.Node{list[1], list[0]}
	selected, ok := pm.editSpokeNode()
	if !ok || selected.ID != list[0].ID || selected.EffectiveAlias() != "Renamed London" {
		t.Fatalf("stable-ID selection resolved to %+v, ok=%v", selected, ok)
	}

	pm.phase = protocolPhaseConfirm
	confirm := pm.View()
	for _, want := range []string{list[0].ID, "Target", "Spoke", "Credentials", "preserve existing", "authenticated Agent over WireGuard"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("spoke confirmation missing %q:\n%s", want, confirm)
		}
	}
}

func TestSpokeProtocolValidationCoversSelectionAndActivePortConflicts(t *testing.T) {
	if err := validateSpokeProtocolField(field{key: "protocols"}, "", nil); err == nil ||
		!strings.Contains(err.Error(), "at least one") {
		t.Fatalf("empty protocol selection = %v", err)
	}
	values := map[string]string{
		"protocols":           "hysteria2,anytls",
		"hysteria2_port":      "9443",
		"anytls_port":         "9443",
		"monitor":             "yes",
		"monitor_port":        "19090",
		"reality_vision_port": "8443",
	}
	if err := validateSpokeProtocolField(field{key: "anytls_port"}, "9443", values); err == nil ||
		!strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate active protocol port = %v", err)
	}
	values["protocols"] = "hysteria2"
	if err := validateSpokeProtocolField(field{key: "hysteria2_port"}, "9443", values); err != nil {
		t.Fatalf("inactive protocol port caused a false conflict: %v", err)
	}
	if err := validateSpokeProtocolField(field{key: "hysteria2_port"}, "443", values); err == nil ||
		!strings.Contains(err.Error(), "reserved by Nginx") {
		t.Fatalf("Nginx port conflict = %v", err)
	}
	if err := validateSpokeProtocolField(field{key: "hysteria2_port"}, "19090", values); err == nil ||
		!strings.Contains(err.Error(), "monitor service") {
		t.Fatalf("monitor port conflict = %v", err)
	}
}

func TestSpokeProtocolRunForwardsProgressEvents(t *testing.T) {
	original := applySpokeProtocolRun
	t.Cleanup(func() { applySpokeProtocolRun = original })
	applySpokeProtocolRun = func(_ *protocolManager, _ context.Context, _ *logWriter, progress func(deploy.Event)) error {
		deploy.EmitProgress(progress, deploy.Event{
			Index: 1, Total: 5, Label: "Registry settings", Status: "running",
		})
		return nil
	}
	pm := &protocolManager{
		phase:      protocolPhaseConfirm,
		action:     protocolActionEditSpoke,
		host:       supportedTestHost(),
		commandRun: newCommandRun(),
	}
	wait := pm.startRun()
	if wait == nil {
		t.Fatal("spoke protocol run did not start")
	}
	msg, ok := wait().(runMsg)
	if !ok || msg.event == nil || msg.event.Label != "Registry settings" {
		t.Fatalf("first run message = %#v, want forwarded progress event", msg)
	}
}

func TestSpokeProtocolTransactionPersistsSuccessfulApply(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	original := nodes.Node{
		ID: "cccccccccccccccccccccccccccccccc", Alias: "London", Domain: "uk.example.com",
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		RealityVisionPort: 8443, RealityGRPCPort: 8444, TUICPort: 10443, AnyTLSPort: 11443,
		MonitorAlias: "keep me",
	}
	if err := nodes.Save(layout, []nodes.Node{original}); err != nil {
		t.Fatalf("save original node: %v", err)
	}
	change, err := spokeProtocolRegistryChange(map[string]string{
		"protocols":           "vless-reality-grpc,anytls",
		"reality_sni":         "https://www.cloudflare.com/cdn-cgi/trace",
		"reality_vision_port": "18443",
		"reality_grpc_port":   "18444",
		"hysteria2_port":      "19443",
		"tuic_port":           "20443",
		"anytls_port":         "21443",
	})
	if err != nil {
		t.Fatalf("build protocol change: %v", err)
	}
	var remote nodes.Node
	err = applySpokeRegistryReconfigure(
		context.Background(), layout, original.ID, io.Discard, nil, change,
		func(_ context.Context, node nodes.Node, _ io.Writer) error {
			remote = cloneSpokeNode(node)
			return nil
		},
		func(context.Context, nodes.Node, io.Writer) error {
			t.Fatal("rollback should not run after a successful Agent apply")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("apply successful transaction: %v", err)
	}
	if strings.Join(remote.EnabledProtocols, ",") != "vless-reality-grpc,anytls" ||
		remote.RealityServerName != "www.cloudflare.com" || remote.RealityGRPCPort != 18444 ||
		remote.AnyTLSPort != 21443 {
		t.Fatalf("remote target = %+v", remote)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load registry: list=%+v err=%v", list, loadErr)
	}
	got := list[0]
	if strings.Join(got.EnabledProtocols, ",") != "vless-reality-grpc,anytls" ||
		got.RealityServerName != "www.cloudflare.com" || got.AnyTLSPort != 21443 {
		t.Fatalf("persisted protocol settings = %+v", got)
	}
	if got.MonitorAlias != original.MonitorAlias {
		t.Fatalf("unrelated field changed: monitor alias %q", got.MonitorAlias)
	}
}

func TestSpokeProtocolTransactionRollsBackRegistryAndRemote(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	original := nodes.Node{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Alias: "London", Domain: "uk.example.com",
		Installed:             true,
		AgentVersion:          "old-agent",
		IncludeInSubscription: true,
		EnabledProtocols:      []string{"vless-reality-vision"},
		RealityServerName:     "www.microsoft.com",
		RealityVisionPort:     8443,
		RealityGRPCPort:       8444,
		Hysteria2Port:         9443,
		TUICPort:              10443,
		AnyTLSPort:            11443,
		MonitorAlias:          "London monitor",
	}
	if err := nodes.Save(layout, []nodes.Node{original}); err != nil {
		t.Fatalf("save original node: %v", err)
	}
	change, err := spokeProtocolRegistryChange(map[string]string{
		"protocols":           "hysteria2,tuic",
		"reality_sni":         "https://www.cloudflare.com/",
		"reality_vision_port": "18443",
		"reality_grpc_port":   "18444",
		"hysteria2_port":      "19443",
		"tuic_port":           "20443",
		"anytls_port":         "21443",
	})
	if err != nil {
		t.Fatalf("build protocol change: %v", err)
	}

	var applied nodes.Node
	applyErr := errors.New("agent rejected update")
	applyRemote := func(_ context.Context, node nodes.Node, _ io.Writer) error {
		applied = cloneSpokeNode(node)
		// Simulate an independent authenticated health update while the Agent
		// request is in flight. Protocol rollback must retain these fields.
		if err := nodes.Mutate(layout, original.ID, func(current *nodes.Node) error {
			current.AgentVersion = "new-agent"
			current.LastSeen = time.Unix(1234, 0).UTC()
			current.MonitorAlias = "updated concurrently"
			return nil
		}); err != nil {
			t.Fatalf("concurrent status update: %v", err)
		}
		return applyErr
	}
	var rolledBack nodes.Node
	rollbackRemote := func(_ context.Context, node nodes.Node, _ io.Writer) error {
		rolledBack = cloneSpokeNode(node)
		return nil
	}
	var events []deploy.Event
	err = applySpokeRegistryReconfigure(
		context.Background(), layout, original.ID, io.Discard,
		func(event deploy.Event) { events = append(events, event) },
		change, applyRemote, rollbackRemote,
	)
	if err == nil || !strings.Contains(err.Error(), applyErr.Error()) ||
		!strings.Contains(err.Error(), "previous settings restored") {
		t.Fatalf("transaction error = %v", err)
	}
	if strings.Join(applied.EnabledProtocols, ",") != "hysteria2,tuic" ||
		applied.Hysteria2Port != 19443 || applied.TUICPort != 20443 {
		t.Fatalf("remote apply did not receive target settings: %+v", applied)
	}
	if strings.Join(rolledBack.EnabledProtocols, ",") != "vless-reality-vision" ||
		rolledBack.RealityVisionPort != original.RealityVisionPort {
		t.Fatalf("remote rollback did not receive original settings: %+v", rolledBack)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load rolled-back registry: list=%+v err=%v", list, loadErr)
	}
	got := list[0]
	if strings.Join(got.EnabledProtocols, ",") != "vless-reality-vision" ||
		got.RealityVisionPort != original.RealityVisionPort ||
		got.Hysteria2Port != original.Hysteria2Port {
		t.Fatalf("protocol fields were not restored: %+v", got)
	}
	if got.AgentVersion != "new-agent" || got.MonitorAlias != "updated concurrently" ||
		!got.LastSeen.Equal(time.Unix(1234, 0).UTC()) {
		t.Fatalf("unrelated concurrent fields were overwritten: %+v", got)
	}
	if len(events) != 2 || events[0].Status != "running" || events[1].Status != "ok" {
		t.Fatalf("registry progress = %+v", events)
	}
}

func TestSubscriptionSpokeFormContainsOnlySubscriptionSettings(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	node := nodes.Node{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Alias: "London UI", SubscriptionAlias: "UK",
		Domain: "uk.example.com", WGIP: "10.90.0.2", Installed: true,
		IncludeInSubscription: true,
		EnabledProtocols:      []string{"hysteria2"},
		Hysteria2Port:         9443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatalf("save node: %v", err)
	}
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.editNodeIndex = 0
	sm.startEditSpokeForm()

	if len(sm.fields) != 2 || sm.fields[0].key != "spoke_alias" || sm.fields[1].key != "include_subscription" {
		t.Fatalf("subscription spoke fields = %+v", sm.fields)
	}
	for _, f := range sm.fields {
		if f.key == "protocols" || f.key == "reality_sni" || strings.HasSuffix(f.key, "_port") {
			t.Fatalf("Subscription settings retained protocol field %q", f.key)
		}
	}
	sm.phase = subscriptionPhaseConfirm
	view := sm.View()
	for _, forbidden := range []string{"Enabled protocols", "Reality Vision port", "Hysteria2 port", "AnyTLS port"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("subscription confirmation retained %q:\n%s", forbidden, view)
		}
	}
}
