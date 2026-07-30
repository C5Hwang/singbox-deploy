package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

func TestProtocolManagementOffersSymmetricHubAndSpokeActions(t *testing.T) {
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
	for _, want := range []string{
		"Hub", "Spokes", "Registered spokes: 2",
		"Hub · Enabled protocols",
		"Hub · Edit protocol settings",
		"Hub · Edit Reality SNI",
		"Spoke · Enabled protocols",
		"Spoke · Edit protocol settings",
		"Spoke · Edit Reality SNI",
	} {
		if !strings.Contains(actionView, want) {
			t.Fatalf("protocol action page missing %q:\n%s", want, actionView)
		}
	}
	if strings.Contains(actionView, "Edit protocols / SNI / ports") {
		t.Fatalf("spoke actions remain bundled:\n%s", actionView)
	}
}

func TestProtocolManagementChangesSpokeProtocolSetByStableID(t *testing.T) {
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
	pm.cursor = pm.actionCursor(protocolActionChangeSpoke)
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm || pm.editNodeID != "" {
		t.Fatalf("open spoke selector: done=%v phase=%v id=%q", done, pm.phase, pm.editNodeID)
	}
	selector := pm.View()
	for _, want := range []string{"Choose spoke", "Spoke to manage", "London UI", "10.90.0.2", "11111111", "reaches every spoke over WireGuard"} {
		if !strings.Contains(selector, want) {
			t.Fatalf("spoke selector missing %q:\n%s", want, selector)
		}
	}

	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseSelect || pm.editNodeID != list[0].ID {
		t.Fatalf("select spoke: done=%v phase=%v id=%q", done, pm.phase, pm.editNodeID)
	}
	if got := protocolSelectionValue(pm.targetProtocols()); got != "vless-reality-vision,hysteria2" {
		t.Fatalf("seeded target protocols = %q", got)
	}
	for _, want := range []string{"Spoke · Enabled protocols", "London UI", "Current:", "Target:"} {
		if !strings.Contains(pm.View(), want) {
			t.Fatalf("spoke protocol selection missing %q:\n%s", want, pm.View())
		}
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

	pm.selected[string(config.ProtocolTUIC)] = true
	pm.prepareChangeConfirm()
	if pm.phase != protocolPhaseForm {
		t.Fatalf("added protocol should request its port, phase=%v err=%q", pm.phase, pm.fieldErr)
	}
	if len(pm.fields) != 1 || pm.fields[0].key != "tuic_port" {
		t.Fatalf("spoke install fields = %+v, want only TUIC port", pm.fields)
	}
	// The note covers only the port. Credential preservation is stated once, in
	// the confirmation summary asserted further down.
	if pm.fields[0].note != uiparams.NotePortListen {
		t.Fatalf("port note = %q, want the shared listen-port note", pm.fields[0].note)
	}
	for _, f := range pm.fields {
		if f.key == "reality_sni" || strings.Contains(f.key, "uuid") || strings.Contains(f.key, "password") {
			t.Fatalf("spoke install/remove exposed unrelated field %q", f.key)
		}
	}

	pm.phase = protocolPhaseConfirm
	confirm := pm.View()
	for _, want := range []string{
		list[0].ID, "Target", "Spoke", "Enabled protocols",
		"Credentials", "preserve existing", "authenticated Agent over WireGuard",
	} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("spoke confirmation missing %q:\n%s", want, confirm)
		}
	}
	for _, forbidden := range []string{"Reality SNI", "Reality Vision port", "Hysteria2 port", "AnyTLS port"} {
		if strings.Contains(confirm, forbidden) {
			t.Fatalf("spoke install confirmation includes unrelated %q:\n%s", forbidden, confirm)
		}
	}
}

