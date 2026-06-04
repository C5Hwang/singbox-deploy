package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestProgressPercent(t *testing.T) {
	p := Progress{Current: 4, Total: 10, Label: "Install sing-box"}
	if p.Percent() != 0.4 {
		t.Fatalf("Percent = %v", p.Percent())
	}
	if p.Title() != "4/10 Install sing-box" {
		t.Fatalf("Title = %q", p.Title())
	}
}

func TestProgressZeroTotal(t *testing.T) {
	p := Progress{Current: 0, Total: 0, Label: "Init"}
	if p.Percent() != 0 {
		t.Fatalf("Percent = %v", p.Percent())
	}
}

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

func TestInstallStepsLabeled(t *testing.T) {
	steps := InstallSteps()
	if len(steps) == 0 {
		t.Fatalf("expected install steps")
	}
	if steps[0].Label == "" {
		t.Fatalf("first step has no label")
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

func TestInstallViewKeepsMenuVisible(t *testing.T) {
	m := NewModel()
	m.SetSize(120, 40)
	m.wizard = &wizard{phase: phasePreflight, hosts: "ready", host: supportedTestHost()}
	view := m.View()
	if !strings.Contains(view, "Menu") {
		t.Fatalf("install view should keep menu and wizard visible:\n%s", view)
	}
}

func TestWideInstallContentUsesAvailableHeightAndMenuAdapts(t *testing.T) {
	m := NewModel()
	m.SetSize(160, 60)
	w := &wizard{phase: phaseRunning, bar: progressBarForTest()}
	m.wizard = w
	view := m.View()
	if got := lipgloss.Height(view); got != 60 {
		t.Fatalf("view height = %d, want 60:\n%s", got, view)
	}
	if want := 60 - 1 - panelStyle.GetVerticalFrameSize(); w.height != want {
		t.Fatalf("wizard height = %d, want available content height %d", w.height, want)
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
	m.wizard = confirmWizardForTest()
	view := m.View()
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("view height = %d, want 12:\n%s", got, view)
	}
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[len(lines)-1], "enter install") {
		t.Fatalf("footer should stay on final row:\n%s", view)
	}
}

func supportedTestHost() system.Host {
	return system.Host{OS: system.OSRelease{Family: system.FamilyDebian, ID: "ubuntu"}, Arch: "amd64", IsRoot: true}
}

func confirmWizardForTest() *wizard {
	return &wizard{
		phase: phaseConfirm,
		values: map[string]string{
			"domain":                 "example.com",
			"challenge":              "http-01",
			"protocols":              defaultProtocolValue(),
			"reality_sni":            "www.microsoft.com",
			"display_name":           "Node",
			"traffic_monitor":        "yes",
			"traffic_in_limit_gb":    "0",
			"traffic_out_limit_gb":   "0",
			"traffic_total_limit_gb": "0",
		},
		host: supportedTestHost(),
	}
}

func TestInstallFieldShowsUsageNote(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New(), width: 80}
	w.startForm()
	view := w.View()
	if !strings.Contains(view, "Used for certificate issuance") {
		t.Fatalf("field usage note missing:\n%s", view)
	}
}

