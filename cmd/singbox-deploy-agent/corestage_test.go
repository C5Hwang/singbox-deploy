package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
)

const stageTestTag = "v1.12.4"

func stageTestMetadata(asset string, archive []byte) []byte {
	sum := sha256.Sum256(archive)
	return []byte(fmt.Sprintf(`{"assets":[{"name":%q,"digest":"sha256:%s"}]}`, asset, hex.EncodeToString(sum[:])))
}

func pushedStageRequest(arch string, archive []byte) nodeapi.CoreStageRequest {
	name := release.SingBoxArchiveName(stageTestTag, "linux", arch)
	sum := sha256.Sum256(archive)
	return nodeapi.CoreStageRequest{
		SingBoxVersion:  stageTestTag,
		ArchiveName:     name,
		ArchiveSHA256:   hex.EncodeToString(sum[:]),
		Archive:         archive,
		ReleaseMetadata: stageTestMetadata(name, archive),
	}
}

func stageTestHandler(t *testing.T, download func(context.Context, string, string) error) *agentHandler {
	t.Helper()
	return &agentHandler{
		layout:       paths.LayoutForRoot(t.TempDir()),
		coreArch:     func() (string, error) { return "amd64", nil },
		downloadCore: download,
	}
}

// fetchStaged reads one URL through the staged downloader, failing the test
// if it would reach the network.
func fetchStaged(t *testing.T, layout paths.Layout, url string) ([]byte, error) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "out")
	download := stagedCoreDownload(layout, func(context.Context, string, string) error {
		return errors.New("network fallback used")
	})
	if err := download(context.Background(), url, dest); err != nil {
		return nil, err
	}
	return os.ReadFile(dest)
}

func TestStageCoreDirectDownloadServesInstallFromStage(t *testing.T) {
	archive := []byte("upstream archive")
	name := release.SingBoxArchiveName(stageTestTag, "linux", "amd64")
	h := stageTestHandler(t, func(_ context.Context, url, dest string) error {
		switch url {
		case release.SingBoxArchiveURL(stageTestTag, name):
			return os.WriteFile(dest, archive, 0o600)
		case release.SingBoxReleaseMetadataURL(stageTestTag):
			return os.WriteFile(dest, stageTestMetadata(name, archive), 0o600)
		}
		return fmt.Errorf("unexpected url %s", url)
	})

	var log bytes.Buffer
	if err := h.StageCore(context.Background(), nodeapi.CoreStageRequest{SingBoxVersion: stageTestTag}, &log); err != nil {
		t.Fatalf("StageCore: %v", err)
	}
	if !strings.Contains(log.String(), "downloaded from GitHub") {
		t.Fatalf("log = %q", log.String())
	}
	got, err := fetchStaged(t, h.layout, release.SingBoxArchiveURL(stageTestTag, name))
	if err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("staged archive = %q, err = %v", got, err)
	}
	metadata, err := fetchStaged(t, h.layout, release.SingBoxReleaseMetadataURL(stageTestTag))
	if err != nil || !bytes.Equal(metadata, stageTestMetadata(name, archive)) {
		t.Fatalf("staged metadata = %q, err = %v", metadata, err)
	}
	if _, err := fetchStaged(t, h.layout, release.SingBoxArchiveURL("v1.12.3", name)); err == nil {
		t.Fatal("a different release must fall through to the network")
	}
}

func TestStageCoreDirectFailureKeepsNoStage(t *testing.T) {
	h := stageTestHandler(t, func(context.Context, string, string) error {
		return errors.New("connection reset")
	})
	err := h.StageCore(context.Background(), nodeapi.CoreStageRequest{SingBoxVersion: stageTestTag}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "from GitHub") || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("StageCore error = %v", err)
	}
	if _, ok := readCoreStage(h.layout); ok {
		t.Fatal("a failed download must not leave a stage")
	}
	if _, err := os.Stat(coreStageDir(h.layout) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary staging dir left behind: %v", err)
	}
}

func TestStageCorePushedArchiveIsVerifiedAgainstUpstreamMetadata(t *testing.T) {
	h := stageTestHandler(t, func(context.Context, string, string) error {
		t.Fatal("a pushed archive must not touch the network")
		return nil
	})
	archive := []byte("hub archive")
	var log bytes.Buffer
	if err := h.StageCore(context.Background(), pushedStageRequest("amd64", archive), &log); err != nil {
		t.Fatalf("StageCore: %v", err)
	}
	if !strings.Contains(log.String(), "Hub-supplied") {
		t.Fatalf("log = %q", log.String())
	}
	name := release.SingBoxArchiveName(stageTestTag, "linux", "amd64")
	if got, err := fetchStaged(t, h.layout, release.SingBoxArchiveURL(stageTestTag, name)); err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("staged archive = %q, err = %v", got, err)
	}

	t.Run("wrong architecture", func(t *testing.T) {
		err := h.StageCore(context.Background(), pushedStageRequest("arm64", archive), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "needs "+name) {
			t.Fatalf("StageCore error = %v", err)
		}
	})
	t.Run("metadata disagrees", func(t *testing.T) {
		req := pushedStageRequest("amd64", []byte("other archive"))
		req.ReleaseMetadata = stageTestMetadata(name, archive)
		err := h.StageCore(context.Background(), req, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("StageCore error = %v", err)
		}
	})
	// Rejected pushes leave the previously verified stage in place.
	if got, err := fetchStaged(t, h.layout, release.SingBoxArchiveURL(stageTestTag, name)); err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("stage after rejected pushes = %q, err = %v", got, err)
	}
}

func TestStagedCoreDownloadRejectsModifiedArchive(t *testing.T) {
	h := stageTestHandler(t, nil)
	if err := h.StageCore(context.Background(), pushedStageRequest("amd64", []byte("hub archive")), io.Discard); err != nil {
		t.Fatalf("StageCore: %v", err)
	}
	name := release.SingBoxArchiveName(stageTestTag, "linux", "amd64")
	if err := os.WriteFile(filepath.Join(coreStageDir(h.layout), name), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchStaged(t, h.layout, release.SingBoxArchiveURL(stageTestTag, name)); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("modified stage error = %v", err)
	}
}

func TestAgentCoreChangeClearsConsumedStage(t *testing.T) {
	h := stageTestHandler(t, nil)
	if err := h.StageCore(context.Background(), pushedStageRequest("amd64", []byte("hub archive")), io.Discard); err != nil {
		t.Fatalf("StageCore: %v", err)
	}
	h.runCoreManager = func(_ context.Context, _ core.Action, tag string, _ io.Writer) (core.Result, error) {
		return core.Result{Tag: tag}, nil
	}
	h.readCoreVersion = func(context.Context) (string, error) { return stageTestTag, nil }
	h.coreActive = func(context.Context) bool { return true }
	if err := h.ChangeCore(context.Background(), nodeapi.CoreRequest{SingBoxVersion: stageTestTag}, io.Discard); err != nil {
		t.Fatalf("ChangeCore: %v", err)
	}
	if _, err := os.Stat(coreStageDir(h.layout)); !os.IsNotExist(err) {
		t.Fatalf("stage left after a successful core change: %v", err)
	}
}
