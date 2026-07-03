package uninstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Options controls which managed data under /etc/singbox-deploy is
// removed. Services, renewal entries, and the managed Nginx config are always
// removed because they are project-owned runtime integration points.
type Options struct {
	Runner system.Runner
	Layout paths.Layout

	SystemdDir    string // default /etc/systemd/system
	NginxConfPath string // default /etc/nginx/conf.d/singbox-deploy.conf
	CronPath      string // default /etc/cron.d/singbox-deploy-cert-renew

	DeleteRuntime       bool // state files and sing-box binary/config directory
	DeleteCertificates  bool
	DeleteMonitorDB     bool
	DeleteSite          bool
	DeleteSubscriptions bool

	Progress func(deploy.Event)
}

// Run removes only singbox-deploy managed integration files and selected
// data categories. Unknown files under the layout root are never removed.
func Run(ctx context.Context, opts Options) error {
	opts.defaults()
	return deploy.RunSteps(ctx, opts.Progress, opts.steps())
}

func (o *Options) defaults() {
	if o.Runner == nil {
		o.Runner = system.NewExecRunner(nil)
	}
	if o.Layout.Root == "" {
		o.Layout = paths.DefaultLayout()
	}
	if o.SystemdDir == "" {
		o.SystemdDir = "/etc/systemd/system"
	}
	if o.NginxConfPath == "" {
		o.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	if o.CronPath == "" {
		o.CronPath = "/etc/cron.d/singbox-deploy-cert-renew"
	}
}

func (o Options) steps() []deploy.Step {
	return []deploy.Step{
		{Label: "Stop services", Detail: "stop and disable managed systemd units", Run: o.stepStopServices},
		{Label: "Firewall", Detail: "close managed protocol and subscription ports", Run: o.stepFirewall},
		{Label: "Systemd units", Detail: "remove managed systemd unit and timer files", Run: o.stepSystemdUnits},
		{Label: "ACME renewal", Detail: "remove managed cron renewal entry if present", Run: o.stepCronRenewal},
		{Label: "Nginx config", Detail: "remove only the managed singbox-deploy Nginx config", Run: o.stepNginxConfig},
		{Label: "Selected data", Detail: "remove selected /etc/singbox-deploy data categories", Run: o.stepSelectedData},
	}
}

// stepFirewall closes the ports the deployment opened, except 80/443 which are
// shared with any other Nginx site on the host. It is best-effort: a missing
// state file or an undetectable firewall must not block uninstall.
func (o Options) stepFirewall(context.Context) error {
	fw := system.DetectFirewall()
	if fw == system.FirewallNone {
		return nil
	}
	cfg, err := deploy.LoadProtocolConfig(o.Layout)
	if err != nil {
		return nil // no recoverable state; nothing to close
	}
	ports := deploy.ManagedFirewallPorts(cfg)
	kept := ports[:0]
	for _, p := range ports {
		if p.Number == 80 || p.Number == 443 {
			continue
		}
		kept = append(kept, p)
	}
	for _, cmd := range system.FirewallRemoveCommands(fw, kept) {
		_ = o.Runner.Run(cmd)
	}
	return nil
}

func (o Options) stepStopServices(context.Context) error {
	for _, unit := range []string{system.CertRenewTimer, system.MonitorService, system.SingBoxService} {
		if !fileExists(filepath.Join(o.SystemdDir, unit)) {
			continue
		}
		cmd := system.Command{Name: "systemctl", Args: []string{"disable", "--now", unit}}
		if err := o.Runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd.String(), err)
		}
	}
	if fileExists(filepath.Join(o.SystemdDir, system.CertRenewService)) {
		cmd := system.Systemctl("stop", system.CertRenewService)
		if err := o.Runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd.String(), err)
		}
	}
	return nil
}

func (o Options) stepSystemdUnits(context.Context) error {
	removed := false
	for _, unit := range []string{system.SingBoxService, system.MonitorService, system.CertRenewService, system.CertRenewTimer} {
		ok, err := removeFileIfExists(filepath.Join(o.SystemdDir, unit))
		if err != nil {
			return err
		}
		removed = removed || ok
	}
	if !removed {
		return nil
	}
	cmd := system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}
	if err := o.Runner.Run(cmd); err != nil {
		return fmt.Errorf("%s: %w", cmd.String(), err)
	}
	return nil
}

func (o Options) stepCronRenewal(context.Context) error {
	_, err := removeFileIfExists(o.CronPath)
	return err
}

func (o Options) stepNginxConfig(context.Context) error {
	removed, err := removeFileIfExists(o.NginxConfPath)
	if err != nil {
		return err
	}
	if removed {
		// Reload so the running Nginx drops the just-removed managed vhost
		// (and its now-deleted certificate); ignore errors when Nginx is not
		// running.
		_ = o.Runner.Run(system.Systemctl("reload", "nginx"))
	}
	return nil
}

func (o Options) stepSelectedData(context.Context) error {
	root := o.Layout.Root
	if o.DeleteRuntime {
		if err := removeManagedDir(root, o.Layout.StateDir); err != nil {
			return err
		}
		if err := removeManagedDir(root, filepath.Dir(o.Layout.SingBoxBin)); err != nil {
			return err
		}
	}
	if o.DeleteCertificates {
		if err := removeManagedDir(root, o.Layout.TLSDir); err != nil {
			return err
		}
	}
	if o.DeleteMonitorDB {
		if err := removeManagedFile(root, o.Layout.MonitorDB); err != nil {
			return err
		}
		if err := removeEmptyManagedDir(root, filepath.Dir(o.Layout.MonitorDB)); err != nil {
			return err
		}
	}
	if o.DeleteSite {
		if err := removeManagedDir(root, o.Layout.WebRoot); err != nil {
			return err
		}
	}
	if o.DeleteSubscriptions {
		if err := removeManagedDir(root, o.Layout.SubscribeDir); err != nil {
			return err
		}
	}
	return removeEmptyLayoutRoot(o.Layout.Root)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeFileIfExists(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func removeManagedDir(root, target string) error {
	if err := validateManagedPath(root, target); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Clean(target))
}

func removeManagedFile(root, target string) error {
	if err := validateManagedPath(root, target); err != nil {
		return err
	}
	_, err := removeFileIfExists(filepath.Clean(target))
	return err
}

func removeEmptyManagedDir(root, target string) error {
	if err := validateManagedPath(root, target); err != nil {
		return err
	}
	return removeEmptyDir(filepath.Clean(target))
}

func validateManagedPath(root, target string) error {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)
	if cleanRoot == "." || cleanRoot == string(os.PathSeparator) || cleanRoot == "" {
		return fmt.Errorf("refusing to remove managed path with unsafe root %q", root)
	}
	if cleanTarget == cleanRoot || cleanTarget == "." || cleanTarget == string(os.PathSeparator) {
		return fmt.Errorf("refusing to remove layout root directly: %s", target)
	}
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return err
	}
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing to remove path outside layout root: %s", target)
	}
	return nil
}

func removeEmptyDir(path string) error {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	return os.Remove(path)
}

func removeEmptyLayoutRoot(root string) error {
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "." || cleanRoot == string(os.PathSeparator) || cleanRoot == "" {
		return nil
	}
	return removeEmptyDir(cleanRoot)
}