func TestProtocolManagementMenuOpens(t *testing.T) {
	layout := protocolManagerState(t, "reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, layout)
	m := NewModel()
	m.cursor = 1
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.protocols == nil {
		t.Fatalf("protocol manager was not opened")
	}
	view := m.View()
	if !strings.Contains(view, "Protocol Management") || !strings.Contains(view, "Current:") || !strings.Contains(view, "reality-vision") {
		t.Fatalf("protocol manager view missing expected content:\n%s", view)
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
	if !strings.Contains(pm.View(), "Reality URL/SNI") {
		t.Fatalf("missing Reality SNI prompt:\n%s", pm.View())
	}
}

func TestProtocolManagementEditProtocolShowsCredentialAndPortFields(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	pm.setSize(100, 30)
	pm.cursor = 1
	_, done := pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseEditPick {
		t.Fatalf("enter should open edit picker, phase=%v done=%v", pm.phase, done)
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || pm.phase != protocolPhaseForm {
		t.Fatalf("enter should open edit form, phase=%v done=%v", pm.phase, done)
	}
	view := pm.View()
	if !strings.Contains(view, "Hysteria2 password") || !strings.Contains(view, "default: hypass") {
		t.Fatalf("missing password edit field:\n%s", view)
	}
	_, done = pm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || !strings.Contains(pm.View(), "Hysteria2 port") || !strings.Contains(pm.View(), "default: 9443") {
		t.Fatalf("missing port edit field:\n%s", pm.View())
	}
}

func TestProtocolManagementRealitySNIEntryOnlyForRealityProtocols(t *testing.T) {
	layout := protocolManagerState(t, "hysteria2", "")
	withProtocolManagerDeps(t, layout)
	pm := newProtocolManager()
	if strings.Contains(pm.View(), "Edit Reality SNI") {
		t.Fatalf("Reality SNI entry should be hidden without Reality protocols:\n%s", pm.View())
	}

	realityLayout := protocolManagerState(t, "reality-vision", "www.microsoft.com")
	withProtocolManagerDeps(t, realityLayout)
	pm = newProtocolManager()
	view := pm.View()
	if !strings.Contains(view, "Edit Reality SNI") {
		t.Fatalf("Reality SNI entry missing for Reality protocols:\n%s", view)
	}
	pm.cursor = 2
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
	writeStatusState(t, layout.StateDir, "subscribe_token", "tok")
	writeStatusState(t, layout.StateDir, "enabled_protocols", "reality-vision,tuic")
	writeStatusState(t, layout.StateDir, "traffic_monitor", "yes")
	writeStatusState(t, layout.StateDir, "traffic_in_limit_bytes", fmt.Sprintf("%d", uint64(40)<<30))
	writeStatusState(t, layout.StateDir, "traffic_out_limit_bytes", fmt.Sprintf("%d", uint64(50)<<30))
	writeStatusState(t, layout.StateDir, "traffic_total_limit_bytes", fmt.Sprintf("%d", uint64(100)<<30))
	writeStatusState(t, layout.StateDir, "reset_day", "7")
	if err := os.MkdirAll(filepath.Dir(layout.SingBoxBin), 0o755); err != nil {
		t.Fatalf("mkdir sing-box dir: %v", err)
	}
	if err := os.WriteFile(layout.SingBoxBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write sing-box binary: %v", err)
	}

	oldLayout := defaultStatusLayout
	oldDetect := detectStatusHost
	oldOutput := statusCommandOutput
	defer func() {
		defaultStatusLayout = oldLayout
		detectStatusHost = oldDetect
		statusCommandOutput = oldOutput
	}()
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
	if status.Protocols != "reality-vision,tuic" {
		t.Fatalf("Protocols = %q", status.Protocols)
	}
	if status.Subscription != "https://example.com:2096/s/default/tok" {
		t.Fatalf("Subscription = %q", status.Subscription)
	}
	if status.ClashMetaSub != "https://example.com:2096/s/clashMetaProfiles/tok" {
		t.Fatalf("ClashMetaSub = %q", status.ClashMetaSub)
	}
	if status.SingBoxSub != "https://example.com:2096/s/sing-box/tok" {
		t.Fatalf("SingBoxSub = %q", status.SingBoxSub)
	}
	if status.TrafficQuota != "in limit 40 GB, out limit 50 GB, total limit 100 GB, reset day 7" {
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
		"monitor_port":           "19090",
		"traffic_monitor":        "no",
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
	t.Cleanup(func() {
		protocolUILayout = oldLayout
		detectProtocolHost = oldDetect
	})
	protocolUILayout = func() paths.Layout { return layout }
	detectProtocolHost = func() (system.Host, error) { return supportedTestHost(), nil }
}

func TestRunningStatusLevelClassifiesColors(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  statusLevel
	}{
		{value: "running", want: statusLevelRunning},
		{value: "active", want: statusLevelRunning},
		{value: "not running", want: statusLevelStopped},
		{value: "failed", want: statusLevelStopped},
		{value: "unknown", want: statusLevelUnknown},
	} {
		if got := runningStatusLevel(tc.value); got != tc.want {
			t.Fatalf("runningStatusLevel(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestRunningCompletionRequiresEnterBeforeSummary(t *testing.T) {
	w := &wizard{phase: phaseRunning, ch: make(chan runMsg, 1), bar: progressBarForTest()}
	cmd := w.handleRun(runMsg{done: true})
	if cmd != nil {
		t.Fatalf("completion should not wait for another run message")
	}
	if w.phase != phaseRunning || !w.runComplete {
		t.Fatalf("completion should stay on running phase, phase=%v complete=%v", w.phase, w.runComplete)
	}
	if !strings.Contains(w.View(), "press enter to show summary") {
		t.Fatalf("running view missing enter summary prompt:\n%s", w.View())
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done || w.phase != phaseDone {
		t.Fatalf("enter should move to summary without closing, phase=%v done=%v", w.phase, done)
	}
}

func TestProtocolRunningCompletionRequiresEnterBeforeSummary(t *testing.T) {
	pm := &protocolManager{phase: protocolPhaseRunning, ch: make(chan runMsg, 1), bar: progressBarForTest()}
	cmd := pm.handleRun(runMsg{done: true})
	if cmd != nil {
		t.Fatalf("completion should not wait for another protocol run message")
	}
	if pm.phase != protocolPhaseRunning || !pm.runComplete {
		t.Fatalf("completion should stay on running phase, phase=%v complete=%v", pm.phase, pm.runComplete)
	}
	if !strings.Contains(pm.View(), "press enter to show summary") {
		t.Fatalf("protocol running view missing enter summary prompt:\n%s", pm.View())
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
	w := &wizard{phase: phaseRunning, height: 10, bar: progressBarForTest()}
	for i := 1; i <= 20; i++ {
		w.appendLog(fmt.Sprintf("line-%02d", i))
	}
	if len(w.logBuf) != 20 {
		t.Fatalf("log lines = %d, want 20", len(w.logBuf))
	}
	view := w.View()
	if !strings.Contains(view, "line-20") || strings.Contains(view, "line-01") {
		t.Fatalf("running view should start at latest logs:\n%s", view)
	}
	for range 20 {
		_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyUp})
		if done {
			t.Fatalf("scrolling log should not close wizard")
		}
	}
	view = w.View()
	if !strings.Contains(view, "line-01") || strings.Contains(view, "line-20") {
		t.Fatalf("running view should scroll to older logs:\n%s", view)
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if done {
		t.Fatalf("jumping to latest log should not close wizard")
	}
	view = w.View()
	if !strings.Contains(view, "line-20") {
		t.Fatalf("running view should jump back to latest logs:\n%s", view)
	}
}

func TestRunningViewFitsAssignedHeightWithWrappedLog(t *testing.T) {
	w := &wizard{phase: phaseRunning, bar: progressBarForTest()}
	w.setSize(32, 10)
	w.appendLog(strings.Repeat("long-command ", 20))
	if got := lipgloss.Height(w.View()); got > w.height {
		t.Fatalf("running view height = %d, want <= %d:\n%s", got, w.height, w.View())
	}
}

func TestConfirmViewScrollsWithKeysAndMouse(t *testing.T) {
	w := confirmWizardForTest()
	w.setSize(60, 8)
	if strings.Contains(w.View(), "anytls port") {
		t.Fatalf("confirm view should start at the top:\n%s", w.View())
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if done {
		t.Fatalf("scrolling confirm view should not close wizard")
	}
	if !strings.Contains(w.View(), "anytls port") {
		t.Fatalf("confirm view should scroll to the bottom:\n%s", w.View())
	}

	w.confirmScroll = 0
	_, done = w.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if done {
		t.Fatalf("mouse wheel should not close wizard")
	}
	if w.confirmScroll == 0 {
		t.Fatalf("mouse wheel down should scroll confirm view")
	}
	_, done = w.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if done {
		t.Fatalf("mouse wheel should not close wizard")
	}
	if w.confirmScroll != 0 {
		t.Fatalf("mouse wheel up should scroll back to top, got %d", w.confirmScroll)
	}
}

func progressBarForTest() progress.Model {
	return progress.New(progress.WithDefaultGradient())
}

func TestInstallFormCanGoBackToPreviousField(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New()}
	w.startForm()
	w.input.SetValue("example.com")
	w.commitField()
	w.input.SetValue("admin@example.com")
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if done {
		t.Fatalf("back should not close wizard")
	}
	if w.fieldIx != 0 {
		t.Fatalf("fieldIx = %d, want 0", w.fieldIx)
	}
	if got := w.input.Value(); got != "example.com" {
		t.Fatalf("restored input = %q, want domain", got)
	}
}

func TestDomainValidationBlocksInvalidDomain(t *testing.T) {
	w := &wizard{
		phase:  phaseForm,
		fields: installFields(),
		values: map[string]string{},
		input:  textinput.New(),
		validateDomain: func(_ context.Context, domain string) error {
			if domain != "bad.example" {
				t.Fatalf("validator domain = %q", domain)
			}
			return fmt.Errorf("domain resolves elsewhere")
		},
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

func TestInstallFormSelectsSingleChoiceFields(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New()}
	w.startForm()
	w.input.SetValue("example.com")
	w.commitField()
	w.commitField()

	if got := w.fields[w.fieldIx].key; got != "challenge" {
		t.Fatalf("current field = %q, want challenge", got)
	}
	if !strings.Contains(w.View(), "> http-01") {
		t.Fatalf("challenge should render as a selection:\n%s", w.View())
	}
	_, done := w.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if done {
		t.Fatalf("moving selection should not close wizard")
	}
	w.commitField()
	if got := w.values["challenge"]; got != "dns-01" {
		t.Fatalf("challenge = %q, want dns-01", got)
	}
	if got := w.fields[w.fieldIx].key; got != "dns_provider" {
		t.Fatalf("current field = %q, want dns_provider", got)
	}
}

func TestDNSCredentialNoteMatchesSelectedProvider(t *testing.T) {
	fields := installFields()
	for _, tc := range []struct {
		provider string
		want     string
		link     string
		avoid    string
	}{
		{provider: "cloudflare", want: "Cloudflare uses an API token.", link: "https://dash.cloudflare.com/profile/api-tokens", avoid: "Aliyun uses"},
		{provider: "aliyun", want: "Aliyun uses accessKey:secretKey", link: "https://ram.console.aliyun.com/manage/ak", avoid: "Cloudflare uses"},
	} {
		w := &wizard{
			phase:  phaseForm,
			fields: fields,
			values: map[string]string{"dns_provider": tc.provider},
			input:  textinput.New(),
			width:  100,
		}
		w.setField(fieldIndex(t, fields, "dns_credential"))
		view := w.View()
		if !strings.Contains(view, tc.want) || !strings.Contains(view, tc.link) {
			t.Fatalf("%s note missing provider text or link:\n%s", tc.provider, view)
		}
		if !strings.Contains(view, "You can apply at "+tc.link) {
			t.Fatalf("%s note should use application hint format:\n%s", tc.provider, view)
		}
		if strings.Contains(view, tc.avoid) {
			t.Fatalf("%s note should not include other provider text:\n%s", tc.provider, view)
		}
	}
}

func TestProtocolMultiSelectRequiresAtLeastOne(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New()}
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

func TestTrafficMonitorFieldsHiddenWhenDisabled(t *testing.T) {
	vals := map[string]string{"traffic_monitor": "no"}
	fields := installFields()
	for _, key := range []string{"traffic_in_limit_gb", "traffic_out_limit_gb", "traffic_total_limit_gb", "reset_day"} {
		f := fields[fieldIndex(t, fields, key)]
		if f.skip == nil || !f.skip(vals) {
			t.Fatalf("%s should be hidden when traffic monitor is disabled", key)
		}
	}
}

func TestProtocolParameterViewShowsCurrentProtocol(t *testing.T) {
	w := &wizard{
		phase:  phaseForm,
		fields: installFields(),
		values: map[string]string{"protocols": string(config.ProtocolRealityVision)},
		input:  textinput.New(),
		width:  80,
	}
	w.setField(fieldIndex(t, w.fields, "reality_vision_uuid"))
	view := w.View()
	if !strings.Contains(view, "Setting parameters for: reality-vision") {
		t.Fatalf("current protocol marker missing:\n%s", view)
	}
}

func TestBuildConfigUsesSelectedProtocolParameters(t *testing.T) {
	w := &wizard{
		values: map[string]string{
			"domain":                 "example.com",
			"challenge":              "http-01",
			"protocols":              "reality-vision,tuic",
			"reality_sni":            "https://www.cloudflare.com/cdn-cgi/trace",
			"reality_vision_uuid":    "11111111-1111-4111-8111-111111111111",
			"reality_vision_port":    "24443",
			"tuic_uuid":              "22222222-2222-4222-8222-222222222222",
			"tuic_password":          "tuic-secret",
			"tuic_port":              "24444",
			"display_name":           "Node",
			"traffic_monitor":        "yes",
			"traffic_in_limit_gb":    "40",
			"traffic_out_limit_gb":   "50",
			"traffic_total_limit_gb": "100",
			"reset_day":              "1",
		},
		host: supportedTestHost(),
	}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}

	if got := protocolsValue(cfg.Enabled); got != "reality-vision,tuic" {
		t.Fatalf("enabled = %q", got)
	}
	if cfg.RealityServerName != "www.cloudflare.com" {
		t.Fatalf("RealityServerName = %q", cfg.RealityServerName)
	}
	if cfg.Ports.RealityVision != 24443 || cfg.Ports.TUIC != 24444 {
		t.Fatalf("ports = %#v", cfg.Ports)
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
		t.Fatalf("traffic monitor config = enabled %v in %d out %d total %d", cfg.DeployMonitor, cfg.TrafficInLimitBytes, cfg.TrafficOutLimitBytes, cfg.TrafficTotalLimitBytes)
	}
}

func TestBuildConfigRandomizesBlankSelectedPorts(t *testing.T) {
	w := &wizard{
		values: map[string]string{
			"domain":                 "example.com",
			"challenge":              "http-01",
			"protocols":              "hysteria2,anytls",
			"display_name":           "Node",
			"traffic_monitor":        "yes",
			"traffic_in_limit_gb":    "0",
			"traffic_out_limit_gb":   "0",
			"traffic_total_limit_gb": "0",
			"reset_day":              "1",
		},
		host: supportedTestHost(),
	}
	cfg, err := w.buildConfig()
	if err != nil {
		t.Fatalf("buildConfig error: %v", err)
	}

	if got := protocolsValue(cfg.Enabled); got != "hysteria2,anytls" {
		t.Fatalf("enabled = %q", got)
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

func TestBuildConfigDisablesTrafficMonitor(t *testing.T) {
	w := &wizard{
		values: map[string]string{
			"domain":                 "example.com",
			"challenge":              "http-01",
			"protocols":              "tuic",
			"tuic_uuid":              "22222222-2222-4222-8222-222222222222",
			"display_name":           "Node",
			"traffic_monitor":        "no",
			"traffic_in_limit_gb":    "40",
			"traffic_out_limit_gb":   "50",
			"traffic_total_limit_gb": "100",
		},
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
