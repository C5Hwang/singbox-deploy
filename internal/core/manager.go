// Package core manages the installed sing-box core binary and service.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"golang.org/x/mod/semver"
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
	ProbeVersion   func(ctx context.Context, path string) (string, error)
	Rename         func(oldPath, newPath string) error
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
	if m.ProbeVersion == nil {
		m.ProbeVersion = InstalledVersion
	}
	if m.Rename == nil {
		m.Rename = os.Rename
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
	targetTag, err := normalizeVersionTag(tag)
	if err != nil {
		return Result{}, fmt.Errorf("target release: %w", err)
	}
	tx := &replaceTransaction{}
	steps := append([]deploy.Step{{Label: "Target", Detail: label + " to " + targetTag, Run: func(context.Context) error { return nil }}}, m.replaceSteps(&targetTag, tx)...)
	if err := deploy.RunSteps(ctx, m.Progress, steps); err != nil {
		if tx.mutationStarted && !tx.committed {
			if rollbackErr := m.rollbackReplacement(tx); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
		if !tx.keepUpdateDir {
			if cleanupErr := os.RemoveAll(m.updateDir()); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("clean update files: %w", cleanupErr))
			}
		}
		return Result{}, err
	}
	return Result{Tag: targetTag}, nil
}

func (m *Manager) updateDir() string {
	return filepath.Join(filepath.Dir(m.Layout.SingBoxBin), ".updates")
}

type replaceTransaction struct {
	archivePath   string
	metadataPath  string
	candidatePath string
	backupPath    string
	restorePath   string

	mutationStarted bool
	committed       bool
	keepUpdateDir   bool
}

// replaceSteps downloads and fully validates the candidate binary before the
// running service is stopped or the old binary touched, so any failure up to
// Replace leaves the existing install running untouched.
func (m *Manager) replaceSteps(tag *string, tx *replaceTransaction) []deploy.Step {
	return []deploy.Step{
		{Label: "Download", Detail: "download selected sing-box release", Run: func(ctx context.Context) error {
			if strings.TrimSpace(*tag) == "" {
				return fmt.Errorf("release tag is empty")
			}
			updateDir := m.updateDir()
			tx.backupPath = filepath.Join(updateDir, "sing-box.backup")
			tx.restorePath = filepath.Join(updateDir, "sing-box.restore")
			if _, err := os.Stat(tx.backupPath); err == nil {
				tx.keepUpdateDir = true
				return fmt.Errorf("recovery backup already exists at %s; restore or remove it before another update", tx.backupPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect recovery backup: %w", err)
			}
			if err := os.RemoveAll(updateDir); err != nil {
				return fmt.Errorf("clean previous update files: %w", err)
			}
			if err := os.MkdirAll(updateDir, 0o755); err != nil {
				return err
			}
			archive := release.SingBoxArchiveName(*tag, m.GOOS, m.GOARCH)
			tx.archivePath = filepath.Join(updateDir, archive)
			tx.metadataPath = filepath.Join(updateDir, "release.json")
			tx.candidatePath = filepath.Join(updateDir, "sing-box-"+release.SafeTag(*tag))
			url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", *tag, archive)
			if err := m.Download(ctx, url, tx.archivePath); err != nil {
				return err
			}
			info, err := os.Stat(tx.archivePath)
			if err != nil {
				return fmt.Errorf("verify downloaded archive: %w", err)
			}
			if info.Size() == 0 {
				return fmt.Errorf("downloaded archive is empty")
			}
			metadataURL := fmt.Sprintf("https://api.github.com/repos/SagerNet/sing-box/releases/tags/%s", *tag)
			if err := m.Download(ctx, metadataURL, tx.metadataPath); err != nil {
				return fmt.Errorf("download upstream release metadata: %w", err)
			}
			return nil
		}},
		{Label: "Verify", Detail: "verify checksum, extract, and inspect sing-box binary", Run: func(context.Context) error {
			archive := filepath.Base(tx.archivePath)
			if err := verifyReleaseAssetChecksum(tx.metadataPath, archive, tx.archivePath); err != nil {
				return err
			}
			f, err := os.Open(tx.archivePath)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := release.ExtractSingBox(f, tx.candidatePath); err != nil {
				return err
			}
			return verifyCandidate(tx.candidatePath)
		}},
		{Label: "Version", Detail: "verify candidate reports the selected release", Run: func(ctx context.Context) error {
			version, err := m.ProbeVersion(ctx, tx.candidatePath)
			if err != nil {
				return fmt.Errorf("probe candidate version: %w", err)
			}
			if version != *tag {
				return fmt.Errorf("candidate version mismatch: got %s, want %s", version, *tag)
			}
			return nil
		}},
		{Label: "Validate", Detail: "validate config with new binary", Run: func(context.Context) error {
			return m.run(system.Command{Name: tx.candidatePath, Args: []string{"check", "-c", m.Layout.ConfigJSON}})
		}},
		{Label: "Backup", Detail: "create a recoverable backup of the installed binary", Run: func(context.Context) error {
			if err := copyRegularFile(m.Layout.SingBoxBin, tx.backupPath); err != nil {
				return fmt.Errorf("backup installed sing-box: %w", err)
			}
			matches, err := filesHaveSameSHA256(m.Layout.SingBoxBin, tx.backupPath)
			if err != nil {
				return fmt.Errorf("verify sing-box backup: %w", err)
			}
			if !matches {
				return fmt.Errorf("verify sing-box backup: checksum mismatch")
			}
			return nil
		}},
		{Label: "Stop", Detail: "stop sing-box.service", Run: func(context.Context) error {
			// From this point onward every error must either roll the old binary
			// back or preserve the backup for manual recovery.
			tx.mutationStarted = true
			tx.keepUpdateDir = true
			return m.run(system.Systemctl("stop", system.SingBoxService))
		}},
		{Label: "Replace", Detail: "replace managed sing-box binary", Run: func(context.Context) error {
			if err := os.MkdirAll(filepath.Dir(m.Layout.SingBoxBin), 0o755); err != nil {
				return err
			}
			return m.Rename(tx.candidatePath, m.Layout.SingBoxBin)
		}},
		{Label: "Restart", Detail: "restart sing-box.service", Run: func(context.Context) error {
			if err := m.run(system.Systemctl("restart", system.SingBoxService)); err != nil {
				return err
			}
			tx.committed = true
			tx.keepUpdateDir = false
			return nil
		}},
		{Label: "Cleanup", Detail: "remove temporary download files", Run: func(context.Context) error {
			return os.RemoveAll(m.updateDir())
		}},
	}
}

func (m *Manager) rollbackReplacement(tx *replaceTransaction) error {
	if strings.TrimSpace(tx.backupPath) == "" {
		tx.keepUpdateDir = true
		return fmt.Errorf("rollback old sing-box: recovery backup path is unavailable")
	}
	if err := os.Remove(tx.restorePath); err != nil && !os.IsNotExist(err) {
		tx.keepUpdateDir = true
		return fmt.Errorf("rollback old sing-box: remove stale restore file: %w", err)
	}
	if err := copyRegularFile(tx.backupPath, tx.restorePath); err != nil {
		tx.keepUpdateDir = true
		return fmt.Errorf("rollback old sing-box: stage backup: %w", err)
	}
	if err := m.Rename(tx.restorePath, m.Layout.SingBoxBin); err != nil {
		tx.keepUpdateDir = true
		return fmt.Errorf("rollback old sing-box: restore binary: %w (backup retained at %s)", err, tx.backupPath)
	}
	if err := m.run(system.Systemctl("restart", system.SingBoxService)); err != nil {
		tx.keepUpdateDir = true
		return fmt.Errorf("rollback old sing-box: restart service: %w (backup retained at %s)", err, tx.backupPath)
	}
	tx.keepUpdateDir = false
	return nil
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

// InstalledVersion executes "<path> version" and returns the exact release tag
// reported by sing-box, normalized to the upstream "v1.2.3" form.
func InstalledVersion(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("sing-box path is required")
	}
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return "", fmt.Errorf("run %s version: %w", path, err)
		}
		return "", fmt.Errorf("run %s version: %w: %s", path, err, detail)
	}
	return ParseVersionOutput(out)
}