func TestProtocolManagementEditsInstalledSpokeProtocolCredentialAndPort(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Alias: "London", Domain: "uk.example.com",
		WGIP: "10.90.0.2", Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		RealityVisionPort: 8443, RealityGRPCPort: 8444, TUICPort: 10443, AnyTLSPort: 11443,
		MonitorPort: deploy.DefaultMonitorPort,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatalf("save node: %v", err)
	}
	withProtocolManagerDeps(t, layout)

	pm := newProtocolManager()
	pm.setSize(100, 30)
	pm.cursor = pm.actionCursor(protocolActionEditSpoke)
	pm.activateAction()
	if pm.phase != protocolPhaseForm || pm.editNodeID != "" {
		t.Fatalf("open spoke selector: phase=%v id=%q", pm.phase, pm.editNodeID)
	}
	_, _ = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pm.phase != protocolPhaseEditPick || pm.editNodeID != node.ID {
		t.Fatalf("select spoke: phase=%v id=%q", pm.phase, pm.editNodeID)
	}
	for _, want := range []string{"Spoke · Edit", "Choose a protocol to edit", "credentials and port", "hysteria2"} {
		if !strings.Contains(pm.View(), want) {
			t.Fatalf("spoke edit picker missing %q:\n%s", want, pm.View())
		}
	}
	cmd, _ := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pm.phase != protocolPhaseLoadingSpokeState || cmd == nil {
		t.Fatalf("spoke settings load did not start asynchronously: phase=%v cmd=%v", pm.phase, cmd != nil)
	}
	_, _ = pm.Update(cmd())
	if pm.phase != protocolPhaseForm || pm.editProto != config.ProtocolHysteria2 {
		t.Fatalf("open spoke port form: phase=%v proto=%q", pm.phase, pm.editProto)
	}
	if len(pm.fields) != 2 || pm.fields[0].key != "hysteria2_password" ||
		pm.fields[1].key != "hysteria2_port" {
		t.Fatalf("spoke edit fields = %+v, want Hysteria2 password and port", pm.fields)
	}
	if !pm.fields[0].secret {
		t.Fatal("Hysteria2 password field is not masked")
	}
	view := pm.View()
	if strings.Contains(view, pm.values["hysteria2_password"]) {
		t.Fatalf("spoke password leaked in form:\n%s", view)
	}
	pm.phase = protocolPhaseConfirm
	confirm := pm.View()
	if strings.Contains(confirm, pm.values["hysteria2_password"]) ||
		!strings.Contains(confirm, "•••••••• (set)") {
		t.Fatalf("spoke password was not masked in confirmation:\n%s", confirm)
	}
}

func TestSpokeProtocolEditFieldsMatchHubWithoutBundlingRealitySNI(t *testing.T) {
	tests := []struct {
		proto config.Protocol
		keys  []string
	}{
		{config.ProtocolRealityVision, []string{"reality_vision_uuid", "reality_vision_port"}},
		{config.ProtocolRealityGRPC, []string{"reality_grpc_uuid", "reality_grpc_port"}},
		{config.ProtocolHysteria2, []string{"hysteria2_password", "hysteria2_port"}},
		{config.ProtocolTUIC, []string{"tuic_uuid", "tuic_password", "tuic_port"}},
		{config.ProtocolAnyTLS, []string{"anytls_password", "anytls_port"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.proto), func(t *testing.T) {
			fields := spokeProtocolEditFields(tt.proto)
			var keys []string
			for _, field := range fields {
				keys = append(keys, field.key)
				if field.key == "reality_sni" || field.key == "reality_handshake_port" ||
					field.key == "reality_private_key" || field.key == "reality_public_key" ||
					field.key == "reality_short_id" {
					t.Fatalf("Reality shared field was bundled into protocol edit: %+v", fields)
				}
				if !strings.HasSuffix(field.key, "_port") && !field.secret {
					t.Fatalf("credential field %q is not masked", field.key)
				}
			}
			if strings.Join(keys, ",") != strings.Join(tt.keys, ",") {
				t.Fatalf("fields = %v, want %v", keys, tt.keys)
			}
		})
	}
}

