package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestCertificateMenuEntryOpens(t *testing.T) {
	m := NewModel()
	m.SetSize(180, 40)
	setMenuCursor(t, m, "Certificate management")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.certificates == nil {
		t.Fatalf("certificate manager was not opened")
	}
	view := m.View()
	for _, want := range []string{"Certificate management", "Add certificate", "Renew certificate", "Manage DNS zones"} {
		if !strings.Contains(view, want) {
			t.Fatalf("certificate view missing %q:\n%s", want, view)
		}
	}
}

func TestNodesMenuEntryGatedBeforeHubInstall(t *testing.T) {
	m := NewModel()
	m.SetSize(180, 40)
	setMenuCursor(t, m, "Spoke nodes")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.nodes == nil {
		t.Fatalf("node manager was not opened")
	}
	// With no hub install present, adding a node must be gated.
	if m.nodes.hubReady {
		t.Skip("a hub is installed on this host; gate test not applicable")
	}
	view := m.View()
	if !strings.Contains(view, "Run Setup first") {
		t.Fatalf("node view should gate on hub install:\n%s", view)
	}
	// Selecting "Add spoke node" while gated surfaces the reason instead of a form.
	m.nodes.actionCur = 0
	_, _ = m.nodes.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.nodes.phase != nodePhaseList {
		t.Fatalf("gated add should stay on the list, phase=%d", m.nodes.phase)
	}
}

func TestCertFormRedirectsWhenNoCredential(t *testing.T) {
	m := newCertManager()
	m.creds = nil // no credentials cover anything
	m.beginCertForm()
	// Fill the domain field and complete.
	m.form.values["domain"] = "vpn.example.com"
	m.completeForm()
	if m.phase != certPhaseCredForm {
		t.Fatalf("expected redirect to credential form, phase=%d", m.phase)
	}
	if !m.resumeIssueAfterCred || m.pendingDomain != "vpn.example.com" {
		t.Fatalf("issuance should be queued to resume: resume=%v domain=%q", m.resumeIssueAfterCred, m.pendingDomain)
	}
	// The credential form is pre-seeded with the domain.
	if m.form.values["domain"] != "vpn.example.com" {
		t.Fatalf("credential form not seeded with domain: %q", m.form.values["domain"])
	}
}

func TestRootModelSuspendsAndRestoresNodeFormForCertificate(t *testing.T) {
	root := NewModel()
	flow := newNodeManager()
	flow.phase = nodePhaseForm
	flow.certificateDomainRequest = "spoke.example.com"
	root.nodes = flow

	// A non-key message lets the root observe the redirect request without
	// changing the form's own phase.
	_, _ = root.Update(struct{}{})
	if root.nodes != nil || root.suspendedNodes != flow || root.certificates == nil {
		t.Fatalf("node form was not suspended: nodes=%p suspended=%p certs=%p", root.nodes, root.suspendedNodes, root.certificates)
	}
	if root.certificates.form.values["domain"] != "spoke.example.com" || !root.certificates.returnAfterIssue {
		t.Fatalf("certificate flow was not seeded: values=%v return=%v", root.certificates.form.values, root.certificates.returnAfterIssue)
	}

	// A completed issuance returns to the exact retained node form. The operator
	// can press Enter again to revalidate the now-managed domain and continue.
	root.certificates.phase = certPhaseDone
	root.certificates.run.runErr = nil
	_, _ = root.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if root.certificates != nil || root.nodes != flow || root.suspendedNodes != nil {
		t.Fatalf("node form was not restored: nodes=%p suspended=%p certs=%p", root.nodes, root.suspendedNodes, root.certificates)
	}
}

func TestRootModelSuspendsAndRestoresInstallFormForCertificate(t *testing.T) {
	root := NewModel()
	flow := newInstallFlow()
	flow.phase = phaseForm
	flow.certificateDomainRequest = "hub.example.com"
	root.install = flow

	_, _ = root.Update(struct{}{})
	if root.install != nil || root.suspendedInstall != flow || root.certificates == nil {
		t.Fatalf("install form was not suspended")
	}
	if root.certificates.form.values["domain"] != "hub.example.com" {
		t.Fatalf("certificate flow seed = %v", root.certificates.form.values)
	}
	root.certificates.phase = certPhaseDone
	root.certificates.run.runErr = nil
	_, _ = root.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if root.certificates != nil || root.install != flow || root.suspendedInstall != nil {
		t.Fatalf("install form was not restored")
	}
}

