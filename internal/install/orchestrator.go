package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
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
	Runner   system.Runner
	Layout   paths.Layout
	ACME     *acme.Manager
	Releases *release.Client

	// Hooks (nil values fall back to real implementations in Run).
	Download      func(ctx context.Context, url, dest string) error
	LatestSingBox func(ctx context.Context) (string, error)
	Progress      func(Event)

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
}

// steps returns the ordered install steps.
func (o *Orchestrator) steps() []step {
	return []step{
		{"Dependencies", "install base packages", o.stepDependencies},
		{"Nginx", "install nginx.org mainline", o.stepNginxInstall},
		{"Firewall", "open required ports", o.stepFirewall},
		{"Certificates", "obtain Let's Encrypt certificate", o.stepCertificates},
		{"sing-box core", "download latest stable", o.stepSingBox},
		{"Config", "generate and validate config.json", o.stepConfig},
		{"Services", "install and start sing-box.service", o.stepServices},
		{"Subscriptions", "generate subscription files", o.stepSubscriptions},
		{"Nginx config", "write managed config and reload", o.stepNginxConfig},
		{"Monitor", "install and start traffic monitor", o.stepMonitor},
		{"Finalize", "write account state", o.stepFinalize},
	}
}

// Run executes every step in order, emitting progress. It stops at the first
// failing step and returns its error.
func (o *Orchestrator) Run(ctx context.Context, cfg Config) error {
	o.defaults()
	steps := o.steps()
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
	// HTTP-01 binds port 80; free it by stopping Nginx (ignore if not running).
	if cfg.Challenge == acme.ChallengeHTTP01 {
		_ = o.Runner.Run(system.Command{Name: "systemctl", Args: []string{"stop", "nginx"}})
	}
	cert, err := o.ACME.Obtain(ctx, acme.Request{
		Domain:      cfg.Domain,
		Email:       cfg.Email,
		Challenge:   cfg.Challenge,
		DNSProvider: cfg.DNSProvider,
		Credentials: cfg.DNSCredentials,
	})
	if err != nil {
		return err
	}
	certPath, keyPath := o.certPaths(cfg)
	if err := writeFile(certPath, cert.CertificatePEM, 0o644); err != nil {
		return err
	}
	return writeFile(keyPath, cert.PrivateKeyPEM, 0o600)
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
	if err := writeFile(o.Layout.ConfigJSON, cfgBytes, 0o644); err != nil {
		return err
	}
	return o.run(system.Command{Name: o.Layout.SingBoxBin, Args: []string{"check", "-c", o.Layout.ConfigJSON}})
}

func (o *Orchestrator) stepServices(_ context.Context, _ Config) error {
	unit, err := templatefs.Render("service/sing-box.service.tmpl", map[string]any{
		"SingBoxBin": o.Layout.SingBoxBin,
		"ConfigPath": o.Layout.ConfigJSON,
	})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(o.SystemdDir, system.SingBoxService), []byte(unit), 0o644); err != nil {
		return err
	}
	return o.run(
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.SingBoxService}},
	)
}

func (o *Orchestrator) stepSubscriptions(_ context.Context, cfg Config) error {
	out, err := cfg.buildSubscriptions()
	if err != nil {
		return err
	}
	token := subscriptionToken(cfg.Salt)
	files := map[string]string{
		"default/" + token:           out.DefaultBase64,
		"clashMeta/" + token:         out.ClashFragment,
		"clashMetaProfiles/" + token: out.ClashProfile,
		"sing-boxProfiles/" + token:  out.SingBoxOutbounds,
		"sing-box/" + token:          out.SingBoxProfile,
	}
	for rel, body := range files {
		if err := writeFile(filepath.Join(o.Layout.SubscribeDir, rel), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return writeFile(filepath.Join(o.Layout.StateDir, "subscribe_salt"), []byte(cfg.Salt+"\n"), 0o600)
}

func (o *Orchestrator) stepNginxConfig(_ context.Context, cfg Config) error {
	certPath, keyPath := o.certPaths(cfg)
	conf, err := templatefs.Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":   cfg.SubscribePort,
		"Domain":          cfg.Domain,
		"CertificatePath": certPath,
		"KeyPath":         keyPath,
		"WebRoot":         o.Layout.WebRoot,
		"SubscribeDir":    o.Layout.SubscribeDir,
		"MonitorPort":     cfg.MonitorPort,
	})
	if err != nil {
		return err
	}
	if err := writeFile(o.NginxConfPath, []byte(conf), 0o644); err != nil {
		return err
	}
	index, err := templatefs.Render("site/default/index.html.tmpl", map[string]any{
		"Title":    cfg.Domain,
		"Subtitle": "It works.",
	})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(o.Layout.WebRoot, "index.html"), []byte(index), 0o644); err != nil {
		return err
	}
	return o.run(
		system.Command{Name: "nginx", Args: []string{"-t"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", "nginx"}},
		system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
	)
}

func (o *Orchestrator) stepMonitor(_ context.Context, cfg Config) error {
	unit, err := templatefs.Render("service/singbox-deploy-monitor.service.tmpl", map[string]any{
		"DeployBin":       o.DeployBin,
		"MonitorPort":     cfg.MonitorPort,
		"Interface":       cfg.MonitorInterface,
		"DB":              o.Layout.TrafficDB,
		"LimitBytes":      cfg.TrafficLimitBytes,
		"ResetDay":        cfg.ResetDay,
		"IntervalSeconds": 300,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Layout.TrafficDB), 0o755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(o.SystemdDir, system.MonitorService), []byte(unit), 0o644); err != nil {
		return err
	}
	return o.run(
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", "--now", system.MonitorService}},
	)
}

func (o *Orchestrator) stepFinalize(_ context.Context, cfg Config) error {
	state := map[string]string{
		"domain":              cfg.Domain,
		"email":               cfg.Email,
		"display_name":        cfg.DisplayName,
		"reality_public_key":  cfg.Creds.RealityPublicKey,
		"reality_short_id":    cfg.Creds.RealityShortID,
		"reality_server_name": cfg.RealityServerName,
		"subscribe_token":     subscriptionToken(cfg.Salt),
		"monitor_port":        itoa(cfg.MonitorPort),
		"subscribe_port":      itoa(cfg.SubscribePort),
	}
	for name, value := range state {
		if err := writeFile(filepath.Join(o.Layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
