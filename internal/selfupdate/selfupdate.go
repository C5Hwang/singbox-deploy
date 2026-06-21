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
	repo     = "C5Hwang/singbox-deploy"
	owner    = "C5Hwang"
	repoName = "singbox-deploy"
)

// localBinaries lists every master-side binary that gets replaced on
// self-update. Each entry is downloaded under its own name from the same
// release tag so the master's tool surface stays at one version.
var localBinaries = []string{"singbox-deploy", "singbox-monitor"}

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

// Run downloads and replaces every master-side binary (singbox-deploy and
// singbox-monitor) with the target tag in one pass. Pushing matching
// versions to cluster nodes is handled separately by cluster.BroadcastUpgrade
// so the caller can decide whether to fan out to the fleet.
func (m *Manager) Run(ctx context.Context, tag string) (Result, error) {
	m.Defaults()
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Result{}, fmt.Errorf("target release is required")
	}

	updateDir := filepath.Join("/usr/bin", ".singbox-deploy-update")

	type step struct {
		label  string
		detail string
		run    func(context.Context) error
	}

	var steps []step
	steps = append(steps, step{label: "Prepare", detail: "create staging directory", run: func(context.Context) error {
		return os.MkdirAll(updateDir, 0o755)
	}})
	candidates := make(map[string]string, len(localBinaries))
	for _, name := range localBinaries {
		name := name
		asset := fmt.Sprintf("%s-linux-%s", name, m.GOARCH)
		url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
		candidatePath := filepath.Join(updateDir, name+"-"+safeTag(tag))
		candidates[name] = candidatePath
		steps = append(steps,
			step{label: "Download " + name, detail: "download " + tag, run: func(ctx context.Context) error {
				return m.Download(ctx, url, candidatePath)
			}},
			step{label: "Verify " + name, detail: "verify downloaded binary", run: func(context.Context) error {
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
		)
	}
	for _, name := range localBinaries {
		name := name
		dest := filepath.Join("/usr/bin", name)
		steps = append(steps, step{label: "Replace " + name, detail: "replace " + dest, run: func(context.Context) error {
			return os.Rename(candidates[name], dest)
		}})
	}
	steps = append(steps, step{label: "Cleanup", detail: "remove temporary files", run: func(context.Context) error {
		return os.RemoveAll(updateDir)
	}})

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
