package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// subscriptionServiceNginxMarker is the named location every /s/ placement
// hands query-bearing requests to. A managed config without it predates the
// subscription service.
const subscriptionServiceNginxMarker = "@subscription_dynamic"

const subscriptionServiceLockName = ".subscription-service.lock"

// subscriptionServicePortLabel names DefaultSubscriptionServicePort in port
// checks and collision errors.
const subscriptionServicePortLabel = "subscription service"

// RenderSubscriptionServiceUnit renders the systemd unit of the loopback
// subscription service.
func RenderSubscriptionServiceUnit(layout paths.Layout, deployBin string) (string, error) {
	return templatefs.Render("service/singbox-deploy-subscription.service.tmpl", map[string]any{
		"DeployBin":        deployBin,
		"Port":             strconv.Itoa(DefaultSubscriptionServicePort),
		"SubscribeDir":     layout.SubscribeDir,
		"SubscribeKeysDir": layout.SubscribeKeysDir,
		"StateDir":         layout.StateDir,
		"TLSDir":           layout.TLSDir,
	})
}

// InstallSubscriptionService writes the subscription service unit, enables it
// and restarts it. restart (not enable --now) makes a reinstall or an upgraded
// unit take effect even when the old process is still running.
func InstallSubscriptionService(runner system.Runner, layout paths.Layout, systemdDir, deployBin string) error {
	unit, err := RenderSubscriptionServiceUnit(layout, deployBin)
	if err != nil {
		return err
	}
	if err := WriteFile(filepath.Join(systemdDir, system.SubscriptionService), []byte(unit), 0o644); err != nil {
		return err
	}
	return RunCommands(runner,
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", system.SubscriptionService}},
		system.Systemctl("restart", system.SubscriptionService),
	)
}

// SubscriptionServiceMigration carries what EnsureSubscriptionService needs to
// bring an already-deployed node up to the subscription service.
type SubscriptionServiceMigration struct {
	Runner        system.Runner
	Layout        paths.Layout
	SystemdDir    string // default /etc/systemd/system
	DeployBin     string // default /usr/bin/singbox-deploy
	NginxConfPath string // default /etc/nginx/conf.d/singbox-deploy.conf
}

// EnsureSubscriptionService brings a node deployed before the subscription
// service existed up to date: the unit is installed and started, then the
// managed Nginx config is rewritten so query-bearing /s/ requests reach it.
// Both halves compare against what is on disk, and a current unit is also
// asked whether it is enabled and running, so a current node costs two probes
// and a half-finished earlier attempt (a failed start, or a reinstall that
// stopped the service and then failed) is completed by the next one. Only a
// node whose managed Nginx config publishes /s/ qualifies; a spoke, or a host
// whose install never got as far as Nginx, is not touched. Neither is a hub
// upgraded with one of its own ports on the service's loopback port: the
// service could not bind, so it is left out until that port is moved.
//
// The unit goes first: an Nginx config that proxies to a service that is not
// there would turn every cache-busting subscription refresh into a 502.
func EnsureSubscriptionService(ctx context.Context, m SubscriptionServiceMigration) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m.Layout.Root == "" {
		m.Layout = paths.DefaultLayout()
	}
	if m.SystemdDir == "" {
		m.SystemdDir = "/etc/systemd/system"
	}
	if m.DeployBin == "" {
		m.DeployBin = "/usr/bin/singbox-deploy"
	}
	if m.NginxConfPath == "" {
		m.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	if m.Runner == nil {
		return false, fmt.Errorf("subscription service migration runner is required")
	}
	conf, err := os.ReadFile(m.NginxConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read managed Nginx config: %w", err)
	}
	if !bytes.Contains(conf, []byte("location /s/")) {
		return false, nil
	}

	unlock, err := lockStateFile(ctx, m.Layout.StateDir, subscriptionServiceLockName)
	if err != nil {
		return false, err
	}
	defer unlock()
	// Another process may have finished the job while this one waited.
	if conf, err = os.ReadFile(m.NginxConfPath); err != nil {
		return false, fmt.Errorf("read managed Nginx config: %w", err)
	}

	cfg, err := LoadProtocolConfig(m.Layout)
	if err != nil {
		return false, fmt.Errorf("load configuration: %w", err)
	}
	if owner := subscriptionServicePortOwner(cfg); owner != "" {
		return false, fmt.Errorf("port %d is used by %s; move it to another port to enable Tailscale-mode subscription links", DefaultSubscriptionServicePort, owner)
	}

	changed := false
	unitPath := filepath.Join(m.SystemdDir, system.SubscriptionService)
	unit, err := RenderSubscriptionServiceUnit(m.Layout, m.DeployBin)
	if err != nil {
		return false, err
	}
	current, err := os.ReadFile(unitPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read subscription service unit: %w", err)
	}
	if err != nil || !bytes.Equal(current, []byte(unit)) || !subscriptionServiceRunning(m.Runner) {
		if err := InstallSubscriptionService(m.Runner, m.Layout, m.SystemdDir, m.DeployBin); err != nil {
			return false, fmt.Errorf("install subscription service: %w", err)
		}
		changed = true
	}

	if bytes.Contains(conf, []byte(subscriptionServiceNginxMarker)) {
		return changed, nil
	}
	if err := reapplyManagedNginx(m.Runner, m.Layout, cfg, m.NginxConfPath, conf); err != nil {
		return changed, err
	}
	return true, nil
}

