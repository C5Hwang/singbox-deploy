package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestRenderSubscriptionServiceUnitIsSandboxedAndLoopbackOnly(t *testing.T) {
	layout := paths.LayoutForRoot("/etc/singbox-deploy")
	unit, err := RenderSubscriptionServiceUnit(layout, "/usr/bin/singbox-deploy")
	if err != nil {
		t.Fatalf("RenderSubscriptionServiceUnit: %v", err)
	}
	for _, want := range []string{
		`ExecStart=/usr/bin/singbox-deploy subscription serve --listen 127.0.0.1:` + strconv.Itoa(DefaultSubscriptionServicePort) +
			` --subscribe-dir "/etc/singbox-deploy/subscribe" --keys-dir "/etc/singbox-deploy/subscribe-keys"` + "\n",
		"Type=exec",
		"DynamicUser=yes\n",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=\n",
		"AmbientCapabilities=\n",
		"ProtectSystem=strict",
		"PrivateTmp=yes",
		"ReadOnlyPaths=/run -/etc/singbox-deploy/subscribe -/etc/singbox-deploy/subscribe-keys\n",
		"InaccessiblePaths=-/etc/singbox-deploy/state -/etc/singbox-deploy/tls\n",
		"IPAddressAllow=localhost",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("subscription unit missing %q:\n%s", want, unit)
		}
	}
	// It never needs root: the subscription salts stay hidden, and everything
	// it reads is world-readable.
	for _, unwanted := range []string{"User=root", "CAP_", "--state-dir"} {
		if strings.Contains(unit, unwanted) {
			t.Fatalf("subscription unit must not set %q:\n%s", unwanted, unit)
		}
	}
}

