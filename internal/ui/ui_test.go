package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/protocol"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

func TestNarrowLayoutMode(t *testing.T) {
	m := NewModel()
	m.SetSize(70, 24)
	if m.LayoutMode() != LayoutNarrow {
		t.Fatalf("mode = %v", m.LayoutMode())
	}
	m.SetSize(120, 30)
	if m.LayoutMode() != LayoutWide {
		t.Fatalf("mode = %v", m.LayoutMode())
	}
}

func TestViewUsesInternalPanels(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	view := m.View()
	if strings.Count(view, "╭") < 2 {
		t.Fatalf("view should render menu and content panels:\n%s", view)
	}
}

func TestSelfUpdateChecksLatestAsynchronously(t *testing.T) {
	origHost := detectSelfUpdateHost
	detectSelfUpdateHost = func() (system.Host, error) { return supportedTestHost(), nil }
	defer func() { detectSelfUpdateHost = origHost }()

	sm := newSelfUpdateManager()
	// The constructor must not block on the network; it starts in the checking
	// phase and only leaves it once the async result arrives.
	if sm.phase != selfUpdatePhaseChecking {
		t.Fatalf("expected checking phase after construction, got %d", sm.phase)
	}
	if !strings.Contains(sm.View(), "Checking for the latest release") {
		t.Fatalf("checking view missing progress text:\n%s", sm.View())
	}

	sm.Update(selfUpdateCheckedMsg{tag: "v2.0.0"})
	if sm.phase != selfUpdatePhaseCheck {
		t.Fatalf("expected check phase after result, got %d", sm.phase)
	}
	if sm.latestTag != "v2.0.0" {
		t.Fatalf("latestTag = %q, want v2.0.0", sm.latestTag)
	}
}

func TestSelfUpdateCheckRefusesOlderLatestRelease(t *testing.T) {
	sm := &selfUpdateManager{currentVersion: "v3.0.0"}
	sm.applyCheckResult(selfUpdateCheckedMsg{tag: "v2.0.0"})
	if sm.phase != selfUpdatePhaseCheck || !strings.Contains(sm.checkErr, "refusing self-update downgrade") {
		t.Fatalf("older-release check phase=%d error=%q", sm.phase, sm.checkErr)
	}
}

type selfUpdateRecordingRunner struct{ commands []string }

func (r *selfUpdateRecordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

func withSelfUpdateBackendDeps(t *testing.T, active bool, runner system.Runner) {
	t.Helper()
	oldActive := selfUpdateMonitorActive
	oldRunner := selfUpdateServiceRunner
	oldUpgrade := selfUpdateUpgradeSpokes
	oldRestore := selfUpdateRestoreSpokes
	t.Cleanup(func() {
		selfUpdateMonitorActive = oldActive
		selfUpdateServiceRunner = oldRunner
		selfUpdateUpgradeSpokes = oldUpgrade
		selfUpdateRestoreSpokes = oldRestore
	})
	selfUpdateMonitorActive = func(context.Context) (bool, error) { return active, nil }
	selfUpdateServiceRunner = func(*logWriter) system.Runner { return runner }
	selfUpdateUpgradeSpokes = func(context.Context, string, string, *logWriter) error { return nil }
	selfUpdateRestoreSpokes = func(context.Context, *logWriter) error { return nil }
}

func TestSelfUpdateTryRestartsPreviouslyActiveMonitorWithoutStoppingIt(t *testing.T) {
	runner := &selfUpdateRecordingRunner{}
	withSelfUpdateBackendDeps(t, true, runner)
	logs := &logWriter{ch: make(chan runMsg, 16)}
	mgr := (&selfUpdateManager{host: supportedTestHost(), currentVersion: "v1.0.0"}).backendManager(logs)
	if err := mgr.BeforeReplace(context.Background(), "/tmp/candidate", "v2.0.0"); err != nil {
		t.Fatalf("BeforeReplace: %v", err)
	}
	if err := mgr.AfterReplace(context.Background(), "v2.0.0"); err != nil {
		t.Fatalf("AfterReplace: %v", err)
	}
	want := []string{
		"systemctl try-restart " + system.MonitorService,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("monitor activation commands = %v, want %v", runner.commands, want)
	}
}

func TestSelfUpdateLeavesPreviouslyInactiveMonitorStopped(t *testing.T) {
	runner := &selfUpdateRecordingRunner{}
	withSelfUpdateBackendDeps(t, false, runner)
	logs := &logWriter{ch: make(chan runMsg, 16)}
	mgr := (&selfUpdateManager{host: supportedTestHost(), currentVersion: "v1.0.0"}).backendManager(logs)
	if err := mgr.BeforeReplace(context.Background(), "/tmp/candidate", "v2.0.0"); err != nil {
		t.Fatalf("BeforeReplace: %v", err)
	}
	if err := mgr.AfterReplace(context.Background(), "v2.0.0"); err != nil {
		t.Fatalf("AfterReplace: %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("inactive monitor service was mutated: %v", runner.commands)
	}
}

func TestSelfUpdateCommittedRunForcesOldTUIToExit(t *testing.T) {
	for _, runErr := range []error{nil, fmt.Errorf("post-update action failed")} {
		sm := &selfUpdateManager{
			phase:     selfUpdatePhaseDone,
			resultTag: "v2.0.0",
			commandRun: commandRun{
				runErr: runErr,
			},
		}
		cmd, done := sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
		if done {
			t.Fatal("committed update returned the stale TUI to its menu")
		}
		if cmd == nil {
			t.Fatal("committed update did not request process exit")
		}
		if msg := cmd(); msg == nil {
			t.Fatal("expected tea.Quit message")
		} else if _, ok := msg.(tea.QuitMsg); !ok {
			t.Fatalf("expected tea.QuitMsg, got %T", msg)
		}
	}
}

func TestSelfUpdateCommittedErrorViewDoesNotClaimRollback(t *testing.T) {
	sm := &selfUpdateManager{
		phase:          selfUpdatePhaseDone,
		currentVersion: "v1.0.0",
		resultTag:      "v2.0.0",
		commandRun: commandRun{
			runErr: fmt.Errorf("monitor restart failed"),
		},
	}
	view := sm.View()
	for _, want := range []string{"Installed version", "v2.0.0", "new hub binary is installed", "monitor restart failed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("committed-error view missing %q:\n%s", want, view)
		}
	}
}

func TestEveryMenuItemHasActivation(t *testing.T) {
	m := NewModel()
	items := m.flatItems()
	if len(items) == 0 {
		t.Fatal("expected a non-empty menu")
	}
	for i, item := range items {
		if item.Activate == nil {
			t.Fatalf("menu item %d (%q) has no activation callback", i, item.Label)
		}
	}
}

func setMenuCursor(t *testing.T, m *Model, label string) {
	t.Helper()
	for i, item := range m.flatItems() {
		if item.Label == label {
			m.cursor = i
			return
		}
	}
	t.Fatalf("menu item %q not found", label)
}

func TestActivateOpensSubFlowByCursor(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	// Activation must open the selected item's manager without any hardcoded
	// index knowledge in activate().
	setMenuCursor(t, m, "Protocol settings")
	m.activate()
	if m.protocols == nil {
		t.Fatal("activating the protocol menu item should open the protocol manager")
	}
}

func TestCtrlCQuitsFromActiveSubFlow(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	m.install = &installFlow{phase: phaseRunning, run: commandRun{bar: progressBarForTest()}}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c during an active sub-flow must return a quit command")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("expected a tea.Quit message")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestInstallViewKeepsMenuVisible(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	m.install = &installFlow{phase: phasePreflight, hosts: "ready", host: supportedTestHost()}
	view := m.View()
	if !strings.Contains(view, "Menu") {
		t.Fatalf("install view should keep menu and install flow visible:\n%s", view)
	}
}

func TestWideInstallContentUsesAvailableHeightAndMenuAdapts(t *testing.T) {
	m := NewModel()
	m.SetSize(160, 60)
	w := &installFlow{phase: phaseRunning, run: commandRun{bar: progressBarForTest()}}
	m.install = w
	view := m.View()
	if got := lipgloss.Height(view); got != 60 {
		t.Fatalf("view height = %d, want 60:\n%s", got, view)
	}
	if want := 60 - 1 - panelStyle.GetVerticalFrameSize(); w.form.height != want {
		t.Fatalf("install flow height = %d, want available content height %d", w.form.height, want)
	}

	body := m.bodyView(160, 59)
	menuHeight := lipgloss.Height(panelStyle.Width(sidebarWidth).Render(m.menuView(sidebarWidth - 4)))
	lines := strings.Split(body, "\n")
	if menuHeight >= len(lines) {
		t.Fatalf("menu height %d should be shorter than body height %d:\n%s", menuHeight, len(lines), body)
	}
	if !strings.HasPrefix(lines[menuHeight-1], "╰") {
		t.Fatalf("menu should end at its content height:\n%s", body)
	}
	if strings.HasPrefix(lines[menuHeight], "│") || strings.HasPrefix(lines[menuHeight], "╰") {
		t.Fatalf("menu should not extend to the right panel height:\n%s", body)
	}
}

func TestViewKeepsFooterAtConfiguredBottom(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 12)
	m.install = confirmInstallFlowForTest()
	view := m.View()
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("view height = %d, want 12:\n%s", got, view)
	}
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[len(lines)-1], "Enter/Y: Setup") {
		t.Fatalf("footer should stay on final row:\n%s", view)
	}
}

