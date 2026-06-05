package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestUninstallRemovesOnlyManagedSelectedArtifacts(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(filepath.Join(root, "etc", "singbox-deploy"))
	systemdDir := filepath.Join(root, "systemd")
	nginxDir := filepath.Join(root, "nginx")
	nginxConf := filepath.Join(nginxDir, "singbox-deploy.conf")
	cronPath := filepath.Join(root, "cron", "singbox-deploy-cert-renew")
	runner := &recordingRunner{}

	for _, path := range []string{
		filepath.Join(layout.StateDir, "domain"),
		layout.SingBoxBin,
		filepath.Join(layout.TLSDir, "example.com.crt"),
		layout.TrafficDB,
		filepath.Join(layout.WebRoot, "index.html"),
		filepath.Join(layout.SubscribeDir, "default", "token"),
		filepath.Join(layout.Root, "custom.txt"),
		nginxConf,
		filepath.Join(nginxDir, "unrelated.conf"),
		cronPath,
	} {
		writeTestFile(t, path)
	}
	for _, unit := range []string{system.SingBoxService, system.MonitorService, system.CertRenewService, system.CertRenewTimer} {
		writeTestFile(t, filepath.Join(systemdDir, unit))
	}

	err := Uninstall(context.Background(), UninstallOptions{
		Runner:              runner,
		Layout:              layout,
		SystemdDir:          systemdDir,
		NginxConfPath:       nginxConf,
		CronPath:            cronPath,
		DeleteRuntime:       true,
		DeleteTrafficDB:     true,
		DeleteSubscriptions: true,
	})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}

	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"systemctl disable --now singbox-deploy-cert-renew.timer",
		"systemctl disable --now singbox-deploy-monitor.service",
		"systemctl disable --now sing-box.service",
		"systemctl stop singbox-deploy-cert-renew.service",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}

	for _, path := range []string{layout.StateDir, filepath.Dir(layout.SingBoxBin), layout.TrafficDB, layout.SubscribeDir, nginxConf, cronPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed, stat err=%v", path, err)
		}
	}
	for _, path := range []string{layout.TLSDir, layout.WebRoot, filepath.Join(layout.Root, "custom.txt"), filepath.Join(nginxDir, "unrelated.conf")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should be kept: %v", path, err)
		}
	}
}

func TestUninstallRejectsSelectedPathOutsideLayoutRoot(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(filepath.Join(root, "managed"))
	layout.TLSDir = filepath.Join(root, "outside-tls")
	writeTestFile(t, filepath.Join(layout.TLSDir, "example.com.crt"))

	err := Uninstall(context.Background(), UninstallOptions{
		Runner:             &recordingRunner{},
		Layout:             layout,
		SystemdDir:         filepath.Join(root, "systemd"),
		NginxConfPath:      filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CronPath:           filepath.Join(root, "cron", "renew"),
		DeleteCertificates: true,
	})
	if err == nil || !strings.Contains(err.Error(), "outside layout root") {
		t.Fatalf("expected outside-root guard error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(layout.TLSDir, "example.com.crt")); statErr != nil {
		t.Fatalf("outside file should not be removed: %v", statErr)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