func TestOrchestratorInstallsSubscriptionServiceBeforeNginx(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	runner := &recordingRunner{}
	o := &Orchestrator{
		Runner:         runner,
		Layout:         layout,
		CertManager:    certManagerFor(t, layout, fakeIssuer{}),
		LatestSingBox:  func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:       func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		CheckConflicts: func(context.Context, Config) error { return nil },
		CheckPorts:     func(context.Context, Config) error { return nil },
		DeployBin:      "/usr/bin/singbox-deploy",
		SystemdDir:     filepath.Join(root, "systemd"),
		NginxConfPath:  filepath.Join(root, "nginx", "singbox-deploy.conf"),
	}
	if err := o.Run(context.Background(), testConfig(t)); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	mustExist(t, filepath.Join(o.SystemdDir, system.SubscriptionService))
	joined := strings.Join(runner.commands, "\n")
	restart := strings.Index(joined, "systemctl restart "+system.SubscriptionService)
	enable := strings.Index(joined, "systemctl enable "+system.SubscriptionService)
	nginxTest := strings.Index(joined, "nginx -t")
	if restart < 0 || enable < 0 || enable > restart {
		t.Fatalf("subscription service must be enabled then restarted:\n%s", joined)
	}
	if restart > nginxTest {
		t.Fatalf("subscription service must be up before Nginx is pointed at it:\n%s", joined)
	}
	// A reinstall frees the service's loopback port before the port check.
	if strings.Index(joined, "systemctl stop "+system.SubscriptionService) > strings.Index(joined, "apt-get") {
		t.Fatalf("subscription service must be stopped with the other managed services:\n%s", joined)
	}
	conf, err := os.ReadFile(o.NginxConfPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	if !strings.Contains(string(conf), "proxy_pass http://127.0.0.1:"+strconv.Itoa(DefaultSubscriptionServicePort)+";") {
		t.Fatalf("nginx config does not route to the subscription service:\n%s", conf)
	}
}

func TestOrchestratorSpokeModeInstallsNoSubscriptionService(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	runner := &recordingRunner{}
	cfg := testConfig(t)
	cfg.SpokeMode = true
	certPath, keyPath := CertificatePaths(layout, cfg.Domain)
	writeTestCertificatePair(t, certPath, keyPath, cfg.Domain)
	o := &Orchestrator{
		Runner:         runner,
		Layout:         layout,
		CertManager:    certManagerFor(t, layout, fakeIssuer{}),
		LatestSingBox:  func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:       func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		CheckConflicts: func(context.Context, Config) error { return nil },
		CheckPorts:     func(context.Context, Config) error { return nil },
		DeployBin:      "/usr/bin/singbox-deploy",
		SystemdDir:     filepath.Join(root, "systemd"),
		NginxConfPath:  filepath.Join(root, "nginx", "singbox-deploy.conf"),
	}
	if err := o.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if joined := strings.Join(runner.commands, "\n"); strings.Contains(joined, system.SubscriptionService) {
		t.Fatalf("spoke must not touch the subscription service:\n%s", joined)
	}
	mustNotExist(t, filepath.Join(o.SystemdDir, system.SubscriptionService))
}

// Every placement of /s/ keeps serving plain links from disk and hands only
// query-bearing ones to the service, through a named location of its own
// server block.
func TestWriteManagedNginxConfigRoutesQueriesInEverySubscriptionPlacement(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "own block", cfg: Config{Domain: "example.com", SubscribePort: DefaultSubscribePort, MonitorPublicPort: DefaultMonitorPublicPort, MonitorPort: DefaultMonitorPort}},
		{name: "site block", cfg: Config{Domain: "example.com", SubscribePort: 443, MonitorPublicPort: DefaultMonitorPublicPort, MonitorPort: DefaultMonitorPort}},
		{name: "monitor block", cfg: Config{Domain: "example.com", MonitorDomain: "monitor.example.com", SubscribePort: DefaultMonitorPublicPort, MonitorPublicPort: DefaultMonitorPublicPort, MonitorPort: DefaultMonitorPort, DeployMonitor: true, DeployMonitorFrontend: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			layout := paths.LayoutForRoot(root)
			confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
			if err := WriteManagedNginxConfig(layout, tc.cfg, confPath); err != nil {
				t.Fatalf("WriteManagedNginxConfig: %v", err)
			}
			raw, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatal(err)
			}
			conf := string(raw)
			if got := strings.Count(conf, "location /s/ {"); got != 1 {
				t.Fatalf("expected one /s/ location, got %d:\n%s", got, conf)
			}
			location := conf[strings.Index(conf, "location /s/ {"):]
			location = location[:strings.Index(location, "\n    }\n")]
			for _, want := range []string{
				"error_page 418 = @subscription_dynamic;",
				"if ($args) {\n            return 418;\n        }",
				"alias " + layout.SubscribeDir + "/;",
				"default_type text/plain;",
				"charset utf-8;",
			} {
				if !strings.Contains(location, want) {
					t.Fatalf("/s/ location missing %q:\n%s", want, location)
				}
			}
			named := conf[strings.Index(conf, "location @subscription_dynamic {"):]
			named = named[:strings.Index(named, "\n    }\n")]
			for _, want := range []string{
				"access_log off;",
				// Upstream errors and spooled responses would record the auth key.
				"error_log /dev/null crit;",
				"proxy_max_temp_file_size 0;",
				// No URI part, so Nginx forwards the original URI and query.
				"proxy_pass http://127.0.0.1:" + strconv.Itoa(DefaultSubscriptionServicePort) + ";",
				"proxy_set_header Host $host;",
				"proxy_set_header X-Forwarded-Proto $scheme;",
			} {
				if !strings.Contains(named, want) {
					t.Fatalf("named location missing %q:\n%s", want, named)
				}
			}
			// The named location must sit in the same server block as /s/.
			serverOf := func(idx int) int { return strings.LastIndex(conf[:idx], "server {") }
			if serverOf(strings.Index(conf, "location /s/ {")) != serverOf(strings.Index(conf, "location @subscription_dynamic {")) {
				t.Fatalf("named location is not in the /s/ server block:\n%s", conf)
			}
		})
	}
}

