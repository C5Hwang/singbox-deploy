package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/agentfirewall"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

const testInstallTransactionID = "0123456789abcdef0123456789abcdef"

type handlerRecordingRunner struct {
	commands     []string
	failContains string
}

func (r *handlerRecordingRunner) Run(cmd system.Command) error {
	rendered := cmd.String()
	r.commands = append(r.commands, rendered)
	if r.failContains != "" && strings.Contains(rendered, r.failContains) {
		return errors.New("injected command failure")
	}
	return nil
}

func TestAgentMutationsSerializeAndWaitHonorsContext(t *testing.T) {
	h := &agentHandler{}
	if err := h.beginMutation(context.Background()); err != nil {
		t.Fatalf("acquire first mutation: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- h.beginMutation(ctx)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting mutation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting mutation ignored context cancellation")
	}

	h.endMutation()
	if err := h.beginMutation(context.Background()); err != nil {
		t.Fatalf("gate was not released after cancelled waiter: %v", err)
	}
	h.endMutation()
}

func TestAgentMutationsRejectCommittedRestartAndShutdown(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		h := &agentHandler{restartPending: true}
		err := h.beginMutation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "restart is pending") {
			t.Fatalf("beginMutation error = %v", err)
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		h := &agentHandler{shutdownPending: true}
		err := h.beginMutation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "shutdown is pending") {
			t.Fatalf("beginMutation error = %v", err)
		}
	})
}

func TestAgentProgressLoggerEmitsOnlyTerminalEvents(t *testing.T) {
	var log bytes.Buffer
	progress := agentProgressLogger(&log)
	progress(deploy.Event{
		Index: 1, Total: 2, Label: "Packages", Detail: "install dependencies", Status: "running",
	})
	progress(deploy.Event{
		Index: 1, Total: 2, Label: "Packages", Detail: "install dependencies", Status: "ok",
	})
	progress(deploy.Event{
		Index: 2, Total: 2, Label: "Services", Detail: "restart services", Status: "running",
	})
	progress(deploy.Event{
		Index:  2,
		Total:  2,
		Label:  "Services",
		Detail: "restart services",
		Status: "fail",
		Err:    errors.New("injected activation failure"),
	})

	const want = "" +
		"[1/2] Packages: complete - install dependencies\n" +
		"[2/2] Services: failed - restart services: injected activation failure\n"
	if got := log.String(); got != want {
		t.Fatalf("progress log = %q, want %q", got, want)
	}
}

func TestAgentFullInstallRejectsExistingStandaloneBeforeMutation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(layout.StateDir).WriteString("domain", "standalone.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	runnerCreated := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			runnerCreated = true
			return &handlerRecordingRunner{}
		},
	}
	err := h.Install(context.Background(), nodeapi.InstallRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "automatic standalone-to-spoke conversion is disabled") {
		t.Fatalf("Install error = %v", err)
	}
	if runnerCreated {
		t.Fatal("Install created a command runner before rejecting existing deployment")
	}
	domain, readErr := state.NewStore(layout.StateDir).ReadValue("domain", true)
	if readErr != nil || domain != "standalone.example.com" {
		t.Fatalf("existing domain changed: domain=%q err=%v", domain, readErr)
	}
}

func TestAgentFullInstallRequiresRollbackOwnershipBeforeMutation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	runnerCreated := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			runnerCreated = true
			return &handlerRecordingRunner{}
		},
	}
	err := h.Install(context.Background(), nodeapi.InstallRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid install transaction ID") {
		t.Fatalf("Install error = %v", err)
	}
	if runnerCreated {
		t.Fatal("Install created a command runner without rollback ownership")
	}
	if _, err := os.Stat(agentConfigDir(layout)); !os.IsNotExist(err) {
		t.Fatalf("Install wrote Agent state before validating ownership: %v", err)
	}
}

func TestAgentHealthReportsStateReadFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Join(layout.StateDir, "domain"), 0o700); err != nil {
		t.Fatal(err)
	}
	health := (&agentHandler{layout: layout}).Health()
	if health.OK || !strings.Contains(health.Error, "read deployment state") {
		t.Fatalf("Health = %+v, want explicit state read failure", health)
	}
}

func TestRollbackUninstallRequiresMatchingInstallOwner(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	agentStore := state.NewStore(agentConfigDir(layout))
	if err := store.WriteString("domain", "standalone.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err == nil {
		t.Fatal("rollback without an ownership marker was authorized")
	}
	if err := agentStore.WriteString(installTransactionFile, strings.Repeat("a", 32)+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("mismatched rollback error = %v", err)
	}
	if err := agentStore.WriteString(installTransactionFile, testInstallTransactionID+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err != nil {
		t.Fatalf("matching rollback owner rejected: %v", err)
	}
	domain, err := store.ReadValue("domain", true)
	if err != nil || domain != "standalone.example.com" {
		t.Fatalf("authorization checks mutated existing deployment: domain=%q err=%v", domain, err)
	}
}

func TestKeepOverlayUninstallBecomesTerminalAfterOwnedCleanup(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(agentConfigDir(layout)).WriteString(installTransactionFile, testInstallTransactionID+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupCalled := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			return &handlerRecordingRunner{}
		},
		runUninstall: func(_ context.Context, opts uninstall.Options) error {
			cleanupCalled = true
			if !opts.PreserveAgentState {
				t.Fatal("rollback cleanup did not preserve Agent state")
			}
			return nil
		},
	}
	if err := h.Uninstall(context.Background(), nodeapi.UninstallRequest{
		KeepOverlay:           true,
		RollbackTransactionID: testInstallTransactionID,
	}, io.Discard); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !cleanupCalled || !h.shutdownPending {
		t.Fatalf("cleanupCalled=%v shutdownPending=%v", cleanupCalled, h.shutdownPending)
	}
	if err := h.beginMutation(context.Background()); err == nil || !strings.Contains(err.Error(), "shutdown is pending") {
		t.Fatalf("post-rollback mutation error = %v", err)
	}
}

func TestPrepareAgentTeardownCompletesDurableWorkBeforeFirewall(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallUFW,
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	teardownPaths := []string{
		filepath.Join(t.TempDir(), "singbox-deploy-agent.service"),
		filepath.Join(t.TempDir(), "sbwg0.conf"),
	}
	for _, path := range teardownPaths {
		if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, runner, runner, rule, true, teardownPaths); err != nil {
		t.Fatalf("prepareAgentAndOverlayTeardown: %v", err)
	}
	for _, path := range teardownPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("teardown path %s still exists: %v", path, err)
		}
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("Agent state still exists: %v", err)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if !strings.HasPrefix(runner.commands[len(runner.commands)-1], "ufw ") {
		t.Fatalf("firewall was not the final command: %#v", runner.commands)
	}
}

