package main

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/agentfirewall"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

// agentHandler implements nodeapi.Handler by driving the local deploy flow in
// spoke mode.
const installTransactionFile = "install_transaction_id"

type agentHandler struct {
	layout  paths.Layout
	monitor *monitorSupervisor

	mutationOnce    sync.Once
	mutationGate    chan struct{}
	restartPending  bool
	shutdownPending bool
	newRunner       func(context.Context, io.Writer) system.Runner
	runUninstall    func(context.Context, uninstall.Options) error
	// Injectable seams keep the on-disk replacement and delayed restart
	// independently testable without touching the running test executable.
	agentExecutable func() (string, error)
	inspectAgent    func(context.Context, string, string) error
	scheduleRestart func()
	scheduleStop    func()

	// Core seams keep health/version verification and Manager orchestration
	// testable without replacing the host's actual sing-box binary.
	readCoreVersion func(context.Context) (string, error)
	coreActive      func(context.Context) bool
	runCoreManager  func(context.Context, core.Action, string, io.Writer) (core.Result, error)
}

func (h *agentHandler) Health() nodeapi.HealthResponse {
	store := state.NewStore(h.layout.StateDir)
	domain, err := store.ReadValue("domain", false)
	if err != nil {
		return nodeapi.HealthResponse{
			OK:      false,
			Version: version,
			Error:   fmt.Sprintf("read deployment state: %v", err),
		}
	}
	installed := domain != ""
	if !installed {
		return nodeapi.HealthResponse{
			OK:        true,
			Version:   version,
			Installed: false,
			Domain:    domain,
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active := h.isCoreActive(ctx)
	coreVersion, err := h.currentCoreVersion(ctx)
	if err != nil {
		return nodeapi.HealthResponse{
			OK:            false,
			Version:       version,
			Installed:     true,
			SingBoxActive: active,
			Domain:        domain,
			Error:         fmt.Sprintf("inspect sing-box core version: %v", err),
		}
	}
	return nodeapi.HealthResponse{
		OK:             true,
		Version:        version,
		Installed:      true,
		SingBoxVersion: coreVersion,
		SingBoxActive:  active,
		Domain:         domain,
	}
}

func (h *agentHandler) Install(ctx context.Context, req nodeapi.InstallRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	if children, err := nodes.Load(h.layout); err != nil {
		return fmt.Errorf("inspect existing Hub registry before spoke conversion: %w", err)
	} else if len(children) > 0 {
		return fmt.Errorf("cannot convert a Hub managing %d spoke node(s) into a spoke; remove or force-detach its children first", len(children))
	}
	if !req.ConfigOnly {
		domain, err := state.NewStore(h.layout.StateDir).ReadValue("domain", false)
		if err != nil {
			return fmt.Errorf("inspect existing deployment before spoke install: %w", err)
		}
		if domain != "" {
			return fmt.Errorf("this server already has a managed deployment for %s; automatic standalone-to-spoke conversion is disabled because it cannot be rolled back safely", domain)
		}
		if err := nodeapi.ValidateInstallTransactionID(req.InstallTransactionID); err != nil {
			return err
		}
	}
	if err := nodeapi.ValidateInstallSingBoxVersion(req); err != nil {
		return err
	}
	cfg, err := h.buildSpokeConfig(req)
	if err != nil {
		return err
	}
	host, err := system.DetectHost()
	if err != nil {
		return fmt.Errorf("detect host: %w", err)
	}
	if !req.ConfigOnly {
		if err := state.NewStore(agentConfigDir(h.layout)).WriteString(installTransactionFile, req.InstallTransactionID+"\n", 0o600); err != nil {
			return fmt.Errorf("record full-install transaction ownership: %w", err)
		}
	}
	runner := h.commandRunner(ctx, log)
	// Remove stale Hub-only services left by an interrupted older deployment
	// before activating the spoke. A full standalone deployment was rejected
	// above, so this cleanup cannot destroy a live migration source.
	disableLegacyHubServices(runner, "/etc/systemd/system")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.writePushedCertificate(req.Domain, req.CertificatePEM, req.PrivateKeyPEM); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	orch := h.newSpokeOrchestrator(ctx, req, host, runner, log)
	applyErr := runSpokeDeployment(
		req.ConfigOnly,
		func() error { return orch.Run(ctx, cfg) },
		func() error { return orch.Reconfigure(ctx, cfg) },
	)
	if applyErr != nil {
		return applyErr
	}
	if err := removeLegacyHubArtifacts(h.layout); err != nil {
		return fmt.Errorf("remove legacy standalone management artifacts: %w", err)
	}
	if err := runner.Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return fmt.Errorf("reload systemd after spoke activation: %w", err)
	}
	if h.monitor != nil {
		h.monitor.reload()
	}
	return nil
}

func (h *agentHandler) newSpokeOrchestrator(
	_ context.Context,
	req nodeapi.InstallRequest,
	host system.Host,
	runner system.Runner,
	log io.Writer,
) *deploy.Orchestrator {
	pinnedVersion := req.SingBoxVersion
	return &deploy.Orchestrator{
		Runner: runner,
		Layout: h.layout,
		GOOS:   "linux",
		GOARCH: host.Arch,
		// A full spoke install must resolve to the exact hub-selected tag. This
		// fixed resolver deliberately performs no "latest" release lookup.
		LatestSingBox: func(context.Context) (string, error) {
			if err := nodeapi.ValidateStableSingBoxTag(pinnedVersion); err != nil {
				return "", err
			}
			return pinnedVersion, nil
		},
		// The hub decides when to (re)install; skip host-side conflict/port gating.
		CheckConflicts: func(context.Context, deploy.Config) error { return nil },
		CheckPorts:     func(context.Context, deploy.Config) error { return nil },
		Progress:       agentProgressLogger(log),
	}
}

// runSpokeDeployment keeps the config-only path structurally separate from a
// full install. Reconfigure never invokes the Orchestrator's core download
// step, while every full install does.
func runSpokeDeployment(configOnly bool, fullInstall, reconfigure func() error) error {
	if configOnly {
		return reconfigure()
	}
	return fullInstall()
}

func disableLegacyHubServices(runner system.Runner, systemdDir string) {
	for _, unit := range []string{system.CertRenewTimer, system.CertRenewService, system.MonitorService} {
		if _, err := os.Stat(filepath.Join(systemdDir, unit)); err != nil {
			continue
		}
		_ = runner.Run(system.Command{Name: "systemctl", Args: []string{"disable", "--now", unit}})
	}
}

func removeLegacyHubArtifacts(layout paths.Layout) error {
	for _, path := range legacyHubArtifactPaths(layout) {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func legacyHubArtifactPaths(layout paths.Layout) []string {
	return []string{
		"/usr/bin/singbox-deploy",
		"/etc/systemd/system/" + system.CertRenewTimer,
		"/etc/systemd/system/" + system.CertRenewService,
		"/etc/systemd/system/" + system.MonitorService,
		filepath.Join(layout.StateDir, "dns_credentials"),
		filepath.Join(layout.StateDir, "certs"),
		filepath.Join(layout.StateDir, "acme_account_key"),
		filepath.Join(layout.StateDir, ".certmgr-issue.lock"),
		filepath.Join(layout.StateDir, "dns_provider"),
		filepath.Join(layout.StateDir, "dns_credential"),
		filepath.Join(layout.StateDir, "acme_challenge"),
		filepath.Join(layout.StateDir, "certmgr_schema_version"),
		filepath.Join(layout.StateDir, "remotes"),
		filepath.Join(layout.StateDir, "monitor_sources"),
		filepath.Join(layout.StateDir, "spoke_subscriptions"),
		filepath.Join(layout.StateDir, "remote_monitor.json"),
		filepath.Join(layout.StateDir, "nodes"),
		filepath.Join(layout.StateDir, "nodes.lock"),
		filepath.Join(layout.StateDir, "hub_wg_private_key"),
		filepath.Join(layout.StateDir, "hub_wg_public_key"),
		filepath.Join(layout.StateDir, "hub_wg_endpoint_host"),
		filepath.Join(layout.StateDir, "hub_wg_listen_port"),
		filepath.Join(layout.StateDir, "hub_wg_subnet"),
		filepath.Join(layout.StateDir, "hub_installed"),
	}
}

func (h *agentHandler) ApplyCert(ctx context.Context, req nodeapi.CertRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	certPath, keyPath := certmgr.CertPaths(h.layout, req.Domain)
	oldCert, certErr := os.ReadFile(certPath)
	oldKey, keyErr := os.ReadFile(keyPath)
	if err := h.writePushedCertificate(req.Domain, req.CertificatePEM, req.PrivateKeyPEM); err != nil {
		return err
	}
	fmt.Fprintf(log, "installed refreshed certificate for %s\n", req.Domain)
	runner := h.commandRunner(ctx, log)
	if err := deploy.RunCommands(runner,
		system.Systemctl("restart", system.SingBoxService),
		system.Systemctl("restart", "nginx"),
	); err != nil {
		// If activation fails, restore the last complete pair and make a best
		// effort to bring services back. The hub keeps this delivery pending.
		if certErr == nil && keyErr == nil {
			_ = state.WriteFilePair(keyPath, oldKey, 0o600, certPath, oldCert, 0o644)
			_ = deploy.RunCommands(runner,
				system.Systemctl("restart", system.SingBoxService),
				system.Systemctl("restart", "nginx"),
			)
		}
		return fmt.Errorf("activate refreshed certificate: %w", err)
	}
	return nil
}

func (h *agentHandler) Uninstall(ctx context.Context, req nodeapi.UninstallRequest, log io.Writer) (retErr error) {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	agentStateDir := filepath.Join(h.layout.StateDir, "agent")
	if req.KeepOverlay {
		if err := authorizeRollbackUninstall(h.layout, req.RollbackTransactionID); err != nil {
			return err
		}
	}
	var firewallRule agentfirewall.Rule
	var hasFirewallRule bool
	if !req.KeepOverlay {
		var err error
		firewallRule, hasFirewallRule, err = agentfirewall.Load(agentStateDir)
		if err != nil {
			return fmt.Errorf("load Agent firewall state: %w", err)
		}
	}
	if h.monitor != nil {
		h.monitor.stop()
		defer func() {
			if retErr != nil {
				h.monitor.reload()
			}
		}()
	}
	runner := h.commandRunner(ctx, log)
	runUninstall := h.runUninstall
	if runUninstall == nil {
		runUninstall = uninstall.Run
	}
	if err := runUninstall(ctx, uninstall.Options{
		Runner:              runner,
		Layout:              h.layout,
		DeleteRuntime:       true,
		DeleteCertificates:  true,
		DeleteMonitorDB:     true,
		DeleteSite:          true,
		DeleteSubscriptions: true,
		PreserveAgentState:  true,
		Progress:            agentProgressLogger(log),
	}); err != nil {
		return err
	}
	if !req.KeepOverlay {
		recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelRecovery()
		recoveryRunner := h.commandRunner(recoveryCtx, log)
		if err := prepareAgentAndOverlayTeardown(h.layout, runner, recoveryRunner, firewallRule, hasFirewallRule, agentTeardownPaths()); err != nil {
			return err
		}
		fmt.Fprintln(log, "spoke runtime, agent, and WireGuard state removed; service stop scheduled")
		if h.scheduleStop != nil {
			h.scheduleStop()
		} else {
			time.AfterFunc(750*time.Millisecond, stopAgentAndOverlay)
		}
	}
	h.shutdownPending = true
	return nil
}

func authorizeRollbackUninstall(layout paths.Layout, transactionID string) error {
	if err := nodeapi.ValidateInstallTransactionID(transactionID); err != nil {
		return fmt.Errorf("authorize rollback uninstall: %w", err)
	}
	owner, err := state.NewStore(agentConfigDir(layout)).ReadValue(installTransactionFile, true)
	if err != nil {
		return fmt.Errorf("authorize rollback uninstall: %w", err)
	}
	if owner != transactionID {
		return fmt.Errorf("refusing rollback uninstall: transaction %s does not own this deployment", transactionID)
	}
	return nil
}

func prepareAgentAndOverlayTeardown(layout paths.Layout, runner, recoveryRunner system.Runner, firewallRule agentfirewall.Rule, hasFirewallRule bool, teardownPaths []string) error {
	agentDir := filepath.Join(layout.StateDir, "agent")
	const (
		unitsDisabledMarker      = "teardown_units_disabled"
		firewallCleanupNextFile  = "firewall_cleanup_next"
		maxAgentStateSnapshotLen = 4 << 20
		maxTeardownFilesSnapshot = nodeapi.MaxAgentBinarySize + (8 << 20)
	)
	cleanRoot := filepath.Clean(layout.Root)
	cleanAgentDir := filepath.Clean(agentDir)
	rel, err := filepath.Rel(cleanRoot, cleanAgentDir)
	if err != nil || cleanRoot == "." || cleanRoot == string(os.PathSeparator) || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to remove unsafe Agent state path %q", agentDir)
	}
	if info, statErr := os.Lstat(agentDir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to tear down symlinked Agent state path %q", agentDir)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect Agent state path: %w", statErr)
	}

	var firewallCommands, firewallRestoreCommands []system.Command
	if hasFirewallRule {
		firewallCommands, err = firewallRule.RemoveCommands()
		if err != nil {
			return fmt.Errorf("build Agent firewall cleanup: %w", err)
		}
		firewallRestoreCommands, err = firewallRule.OpenCommands()
		if err != nil {
			return fmt.Errorf("build Agent firewall recovery: %w", err)
		}
	}
	firewallStart := 0
	if raw, readErr := state.NewStore(agentDir).ReadValue(firewallCleanupNextFile, false); readErr != nil {
		return fmt.Errorf("read Agent firewall cleanup progress: %w", readErr)
	} else if raw != "" {
		firewallStart, err = strconv.Atoi(raw)
		if err != nil || firewallStart < 0 || firewallStart > len(firewallCommands) {
			return fmt.Errorf("invalid Agent firewall cleanup progress %q", raw)
		}
	}
	stateSnapshot, err := snapshotAgentState(agentDir, maxAgentStateSnapshotLen)
	if err != nil {
		return err
	}
	teardownSnapshot, err := snapshotTeardownFiles(teardownPaths, maxTeardownFilesSnapshot)
	if err != nil {
		return err
	}
	if recoveryRunner == nil {
		recoveryRunner = runner
	}
	markerPath := filepath.Join(agentDir, unitsDisabledMarker)
	rollback := func(cause error, restoreFirewall bool) error {
		restoreErr := restoreAgentControlPlane(
			agentDir,
			stateSnapshot,
			teardownSnapshot,
			recoveryRunner,
			markerPath,
			firewallCleanupNextFile,
			restoreFirewall,
			firewallRestoreCommands,
		)
		return errors.Join(cause, wrapOptionalError("restore Agent control plane", restoreErr))
	}
	if _, statErr := os.Stat(markerPath); os.IsNotExist(statErr) {
		for _, unit := range []string{"singbox-deploy-agent.service", "wg-quick@sbwg0.service"} {
			cmd := system.Command{Name: "systemctl", Args: []string{"disable", unit}}
			if err := runner.Run(cmd); err != nil {
				return rollback(fmt.Errorf("%s: %w", cmd.String(), err), false)
			}
		}
		if err := state.NewStore(agentDir).WriteString(unitsDisabledMarker, "yes\n", 0o600); err != nil {
			return rollback(fmt.Errorf("record disabled Agent control-plane units: %w", err), false)
		}
	} else if statErr != nil {
		return fmt.Errorf("inspect Agent teardown progress: %w", statErr)
	}
	for _, path := range teardownPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return rollback(fmt.Errorf("remove %s: %w", path, err), false)
		}
	}
	reload := system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}
	if err := runner.Run(reload); err != nil {
		return rollback(fmt.Errorf("%s: %w", reload.String(), err), false)
	}
	// The firewall rule is intentionally last: until every other durable
	// cleanup step succeeds, the Hub must remain able to retry this request.
	if err := os.RemoveAll(agentDir); err != nil {
		return rollback(fmt.Errorf("remove Agent state: %w", err), false)
	}
	for i := firewallStart; i < len(firewallCommands); i++ {
		command := firewallCommands[i]
		if runErr := runner.Run(command); runErr != nil {
			return fmt.Errorf("remove Agent firewall rule: %w", rollback(
				fmt.Errorf("command %q: %w", command.String(), runErr),
				true,
			))
		}
	}
	return nil
}