func TestWriteManagedNginxConfigSpokeHasNoSubscriptionRouting(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	confPath := filepath.Join(root, "nginx", "singbox-deploy.conf")
	cfg := Config{Domain: "example.com", SubscribePort: DefaultSubscribePort, SpokeMode: true}
	if err := WriteManagedNginxConfig(layout, cfg, confPath); err != nil {
		t.Fatalf("WriteManagedNginxConfig: %v", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "@subscription_dynamic") || strings.Contains(string(conf), "/s/") {
		t.Fatalf("spoke nginx must not route subscriptions:\n%s", conf)
	}
}

// installedHub writes the state and managed files of a hub deployed before the
// subscription service: a complete install whose Nginx serves /s/ statically.
func installedHub(t *testing.T) (paths.Layout, SubscriptionServiceMigration) {
	t.Helper()
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	if err := WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatalf("WriteInstallState: %v", err)
	}
	m := SubscriptionServiceMigration{
		Layout:        layout,
		SystemdDir:    filepath.Join(root, "systemd"),
		DeployBin:     "/usr/bin/singbox-deploy",
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
	}
	legacy := `server {
    listen 2096 ssl;
    location /s/ {
        alias ` + layout.SubscribeDir + `/;
        default_type text/plain;
        charset utf-8;
    }
}
`
	if err := WriteFile(m.NginxConfPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	return layout, m
}

func TestEnsureSubscriptionServiceUpgradesAnExistingHubOnce(t *testing.T) {
	_, m := installedHub(t)
	runner := &recordingRunner{}
	m.Runner = runner

	changed, err := EnsureSubscriptionService(context.Background(), m)
	if err != nil || !changed {
		t.Fatalf("EnsureSubscriptionService = %v, %v; want true, nil", changed, err)
	}
	mustExist(t, filepath.Join(m.SystemdDir, system.SubscriptionService))
	joined := strings.Join(runner.commands, "\n")
	restart := strings.Index(joined, "systemctl restart "+system.SubscriptionService)
	nginxTest := strings.Index(joined, "nginx -t")
	if restart < 0 || nginxTest < 0 || restart > nginxTest {
		t.Fatalf("unit must be started before Nginx is rewritten:\n%s", joined)
	}
	if !strings.Contains(joined, "systemctl restart nginx") {
		t.Fatalf("nginx was not restarted onto the new config:\n%s", joined)
	}
	conf, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "@subscription_dynamic") {
		t.Fatalf("nginx config was not rewritten:\n%s", conf)
	}

	// A second process finds everything current and changes nothing; it only
	// asks systemd whether the service is still enabled and running.
	again := &recordingRunner{}
	m.Runner = again
	changed, err = EnsureSubscriptionService(context.Background(), m)
	if err != nil || changed {
		t.Fatalf("second EnsureSubscriptionService = %v, %v; want false, nil", changed, err)
	}
	probes := []string{
		"systemctl is-enabled --quiet " + system.SubscriptionService,
		"systemctl is-active --quiet " + system.SubscriptionService,
	}
	if strings.Join(again.commands, "\n") != strings.Join(probes, "\n") {
		t.Fatalf("current node was touched again: %v", again.commands)
	}
}

