// Package selfupdate downloads and replaces the singbox-deploy binary from
// GitHub Releases. It mirrors the step-based pattern of internal/core.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/release"
)

const (
	repo       = "C5Hwang/singbox-deploy"
	owner      = "C5Hwang"
	repoName   = "singbox-deploy"
	installBin = "/usr/bin/singbox-deploy"
)

// Result is returned after a successful update check or apply.
type Result struct {
	Tag      string
	UpToDate bool
}

// Manager performs self-update operations.
type Manager struct {
	Releases     *release.Client
	Download     func(ctx context.Context, url, dest string) error
	LatestStable func(ctx context.Context) (string, error)
	Progress     func(deploy.Event)
	Version      string
	GOARCH       string
}

// Defaults fills unset production dependencies.
func (m *Manager) Defaults() {
	if m.Releases == nil {
		m.Releases = release.NewClient("", nil)
	}
	if m.Download == nil {
		m.Download = func(ctx context.Context, url, dest string) error {
			return release.DownloadTo(ctx, nil, url, dest)
		}
	}
	if m.LatestStable == nil {
		m.LatestStable = func(ctx context.Context) (string, error) {
			return m.Releases.LatestStable(ctx, owner, repoName)
		}
	}
	if m.GOARCH == "" {
		m.GOARCH = "amd64"
	}
}

// CheckLatest returns the latest stable tag without applying anything.
func (m *Manager) CheckLatest(ctx context.Context) (string, error) {
	m.Defaults()
	return m.LatestStable(ctx)
}

// Run downloads and replaces the singbox-deploy binary with the target tag.
func (m *Manager) Run(ctx context.Context, tag string) (Result, error) {
	m.Defaults()
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Result{}, fmt.Errorf("target release is required")
	}

	asset := fmt.Sprintf("singbox-deploy-linux-%s", m.GOARCH)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
	updateDir := filepath.Join(filepath.Dir(installBin), ".singbox-deploy-update")
	candidatePath := filepath.Join(updateDir, "singbox-deploy-"+safeTag(tag))

	type step struct {
		label  string
		detail string
		run    func(context.Context) error
	}

	steps := []step{
		{label: "Download", detail: "download " + tag, run: func(ctx context.Context) error {
			if err := os.MkdirAll(updateDir, 0o755); err != nil {
				return err
			}
			return m.Download(ctx, url, candidatePath)
		}},
		{label: "Verify", detail: "verify downloaded binary", run: func(context.Context) error {
			info, err := os.Stat(candidatePath)
			if err != nil {
				return fmt.Errorf("verify downloaded binary: %w", err)
			}
			if info.IsDir() {
				return fmt.Errorf("downloaded path is a directory")
			}
			if info.Size() == 0 {
				return fmt.Errorf("downloaded binary is empty")
			}
			return os.Chmod(candidatePath, 0o755)
		}},
		{label: "Replace", detail: "replace " + installBin, run: func(context.Context) error {
			return os.Rename(candidatePath, installBin)
		}},
		{label: "Cleanup", detail: "remove temporary files", run: func(context.Context) error {
			return os.RemoveAll(updateDir)
		}},
	}

	for i, s := range steps {
		m.emit(deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx); err != nil {
			m.emit(deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Result{}, fmt.Errorf("%s: %w", s.label, err)
		}
		m.emit(deploy.Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return Result{Tag: tag}, nil
}

func (m *Manager) emit(e deploy.Event) {
	if m.Progress != nil {
		m.Progress(e)
	}
}

func safeTag(tag string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-")
	return replacer.Replace(tag)
}