func TestCertificateDeleteRefusesInstalledSpokeConsumer(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const domain = "spoke.example.com"
	if err := certmgr.Register(layout, domain); err != nil {
		t.Fatalf("register certificate: %v", err)
	}
	if err := nodes.Add(layout, nodes.Node{
		Alias: "tokyo", SSHHost: "tokyo.example.com", Domain: domain,
		WGIP: "10.90.0.2", Installed: true,
	}); err != nil {
		t.Fatalf("register spoke: %v", err)
	}
	list, err := nodes.Load(layout)
	if err != nil || len(list) != 1 {
		t.Fatalf("load spoke registry: list=%+v err=%v", list, err)
	}

	m := newCertManager()
	m.layout = layout
	m.reload()
	if len(m.inventory) != 1 || m.inventory[0].Domain != domain {
		t.Fatalf("unexpected certificate inventory: %+v", m.inventory)
	}
	m.phase = certPhaseCertPick
	m.pickCursor = 0
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice.text, "cannot delete "+domain) || !strings.Contains(m.notice.text, "certificate is used by tokyo ("+domain+")") {
		t.Fatalf("delete was not blocked: %q", m.notice.text)
	}
	if strings.Contains(m.notice.text, list[0].ID) {
		t.Fatalf("delete protection leaked raw spoke ID %q: %q", list[0].ID, m.notice.text)
	}
	managed, err := certmgr.IsManaged(layout, domain)
	if err != nil || !managed {
		t.Fatalf("in-use certificate was deregistered: managed=%v err=%v", managed, err)
	}
}

func TestNodeHostKeyFingerprintRequiresExplicitConfirmation(t *testing.T) {
	m := newNodeManager()
	m.pendingTarget = bootstrap.Target{
		Host: "2001:db8::20", Port: 22, User: "root",
		Auth: bootstrap.Auth{PrivateKeyPEM: []byte("secret-key")},
	}
	m.phase = nodePhaseHostKeyScan
	info := bootstrap.HostKeyInfo{Algorithm: "ssh-ed25519", Fingerprint: "SHA256:confirmed-key"}
	_, _ = m.Update(nodeHostKeyScanMsg{info: info})
	if m.phase != nodePhaseHostKeyConfirm {
		t.Fatalf("scan did not enter confirmation phase: %d", m.phase)
	}
	view := m.View()
	for _, want := range []string{"[2001:db8::20]:22", "ssh-ed25519", "SHA256:confirmed-key", "Press y"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation view missing %q:\n%s", want, view)
		}
	}
	// Enter is intentionally not an affirmative action: the operator must type y.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseHostKeyConfirm {
		t.Fatalf("Enter implicitly trusted the key")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.phase != nodePhaseList || len(m.pendingTarget.Auth.PrivateKeyPEM) != 0 {
		t.Fatalf("cancel did not discard pending auth: phase=%d auth=%+v", m.phase, m.pendingTarget.Auth)
	}
}

// A duplicate alias would collide in the aggregated subscription, so the form
// must reject it before SSH provisioning starts rather than after.
func TestAddSpokeFormRejectsDuplicateAlias(t *testing.T) {
	m := newNodeManager()
	m.list = []nodes.Node{
		{ID: "11111111111111111111111111111111", Alias: "Tokyo", Domain: "a.example.com"},
		{ID: "22222222222222222222222222222222", Domain: "b.example.com"},
	}
	aliasField := field{key: "alias"}
	for _, duplicate := range []string{"tokyo", " TOKYO ", "b.example.com"} {
		if err := m.validateForm(aliasField, duplicate, nil); err == nil {
			t.Fatalf("alias %q was accepted despite colliding with an existing spoke", duplicate)
		} else if !strings.Contains(err.Error(), "already used by") {
			t.Fatalf("alias %q error = %v", duplicate, err)
		}
	}
	if err := m.validateForm(aliasField, "Osaka", nil); err != nil {
		t.Fatalf("distinct alias rejected: %v", err)
	}
}

