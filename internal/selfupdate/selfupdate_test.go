package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestRunCallsReplaceFailureRollbackAfterSpokesPrepared(t *testing.T) {
	body := []byte("verified-candidate")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	// Renaming a regular candidate over an existing directory fails after the
	// spoke preparation hook has succeeded.
	installPath := filepath.Join(root, "occupied")
	if err := os.Mkdir(installPath, 0o755); err != nil {
		t.Fatal(err)
	}
	prepared := false
	rolledBack := false
	activated := false
	m := &Manager{
		Download:   fakeDownload(bodies),
		GOARCH:     "amd64",
		InstallBin: installPath,
		BeforeReplace: func(_ context.Context, candidatePath, targetVersion string) error {
			prepared = true
			if targetVersion != "v2.0.0" {
				t.Fatalf("target version = %q", targetVersion)
			}
			if info, err := os.Stat(candidatePath); err != nil || info.Mode()&0o111 == 0 {
				t.Fatalf("candidate was not verified/executable: info=%v err=%v", info, err)
			}
			return nil
		},
		ReplaceFailed: func(_ context.Context, targetVersion string) error {
			rolledBack = true
			if targetVersion != "v2.0.0" {
				t.Fatalf("rollback target version = %q", targetVersion)
			}
			return nil
		},
		AfterReplace: func(context.Context, string) error {
			activated = true
			return nil
		},
	}
	if _, err := m.Run(context.Background(), "v2.0.0"); err == nil {
		t.Fatal("expected hub replacement to fail")
	}
	if !prepared || !rolledBack {
		t.Fatalf("prepared=%v rolledBack=%v", prepared, rolledBack)
	}
	if activated {
		t.Fatal("post-replace activation must not run when replacement failed")
	}
}

func TestRunCallsAfterReplaceForCommittedHub(t *testing.T) {
	body := []byte("verified-candidate")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	activated := false
	m := &Manager{
		Download:   fakeDownload(bodies),
		GOARCH:     "amd64",
		InstallBin: installPath,
		AfterReplace: func(_ context.Context, targetVersion string) error {
			activated = true
			if targetVersion != "v2.0.0" {
				t.Fatalf("activation target version = %q", targetVersion)
			}
			installed, err := os.ReadFile(installPath)
			if err != nil {
				t.Fatalf("read committed hub during activation: %v", err)
			}
			if string(installed) != string(body) {
				t.Fatalf("installed hub = %q", installed)
			}
			return nil
		},
	}
	if _, err := m.Run(context.Background(), "v2.0.0"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !activated {
		t.Fatal("post-replace activation was not called")
	}
}

func TestRunReturnsCommittedResultAndCleansUpWhenActivationFails(t *testing.T) {
	body := []byte("verified-candidate")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	m := &Manager{
		Download:   fakeDownload(bodies),
		GOARCH:     "amd64",
		InstallBin: installPath,
		AfterReplace: func(context.Context, string) error {
			return errors.New("monitor restart failed")
		},
	}
	result, err := m.Run(context.Background(), "v2.0.0")
	if err == nil {
		t.Fatal("expected committed activation error")
	}
	var committedErr *CommittedError
	if !errors.As(err, &committedErr) {
		t.Fatalf("error type = %T, want *CommittedError: %v", err, err)
	}
	if result.Tag != "v2.0.0" {
		t.Fatalf("result tag = %q, want committed v2.0.0", result.Tag)
	}
	installed, readErr := os.ReadFile(installPath)
	if readErr != nil {
		t.Fatalf("read committed binary: %v", readErr)
	}
	if string(installed) != string(body) {
		t.Fatalf("installed binary = %q", installed)
	}
	updateDir := filepath.Join(root, ".singbox-deploy-update")
	if _, statErr := os.Stat(updateDir); !os.IsNotExist(statErr) {
		t.Fatalf("temporary update directory remains after committed failure: %v", statErr)
	}
}
