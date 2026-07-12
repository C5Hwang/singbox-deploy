// Package selfupdate downloads and replaces the singbox-deploy binary from
// GitHub Releases. It mirrors the step-based pattern of internal/core.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// Result identifies the installed release. Tag is also populated when the
// binary replacement committed but a post-replace action failed.
type Result struct {
	Tag      string
	UpToDate bool
}

// CommittedError reports a failure that happened after the new hub binary was
// atomically installed. Callers must not describe this state as an update
// rollback or retry it as though the old binary were still installed.
type CommittedError struct {
	Err error
}

func (e *CommittedError) Error() string {
	return "hub update committed, but a post-update step failed: " + e.Err.Error()
}

func (e *CommittedError) Unwrap() error { return e.Err }

// Manager performs self-update operations.
type Manager struct {
	Releases     *release.Client
	Download     func(ctx context.Context, url, dest string) error
	LatestStable func(ctx context.Context) (string, error)
	Progress     func(deploy.Event)
	Version      string
	GOARCH       string
	// BeforeReplace receives the verified, executable candidate path. The TUI
	// uses it to export the candidate's embedded agents and upgrade every spoke
	// before the hub binary itself is replaced, preserving version equality.
	BeforeReplace func(ctx context.Context, candidatePath, targetVersion string) error
	// ReplaceFailed is invoked when BeforeReplace succeeded but committing the
	// hub binary failed. A hub/spoke deployment uses it to roll already-upgraded
	// agents back to the still-running hub version.
	ReplaceFailed func(ctx context.Context, targetVersion string) error
	// AfterReplace is invoked after the new hub binary has been committed. It is
	// used to restart long-lived services that would otherwise keep executing
	// the unlinked old binary.
	AfterReplace func(ctx context.Context, targetVersion string) error
	// InstallBin is the path of the binary to replace; defaults to
	// /usr/bin/singbox-deploy. Overridable for tests.
	InstallBin string
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
	if m.InstallBin == "" {
		m.InstallBin = installBin
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
	sumsURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS", repo, tag)
	updateDir := filepath.Join(filepath.Dir(m.InstallBin), ".singbox-deploy-update")
	candidatePath := filepath.Join(updateDir, "singbox-deploy-"+release.SafeTag(tag))
	sumsPath := filepath.Join(updateDir, "SHA256SUMS")
	committed := false

	steps := []deploy.Step{
		{Label: "Download", Detail: "download " + tag, Run: func(ctx context.Context) error {
			if err := os.MkdirAll(updateDir, 0o755); err != nil {
				return err
			}
			return m.Download(ctx, url, candidatePath)
		}},
		{Label: "Verify", Detail: "verify checksum of downloaded binary", Run: func(ctx context.Context) error {
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
			if err := m.Download(ctx, sumsURL, sumsPath); err != nil {
				return fmt.Errorf("download checksums: %w", err)
			}
			if err := verifyChecksum(candidatePath, sumsPath, asset); err != nil {
				return err
			}
			return os.Chmod(candidatePath, 0o755)
		}},
	}
	if m.BeforeReplace != nil {
		steps = append(steps, deploy.Step{Label: "Spoke agents", Detail: "upgrade embedded agents to " + tag, Run: func(ctx context.Context) error {
			return m.BeforeReplace(ctx, candidatePath, tag)
		}})
	}
	steps = append(steps,
		deploy.Step{Label: "Replace", Detail: "replace " + m.InstallBin, Run: func(ctx context.Context) error {
			if err := os.Rename(candidatePath, m.InstallBin); err != nil {
				if m.ReplaceFailed != nil {
					return errors.Join(err, m.ReplaceFailed(ctx, tag))
				}
				return err
			}
			committed = true
			return nil
		}},
	)
	if m.AfterReplace != nil {
		steps = append(steps, deploy.Step{Label: "Activate", Detail: "restart services on the updated hub", Run: func(ctx context.Context) error {
			return m.AfterReplace(ctx, tag)
		}})
	}
	steps = append(steps, deploy.Step{Label: "Cleanup", Detail: "remove temporary files", Run: func(context.Context) error {
		return os.RemoveAll(updateDir)
	}})

	if err := deploy.RunSteps(ctx, m.Progress, steps); err != nil {
		if !committed {
			return Result{}, err
		}
		// RunSteps stops at the first error, so an activation failure would
		// otherwise skip Cleanup. Retry RemoveAll here even when Cleanup itself
		// was the failing step; it is idempotent and keeps committed updates from
		// accumulating stale candidates.
		if cleanupErr := os.RemoveAll(updateDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup committed update: %w", cleanupErr))
		}
		return Result{Tag: tag}, &CommittedError{Err: err}
	}
	return Result{Tag: tag}, nil
}

// verifyChecksum confirms the SHA-256 of binPath matches the entry for asset in
// a `sha256sum`-format sums file ("<hex>  <name>" per line).
func verifyChecksum(binPath, sumsPath, asset string) error {
	expected, err := expectedChecksum(sumsPath, asset)
	if err != nil {
		return err
	}
	f, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, expected, actual)
	}
	return nil
}

func expectedChecksum(sumsPath, asset string) (string, error) {
	f, err := os.Open(sumsPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum found for %s", asset)
}
