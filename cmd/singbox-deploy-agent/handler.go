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
	"strings"
	"sync"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/agentfirewall"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

// agentHandler implements nodeapi.Handler by driving the local deploy flow in
// spoke mode.
type agentHandler struct {
	layout  paths.Layout
	monitor *monitorSupervisor

	upgradeMu      sync.Mutex
	restartPending bool
	// Injectable seams keep the on-disk replacement and delayed restart
	// independently testable without touching the running test executable.
	agentExecutable func() (string, error)
	inspectAgent    func(context.Context, string, string) error
	scheduleRestart func()
}

func (h *agentHandler) Health() nodeapi.HealthResponse {
	store := state.NewStore(h.layout.StateDir)
	domain, _ := store.ReadValue("domain", false)
	return nodeapi.HealthResponse{
		OK:            true,
		Version:       version,
		Installed:     domain != "",
		SingBoxActive: singBoxActive(),
		Domain:        domain,
	}
}

func (h *agentHandler) Install(ctx context.Context, req nodeapi.InstallRequest, log io.Writer) error {
	if children, err := nodes.Load(h.layout); err != nil {
		return fmt.Errorf("inspect existing Hub registry before spoke conversion: %w", err)
	} else if len(children) > 0 {
		return fmt.Errorf("cannot convert a Hub managing %d spoke node(s) into a spoke; remove or force-detach its children first", len(children))
	}
	// A spoke may be an existing standalone deployment being enrolled into the
	// new Hub. Stop its old Hub-only daemons before applying config so it cannot
	// renew certificates or serve a second monitor concurrently.
	disableLegacyHubServices(system.NewExecRunner(log))
	if err := h.writePushedCertificate(req.Domain, req.CertificatePEM, req.PrivateKeyPEM); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	cfg, err := h.buildSpokeConfig(req)
	if err != nil {
		return err
	}
	host, err := system.DetectHost()
	if err != nil {
		return fmt.Errorf("detect host: %w", err)
	}
	orch := &deploy.Orchestrator{
		Runner:   system.NewExecRunner(log),
		Layout:   h.layout,
		Releases: release.NewClient("", nil),
		GOOS:     "linux",
		GOARCH:   host.Arch,
		// The hub decides when to (re)install; skip host-side conflict/port gating.
		CheckConflicts: func(context.Context, deploy.Config) error { return nil },
		CheckPorts:     func(context.Context, deploy.Config) error { return nil },
		Progress: func(e deploy.Event) {
			fmt.Fprintf(log, "[%d/%d] %s: %s\n", e.Index, e.Total, e.Label, e.Detail)
		},
	}
	var applyErr error
	if req.ConfigOnly {
		applyErr = orch.Reconfigure(ctx, cfg)
	} else {
		applyErr = orch.Run(ctx, cfg)
	}
	if applyErr != nil {
		return applyErr
	}
	if err := removeLegacyHubArtifacts(h.layout); err != nil {
		return fmt.Errorf("remove legacy standalone management artifacts: %w", err)
	}
	if err := system.NewExecRunner(log).Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return fmt.Errorf("reload systemd after spoke migration: %w", err)
	}
	h.monitor.reload()
	return nil
}

func disableLegacyHubServices(runner system.Runner) {
	for _, unit := range []string{system.CertRenewTimer, system.CertRenewService, system.MonitorService} {
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

func (h *agentHandler) ApplyCert(_ context.Context, req nodeapi.CertRequest, log io.Writer) error {
	certPath, keyPath := certmgr.CertPaths(h.layout, req.Domain)
	oldCert, certErr := os.ReadFile(certPath)
	oldKey, keyErr := os.ReadFile(keyPath)
	if err := h.writePushedCertificate(req.Domain, req.CertificatePEM, req.PrivateKeyPEM); err != nil {
		return err
	}
	fmt.Fprintf(log, "installed refreshed certificate for %s\n", req.Domain)
	runner := system.NewExecRunner(log)
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

func (h *agentHandler) Uninstall(ctx context.Context, req nodeapi.UninstallRequest, log io.Writer) error {
	if h.monitor != nil {
		h.monitor.stop()
	}
	agentStateDir := filepath.Join(h.layout.StateDir, "agent")
	var firewallRule agentfirewall.Rule
	var hasFirewallRule bool
	if !req.KeepOverlay {
		var err error
		firewallRule, hasFirewallRule, err = agentfirewall.Load(agentStateDir)
		if err != nil {
			return fmt.Errorf("load Agent firewall state: %w", err)
		}
	}
	runner := system.NewExecRunner(log)
	if err := uninstall.Run(ctx, uninstall.Options{
		Runner:              runner,
		Layout:              h.layout,
		DeleteRuntime:       true,
		DeleteCertificates:  true,
		DeleteMonitorDB:     true,
		DeleteSite:          true,
		DeleteSubscriptions: true,
		Progress: func(e deploy.Event) {
			fmt.Fprintf(log, "[%d/%d] %s: %s\n", e.Index, e.Total, e.Label, e.Detail)
		},
	}); err != nil {
		return err
	}
	if !req.KeepOverlay {
		if hasFirewallRule {
			removeCommands, err := firewallRule.RemoveCommands()
			if err == nil {
				err = deploy.RunCommands(runner, removeCommands...)
			}
			if err != nil {
				// uninstall.Run may already have removed StateDir. Restore the
				// exact rule metadata so a retained Agent/overlay can retry.
				if saveErr := agentfirewall.Save(agentStateDir, firewallRule); saveErr != nil {
					err = errors.Join(err, fmt.Errorf("restore Agent firewall cleanup state: %w", saveErr))
				}
				return fmt.Errorf("remove Agent firewall rule: %w", err)
			}
		}
		fmt.Fprintln(log, "spoke runtime removed; agent and WireGuard teardown scheduled")
		time.AfterFunc(750*time.Millisecond, func() { teardownAgentAndOverlay(h.layout) })
	}
	return nil
}

func teardownAgentAndOverlay(layout paths.Layout) {
	// Remove durable material first while this process and tunnel are still
	// alive, then queue service stops without blocking on our own termination.
	agentDir := filepath.Join(layout.StateDir, "agent")
	for _, path := range agentTeardownPaths() {
		_ = os.Remove(path)
	}
	_ = os.RemoveAll(agentDir)
	_ = exec.Command("systemctl", "disable", "singbox-deploy-agent.service").Run()
	_ = exec.Command("systemctl", "disable", "wg-quick@sbwg0.service").Run()
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "--no-block", "stop", "wg-quick@sbwg0.service").Run()
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
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.restartPending {
		return fmt.Errorf("an agent upgrade has already committed; restart is pending")
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

func singBoxActive() bool {
	err := exec.Command("systemctl", "is-active", "--quiet", system.SingBoxService).Run()
	return err == nil
}
