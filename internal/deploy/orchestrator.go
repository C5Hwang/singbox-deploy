package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// Event reports the progress of one install step to the UI.
type Event struct {
	Index  int    // 1-based step number
	Total  int    // total steps
	Label  string // short step name
	Detail string // current action summary
	Status string // "running", "ok", or "fail"
	Err    error  // set when Status == "fail"
}

// Orchestrator runs the full install flow. System mutations go through Runner;
// files are written under Layout; network operations are injectable hooks so
// the flow can be tested with a recording runner and a temporary root.
type Orchestrator struct {
	Runner system.Runner
	Layout paths.Layout
	// CertManager issues the hub's certificate via DNS-01. Spokes never issue
	// (the hub pushes their pair), so it is unused in spoke mode.
	CertManager *certmgr.Manager
	Releases    *release.Client

	// Hooks (nil values fall back to real implementations in Run).
	Download       func(ctx context.Context, url, dest string) error
	LatestSingBox  func(ctx context.Context) (string, error)
	CheckConflicts func(ctx context.Context, cfg Config) error
	CheckPorts     func(ctx context.Context, cfg Config) error
	// CheckReconfigurePorts validates only newly-added listen sockets while the
	// currently-managed sockets remain active.
	CheckReconfigurePorts func(ctx context.Context, cfg Config, added []system.Port) error
	Progress              func(Event)

	GOOS, GOARCH  string
	DeployBin     string // path to the singbox-deploy binary (for the monitor unit)
	SystemdDir    string // default /etc/systemd/system
	NginxConfPath string // default /etc/nginx/conf.d/singbox-deploy.conf
}

// step is one labeled install action.
type step struct {
	label  string
	detail string
	run    func(ctx context.Context, cfg Config) error
}

// certPaths returns the certificate and key paths for the domain.
func (o *Orchestrator) certPaths(cfg Config) (cert, key string) {
	return CertificatePaths(o.Layout, cfg.Domain)
}

