package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

const (
	installTransactionFile = "install_transaction_id"
	agentBackupSuffix      = ".singbox-deploy-backup"
)

// agentHandler implements nodeapi.Handler by driving the local deploy flow in
// spoke mode.
type agentHandler struct {
	layout  paths.Layout
	monitor *monitorSupervisor
	// systemdDir is a test seam for protocol reconfigure integration tests.
	// Production leaves it empty and deploy.Orchestrator uses the system path.
	systemdDir    string
	nginxConfPath string

	mutationOnce        sync.Once
	mutationGate        chan struct{}
	restartPending      bool
	shutdownPending     bool
	pendingAgentRestore *agentReplacement
	newRunner           func(context.Context, io.Writer) system.Runner
	runUninstall        func(context.Context, uninstall.Options) error
	// Injectable seams keep the on-disk replacement and independent restart
	// independently testable without touching the running test executable.
	agentExecutable func() (string, error)
	inspectAgent    func(context.Context, string, string) error
	scheduleRestart func() error
	renameAgent     func(oldPath, newPath string) error
	scheduleStop    func()

	// Core seams keep health/version verification and Manager orchestration
	// testable without replacing the host's actual sing-box binary.
	readCoreVersion func(context.Context) (string, error)
	coreActive      func(context.Context) bool
	runCoreManager  func(context.Context, core.Action, string, io.Writer) (core.Result, error)
	// quotaStopped is a seam over the monitor store's quota-stop marker so
	// health tests do not need a SQLite database on disk.
	quotaStopped func() (bool, error)
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
	quotaStopped := !active && h.isQuotaStopped()
	coreVersion, err := h.currentCoreVersion(ctx)
	if err != nil {
		return nodeapi.HealthResponse{
			OK:            false,
			Version:       version,
			Installed:     true,
			SingBoxActive: active,
			QuotaStopped:  quotaStopped,
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
		QuotaStopped:   quotaStopped,
		Domain:         domain,
	}
}

// isQuotaStopped reports whether the monitor owns the current sing-box stop.
// A read failure reads as false: the hub then treats the spoke as genuinely
// down, which fails closed for coordinated self-update.
func (h *agentHandler) isQuotaStopped() bool {
	read := h.quotaStopped
	if read == nil {
		read = func() (bool, error) {
			return monitor.QuotaStopState(h.layout.MonitorDB)
		}
	}
	stopped, err := read()
	return err == nil && stopped
}

func (h *agentHandler) ProtocolState(ctx context.Context) (nodeapi.ProtocolStateResponse, error) {
	if err := h.beginMutation(nonNilContext(ctx)); err != nil {
		return nodeapi.ProtocolStateResponse{}, err
	}
	defer h.endMutation()
	return h.protocolStateUnlocked()
}

func (h *agentHandler) protocolStateUnlocked() (nodeapi.ProtocolStateResponse, error) {
	cfg, err := deploy.LoadProtocolConfig(h.layout)
	if err != nil {
		return nodeapi.ProtocolStateResponse{}, fmt.Errorf("load protocol state: %w", err)
	}
	response := nodeapi.ProtocolStateResponse{
		Domain:               cfg.Domain,
		RealityServerName:    cfg.RealityServerName,
		RealityHandshakePort: cfg.RealityHandshakePort,
		EnabledProtocols:     protocolNamesForState(cfg.Enabled),
		Ports: nodeapi.PortSet{
			RealityVision: cfg.Ports.RealityVision,
			RealityGRPC:   cfg.Ports.RealityGRPC,
			Hysteria2:     cfg.Ports.Hysteria2,
			TUIC:          cfg.Ports.TUIC,
			AnyTLS:        cfg.Ports.AnyTLS,
		},
		Credentials: protocolCredentialsFromDeploy(cfg.Creds),
	}
	revision, err := nodeapi.ProtocolStateRevision(response)
	if err != nil {
		return nodeapi.ProtocolStateResponse{}, fmt.Errorf("revision protocol state: %w", err)
	}
	response.Revision = revision
	return response, nil
}

func (h *agentHandler) TrafficUsage(ctx context.Context) (nodeapi.TrafficUsage, error) {
	if err := h.beginMutation(nonNilContext(ctx)); err != nil {
		return nodeapi.TrafficUsage{}, err
	}
	defer h.endMutation()
	cfg, err := deploy.LoadProtocolConfig(h.layout)
	if err != nil {
		return nodeapi.TrafficUsage{}, fmt.Errorf("load traffic monitor state: %w", err)
	}
	usage, err := h.currentTrafficUsage(cfg)
	if err != nil {
		return nodeapi.TrafficUsage{}, err
	}
	return nodeapi.TrafficUsage{
		InBytes: usage.Totals.InBytes, OutBytes: usage.Totals.OutBytes,
		CycleStart: usage.CycleStart.Unix(),
	}, nil
}

func (h *agentHandler) SetTrafficUsage(ctx context.Context, req nodeapi.TrafficUsageRequest) (nodeapi.TrafficUsageUpdate, error) {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return nodeapi.TrafficUsageUpdate{}, err
	}
	defer h.endMutation()
	if err := nodeapi.ValidateTrafficUsageRequest(req); err != nil {
		return nodeapi.TrafficUsageUpdate{}, err
	}
	cfg, err := deploy.LoadProtocolConfig(h.layout)
	if err != nil {
		return nodeapi.TrafficUsageUpdate{}, fmt.Errorf("load traffic monitor state: %w", err)
	}
	target := monitor.TrafficTotals{InBytes: req.InBytes, OutBytes: req.OutBytes}
	var update monitor.TrafficUsageUpdate
	if cfg.DeployMonitor && h.monitor != nil {
		update, err = h.monitor.setTrafficUsage(req.ExpectedCycleStart, target)
	} else {
		update, err = h.setStoredTrafficUsage(cfg, req.ExpectedCycleStart, target)
	}
	if errors.Is(err, monitor.ErrTrafficCycleChanged) {
		return nodeapi.TrafficUsageUpdate{}, nodeapi.TrafficCycleConflict()
	}
	if err != nil {
		return nodeapi.TrafficUsageUpdate{}, fmt.Errorf("set current traffic usage: %w", err)
	}
	return nodeapi.TrafficUsageUpdate{
		Previous: nodeapi.TrafficUsage{
			InBytes: update.Previous.Totals.InBytes, OutBytes: update.Previous.Totals.OutBytes,
			CycleStart: update.Previous.CycleStart.Unix(),
		},
		Applied: nodeapi.TrafficUsage{
			InBytes: update.Applied.Totals.InBytes, OutBytes: update.Applied.Totals.OutBytes,
			CycleStart: update.Applied.CycleStart.Unix(),
		},
		Warning: update.Warning,
	}, nil
}