func TestSpokeProtocolTargetAndRollbackRevisionsCoverEveryProtocol(t *testing.T) {
	generated, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	originalCredentials := nodeapi.ProtocolCredentials{
		RealityVisionUUID: generated.RealityVisionUUID,
		RealityGRPCUUID:   generated.RealityGRPCUUID,
		HysteriaPassword:  generated.HysteriaPassword,
		TUICUUID:          generated.TUICUUID,
		TUICPassword:      generated.TUICPassword,
		AnyTLSPassword:    generated.AnyTLSPassword,
		RealityPrivateKey: generated.RealityPrivateKey,
		RealityPublicKey:  generated.RealityPublicKey,
		RealityShortID:    generated.RealityShortID,
	}
	current := nodeapi.ProtocolStateResponse{
		Revision:             strings.Repeat("a", 64),
		Domain:               "uk.example.com",
		RealityServerName:    "www.example.com",
		RealityHandshakePort: 443,
		EnabledProtocols:     []string{"vless-reality-vision", "vless-reality-grpc", "hysteria2", "tuic", "anytls"},
		Ports: nodeapi.PortSet{
			RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443,
			TUIC: 10443, AnyTLS: 11443,
		},
		Credentials: originalCredentials,
	}
	originalRevision, err := nodeapi.ProtocolStateRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	current.Revision = originalRevision

	tests := []struct {
		name  string
		proto config.Protocol
		port  int
		edit  func(*nodeapi.ProtocolCredentials)
	}{
		{"reality vision", config.ProtocolRealityVision, 18443, func(c *nodeapi.ProtocolCredentials) {
			c.RealityVisionUUID = "11111111-1111-4111-8111-111111111111"
		}},
		{"reality grpc", config.ProtocolRealityGRPC, 18444, func(c *nodeapi.ProtocolCredentials) {
			c.RealityGRPCUUID = "22222222-2222-4222-8222-222222222222"
		}},
		{"hysteria2", config.ProtocolHysteria2, 19443, func(c *nodeapi.ProtocolCredentials) {
			c.HysteriaPassword = "changed-hysteria-password"
		}},
		{"tuic", config.ProtocolTUIC, 20443, func(c *nodeapi.ProtocolCredentials) {
			c.TUICUUID = "33333333-3333-4333-8333-333333333333"
			c.TUICPassword = "changed-tuic-password"
		}},
		{"anytls", config.ProtocolAnyTLS, 21443, func(c *nodeapi.ProtocolCredentials) {
			c.AnyTLSPassword = "changed-anytls-password"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetCredentials := originalCredentials
			tt.edit(&targetCredentials)
			patch := nodeapi.ProtocolPatch{
				Protocol: string(tt.proto), Port: tt.port, Credentials: targetCredentials,
			}
			gotTarget, err := spokeProtocolTargetRevision(current, patch)
			if err != nil {
				t.Fatal(err)
			}
			applied := current
			if err := applyProtocolPatchToState(&applied, patch); err != nil {
				t.Fatal(err)
			}
			applied.Revision = ""
			wantTarget, err := nodeapi.ProtocolStateRevision(applied)
			if err != nil {
				t.Fatal(err)
			}
			if gotTarget != wantTarget {
				t.Fatalf("target revision=%q, want %q", gotTarget, wantTarget)
			}

			rollback, err := spokeProtocolRollbackPatch(tt.proto, current)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyProtocolPatchToState(&applied, rollback); err != nil {
				t.Fatal(err)
			}
			applied.Revision = ""
			gotRollback, err := nodeapi.ProtocolStateRevision(applied)
			if err != nil {
				t.Fatal(err)
			}
			if gotRollback != originalRevision {
				t.Fatalf("rollback revision=%q, want original %q", gotRollback, originalRevision)
			}
		})
	}
}