type agentStateFile struct {
	name string
	data []byte
	mode os.FileMode
}

func snapshotAgentState(dir string, maxBytes int64) ([]agentStateFile, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Agent state snapshot: %w", err)
	}
	var total int64
	files := make([]agentStateFile, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect Agent state %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to snapshot non-regular Agent state entry %s", entry.Name())
		}
		total += info.Size()
		if total > maxBytes {
			return nil, fmt.Errorf("Agent state snapshot exceeds %d bytes", maxBytes)
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("snapshot Agent state %s: %w", entry.Name(), err)
		}
		if int64(len(data)) != info.Size() {
			return nil, fmt.Errorf("Agent state %s changed while being snapshotted", entry.Name())
		}
		files = append(files, agentStateFile{name: entry.Name(), data: data, mode: info.Mode().Perm()})
	}
	return files, nil
}

func restoreAgentState(dir string, files []agentStateFile) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, file := range files {
		if err := state.WriteFileAtomic(filepath.Join(dir, file.name), file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

type teardownFile struct {
	path          string
	data          []byte
	mode          os.FileMode
	symlinkTarget string
	exists        bool
	symlink       bool
}

func snapshotTeardownFiles(paths []string, maxBytes int64) ([]teardownFile, error) {
	var total int64
	files := make([]teardownFile, 0, len(paths))
	for _, path := range paths {
		cleanPath := filepath.Clean(path)
		if !filepath.IsAbs(cleanPath) || cleanPath == string(os.PathSeparator) {
			return nil, fmt.Errorf("refusing to snapshot unsafe teardown path %q", path)
		}
		file := teardownFile{path: cleanPath}
		info, err := os.Lstat(cleanPath)
		if os.IsNotExist(err) {
			files = append(files, file)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect teardown path %s: %w", cleanPath, err)
		}
		file.exists = true
		if info.Mode()&os.ModeSymlink != 0 {
			file.symlink = true
			file.symlinkTarget, err = os.Readlink(cleanPath)
			if err != nil {
				return nil, fmt.Errorf("read teardown symlink %s: %w", cleanPath, err)
			}
			files = append(files, file)
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to snapshot non-regular teardown path %s", cleanPath)
		}
		total += info.Size()
		if total > maxBytes {
			return nil, fmt.Errorf("control-plane teardown snapshot exceeds %d bytes", maxBytes)
		}
		file.data, err = os.ReadFile(cleanPath)
		if err != nil {
			return nil, fmt.Errorf("snapshot teardown path %s: %w", cleanPath, err)
		}
		if int64(len(file.data)) != info.Size() {
			return nil, fmt.Errorf("teardown path %s changed while being snapshotted", cleanPath)
		}
		file.mode = info.Mode().Perm()
		files = append(files, file)
	}
	return files, nil
}

func restoreTeardownFiles(files []teardownFile) error {
	var errs []error
	for _, file := range files {
		if !file.exists {
			if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove newly-created teardown path %s: %w", file.path, err))
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("create parent for %s: %w", file.path, err))
			continue
		}
		if file.symlink {
			if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("replace teardown symlink %s: %w", file.path, err))
				continue
			}
			if err := os.Symlink(file.symlinkTarget, file.path); err != nil {
				errs = append(errs, fmt.Errorf("restore teardown symlink %s: %w", file.path, err))
			}
			continue
		}
		if err := state.WriteFileAtomic(file.path, file.data, file.mode); err != nil {
			errs = append(errs, fmt.Errorf("restore teardown file %s: %w", file.path, err))
		}
	}
	return errors.Join(errs...)
}

