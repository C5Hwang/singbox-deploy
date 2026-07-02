// Package selfupdate downloads and replaces the singbox-deploy binary from
// GitHub Releases. It mirrors the step-based pattern of internal/core.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	candidatePath := filepath.Join(updateDir, "singbox-deploy-"+safeTag(tag))
	sumsPath := filepath.Join(updateDir, "SHA256SUMS")

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
		{label: "Verify", detail: "verify checksum of downloaded binary", run: func(ctx context.Context) error {
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
		{label: "Replace", detail: "replace " + m.InstallBin, run: func(context.Context) error {
			return os.Rename(candidatePath, m.InstallBin)
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

func safeTag(tag string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-")
	return replacer.Replace(tag)
}
