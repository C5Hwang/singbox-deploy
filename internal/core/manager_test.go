package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

func TestChangeStableDownloadsReplacesValidatesAndRestarts(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(layout.ConfigJSON), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(layout.ConfigJSON, []byte(`{"log":{"level":"info"}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &recordingRunner{}
	var events []deploy.Event
	var downloadedURL string
	var probedPath string
	m := &Manager{
		Runner: runner,
		Layout: layout,
		GOOS:   "linux",
		GOARCH: "amd64",
		Download: testReleaseDownloader(t, "new-sing-box", "", func(url string) {
			downloadedURL = url
		}),
		ProbeVersion: func(_ context.Context, path string) (string, error) {
			probedPath = path
			return "v1.12.4", nil
		},
		Progress: func(e deploy.Event) { events = append(events, e) },
	}

	res, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Tag != "v1.12.4" {
		t.Fatalf("Result tag = %q", res.Tag)
	}
	if !strings.Contains(downloadedURL, "/v1.12.4/sing-box-1.12.4-linux-amd64.tar.gz") {
		t.Fatalf("download url = %q", downloadedURL)
	}
	body, err := os.ReadFile(layout.SingBoxBin)
	if err != nil {
		t.Fatalf("read sing-box binary: %v", err)
	}
	if string(body) != "new-sing-box" {
		t.Fatalf("binary body = %q", body)
	}
	joined := strings.Join(runner.commands, "\n")
	candidate := filepath.Join(filepath.Dir(layout.SingBoxBin), ".updates", "sing-box-v1.12.4")
	if probedPath != candidate {
		t.Fatalf("version probe path = %q, want %q", probedPath, candidate)
	}
	for _, want := range []string{
		candidate + " check -c " + layout.ConfigJSON,
		"systemctl stop sing-box.service",
		"systemctl restart sing-box.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	if strings.Index(joined, candidate+" check") > strings.Index(joined, "systemctl stop") {
		t.Fatalf("candidate must be validated before the service is stopped:\n%s", joined)
	}
	if len(events) == 0 || events[len(events)-1].Label != "Cleanup" || events[len(events)-1].Status != "ok" {
		t.Fatalf("unexpected final event: %#v", events)
	}
}

type failingRunner struct {
	recordingRunner
	failOn string
}

func (r *failingRunner) Run(c system.Command) error {
	_ = r.recordingRunner.Run(c)
	if strings.Contains(c.String(), r.failOn) {
		return errors.New("runner failure")
	}
	return nil
}

func TestChangeStableKeepsOldBinaryWhenValidationFails(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(layout.SingBoxBin), 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	if err := os.WriteFile(layout.SingBoxBin, []byte("old-sing-box"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	runner := &failingRunner{failOn: " check -c "}
	m := &Manager{
		Runner:       runner,
		Layout:       layout,
		GOOS:         "linux",
		GOARCH:       "amd64",
		Download:     testReleaseDownloader(t, "bad-sing-box", "", nil),
		ProbeVersion: fixedVersionProbe("v1.12.4"),
	}

	if _, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4"); err == nil {
		t.Fatal("expected validation failure")
	}
	body, err := os.ReadFile(layout.SingBoxBin)
	if err != nil {
		t.Fatalf("read sing-box binary: %v", err)
	}
	if string(body) != "old-sing-box" {
		t.Fatalf("old binary must stay in place, got %q", body)
	}
	joined := strings.Join(runner.commands, "\n")
	if strings.Contains(joined, "systemctl stop") {
		t.Fatalf("service must not be stopped when validation fails:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(layout.SingBoxBin), ".updates")); !os.IsNotExist(err) {
		t.Fatalf("update dir should be cleaned up on failure, stat err = %v", err)
	}
}

func TestChangeStableUsesSelectedRelease(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &recordingRunner{}
	var downloadedURL string
	m := &Manager{
		Runner: runner,
		Layout: layout,
		GOOS:   "linux",
		GOARCH: "arm64",
		Download: testReleaseDownloader(t, "selected-sing-box", "", func(url string) {
			downloadedURL = url
		}),
		ProbeVersion: fixedVersionProbe("v1.11.9"),
	}

	res, err := m.Run(context.Background(), ActionChangeStable, "v1.11.9")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Tag != "v1.11.9" {
		t.Fatalf("Result tag = %q", res.Tag)
	}
	if !strings.Contains(downloadedURL, "/v1.11.9/sing-box-1.11.9-linux-arm64.tar.gz") {
		t.Fatalf("download url = %q", downloadedURL)
	}
}

func TestChangeStableRejectsWrongCandidateVersionBeforeStopping(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &recordingRunner{}
	m := &Manager{
		Runner:       runner,
		Layout:       layout,
		GOOS:         "linux",
		GOARCH:       "amd64",
		Download:     testReleaseDownloader(t, "wrong-version-sing-box", "", nil),
		ProbeVersion: fixedVersionProbe("v1.12.3"),
	}

	_, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4")
	if err == nil || !strings.Contains(err.Error(), "candidate version mismatch: got v1.12.3, want v1.12.4") {
		t.Fatalf("expected exact version mismatch, got %v", err)
	}
	assertInstalledBinary(t, layout, "old-sing-box")
	if joined := strings.Join(runner.commands, "\n"); strings.Contains(joined, "systemctl stop") {
		t.Fatalf("service must not be stopped for a wrong-version candidate:\n%s", joined)
	}
	assertUpdateDirRemoved(t, layout)
}

func TestChangeStableRejectsChecksumMismatchBeforeVersionProbe(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &recordingRunner{}
	probed := false
	m := &Manager{
		Runner:   runner,
		Layout:   layout,
		GOOS:     "linux",
		GOARCH:   "amd64",
		Download: testReleaseDownloader(t, "tampered-sing-box", strings.Repeat("0", 64), nil),
		ProbeVersion: func(context.Context, string) (string, error) {
			probed = true
			return "v1.12.4", nil
		},
	}

	_, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if probed {
		t.Fatal("version probe must not run before the archive checksum passes")
	}
	assertInstalledBinary(t, layout, "old-sing-box")
	if joined := strings.Join(runner.commands, "\n"); strings.Contains(joined, "systemctl stop") {
		t.Fatalf("service must not be stopped for a checksum mismatch:\n%s", joined)
	}
	assertUpdateDirRemoved(t, layout)
}

func TestChangeStableRestoresAndRestartsOldBinaryWhenReplaceFails(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &recordingRunner{}
	var renameCalls []string
	failedReplace := false
	backupReady := false
	m := &Manager{
		Runner:       runner,
		Layout:       layout,
		GOOS:         "linux",
		GOARCH:       "amd64",
		Download:     testReleaseDownloader(t, "new-sing-box", "", nil),
		ProbeVersion: fixedVersionProbe("v1.12.4"),
		Rename: func(oldPath, newPath string) error {
			renameCalls = append(renameCalls, oldPath+" -> "+newPath)
			if !failedReplace && strings.Contains(filepath.Base(oldPath), "sing-box-v1.12.4") && newPath == layout.SingBoxBin {
				backup, readErr := os.ReadFile(filepath.Join(filepath.Dir(layout.SingBoxBin), ".updates", "sing-box.backup"))
				backupReady = readErr == nil && string(backup) == "old-sing-box"
				failedReplace = true
				return errors.New("replace failure")
			}
			return os.Rename(oldPath, newPath)
		},
	}

	_, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4")
	if err == nil || !strings.Contains(err.Error(), "replace failure") {
		t.Fatalf("expected replace failure, got %v", err)
	}
	if !failedReplace {
		t.Fatal("replace failure seam did not run")
	}
	if !backupReady {
		t.Fatal("recoverable old-binary backup was not ready before replacement")
	}
	assertInstalledBinary(t, layout, "old-sing-box")
	joined := strings.Join(runner.commands, "\n")
	if strings.Count(joined, "systemctl stop sing-box.service") != 1 ||
		strings.Count(joined, "systemctl restart sing-box.service") != 1 {
		t.Fatalf("replace failure must stop once and restart restored old service once:\n%s", joined)
	}
	if len(renameCalls) != 2 || !strings.Contains(renameCalls[1], "sing-box.restore -> "+layout.SingBoxBin) {
		t.Fatalf("rename calls do not show atomic restore: %#v", renameCalls)
	}
	assertUpdateDirRemoved(t, layout)
}

type failFirstRestartRunner struct {
	recordingRunner
	failed bool
}

func (r *failFirstRestartRunner) Run(c system.Command) error {
	_ = r.recordingRunner.Run(c)
	if c.String() == "systemctl restart sing-box.service" && !r.failed {
		r.failed = true
		return errors.New("new core restart failure")
	}
	return nil
}

func TestChangeStableRollsBackOldBinaryWhenNewRestartFails(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeInstalledBinary(t, layout, "old-sing-box")
	runner := &failFirstRestartRunner{}
	m := &Manager{
		Runner:       runner,
		Layout:       layout,
		GOOS:         "linux",
		GOARCH:       "amd64",
		Download:     testReleaseDownloader(t, "new-sing-box", "", nil),
		ProbeVersion: fixedVersionProbe("v1.12.4"),
	}

	_, err := m.Run(context.Background(), ActionChangeStable, "v1.12.4")
	if err == nil || !strings.Contains(err.Error(), "new core restart failure") {
		t.Fatalf("expected new-core restart failure, got %v", err)
	}
	assertInstalledBinary(t, layout, "old-sing-box")
	joined := strings.Join(runner.commands, "\n")
	if strings.Count(joined, "systemctl stop sing-box.service") != 1 ||
		strings.Count(joined, "systemctl restart sing-box.service") != 2 {
		t.Fatalf("restart failure must trigger a second restart after rollback:\n%s", joined)
	}
	assertUpdateDirRemoved(t, layout)
}

func TestParseVersionOutputReturnsExactVTag(t *testing.T) {
	out := []byte("\nsing-box version 1.14.0-alpha.26\n\nEnvironment: go1.25 linux/amd64\n")
	got, err := ParseVersionOutput(out)
	if err != nil {
		t.Fatalf("ParseVersionOutput error: %v", err)
	}
	if got != "v1.14.0-alpha.26" {
		t.Fatalf("version = %q", got)
	}

	for _, bad := range [][]byte{
		[]byte("sing-box version 1.12.4(forked)\n"),
		[]byte("other-box version 1.12.4\n"),
		[]byte("sing-box version 1.12.4 extra\n"),
	} {
		if _, err := ParseVersionOutput(bad); err == nil {
			t.Fatalf("expected invalid version output rejection for %q", bad)
		}
	}
}

func TestInstalledVersionExecutesVersionCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "sing-box")
	body := "#!/bin/sh\n[ \"$1\" = version ] || exit 9\nprintf 'sing-box version 1.12.4\\n\\nEnvironment: test\\n'\n"
	if err := os.WriteFile(binary, []byte(body), 0o755); err != nil {
		t.Fatalf("write test binary: %v", err)
	}
	got, err := InstalledVersion(context.Background(), binary)
	if err != nil {
		t.Fatalf("InstalledVersion error: %v", err)
	}
	if got != "v1.12.4" {
		t.Fatalf("InstalledVersion = %q", got)
	}
}

func TestServiceActionRunsSystemctl(t *testing.T) {
	runner := &recordingRunner{}
	m := &Manager{Runner: runner, Layout: paths.LayoutForRoot(t.TempDir())}
	if _, err := m.Run(context.Background(), ActionStart, ""); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(runner.commands) != 1 || runner.commands[0] != "systemctl start sing-box.service" {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func fixedVersionProbe(version string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) {
		return version, nil
	}
}

func writeInstalledBinary(t *testing.T, layout paths.Layout, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(layout.SingBoxBin), 0o755); err != nil {
		t.Fatalf("mkdir sing-box dir: %v", err)
	}
	if err := os.WriteFile(layout.SingBoxBin, []byte(content), 0o755); err != nil {
		t.Fatalf("write installed sing-box: %v", err)
	}
}

func assertInstalledBinary(t *testing.T, layout paths.Layout, want string) {
	t.Helper()
	body, err := os.ReadFile(layout.SingBoxBin)
	if err != nil {
		t.Fatalf("read installed sing-box: %v", err)
	}
	if string(body) != want {
		t.Fatalf("installed sing-box = %q, want %q", body, want)
	}
}

func assertUpdateDirRemoved(t *testing.T, layout paths.Layout) {
	t.Helper()
	updateDir := filepath.Join(filepath.Dir(layout.SingBoxBin), ".updates")
	if _, err := os.Stat(updateDir); !os.IsNotExist(err) {
		t.Fatalf("update dir should be removed, stat err = %v", err)
	}
}

// testReleaseDownloader serves both assets requested by Manager.Download: the
// sing-box archive and GitHub's release metadata containing the asset digest.
func testReleaseDownloader(t *testing.T, content, digestOverride string, archiveURL func(string)) func(context.Context, string, string) error {
	t.Helper()
	var asset, digest string
	return func(_ context.Context, url, dest string) error {
		switch {
		case strings.Contains(url, "/releases/download/"):
			if archiveURL != nil {
				archiveURL(url)
			}
			if err := writeTestSingBoxArchive(dest, content); err != nil {
				return err
			}
			asset = filepath.Base(dest)
			sum, err := sha256File(dest)
			if err != nil {
				return err
			}
			digest = hex.EncodeToString(sum)
			if digestOverride != "" {
				digest = digestOverride
			}
			return nil
		case strings.Contains(url, "/repos/SagerNet/sing-box/releases/tags/"):
			if asset == "" || digest == "" {
				return fmt.Errorf("release metadata requested before archive")
			}
			payload, err := json.Marshal(map[string]any{
				"assets": []map[string]string{{
					"name":   asset,
					"digest": "sha256:" + digest,
				}},
			})
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dest, payload, 0o644)
		default:
			return fmt.Errorf("unexpected download URL %q", url)
		}
	}
}

func writeTestSingBoxArchive(dest, content string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "sing-box-test/sing-box", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