func supportedTestHost() system.Host {
	return system.Host{OS: system.OSRelease{Family: system.FamilyDebian, ID: "ubuntu"}, Arch: "amd64", IsRoot: true}
}

func confirmInstallFlowForTest() *installFlow {
	return &installFlow{
		phase: phaseConfirm,
		form: installFormWithValuesForTest(map[string]string{
			"domain":              "example.com",
			"protocols":           defaultProtocolValue(),
			"reality_sni":         "www.microsoft.com",
			"display_name":        "Node",
			"monitor":             "yes",
			"traffic_in_limit":    "0",
			"traffic_out_limit":   "0",
			"traffic_total_limit": "0",
		}),
		host: supportedTestHost(),
	}
}

func installFormForTest() installForm {
	w := newInstallForm()
	w.validateDomain = nil
	w.validateDomainCovered = nil
	return w
}

func installFormWithValuesForTest(values map[string]string) installForm {
	w := installFormForTest()
	w.values = values
	return w
}

func TestInstallFieldShowsUsageNote(t *testing.T) {
	w := installFormForTest()
	w.width = 80
	w.startForm()
	view := w.View()
	if !strings.Contains(view, "Needs a DNS zone covering this name in Certificate management") {
		t.Fatalf("field usage note missing:\n%s", view)
	}
}

func TestProtocolManagementMenuOpens(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, layout)
	m := NewModel()
	setMenuCursor(t, m, "Protocol settings")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.protocols == nil {
		t.Fatalf("protocol manager was not opened")
	}
	view := m.View()
	if !strings.Contains(view, "Protocol settings") || !strings.Contains(view, "Current:") || !strings.Contains(view, "vless-reality-vision") {
		t.Fatalf("protocol manager view missing expected content:\n%s", view)
	}
}

func TestProtocolUpdateRefreshesHubSubscriptions(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, layout)

	updateProtocolsRun = func(context.Context, protocol.UpdateOptions) (deploy.Config, error) {
		return deploy.LoadProtocolConfig(layout)
	}
	refreshed := make(chan struct{}, 1)
	refreshProtocolSubscriptions = func(io.Writer) {
		refreshed <- struct{}{}
	}

	pm := newProtocolManager()
	if pm.loadErr != nil {
		t.Fatalf("load protocol manager: %v", pm.loadErr)
	}
	wait := pm.startRun()
	msg, ok := wait().(runMsg)
	if !ok || !msg.done || msg.err != nil {
		t.Fatalf("protocol update result = %#v", msg)
	}
	select {
	case <-refreshed:
	default:
		t.Fatal("successful protocol update did not refresh Hub subscriptions")
	}
}

func TestSubscriptionMenuEntryOpens(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withSubscriptionDeps(t, layout)

	m := NewModel()
	setMenuCursor(t, m, "Subscription settings")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.subscribe == nil {
		t.Fatalf("subscription manager was not opened")
	}
	view := m.View()
	if !strings.Contains(view, "Subscription settings") || !strings.Contains(view, "Spoke nodes") || !strings.Contains(view, "Edit hub display name") {
		t.Fatalf("subscription manager view missing expected content:\n%s", view)
	}
	if !strings.Contains(view, "WireGuard") || strings.Contains(view, "Add remote subscription") {
		t.Fatalf("subscription manager should expose only private spoke management:\n%s", view)
	}
}

func TestMonitorMenuEntryOpens(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	writeStatusState(t, layout.StateDir, "monitor", "yes")
	writeStatusState(t, layout.StateDir, "monitor_alias", "US-local")
	writeStatusState(t, layout.StateDir, "traffic_in_limit_bytes", fmt.Sprintf("%d", uint64(40)<<30))
	writeStatusState(t, layout.StateDir, "traffic_out_limit_bytes", fmt.Sprintf("%d", uint64(50)<<30))
	writeStatusState(t, layout.StateDir, "traffic_total_limit_bytes", fmt.Sprintf("%d", uint64(100)<<30))
	writeStatusState(t, layout.StateDir, "reset_day", "7")
	writeStatusState(t, layout.StateDir, "reset_hour", "4")
	writeStatusState(t, layout.StateDir, "monitor_interface", "eth0")
	writeStatusState(t, layout.StateDir, "monitor_interval_seconds", "300")
	withMonitorDeps(t, layout)

	m := NewModel()
	setMenuCursor(t, m, "Monitoring")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.monitor == nil {
		t.Fatalf("monitor manager was not opened")
	}
	view := m.View()
	for _, want := range []string{"Monitor", "Monitor alias", "US-local", "Adjust hub traffic counters", "Edit spoke monitor settings", "Adjust spoke traffic counters", "WireGuard only"} {
		if !strings.Contains(view, want) {
			t.Fatalf("monitor manager view missing %q:\n%s", want, view)
		}
	}
}

func TestCoreManagementMenuEntryOpens(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withCoreDeps(t, layout)

	m := NewModel()
	setMenuCursor(t, m, "sing-box core")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.core == nil {
		t.Fatalf("core manager was not opened")
	}
	view := m.View()
	for _, want := range []string{"sing-box core", "Current version", "sing-box version 1.12.0", "Change sing-box version", "View sing-box.service logs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("core manager view missing %q:\n%s", want, view)
		}
	}
}

func TestUninstallMenuEntryOpens(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withUninstallDeps(t, layout)

	m := NewModel()
	m.SetSize(180, 40)
	setMenuCursor(t, m, "Uninstall")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.uninstall == nil {
		t.Fatalf("uninstall manager was not opened")
	}
	view := m.View()
	for _, want := range []string{"Uninstall · Confirm", "sing-box.service", "singbox-deploy-monitor.service", "Certificates", "Monitor database", "Masquerade site files", "Subscription outputs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("uninstall view missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "Unrelated Nginx configs") {
		t.Fatalf("uninstall view should state unrelated Nginx configs are kept:\n%s", view)
	}
}

func TestUninstallConfirmTogglesOptionalData(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withUninstallDeps(t, layout)

	um := newUninstallManager()
	if !um.selected(uninstallRuntimeKey) {
		t.Fatalf("runtime state/config should be selected by default")
	}
	if um.selected(uninstallCertificatesKey) {
		t.Fatalf("certificates should be kept by default")
	}
	um.cursor = 1
	_, done := um.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	if done || !um.selected(uninstallCertificatesKey) {
		t.Fatalf("space should toggle certificates, done=%v selected=%v", done, um.selected(uninstallCertificatesKey))
	}
	view := um.View()
	if !strings.Contains(view, "[x] Certificates") {
		t.Fatalf("certificates should render selected after toggle:\n%s", view)
	}
}

func TestCoreManagementNonRootShowsBlockerOnlyAfterSelection(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withCoreDeps(t, layout)
	detectCoreHost = func() (system.Host, error) {
		host := supportedTestHost()
		host.IsRoot = false
		return host, nil
	}

	cm := newCoreManager()
	msg := "core management must be run as root"
	if strings.Contains(cm.View(), msg) {
		t.Fatalf("core action view should not show initial blocker:\n%s", cm.View())
	}

	cm.activateAction()
	view := cm.View()
	if cm.phase != corePhaseAction || strings.Count(view, msg) != 1 {
		t.Fatalf("core action selection should show one blocker, phase=%v:\n%s", cm.phase, view)
	}
}

