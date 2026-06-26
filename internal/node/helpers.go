package node

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Upgrader is the function called by handleUpgrade. Tests substitute this
// hook so they don't reach out to GitHub.
type Upgrader func(ctx context.Context, s *Server, req cluster.UpgradeRequest) error

// Teardowner is the function called by handleTeardown. Tests substitute this
// to verify cleanup without actually exiting.
type Teardowner func(s *Server) error

// defaultUpgrader downloads new singbox-node, singbox-monitor, and (optional)
// sing-box core binaries from GitHub Release, replaces them in place, and
// restarts the affected systemd units. The upgrade is best-effort: a failure
// in one binary fails the whole call so the master can retry.
func defaultUpgrader(ctx context.Context, s *Server, req cluster.UpgradeRequest) error {
	if strings.TrimSpace(req.Version) != "" {
		if err := upgradeNodeBinaries(ctx, req.Version); err != nil {
			return fmt.Errorf("upgrade node binaries: %w", err)
		}
		if err := s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
			return err
		}
		if err := s.Runner.Run(system.Systemctl("restart", "singbox-node.service")); err != nil {
			return err
		}
		if err := s.Runner.Run(system.Systemctl("restart", system.MonitorService)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.CoreVersion) != "" {
		if err := upgradeSingBoxCore(ctx, s.Layout, req.CoreVersion); err != nil {
			return fmt.Errorf("upgrade sing-box core: %w", err)
		}
		if err := s.Runner.Run(system.Systemctl("restart", system.SingBoxService)); err != nil {
			return err
		}
	}
	return nil
}

func upgradeNodeBinaries(ctx context.Context, version string) error {
	for _, name := range []string{"singbox-node", "singbox-monitor"} {
		if err := downloadAgentBinary(ctx, version, name); err != nil {
			return err
		}
	}
	return nil
}

// downloadAgentBinary fetches the given release asset (singbox-node or
// singbox-monitor) for the agent's architecture and atomically replaces the
// binary under /usr/bin.
func downloadAgentBinary(ctx context.Context, version, name string) error {
	const repo = "C5Hwang/singbox-deploy"
	asset := fmt.Sprintf("%s-linux-%s", name, goArch())
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, version, asset)
	dest := filepath.Join("/usr/bin", name)
	tmp := dest + ".new"
	if err := release.DownloadTo(ctx, nil, url, tmp); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}

// ensureMonitorBinary makes sure /usr/bin/singbox-monitor exists before the
// monitor unit is (re-)rendered. When add-node sets MonitorEnabled=false (or a
// later TUI edit tears down monitor with Disabled=true), the SSH install path
// skips shipping the binary / the teardown deletes it. A subsequent
// UpdateMonitor that flips the unit back on would otherwise daemon-reload an
// ExecStart pointing at a missing binary and silently fail to restart. Fetches
// from the same GitHub release tag the agent itself was installed with so the
// re-shipped binary matches the rest of the install.
func ensureMonitorBinary(ctx context.Context, version string) error {
	if _, err := os.Stat("/usr/bin/singbox-monitor"); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat singbox-monitor: %w", err)
	}
	if v := strings.TrimSpace(version); v == "" || v == "dev" {
		return fmt.Errorf("singbox-monitor missing and agent has no release version (%q) to fetch it from", version)
	}
	return downloadAgentBinary(ctx, version, "singbox-monitor")
}

func upgradeSingBoxCore(ctx context.Context, layout paths.Layout, coreVersion string) error {
	archive := release.SingBoxArchiveName("v"+strings.TrimPrefix(coreVersion, "v"), "linux", goArch())
	url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/%s", strings.TrimPrefix(coreVersion, "v"), archive)
	tmp := filepath.Join(filepath.Dir(layout.SingBoxBin), archive)
	if err := release.DownloadTo(ctx, nil, url, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	return release.ExtractSingBox(f, layout.SingBoxBin)
}

func goArch() string {
	if a := os.Getenv("SINGBOX_DEPLOY_ARCH"); a != "" {
		return a
	}
	return runtime.GOARCH
}

// defaultTeardown runs the full local uninstall sequence on the node.
func defaultTeardown(s *Server) error {
	// Stop services first so they don't interfere with cleanup.
	for _, unit := range []string{system.MonitorService, system.SingBoxService, "singbox-node.service", "nginx.service"} {
		_ = s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"disable", "--now", unit}})
	}
	// Stop and disable WireGuard.
	_ = s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"disable", "--now", "wg-quick@wg-sdeploy.service"}})

	// Remove systemd units the agent owns.
	for _, unit := range []string{
		"singbox-node.service",
		system.MonitorService,
		system.SingBoxService,
		system.CertRenewService,
		system.CertRenewTimer,
	} {
		_ = os.Remove(filepath.Join("/etc/systemd/system", unit))
	}
	_ = s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}})

	// Remove the layout root and binaries.
	_ = os.RemoveAll(s.Layout.Root)
	_ = os.Remove("/usr/bin/singbox-node")
	_ = os.Remove("/usr/bin/singbox-monitor")
	_ = os.Remove("/etc/wireguard/wg-sdeploy.conf")
	_ = os.Remove("/etc/nginx/conf.d/singbox-deploy.conf")
	return nil
}

// tlsPaths returns the certificate/key paths under the layout for a domain.
func tlsPaths(layout paths.Layout, domain string) (string, string) {
	return filepath.Join(layout.TLSDir, domain+".crt"),
		filepath.Join(layout.TLSDir, domain+".key")
}

