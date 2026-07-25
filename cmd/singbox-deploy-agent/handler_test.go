package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

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

func TestMonitorSupervisorUsesAgentProcessContext(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(layout.MonitorDB), 0o755); err != nil {
		t.Fatal(err)
	}
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

	processCtx, stopProcess := context.WithCancel(context.Background())
	supervisor := newMonitorSupervisor(processCtx, layout)
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
