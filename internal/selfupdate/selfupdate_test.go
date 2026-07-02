package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fakeDownload serves the mapped body for each requested URL suffix.
func fakeDownload(bodies map[string][]byte) func(context.Context, string, string) error {
	return func(_ context.Context, url, dest string) error {
		for suffix, body := range bodies {
			if strings.HasSuffix(url, suffix) {
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return err
				}
				return os.WriteFile(dest, body, 0o644)
			}
		}
		return os.ErrNotExist
	}
}

func TestVerifyChecksumMatch(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	body := []byte("binary-contents")
	if err := os.WriteFile(bin, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte(sha256Hex(body)+"  singbox-deploy-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(bin, sums, "singbox-deploy-linux-amd64"); err != nil {
		t.Fatalf("expected checksum match: %v", err)
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte(sha256Hex([]byte("original"))+"  singbox-deploy-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(bin, sums, "singbox-deploy-linux-amd64"); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestRunRejectsTamperedBinary(t *testing.T) {
	good := []byte("real-new-binary")
	// SHA256SUMS advertises the good hash, but the served asset is tampered.
	// Run must fail at Verify, before the Replace step touches the install path.
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": []byte("tampered-binary"),
		"SHA256SUMS":                 []byte(sha256Hex(good) + "  singbox-deploy-linux-amd64\n"),
	}
	m := &Manager{
		Download:     fakeDownload(bodies),
		LatestStable: func(context.Context) (string, error) { return "v9.9.9", nil },
		GOARCH:       "amd64",
		InstallBin:   filepath.Join(t.TempDir(), "singbox-deploy"),
	}

	_, err := m.Run(context.Background(), "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch failure, got %v", err)
	}
}
