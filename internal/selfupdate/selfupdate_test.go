package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func acceptCandidate(context.Context, string, string) error { return nil }

func reportInstalled(version string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return version, nil }
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
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		LatestStable:     func(context.Context) (string, error) { return "v9.9.9", nil },
		GOARCH:           "amd64",
		InstallBin:       filepath.Join(t.TempDir(), "singbox-deploy"),
		InstalledVersion: reportInstalled("v1.0.0"),
	}

	_, err := m.Run(context.Background(), "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch failure, got %v", err)
	}
	updateDir := filepath.Join(filepath.Dir(m.InstallBin), ".singbox-deploy-update")
	if _, statErr := os.Stat(updateDir); !os.IsNotExist(statErr) {
		t.Fatalf("failed update staging directory remains: %v", statErr)
	}
}

func TestValidateTransitionPreventsDowngradeAndRecognizesEquality(t *testing.T) {
	tests := []struct {
		name, current, target string
		upToDate              bool
		wantErr               string
	}{
		{name: "upgrade", current: "v2.0.0", target: "v2.1.0"},
		{name: "equal without v prefix", current: "2.0.0", target: "v2.0.0", upToDate: true},
		{name: "downgrade", current: "v2.1.0", target: "v2.0.0", wantErr: "refusing self-update downgrade"},
		{name: "dev to stable", current: "dev", target: "v2.0.0"},
		{name: "invalid target", current: "v2.0.0", target: "latest", wantErr: "not a stable semantic version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upToDate, err := ValidateTransition(tt.current, tt.target)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ValidateTransition error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || upToDate != tt.upToDate {
				t.Fatalf("ValidateTransition = %v, %v; want %v, nil", upToDate, err, tt.upToDate)
			}
		})
	}
}

func TestRunSkipsDownloadForSameVersionAndRejectsDowngrade(t *testing.T) {
	for _, tt := range []struct {
		name, current, target string
		upToDate              bool
		wantErr               string
	}{
		{name: "same", current: "v2.0.0", target: "2.0.0", upToDate: true},
		{name: "older", current: "v2.1.0", target: "v2.0.0", wantErr: "older than installed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			downloads := 0
			m := &Manager{
				Version: tt.current,
				Download: func(context.Context, string, string) error {
					downloads++
					return nil
				},
				InstallBin: filepath.Join(t.TempDir(), "singbox-deploy"),
			}
			result, err := m.Run(context.Background(), tt.target)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Run error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil || result.UpToDate != tt.upToDate {
				t.Fatalf("Run result = %+v, %v", result, err)
			}
			if downloads != 0 {
				t.Fatalf("downloads = %d, want 0", downloads)
			}
		})
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
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		InspectCandidate: acceptCandidate,
		InstalledVersion: reportInstalled("v1.0.0"),
		GOARCH:           "amd64",
		InstallBin:       installPath,
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
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		InspectCandidate: acceptCandidate,
		InstalledVersion: reportInstalled("v1.0.0"),
		GOARCH:           "amd64",
		InstallBin:       installPath,
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

func TestRunRejectsConcurrentUpdateBeforeDownloadOrSpokeMutation(t *testing.T) {
	body := []byte("verified-candidate")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	enteredSpokeStep := make(chan struct{})
	releaseSpokeStep := make(chan struct{})
	first := &Manager{
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		InspectCandidate: acceptCandidate,
		InstalledVersion: reportInstalled("v1.0.0"),
		GOARCH:           "amd64",
		InstallBin:       installPath,
		BeforeReplace: func(context.Context, string, string) error {
			close(enteredSpokeStep)
			<-releaseSpokeStep
			return nil
		},
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), "v2.0.0")
		firstResult <- err
	}()
	<-enteredSpokeStep

	var secondDownloads atomic.Int32
	second := &Manager{
		Version: "v1.0.0",
		Download: func(context.Context, string, string) error {
			secondDownloads.Add(1)
			return nil
		},
		InspectCandidate: acceptCandidate,
		InstalledVersion: reportInstalled("v1.0.0"),
		GOARCH:           "amd64",
		InstallBin:       installPath,
		BeforeReplace: func(context.Context, string, string) error {
			t.Fatal("concurrent update reached spoke mutation")
			return nil
		},
	}
	if _, err := second.Run(context.Background(), "v2.0.0"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent update error = %v", err)
	}
	if calls := secondDownloads.Load(); calls != 0 {
		t.Fatalf("concurrent update downloads = %d, want 0", calls)
	}
	close(releaseSpokeStep)
	if err := <-firstResult; err != nil {
		t.Fatalf("first update: %v", err)
	}
	lockPath := filepath.Join(root, ".singbox-deploy-update.lock")
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat persistent lock: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRunOldProcessSkipsTransactionAlreadyCommittedByPeer(t *testing.T) {
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	oldBinary := []byte("#!/bin/sh\nprintf '%s\\n' v1.0.0\n")
	newBinary := []byte("#!/bin/sh\nprintf '%s\\n' v2.0.0\n")
	if err := os.WriteFile(installPath, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": newBinary,
		"SHA256SUMS":                 []byte(sha256Hex(newBinary) + "  singbox-deploy-linux-amd64\n"),
	}
	firstSpokeCalls := 0
	first := &Manager{
		Version:    "v1.0.0",
		Download:   fakeDownload(bodies),
		GOARCH:     "amd64",
		InstallBin: installPath,
		BeforeReplace: func(context.Context, string, string) error {
			firstSpokeCalls++
			return nil
		},
	}
	if _, err := first.Run(context.Background(), "v2.0.0"); err != nil {
		t.Fatalf("first update: %v", err)
	}

	var secondDownloads atomic.Int32
	secondSpokeCalls := 0
	secondRollbackCalls := 0
	staleProcess := &Manager{
		Version: "v1.0.0",
		Download: func(context.Context, string, string) error {
			secondDownloads.Add(1)
			return errors.New("stale process must not download")
		},
		GOARCH:     "amd64",
		InstallBin: installPath,
		BeforeReplace: func(context.Context, string, string) error {
			secondSpokeCalls++
			return nil
		},
		ReplaceFailed: func(context.Context, string) error {
			secondRollbackCalls++
			return nil
		},
	}
	result, err := staleProcess.Run(context.Background(), "v2.0.0")
	if err != nil {
		t.Fatalf("stale process retry: %v", err)
	}
	if !result.UpToDate || result.Tag != "v2.0.0" {
		t.Fatalf("stale process result = %+v, want target already installed", result)
	}
	if firstSpokeCalls != 1 || secondSpokeCalls != 0 || secondRollbackCalls != 0 {
		t.Fatalf("spoke calls: first=%d second=%d rollback=%d", firstSpokeCalls, secondSpokeCalls, secondRollbackCalls)
	}
	if downloads := secondDownloads.Load(); downloads != 0 {
		t.Fatalf("stale process downloads = %d, want 0", downloads)
	}
}