// defaults fills unset fields with production values.
func (o *Orchestrator) defaults() {
	if o.SystemdDir == "" {
		o.SystemdDir = "/etc/systemd/system"
	}
	if o.NginxConfPath == "" {
		o.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	if o.GOOS == "" {
		o.GOOS = "linux"
	}
	if o.GOARCH == "" {
		o.GOARCH = "amd64"
	}
	if o.DeployBin == "" {
		o.DeployBin = "/usr/bin/singbox-deploy"
	}
	if o.Download == nil {
		o.Download = func(ctx context.Context, url, dest string) error {
			return release.DownloadTo(ctx, nil, url, dest)
		}
	}
	if o.LatestSingBox == nil && o.Releases != nil {
		o.LatestSingBox = func(ctx context.Context) (string, error) {
			return o.Releases.LatestStable(ctx, "SagerNet", "sing-box")
		}
	}
	if o.CheckConflicts == nil {
		o.CheckConflicts = o.checkConflicts
	}
	if o.CheckPorts == nil {
		o.CheckPorts = o.checkPorts
	}
	if o.CheckReconfigurePorts == nil {
		o.CheckReconfigurePorts = func(ctx context.Context, cfg Config, added []system.Port) error {
			if err := cfg.ValidatePorts(); err != nil {
				return err
			}
			return system.CheckPorts(ctx, "", added)
		}
	}
	if o.CertManager == nil {
		o.CertManager = &certmgr.Manager{Layout: o.Layout}
	}
}

// steps returns the ordered install steps.
func (o *Orchestrator) steps(cfg Config) []step {
	steps := []step{
		{"Conflict check", "detect existing sing-box service or binary", o.stepConflictCheck},
		{"Stop services", "stop managed services so a reinstall can rebind ports", o.stepStopManaged},
		{"Port check", "check required ports are free and publicly reachable", o.stepPortCheck},
		{"Dependencies", "install base packages", o.stepDependencies},
		{"Nginx", "install nginx.org mainline", o.stepNginxInstall},
		{"Firewall", "open required ports", o.stepFirewall},
		{"Certificates", "reuse or obtain TLS certificate", o.stepCertificates},
		{"sing-box core", "download latest stable", o.stepSingBox},
		{"Config", "generate and validate config.json", o.stepConfig},
		{"Services", "install and start sing-box.service", o.stepServices},
		{"Subscriptions", "generate subscription files", o.stepSubscriptions},
		{"Nginx config", "write managed config, deploy site, and reload", o.stepNginxConfig},
	}
	// On a spoke the agent daemon runs the monitor sampler in-process, so no
	// standalone monitor unit is installed; the hub still installs one.
	if cfg.DeployMonitor && !cfg.SpokeMode {
		steps = append(steps, step{"Monitor", "install and start monitor", o.stepMonitor})
	}
	steps = append(steps, step{"Finalize", "write account state", o.stepFinalize})
	return steps
}

// Run executes every step in order, emitting progress. It stops at the first
// failing step and returns its error.
func (o *Orchestrator) Run(ctx context.Context, cfg Config) error {
	o.defaults()
	local := o.steps(cfg)
	steps := make([]Step, len(local))
	for i, s := range local {
		steps[i] = Step{Label: s.label, Detail: s.detail, Run: func(ctx context.Context) error { return s.run(ctx, cfg) }}
	}
	return RunSteps(ctx, o.Progress, steps)
}

// Reconfigure applies an already-installed node's desired configuration
// without reinstalling packages, Nginx, or the sing-box core. Certificates are
// validated and written by the spoke agent before this method is called.
func (o *Orchestrator) Reconfigure(ctx context.Context, cfg Config) (retErr error) {
	o.defaults()
	old, err := LoadProtocolConfig(o.Layout)
	if err != nil {
		return fmt.Errorf("load current configuration before reconfigure: %w", err)
	}
	// These runtime-only fields are intentionally not persisted by
	// LoadProtocolConfig but affect the managed firewall port set.
	old.SpokeMode = cfg.SpokeMode
	old.Firewall = cfg.Firewall
	added, stale := firewallPortChanges(old, cfg)
	candidate := ProtocolConfigCandidate(o.Layout)
	firewallTouched := false
	activated := false
	defer func() {
		_ = os.Remove(candidate)
		if retErr == nil || !firewallTouched || activated || cfg.Firewall == system.FirewallNone {
			return
		}
		// A failure before the new config was activated must not leave newly
		// opened ports behind. Cleanup errors are joined so operators can see
		// that manual firewall repair may be required.
		if cleanupErr := o.run(system.FirewallRemoveCommands(cfg.Firewall, added)...); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("roll back newly-opened firewall ports: %w", cleanupErr))
		}
	}()

	oldConfig, err := os.ReadFile(o.Layout.ConfigJSON)
	if err != nil {
		return fmt.Errorf("read current sing-box config before reconfigure: %w", err)
	}
	local := []step{
		{"Port check", "check new or changed protocol ports", func(ctx context.Context, cfg Config) error {
			return o.CheckReconfigurePorts(ctx, cfg, added)
		}},
		{"Firewall", "open new or changed protocol ports", func(context.Context, Config) error {
			if cfg.Firewall == system.FirewallNone || len(added) == 0 {
				return nil
			}
			firewallTouched = true
			return o.run(system.FirewallCommands(cfg.Firewall, added)...)
		}},
		{"Config", "render and validate candidate config.json", o.stepReconfigureConfig},
		{"Services", "activate the validated config and reload sing-box", func(ctx context.Context, cfg Config) error {
			if err := os.Rename(candidate, o.Layout.ConfigJSON); err != nil {
				return err
			}
			if err := o.stepServices(ctx, cfg); err != nil {
				// If an old config existed, restore it and restart the old
				// service. Retain the new firewall rule only when restoration
				// itself fails and the active service state is uncertain.
				restoreErr := WriteFile(o.Layout.ConfigJSON, oldConfig, 0o600)
				if restoreErr == nil {
					restoreErr = o.Runner.Run(system.Systemctl("restart", system.SingBoxService))
				}
				if restoreErr != nil {
					activated = true
					return errors.Join(err, fmt.Errorf("restore previous sing-box config: %w", restoreErr))
				}
				return err
			}
			activated = true
			return nil
		}},
		{"Finalize", "persist desired node state", o.stepFinalize},
		{"Firewall cleanup", "close removed or superseded protocol ports", func(context.Context, Config) error {
			if cfg.Firewall == system.FirewallNone || len(stale) == 0 {
				return nil
			}
			return o.run(system.FirewallRemoveCommands(cfg.Firewall, stale)...)
		}},
		{"Subscriptions", "regenerate private node subscription data", o.stepSubscriptions},
		{"Nginx config", "rewrite managed config and reload", o.stepNginxConfig},
	}
	steps := make([]Step, len(local))
	for i, s := range local {
		steps[i] = Step{Label: s.label, Detail: s.detail, Run: func(ctx context.Context) error { return s.run(ctx, cfg) }}
	}
	return RunSteps(ctx, o.Progress, steps)
}

