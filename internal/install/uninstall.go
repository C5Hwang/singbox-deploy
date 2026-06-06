package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// UninstallOptions controls which managed data under /etc/singbox-deploy is
// removed. Services, renewal entries, and the managed Nginx config are always
// removed because they are project-owned runtime integration points.
type UninstallOptions struct {
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

	Progress func(Event)
}

type uninstallStep struct {
	label  string
	detail string
	run    func(context.Context) error
}

// Uninstall removes only singbox-deploy managed integration files and selected
// data categories. Unknown files under the layout root are never removed.
func Uninstall(ctx context.Context, opts UninstallOptions) error {
	opts.defaults()
	steps := opts.steps()
	for i, s := range steps {
		opts.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx); err != nil {
			opts.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return fmt.Errorf("%s: %w", s.label, err)
		}
		opts.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return nil
}

func (o *UninstallOptions) defaults() {
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

func (o UninstallOptions) steps() []uninstallStep {
	return []uninstallStep{
		{"Stop services", "stop and disable managed systemd units", o.stepStopServices},
		{"Systemd units", "remove managed systemd unit and timer files", o.stepSystemdUnits},
		{"ACME renewal", "remove managed cron renewal entry if present", o.stepCronRenewal},
		{"Nginx config", "remove only the managed singbox-deploy Nginx config", o.stepNginxConfig},
		{"Selected data", "remove selected /etc/singbox-deploy data categories", o.stepSelectedData},
	}
}

func (o UninstallOptions) emit(e Event) {
	if o.Progress != nil {
		o.Progress(e)
	}
}

func (o UninstallOptions) stepStopServices(context.Context) error {
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
		cmd := system.Command{Name: "systemctl", Args: []string{"stop", system.CertRenewService}}
		if err := o.Runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd.String(), err)
		}
	}
	return nil
}

func (o UninstallOptions) stepSystemdUnits(context.Context) error {
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

func (o UninstallOptions) stepCronRenewal(context.Context) error {
	_, err := removeFileIfExists(o.CronPath)
	return err
}

func (o UninstallOptions) stepNginxConfig(context.Context) error {
	_, err := removeFileIfExists(o.NginxConfPath)
	return err
}

func (o UninstallOptions) stepSelectedData(context.Context) error {
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
