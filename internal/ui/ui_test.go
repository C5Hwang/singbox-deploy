package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/C5Hwang/singbox-deploy/internal/config"
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

func TestWideInstallContentUsesMenuHeight(t *testing.T) {
	m := NewModel()
	m.SetSize(160, 60)
	w := &wizard{phase: phaseRunning, bar: progressBarForTest()}
	m.wizard = w
	_ = m.View()
	if want := lipgloss.Height(m.menuView(sidebarWidth - 4)); w.height != want {
		t.Fatalf("wizard height = %d, want menu height %d", w.height, want)
	}
}

func supportedTestHost() system.Host {
	return system.Host{OS: system.OSRelease{Family: system.FamilyDebian, ID: "ubuntu"}, Arch: "amd64", IsRoot: true}
}

func TestInstallFieldShowsUsageNote(t *testing.T) {
	w := &wizard{phase: phaseForm, fields: installFields(), values: map[string]string{}, input: textinput.New(), width: 80}
	w.startForm()
	view := w.View()
	if !strings.Contains(view, "Used for certificate issuance") {
		t.Fatalf("field usage note missing:\n%s", view)
	}
}

func TestDryRunShortcutShowsIndicator(t *testing.T) {
	m := NewModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !m.dryRun {
		t.Fatalf("dry-run shortcut did not enable mode")
	}
	if !strings.Contains(m.View(), "dry-run mode") {
		t.Fatalf("view missing dry-run indicator:\n%s", m.View())
	}
}

func TestDryRunRunningPausesAfterVisibleUpdate(t *testing.T) {
	w := &wizard{phase: phaseRunning, dryRun: true, ch: make(chan runMsg, 1)}
	cmd := w.handleRun(runMsg{logLine: "[dry-run] apt-get update"})
	if cmd != nil {
		t.Fatalf("dry-run visible update should wait for enter")
	}
	if !w.dryRunAwaitingEnter {
		t.Fatalf("dry-run should wait for enter after visible update")
	}
	if !strings.Contains(w.View(), "enter to show next dry-run update") {
		t.Fatalf("running view missing dry-run prompt:\n%s", w.View())
	}
}

func TestDryRunRunningEnterReadsNextUpdate(t *testing.T) {
	w := &wizard{phase: phaseRunning, dryRun: true, ch: make(chan runMsg, 1)}
	w.handleRun(runMsg{logLine: "first"})
	w.ch <- runMsg{logLine: "second"}
	cmd, done := w.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if done {
		t.Fatalf("enter should continue dry-run, not close wizard")
	}
	if cmd == nil {
		t.Fatalf("enter should read next dry-run update")
	}
	msg, ok := cmd().(runMsg)
	if !ok || msg.logLine != "second" {
		t.Fatalf("next message = %#v", msg)
	}
	w.handleRun(msg)
	if !strings.Contains(strings.Join(w.logBuf, "\n"), "second") {
		t.Fatalf("log buffer missing next update: %#v", w.logBuf)
	}
}

func TestLogWriterSanitizesTerminalControlOutput(t *testing.T) {
	ch := make(chan runMsg, 4)
	lw := &logWriter{ch: ch, block: true}
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
	w.appendLog("[dry-run] " + strings.Repeat("long-command ", 20))
	if got := lipgloss.Height(w.View()); got > w.height {
		t.Fatalf("running view height = %d, want <= %d:\n%s", got, w.height, w.View())
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

func TestDryRunSkipsDomainIPValidation(t *testing.T) {
	w := &wizard{
		phase:  phaseForm,
		fields: installFields(),
		values: map[string]string{},
		input:  textinput.New(),
		dryRun: true,
		validateDomain: func(context.Context, string) error {
			t.Fatalf("dry-run should not validate domain IP")
			return nil
		},
	}
	w.startForm()
	w.input.SetValue("dry-run.example")
	w.commitField()

	if got := w.values["domain"]; got != "dry-run.example" {
		t.Fatalf("domain = %q, want dry-run.example", got)
	}
	if got := w.fields[w.fieldIx].key; got != "email" {
		t.Fatalf("current field = %q, want email", got)
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
			"domain":              "example.com",
			"challenge":           "http-01",
			"protocols":           "reality-vision,tuic",
			"reality_sni":         "https://www.cloudflare.com/cdn-cgi/trace",
			"reality_vision_uuid": "11111111-1111-4111-8111-111111111111",
			"reality_vision_port": "24443",
			"tuic_uuid":           "22222222-2222-4222-8222-222222222222",
			"tuic_port":           "24444",
			"display_name":        "Node",
			"traffic_limit_gb":    "0",
			"reset_day":           "1",
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
	if cfg.Ports.Hysteria2 != 0 || cfg.Ports.AnyTLS != 0 {
		t.Fatalf("unselected protocol ports should stay zero: %#v", cfg.Ports)
	}
}

func TestBuildConfigRandomizesBlankSelectedPorts(t *testing.T) {
	w := &wizard{
		values: map[string]string{
			"domain":           "example.com",
			"challenge":        "http-01",
			"protocols":        "hysteria2,anytls",
			"display_name":     "Node",
			"traffic_limit_gb": "0",
			"reset_day":        "1",
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