func (o *Orchestrator) stepReconfigureConfig(_ context.Context, cfg Config) error {
	if err := WriteProtocolConfigCandidate(o.Layout, cfg); err != nil {
		return err
	}
	return o.run(system.Command{Name: o.Layout.SingBoxBin, Args: []string{"check", "-c", ProtocolConfigCandidate(o.Layout)}})
}

func firewallPortChanges(old, next Config) (added, stale []system.Port) {
	oldPorts := old.firewallPorts()
	nextPorts := next.firewallPorts()
	oldSet := make(map[string]bool, len(oldPorts))
	nextSet := make(map[string]bool, len(nextPorts))
	for _, port := range oldPorts {
		oldSet[firewallPortKey(port)] = true
	}
	for _, port := range nextPorts {
		nextSet[firewallPortKey(port)] = true
		if !oldSet[firewallPortKey(port)] {
			added = append(added, port)
		}
	}
	for _, port := range oldPorts {
		if !nextSet[firewallPortKey(port)] {
			stale = append(stale, port)
		}
	}
	return added, stale
}

func firewallPortKey(port system.Port) string {
	return fmt.Sprintf("%s/%d", strings.ToLower(port.Proto), port.Number)
}

func (o *Orchestrator) run(cmds ...system.Command) error {
	return RunCommands(o.Runner, cmds...)
}

// --- steps ---

func (o *Orchestrator) stepConflictCheck(ctx context.Context, cfg Config) error {
	return o.CheckConflicts(ctx, cfg)
}

func (o *Orchestrator) stepPortCheck(ctx context.Context, cfg Config) error {
	return o.CheckPorts(ctx, cfg)
}

// stepStopManaged stops the managed sing-box and nginx services before the port
// check so reinstalling a running deployment does not fail on ports the old
// services still hold. Errors are ignored: on a fresh install nothing is
// running, and stepServices/stepNginxConfig restart them with the new config.
func (o *Orchestrator) stepStopManaged(_ context.Context, _ Config) error {
	_ = o.Runner.Run(system.Systemctl("stop", system.SingBoxService))
	_ = o.Runner.Run(system.Systemctl("stop", "nginx"))
	return nil
}

func (o *Orchestrator) stepDependencies(_ context.Context, cfg Config) error {
	return system.RunInstallPlan(o.Runner, system.BuildInstallPlan(cfg.OS))
}

func (o *Orchestrator) stepNginxInstall(_ context.Context, cfg Config) error {
	return o.run(NginxInstallCommands(cfg.OS)...)
}

