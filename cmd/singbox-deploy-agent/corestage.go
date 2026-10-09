package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/release"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// The Hub stages the exact sing-box release before every full install or core
// change. The Agent first fetches it from GitHub itself; a spoke that cannot
// reach GitHub (mainland China) gets the archive pushed by the Hub instead.
// Install and core change then read the staged files rather than downloading.
const (
	// directCoreDownloadTimeout bounds the Agent's own GitHub attempt so an
	// unusably slow route falls back to the Hub push within minutes.
	directCoreDownloadTimeout = 3 * time.Minute
	// directCoreDownloadIdle aborts a transfer that stops receiving data,
	// which is how a filtered GitHub connection usually fails.
	directCoreDownloadIdle = 30 * time.Second

	coreStageManifestFile = "stage.json"
	coreStageMetadataFile = "release.json"
)

// coreStageManifest names the staged release. It is written last, so a stage
// without one is incomplete and ignored.
type coreStageManifest struct {
	SingBoxVersion string `json:"singBoxVersion"`
	Archive        string `json:"archive"`
	SHA256         string `json:"sha256"`
}

// coreStageDir sits beside the managed binary so a runtime uninstall removes
// it together with the core.
func coreStageDir(layout paths.Layout) string {
	return filepath.Join(filepath.Dir(layout.SingBoxBin), ".staged")
}

// StageCore makes the requested release archive and its upstream metadata
// available for the next install or core change. Either source is verified
// against GitHub's published digest before it replaces the previous stage.
func (h *agentHandler) StageCore(ctx context.Context, req nodeapi.CoreStageRequest, log io.Writer) error {
	ctx = nonNilContext(ctx)
	if err := h.beginMutation(ctx); err != nil {
		return err
	}
	defer h.endMutation()

	if err := nodeapi.ValidateCoreStageRequest(req); err != nil {
		return err
	}
	detectArch := h.coreArch
	if detectArch == nil {
		detectArch = func() (string, error) {
			host, err := system.DetectHost()
			return host.Arch, err
		}
	}
	arch, err := detectArch()
	if err != nil {
		return fmt.Errorf("detect host for sing-box staging: %w", err)
	}
	tag := req.SingBoxVersion
	name := release.SingBoxArchiveName(tag, "linux", arch)

	dir := coreStageDir(h.layout)
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clean previous sing-box staging: %w", err)
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	manifest := coreStageManifest{SingBoxVersion: tag, Archive: name}
	if req.Pushed() {
		if req.ArchiveName != name {
			return fmt.Errorf("Hub pushed %s but this %s spoke needs %s", req.ArchiveName, arch, name)
		}
		archivePath := filepath.Join(tmp, name)
		metadataPath := filepath.Join(tmp, coreStageMetadataFile)
		if err := os.WriteFile(archivePath, req.Archive, 0o600); err != nil {
			return fmt.Errorf("write pushed sing-box archive: %w", err)
		}
		if err := os.WriteFile(metadataPath, req.ReleaseMetadata, 0o600); err != nil {
			return fmt.Errorf("write pushed release metadata: %w", err)
		}
		if err := release.VerifyReleaseAssetChecksum(metadataPath, name, archivePath); err != nil {
			return fmt.Errorf("verify pushed sing-box archive: %w", err)
		}
		manifest.SHA256 = req.ArchiveSHA256
		fmt.Fprintf(log, "staged Hub-supplied sing-box %s (%s)\n", tag, name)
	} else {
		download := h.downloadCore
		if download == nil {
			download = release.NewStallGuardedDownloader(directCoreDownloadIdle)
		}
		fmt.Fprintf(log, "downloading sing-box %s from GitHub...\n", tag)
		directCtx, cancel := context.WithTimeout(ctx, directCoreDownloadTimeout)
		defer cancel()
		archive, err := release.FetchSingBoxArchive(directCtx, download, tag, "linux", arch, tmp)
		if err != nil {
			return fmt.Errorf("download sing-box %s from GitHub: %w", tag, err)
		}
		manifest.SHA256 = archive.SHA256
		fmt.Fprintf(log, "staged sing-box %s downloaded from GitHub (%s)\n", tag, name)
	}

	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := state.WriteFileAtomic(filepath.Join(tmp, coreStageManifestFile), body, 0o600); err != nil {
		return fmt.Errorf("record sing-box staging: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("replace previous sing-box staging: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("activate sing-box staging: %w", err)
	}
	return nil
}

// stagedCoreDownload serves the staged release's archive and metadata URLs
// from disk and sends every other URL to fallback. Without a stage it is
// exactly fallback, which keeps an install driven by an older Hub working.
func stagedCoreDownload(layout paths.Layout, fallback func(ctx context.Context, url, dest string) error) func(ctx context.Context, url, dest string) error {
	return func(ctx context.Context, url, dest string) error {
		manifest, ok := readCoreStage(layout)
		if !ok {
			return fallback(ctx, url, dest)
		}
		dir := coreStageDir(layout)
		switch url {
		case release.SingBoxArchiveURL(manifest.SingBoxVersion, manifest.Archive):
			src := filepath.Join(dir, manifest.Archive)
			if err := copyStagedFile(src, dest); err != nil {
				return fmt.Errorf("use staged sing-box archive: %w", err)
			}
			digest, err := release.FileSHA256(dest)
			if err != nil {
				return fmt.Errorf("hash staged sing-box archive: %w", err)
			}
			if digest != manifest.SHA256 {
				return fmt.Errorf("staged sing-box archive %s changed on disk", manifest.Archive)
			}
			return nil
		case release.SingBoxReleaseMetadataURL(manifest.SingBoxVersion):
			if err := copyStagedFile(filepath.Join(dir, coreStageMetadataFile), dest); err != nil {
				return fmt.Errorf("use staged release metadata: %w", err)
			}
			return nil
		}
		return fallback(ctx, url, dest)
	}
}

// downloadDirect is the network fallback behind a stage.
func downloadDirect(ctx context.Context, url, dest string) error {
	return release.DownloadTo(ctx, nil, url, dest)
}

func readCoreStage(layout paths.Layout) (coreStageManifest, bool) {
	body, err := os.ReadFile(filepath.Join(coreStageDir(layout), coreStageManifestFile))
	if err != nil {
		return coreStageManifest{}, false
	}
	var manifest coreStageManifest
	if err := json.Unmarshal(body, &manifest); err != nil || manifest.SingBoxVersion == "" || manifest.Archive == "" {
		return coreStageManifest{}, false
	}
	return manifest, true
}

// clearCoreStage drops a consumed stage. Failure only leaves a stale archive
// behind, which the next stage replaces, so it is not an operation error.
func clearCoreStage(layout paths.Layout, log io.Writer) {
	if layout.SingBoxBin == "" {
		return
	}
	if err := os.RemoveAll(coreStageDir(layout)); err != nil {
		fmt.Fprintf(log, "warning: remove staged sing-box files: %v\n", err)
	}
}

func copyStagedFile(src, dest string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); retErr == nil && closeErr != nil {
			retErr = closeErr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