func TestSpokeProtocolNodeRevisionMatchesAgentNormalization(t *testing.T) {
	generated, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	current := nodeapi.ProtocolStateResponse{
		Domain:               "agent.example.com",
		RealityServerName:    "old.example.com",
		RealityHandshakePort: 8443,
		EnabledProtocols:     []string{"hysteria2"},
		Ports:                nodeapi.PortSet{Hysteria2: 9443},
		Credentials: nodeapi.ProtocolCredentials{
			RealityVisionUUID: generated.RealityVisionUUID,
			RealityGRPCUUID:   generated.RealityGRPCUUID,
			HysteriaPassword:  generated.HysteriaPassword,
			TUICUUID:          generated.TUICUUID,
			TUICPassword:      generated.TUICPassword,
			AnyTLSPassword:    generated.AnyTLSPassword,
			RealityPrivateKey: generated.RealityPrivateKey,
			RealityPublicKey:  generated.RealityPublicKey,
			RealityShortID:    generated.RealityShortID,
		},
	}
	node := nodes.Node{
		// ReplaceProtocolState deliberately preserves the Agent's Domain even
		// if an inconsistent registry snapshot contains another value.
		Domain:               "registry.example.com",
		RealityServerName:    "new.example.com",
		RealityHandshakePort: 0,
		EnabledProtocols:     []string{"anytls"},
		AnyTLSPort:           21443,
	}
	got, err := spokeProtocolNodeRevision(current, node)
	if err != nil {
		t.Fatal(err)
	}
	expected := current
	expected.Domain = "agent.example.com"
	expected.RealityServerName = node.RealityServerName
	expected.RealityHandshakePort = config.DefaultRealityHandshakePort
	expected.EnabledProtocols = []string{"anytls"}
	expected.Ports = nodeapi.PortSet{AnyTLS: node.AnyTLSPort}
	want, err := nodeapi.ProtocolStateRevision(expected)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("normalized target revision=%q, want %q", got, want)
	}
}