func (o *Orchestrator) stepFirewall(_ context.Context, cfg Config) error {
	if cfg.Firewall == system.FirewallNone {
		return nil
	}
	return o.run(system.FirewallCommands(cfg.Firewall, cfg.firewallPorts())...)
}

func (o *Orchestrator) stepCertificates(ctx context.Context, cfg Config) error {
	if err := o.ensureCertificate(ctx, cfg); err != nil {
		return err
	}
	// Track the hub's own certificate in the central inventory so it is renewed
	// and shown alongside the spokes'. Spoke certificates are registered on the
	// hub when the node is added, not here.
	if !cfg.SpokeMode {
		if err := certmgr.Register(o.Layout, cfg.Domain); err != nil {
			return err
		}
	}
	return nil
}

// ensureCertificate guarantees a usable certificate pair exists on disk for the
// domain. It reuses a valid managed pair or an existing Let's Encrypt cert, and
// otherwise issues one via DNS-01 through the central certificate manager. A
// spoke never issues: the hub pushes its pair before install, so a missing pair
// is a provisioning error.
func (o *Orchestrator) ensureCertificate(ctx context.Context, cfg Config) error {
	certPath, keyPath := o.certPaths(cfg)
	if ok, err := certificatePairUsable(certPath, keyPath, cfg.Domain, time.Now()); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("check existing certificate: %w", err)
		}
	} else if ok {
		return nil
	}
	if cfg.SpokeMode {
		return fmt.Errorf("certificate for %s was not provisioned by the hub", cfg.Domain)
	}
	if ok, err := o.importExistingCertificate(cfg, certPath, keyPath); err != nil {
		return err
	} else if ok {
		return nil
	}
	// DNS-01 needs no port 80, so Nginx keeps serving throughout issuance. The
	// certificate manager writes the pair to the managed paths and registers it.
	if o.CertManager == nil {
		return fmt.Errorf("no certificate manager configured")
	}
	if _, err := o.CertManager.Issue(ctx, cfg.Domain); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) stepSingBox(ctx context.Context, cfg Config) error {
	if o.LatestSingBox == nil {
		return fmt.Errorf("no sing-box release resolver configured")
	}
	tag, err := o.LatestSingBox(ctx)
	if err != nil {
		return err
	}
	archive := release.SingBoxArchiveName(tag, o.GOOS, o.GOARCH)
	url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", tag, archive)
	archivePath := filepath.Join(filepath.Dir(o.Layout.SingBoxBin), archive)
	if err := o.Download(ctx, url, archivePath); err != nil {
		return err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(archivePath)
	return release.ExtractSingBox(f, o.Layout.SingBoxBin)
}

func (o *Orchestrator) stepConfig(_ context.Context, cfg Config) error {
	certPath, keyPath := o.certPaths(cfg)
	cfgBytes, err := config.Build(cfg.serverOptions(certPath, keyPath))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.Layout.FragmentsDir, 0o755); err != nil {
		return err
	}
	// The rendered config embeds the Reality private key and all user
	// passwords; keep it root-only (sing-box runs as root).
	if err := WriteFile(o.Layout.ConfigJSON, cfgBytes, 0o600); err != nil {
		return err
	}
	return o.run(system.Command{Name: o.Layout.SingBoxBin, Args: []string{"check", "-c", o.Layout.ConfigJSON}})
}