func TestPrepareAgentTeardownRestoresFirewallStateOnFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallUFW,
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	if err := state.NewStore(agentDir).WriteString("token", "still-authenticated\n", 0o600); err != nil {
		t.Fatal(err)
	}
	teardownPaths := []string{
		filepath.Join(t.TempDir(), "singbox-deploy-agent.service"),
		filepath.Join(t.TempDir(), "sbwg0.conf"),
	}
	wantData := [][]byte{[]byte("agent-unit"), []byte("wireguard-config")}
	wantModes := []os.FileMode{0o644, 0o600}
	for i, path := range teardownPaths {
		if err := os.WriteFile(path, wantData[i], wantModes[i]); err != nil {
			t.Fatal(err)
		}
	}
	runner := &handlerRecordingRunner{failContains: "ufw "}
	recovery := &handlerRecordingRunner{}
	err := prepareAgentAndOverlayTeardown(layout, runner, recovery, rule, true, teardownPaths)
	if err == nil || !strings.Contains(err.Error(), "remove Agent firewall rule") {
		t.Fatalf("teardown error = %v", err)
	}
	restored, ok, loadErr := agentfirewall.Load(agentDir)
	if loadErr != nil || !ok {
		t.Fatalf("firewall cleanup state not restored: ok=%v err=%v", ok, loadErr)
	}
	if restored != rule {
		t.Fatalf("restored rule = %#v, want %#v", restored, rule)
	}
	token, tokenErr := state.NewStore(agentDir).ReadValue("token", true)
	if tokenErr != nil || token != "still-authenticated" {
		t.Fatalf("Agent token not restored: token=%q err=%v", token, tokenErr)
	}
	for i, path := range teardownPaths {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, wantData[i]) {
			t.Fatalf("control-plane file %s not restored: data=%q err=%v", path, got, readErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat restored control-plane file %s: %v", path, statErr)
		}
		if info.Mode().Perm() != wantModes[i] {
			t.Fatalf("control-plane file %s mode=%v, want %v", path, info.Mode().Perm(), wantModes[i])
		}
	}
	recoveryLog := strings.Join(recovery.commands, "\n")
	for _, want := range []string{
		"systemctl daemon-reload",
		"systemctl enable wg-quick@sbwg0.service",
		"systemctl enable singbox-deploy-agent.service",
		"ufw allow in on sbwg0",
	} {
		if !strings.Contains(recoveryLog, want) {
			t.Fatalf("recovery commands missing %q:\n%s", want, recoveryLog)
		}
	}

	retryRunner := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, retryRunner, &handlerRecordingRunner{}, restored, true, teardownPaths); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	for _, unit := range []string{"singbox-deploy-agent.service", "wg-quick@sbwg0.service"} {
		if !strings.Contains(strings.Join(retryRunner.commands, "\n"), "systemctl disable "+unit) {
			t.Fatalf("retry did not disable restored unit %s: %#v", unit, retryRunner.commands)
		}
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("Agent state still exists after retry: %v", err)
	}
	for _, path := range teardownPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("restored teardown path %s survived successful retry: %v", path, err)
		}
	}
}

func TestPrepareAgentTeardownRestoresFirewalldBeforeRetry(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallFirewalld,
		Zone:      "public",
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	if err := state.NewStore(agentDir).WriteString("token", "retry-token\n", 0o600); err != nil {
		t.Fatal(err)
	}
	first := &handlerRecordingRunner{failContains: "firewall-cmd --reload"}
	recovery := &handlerRecordingRunner{}
	err := prepareAgentAndOverlayTeardown(layout, first, recovery, rule, true, nil)
	if err == nil {
		t.Fatal("expected injected firewalld reload failure")
	}
	if token, readErr := state.NewStore(agentDir).ReadValue("token", true); readErr != nil || token != "retry-token" {
		t.Fatalf("full Agent state not restored: token=%q err=%v", token, readErr)
	}
	recoveryLog := strings.Join(recovery.commands, "\n")
	if !strings.Contains(recoveryLog, "--add-rich-rule") || !strings.Contains(recoveryLog, "firewall-cmd --reload") {
		t.Fatalf("firewalld rule was not reopened after ambiguous failure:\n%s", recoveryLog)
	}
	if _, statErr := os.Stat(filepath.Join(agentDir, "firewall_cleanup_next")); !os.IsNotExist(statErr) {
		t.Fatalf("firewall cleanup progress was not reset after reopening rule: %v", statErr)
	}

	retry := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, retry, &handlerRecordingRunner{}, rule, true, nil); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	joined := strings.Join(retry.commands, "\n")
	if !strings.Contains(joined, "--remove-rich-rule") {
		t.Fatalf("retry did not restart firewall cleanup after reopening the rule:\n%s", joined)
	}
	if !strings.Contains(joined, "firewall-cmd --reload") {
		t.Fatalf("retry did not resume at reload:\n%s", joined)
	}
}

