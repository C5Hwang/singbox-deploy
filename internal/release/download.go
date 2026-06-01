package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxBinarySize caps extraction to guard against malformed archives (128 MiB).
const maxBinarySize = 128 << 20

// DownloadTo fetches url into destPath, creating parent directories.
func DownloadTo(ctx context.Context, httpClient *http.Client, url, destPath string) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// ExtractSingBox reads a sing-box .tar.gz stream and writes the "sing-box"
// binary to destPath with mode 0755. The archive nests the binary under a
// versioned directory (sing-box-<ver>-<os>-<arch>/sing-box).
func ExtractSingBox(gzStream io.Reader, destPath string) error {
	gz, err := gzip.NewReader(gzStream)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("sing-box binary not found in archive")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(hdr.Name) != "sing-box" {
			continue
		}
		if strings.Contains(hdr.Name, "..") {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.CopyN(out, tr, maxBinarySize); err != nil && err != io.EOF {
			return err
		}
		return nil
	}
}