// A unit from an older release is replaced and restarted, without touching an
// Nginx config that already routes to it.
func TestEnsureSubscriptionServiceRefreshesAStaleUnitOnly(t *testing.T) {
	layout, m := installedHub(t)
	cfg, err := LoadProtocolConfig(layout)
	if err != nil {
		t.Fatalf("LoadProtocolConfig: %v", err)
	}
	if err := WriteManagedNginxConfig(layout, cfg, m.NginxConfPath); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(m.SystemdDir, system.SubscriptionService)
	if err := WriteFile(unitPath, []byte("[Service]\nExecStart=/old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	m.Runner = runner
	changed, err := EnsureSubscriptionService(context.Background(), m)
	if err != nil || !changed {
		t.Fatalf("EnsureSubscriptionService = %v, %v; want true, nil", changed, err)
	}
	want := []string{
		"systemctl daemon-reload",
		"systemctl enable " + system.SubscriptionService,
		"systemctl restart " + system.SubscriptionService,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, want)
	}
}

// Nothing is deployed yet, or this host is a spoke whose Nginx publishes no
// subscription: there is nothing to route, so nothing is installed.
func TestEnsureSubscriptionServiceSkipsNodesThatPublishNoSubscription(t *testing.T) {
	t.Run("no managed nginx config", func(t *testing.T) {
		_, m := installedHub(t)
		if err := os.Remove(m.NginxConfPath); err != nil {
			t.Fatal(err)
		}
		runner := &recordingRunner{}
		m.Runner = runner
		changed, err := EnsureSubscriptionService(context.Background(), m)
		if err != nil || changed || len(runner.commands) != 0 {
			t.Fatalf("EnsureSubscriptionService = %v, %v, commands %v", changed, err, runner.commands)
		}
		mustNotExist(t, filepath.Join(m.SystemdDir, system.SubscriptionService))
	})
	t.Run("spoke nginx config", func(t *testing.T) {
		layout, m := installedHub(t)
		cfg, err := LoadProtocolConfig(layout)
		if err != nil {
			t.Fatal(err)
		}
		cfg.SpokeMode = true
		if err := WriteManagedNginxConfig(layout, cfg, m.NginxConfPath); err != nil {
			t.Fatal(err)
		}
		runner := &recordingRunner{}
		m.Runner = runner
		changed, err := EnsureSubscriptionService(context.Background(), m)
		if err != nil || changed || len(runner.commands) != 0 {
			t.Fatalf("EnsureSubscriptionService = %v, %v, commands %v", changed, err, runner.commands)
		}
		mustNotExist(t, filepath.Join(m.SystemdDir, system.SubscriptionService))
	})
}

// A unit that fails to start must leave Nginx alone: routing queries to a
// service that is not there would break every cache-busting refresh.
func TestEnsureSubscriptionServiceLeavesNginxAloneWhenTheUnitFails(t *testing.T) {
	_, m := installedHub(t)
	before, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &selectiveFailRunner{failContains: "restart " + system.SubscriptionService}
	m.Runner = runner
	if _, err := EnsureSubscriptionService(context.Background(), m); err == nil {
		t.Fatal("expected the failed unit start to be reported")
	}
	after, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("nginx config changed although the service failed to start:\n%s", after)
	}
	if joined := strings.Join(runner.commands, "\n"); strings.Contains(joined, "nginx") {
		t.Fatalf("nginx was touched although the service failed to start:\n%s", joined)
	}

	// The unit on disk is already current, but the service it describes is
	// not running: the next process installs it again before touching Nginx.
	retry := &selectiveFailRunner{failContains: "is-active"}
	m.Runner = retry
	changed, err := EnsureSubscriptionService(context.Background(), m)
	if err != nil || !changed {
		t.Fatalf("retry = %v, %v; want true, nil", changed, err)
	}
	joined := strings.Join(retry.commands, "\n")
	restart := strings.Index(joined, "systemctl restart "+system.SubscriptionService)
	enable := strings.Index(joined, "systemctl enable "+system.SubscriptionService)
	nginxTest := strings.Index(joined, "nginx -t")
	if enable < 0 || restart < enable || nginxTest < restart {
		t.Fatalf("retry must enable and restart the service before Nginx:\n%s", joined)
	}
}