func TestAgentUpgradeAtomicallyReplacesAndSchedulesRestart(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	if err := os.WriteFile(target, []byte("old-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	restarted := false
	inspected := false
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent: func(_ context.Context, staged, expected string) error {
			inspected = true
			if expected != "v9.8.7" {
				t.Fatalf("expected version = %q", expected)
			}
			got, err := os.ReadFile(staged)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("staged payload mismatch")
			}
			return nil
		},
		scheduleRestart: func() { restarted = true },
	}
	if err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v9.8.7", payload), io.Discard); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || !inspected || !restarted {
		t.Fatalf("upgrade did not commit/inspect/restart: equal=%v inspected=%v restarted=%v", bytes.Equal(got, payload), inspected, restarted)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("upgraded mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestAgentUpgradeFailurePreservesOldExecutable(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("known-good-old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent: func(context.Context, string, string) error {
			return os.ErrInvalid
		},
		scheduleRestart: func() { t.Fatal("restart scheduled after failed validation") },
	}
	err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v2", payload), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "verify staged") {
		t.Fatalf("expected staged validation error, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatalf("old executable changed after failed upgrade")
	}

	badHash := nodeapi.NewUpgradeRequest("v2", payload)
	badHash.SHA256 = strings.Repeat("0", 64)
	if err := h.Upgrade(context.Background(), badHash, io.Discard); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected SHA-256 mismatch, got %v", err)
	}
	got, _ = os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatalf("old executable changed after digest failure")
	}
}

func TestAgentUpgradeRejectsWrongELFArchitecture(t *testing.T) {
	payload := append([]byte(nil), readHostELF(t)...)
	if len(payload) < 20 {
		t.Fatal("host ELF unexpectedly short")
	}
	other := uint16(elf.EM_X86_64)
	if runtime.GOARCH == "amd64" {
		other = uint16(elf.EM_AARCH64)
	}
	if payload[5] == byte(elf.ELFDATA2MSB) {
		binary.BigEndian.PutUint16(payload[18:20], other)
	} else {
		binary.LittleEndian.PutUint16(payload[18:20], other)
	}
	target := filepath.Join(t.TempDir(), "agent")
	old := []byte("old")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{agentExecutable: func() (string, error) { return target, nil }}
	err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v2", payload), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("expected architecture rejection, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatal("wrong-architecture payload replaced executable")
	}
}

func TestInspectStagedAgentChecksReportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'v3.0.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := inspectStagedAgent(context.Background(), path, "v3.0.0"); err != nil {
		t.Fatalf("inspect matching version: %v", err)
	}
	if err := inspectStagedAgent(context.Background(), path, "v4.0.0"); err == nil || !strings.Contains(err.Error(), "reports version") {
		t.Fatalf("expected reported-version mismatch, got %v", err)
	}
}