// ParseVersionOutput parses the first non-empty line printed by
// "sing-box version". It intentionally returns the complete semantic version
// rather than comparing precedence, so a different pre-release/build suffix is
// rejected during replacement.
func ParseVersionOutput(out []byte) (string, error) {
	var firstLine string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			firstLine = line
			break
		}
	}
	fields := strings.Fields(firstLine)
	if len(fields) != 3 || fields[0] != "sing-box" || fields[1] != "version" {
		return "", fmt.Errorf("unexpected sing-box version output %q", firstLine)
	}
	tag, err := normalizeVersionTag(fields[2])
	if err != nil {
		return "", fmt.Errorf("unexpected sing-box version output %q: %w", firstLine, err)
	}
	return tag, nil
}

func normalizeVersionTag(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if !semver.IsValid(tag) {
		return "", fmt.Errorf("invalid semantic version %q", tag)
	}
	return tag, nil
}

type releaseMetadata struct {
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func verifyReleaseAssetChecksum(metadataPath, asset, archivePath string) error {
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("read upstream release metadata: %w", err)
	}
	var metadata releaseMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return fmt.Errorf("parse upstream release metadata: %w", err)
	}
	var digest string
	for _, candidate := range metadata.Assets {
		if candidate.Name == asset {
			digest = strings.TrimSpace(candidate.Digest)
			break
		}
	}
	if digest == "" {
		return fmt.Errorf("upstream release metadata has no digest for %s", asset)
	}
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm != "sha256" {
		return fmt.Errorf("unsupported upstream digest for %s: %q", asset, digest)
	}
	want, err := hex.DecodeString(encoded)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("invalid upstream SHA-256 digest for %s: %q", asset, digest)
	}
	got, err := sha256File(archivePath)
	if err != nil {
		return fmt.Errorf("hash downloaded archive: %w", err)
	}
	if !equalDigest(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, encoded, hex.EncodeToString(got))
	}
	return nil
}

func equalDigest(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func sha256File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func filesHaveSameSHA256(a, b string) (bool, error) {
	aDigest, err := sha256File(a)
	if err != nil {
		return false, err
	}
	bDigest, err := sha256File(b)
	if err != nil {
		return false, err
	}
	return equalDigest(aDigest, bDigest), nil
}

func copyRegularFile(src, dest string) (retErr error) {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); retErr == nil && closeErr != nil {
			retErr = closeErr
		}
		if retErr != nil {
			_ = os.Remove(dest)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return os.Chmod(dest, info.Mode().Perm())
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