func restoreAgentControlPlane(
	agentDir string,
	stateFiles []agentStateFile,
	teardownFiles []teardownFile,
	runner system.Runner,
	unitsDisabledMarkerPath string,
	firewallCleanupNextFile string,
	restoreFirewall bool,
	firewallRestoreCommands []system.Command,
) error {
	var errs []error
	if restoreFirewall {
		if err := deploy.RunCommands(runner, firewallRestoreCommands...); err != nil {
			errs = append(errs, fmt.Errorf("re-open Agent firewall rule: %w", err))
		}
	}
	if err := restoreAgentState(agentDir, stateFiles); err != nil {
		errs = append(errs, fmt.Errorf("restore Agent state: %w", err))
	}
	if err := restoreTeardownFiles(teardownFiles); err != nil {
		errs = append(errs, err)
	}
	reload := system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}
	if err := runner.Run(reload); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", reload.String(), err))
	}
	for _, unit := range []string{"wg-quick@sbwg0.service", "singbox-deploy-agent.service"} {
		cmd := system.Command{Name: "systemctl", Args: []string{"enable", unit}}
		if err := runner.Run(cmd); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cmd.String(), err))
		}
	}
	if err := os.Remove(unitsDisabledMarkerPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("clear disabled-unit teardown marker: %w", err))
	}
	if restoreFirewall {
		stagePath := filepath.Join(agentDir, firewallCleanupNextFile)
		if err := os.Remove(stagePath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("reset firewall cleanup progress: %w", err))
		}
	}
	return errors.Join(errs...)
}

