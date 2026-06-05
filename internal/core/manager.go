// Package core manages the installed sing-box core binary and service.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/install"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Action identifies one core-management operation.
type Action string

const (
	ActionChangeStable Action = "change-stable"
	ActionStart        Action = "start"
	ActionStop         Action = "stop"
	ActionRestart      Action = "restart"
)

// Result is returned after a successful action.
type Result struct {
	Tag string
}

// Manager performs core-management operations. Network and command execution are
// injectable so the flows are testable without touching the host.
type Manager struct {
	Runner   system.Runner
	Layout   paths.Layout
	Releases *release.Client

	Download       func(ctx context.Context, url, dest string) error
	StableReleases func(ctx context.Context, n int) ([]string, error)
	Progress       func(install.Event)

	GOOS   string
	GOARCH string
}

type step struct {
	label  string
	detail string
	run    func(context.Context) error
}

// Defaults fills unset production dependencies.
func (m *Manager) Defaults() {
	if m.Layout.Root == "" {
		m.Layout = paths.DefaultLayout()
	}
	if m.Runner == nil {
		m.Runner = system.NewExecRunner(nil)
	}
	if m.Releases == nil {
		m.Releases = release.NewClient("", nil)
	}
	if m.Download == nil {
		m.Download = func(ctx context.Context, url, dest string) error {
			return release.DownloadTo(ctx, nil, url, dest)
		}
	}
	if m.StableReleases == nil {
		m.StableReleases = func(ctx context.Context, n int) ([]string, error) {
			return m.Releases.StableReleases(ctx, "SagerNet", "sing-box", n)
		}
	}
	if m.GOOS == "" {
		m.GOOS = "linux"
	}
	if m.GOARCH == "" {
		m.GOARCH = "amd64"
	}
}

// RecentStable returns up to n recent stable sing-box releases.
func (m *Manager) RecentStable(ctx context.Context, n int) ([]string, error) {
	m.Defaults()
	return m.StableReleases(ctx, n)
}

// Run executes one core-management action.
func (m *Manager) Run(ctx context.Context, action Action, tag string) (Result, error) {
	m.Defaults()
	switch action {
	case ActionChangeStable:
		if strings.TrimSpace(tag) == "" {
			return Result{}, fmt.Errorf("target release is required")
		}
		return m.replaceWithTag(ctx, strings.TrimSpace(tag), "Change")
	case ActionStart:
		return m.serviceAction(ctx, "Start", "start")
	case ActionStop:
		return m.serviceAction(ctx, "Stop", "stop")
	case ActionRestart:
		return m.serviceAction(ctx, "Restart", "restart")
	default:
		return Result{}, fmt.Errorf("unsupported core action %q", action)
	}
}

func (m *Manager) replaceWithTag(ctx context.Context, tag, label string) (Result, error) {
	steps := append([]step{{label: "Target", detail: label + " to " + tag, run: func(context.Context) error { return nil }}}, m.replaceSteps(&tag)...)
	if err := m.runSteps(ctx, steps); err != nil {
		return Result{}, err
	}
	return Result{Tag: tag}, nil
}

func (m *Manager) replaceSteps(tag *string) []step {
	var archivePath, candidatePath string
	return []step{
		{label: "Stop", detail: "stop sing-box.service", run: func(context.Context) error {
			return m.run(system.Systemctl("stop", system.SingBoxService))
		}},
		{label: "Download", detail: "download selected sing-box release", run: func(ctx context.Context) error {
			if strings.TrimSpace(*tag) == "" {
				return fmt.Errorf("release tag is empty")
			}
			binDir := filepath.Dir(m.Layout.SingBoxBin)
			updateDir := filepath.Join(binDir, ".updates")
			if err := os.MkdirAll(updateDir, 0o755); err != nil {
				return err
			}
			archive := release.SingBoxArchiveName(*tag, m.GOOS, m.GOARCH)
			archivePath = filepath.Join(updateDir, archive)
			candidatePath = filepath.Join(updateDir, "sing-box-"+safeTag(*tag))
			url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", *tag, archive)
			if err := m.Download(ctx, url, archivePath); err != nil {
				return err
			}
			info, err := os.Stat(archivePath)
			if err != nil {
				return fmt.Errorf("verify downloaded archive: %w", err)
			}
			if info.Size() == 0 {
				return fmt.Errorf("downloaded archive is empty")
			}
			return nil
		}},
		{label: "Verify", detail: "extract and verify sing-box binary", run: func(context.Context) error {
			f, err := os.Open(archivePath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := release.ExtractSingBox(f, candidatePath); err != nil {
				return err
			}
			return verifyCandidate(candidatePath)
		}},
		{label: "Replace", detail: "replace managed sing-box binary", run: func(context.Context) error {
			if err := os.MkdirAll(filepath.Dir(m.Layout.SingBoxBin), 0o755); err != nil {
				return err
			}
			return os.Rename(candidatePath, m.Layout.SingBoxBin)
		}},
		{label: "Validate", detail: "validate config with new binary", run: func(context.Context) error {
			return m.run(system.Command{Name: m.Layout.SingBoxBin, Args: []string{"check", "-c", m.Layout.ConfigJSON}})
		}},
		{label: "Restart", detail: "restart sing-box.service", run: func(context.Context) error {
			return m.run(system.Systemctl("restart", system.SingBoxService))
		}},
		{label: "Cleanup", detail: "remove temporary download files", run: func(context.Context) error {
			if archivePath == "" {
				return nil
			}
			return os.RemoveAll(filepath.Dir(archivePath))
		}},
	}
}

func (m *Manager) serviceAction(ctx context.Context, label, systemctlAction string) (Result, error) {
	steps := []step{{label: label, detail: systemctlAction + " sing-box.service", run: func(context.Context) error {
		return m.run(system.Systemctl(systemctlAction, system.SingBoxService))
	}}}
	return Result{}, m.runSteps(ctx, steps)
}

func (m *Manager) runSteps(ctx context.Context, steps []step) error {
	for i, s := range steps {
		m.emit(install.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx); err != nil {
			m.emit(install.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return fmt.Errorf("%s: %w", s.label, err)
		}
		m.emit(install.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return nil
}

func (m *Manager) emit(e install.Event) {
	if m.Progress != nil {
		m.Progress(e)
	}
}

func (m *Manager) run(cmds ...system.Command) error {
	for _, c := range cmds {
		if err := m.Runner.Run(c); err != nil {
			return fmt.Errorf("command %q: %w", c.String(), err)
		}
	}
	return nil
}

func verifyCandidate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify extracted binary: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("extracted sing-box path is a directory")
	}
	if info.Size() == 0 {
		return fmt.Errorf("extracted sing-box binary is empty")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("extracted sing-box binary is not executable")
	}
	return nil
}

func safeTag(tag string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-")
	return replacer.Replace(tag)
}