func TestRunRejectsUnexpectedInstalledVersionAfterLock(t *testing.T) {
	downloads := 0
	m := &Manager{
		Version: "v1.0.0",
		Download: func(context.Context, string, string) error {
			downloads++
			return nil
		},
		InstalledVersion: reportInstalled("v1.5.0"),
		InstallBin:       filepath.Join(t.TempDir(), "singbox-deploy"),
	}
	_, err := m.Run(context.Background(), "v2.0.0")
	if err == nil || !strings.Contains(err.Error(), "restart the Hub TUI") {
		t.Fatalf("unexpected installed version error = %v", err)
	}
	if downloads != 0 {
		t.Fatalf("downloads = %d, want 0", downloads)
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
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		InspectCandidate: acceptCandidate,
		InstalledVersion: reportInstalled("v1.0.0"),
		GOARCH:           "amd64",
		InstallBin:       installPath,
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

func TestRunRejectsWrongCandidateVersionBeforeReplace(t *testing.T) {
	body := []byte("#!/bin/sh\nprintf '%s\\n' v2.0.1\n")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	old := []byte("old-hub-binary")
	if err := os.WriteFile(installPath, old, 0o755); err != nil {
		t.Fatal(err)
	}
	beforeReplace := false
	m := &Manager{
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		GOARCH:           "amd64",
		InstallBin:       installPath,
		InstalledVersion: reportInstalled("v1.0.0"),
		BeforeReplace: func(context.Context, string, string) error {
			beforeReplace = true
			return nil
		},
	}

	_, err := m.Run(context.Background(), "v2.0.0")
	if err == nil || !strings.Contains(err.Error(), `candidate reports version "v2.0.1", expected "v2.0.0"`) {
		t.Fatalf("expected exact candidate version failure, got %v", err)
	}
	if beforeReplace {
		t.Fatal("spoke preparation must not run for a wrong-version hub candidate")
	}
	installed, readErr := os.ReadFile(installPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(installed) != string(old) {
		t.Fatalf("installed hub changed after rejected candidate: got %q", installed)
	}
}

func TestRunRejectsDamagedCandidateBeforeReplace(t *testing.T) {
	body := []byte("not an executable image")
	bodies := map[string][]byte{
		"singbox-deploy-linux-amd64": body,
		"SHA256SUMS":                 []byte(sha256Hex(body) + "  singbox-deploy-linux-amd64\n"),
	}
	root := t.TempDir()
	installPath := filepath.Join(root, "singbox-deploy")
	old := []byte("old-hub-binary")
	if err := os.WriteFile(installPath, old, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		Version:          "v1.0.0",
		Download:         fakeDownload(bodies),
		GOARCH:           "amd64",
		InstallBin:       installPath,
		InstalledVersion: reportInstalled("v1.0.0"),
	}

	_, err := m.Run(context.Background(), "v2.0.0")
	if err == nil || !strings.Contains(err.Error(), "run candidate --version") {
		t.Fatalf("expected damaged candidate execution failure, got %v", err)
	}
	installed, readErr := os.ReadFile(installPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(installed) != string(old) {
		t.Fatalf("installed hub changed after rejected candidate: got %q", installed)
	}
}
