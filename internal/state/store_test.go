package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadWriteString(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.WriteString("subscribe_salt", "abc123\n", 0o600); err != nil {
		t.Fatalf("WriteString error: %v", err)
	}
	got, err := s.ReadString("subscribe_salt")
	if err != nil {
		t.Fatalf("ReadString error: %v", err)
	}
	if got != "abc123\n" {
		t.Fatalf("got %q", got)
	}
	info, err := os.Stat(filepath.Join(dir, "subscribe_salt"))
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestWriteFilePairRollsBackFirstFileWhenSecondRenameFails(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "example.key")
	certPath := filepath.Join(dir, "example.crt")
	if err := os.WriteFile(keyPath, []byte("old key"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, []byte("old cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected second rename failure")
	renameCalls := 0
	renameFile := func(oldPath, newPath string) error {
		renameCalls++
		if renameCalls == 2 {
			return injected
		}
		return os.Rename(oldPath, newPath)
	}
	err := writeFilePair(
		keyPath, []byte("new key"), 0o600,
		certPath, []byte("new cert"), 0o644,
		renameFile,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("writeFilePair error = %v, want injected error", err)
	}
	if renameCalls != 3 {
		t.Fatalf("rename calls = %d, want commit, failure, rollback", renameCalls)
	}
	assertFileContents(t, keyPath, "old key")
	assertFileContents(t, certPath, "old cert")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored key mode = %v, want 0640", info.Mode().Perm())
	}
	assertNoPairTemps(t, dir)
}

func TestWriteFilePairRemovesFirstFileWhenSecondRenameFailsOnNewPair(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "example.key")
	certPath := filepath.Join(dir, "example.crt")
	injected := errors.New("injected second rename failure")
	renameCalls := 0
	renameFile := func(oldPath, newPath string) error {
		renameCalls++
		if renameCalls == 2 {
			return injected
		}
		return os.Rename(oldPath, newPath)
	}

	err := writeFilePair(
		keyPath, []byte("new key"), 0o600,
		certPath, []byte("new cert"), 0o644,
		renameFile,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("writeFilePair error = %v, want injected error", err)
	}
	for _, path := range []string{keyPath, certPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("stat %s error = %v, want not exist", path, statErr)
		}
	}
	assertNoPairTemps(t, dir)
}

func TestWriteFilePairRetainsRecoveryCopyWhenRollbackFails(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "example.key")
	certPath := filepath.Join(dir, "example.crt")
	if err := os.WriteFile(keyPath, []byte("old key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, []byte("old cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	commitFailure := errors.New("injected second rename failure")
	rollbackFailure := errors.New("injected rollback failure")
	renameCalls := 0
	renameFile := func(oldPath, newPath string) error {
		renameCalls++
		switch renameCalls {
		case 2:
			return commitFailure
		case 3:
			return rollbackFailure
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	err := writeFilePair(
		keyPath, []byte("new key"), 0o600,
		certPath, []byte("new cert"), 0o644,
		renameFile,
	)
	if !errors.Is(err, commitFailure) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("writeFilePair error = %v, want commit and rollback errors", err)
	}
	assertFileContents(t, keyPath, "new key")
	assertFileContents(t, certPath, "old cert")
	matches, globErr := filepath.Glob(filepath.Join(dir, "example.key.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("recovery copies = %v, want one", matches)
	}
	assertFileContents(t, matches[0], "old key")
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
}

func assertNoPairTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}