func wrapOptionalError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func stopAgentAndOverlay() {
	// The durable wg-quick config is already gone, so tear down the simple
	// managed interface directly instead of asking wg-quick to re-read a
	// missing file. Queueing our own stop avoids waiting on this process.
	_ = exec.Command("ip", "link", "delete", "dev", "sbwg0").Run()
	_ = exec.Command("systemctl", "--no-block", "stop", "singbox-deploy-agent.service").Run()
}

func agentTeardownPaths() []string {
	return []string{
		"/etc/systemd/system/singbox-deploy-agent.service",
		"/etc/wireguard/sbwg0.conf",
		"/etc/wireguard/sbwg0.conf.singbox-deploy.template",
		"/etc/wireguard/sbwg0.key",
		"/etc/wireguard/sbwg0.key.singbox-deploy.tmp",
		"/usr/bin/singbox-deploy-agent",
	}
}

// Upgrade validates and stages a hub-supplied agent before atomically replacing
// the current executable. The systemd restart is deliberately delayed until
// after this handler has returned so the streamed acknowledgement reaches the
// hub before the process is stopped.
func (h *agentHandler) Upgrade(ctx context.Context, req nodeapi.UpgradeRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	if err := nodeapi.ValidateUpgradeRequest(req); err != nil {
		return err
	}
	if err := validateAgentELF(req.Binary); err != nil {
		return err
	}
	executable := h.agentExecutable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return fmt.Errorf("locate current agent executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	inspect := h.inspectAgent
	if inspect == nil {
		inspect = inspectStagedAgent
	}
	if err := replaceAgentAtomically(ctx, path, req.Binary, req.Version, inspect); err != nil {
		return err
	}
	h.restartPending = true

	fmt.Fprintf(log, "installed agent %s (%s); service restart scheduled\n", req.Version, req.SHA256)
	if h.scheduleRestart != nil {
		h.scheduleRestart()
	} else {
		time.AfterFunc(750*time.Millisecond, func() {
			// --no-block queues the job with systemd without waiting for this very
			// service to return, avoiding a restart/deadlock cycle.
			_ = exec.Command("systemctl", "--no-block", "restart", "singbox-deploy-agent.service").Run()
		})
	}
	return nil
}

// ChangeCore replaces the local sing-box binary with one exact stable release,
// then independently verifies the installed tag and active service state before
// acknowledging success to the Hub.
func (h *agentHandler) ChangeCore(ctx context.Context, req nodeapi.CoreRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	if err := nodeapi.ValidateCoreRequest(req); err != nil {
		return err
	}
	run := h.runCoreManager
	if run == nil {
		run = h.runCoreManagerDefault
	}
	result, err := run(ctx, core.ActionChangeStable, req.SingBoxVersion, log)
	if err != nil {
		return err
	}
	if result.Tag != req.SingBoxVersion {
		return fmt.Errorf("sing-box core manager reported target %q, expected %q", result.Tag, req.SingBoxVersion)
	}
	reported, err := h.currentCoreVersion(ctx)
	if err != nil {
		return fmt.Errorf("verify installed sing-box core version: %w", err)
	}
	if reported != req.SingBoxVersion {
		return fmt.Errorf("installed sing-box core reports %q, expected %q", reported, req.SingBoxVersion)
	}
	if !h.isCoreActive(ctx) {
		return fmt.Errorf("sing-box service is not active after changing core to %s", req.SingBoxVersion)
	}
	fmt.Fprintf(log, "verified sing-box core %s and active %s\n", reported, system.SingBoxService)
	return nil
}

func (h *agentHandler) runCoreManagerDefault(
	ctx context.Context,
	action core.Action,
	tag string,
	log io.Writer,
) (core.Result, error) {
	host, err := system.DetectHost()
	if err != nil {
		return core.Result{}, fmt.Errorf("detect host for sing-box core change: %w", err)
	}
	manager := &core.Manager{
		Runner:   h.commandRunner(ctx, log),
		Layout:   h.layout,
		Progress: agentProgressLogger(log),
		GOOS:     "linux",
		GOARCH:   host.Arch,
	}
	return manager.Run(ctx, action, tag)
}

func (h *agentHandler) currentCoreVersion(ctx context.Context) (string, error) {
	read := h.readCoreVersion
	if read == nil {
		read = func(ctx context.Context) (string, error) {
			return core.InstalledVersion(ctx, h.layout.SingBoxBin)
		}
	}
	version, err := read(ctx)
	if err != nil {
		return "", err
	}
	return nodeapi.NormalizeSingBoxVersion(version)
}

func (h *agentHandler) isCoreActive(ctx context.Context) bool {
	if h.coreActive != nil {
		return h.coreActive(ctx)
	}
	return singBoxActiveContext(ctx)
}

func (h *agentHandler) beginMutation(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mutationOnce.Do(func() {
		h.mutationGate = make(chan struct{}, 1)
		h.mutationGate <- struct{}{}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.mutationGate:
	}
	if err := ctx.Err(); err != nil {
		h.endMutation()
		return err
	}
	switch {
	case h.restartPending:
		h.endMutation()
		return fmt.Errorf("an agent upgrade has already committed; restart is pending")
	case h.shutdownPending:
		h.endMutation()
		return fmt.Errorf("agent uninstall has already committed; shutdown is pending")
	default:
		return nil
	}
}

func (h *agentHandler) endMutation() {
	h.mutationGate <- struct{}{}
}

func (h *agentHandler) commandRunner(ctx context.Context, out io.Writer) system.Runner {
	if h.newRunner != nil {
		return h.newRunner(ctx, out)
	}
	return system.NewExecRunnerContext(ctx, out)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// agentProgressLogger emits one line per completed step. RunSteps reports both
// a running and a terminal event; streaming both without the status made every
// successful Agent install/uninstall step appear twice. Failed terminal events
// retain the underlying error so the streamed log remains diagnostic.
func agentProgressLogger(log io.Writer) func(deploy.Event) {
	return func(e deploy.Event) {
		var outcome string
		switch e.Status {
		case "ok":
			outcome = "complete"
		case "fail":
			outcome = "failed"
		default:
			return
		}
		if e.Detail != "" {
			outcome += " - " + e.Detail
		}
		if e.Status == "fail" && e.Err != nil {
			outcome += ": " + e.Err.Error()
		}
		fmt.Fprintf(log, "[%d/%d] %s: %s\n", e.Index, e.Total, e.Label, outcome)
	}
}

func validateAgentELF(binary []byte) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("agent self-upgrade is supported only on linux, not %s", runtime.GOOS)
	}
	f, err := elf.NewFile(bytes.NewReader(binary))
	if err != nil {
		return fmt.Errorf("agent payload is not a valid ELF executable: %w", err)
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("agent ELF has unsupported type %s", f.Type)
	}
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("agent ELF targets unsupported OS ABI %s", f.OSABI)
	}
	wantMachine := elf.Machine(0)
	switch runtime.GOARCH {
	case "amd64":
		wantMachine = elf.EM_X86_64
	case "arm64":
		wantMachine = elf.EM_AARCH64
	default:
		return fmt.Errorf("agent self-upgrade is unsupported on architecture %s", runtime.GOARCH)
	}
	if f.Machine != wantMachine {
		return fmt.Errorf("agent ELF architecture %s does not match running %s agent", f.Machine, runtime.GOARCH)
	}
	return nil
}