func (h *agentHandler) currentTrafficUsage(cfg deploy.Config) (monitor.TrafficUsage, error) {
	if cfg.DeployMonitor && h.monitor != nil {
		usage, err := h.monitor.trafficUsage()
		if err != nil {
			return monitor.TrafficUsage{}, fmt.Errorf("read active traffic monitor usage: %w", err)
		}
		return usage, nil
	}
	if err := os.MkdirAll(filepath.Dir(h.layout.MonitorDB), 0o755); err != nil {
		return monitor.TrafficUsage{}, fmt.Errorf("create traffic monitor store directory: %w", err)
	}
	now := time.Now().UTC()
	totals, err := monitor.CurrentTrafficTotals(h.layout, cfg.ResetDay, cfg.ResetHour, now)
	if err != nil {
		return monitor.TrafficUsage{}, fmt.Errorf("read stored traffic usage: %w", err)
	}
	return monitor.TrafficUsage{
		Totals: totals, CycleStart: monitor.CycleStart(now, cfg.ResetDay, cfg.ResetHour),
	}, nil
}

func (h *agentHandler) setStoredTrafficUsage(
	cfg deploy.Config,
	expectedCycleStart int64,
	target monitor.TrafficTotals,
) (monitor.TrafficUsageUpdate, error) {
	now := time.Now().UTC()
	cycleStart := monitor.CycleStart(now, cfg.ResetDay, cfg.ResetHour)
	if cycleStart.Unix() != expectedCycleStart {
		return monitor.TrafficUsageUpdate{}, monitor.ErrTrafficCycleChanged
	}
	if err := os.MkdirAll(filepath.Dir(h.layout.MonitorDB), 0o755); err != nil {
		return monitor.TrafficUsageUpdate{}, err
	}
	store, err := monitor.OpenStore(h.layout.MonitorDB)
	if err != nil {
		return monitor.TrafficUsageUpdate{}, err
	}
	defer store.Close()
	previous, err := store.ReplaceTotalsSince(cycleStart.Unix(), now.Unix(), target)
	if err != nil {
		return monitor.TrafficUsageUpdate{}, err
	}
	return monitor.TrafficUsageUpdate{
		Previous: monitor.TrafficUsage{Totals: previous, CycleStart: cycleStart},
		Applied:  monitor.TrafficUsage{Totals: target, CycleStart: cycleStart},
	}, nil
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
	if req.ExpectedProtocolRevision != "" {
		current, err := h.protocolStateUnlocked()
		if err != nil {
			return fmt.Errorf("check protocol revision precondition: %w", err)
		}
		if current.Revision != req.ExpectedProtocolRevision {
			return nodeapi.ProtocolRevisionConflict()
		}
	}
	var (
		cfg deploy.Config
		err error
	)
	if req.ProtocolPatch != nil {
		cfg, err = h.buildProtocolPatchConfig(*req.ProtocolPatch)
	} else {
		cfg, err = h.buildSpokeConfig(req)
	}
	if err != nil {
		return err
	}
	protocolOnly := req.ProtocolPatch != nil || req.ReplaceProtocolState
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
	if !protocolOnly {
		// Remove stale Hub-only services left by an interrupted older
		// deployment before activating the spoke. A protocol-only patch must
		// not touch unrelated service state.
		disableLegacyHubServices(runner, "/etc/systemd/system")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !protocolOnly {
		if err := h.writePushedCertificate(req.Domain, req.CertificatePEM, req.PrivateKeyPEM); err != nil {
			return fmt.Errorf("write certificate: %w", err)
		}
	}
	orchRunner := runner
	if protocolOnly {
		orchRunner = protocolOnlyRunner{Runner: runner}
	}
	orch := h.newSpokeOrchestrator(ctx, req, host, orchRunner, log)
	applyErr := runSpokeDeployment(
		req.ConfigOnly,
		func() error { return orch.Run(ctx, cfg) },
		func() error { return orch.Reconfigure(ctx, cfg) },
	)
	if applyErr != nil {
		return applyErr
	}
	if protocolOnly {
		return nil
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

// protocolOnlyRunner suppresses the systemd manager reload emitted by the
// generic reconfigure path. A protocol-only change still restarts sing-box and
// reloads Nginx, but must not mutate unrelated service-manager state.
type protocolOnlyRunner struct {
	system.Runner
}

func (r protocolOnlyRunner) Run(command system.Command) error {
	if command.Name == "systemctl" && len(command.Args) == 1 && command.Args[0] == "daemon-reload" {
		return nil
	}
	return r.Runner.Run(command)
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
		Runner:        runner,
		Layout:        h.layout,
		SystemdDir:    h.systemdDir,
		NginxConfPath: h.nginxConfPath,
		GOOS:          "linux",
		GOARCH:        host.Arch,
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
		filepath.Join(layout.StateDir, "email"),
		filepath.Join(layout.StateDir, "certmgr_schema_version"),
		filepath.Join(layout.StateDir, "remotes"),
		filepath.Join(layout.StateDir, "monitor_sources"),
		filepath.Join(layout.StateDir, "spoke_subscriptions"),
		filepath.Join(layout.StateDir, "subscription_groups"),
		filepath.Join(layout.StateDir, "subscription_groups.lock"),
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
		maxTeardownFilesSnapshot = 2*nodeapi.MaxAgentBinarySize + (8 << 20)
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
	stopAgentAndOverlayWith(func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	})
}

func stopAgentAndOverlayWith(run func(string, ...string) error) {
	// The durable wg-quick config is already gone, so tear down the simple
	// managed interface directly. Still stop and reset the templated unit:
	// wg-quick is Type=oneshot with RemainAfterExit, so deleting only the link
	// leaves systemd reporting an active deployment after the spoke is gone.
	// Its ExecStop can fail because the config was deliberately removed before
	// this delayed callback; reset-failed turns that expected state inactive.
	_ = run("ip", "link", "delete", "dev", "sbwg0")
	_ = run("systemctl", "stop", "wg-quick@sbwg0.service")
	_ = run("systemctl", "reset-failed", "wg-quick@sbwg0.service")
	_ = run("systemctl", "--no-block", "stop", "singbox-deploy-agent.service")
}

func agentTeardownPaths() []string {
	const agentPath = agentBinaryPath
	return []string{
		"/etc/systemd/system/singbox-deploy-agent.service",
		"/etc/wireguard/sbwg0.conf",
		"/etc/wireguard/sbwg0.conf.singbox-deploy.template",
		"/etc/wireguard/sbwg0.key",
		"/etc/wireguard/sbwg0.key.singbox-deploy.tmp",
		agentPath,
		agentBackupPath(agentPath),
	}
}

// Upgrade validates and stages a hub-supplied agent before atomically replacing
// the current executable. The restart is queued in an independent transient
// systemd timer: scheduling errors are therefore visible while this handler can
// still restore its backup, while successful jobs remain alive after the old
// Agent returns its streamed acknowledgement and exits.
func (h *agentHandler) Upgrade(ctx context.Context, req nodeapi.UpgradeRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	rename := h.renameAgent
	if rename == nil {
		rename = os.Rename
	}
	if h.pendingAgentRestore != nil {
		if err := restoreAgentReplacement(*h.pendingAgentRestore, rename); err != nil {
			return fmt.Errorf("retry previous Agent upgrade recovery: %w", err)
		}
		h.pendingAgentRestore = nil
	}

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
	replacement, err := replaceAgentAtomically(ctx, path, req.Binary, req.Version, inspect, rename)
	if err != nil {
		return err
	}

	schedule := h.scheduleRestart
	if schedule == nil {
		schedule = scheduleAgentRestart
	}
	if err := schedule(); err != nil {
		scheduleErr := fmt.Errorf("schedule Agent service restart: %w", err)
		if restoreErr := restoreAgentReplacement(replacement, rename); restoreErr != nil {
			h.pendingAgentRestore = &replacement
			return errors.Join(scheduleErr, fmt.Errorf("restore previous Agent executable: %w", restoreErr))
		}
		return fmt.Errorf("%w; previous Agent executable restored, upgrade can be retried", scheduleErr)
	}

	h.restartPending = true
	fmt.Fprintf(log, "installed agent %s (%s); independent service restart scheduled\n", req.Version, req.SHA256)
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

type agentReplacement struct {
	path       string
	backupPath string
}

func agentBackupPath(path string) string {
	return path + agentBackupSuffix
}

// replaceAgentAtomically leaves the old executable untouched on every
// validation/staging failure. A synced, byte-verified backup is committed before
// the candidate rename, so a later restart-scheduling failure can restore the
// previously running executable without relying on the Hub connection.
func replaceAgentAtomically(
	ctx context.Context,
	path string,
	binary []byte,
	expectedVersion string,
	inspect func(context.Context, string, string) error,
	rename func(string, string) error,
) (agentReplacement, error) {
	replacement := agentReplacement{path: path, backupPath: agentBackupPath(path)}
	if rename == nil {
		rename = os.Rename
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".upgrade-*")
	if err != nil {
		return replacement, fmt.Errorf("stage agent upgrade: %w", err)
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
		return replacement, fmt.Errorf("write staged agent: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return replacement, fmt.Errorf("chmod staged agent: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return replacement, fmt.Errorf("sync staged agent: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return replacement, fmt.Errorf("close staged agent: %w", err)
	}
	if err := inspect(ctx, tmpPath, expectedVersion); err != nil {
		return replacement, fmt.Errorf("verify staged agent version: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return replacement, err
	}
	if err := copyAgentFileAtomic(path, replacement.backupPath, rename); err != nil {
		return replacement, fmt.Errorf("back up current Agent executable: %w", err)
	}
	if err := rename(tmpPath, path); err != nil {
		removeErr := os.Remove(replacement.backupPath)
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return replacement, errors.Join(
				fmt.Errorf("commit agent upgrade: %w", err),
				fmt.Errorf("remove unused Agent backup: %w", removeErr),
			)
		}
		return replacement, fmt.Errorf("commit agent upgrade: %w", err)
	}
	committed = true
	// Best-effort directory sync makes the rename durable without turning a
	// successfully committed replacement into a reported failure.
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return replacement, nil
}

// restoreAgentReplacement copies rather than consumes the backup, so every
// failure before the final path rename retains a known-good recovery artifact.
func restoreAgentReplacement(replacement agentReplacement, rename func(string, string) error) error {
	if err := copyAgentFileAtomic(replacement.backupPath, replacement.path, rename); err != nil {
		return fmt.Errorf("restore %s from %s: %w (backup retained)",
			replacement.path, replacement.backupPath, err)
	}
	if err := os.Remove(replacement.backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove restored Agent backup %s: %w", replacement.backupPath, err)
	}
	// The restored path was already synced by copyAgentFileAtomic. Syncing the
	// backup removal is best effort; failure here must not turn a completed
	// restoration into a state that blocks the next safe retry.
	if d, err := os.Open(filepath.Dir(replacement.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func copyAgentFileAtomic(sourcePath, targetPath string, rename func(string, string) error) error {
	if rename == nil {
		rename = os.Rename
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %s is not a regular file", sourcePath)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("source %s is empty", sourcePath)
	}

	targetDir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(targetDir, filepath.Base(targetPath)+".copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	sourceHash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sourceHash), source)
	if err != nil {
		return err
	}
	if n != info.Size() {
		return fmt.Errorf("source %s changed while being copied: read %d of %d bytes", sourcePath, n, info.Size())
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stagedHash := sha256.New()
	if _, err := io.Copy(stagedHash, tmp); err != nil {
		return err
	}
	if !bytes.Equal(sourceHash.Sum(nil), stagedHash.Sum(nil)) {
		return fmt.Errorf("staged copy checksum mismatch")
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rename(tmpPath, targetPath); err != nil {
		return err
	}
	committed = true
	dir, err := os.Open(targetDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	return nil
}

func scheduleAgentRestart() error {
	return scheduleAgentRestartWith(func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	})
}

func scheduleAgentRestartWith(run func(string, ...string) ([]byte, error)) error {
	out, err := run(
		"systemd-run",
		"--quiet",
		"--collect",
		"--no-block",
		"--on-active=1s",
		"systemctl",
		"restart",
		"singbox-deploy-agent.service",
	)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
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
	if req.ReplaceProtocolState {
		if err := nodeapi.ValidateProtocolStateReplacement(req); err != nil {
			return deploy.Config{}, err
		}
	}
	var creds deploy.Credentials
	var salt string
	existing, loadErr := deploy.LoadProtocolConfig(h.layout)
	if loadErr == nil {
		creds = existing.Creds
		salt = existing.Salt
	} else {
		if req.ConfigOnly {
			return deploy.Config{}, fmt.Errorf("load current config before reconfigure: %w", loadErr)
		}
		var err error
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
	cfg := deploy.Config{
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
	}
	if req.ConfigOnly && !req.ReplaceProtocolState {
		cfg.Enabled = append([]config.Protocol(nil), existing.Enabled...)
		cfg.Ports = existing.Ports
		cfg.RealityServerName = existing.RealityServerName
		cfg.RealityHandshakePort = existing.RealityHandshakePort
		cfg.Creds = existing.Creds
	} else if req.ConfigOnly && req.ReplaceProtocolState {
		// A complete protocol replacement still owns only protocol fields.
		// Begin from current Agent state so a concurrent monitor/display/site
		// update cannot be overwritten by a stale Hub node snapshot.
		replacement := existing
		replacement.Enabled = append([]config.Protocol(nil), cfg.Enabled...)
		replacement.Ports = cfg.Ports
		replacement.RealityServerName = cfg.RealityServerName
		replacement.RealityHandshakePort = cfg.RealityHandshakePort
		replacement.SpokeMode = true
		replacement.OS = host.OS
		replacement.Firewall = host.Firewall
		cfg = replacement
	}
	return cfg, nil
}

// buildProtocolPatchConfig starts from the Agent's current persisted state and
// changes only the selected protocol's owned credential fields and listen
// port. In particular it never copies general reconfigure fields supplied by
// the Hub request.
func (h *agentHandler) buildProtocolPatchConfig(patch nodeapi.ProtocolPatch) (deploy.Config, error) {
	if err := nodeapi.ValidateProtocolPatch(patch); err != nil {
		return deploy.Config{}, err
	}
	cfg, err := deploy.LoadProtocolConfig(h.layout)
	if err != nil {
		return deploy.Config{}, fmt.Errorf("load current config for protocol patch: %w", err)
	}
	target := config.Protocol(patch.Protocol)
	installed := false
	for _, protocol := range cfg.Enabled {
		if protocol == target {
			installed = true
			break
		}
	}
	if !installed {
		return deploy.Config{}, fmt.Errorf("cannot patch protocol %q because it is not installed", patch.Protocol)
	}
	switch target {
	case config.ProtocolRealityVision:
		cfg.Creds.RealityVisionUUID = patch.Credentials.RealityVisionUUID
		cfg.Ports.RealityVision = patch.Port
	case config.ProtocolRealityGRPC:
		cfg.Creds.RealityGRPCUUID = patch.Credentials.RealityGRPCUUID
		cfg.Ports.RealityGRPC = patch.Port
	case config.ProtocolHysteria2:
		cfg.Creds.HysteriaPassword = patch.Credentials.HysteriaPassword
		cfg.Ports.Hysteria2 = patch.Port
	case config.ProtocolTUIC:
		cfg.Creds.TUICUUID = patch.Credentials.TUICUUID
		cfg.Creds.TUICPassword = patch.Credentials.TUICPassword
		cfg.Ports.TUIC = patch.Port
	case config.ProtocolAnyTLS:
		cfg.Creds.AnyTLSPassword = patch.Credentials.AnyTLSPassword
		cfg.Ports.AnyTLS = patch.Port
	}
	host, err := system.DetectHost()
	if err != nil {
		return deploy.Config{}, fmt.Errorf("detect host for protocol patch: %w", err)
	}
	cfg.SpokeMode = true
	cfg.OS = host.OS
	cfg.Firewall = host.Firewall
	return cfg, nil
}

func protocolCredentialsFromDeploy(creds deploy.Credentials) nodeapi.ProtocolCredentials {
	return nodeapi.ProtocolCredentials{
		RealityVisionUUID: creds.RealityVisionUUID,
		RealityGRPCUUID:   creds.RealityGRPCUUID,
		HysteriaPassword:  creds.HysteriaPassword,
		TUICUUID:          creds.TUICUUID,
		TUICPassword:      creds.TUICPassword,
		AnyTLSPassword:    creds.AnyTLSPassword,
		RealityPrivateKey: creds.RealityPrivateKey,
		RealityPublicKey:  creds.RealityPublicKey,
		RealityShortID:    creds.RealityShortID,
	}
}

func protocolNamesForState(protocols []config.Protocol) []string {
	names := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		names = append(names, string(protocol))
	}
	return names
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
