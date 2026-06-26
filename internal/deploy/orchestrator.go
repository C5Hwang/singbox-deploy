package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// DNSCredentialLookup resolves DNS-01 API credentials for the longest matching
// stored root domain. Returns (provider, env-form credentials, error); error
// wraps os.ErrNotExist when no stored root domain covers host. The adapter is
// a plain function to avoid a cycle between deploy and cluster.
type DNSCredentialLookup func(host string) (provider string, credentials map[string]string, err error)

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
	Runner   system.Runner
	Layout   paths.Layout
	ACME     *acme.Manager
	Releases *release.Client

	// Hooks (nil values fall back to real implementations in Run).
	Download       func(ctx context.Context, url, dest string) error
	LatestSingBox  func(ctx context.Context) (string, error)
	CheckConflicts func(ctx context.Context, cfg Config) error
	CheckPorts     func(ctx context.Context, cfg Config) error
	DNSLookup      DNSCredentialLookup
	Progress       func(Event)

	GOOS, GOARCH  string
	DeployBin     string // path to the singbox-deploy binary (used by cert renew unit)
	MonitorBin    string // path to the singbox-monitor binary (used by the monitor unit)
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
	cert = filepath.Join(o.Layout.TLSDir, cfg.Domain+".crt")
	key = filepath.Join(o.Layout.TLSDir, cfg.Domain+".key")
	return
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
	if o.MonitorBin == "" {
		o.MonitorBin = "/usr/bin/singbox-monitor"
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
}

// steps returns the ordered install steps.
func (o *Orchestrator) steps(cfg Config) []step {
	steps := []step{
		{"Conflict check", "detect existing sing-box service or binary", o.stepConflictCheck},
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
	if cfg.DeployMonitor {
		steps = append(steps, step{"Monitor", "install and start monitor", o.stepMonitor})
	}
	steps = append(steps, step{"Finalize", "write account state", o.stepFinalize})
	return steps
}

// Run executes every step in order, emitting progress. It stops at the first
// failing step and returns its error.
func (o *Orchestrator) Run(ctx context.Context, cfg Config) error {
	o.defaults()
	steps := o.steps(cfg)
	total := len(steps)
	for i, s := range steps {
		o.emit(Event{Index: i + 1, Total: total, Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			o.emit(Event{Index: i + 1, Total: total, Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return fmt.Errorf("%s: %w", s.label, err)
		}
		o.emit(Event{Index: i + 1, Total: total, Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return nil
}

func (o *Orchestrator) emit(e Event) {
	if o.Progress != nil {
		o.Progress(e)
	}
}

func (o *Orchestrator) run(cmds ...system.Command) error {
	for _, c := range cmds {
		if err := o.Runner.Run(c); err != nil {
			return fmt.Errorf("command %q: %w", c.String(), err)
		}
	}
	return nil
}

// --- steps ---

func (o *Orchestrator) stepConflictCheck(ctx context.Context, cfg Config) error {
	return o.CheckConflicts(ctx, cfg)
}

func (o *Orchestrator) stepPortCheck(ctx context.Context, cfg Config) error {
	return o.CheckPorts(ctx, cfg)
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
	cmds := system.FirewallCommands(cfg.Firewall, cfg.firewallPorts())
	if cfg.Firewall == system.FirewallFirewalld {
		cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
	}
	return o.run(cmds...)
}

func (o *Orchestrator) stepCertificates(ctx context.Context, cfg Config) error {
	certPath, keyPath := o.certPaths(cfg)
	if ok, err := certificatePairUsable(certPath, keyPath, cfg.Domain, now()); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("check existing certificate: %w", err)
		}
	} else if ok {
		return nil
	}
	if ok, err := o.importExistingCertificate(cfg, certPath, keyPath); err != nil {
		return err
	} else if ok {
		return nil
	}

	if o.DNSLookup == nil {
		return fmt.Errorf("no DNS credential lookup configured")
	}
	provider, creds, err := o.DNSLookup(cfg.Domain)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no DNS credentials configured for %s; add them in the Certificate & site menu", cfg.Domain)
		}
		return fmt.Errorf("find dns credentials for %s: %w", cfg.Domain, err)
	}
	cert, err := o.ACME.Obtain(ctx, acme.Request{
		Domain:      cfg.Domain,
		DNSProvider: provider,
		Credentials: creds,
	})
	if err != nil {
		return err
	}
	if err := WriteFile(certPath, cert.CertificatePEM, 0o644); err != nil {
		return err
	}
	return WriteFile(keyPath, cert.PrivateKeyPEM, 0o600)
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
	if err := WriteFile(o.Layout.ConfigJSON, cfgBytes, 0o644); err != nil {
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
	return o.run(
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.SingBoxService}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.CertRenewTimer}},
	)
}

func (o *Orchestrator) stepSubscriptions(_ context.Context, cfg Config) error {
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
		system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
	)
}

func (o *Orchestrator) stepMonitor(_ context.Context, cfg Config) error {
	if !cfg.DeployMonitor {
		return nil
	}
	unit, err := RenderMonitorUnit(o.Layout, o.MonitorBin, cfg)
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
// monitorBin is the path to the singbox-monitor binary that the unit invokes.
func RenderMonitorUnit(layout paths.Layout, monitorBin string, cfg Config) (string, error) {
	return RenderMonitorUnitWithListen(layout, monitorBin, cfg, fmt.Sprintf("127.0.0.1:%d", cfg.MonitorPort))
}

// RenderMonitorUnitWithListen mirrors RenderMonitorUnit but takes an explicit
// listen address. Nodes need this so the unit binds to the WireGuard IP on
// the well-known monitor port (the master polls them via that endpoint to
// aggregate samples); the master itself stays on 127.0.0.1 behind Nginx.
func RenderMonitorUnitWithListen(layout paths.Layout, monitorBin string, cfg Config, listen string) (string, error) {
	interval := cfg.MonitorIntervalSeconds
	if interval <= 0 {
		interval = DefaultMonitorIntervalSeconds
	}
	return templatefs.Render("service/singbox-deploy-monitor.service.tmpl", map[string]any{
		"MonitorBin":      monitorBin,
		"Listen":          listen,
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
		"domain":                 cfg.Domain,
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
		"subscribe_token":        subscriptionToken(cfg.Salt),
		"subscribe_port":         itoa(cfg.SubscribePort),
		"monitor_public_port":    itoa(cfg.MonitorPublicPort),
		"monitor_port":           itoa(cfg.MonitorPort),
		"monitor_interface":      cfg.MonitorInterface,
		"monitor":                yesNoString(cfg.DeployMonitor),
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