func TestAddSpokeFormCollectsProtocolAndMonitorSettings(t *testing.T) {
	m := &nodeManager{form: newParameterForm(nil)}
	m.beginForm()
	fieldKeys := make(map[string]bool, len(m.form.fields))
	for _, field := range m.form.fields {
		fieldKeys[field.key] = true
	}
	for _, key := range []string{
		"protocols", "reality_sni", "reality_vision_port", "reality_grpc_port",
		"hysteria2_port", "tuic_port", "anytls_port", "monitor", "monitor_alias",
		"monitor_interface", "monitor_interval_seconds", "traffic_in_limit",
		"traffic_out_limit", "traffic_total_limit", "reset_day", "reset_hour",
	} {
		if !fieldKeys[key] {
			t.Errorf("add-spoke form is missing %q", key)
		}
	}

	m.form.values = map[string]string{
		"alias":                    "UK",
		"ssh_host":                 "192.0.2.20",
		"ssh_port":                 "36169",
		"ssh_user":                 "root",
		"ssh_auth":                 "password",
		"ssh_password":             "memory-only",
		"domain":                   "uk.example.com",
		"protocols":                "vless-reality-vision,hysteria2",
		"reality_sni":              "https://www.cloudflare.com/path",
		"reality_vision_port":      "18001",
		"reality_grpc_port":        "18002",
		"hysteria2_port":           "18003",
		"tuic_port":                "18004",
		"anytls_port":              "18005",
		"monitor":                  "yes",
		"monitor_alias":            "UK-monitor",
		"monitor_interface":        "auto",
		"monitor_interval_seconds": "30",
		"traffic_in_limit":         "5GB",
		"traffic_out_limit":        "6GB",
		"traffic_total_limit":      "10GB",
		"reset_day":                "12",
		"reset_hour":               "3",
	}
	m.completeForm()

	node := m.pendingRegistry
	if got := strings.Join(node.EnabledProtocols, ","); got != "vless-reality-vision,hysteria2" {
		t.Errorf("enabled protocols = %q", got)
	}
	if node.RealityServerName != "www.cloudflare.com" ||
		node.RealityVisionPort != 18001 || node.Hysteria2Port != 18003 {
		t.Errorf("protocol settings were not preserved: %+v", node)
	}
	if !node.Monitor || node.MonitorAlias != "UK-monitor" || node.MonitorInterface != "" ||
		node.MonitorIntervalSeconds != 30 || node.ResetDay != 12 || node.ResetHour != 3 {
		t.Errorf("monitor settings were not preserved: %+v", node)
	}
	if node.TrafficInLimitBytes != uint64(5)<<30 ||
		node.TrafficOutLimitBytes != uint64(6)<<30 ||
		node.TrafficTotalLimitBytes != uint64(10)<<30 {
		t.Errorf("traffic limits were not preserved: %+v", node)
	}
	if m.pendingTarget.Auth.Password == "" || m.form.values["ssh_password"] != "" {
		t.Fatal("SSH password was not moved out of form state")
	}
}

func TestAddSpokeFormValidatesActivePortSet(t *testing.T) {
	m := &nodeManager{}
	portField := field{key: "hysteria2_port"}
	values := map[string]string{
		"protocols":                "hysteria2",
		"hysteria2_port":           "12000",
		"anytls_port":              "12000",
		"monitor":                  "no",
		"monitor_interval_seconds": "60",
	}
	if err := m.validateForm(portField, "12000", values); err != nil {
		t.Fatalf("inactive AnyTLS port caused a false conflict: %v", err)
	}
	values["protocols"] = "hysteria2,anytls"
	if err := m.validateForm(portField, "12000", values); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("active duplicate protocol port = %v", err)
	}
	values["protocols"] = "hysteria2"
	values["monitor"] = "yes"
	values["hysteria2_port"] = "19090"
	if err := m.validateForm(portField, "19090", values); err == nil || !strings.Contains(err.Error(), "monitor service") {
		t.Fatalf("monitor port collision = %v", err)
	}
	if err := m.validateForm(field{key: "monitor_alias"}, "", values); err != nil {
		t.Fatalf("blank monitor alias should fall back to node alias: %v", err)
	}
}

// Editing a spoke must not report the node's own alias as a conflict.
func TestSpokeSubscriptionFormAliasUniqueness(t *testing.T) {
	sm := &subscriptionManager{
		nodes: []nodes.Node{
			{ID: "11111111111111111111111111111111", Alias: "Tokyo", Domain: "a.example.com"},
			{ID: "22222222222222222222222222222222", Alias: "Osaka", Domain: "b.example.com"},
		},
		editNodeIndex: 1,
	}
	aliasField := field{key: "spoke_alias"}
	if err := sm.validateSpokeField(aliasField, "Osaka", nil); err != nil {
		t.Fatalf("keeping the node's own alias was rejected: %v", err)
	}
	if err := sm.validateSpokeField(aliasField, "tokyo", nil); err == nil ||
		!strings.Contains(err.Error(), "already used by") {
		t.Fatalf("renaming onto another spoke's alias = %v", err)
	}
	if err := sm.validateSpokeField(aliasField, "Kyoto", nil); err != nil {
		t.Fatalf("distinct alias rejected: %v", err)
	}
}

