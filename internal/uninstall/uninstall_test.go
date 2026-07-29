package uninstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

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
		layout.MonitorDB,
		layout.MonitorDB + "-journal",
		layout.MonitorDB + "-wal",
		layout.MonitorDB + "-shm",
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

	err := Run(context.Background(), Options{
		Runner:              runner,
		Layout:              layout,
		SystemdDir:          systemdDir,
		NginxConfPath:       nginxConf,
		CronPath:            cronPath,
		DeleteRuntime:       true,
		DeleteMonitorDB:     true,
		DeleteSubscriptions: true,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"systemctl disable --now singbox-deploy-cert-renew.timer",
		"systemctl disable --now singbox-deploy-monitor.service",
		"systemctl disable --now sing-box.service",
		"systemctl stop singbox-deploy-cert-renew.service",
		"systemctl daemon-reload",
		"systemctl reload nginx",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}

	for _, path := range []string{
		layout.StateDir,
		filepath.Dir(layout.SingBoxBin),
		layout.MonitorDB,
		layout.MonitorDB + "-journal",
		layout.MonitorDB + "-wal",
		layout.MonitorDB + "-shm",
		layout.SubscribeDir,
		nginxConf,
		cronPath,
	} {
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

	err := Run(context.Background(), Options{
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

func TestUninstallCanPreserveAgentStateUntilSelfTeardown(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(filepath.Join(root, "managed"))
	agentToken := filepath.Join(layout.StateDir, "agent", "token")
	for _, path := range []string{
		filepath.Join(layout.StateDir, "domain"),
		filepath.Join(layout.StateDir, "protocols"),
		agentToken,
		layout.SingBoxBin,
	} {
		writeTestFile(t, path)
	}

	err := Run(context.Background(), Options{
		Runner:             &recordingRunner{},
		Layout:             layout,
		SystemdDir:         filepath.Join(root, "systemd"),
		NginxConfPath:      filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CronPath:           filepath.Join(root, "cron", "renew"),
		DeleteRuntime:      true,
		PreserveAgentState: true,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if _, err := os.Stat(agentToken); err != nil {
		t.Fatalf("Agent token should be preserved: %v", err)
	}
	for _, path := range []string{
		filepath.Join(layout.StateDir, "domain"),
		filepath.Join(layout.StateDir, "protocols"),
		filepath.Dir(layout.SingBoxBin),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed, stat err=%v", path, err)
		}
	}
}

func TestPreserveAgentStateRejectsSymlinkedStateDirectory(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(filepath.Join(root, "managed"))
	outside := filepath.Join(root, "outside")
	outsideFile := filepath.Join(outside, "must-stay")
	writeTestFile(t, outsideFile)
	if err := os.MkdirAll(layout.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, layout.StateDir); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), Options{
		Runner:             &recordingRunner{},
		Layout:             layout,
		SystemdDir:         filepath.Join(root, "systemd"),
		NginxConfPath:      filepath.Join(root, "nginx", "singbox-deploy.conf"),
		CronPath:           filepath.Join(root, "cron", "renew"),
		DeleteRuntime:      true,
		PreserveAgentState: true,
	})
	if err == nil || !strings.Contains(err.Error(), "non-directory managed path") {
		t.Fatalf("expected symlink guard error, got %v", err)
	}
	if _, statErr := os.Stat(outsideFile); statErr != nil {
		t.Fatalf("outside state was modified: %v", statErr)
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