// subscriptionServicePortOwner names the configured port that already takes
// the service's loopback port, or "" when it is free. Only TCP can collide.
func subscriptionServicePortOwner(cfg Config) string {
	for _, p := range cfg.portChecks() {
		if p.Number == DefaultSubscriptionServicePort && p.Proto == "tcp" && p.Label != subscriptionServicePortLabel {
			return p.Label
		}
	}
	return ""
}

// subscriptionServiceRunning reports whether the installed unit is both
// enabled and active. A unit file that matches the current one says nothing
// about whether an earlier start, or a reinstall that stopped it, finished.
func subscriptionServiceRunning(runner system.Runner) bool {
	for _, probe := range []string{"is-enabled", "is-active"} {
		if runner.Run(system.Command{Name: "systemctl", Args: []string{probe, "--quiet", system.SubscriptionService}}) != nil {
			return false
		}
	}
	return true
}

// reapplyManagedNginx rewrites the managed Nginx config from the current
// template. A config Nginx rejects is replaced by the previous one, so a
// failed migration leaves the node serving exactly what it served before.
// Nginx is restarted onto the restored file only when the failure came after
// "nginx -t" passed: until then the running Nginx still holds the previous
// config, and a test that failed for a reason of its own (a broken file
// elsewhere in conf.d, a missing certificate) would keep it from starting.
func reapplyManagedNginx(runner system.Runner, layout paths.Layout, cfg Config, nginxConfPath string, previous []byte) error {
	tracked := &nginxTestTracker{Runner: runner}
	err := ApplyManagedNginx(tracked, layout, cfg, nginxConfPath)
	if err == nil {
		return nil
	}
	restoreErr := WriteFile(nginxConfPath, previous, 0o644)
	if restoreErr == nil && tracked.passed {
		restoreErr = RunCommands(runner, system.Systemctl("restart", "nginx"))
	}
	if restoreErr != nil {
		return errors.Join(fmt.Errorf("apply Nginx config: %w", err), fmt.Errorf("restore previous Nginx config: %w", restoreErr))
	}
	return fmt.Errorf("apply Nginx config: %w (previous Nginx config restored)", err)
}

// nginxTestTracker records whether "nginx -t" passed among the commands run
// through it.
type nginxTestTracker struct {
	system.Runner
	passed bool
}

func (t *nginxTestTracker) Run(c system.Command) error {
	err := t.Runner.Run(c)
	if err == nil && c.Name == "nginx" && slices.Equal(c.Args, []string{"-t"}) {
		t.passed = true
	}
	return err
}

// lockStateFile takes an exclusive cross-process lock on a file in the state
// directory, so the Hub TUI and the monitor starting together do not install
// the unit or restart Nginx twice over.
func lockStateFile(ctx context.Context, stateDir, name string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", name, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