// A unit that is current but was stopped (a reinstall stops it first) or never
// enabled is started again without touching an Nginx config that is current.
func TestEnsureSubscriptionServiceRestartsAStoppedCurrentUnit(t *testing.T) {
	for _, probe := range []string{"is-enabled", "is-active"} {
		t.Run(probe, func(t *testing.T) {
			layout, m := installedHub(t)
			cfg, err := LoadProtocolConfig(layout)
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteManagedNginxConfig(layout, cfg, m.NginxConfPath); err != nil {
				t.Fatal(err)
			}
			m.Runner = &recordingRunner{}
			if _, err := EnsureSubscriptionService(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			runner := &selectiveFailRunner{failContains: probe}
			m.Runner = runner
			changed, err := EnsureSubscriptionService(context.Background(), m)
			if err != nil || !changed {
				t.Fatalf("EnsureSubscriptionService = %v, %v; want true, nil", changed, err)
			}
			joined := strings.Join(runner.commands, "\n")
			if !strings.Contains(joined, "systemctl restart "+system.SubscriptionService) {
				t.Fatalf("stopped service was not restarted:\n%s", joined)
			}
			if strings.Contains(joined, "nginx") {
				t.Fatalf("current Nginx config was touched:\n%s", joined)
			}
		})
	}
}

// An upgraded hub whose own port already takes the service's loopback port
// gets an explanation instead of a service that can never bind.
func TestEnsureSubscriptionServiceRefusesATakenPort(t *testing.T) {
	layout, m := installedHub(t)
	cfg, err := LoadProtocolConfig(layout)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ports.RealityVision = DefaultSubscriptionServicePort
	if err := WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	m.Runner = runner
	changed, err := EnsureSubscriptionService(context.Background(), m)
	if err == nil || changed || !strings.Contains(err.Error(), "VLESS Reality Vision") {
		t.Fatalf("EnsureSubscriptionService = %v, %v; want a port collision naming the owner", changed, err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands ran despite the collision: %v", runner.commands)
	}
	mustNotExist(t, filepath.Join(m.SystemdDir, system.SubscriptionService))
	if after, _ := os.ReadFile(m.NginxConfPath); string(after) != string(before) {
		t.Fatalf("nginx config changed despite the collision:\n%s", after)
	}
}

// A config Nginx rejects is rolled back, and the next process tries again
// because the on-disk config still lacks the dynamic route.
func TestEnsureSubscriptionServiceRestoresNginxConfigOnRejection(t *testing.T) {
	_, m := installedHub(t)
	before, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	rejecting := &selectiveFailRunner{failContains: "nginx -t"}
	m.Runner = rejecting
	if _, err := EnsureSubscriptionService(context.Background(), m); err == nil || !strings.Contains(err.Error(), "previous Nginx config restored") {
		t.Fatalf("EnsureSubscriptionService error = %v, want restored rejection", err)
	}
	// The running Nginx never left the previous config, and a test that failed
	// for a reason of its own would keep a restart from coming back up.
	if joined := strings.Join(rejecting.commands, "\n"); strings.Contains(joined, "restart nginx") {
		t.Fatalf("nginx was restarted after its config test failed:\n%s", joined)
	}
	after, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("rejected nginx config was not restored:\n%s", after)
	}

	retry := &recordingRunner{}
	m.Runner = retry
	changed, err := EnsureSubscriptionService(context.Background(), m)
	if err != nil || !changed || !strings.Contains(strings.Join(retry.commands, "\n"), "nginx -t") {
		t.Fatalf("retry = %v, %v, commands %v", changed, err, retry.commands)
	}
}

// A config that passed "nginx -t" but whose restart failed has already
// reached Nginx, so the previous one is restored and Nginx restarted onto it.
func TestEnsureSubscriptionServiceRestartsNginxOntoTheRestoredConfig(t *testing.T) {
	_, m := installedHub(t)
	before, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &selectiveFailRunner{failContains: "enable --now nginx"}
	m.Runner = runner
	if _, err := EnsureSubscriptionService(context.Background(), m); err == nil || !strings.Contains(err.Error(), "previous Nginx config restored") {
		t.Fatalf("EnsureSubscriptionService error = %v, want restored failure", err)
	}
	if after, _ := os.ReadFile(m.NginxConfPath); string(after) != string(before) {
		t.Fatalf("failed nginx config was not restored:\n%s", after)
	}
	if joined := strings.Join(runner.commands, "\n"); !strings.HasSuffix(joined, "systemctl restart nginx") {
		t.Fatalf("nginx was not restarted onto the restored config:\n%s", joined)
	}
}
