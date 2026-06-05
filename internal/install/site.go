package install

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	assets "github.com/C5Hwang/singbox-deploy/template"
)

const DefaultSiteTemplate = "massively"

var siteTemplates = []string{
	"massively",
	"ethereal",
	"dimension",
	"forty",
	"parallelism",
	"aerial",
	"landed",
	"fractal",
}

// SiteTemplateOptions returns the supported masquerade site template names.
func SiteTemplateOptions() []string {
	return append([]string(nil), siteTemplates...)
}

// NormalizeSiteTemplate validates a selected masquerade site template, applying
// the default when the input is empty.
func NormalizeSiteTemplate(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultSiteTemplate
	}
	for _, template := range siteTemplates {
		if name == template {
			return name, nil
		}
	}
	return "", fmt.Errorf("unsupported masquerade site template %q", name)
}

func deploySiteTemplate(layout paths.Layout, name string) error {
	name, err := NormalizeSiteTemplate(name)
	if err != nil {
		return err
	}
	data, err := assets.FS.ReadFile("site/" + name + ".zip")
	if err != nil {
		return fmt.Errorf("read masquerade site template %q: %w", name, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open masquerade site template %q: %w", name, err)
	}
	if err := resetWebRoot(layout.WebRoot); err != nil {
		return err
	}
	if err := extractSiteArchive(archive, layout.WebRoot); err != nil {
		return fmt.Errorf("extract masquerade site template %q: %w", name, err)
	}
	return nil
}

func resetWebRoot(root string) error {
	root = filepath.Clean(root)
	if root == "." || root == string(os.PathSeparator) {
		return fmt.Errorf("refuse to reset unsafe web root %q", root)
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return os.MkdirAll(root, 0o755)
}

func extractSiteArchive(archive *zip.Reader, root string) error {
	hasIndex := false
	for _, f := range archive.File {
		name, err := safeZipPath(f.Name)
		if err != nil {
			return err
		}
		if name == "index.html" {
			hasIndex = true
		}
		target := filepath.Join(root, name)
		mode := f.FileInfo().Mode()
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("unsupported zip entry %q", f.Name)
		}
		if err := extractSiteFile(f, target, mode.Perm()); err != nil {
			return err
		}
	}
	if !hasIndex {
		return fmt.Errorf("masquerade site template is missing index.html")
	}
	return nil
}

func safeZipPath(name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe zip entry %q", name)
	}
	return clean, nil
}

func extractSiteFile(f *zip.File, target string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Chmod(perm)
}
