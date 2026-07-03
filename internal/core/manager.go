// Package core manages the installed sing-box core binary and service.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
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
	Progress       func(deploy.Event)

	GOOS   string
	GOARCH string
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
	steps := append([]deploy.Step{{Label: "Target", Detail: label + " to " + tag, Run: func(context.Context) error { return nil }}}, m.replaceSteps(&tag)...)
	if err := deploy.RunSteps(ctx, m.Progress, steps); err != nil {
		os.RemoveAll(m.updateDir())
		return Result{}, err
	}
	return Result{Tag: tag}, nil
}

func (m *Manager) updateDir() string {
	return filepath.Join(filepath.Dir(m.Layout.SingBoxBin), ".updates")
}

// replaceSteps downloads and fully validates the candidate binary before the
// running service is stopped or the old binary touched, so any failure up to
// Replace leaves the existing install running untouched.
func (m *Manager) replaceSteps(tag *string) []deploy.Step {
	var archivePath, candidatePath string
	return []deploy.Step{
		{Label: "Download", Detail: "download selected sing-box release", Run: func(ctx context.Context) error {
			if strings.TrimSpace(*tag) == "" {
				return fmt.Errorf("release tag is empty")
			}
			updateDir := m.updateDir()
			if err := os.MkdirAll(updateDir, 0o755); err != nil {
				return err
			}
			archive := release.SingBoxArchiveName(*tag, m.GOOS, m.GOARCH)
			archivePath = filepath.Join(updateDir, archive)
			candidatePath = filepath.Join(updateDir, "sing-box-"+release.SafeTag(*tag))
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
		{Label: "Verify", Detail: "extract and verify sing-box binary", Run: func(context.Context) error {
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
		{Label: "Validate", Detail: "validate config with new binary", Run: func(context.Context) error {
			return m.run(system.Command{Name: candidatePath, Args: []string{"check", "-c", m.Layout.ConfigJSON}})
		}},
		{Label: "Stop", Detail: "stop sing-box.service", Run: func(context.Context) error {
			return m.run(system.Systemctl("stop", system.SingBoxService))
		}},
		{Label: "Replace", Detail: "replace managed sing-box binary", Run: func(context.Context) error {
			if err := os.MkdirAll(filepath.Dir(m.Layout.SingBoxBin), 0o755); err != nil {
				return err
			}
			return os.Rename(candidatePath, m.Layout.SingBoxBin)
		}},
		{Label: "Restart", Detail: "restart sing-box.service", Run: func(context.Context) error {
			return m.run(system.Systemctl("restart", system.SingBoxService))
		}},
		{Label: "Cleanup", Detail: "remove temporary download files", Run: func(context.Context) error {
			return os.RemoveAll(m.updateDir())
		}},
	}
}

func (m *Manager) serviceAction(ctx context.Context, label, systemctlAction string) (Result, error) {
	steps := []deploy.Step{{Label: label, Detail: systemctlAction + " sing-box.service", Run: func(context.Context) error {
		return m.run(system.Systemctl(systemctlAction, system.SingBoxService))
	}}}
	return Result{}, deploy.RunSteps(ctx, m.Progress, steps)
}

func (m *Manager) run(cmds ...system.Command) error {
	return deploy.RunCommands(m.Runner, cmds...)
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