func (o *Orchestrator) stepServices(_ context.Context, cfg Config) error {
	if err := o.writeCertificateRenewalState(cfg); err != nil {
		return err
	}
	unit, err := templatefs.Render("service/sing-box.service.tmpl", map[string]any{
		"SingBoxBin": o.Layout.SingBoxBin,
		"ConfigPath": o.Layout.ConfigJSON,
	})
	if err != nil {
		return err
	}
	if err := WriteFile(filepath.Join(o.SystemdDir, system.SingBoxService), []byte(unit), 0o644); err != nil {
		return err
	}
	cmds := []system.Command{
		{Name: "systemctl", Args: []string{"daemon-reload"}},
		{Name: "systemctl", Args: []string{"enable", system.SingBoxService}},
		system.Systemctl("restart", system.SingBoxService),
	}
	// The cert-renew timer lives only on the hub: it renews every certificate in
	// the inventory (its own and each spoke's) and pushes refreshed pairs to the
	// spokes. Spokes never run ACME, so they get no timer.
	if !cfg.SpokeMode {
		renewUnit, err := templatefs.Render("service/singbox-deploy-cert-renew.service.tmpl", map[string]any{
			"DeployBin":     o.DeployBin,
			"ThresholdDays": 30,
		})
		if err != nil {
			return err
		}
		if err := WriteFile(filepath.Join(o.SystemdDir, system.CertRenewService), []byte(renewUnit), 0o644); err != nil {
			return err
		}
		renewTimer, err := templatefs.Render("service/singbox-deploy-cert-renew.timer.tmpl", map[string]any{})
		if err != nil {
			return err
		}
		if err := WriteFile(filepath.Join(o.SystemdDir, system.CertRenewTimer), []byte(renewTimer), 0o644); err != nil {
			return err
		}
		cmds = append(cmds, system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.CertRenewTimer}})
	}
	// enable + restart (not enable --now): on a reinstall the service is often
	// already active, where --now is a no-op that would leave the old config
	// loaded. restart guarantees the freshly written config.json takes effect.
	return o.run(cmds...)
}

func (o *Orchestrator) stepSubscriptions(_ context.Context, cfg Config) error {
	// Write this node's own subscription outputs only. On the hub, spoke nodes
	// are folded in afterwards over the overlay (hubctl.RefreshSubscriptions);
	// on a spoke, these local outputs are what the hub fetches to aggregate.
	return WriteSubscriptions(o.Layout, cfg)
}

func (o *Orchestrator) stepNginxConfig(_ context.Context, cfg Config) error {
	_ = os.Remove(filepath.Join(filepath.Dir(o.NginxConfPath), "default.conf"))
	if err := WriteManagedNginxConfig(o.Layout, cfg, o.NginxConfPath); err != nil {
		return err
	}
	if err := deploySiteTemplate(o.Layout, cfg.SiteTemplate); err != nil {
		return err
	}
	return o.run(
		system.Command{Name: "nginx", Args: []string{"-t"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", "nginx"}},
		system.Systemctl("restart", "nginx"),
	)
}

func (o *Orchestrator) stepMonitor(_ context.Context, cfg Config) error {
	if !cfg.DeployMonitor {
		return nil
	}
	unit, err := RenderMonitorUnit(o.Layout, o.DeployBin, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Layout.MonitorDB), 0o755); err != nil {
		return err
	}
	if err := WriteFile(filepath.Join(o.SystemdDir, system.MonitorService), []byte(unit), 0o644); err != nil {
		return err
	}
	return o.run(
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.MonitorService}},
	)
}

// RenderMonitorUnit renders the systemd unit file for the monitor service.
func RenderMonitorUnit(layout paths.Layout, deployBin string, cfg Config) (string, error) {
	interval := cfg.MonitorIntervalSeconds
	if interval <= 0 {
		interval = DefaultMonitorIntervalSeconds
	}
	return templatefs.Render("service/singbox-deploy-monitor.service.tmpl", map[string]any{
		"DeployBin":       deployBin,
		"MonitorPort":     cfg.MonitorPort,
		"Interface":       cfg.MonitorInterface,
		"DB":              layout.MonitorDB,
		"InLimitBytes":    cfg.TrafficInLimitBytes,
		"OutLimitBytes":   cfg.TrafficOutLimitBytes,
		"TotalLimitBytes": cfg.TrafficTotalLimitBytes,
		"ResetDay":        cfg.ResetDay,
		"ResetHour":       cfg.ResetHour,
		"MonitorAlias":    cfg.MonitorAlias,
		"IntervalSeconds": interval,
		"RemoteMonitor":   RemoteMonitorPath(layout),
	})
}