func TestValidateSpokeProtocolStateAcceptsLegacyDefaultHandshakePort(t *testing.T) {
	node := nodes.Node{
		Domain: "legacy.example.com", EnabledProtocols: []string{"hysteria2"},
		Hysteria2Port: 9443, RealityHandshakePort: 0,
	}
	state := nodeapi.ProtocolStateResponse{
		Domain: "legacy.example.com", EnabledProtocols: []string{"hysteria2"},
		Ports:                nodeapi.PortSet{Hysteria2: 9443},
		RealityHandshakePort: config.DefaultRealityHandshakePort,
		Credentials:          nodeapi.ProtocolCredentials{HysteriaPassword: "legacy-password"},
	}
	revision, err := nodeapi.ProtocolStateRevision(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision = revision
	if err := validateSpokeProtocolState(node, state); err != nil {
		t.Fatalf("legacy default handshake port rejected: %v", err)
	}
	state.RealityHandshakePort = 8443
	state.Revision, err = nodeapi.ProtocolStateRevision(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSpokeProtocolState(node, state); err == nil {
		t.Fatal("non-default Agent handshake drift was accepted")
	}
}

func TestSpokeProtocolEditRejectsStaleCredentialSnapshotBeforeMutation(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "abababababababababababababababab", Alias: "London", Domain: "uk.example.com",
		WGIP: "10.90.0.2", Token: "token", Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		RealityVisionPort: 8443, RealityGRPCPort: 8444, TUICPort: 10443, AnyTLSPort: 11443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.action = protocolActionEditSpoke
	pm.editNodeID = node.ID
	pm.cursor = 0
	cmd := pm.startEditForm()
	if cmd == nil || pm.phase != protocolPhaseLoadingSpokeState {
		t.Fatalf("start editor load: cmd=%v phase=%v", cmd != nil, pm.phase)
	}
	_, _ = pm.Update(cmd())
	if !pm.haveSpokeState || pm.phase != protocolPhaseForm {
		t.Fatalf("open editor: state=%v phase=%v err=%q", pm.haveSpokeState, pm.phase, pm.fieldErr)
	}
	stale := pm.editSpokeState
	stale.Credentials.HysteriaPassword += "-changed-elsewhere"
	fetchSpokeProtocolState = func(context.Context, nodes.Node) (nodeapi.ProtocolStateResponse, error) {
		return stale, nil
	}
	err := pm.applySpokeProtocol(context.Background(), &logWriter{ch: make(chan runMsg, 1)}, nil)
	if err == nil || !strings.Contains(err.Error(), "changed after this form was opened") {
		t.Fatalf("stale snapshot error = %v", err)
	}
	persisted, loadErr := nodes.Load(layout)
	if loadErr != nil || len(persisted) != 1 || persisted[0].Hysteria2Port != node.Hysteria2Port {
		t.Fatalf("registry mutated before stale-state rejection: %+v err=%v", persisted, loadErr)
	}
}

func TestSpokeProtocolStateLoadCanBeCancelledWithoutApplyingStaleResult(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", Alias: "London", Domain: "uk.example.com",
		WGIP: "10.90.0.2", Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		RealityVisionPort: 8443, RealityGRPCPort: 8444, TUICPort: 10443, AnyTLSPort: 11443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.action = protocolActionEditSpoke
	pm.editNodeID = node.ID
	pm.cursor = 0
	cmd := pm.startEditForm()
	if cmd == nil || pm.phase != protocolPhaseLoadingSpokeState {
		t.Fatalf("start editor load: cmd=%v phase=%v", cmd != nil, pm.phase)
	}
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !done || pm.spokeStateStop != nil {
		t.Fatalf("cancel load: done=%v cancel retained=%v", done, pm.spokeStateStop != nil)
	}
	_, _ = pm.Update(cmd())
	if pm.haveSpokeState || pm.phase != protocolPhaseEditPick {
		t.Fatalf("cancelled response mutated model: state=%v phase=%v", pm.haveSpokeState, pm.phase)
	}
}

func TestSpokeProtocolStateLoadIgnoresEarlierRequestForSameTarget(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "edededededededededededededededed", Alias: "London", Domain: "uk.example.com",
		WGIP: "10.90.0.2", Installed: true,
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		RealityVisionPort: 8443, RealityGRPCPort: 8444, TUICPort: 10443, AnyTLSPort: 11443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.action = protocolActionEditSpoke
	pm.editNodeID = node.ID
	pm.cursor = 0

	first := pm.startEditForm()
	firstID := pm.spokeStateLoad
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if done || pm.phase != protocolPhaseEditPick {
		t.Fatalf("back from first load: done=%v phase=%v", done, pm.phase)
	}
	second := pm.startEditForm()
	secondID := pm.spokeStateLoad
	if first == nil || second == nil || firstID == secondID {
		t.Fatalf("load generations: first=%d second=%d", firstID, secondID)
	}
	_, _ = pm.Update(first())
	if pm.phase != protocolPhaseLoadingSpokeState || pm.spokeStateStop == nil || pm.haveSpokeState {
		t.Fatalf("old response interrupted newer load: phase=%v cancel=%v state=%v",
			pm.phase, pm.spokeStateStop != nil, pm.haveSpokeState)
	}
	_, _ = pm.Update(second())
	if pm.phase != protocolPhaseForm || !pm.haveSpokeState {
		t.Fatalf("new response was not applied: phase=%v state=%v err=%q",
			pm.phase, pm.haveSpokeState, pm.fieldErr)
	}
}

func TestProtocolManagementEditsSpokeRealitySNISeparately(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	node := nodes.Node{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Alias: "London", Domain: "uk.example.com",
		WGIP: "10.90.0.2", Installed: true,
		EnabledProtocols:  []string{"vless-reality-vision"},
		RealityServerName: "www.cloudflare.com", RealityVisionPort: 8443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatalf("save node: %v", err)
	}
	withProtocolManagerDeps(t, layout)

	pm := newProtocolManager()
	pm.setSize(100, 30)
	pm.cursor = pm.actionCursor(protocolActionRealitySNISpoke)
	pm.activateAction()
	_, _ = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pm.phase != protocolPhaseForm || pm.editNodeID != node.ID {
		t.Fatalf("open spoke SNI form: phase=%v id=%q", pm.phase, pm.editNodeID)
	}
	if len(pm.fields) != 1 || pm.fields[0].key != "reality_sni" {
		t.Fatalf("spoke SNI fields = %+v, want only Reality SNI", pm.fields)
	}
	for _, want := range []string{"Spoke", "London", "Reality SNI", "default: www.cloudflare.com"} {
		if !strings.Contains(pm.View(), want) {
			t.Fatalf("spoke SNI form missing %q:\n%s", want, pm.View())
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
		action:     protocolActionChangeSpoke,
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
	change, err := spokeProtocolSelectionRegistryChange(
		[]config.Protocol{config.ProtocolHysteria2},
		map[string]string{
			"protocols":           "vless-reality-grpc,anytls",
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
		remote.RealityServerName != original.RealityServerName || remote.RealityGRPCPort != 18444 ||
		remote.AnyTLSPort != 21443 || remote.ProtocolSettingsGeneration != 1 {
		t.Fatalf("remote target = %+v", remote)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load registry: list=%+v err=%v", list, loadErr)
	}
	got := list[0]
	if strings.Join(got.EnabledProtocols, ",") != "vless-reality-grpc,anytls" ||
		got.RealityServerName != original.RealityServerName || got.AnyTLSPort != 21443 ||
		got.ProtocolSettingsGeneration != 1 {
		t.Fatalf("persisted protocol settings = %+v", got)
	}
	if got.RealityVisionPort != original.RealityVisionPort ||
		got.Hysteria2Port != original.Hysteria2Port ||
		got.TUICPort != original.TUICPort {
		t.Fatalf("install/remove changed ports it does not own: %+v", got)
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
	change, err := spokeProtocolSelectionRegistryChange(
		[]config.Protocol{config.ProtocolRealityVision},
		map[string]string{
			"protocols":           "vless-reality-vision,tuic",
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
			current.RealityServerName = "concurrent.example.com"
			current.Hysteria2Port = 29443
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
	if strings.Join(applied.EnabledProtocols, ",") != "vless-reality-vision,tuic" ||
		applied.RealityVisionPort != original.RealityVisionPort || applied.TUICPort != 20443 {
		t.Fatalf("remote apply did not receive target settings: %+v", applied)
	}
	if strings.Join(rolledBack.EnabledProtocols, ",") != "vless-reality-vision" ||
		rolledBack.RealityVisionPort != original.RealityVisionPort {
		t.Fatalf("remote rollback did not receive original settings: %+v", rolledBack)
	}
	if rolledBack.AgentVersion != "new-agent" ||
		rolledBack.MonitorAlias != "updated concurrently" ||
		rolledBack.RealityServerName != "concurrent.example.com" ||
		rolledBack.Hysteria2Port != 29443 {
		t.Fatalf("remote rollback discarded concurrent settings: %+v", rolledBack)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load rolled-back registry: list=%+v err=%v", list, loadErr)
	}
	got := list[0]
	if strings.Join(got.EnabledProtocols, ",") != "vless-reality-vision" ||
		got.RealityVisionPort != original.RealityVisionPort ||
		got.TUICPort != original.TUICPort {
		t.Fatalf("protocol fields were not restored: %+v", got)
	}
	if got.AgentVersion != "new-agent" || got.MonitorAlias != "updated concurrently" ||
		!got.LastSeen.Equal(time.Unix(1234, 0).UTC()) {
		t.Fatalf("unrelated concurrent fields were overwritten: %+v", got)
	}
	if got.RealityServerName != "concurrent.example.com" || got.Hysteria2Port != 29443 {
		t.Fatalf("rollback overwrote fields not owned by install/remove: %+v", got)
	}
	if len(events) != 2 || events[0].Status != "running" || events[1].Status != "ok" {
		t.Fatalf("registry progress = %+v", events)
	}
}

func TestSpokeProtocolRollbackPreservesNewerSamePortGeneration(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	original := nodes.Node{
		ID: "acacacacacacacacacacacacacacacac", Alias: "London", Domain: "uk.example.com",
		EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		ProtocolSettingsGeneration: 10,
	}
	if err := nodes.Save(layout, []nodes.Node{original}); err != nil {
		t.Fatal(err)
	}
	change, err := spokeProtocolPortRegistryChange(config.ProtocolHysteria2, map[string]string{
		"hysteria2_port": "10443",
	})
	if err != nil {
		t.Fatal(err)
	}
	applyErr := errors.New("injected post-apply failure")
	var rollbackNode nodes.Node
	err = applySpokeRegistryReconfigure(
		context.Background(), layout, original.ID, io.Discard, nil, change,
		func(_ context.Context, applied nodes.Node, _ io.Writer) error {
			if applied.ProtocolSettingsGeneration != 11 || applied.Hysteria2Port != 10443 {
				t.Fatalf("applied registry state = %+v", applied)
			}
			// A newer credential edit chose the same public port. The visible
			// value alone cannot identify transaction ownership.
			if err := nodes.Mutate(layout, original.ID, func(current *nodes.Node) error {
				current.Hysteria2Port = 10443
				current.ProtocolSettingsGeneration = 12
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return applyErr
		},
		func(_ context.Context, current nodes.Node, _ io.Writer) error {
			rollbackNode = current
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), applyErr.Error()) {
		t.Fatalf("transaction error = %v", err)
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load registry: %+v err=%v", list, loadErr)
	}
	if list[0].Hysteria2Port != 10443 || list[0].ProtocolSettingsGeneration != 12 {
		t.Fatalf("older rollback overwrote newer same-port edit: %+v", list[0])
	}
	if rollbackNode.Hysteria2Port != 10443 || rollbackNode.ProtocolSettingsGeneration != 12 {
		t.Fatalf("remote rollback did not receive concurrent registry state: %+v", rollbackNode)
	}
}

func TestSpokeRegistryChangeRejectsUnknownGeneration(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "bcbcbcbcbcbcbcbcbcbcbcbcbcbcbcbc", Alias: "London",
		Domain: "uk.example.com", EnabledProtocols: []string{"hysteria2"},
		Hysteria2Port: 9443,
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	remoteCalled := false
	err := applySpokeRegistryReconfigure(
		context.Background(), layout, node.ID, io.Discard, nil,
		spokeRegistryChange{
			Detail:     "invalid generation",
			Generation: spokeRegistryGeneration(99),
			Apply:      func(*nodes.Node) error { return nil },
			Restore:    func(*nodes.Node, nodes.Node, nodes.Node) {},
		},
		func(context.Context, nodes.Node, io.Writer) error {
			remoteCalled = true
			return nil
		},
		func(context.Context, nodes.Node, io.Writer) error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("unknown generation error = %v", err)
	}
	if remoteCalled {
		t.Fatal("unknown generation reached the Agent")
	}
}

func TestSpokeRegistryGenerationOverflowDoesNotPersistVisibleChange(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	node := nodes.Node{
		ID: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", Alias: "London",
		Domain: "uk.example.com", EnabledProtocols: []string{"hysteria2"},
		Hysteria2Port: 9443, ProtocolSettingsGeneration: ^uint64(0),
	}
	if err := nodes.Save(layout, []nodes.Node{node}); err != nil {
		t.Fatal(err)
	}
	change, err := spokeProtocolPortRegistryChange(config.ProtocolHysteria2, map[string]string{
		"hysteria2_port": "10443",
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteCalled := false
	err = applySpokeRegistryReconfigure(
		context.Background(), layout, node.ID, io.Discard, nil, change,
		func(context.Context, nodes.Node, io.Writer) error {
			remoteCalled = true
			return nil
		},
		func(context.Context, nodes.Node, io.Writer) error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("overflow error = %v", err)
	}
	if remoteCalled {
		t.Fatal("overflowed transaction reached the Agent")
	}
	list, loadErr := nodes.Load(layout)
	if loadErr != nil || len(list) != 1 {
		t.Fatalf("load registry: list=%+v err=%v", list, loadErr)
	}
	if list[0].Hysteria2Port != node.Hysteria2Port ||
		list[0].ProtocolSettingsGeneration != node.ProtocolSettingsGeneration {
		t.Fatalf("overflowed transaction persisted a partial change: %+v", list[0])
	}
}

func TestSpokeProtocolPortAndSNIChangesOwnOnlyTheirField(t *testing.T) {
	original := nodes.Node{
		EnabledProtocols:  []string{"vless-reality-vision", "hysteria2"},
		RealityServerName: "www.microsoft.com",
		RealityVisionPort: 8443,
		Hysteria2Port:     9443,
	}

	portChange, err := spokeProtocolPortRegistryChange(config.ProtocolHysteria2, map[string]string{
		"hysteria2_port": "19443",
	})
	if err != nil {
		t.Fatalf("build port change: %v", err)
	}
	afterPort := cloneSpokeNode(original)
	if err := portChange.Apply(&afterPort); err != nil {
		t.Fatalf("apply port change: %v", err)
	}
	if afterPort.Hysteria2Port != 19443 || afterPort.RealityServerName != original.RealityServerName {
		t.Fatalf("port change touched unrelated fields: %+v", afterPort)
	}
	appliedPort := cloneSpokeNode(afterPort)
	afterPort.RealityServerName = "concurrent.example.com"
	portChange.Restore(&afterPort, original, appliedPort)
	if afterPort.Hysteria2Port != original.Hysteria2Port ||
		afterPort.RealityServerName != "concurrent.example.com" {
		t.Fatalf("port rollback touched unrelated fields: %+v", afterPort)
	}
	concurrentPort := cloneSpokeNode(appliedPort)
	concurrentPort.Hysteria2Port = 29443
	portChange.Restore(&concurrentPort, original, appliedPort)
	if concurrentPort.Hysteria2Port != 29443 {
		t.Fatalf("port rollback overwrote a concurrent owner: %+v", concurrentPort)
	}

	sniChange, err := spokeRealitySNIRegistryChange(map[string]string{
		"reality_sni": "https://www.cloudflare.com/cdn-cgi/trace",
	})
	if err != nil {
		t.Fatalf("build SNI change: %v", err)
	}
	afterSNI := cloneSpokeNode(original)
	if err := sniChange.Apply(&afterSNI); err != nil {
		t.Fatalf("apply SNI change: %v", err)
	}
	if afterSNI.RealityServerName != "www.cloudflare.com" ||
		afterSNI.Hysteria2Port != original.Hysteria2Port {
		t.Fatalf("SNI change touched unrelated fields: %+v", afterSNI)
	}
	appliedSNI := cloneSpokeNode(afterSNI)
	afterSNI.Hysteria2Port = 29443
	sniChange.Restore(&afterSNI, original, appliedSNI)
	if afterSNI.RealityServerName != original.RealityServerName ||
		afterSNI.Hysteria2Port != 29443 {
		t.Fatalf("SNI rollback touched unrelated fields: %+v", afterSNI)
	}
	concurrentSNI := cloneSpokeNode(appliedSNI)
	concurrentSNI.RealityServerName = "newer.example.com"
	sniChange.Restore(&concurrentSNI, original, appliedSNI)
	if concurrentSNI.RealityServerName != "newer.example.com" {
		t.Fatalf("SNI rollback overwrote a concurrent owner: %+v", concurrentSNI)
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