// replaceAgentAtomically leaves the old executable untouched on every
// validation/staging failure; rename is the final, atomic commit point.
func replaceAgentAtomically(ctx context.Context, path string, binary []byte, expectedVersion string, inspect func(context.Context, string, string) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".upgrade-*")
	if err != nil {
		return fmt.Errorf("stage agent upgrade: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(binary); err != nil {
		return fmt.Errorf("write staged agent: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod staged agent: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync staged agent: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged agent: %w", err)
	}
	if err := inspect(ctx, tmpPath, expectedVersion); err != nil {
		return fmt.Errorf("verify staged agent version: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit agent upgrade: %w", err)
	}
	committed = true
	// Best-effort directory sync makes the rename durable without turning a
	// successfully committed replacement into a reported failure.
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func inspectStagedAgent(ctx context.Context, path, expectedVersion string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(checkCtx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("run staged agent --version: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != expectedVersion {
		return fmt.Errorf("binary reports version %q, expected %q", got, expectedVersion)
	}
	return nil
}

func (h *agentHandler) Subscription(format string) ([]byte, error) {
	dir, ok := subscriptionDir(format)
	if !ok {
		return nil, fmt.Errorf("unknown subscription format %q", format)
	}
	store := state.NewStore(h.layout.StateDir)
	salt, err := store.ReadValue("subscribe_salt", true)
	if err != nil {
		return nil, err
	}
	token := deploy.SubscriptionToken(salt)
	return os.ReadFile(filepath.Join(h.layout.SubscribeDir, dir, token))
}

// MonitorHandler exposes the in-process sampler through nodeapi's fixed,
// bearer-authenticated monitor routes. The supervisor returns 503 while the
// monitor is disabled or being reloaded.
func (h *agentHandler) MonitorHandler() http.Handler {
	if h.monitor == nil {
		return nil
	}
	return h.monitor
}

func subscriptionDir(format string) (string, bool) {
	switch format {
	case nodeapi.FormatDefault:
		return "default", true
	case nodeapi.FormatClashMeta:
		return "clashMeta", true
	case nodeapi.FormatSingBoxProfiles:
		return "singboxProfiles", true
	case nodeapi.FormatSurge:
		return "surge", true
	default:
		return "", false
	}
}

func (h *agentHandler) writePushedCertificate(domain, certPEM, keyPEM string) error {
	if certPEM == "" || keyPEM == "" {
		return nil // no cert in this request (e.g. config-only apply)
	}
	if _, err := certmgr.ValidateCertificatePair([]byte(certPEM), []byte(keyPEM), domain, time.Now()); err != nil {
		return fmt.Errorf("validate certificate for %s: %w", domain, err)
	}
	certPath, keyPath := certmgr.CertPaths(h.layout, domain)
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("invalid certificate domain %q", domain)
	}
	return state.WriteFilePair(keyPath, []byte(keyPEM), 0o600, certPath, []byte(certPEM), 0o644)
}

// buildSpokeConfig assembles the spoke deploy.Config from the hub's request,
// preserving generated credentials and the subscription salt across edits.
func (h *agentHandler) buildSpokeConfig(req nodeapi.InstallRequest) (deploy.Config, error) {
	var creds deploy.Credentials
	var salt string
	if existing, err := deploy.LoadProtocolConfig(h.layout); err == nil {
		creds = existing.Creds
		salt = existing.Salt
	} else {
		creds, err = deploy.GenerateCredentials()
		if err != nil {
			return deploy.Config{}, err
		}
		salt, err = credentials.Salt()
		if err != nil {
			return deploy.Config{}, err
		}
	}
	host, err := system.DetectHost()
	if err != nil {
		return deploy.Config{}, err
	}
	enabled := deploy.CanonicalProtocols(protocolsFromStrings(req.EnabledProtocols))
	if len(enabled) == 0 {
		enabled = config.AllProtocols
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = deploy.DefaultDisplayName
	}
	monitorPort := req.MonitorPort
	if monitorPort <= 0 {
		monitorPort = deploy.DefaultMonitorPort
	}
	return deploy.Config{
		Domain:               req.Domain,
		SpokeMode:            true,
		Enabled:              enabled,
		DisplayName:          displayName,
		Salt:                 salt,
		SiteTemplate:         req.SiteTemplate,
		RealityServerName:    req.RealityServerName,
		RealityHandshakePort: req.RealityHandshakePort,
		// Public subscription/monitor ports are unused on a spoke but still fill
		// the config; nginx renders no public location for them in spoke mode.
		SubscribePort:          deploy.DefaultSubscribePort,
		MonitorPublicPort:      deploy.DefaultMonitorPublicPort,
		MonitorPort:            monitorPort,
		DeployMonitor:          req.Monitor,
		DeployMonitorFrontend:  false,
		MonitorAlias:           req.MonitorAlias,
		MonitorInterface:       req.MonitorInterface,
		MonitorIntervalSeconds: req.MonitorIntervalSeconds,
		TrafficInLimitBytes:    req.TrafficInLimitBytes,
		TrafficOutLimitBytes:   req.TrafficOutLimitBytes,
		TrafficTotalLimitBytes: req.TrafficTotalLimitBytes,
		ResetDay:               req.ResetDay,
		ResetHour:              req.ResetHour,
		Ports: config.Ports{
			RealityVision: req.Ports.RealityVision,
			RealityGRPC:   req.Ports.RealityGRPC,
			Hysteria2:     req.Ports.Hysteria2,
			TUIC:          req.Ports.TUIC,
			AnyTLS:        req.Ports.AnyTLS,
		},
		OS:       host.OS,
		Firewall: host.Firewall,
		Creds:    creds,
	}, nil
}

func protocolsFromStrings(values []string) []config.Protocol {
	out := make([]config.Protocol, 0, len(values))
	for _, v := range values {
		out = append(out, config.Protocol(v))
	}
	return out
}

func singBoxActiveContext(ctx context.Context) bool {
	err := exec.CommandContext(nonNilContext(ctx), "systemctl", "is-active", "--quiet", system.SingBoxService).Run()
	return err == nil
}

func singBoxActive() bool {
	return singBoxActiveContext(context.Background())
}