// serverOptions converts a deploy.Config into config.ServerOptions for the
// renderer. Mirrors deploy.Config.serverOptions which is unexported.
func serverOptions(cfg deploy.Config, certPath, keyPath string) config.ServerOptions {
	return config.ServerOptions{
		Domain:            cfg.Domain,
		TLSCert:           certPath,
		TLSKey:            keyPath,
		RealityPrivateKey: cfg.Creds.RealityPrivateKey,
		RealityServerName: cfg.RealityServerName,
		RealityPort:       realityHandshakePort(cfg),
		RealityShortID:    cfg.Creds.RealityShortID,
		SubscribePort:     cfg.SubscribePort,
		User: config.UserCredentials{
			DisplayName:       cfg.DisplayName,
			RealityVisionUUID: cfg.Creds.RealityVisionUUID,
			RealityGRPCUUID:   cfg.Creds.RealityGRPCUUID,
			HysteriaPassword:  cfg.Creds.HysteriaPassword,
			TUICUUID:          cfg.Creds.TUICUUID,
			TUICPassword:      cfg.Creds.TUICPassword,
			AnyTLSPassword:    cfg.Creds.AnyTLSPassword,
		},
		Ports:   cfg.Ports,
		Enabled: cfg.Enabled,
	}
}

func realityHandshakePort(cfg deploy.Config) int {
	if cfg.RealityHandshakePort > 0 {
		return cfg.RealityHandshakePort
	}
	return config.DefaultRealityHandshakePort
}

func isUnitActive(runner system.Runner, unit string) bool {
	// system.Runner doesn't return stdout — we use a one-shot exec to check.
	// Best-effort: if runner is the default ExecRunner, we can shell out
	// directly. Otherwise we conservatively return false.
	if _, ok := runner.(*system.ExecRunner); !ok {
		return false
	}
	cmd := system.Command{Name: "systemctl", Args: []string{"is-active", "--quiet", unit}}
	err := runner.Run(cmd)
	return err == nil
}

func readCoreVersion(binPath string) string {
	// Best-effort: parse `sing-box version` output. Returns empty string on
	// any failure since this is purely informational.
	if _, err := os.Stat(binPath); err != nil {
		return ""
	}
	return "" // populated by the upgrade path; querying live version requires sing-box's CLI
}

func certExpiryISO(layout paths.Layout, domain string) string {
	if strings.TrimSpace(domain) == "" {
		return ""
	}
	certPath, _ := tlsPaths(layout, domain)
	b, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return cert.NotAfter.UTC().Format(time.RFC3339)
}

func formatUptime(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	total := int(d.Seconds())
	days := total / 86400
	total %= 86400
	hours := total / 3600
	total %= 3600
	mins := total / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return strconv.Itoa(mins) + "m"
}

func writeMonitorState(layout paths.Layout, req cluster.MonitorUpdate) error {
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		cfg = deploy.Config{}
	}
	if req.Interface != "" {
		cfg.MonitorInterface = req.Interface
	}
	if req.SamplingInterval != "" {
		if d, err := time.ParseDuration(req.SamplingInterval); err == nil {
			cfg.MonitorIntervalSeconds = int(d.Seconds())
		}
	}
	cfg.TrafficInLimitBytes = req.InLimitBytes
	cfg.TrafficOutLimitBytes = req.OutLimitBytes
	cfg.TrafficTotalLimitBytes = req.TotalLimitBytes
	if req.ResetDay > 0 {
		cfg.ResetDay = req.ResetDay
	}
	cfg.ResetHour = req.ResetHour
	if strings.TrimSpace(req.Alias) != "" {
		cfg.MonitorAlias = req.Alias
	}
	return deploy.WriteInstallState(layout.StateDir, cfg)
}

// writeNodeMonitorUnit renders /etc/systemd/system/singbox-deploy-monitor.service
// from the master-supplied MonitorUpdate values, binding the listen address
// to the agent's own WireGuard IP on the well-known monitor port so the
// master can pull aggregated samples over the internal subnet. Called by
// handleMonitorConfig on every non-disable update so the unit's ExecStart
// always reflects the latest limits/reset/interval/alias.
func writeNodeMonitorUnit(layout paths.Layout, wgIP string, req cluster.MonitorUpdate) error {
	intervalSeconds := 0
	if req.SamplingInterval != "" {
		if d, err := time.ParseDuration(req.SamplingInterval); err == nil {
			intervalSeconds = int(d.Seconds())
		}
	}
	cfg := deploy.Config{
		TrafficInLimitBytes:    req.InLimitBytes,
		TrafficOutLimitBytes:   req.OutLimitBytes,
		TrafficTotalLimitBytes: req.TotalLimitBytes,
		ResetDay:               req.ResetDay,
		ResetHour:              req.ResetHour,
		MonitorAlias:           req.Alias,
		MonitorInterface:       req.Interface,
		MonitorIntervalSeconds: intervalSeconds,
	}
	listen := fmt.Sprintf("%s:%d", wgIP, deploy.DefaultMonitorPort)
	unit, err := deploy.RenderMonitorUnitWithListen(layout, "/usr/bin/singbox-monitor", cfg, listen)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("/etc/systemd/system", system.MonitorService), []byte(unit), 0o644)
}
