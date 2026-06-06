package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	runner := &recordingRunner{}
	var events []deploy.Event
	var downloadedURL string
	m := &Manager{
		Runner: runner,
		Layout: layout,
		GOOS:   "linux",
		GOARCH: "amd64",
		Download: func(_ context.Context, url, dest string) error {
			downloadedURL = url
			return writeTestSingBoxArchive(dest, "new-sing-box")
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
	for _, want := range []string{
		"systemctl stop sing-box.service",
		layout.SingBoxBin + " check -c " + layout.ConfigJSON,
		"systemctl restart sing-box.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	if len(events) == 0 || events[len(events)-1].Label != "Cleanup" || events[len(events)-1].Status != "ok" {
		t.Fatalf("unexpected final event: %#v", events)
	}
}

func TestChangeStableUsesSelectedRelease(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	runner := &recordingRunner{}
	var downloadedURL string
	m := &Manager{
		Runner: runner,
		Layout: layout,
		GOOS:   "linux",
		GOARCH: "arm64",
		Download: func(_ context.Context, url, dest string) error {
			downloadedURL = url
			return writeTestSingBoxArchive(dest, "selected-sing-box")
		},
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