func TestAgentMonitorHandlerUsesSupervisor(t *testing.T) {
	supervisor := &monitorSupervisor{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/summary" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	h := &agentHandler{monitor: supervisor}
	rec := httptest.NewRecorder()
	h.MonitorHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	inactive := &agentHandler{monitor: &monitorSupervisor{}}
	rec = httptest.NewRecorder()
	inactive.MonitorHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("inactive status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// A monitor that exits on its own retires itself under the write lock, so an
// intentional stop must not hold that lock while waiting for the sampler to
// finish. This asserts the property directly: the run function only returns
// once it has proved a reader can still enter while stop is waiting.
func TestMonitorSupervisorStopDoesNotHoldLockWhileWaiting(t *testing.T) {
	supervisor := newMonitorSupervisor(context.Background(), installedSpokeLayout(t))
	supervisor.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	supervisor.newMonitor = func(_ *monitor.Store, _ monitor.Config) (http.Handler, func(context.Context) error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), func(ctx context.Context) error {
				<-ctx.Done()
				// stop is now blocked on done. Taking the read lock deadlocks if it
				// is waiting while holding the write lock.
				supervisor.mu.RLock()
				supervisor.mu.RUnlock()
				return nil
			}
	}
	supervisor.reload()
	supervisor.mu.RLock()
	started := supervisor.done != nil
	supervisor.mu.RUnlock()
	if !started {
		t.Fatal("monitor did not start from installed spoke state")
	}
	if info, err := os.Stat(filepath.Dir(supervisor.layout.MonitorDB)); err != nil {
		t.Fatalf("monitor store directory was not created: %v", err)
	} else if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("monitor store directory mode = %#o, want 0755", got)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		supervisor.stop()
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop deadlocked waiting for the monitor to exit")
	}
}

// installedSpokeLayout returns a temporary layout whose state files describe an
// installed, monitored spoke.
func installedSpokeLayout(t *testing.T) paths.Layout {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	for name, value := range map[string]string{
		"domain":                   "spoke.example.com\n",
		"monitor":                  "yes\n",
		"monitor_interface":        "lo\n",
		"monitor_interval_seconds": "3600\n",
	} {
		if err := store.WriteString(name, value, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return layout
}

func TestMonitorSupervisorUsesAgentProcessContext(t *testing.T) {
	processCtx, stopProcess := context.WithCancel(context.Background())
	supervisor := newMonitorSupervisor(processCtx, installedSpokeLayout(t))
	supervisor.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	supervisor.newMonitor = func(_ *monitor.Store, _ monitor.Config) (http.Handler, func(context.Context) error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
	}
	supervisor.reload()

	supervisor.mu.RLock()
	done := supervisor.done
	handler := supervisor.handler
	supervisor.mu.RUnlock()
	if done == nil || handler == nil {
		t.Fatal("monitor did not start from installed spoke state")
	}

	// A completed/cancelled HTTP request must have no effect on the process-owned
	// monitor lifecycle.
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil).WithContext(requestCtx)
	rec := httptest.NewRecorder()
	supervisor.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary after request cancellation = %d, want 200", rec.Code)
	}
	select {
	case <-done:
		t.Fatal("monitor stopped with an unrelated request context")
	case <-time.After(50 * time.Millisecond):
	}

	stopProcess()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop with the agent process context")
	}
	supervisor.stop()
}

func TestAgentTeardownRemovesAllWireGuardSecretsAndTemplates(t *testing.T) {
	paths := make(map[string]bool)
	for _, path := range agentTeardownPaths() {
		paths[path] = true
	}
	for _, want := range []string{
		"/etc/wireguard/sbwg0.conf",
		"/etc/wireguard/sbwg0.conf.singbox-deploy.template",
		"/etc/wireguard/sbwg0.key",
		"/etc/wireguard/sbwg0.key.singbox-deploy.tmp",
	} {
		if !paths[want] {
			t.Errorf("teardown does not remove %s", want)
		}
	}
}

func TestLegacyHubArtifactsAreRemovedWhenConvertingToSpoke(t *testing.T) {
	layoutRoot := t.TempDir()
	// Use a synthetic layout only for checking state-relative migration paths;
	// the absolute binary/unit paths are inspected but never removed by this test.
	layout := paths.LayoutForRoot(layoutRoot)
	pathsToRemove := make(map[string]bool)
	for _, path := range legacyHubArtifactPaths(layout) {
		pathsToRemove[path] = true
	}
	for _, want := range []string{
		"/usr/bin/singbox-deploy",
		"/etc/systemd/system/" + system.CertRenewTimer,
		filepath.Join(layout.StateDir, "dns_credentials"),
		filepath.Join(layout.StateDir, "dns_credential"),
		filepath.Join(layout.StateDir, "remotes"),
		filepath.Join(layout.StateDir, "monitor_sources"),
		filepath.Join(layout.StateDir, "spoke_subscriptions"),
	} {
		if !pathsToRemove[want] {
			t.Errorf("spoke migration does not remove %s", want)
		}
	}
}

func readHostELF(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("agent upgrades support linux amd64/arm64")
	}
	b, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatalf("read host ELF: %v", err)
	}
	return b
}