func (o *Orchestrator) stepFinalize(_ context.Context, cfg Config) error {
	return WriteInstallState(o.Layout.StateDir, cfg)
}

// WriteInstallState persists the full install config as individual state files.
func WriteInstallState(stateDir string, cfg Config) error {
	state := map[string]string{
		"enabled_protocols":      protocolStateValue(cfg.EnabledProtocols()),
		"display_name":           cfg.DisplayName,
		"subscribe_salt":         cfg.Salt,
		"site_template":          cfg.siteTemplate(),
		"reality_public_key":     cfg.Creds.RealityPublicKey,
		"reality_private_key":    cfg.Creds.RealityPrivateKey,
		"reality_short_id":       cfg.Creds.RealityShortID,
		"reality_server_name":    cfg.RealityServerName,
		"reality_handshake_port": itoa(cfg.realityHandshakePort()),
		"reality_vision_uuid":    cfg.Creds.RealityVisionUUID,
		"reality_grpc_uuid":      cfg.Creds.RealityGRPCUUID,
		"hysteria2_password":     cfg.Creds.HysteriaPassword,
		"tuic_uuid":              cfg.Creds.TUICUUID,
		"tuic_password":          cfg.Creds.TUICPassword,
		"anytls_password":        cfg.Creds.AnyTLSPassword,
		"reality_vision_port":    itoa(cfg.Ports.RealityVision),
		"reality_grpc_port":      itoa(cfg.Ports.RealityGRPC),
		"hysteria2_port":         itoa(cfg.Ports.Hysteria2),
		"tuic_port":              itoa(cfg.Ports.TUIC),
		"anytls_port":            itoa(cfg.Ports.AnyTLS),
		"subscribe_port":         itoa(cfg.SubscribePort),
		"monitor_public_port":    itoa(cfg.MonitorPublicPort),
		"monitor_port":           itoa(cfg.MonitorPort),
		"monitor_interface":      cfg.MonitorInterface,
		"monitor":                yesNoString(cfg.DeployMonitor),
		"monitor_frontend":       yesNoString(cfg.DeployMonitorFrontend),
	}
	if cfg.DeployMonitor {
		state["monitor_alias"] = cfg.MonitorAlias
		state["traffic_in_limit_bytes"] = fmt.Sprintf("%d", cfg.TrafficInLimitBytes)
		state["traffic_out_limit_bytes"] = fmt.Sprintf("%d", cfg.TrafficOutLimitBytes)
		state["traffic_total_limit_bytes"] = fmt.Sprintf("%d", cfg.TrafficTotalLimitBytes)
		state["reset_day"] = itoa(cfg.ResetDay)
		state["reset_hour"] = itoa(cfg.ResetHour)
		state["monitor_interval_seconds"] = itoa(cfg.MonitorIntervalSeconds)
	}
	// Renewal keys come from the single definition in certificateRenewalState.
	for name, value := range certificateRenewalState(cfg) {
		state[name] = value
	}
	// PublicIP is captured by the interactive domain validation. Agent-driven
	// spoke installs do not have that value, so avoid replacing an existing
	// address with an empty file during a reconfigure.
	if publicIP := strings.TrimSpace(cfg.PublicIP); publicIP != "" {
		state["public_ip"] = publicIP
	}
	for name, value := range state {
		if err := writeStateFile(stateDir, name, value+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func yesNoString(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func protocolStateValue(protocols []config.Protocol) string {
	parts := make([]string, 0, len(protocols))
	for _, proto := range protocols {
		parts = append(parts, string(proto))
	}
	return strings.Join(parts, ",")
}

func (c Config) siteTemplate() string {
	name, err := NormalizeSiteTemplate(c.SiteTemplate)
	if err != nil {
		return DefaultSiteTemplate
	}
	return name
}
