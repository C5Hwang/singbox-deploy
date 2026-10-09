package release

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SingBoxArchiveURL returns the GitHub release download URL of one sing-box
// archive.
func SingBoxArchiveURL(tag, archive string) string {
	return fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", tag, archive)
}

// SingBoxReleaseMetadataURL returns the GitHub API URL of one sing-box
// release, whose asset list carries the SHA-256 digest of every archive.
func SingBoxReleaseMetadataURL(tag string) string {
	return fmt.Sprintf("https://api.github.com/repos/SagerNet/sing-box/releases/tags/%s", tag)
}

// SingBoxArchive is a downloaded sing-box archive whose digest matched the
// upstream release metadata stored beside it.
type SingBoxArchive struct {
	Name         string
	Path         string
	MetadataPath string
	SHA256       string
}

// FetchSingBoxArchive downloads the archive of tag for goos/goarch and the
// release metadata into dir, then verifies the archive against the metadata
// digest. The files are left in dir for the caller to consume.
func FetchSingBoxArchive(
	ctx context.Context,
	download func(ctx context.Context, url, dest string) error,
	tag, goos, goarch, dir string,
) (SingBoxArchive, error) {
	name := SingBoxArchiveName(tag, goos, goarch)
	archive := SingBoxArchive{
		Name:         name,
		Path:         filepath.Join(dir, name),
		MetadataPath: filepath.Join(dir, "release.json"),
	}
	if err := download(ctx, SingBoxArchiveURL(tag, name), archive.Path); err != nil {
		return SingBoxArchive{}, err
	}
	if err := download(ctx, SingBoxReleaseMetadataURL(tag), archive.MetadataPath); err != nil {
		return SingBoxArchive{}, fmt.Errorf("download upstream release metadata: %w", err)
	}
	if err := VerifyReleaseAssetChecksum(archive.MetadataPath, name, archive.Path); err != nil {
		return SingBoxArchive{}, err
	}
	digest, err := FileSHA256(archive.Path)
	if err != nil {
		return SingBoxArchive{}, fmt.Errorf("hash downloaded archive: %w", err)
	}
	archive.SHA256 = digest
	return archive, nil
}

type releaseMetadata struct {
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// VerifyReleaseAssetChecksum checks archivePath against the SHA-256 digest
// GitHub publishes for asset in the release metadata at metadataPath.
func VerifyReleaseAssetChecksum(metadataPath, asset, archivePath string) error {
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
	got, err := FileSHA256(archivePath)
	if err != nil {
		return fmt.Errorf("hash downloaded archive: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(hex.EncodeToString(want))) != 1 {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, encoded, got)
	}
	return nil
}

// FileSHA256 returns the lowercase hexadecimal SHA-256 digest of path.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// errDownloadStalled is the cancellation cause of a download that made no
// progress for its idle window.
var errDownloadStalled = fmt.Errorf("download stalled")

// NewStallGuardedDownloader returns a download function for networks where
// GitHub is reachable only intermittently. Connection setup is bounded, and
// a transfer that receives no bytes for idle is aborted instead of hanging
// until the caller's overall deadline.
func NewStallGuardedDownloader(idle time.Duration) func(ctx context.Context, url, dest string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = idle
	client := &http.Client{Transport: transport}
	return func(ctx context.Context, url, dest string) error {
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		timer := time.AfterFunc(idle, func() { cancel(errDownloadStalled) })
		defer timer.Stop()
		err := downloadTo(ctx, client, url, dest, func(body io.Reader) io.Reader {
			return &idleResetReader{r: body, timer: timer, idle: idle}
		})
		if cause := context.Cause(ctx); err != nil && cause == errDownloadStalled {
			return fmt.Errorf("download %s: no data received for %s", url, idle)
		}
		return err
	}
}

// idleResetReader re-arms the stall timer whenever the body yields data.
type idleResetReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (r *idleResetReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(r.idle)
	}
	return n, err
}