func TestCoreChangeStableListsEightReleases(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withCoreDeps(t, layout)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/SagerNet/sing-box/releases" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v1.12.9","prerelease":false,"draft":false},
			{"tag_name":"v1.12.8","prerelease":false,"draft":false},
			{"tag_name":"v1.12.7","prerelease":false,"draft":false},
			{"tag_name":"v1.12.6","prerelease":false,"draft":false},
			{"tag_name":"v1.12.5","prerelease":false,"draft":false},
			{"tag_name":"v1.12.4","prerelease":false,"draft":false},
			{"tag_name":"v1.12.3","prerelease":false,"draft":false},
			{"tag_name":"v1.12.2","prerelease":false,"draft":false},
			{"tag_name":"v1.12.1","prerelease":false,"draft":false}
		]`))
	}))
	t.Cleanup(srv.Close)
	coreReleaseClient = func() *release.Client { return release.NewClient(srv.URL, srv.Client()) }

	cm := newCoreManager()
	cmd := cm.activateAction()
	if cm.phase != corePhaseStableLoading {
		t.Fatalf("phase = %v, want stable loading; err=%q", cm.phase, cm.fieldErr)
	}
	if cmd == nil {
		t.Fatal("activateAction should return an async fetch command")
	}
	// Drive the async fetch to completion.
	cm.Update(cmd())
	if cm.phase != corePhaseStableSelect {
		t.Fatalf("phase = %v, want stable select; err=%q", cm.phase, cm.fieldErr)
	}
	if len(cm.stableTags) != 8 {
		t.Fatalf("stable tags = %v, want 8 releases", cm.stableTags)
	}
	view := cm.View()
	for _, want := range []string{"Change version", "latest 8 stable", "v1.12.9", "v1.12.2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("stable selection view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "v1.12.1") {
		t.Fatalf("stable selection should exclude ninth release:\n%s", view)
	}

	cm.cursor = 7
	_, done := cm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || cm.phase != corePhaseConfirm || cm.targetTag != "v1.12.2" {
		t.Fatalf("select eighth release: done=%v phase=%v target=%q", done, cm.phase, cm.targetTag)
	}
}

func TestSubscriptionSpokeEditingUsesRegisteredNodes(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	list := []nodes.Node{
		{ID: "node-one", Alias: "JP-01", SubscriptionAlias: "Japan clients", Domain: "one.example.com", WGIP: "10.90.0.2", Installed: true, IncludeInSubscription: true, EnabledProtocols: []string{"vless-reality-vision"}, RealityVisionPort: 8443},
		{ID: "node-two", Alias: "US-01", Domain: "two.example.com", WGIP: "10.90.0.3", Installed: true, IncludeInSubscription: true, EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443},
	}
	if err := nodes.Save(layout, list); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	withSubscriptionDeps(t, layout)

	sm := newSubscriptionManager()
	sm.setSize(100, 30)
	if sm.loadErr != nil {
		t.Fatalf("load subscription manager: %v", sm.loadErr)
	}
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionEditSpoke)
	_, done := sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || sm.phase != subscriptionPhaseForm {
		t.Fatalf("enter should open spoke selector, phase=%v done=%v", sm.phase, done)
	}
	view := sm.View()
	for _, want := range []string{"Spoke subscription settings to edit", "JP-01", "10.90.0.2", "node-one"} {
		if !strings.Contains(view, want) {
			t.Fatalf("spoke selector missing %q:\n%s", want, view)
		}
	}
	_, done = sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || sm.phase != subscriptionPhaseForm || sm.editNodeIndex != 0 {
		t.Fatalf("enter should open selected spoke form, phase=%v index=%d done=%v", sm.phase, sm.editNodeIndex, done)
	}
	view = sm.View()
	for _, want := range []string{"Spoke subscription alias", "Japan clients"} {
		if !strings.Contains(view, want) {
			t.Fatalf("spoke form missing %q:\n%s", want, view)
		}
	}
}

func TestSubscriptionHasNoPublicRemoteCRUD(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withSubscriptionDeps(t, layout)

	sm := newSubscriptionManager()
	view := sm.View()
	for _, forbidden := range []string{"Add remote subscription", "Delete remote subscription", "Remote subscription HTTPS port"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("public remote CRUD %q must not be exposed:\n%s", forbidden, view)
		}
	}
	if !strings.Contains(view, "WireGuard") {
		t.Fatalf("subscription page should identify the private transport:\n%s", view)
	}
}

func TestMenuUsesFunctionalGroups(t *testing.T) {
	m := NewModel()
	want := []struct {
		title string
		items []string
	}{
		{title: "Deployment", items: []string{"Setup", "Certificate management"}},
		{title: "Proxy", items: []string{"Protocol settings", "Relay"}},
		{title: "Services", items: []string{"Subscription settings", "Monitoring"}},
		{title: "Spoke", items: []string{"Spoke nodes"}},
		{title: "System", items: []string{"sing-box core", "Self-update", "Uninstall"}},
	}

	if len(m.groups) != len(want) {
		t.Fatalf("menu group count = %d, want %d", len(m.groups), len(want))
	}
	for i, group := range m.groups {
		if group.Title != want[i].title {
			t.Errorf("menu group %d title = %q, want %q", i, group.Title, want[i].title)
		}
		if len(group.Items) != len(want[i].items) {
			t.Errorf("menu group %q item count = %d, want %d", group.Title, len(group.Items), len(want[i].items))
			continue
		}
		for j, item := range group.Items {
			if item.Label != want[i].items[j] {
				t.Errorf("menu group %q item %d = %q, want %q", group.Title, j, item.Label, want[i].items[j])
			}
		}
	}
}

func TestProtocolManagementAsksRealitySNIWhenEnablingReality(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.setSize(100, 30)
	if pm.loadErr != nil {
		t.Fatalf("load protocol manager: %v", pm.loadErr)
	}
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseSelect {
		t.Fatalf("enter should open install/remove selection, phase=%v done=%v", pm.phase, done)
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	if done {
		t.Fatalf("space should not close protocol manager")
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done {
		t.Fatalf("enter should not close protocol manager")
	}
	if pm.phase != protocolPhaseForm {
		t.Fatalf("phase = %v, want parameter form", pm.phase)
	}
	if !strings.Contains(pm.View(), uiparams.LabelRealitySNI) {
		t.Fatalf("missing Reality SNI prompt:\n%s", pm.View())
	}
}

func TestProtocolManagementEditProtocolShowsCredentialAndPortFields(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.setSize(100, 30)
	pm.cursor = pm.actionCursor(protocolActionEdit)
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseEditPick {
		t.Fatalf("enter should open edit picker, phase=%v done=%v", pm.phase, done)
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm {
		t.Fatalf("enter should open edit form, phase=%v done=%v", pm.phase, done)
	}
	view := pm.View()
	if !strings.Contains(view, "Hysteria2 password") || !strings.Contains(view, "default: •••••••• (set)") {
		t.Fatalf("missing password edit field:\n%s", view)
	}
	if strings.Contains(view, "hypass") {
		t.Fatalf("Hub credential leaked in edit form:\n%s", view)
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || !strings.Contains(pm.View(), "Hysteria2 port") || !strings.Contains(pm.View(), "default: 9443") {
		t.Fatalf("missing port edit field:\n%s", pm.View())
	}
}

func TestProtocolInstallConfirmationMasksNewCredential(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.action = protocolActionChange
	pm.selected = selectedOptions("hysteria2,anytls")
	pm.prepareChangeConfirm()
	if pm.phase != protocolPhaseForm {
		t.Fatalf("prepare add protocol: phase=%v err=%q", pm.phase, pm.fieldErr)
	}
	const secret = "new-anytls-confirmation-secret"
	pm.values["anytls_password"] = secret
	pm.values["anytls_port"] = "11443"
	pm.phase = protocolPhaseConfirm
	view := pm.View()
	if strings.Contains(view, secret) || !strings.Contains(view, "•••••••• (set)") {
		t.Fatalf("new protocol credential was not masked in confirmation:\n%s", view)
	}
}

func TestParameterFormFieldErrorSurvivesCursorBlink(t *testing.T) {
	form := newParameterForm([]field{{key: "hysteria2_port", label: "Hysteria2 port", def: "9443"}})
	form.startForm()
	form.fieldErr = "port must be between 1 and 65535"

	// Non-key messages (cursor blink ticks) are forwarded to the input and
	// must not dismiss a validation error the user has not yet seen.
	form.updateInput(cursor.BlinkMsg{})
	if form.fieldErr == "" {
		t.Fatal("field error was cleared by a cursor blink message")
	}

	form.updateInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if form.fieldErr != "" {
		t.Fatal("field error should clear on a real keystroke")
	}
}

func TestParameterInputShowsTwoCharacterDefaultWhenUnsized(t *testing.T) {
	form := newParameterForm([]field{{key: "hysteria2_port", label: "Hysteria2 port", def: "50"}})
	form.startForm()
	form.input.Cursor.Blink = true
	if got := form.input.View(); !strings.Contains(got, "50") {
		t.Fatalf("unsized placeholder = %q, want full default 50", got)
	}

	form.setSize(0, 0)
	form.input.Cursor.Blink = true
	if got := form.input.View(); !strings.Contains(got, "50") {
		t.Fatalf("zero-width placeholder = %q, want full default 50", got)
	}
}

func TestParameterFormMasksSecretFields(t *testing.T) {
	form := newParameterForm([]field{{key: "credential", label: "API credential", secret: true}})
	form.startForm()
	form.input.SetValue("top-secret-token")
	view := form.View("Credential")
	if strings.Contains(view, "top-secret-token") {
		t.Fatalf("secret value was rendered in clear text:\n%s", view)
	}
	if !strings.Contains(view, "••••") {
		t.Fatalf("secret input did not render a mask:\n%s", view)
	}
}

func TestProtocolManagementRealitySNIEntryOnlyForRealityProtocols(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	if strings.Contains(pm.View(), "Edit Reality SNI") {
		t.Fatalf("Reality SNI entry should be hidden without Reality protocols:\n%s", pm.View())
	}

	realityLayout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, realityLayout)
	pm = newProtocolManager()
	view := pm.View()
	if !strings.Contains(view, "Edit Reality SNI") {
		t.Fatalf("Reality SNI entry missing for Reality protocols:\n%s", view)
	}
	pm.cursor = pm.actionCursor(protocolActionRealitySNI)
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm {
		t.Fatalf("enter should open Reality SNI form, phase=%v done=%v", pm.phase, done)
	}
	if !strings.Contains(pm.View(), "default: www.microsoft.com") {
		t.Fatalf("Reality SNI form should show current default:\n%s", pm.View())
	}
}

func TestLoadStatusUsesPersistedStateAndServiceStates(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	writeStatusState(t, layout.StateDir, "domain", "example.com")
	writeStatusState(t, layout.StateDir, "public_ip", "203.0.113.10")
	writeStatusState(t, layout.StateDir, "subscribe_port", "2096")
	writeStatusState(t, layout.StateDir, "monitor_public_port", "2097")
	writeStatusState(t, layout.StateDir, "enabled_protocols", "vless-reality-vision,tuic")
	writeStatusState(t, layout.StateDir, "subscribe_salt", "abcd1234deadbeef")
	writeStatusState(t, layout.StateDir, "display_name", "US-hub")
	writeStatusState(t, layout.StateDir, "monitor", "yes")
	if err := subgroups.Save(layout, []subgroups.Group{
		{ID: "aa11", Alias: "Default", Salt: "abcd1234deadbeef", Members: []string{subgroups.HubMemberID}},
		{ID: "bb22", Alias: "Family", Salt: "familysalt", Members: []string{subgroups.HubMemberID}},
	}); err != nil {
		t.Fatalf("save subscription groups: %v", err)
	}
	writeStatusState(t, layout.StateDir, "traffic_in_limit_bytes", fmt.Sprintf("%d", uint64(40)<<30))
	writeStatusState(t, layout.StateDir, "traffic_out_limit_bytes", fmt.Sprintf("%d", uint64(50)<<30))
	writeStatusState(t, layout.StateDir, "traffic_total_limit_bytes", fmt.Sprintf("%d", uint64(100)<<30))
	writeStatusState(t, layout.StateDir, "reset_day", "7")
	writeStatusState(t, layout.StateDir, "reset_hour", "4")
	if err := os.MkdirAll(filepath.Dir(layout.SingBoxBin), 0o755); err != nil {
		t.Fatalf("mkdir sing-box dir: %v", err)
	}
	if err := os.WriteFile(layout.SingBoxBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write sing-box binary: %v", err)
	}

	oldLayout := defaultStatusLayout
	oldDetect := detectStatusHost
	oldOutput := statusCommandOutput
	oldNow := statusNow
	defer func() {
		defaultStatusLayout = oldLayout
		detectStatusHost = oldDetect
		statusCommandOutput = oldOutput
		statusNow = oldNow
	}()
	fixedTime := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	statusNow = func() time.Time { return fixedTime }
	defaultStatusLayout = func() paths.Layout { return layout }
	detectStatusHost = func() (system.Host, error) {
		host := supportedTestHost()
		host.OS.VersionID = "24.04"
		return host, nil
	}
	statusCommandOutput = func(name string, args ...string) (string, error) {
		if name == layout.SingBoxBin {
			return "sing-box version 1.12.0\n", nil
		}
		if name != "systemctl" || len(args) != 2 || args[0] != "is-active" {
			return "", fmt.Errorf("unexpected command: %s %v", name, args)
		}
		switch args[1] {
		case system.SingBoxService:
			return "active\n", nil
		case "nginx.service":
			return "inactive\n", fmt.Errorf("inactive")
		case system.MonitorService:
			return "failed\n", fmt.Errorf("failed")
		default:
			return "unknown\n", fmt.Errorf("unknown")
		}
	}

	status := loadStatus()
	if status.Domain != "example.com" || status.PublicIP != "203.0.113.10" {
		t.Fatalf("state fields not loaded: %#v", status)
	}
	if status.OSArch != "ubuntu 24.04/amd64" {
		t.Fatalf("OSArch = %q", status.OSArch)
	}
	if status.SingBoxVer != "sing-box version 1.12.0" {
		t.Fatalf("SingBoxVer = %q", status.SingBoxVer)
	}
	if status.SingBoxState != "running" || status.NginxState != "not running" || status.MonitorState != "not running" {
		t.Fatalf("service states = %#v", status)
	}
	if status.Protocols != "vless-reality-vision, tuic" {
		t.Fatalf("Protocols = %q", status.Protocols)
	}
	if len(status.Groups) != 2 {
		t.Fatalf("Groups = %#v", status.Groups)
	}
	first := status.Groups[0]
	token := deploy.SubscriptionToken("abcd1234deadbeef")
	if first.Alias != "Default" || first.Salt != "abcd1234deadbeef" || first.Members != "US-hub" {
		t.Fatalf("first group = %#v", first)
	}
	if first.Subscription != "https://example.com:2096/s/default/"+token {
		t.Fatalf("Subscription = %q", first.Subscription)
	}
	if first.ClashMetaSub != "https://example.com:2096/s/clashMetaProfiles/"+token {
		t.Fatalf("ClashMetaSub = %q", first.ClashMetaSub)
	}
	if first.SingBoxSub != "https://example.com:2096/s/singboxProfiles/"+token {
		t.Fatalf("SingBoxSub = %q", first.SingBoxSub)
	}
	if first.SurgeSub != "https://example.com:2096/s/surgeProfiles/"+token {
		t.Fatalf("SurgeSub = %q", first.SurgeSub)
	}
	// Each group publishes at its own token, never a shared one.
	if status.Groups[1].Subscription == first.Subscription {
		t.Fatalf("groups share a subscription URL: %q", first.Subscription)
	}
	if status.MonitorUI != "https://example.com:2097/monitor/" {
		t.Fatalf("MonitorUI = %q", status.MonitorUI)
	}
	if status.TrafficQuota != "in limit 40 GB, out limit 50 GB, total limit 100 GB, next reset 2026-07-07 04:00 GMT" {
		t.Fatalf("TrafficQuota = %q", status.TrafficQuota)
	}
}

func writeStatusState(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
		t.Fatalf("write state %s: %v", name, err)
	}
}

func protocolManagerState(t *testing.T, enabled, realitySNI string) paths.Layout {
	t.Helper()
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	values := map[string]string{
		"domain":                 "example.com",
		"display_name":           "US-vps1",
		"subscribe_salt":         "testsalt",
		"enabled_protocols":      enabled,
		"subscribe_port":         "2096",
		"monitor_public_port":    "2097",
		"monitor_port":           "19090",
		"monitor":                "no",
		"reality_server_name":    realitySNI,
		"reality_handshake_port": "443",
		"reality_private_key":    "private",
		"reality_public_key":     "public",
		"reality_short_id":       "0123456789abcdef",
		"reality_vision_uuid":    "11111111-1111-4111-8111-111111111111",
		"reality_grpc_uuid":      "22222222-2222-4222-8222-222222222222",
		"hysteria2_password":     "hypass",
		"tuic_uuid":              "33333333-3333-4333-8333-333333333333",
		"tuic_password":          "tuicpass",
		"anytls_password":        "anypass",
		"reality_vision_port":    "443",
		"reality_grpc_port":      "8443",
		"hysteria2_port":         "9443",
		"tuic_port":              "10443",
		"anytls_port":            "11443",
	}
	for name, value := range values {
		writeStatusState(t, layout.StateDir, name, value)
	}
	return layout
}

func withProtocolManagerDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := protocolUILayout
	oldDetect := detectProtocolHost
	oldUpdate := updateProtocolsRun
	oldRefresh := refreshProtocolSubscriptions
	oldFetchSpokeState := fetchSpokeProtocolState
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("generate spoke protocol credentials: %v", err)
	}
	t.Cleanup(func() {
		protocolUILayout = oldLayout
		detectProtocolHost = oldDetect
		updateProtocolsRun = oldUpdate
		refreshProtocolSubscriptions = oldRefresh
		fetchSpokeProtocolState = oldFetchSpokeState
	})
	protocolUILayout = func() paths.Layout { return layout }
	detectProtocolHost = func() (system.Host, error) { return supportedTestHost(), nil }
	fetchSpokeProtocolState = func(_ context.Context, node nodes.Node) (nodeapi.ProtocolStateResponse, error) {
		realityHandshakePort := node.RealityHandshakePort
		if realityHandshakePort <= 0 {
			realityHandshakePort = config.DefaultRealityHandshakePort
		}
		response := nodeapi.ProtocolStateResponse{
			Domain:               node.Domain,
			RealityServerName:    node.RealityServerName,
			RealityHandshakePort: realityHandshakePort,
			EnabledProtocols:     append([]string(nil), node.EnabledProtocols...),
			Ports: nodeapi.PortSet{
				RealityVision: node.RealityVisionPort,
				RealityGRPC:   node.RealityGRPCPort,
				Hysteria2:     node.Hysteria2Port,
				TUIC:          node.TUICPort,
				AnyTLS:        node.AnyTLSPort,
			},
			Credentials: nodeapi.ProtocolCredentials{
				RealityVisionUUID: creds.RealityVisionUUID,
				RealityGRPCUUID:   creds.RealityGRPCUUID,
				HysteriaPassword:  creds.HysteriaPassword,
				TUICUUID:          creds.TUICUUID,
				TUICPassword:      creds.TUICPassword,
				AnyTLSPassword:    creds.AnyTLSPassword,
				RealityPrivateKey: creds.RealityPrivateKey,
				RealityPublicKey:  creds.RealityPublicKey,
				RealityShortID:    creds.RealityShortID,
			},
		}
		revision, err := nodeapi.ProtocolStateRevision(response)
		if err != nil {
			return nodeapi.ProtocolStateResponse{}, err
		}
		response.Revision = revision
		return response, nil
	}
}

func withSubscriptionDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := subscriptionUILayout
	oldDetect := detectSubscriptionHost
	t.Cleanup(func() {
		subscriptionUILayout = oldLayout
		detectSubscriptionHost = oldDetect
	})
	subscriptionUILayout = func() paths.Layout { return layout }
	detectSubscriptionHost = func() (system.Host, error) { return supportedTestHost(), nil }
}

func withMonitorDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := monitorUILayout
	oldDetect := detectMonitorHost
	oldRun := updateMonitorRun
	t.Cleanup(func() {
		monitorUILayout = oldLayout
		detectMonitorHost = oldDetect
		updateMonitorRun = oldRun
	})
	monitorUILayout = func() paths.Layout { return layout }
	detectMonitorHost = func() (system.Host, error) { return supportedTestHost(), nil }
	updateMonitorRun = func(_ context.Context, opts monitor.UpdateOptions) (monitor.ManageConfig, error) {
		dcfg, err := deploy.LoadProtocolConfig(layout)
		if err != nil {
			return monitor.ManageConfig{}, err
		}
		return monitor.ManageConfig{
			Domain:                 dcfg.Domain,
			DeployMonitor:          dcfg.DeployMonitor,
			MonitorAlias:           dcfg.MonitorAlias,
			MonitorPublicPort:      dcfg.MonitorPublicPort,
			MonitorPort:            dcfg.MonitorPort,
			MonitorInterface:       dcfg.MonitorInterface,
			MonitorIntervalSeconds: dcfg.MonitorIntervalSeconds,
			TrafficInLimitBytes:    dcfg.TrafficInLimitBytes,
			TrafficOutLimitBytes:   dcfg.TrafficOutLimitBytes,
			TrafficTotalLimitBytes: dcfg.TrafficTotalLimitBytes,
			ResetDay:               dcfg.ResetDay,
			ResetHour:              dcfg.ResetHour,
			SubscribePort:          dcfg.SubscribePort,
		}, nil
	}
}

func withCoreDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := coreUILayout
	oldDetect := detectCoreHost
	oldVersion := coreCurrentVersion
	oldService := coreServiceSnapshot
	oldLogs := coreLogOutput
	oldRelease := coreReleaseClient
	oldFleetChange := changeFleetCoreRun
	t.Cleanup(func() {
		coreUILayout = oldLayout
		detectCoreHost = oldDetect
		coreCurrentVersion = oldVersion
		coreServiceSnapshot = oldService
		coreLogOutput = oldLogs
		coreReleaseClient = oldRelease
		changeFleetCoreRun = oldFleetChange
	})
	coreUILayout = func() paths.Layout { return layout }
	detectCoreHost = func() (system.Host, error) { return supportedTestHost(), nil }
	coreCurrentVersion = func(paths.Layout) string { return "sing-box version 1.12.0" }
	coreServiceSnapshot = func() string { return "running" }
	coreLogOutput = func(context.Context, int) (string, error) { return "log line\n", nil }
	changeFleetCoreRun = func(context.Context, paths.Layout, string, io.Writer, func(deploy.Event)) error {
		return nil
	}
}

func withUninstallDeps(t *testing.T, layout paths.Layout) {
	t.Helper()
	oldLayout := uninstallUILayout
	oldDetect := detectUninstallHost
	oldRun := uninstallRun
	t.Cleanup(func() {
		uninstallUILayout = oldLayout
		detectUninstallHost = oldDetect
		uninstallRun = oldRun
	})
	uninstallUILayout = func() paths.Layout { return layout }
	detectUninstallHost = func() (system.Host, error) { return supportedTestHost(), nil }
	uninstallRun = func(context.Context, uninstall.Options) error { return nil }
}

// subscriptionActionCursor finds the action list index of one action.
// Separators are skipped: they leave action at its zero value, which is also
// the first action constant.
func subscriptionActionCursor(t *testing.T, sm *subscriptionManager, action subscriptionAction) int {
	t.Helper()
	for i, item := range sm.actions() {
		if !item.separator && item.action == action {
			return i
		}
	}
	t.Fatalf("subscription action %v not found", action)
	return 0
}

func TestValidateInstallPortConflict(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		val     string
		vals    map[string]string
		wantErr bool
	}{
		{"protocol on 443", "tuic_port", "443", nil, true},
		{"protocol on 80", "hysteria2_port", "80", nil, true},
		{"duplicate protocol ports", "tuic_port", "9443", map[string]string{"hysteria2_port": "9443"}, true},
		{"protocol clashes with subscription", "tuic_port", "2096", map[string]string{"subscribe_port": "2096"}, true},
		{"distinct ports ok", "tuic_port", "10443", map[string]string{"hysteria2_port": "9443"}, false},
		{"subscription and monitor fold on 443", "subscribe_port", "443", map[string]string{"monitor_public_port": "443"}, false},
		{"empty is deferred", "tuic_port", "", map[string]string{"hysteria2_port": "443"}, false},
		{"non-port field ignored", "domain", "443", map[string]string{"subscribe_port": "443"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInstallPortConflict(tt.key, tt.val, tt.vals)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateInstallPortConflict() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestProtocolSelectBackResetsCursorToActionList(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, layout)

	pm := newProtocolManager()
	pm.setSize(100, 30)
	if pm.loadErr != nil {
		t.Fatalf("load protocol manager: %v", pm.loadErr)
	}
	// Enter the protocol select list and move the shared cursor past the end
	// of the (shorter) action list.
	pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pm.phase != protocolPhaseSelect {
		t.Fatalf("expected protocol select, phase=%v", pm.phase)
	}
	for i := 0; i < 4; i++ {
		pm.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	// Back to the action list must land on a valid, highlighted action.
	pm.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if pm.phase != protocolPhaseAction {
		t.Fatalf("expected action list, phase=%v", pm.phase)
	}
	if _, ok := selectedIndex(pm.cursor, len(pm.actions())); !ok {
		t.Fatalf("cursor %d is out of range for %d actions after Back", pm.cursor, len(pm.actions()))
	}
}

func TestSubscriptionRefreshBackReturnsToActionList(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	withSubscriptionDeps(t, layout)

	sm := newSubscriptionManager()
	sm.setSize(100, 30)
	if sm.loadErr != nil {
		t.Fatalf("load subscription manager: %v", sm.loadErr)
	}

	// Open a form action first, then abandon it back to the action list; its
	// fields linger on the embedded parameterForm.
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionDisplayName)
	sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if sm.phase != subscriptionPhaseForm {
		t.Fatalf("expected display-name form, phase=%v", sm.phase)
	}
	sm.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})

	// Now enter Refresh (which has no form) and press Back on its confirm page.
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionRefresh)
	sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if sm.phase != subscriptionPhaseConfirm {
		t.Fatalf("expected refresh confirm, phase=%v", sm.phase)
	}
	sm.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if sm.phase != subscriptionPhaseAction {
		t.Fatalf("Back from refresh confirm must return to the action list, got phase=%v", sm.phase)
	}
}

func TestRunningCompletionRequiresEnterBeforeSummary(t *testing.T) {
	w := &installFlow{phase: phaseRunning, run: commandRun{ch: make(chan runMsg, 1), bar: progressBarForTest()}}
	cmd := w.handleRun(runMsg{done: true})
	if cmd != nil {
		t.Fatalf("completion should not wait for another run message")
	}
	if w.phase != phaseRunning || !w.run.runComplete {
		t.Fatalf("completion should stay on running phase, phase=%v complete=%v", w.phase, w.run.runComplete)
	}
	if got := hintText(w.footerHints()...); !strings.Contains(got, "Enter: Show summary") {
		t.Fatalf("running footer missing enter summary hint: %s", got)
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || w.phase != phaseDone {
		t.Fatalf("enter should move to summary without closing, phase=%v done=%v", w.phase, done)
	}
}

func TestProtocolRunningCompletionRequiresEnterBeforeSummary(t *testing.T) {
	pm := &protocolManager{phase: protocolPhaseRunning, commandRun: commandRun{ch: make(chan runMsg, 1), bar: progressBarForTest()}}
	cmd := pm.handleRun(runMsg{done: true})
	if cmd != nil {
		t.Fatalf("completion should not wait for another protocol run message")
	}
	if pm.phase != protocolPhaseRunning || !pm.runComplete {
		t.Fatalf("completion should stay on running phase, phase=%v complete=%v", pm.phase, pm.runComplete)
	}
	if got := hintText(pm.footerHints()...); !strings.Contains(got, "Enter: Show summary") {
		t.Fatalf("protocol running footer missing enter summary hint: %s", got)
	}
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseDone {
		t.Fatalf("enter should move protocol manager to summary, phase=%v done=%v", pm.phase, done)
	}
}

func TestLogWriterSanitizesTerminalControlOutput(t *testing.T) {
	ch := make(chan runMsg, 4)
	lw := &logWriter{ch: ch}
	input := "Downloading 10%\r\x1b[2KDownloading 20%\n\x1b[?25lsecret\x1b[?25h\n"
	if _, err := lw.Write([]byte(input)); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	for i, want := range []string{"Downloading 10%", "Downloading 20%", "secret"} {
		msg := <-ch
		if msg.logLine != want {
			t.Fatalf("line %d = %q, want %q", i, msg.logLine, want)
		}
		if strings.ContainsAny(msg.logLine, "\x1b\r") {
			t.Fatalf("line %d still has terminal controls: %q", i, msg.logLine)
		}
	}
}

func TestRunningLogKeepsHistoryAndScrolls(t *testing.T) {
	w := &installFlow{phase: phaseRunning, run: commandRun{bar: progressBarForTest()}}
	w.setSize(80, 10)
	for i := 1; i <= 20; i++ {
		w.run.appendLog(fmt.Sprintf("line-%02d", i))
	}
	if len(w.run.logBuf) != 20 {
		t.Fatalf("log lines = %d, want 20", len(w.run.logBuf))
	}
	view := w.View()
	if !strings.Contains(view, "line-20") || strings.Contains(view, "line-01") {
		t.Fatalf("running view should start at latest logs:\n%s", view)
	}
	for range 20 {
		_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyUp})
		if done {
			t.Fatalf("scrolling log should not close install flow")
		}
	}
	view = w.View()
	if !strings.Contains(view, "line-01") || strings.Contains(view, "line-20") {
		t.Fatalf("running view should scroll to older logs:\n%s", view)
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if done {
		t.Fatalf("jumping to latest log should not close install flow")
	}
	view = w.View()
	if !strings.Contains(view, "line-20") {
		t.Fatalf("running view should jump back to latest logs:\n%s", view)
	}
}

func TestRunningViewFitsAssignedHeightWithWrappedLog(t *testing.T) {
	w := &installFlow{phase: phaseRunning, run: commandRun{bar: progressBarForTest()}}
	w.setSize(32, 10)
	w.run.appendLog(strings.Repeat("long-command ", 20))
	if got := lipgloss.Height(w.View()); got > w.form.height {
		t.Fatalf("running view height = %d, want <= %d:\n%s", got, w.form.height, w.View())
	}
}

func TestConfirmViewScrollsWithKeysAndMouse(t *testing.T) {
	w := confirmInstallFlowForTest()
	w.setSize(60, 8)
	if strings.Contains(w.View(), "anytls port") {
		t.Fatalf("confirm view should start at the top:\n%s", w.View())
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if done {
		t.Fatalf("scrolling confirm view should not close install flow")
	}
	if !strings.Contains(w.View(), "anytls port") {
		t.Fatalf("confirm view should scroll to the bottom:\n%s", w.View())
	}

	w.form.confirmScroll = 0
	_, done = w.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if done {
		t.Fatalf("mouse wheel should not close install flow")
	}
	if w.form.confirmScroll == 0 {
		t.Fatalf("mouse wheel down should scroll confirm view")
	}
	_, done = w.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if done {
		t.Fatalf("mouse wheel should not close install flow")
	}
	if w.form.confirmScroll != 0 {
		t.Fatalf("mouse wheel up should scroll back to top, got %d", w.form.confirmScroll)
	}
}

func progressBarForTest() progress.Model {
	return progress.New(progress.WithDefaultGradient())
}

func TestInstallFormCanGoBackToPreviousField(t *testing.T) {
	w := installFormForTest()
	w.startForm()
	w.input.SetValue("example.com")
	w.commitField()
	w.input.SetValue("admin@example.com")
	w.previousField()
	if w.fieldIx != 0 {
		t.Fatalf("fieldIx = %d, want 0", w.fieldIx)
	}
	if got := w.input.Value(); got != "example.com" {
		t.Fatalf("restored input = %q, want domain", got)
	}
}

func TestDomainValidationBlocksInvalidDomain(t *testing.T) {
	w := installFormForTest()
	w.validateDomain = func(_ context.Context, domain string) (string, error) {
		if domain != "bad.example" {
			t.Fatalf("validator domain = %q", domain)
		}
		return "", fmt.Errorf("domain resolves elsewhere")
	}
	w.startForm()
	w.input.SetValue("bad.example")
	w.commitField()

	if w.fieldIx != 0 {
		t.Fatalf("fieldIx = %d, want domain field", w.fieldIx)
	}
	if w.values["domain"] != "" {
		t.Fatalf("domain should not be committed on validation failure")
	}
	if !strings.Contains(w.View(), "domain resolves elsewhere") {
		t.Fatalf("validation error missing from view:\n%s", w.View())
	}
}

func TestDomainValidationCapturesPublicIP(t *testing.T) {
	w := installFormForTest()
	w.validateDomain = func(_ context.Context, domain string) (string, error) {
		if domain != "vpn.example.com" {
			t.Fatalf("validator domain = %q", domain)
		}
		return "203.0.113.10", nil
	}
	w.startForm()
	w.input.SetValue("vpn.example.com")
	w.commitField()

	if w.publicIP != "203.0.113.10" {
		t.Fatalf("captured public IP = %q", w.publicIP)
	}
	if w.values["domain"] != "vpn.example.com" {
		t.Fatalf("validated domain was not committed: %#v", w.values)
	}
}

func TestInstallFormSelectsSingleChoiceFields(t *testing.T) {
	w := installFormForTest()
	w.startForm()
	// domain → protocols (multi) → site_template (single choice).
	w.input.SetValue("example.com")
	w.commitField()
	w.setField(fieldIndex(t, w.fields, "site_template"))
	if !w.currentFieldHasOptions() || w.currentFieldIsMulti() {
		t.Fatalf("site_template should render as a single-choice selection")
	}
	if !strings.Contains(w.View(), "> "+deploy.DefaultSiteTemplate) {
		t.Fatalf("single-choice field should highlight the default:\n%s", w.View())
	}
}

func TestProtocolMultiSelectRequiresAtLeastOne(t *testing.T) {
	w := installFormForTest()
	w.startForm()
	w.setField(fieldIndex(t, w.fields, "protocols"))
	for _, opt := range w.fields[w.fieldIx].options {
		w.optionIx = optionIndex(w.fields[w.fieldIx].options, opt)
		w.toggleOption()
	}
	w.commitField()

	if w.values["protocols"] != "" {
		t.Fatalf("protocols should not commit when none selected")
	}
	if !strings.Contains(w.View(), "select at least one protocol") {
		t.Fatalf("missing validation error:\n%s", w.View())
	}
}

func TestRealityFieldsHiddenWhenRealityNotSelected(t *testing.T) {
	vals := map[string]string{"protocols": string(config.ProtocolTUIC)}
	fields := installFields()
	reality := fields[fieldIndex(t, fields, "reality_sni")]
	if reality.skip == nil || !reality.skip(vals) {
		t.Fatalf("reality field should be hidden when no reality protocol is selected")
	}
	tuic := fields[fieldIndex(t, fields, "tuic_uuid")]
	if tuic.skip != nil && tuic.skip(vals) {
		t.Fatalf("tuic field should be visible when tuic is selected")
	}
	tuicPassword := fields[fieldIndex(t, fields, "tuic_password")]
	if tuicPassword.skip != nil && tuicPassword.skip(vals) {
		t.Fatalf("tuic password field should be visible when tuic is selected")
	}
}

func TestMonitorFieldsHiddenWhenDisabled(t *testing.T) {
	vals := map[string]string{"monitor": "no"}
	fields := installFields()
	for _, key := range []string{"monitor_alias", "monitor_token", "monitor_public_port", "monitor_port", "monitor_interval_seconds", "traffic_in_limit", "traffic_out_limit", "traffic_total_limit", "reset_day", "reset_hour"} {
		f := fields[fieldIndex(t, fields, key)]
		if f.skip == nil || !f.skip(vals) {
			t.Fatalf("%s should be hidden when monitor is disabled", key)
		}
	}
}

func TestProtocolParameterViewShowsCurrentProtocol(t *testing.T) {
	w := installFormWithValuesForTest(map[string]string{"protocols": string(config.ProtocolRealityVision)})
	w.width = 80
	w.setField(fieldIndex(t, w.fields, "reality_vision_uuid"))
	view := w.View()
	if !strings.Contains(view, "Setting parameters for: vless-reality-vision") {
		t.Fatalf("current protocol marker missing:\n%s", view)
	}
}

func TestInstallFieldsIncludeSiteTemplates(t *testing.T) {
	fields := installFields()
	field := fields[fieldIndex(t, fields, "site_template")]
	if field.def != deploy.DefaultSiteTemplate {
		t.Fatalf("site template default = %q", field.def)
	}
	if strings.Join(field.options, ",") != strings.Join(deploy.SiteTemplateOptions(), ",") {
		t.Fatalf("site template options = %#v", field.options)
	}
}

func TestBuildConfigRejectsInvalidSiteTemplate(t *testing.T) {
	w := &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":        "example.com",
			"protocols":     "tuic",
			"display_name":  "Node",
			"site_template": "unknown",
			"monitor":       "no",
		}),
		host: supportedTestHost(),
	}
	_, err := w.buildConfig()
	if err == nil || !strings.Contains(err.Error(), "unsupported masquerade site template") {
		t.Fatalf("expected invalid site template error, got %v", err)
	}
}

func TestSummaryHighlightsRandomValues(t *testing.T) {
	w := confirmInstallFlowForTest()
	summary := w.form.summary(w.host)
	highlightedRandom := flowRandom.Render("random")
	if !strings.Contains(summary, highlightedRandom) {
		t.Fatalf("summary should include highlighted random value:\n%s", summary)
	}
}

func TestSummaryRendererAlignsAndHighlightsTokens(t *testing.T) {
	summary := renderSummary([]summaryLine{
		summaryRow("Short", "running"),
		summaryRow("Longer label", "sing-box version 1.12.0"),
		summaryRow("Certificate", "valid until 2026-06-04"),
		summaryRow("Traffic", "in unlimited, next reset 2026-07-01 00:00 GMT"),
	})
	if !strings.Contains(summary, dimStyle.Render("Short:       ")) {
		t.Fatalf("summary labels should align:\n%s", summary)
	}
	for _, want := range []string{
		statusOK.Render("running"),
		summaryInfo.Render("1.12.0"),
		summaryDate.Render("2026-06-04"),
		summaryDate.Render("2026-07-01 00:00"),
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing highlighted token %q:\n%s", want, summary)
		}
	}
	if got := highlightSummaryText("203.0.113.10"); got != "203.0.113.10" {
		t.Fatalf("IP address should not be highlighted: %q", got)
	}
}

func TestProtocolLabelsAddSpacesForDisplay(t *testing.T) {
	protocols := []config.Protocol{config.ProtocolRealityVision, config.ProtocolTUIC}
	if got := protocolSelectionValue(protocols); got != "vless-reality-vision,tuic" {
		t.Fatalf("protocolSelectionValue = %q", got)
	}
	if got := protocolLabels(protocols); got != "vless-reality-vision, tuic" {
		t.Fatalf("protocolLabels = %q", got)
	}
}

func TestBuildConfigUsesSelectedProtocolParameters(t *testing.T) {
	w := &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":                   "example.com",
			"protocols":                "vless-reality-vision,tuic",
			"reality_sni":              "https://www.cloudflare.com/cdn-cgi/trace",
			"reality_vision_uuid":      "11111111-1111-4111-8111-111111111111",
			"reality_vision_port":      "24443",
			"tuic_uuid":                "22222222-2222-4222-8222-222222222222",
			"tuic_password":            "tuic-secret",
			"tuic_port":                "24444",
			"display_name":             "Node",
			"site_template":            "forty",
			"subscribe_port":           "24445",
			"subscribe_salt":           "abc",
			"monitor":                  "yes",
			"monitor_alias":            "JP-local",
			"monitor_public_port":      "24447",
			"monitor_port":             "24446",
			"monitor_interval_seconds": "60",
			"traffic_in_limit":         "40GB",
			"traffic_out_limit":        "50GB",
			"traffic_total_limit":      "100GB",
			"reset_day":                "1",
			"reset_hour":               "5",
		}),
		host: supportedTestHost(),
	}
	w.form.publicIP = "203.0.113.10"
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}

	if got := protocolSelectionValue(cfg.Enabled); got != "vless-reality-vision,tuic" {
		t.Fatalf("enabled = %q", got)
	}
	if cfg.RealityServerName != "www.cloudflare.com" {
		t.Fatalf("RealityServerName = %q", cfg.RealityServerName)
	}
	if cfg.Ports.RealityVision != 24443 || cfg.Ports.TUIC != 24444 {
		t.Fatalf("ports = %#v", cfg.Ports)
	}
	if cfg.SubscribePort != 24445 || cfg.MonitorPublicPort != 24447 || cfg.MonitorPort != 24446 {
		t.Fatalf("managed ports = subscribe %d monitor public %d monitor local %d", cfg.SubscribePort, cfg.MonitorPublicPort, cfg.MonitorPort)
	}
	if cfg.Salt != "abc" {
		t.Fatalf("Salt = %q", cfg.Salt)
	}
	if cfg.PublicIP != "203.0.113.10" {
		t.Fatalf("PublicIP = %q", cfg.PublicIP)
	}
	if cfg.SiteTemplate != "forty" {
		t.Fatalf("SiteTemplate = %q", cfg.SiteTemplate)
	}
	if cfg.Creds.RealityVisionUUID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("RealityVisionUUID = %q", cfg.Creds.RealityVisionUUID)
	}
	if cfg.Creds.TUICUUID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("TUICUUID = %q", cfg.Creds.TUICUUID)
	}
	if cfg.Creds.TUICPassword != "tuic-secret" {
		t.Fatalf("TUICPassword = %q", cfg.Creds.TUICPassword)
	}
	if cfg.Ports.Hysteria2 != 0 || cfg.Ports.AnyTLS != 0 {
		t.Fatalf("unselected protocol ports should stay zero: %#v", cfg.Ports)
	}
	if !cfg.DeployMonitor || cfg.TrafficInLimitBytes != 40<<30 || cfg.TrafficOutLimitBytes != 50<<30 || cfg.TrafficTotalLimitBytes != 100<<30 {
		t.Fatalf("monitor config = enabled %v in %d out %d total %d", cfg.DeployMonitor, cfg.TrafficInLimitBytes, cfg.TrafficOutLimitBytes, cfg.TrafficTotalLimitBytes)
	}
	if cfg.MonitorAlias != "JP-local" || cfg.ResetHour != 5 || cfg.MonitorIntervalSeconds != 60 {
		t.Fatalf("monitor alias/reset/interval = %q/%d/%d", cfg.MonitorAlias, cfg.ResetHour, cfg.MonitorIntervalSeconds)
	}
}

func TestBuildConfigRejectsManagedPortConflicts(t *testing.T) {
	w := &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":         "example.com",
			"protocols":      "tuic",
			"tuic_port":      "24444",
			"display_name":   "Node",
			"subscribe_port": "24444",
			"monitor":        "no",
		}),
		host: supportedTestHost(),
	}
	_, err := w.buildConfig()
	if err == nil || !strings.Contains(err.Error(), "tuic port 24444 conflicts") {
		t.Fatalf("expected subscribe/protocol port conflict, got %v", err)
	}

	w.form.values["tuic_port"] = "24445"
	w.form.values["monitor"] = "yes"
	w.form.values["monitor_public_port"] = "24444"
	w.form.values["monitor_port"] = "24446"
	_, err = w.buildConfig()
	if err == nil || !strings.Contains(err.Error(), "monitor public port 24444 conflicts") {
		t.Fatalf("expected subscribe/monitor port conflict, got %v", err)
	}

	w.form.values["monitor_public_port"] = "24446"
	w.form.values["monitor_port"] = "24444"
	_, err = w.buildConfig()
	if err == nil || !strings.Contains(err.Error(), "monitor service port 24444 conflicts") {
		t.Fatalf("expected subscribe/monitor local port conflict, got %v", err)
	}
}

func TestBuildConfigRandomizesBlankSelectedPorts(t *testing.T) {
	w := &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":              "example.com",
			"protocols":           "hysteria2,anytls",
			"display_name":        "Node",
			"monitor":             "yes",
			"traffic_in_limit":    "0",
			"traffic_out_limit":   "0",
			"traffic_total_limit": "0",
			"reset_day":           "1",
		}),
		host: supportedTestHost(),
	}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}

	if got := protocolSelectionValue(cfg.Enabled); got != "hysteria2,anytls" {
		t.Fatalf("enabled = %q", got)
	}
	if cfg.SubscribePort != deploy.DefaultSubscribePort || cfg.MonitorPublicPort != deploy.DefaultMonitorPublicPort || cfg.MonitorPort != deploy.DefaultMonitorPort {
		t.Fatalf("default managed ports = subscribe %d monitor public %d monitor local %d", cfg.SubscribePort, cfg.MonitorPublicPort, cfg.MonitorPort)
	}
	if cfg.Salt == "" {
		t.Fatalf("blank subscription salt should generate a random salt")
	}
	if cfg.RealityServerName != "" {
		t.Fatalf("RealityServerName should be empty without reality, got %q", cfg.RealityServerName)
	}
	if cfg.Ports.Hysteria2 < 20000 || cfg.Ports.Hysteria2 > 59999 {
		t.Fatalf("Hysteria2 random port out of range: %d", cfg.Ports.Hysteria2)
	}
	if cfg.Ports.AnyTLS < 20000 || cfg.Ports.AnyTLS > 59999 {
		t.Fatalf("AnyTLS random port out of range: %d", cfg.Ports.AnyTLS)
	}
	if cfg.Ports.Hysteria2 == cfg.Ports.AnyTLS {
		t.Fatalf("random ports should be unique: %#v", cfg.Ports)
	}
}

func TestBuildConfigDisablesMonitor(t *testing.T) {
	w := &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":              "example.com",
			"protocols":           "tuic",
			"tuic_uuid":           "22222222-2222-4222-8222-222222222222",
			"display_name":        "Node",
			"monitor":             "no",
			"traffic_in_limit":    "40GB",
			"traffic_out_limit":   "50GB",
			"traffic_total_limit": "100GB",
		}),
		host: supportedTestHost(),
	}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if cfg.DeployMonitor {
		t.Fatalf("DeployMonitor should be false")
	}
	if cfg.MonitorInterface != "" || cfg.TrafficInLimitBytes != 0 || cfg.TrafficOutLimitBytes != 0 || cfg.TrafficTotalLimitBytes != 0 {
		t.Fatalf("monitor fields should stay empty when disabled: %#v", cfg)
	}
}

func fieldIndex(t *testing.T, fields []field, key string) int {
	t.Helper()
	for i, f := range fields {
		if f.key == key {
			return i
		}
	}
	t.Fatalf("field %q not found", key)
	return -1
}

// Setup never leaves a monitor ungated by accident: a blank answer generates a
// token the same way a blank protocol credential generates one.
func TestBuildConfigGeneratesMonitorTokenWhenBlank(t *testing.T) {
	w := installFlowWithMonitorToken(t, "")
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if err := uiparams.ValidateMonitorToken(cfg.MonitorToken); err != nil || cfg.MonitorToken == "" {
		t.Fatalf("generated monitor token = %q (%v)", cfg.MonitorToken, err)
	}
}

func TestBuildConfigKeepsTypedMonitorToken(t *testing.T) {
	w := installFlowWithMonitorToken(t, "s3cret-monitor-token")
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if cfg.MonitorToken != "s3cret-monitor-token" {
		t.Fatalf("MonitorToken = %q", cfg.MonitorToken)
	}
}

// The sentinel is the deliberate way to publish an ungated dashboard, so it
// must not be replaced by a generated token.
func TestBuildConfigHonoursMonitorTokenNone(t *testing.T) {
	w := installFlowWithMonitorToken(t, uiparams.MonitorTokenNone)
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}
	if cfg.MonitorToken != "" {
		t.Fatalf("MonitorToken = %q, want empty", cfg.MonitorToken)
	}
}

func installFlowWithMonitorToken(t *testing.T, token string) *installFlow {
	t.Helper()
	return &installFlow{
		form: installFormWithValuesForTest(map[string]string{
			"domain":                   "example.com",
			"protocols":                "tuic",
			"tuic_uuid":                "22222222-2222-4222-8222-222222222222",
			"tuic_password":            "tuic-secret",
			"tuic_port":                "24444",
			"display_name":             "Node",
			"site_template":            "forty",
			"subscribe_port":           "24445",
			"subscribe_salt":           "abc",
			"monitor":                  "yes",
			"monitor_alias":            "JP-local",
			"monitor_token":            token,
			"monitor_public_port":      "24447",
			"monitor_port":             "24446",
			"monitor_interval_seconds": "60",
			"reset_day":                "1",
			"reset_hour":               "5",
		}),
		host: supportedTestHost(),
	}
}

// A generated token is only readable from Status, so the screen that hands over
// the dashboard URL has to hand over the token with it.
func TestInstallDoneSummaryPrintsTheMonitorToken(t *testing.T) {
	w := &installFlow{cfg: deploy.Config{
		DisplayName:           "Node",
		Domain:                "example.com",
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
		MonitorPublicPort:     2087,
		MonitorToken:          "s3cr3t-token-value",
	}}
	summary := w.doneSummary()
	if !strings.Contains(summary, "s3cr3t-token-value") {
		t.Fatalf("done summary hides the monitor token:\n%s", summary)
	}
	if !strings.Contains(summary, "/monitor/") {
		t.Fatalf("done summary lost the monitor URL:\n%s", summary)
	}

	w.cfg.MonitorToken = ""
	if open := w.doneSummary(); !strings.Contains(open, "without a gate") {
		t.Fatalf("done summary does not state an ungated dashboard:\n%s", open)
	}
}