func TestSpokeSubscriptionRunForwardsProgressEvents(t *testing.T) {
	original := applySpokeSubscriptionRun
	t.Cleanup(func() { applySpokeSubscriptionRun = original })
	applySpokeSubscriptionRun = func(_ *subscriptionManager, _ context.Context, _ *logWriter, progress func(deploy.Event)) error {
		deploy.EmitProgress(progress, deploy.Event{
			Index: 1, Total: 2, Label: "Spoke configuration", Status: "running",
		})
		return nil
	}
	sm := &subscriptionManager{
		phase:      subscriptionPhaseConfirm,
		action:     subscriptionActionEditSpoke,
		host:       supportedTestHost(),
		commandRun: newCommandRun(),
	}
	wait := sm.startRun()
	if wait == nil {
		t.Fatal("spoke subscription run did not start")
	}
	msg, ok := wait().(runMsg)
	if !ok || msg.event == nil || msg.event.Label != "Spoke configuration" {
		t.Fatalf("first run message = %#v, want forwarded progress event", msg)
	}
}

func TestSpokeMonitorRunForwardsProgressEvents(t *testing.T) {
	original := applySpokeMonitorRun
	t.Cleanup(func() { applySpokeMonitorRun = original })
	applySpokeMonitorRun = func(_ *monitorManager, _ context.Context, _ *logWriter, progress func(deploy.Event)) error {
		deploy.EmitProgress(progress, deploy.Event{
			Index: 1, Total: 2, Label: "Monitor snapshot", Status: "running",
		})
		return nil
	}
	tm := &monitorManager{
		phase:      monitorPhaseConfirm,
		action:     monitorActionEditSpoke,
		host:       supportedTestHost(),
		commandRun: newCommandRun(),
	}
	wait := tm.startRun()
	if wait == nil {
		t.Fatal("spoke monitor run did not start")
	}
	msg, ok := wait().(runMsg)
	if !ok || msg.event == nil || msg.event.Label != "Monitor snapshot" {
		t.Fatalf("first run message = %#v, want forwarded progress event", msg)
	}
}

func TestForceDetachRequiresExplicitYConfirmation(t *testing.T) {
	m := newNodeManager()
	m.hubReady = true
	m.list = []nodes.Node{{ID: "node-id", Alias: "lost", Domain: "lost.example.com"}}
	m.actionCur = 2
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseDeletePick || m.action != "Force detach" {
		t.Fatalf("force action did not open picker: phase=%d action=%q", m.phase, m.action)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseForceConfirm {
		t.Fatalf("picker did not open force confirmation: phase=%d", m.phase)
	}
	if view := m.View(); !strings.Contains(view, "may remain active") || !strings.Contains(view, "Press y") {
		t.Fatalf("force warning is incomplete:\n%s", view)
	}
	// Enter must never be treated as destructive confirmation.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != nodePhaseForceConfirm {
		t.Fatal("Enter force-detached a node without explicit y")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.phase != nodePhaseList || m.pendingRemove.ID != "" {
		t.Fatalf("cancel did not return safely: phase=%d pending=%+v", m.phase, m.pendingRemove)
	}
}

func TestNodeHostKeyScanRunsAsCommand(t *testing.T) {
	original := scanSpokeHostKey
	t.Cleanup(func() { scanSpokeHostKey = original })
	scanSpokeHostKey = func(_ context.Context, target bootstrap.Target) (bootstrap.HostKeyInfo, error) {
		if target.Host != "spoke.example.com" {
			t.Fatalf("unexpected scan target: %+v", target)
		}
		return bootstrap.HostKeyInfo{Fingerprint: "SHA256:async"}, nil
	}
	m := newNodeManager()
	m.form.values = map[string]string{
		"alias": "spoke", "ssh_host": "spoke.example.com", "ssh_port": "22", "ssh_user": "root",
		"ssh_auth": "password", "ssh_password": "memory-only", "domain": "spoke.example.com",
	}
	m.completeForm()
	if m.phase != nodePhaseHostKeyScan || m.startCmd == nil {
		t.Fatalf("form completion did not schedule asynchronous scan")
	}
	msg := m.startCmd()
	if _, ok := msg.(nodeHostKeyScanMsg); !ok {
		t.Fatalf("scan command returned %T", msg)
	}
}

func TestFinalizeHubInstallDoesNotMarkInstalledOnOverlayFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		t.Fatal(err)
	}
	err := finalizeHubInstall(layout, deploy.Config{Domain: "hub.example.com"}, failingOverlayRunner{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "overlay") {
		t.Fatalf("expected overlay failure, got %v", err)
	}
	if nodes.HubInstalled(layout) {
		t.Fatal("hub_installed remained yes after overlay initialization failed")
	}
}

type failingOverlayRunner struct{}

func (failingOverlayRunner) Run(system.Command) error { return errors.New("injected command failure") }
