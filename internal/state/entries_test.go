package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testEntry struct {
	Name  string
	Value string
}

func encodeTestEntry(entry testEntry) map[string]string {
	return map[string]string{"name": entry.Name, "value": entry.Value}
}

func decodeTestEntry(root string) testEntry {
	return testEntry{
		Name:  ReadEntryValue(root, "name", ""),
		Value: ReadEntryValue(root, "value", ""),
	}
}

func TestSaveEntryDirsReplacesWholeTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	initial := []testEntry{{Name: "one", Value: "old"}, {Name: "two", Value: "stale"}}
	if err := SaveEntryDirs(dir, initial, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	want := []testEntry{{Name: "replacement", Value: "new"}}
	if err := SaveEntryDirs(dir, want, encodeTestEntry); err != nil {
		t.Fatalf("replacement SaveEntryDirs error: %v", err)
	}
	got, err := LoadEntryDirs(dir, decodeTestEntry)
	if err != nil {
		t.Fatalf("LoadEntryDirs error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded entries = %#v, want %#v", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "001" {
		t.Fatalf("replacement tree entries = %v, want only 001", entryNames(entries))
	}
	assertNoEntryDirArtifacts(t, dir)
}

func TestUpdateEntryFieldsWritesOnlyTheChangedFieldsInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	initial := []testEntry{{Name: "one", Value: "old"}, {Name: "two", Value: "keep"}}
	if err := SaveEntryDirs(dir, initial, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}
	// A whole-tree restage discards anything it did not write, so surviving
	// markers prove the update stayed in place.
	markers := map[string]string{
		filepath.Join(dir, "001", "marker"): "first",
		filepath.Join(dir, "002", "marker"): "second",
	}
	for path, contents := range markers {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write marker %s: %v", path, err)
		}
	}
	nameBefore, err := os.Stat(filepath.Join(dir, "001", "name"))
	if err != nil {
		t.Fatalf("stat unchanged field: %v", err)
	}

	updated, err := UpdateEntryFields(dir, decodeTestEntry, encodeTestEntry,
		func(entry testEntry) bool { return entry.Name == "one" },
		func(entry *testEntry) error {
			entry.Value = "new"
			return nil
		})
	if err != nil {
		t.Fatalf("UpdateEntryFields error: %v", err)
	}
	if updated != (testEntry{Name: "one", Value: "new"}) {
		t.Fatalf("returned entry = %#v", updated)
	}

	got, err := LoadEntryDirs(dir, decodeTestEntry)
	if err != nil {
		t.Fatalf("LoadEntryDirs error: %v", err)
	}
	want := []testEntry{{Name: "one", Value: "new"}, {Name: "two", Value: "keep"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded entries = %#v, want %#v", got, want)
	}
	for path, contents := range markers {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != contents {
			t.Fatalf("entry tree was restaged; marker %s = %q, err %v", path, body, err)
		}
	}
	nameAfter, err := os.Stat(filepath.Join(dir, "001", "name"))
	if err != nil {
		t.Fatalf("stat unchanged field after update: %v", err)
	}
	if !os.SameFile(nameBefore, nameAfter) {
		t.Fatal("unchanged field file was rewritten")
	}
}

func TestUpdateEntryFieldsReportsMissingEntryAndPropagatesMutationError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	if err := SaveEntryDirs(dir, []testEntry{{Name: "one", Value: "old"}}, encodeTestEntry); err != nil {
		t.Fatalf("SaveEntryDirs error: %v", err)
	}
	noMutation := func(*testEntry) error { t.Fatal("mutate must not run without a match"); return nil }
	if _, err := UpdateEntryFields(dir, decodeTestEntry, encodeTestEntry,
		func(entry testEntry) bool { return entry.Name == "missing" }, noMutation); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing entry error = %v, want ErrEntryNotFound", err)
	}
	if _, err := UpdateEntryFields(filepath.Join(t.TempDir(), "absent"), decodeTestEntry, encodeTestEntry,
		func(testEntry) bool { return true }, noMutation); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing tree error = %v, want ErrEntryNotFound", err)
	}

	sentinel := errors.New("rejected")
	if _, err := UpdateEntryFields(dir, decodeTestEntry, encodeTestEntry,
		func(entry testEntry) bool { return entry.Name == "one" },
		func(entry *testEntry) error {
			entry.Value = "new"
			return sentinel
		}); !errors.Is(err, sentinel) {
		t.Fatalf("mutation error = %v, want %v", err, sentinel)
	}
	got, err := LoadEntryDirs(dir, decodeTestEntry)
	if err != nil {
		t.Fatalf("LoadEntryDirs error: %v", err)
	}
	if len(got) != 1 || got[0].Value != "old" {
		t.Fatalf("failed mutation changed the tree: %#v", got)
	}
}

func TestSaveEntryDirsBuildFailurePreservesPreviousTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	want := []testEntry{{Name: "preserved", Value: "old"}}
	if err := SaveEntryDirs(dir, want, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}

	err := SaveEntryDirs(dir, []testEntry{{Name: "replacement", Value: "new"}}, func(testEntry) map[string]string {
		// A field named "." is never a safe entry filename.
		return map[string]string{".": "cannot replace a directory"}
	})
	if err == nil {
		t.Fatal("SaveEntryDirs unexpectedly succeeded")
	}
	got, loadErr := LoadEntryDirs(dir, decodeTestEntry)
	if loadErr != nil {
		t.Fatalf("LoadEntryDirs error: %v", loadErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries after failed save = %#v, want preserved %#v", got, want)
	}
	assertNoEntryDirArtifacts(t, dir)
}

func TestSaveEntryDirsRejectsFieldTraversal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "entries")
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveEntryDirs(dir, []testEntry{{Name: "old"}}, encodeTestEntry); err != nil {
		t.Fatal(err)
	}
	err := SaveEntryDirs(dir, []testEntry{{Name: "new"}}, func(testEntry) map[string]string {
		return map[string]string{"../../victim": "overwritten"}
	})
	if err == nil {
		t.Fatal("SaveEntryDirs accepted a traversing field name")
	}
	contents, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "preserved" {
		t.Fatalf("victim contents = %q", contents)
	}
	entries, loadErr := LoadEntryDirs(dir, decodeTestEntry)
	if loadErr != nil || len(entries) != 1 || entries[0].Name != "old" {
		t.Fatalf("old tree was not preserved: entries=%+v err=%v", entries, loadErr)
	}
}

func TestSaveEntryDirsCommitFailureRestoresPreviousTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	want := []testEntry{{Name: "preserved", Value: "old"}}
	if err := SaveEntryDirs(dir, want, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}

	injectedErr := errors.New("injected commit failure")
	renameCalls := 0
	err := saveEntryDirs(dir, []testEntry{{Name: "replacement", Value: "new"}}, encodeTestEntry, func(oldPath, newPath string) error {
		renameCalls++
		if renameCalls == 2 {
			return injectedErr
		}
		return os.Rename(oldPath, newPath)
	})
	if !errors.Is(err, injectedErr) {
		t.Fatalf("SaveEntryDirs error = %v, want injected commit failure", err)
	}
	if renameCalls != 3 {
		t.Fatalf("rename calls = %d, want move, failed commit, and restore", renameCalls)
	}
	got, loadErr := LoadEntryDirs(dir, decodeTestEntry)
	if loadErr != nil {
		t.Fatalf("LoadEntryDirs error: %v", loadErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries after failed commit = %#v, want restored %#v", got, want)
	}
	assertNoEntryDirArtifacts(t, dir)
}

func TestLoadEntryDirsWaitsForSaveOnSameTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	if err := SaveEntryDirs(dir, []testEntry{{Name: "old"}}, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}

	encodeStarted := make(chan struct{})
	releaseEncode := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- SaveEntryDirs(dir, []testEntry{{Name: "new"}}, func(entry testEntry) map[string]string {
			close(encodeStarted)
			<-releaseEncode
			return encodeTestEntry(entry)
		})
	}()
	<-encodeStarted

	loadStarted := make(chan struct{})
	type loadResult struct {
		entries []testEntry
		err     error
	}
	loadDone := make(chan loadResult, 1)
	go func() {
		close(loadStarted)
		entries, err := LoadEntryDirs(dir, decodeTestEntry)
		loadDone <- loadResult{entries: entries, err: err}
	}()
	<-loadStarted
	select {
	case result := <-loadDone:
		close(releaseEncode)
		t.Fatalf("LoadEntryDirs completed during SaveEntryDirs with result %#v, error %v", result.entries, result.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseEncode)
	if err := <-saveDone; err != nil {
		t.Fatalf("SaveEntryDirs error: %v", err)
	}
	result := <-loadDone
	if result.err != nil {
		t.Fatalf("LoadEntryDirs error: %v", result.err)
	}
	want := []testEntry{{Name: "new"}}
	if !reflect.DeepEqual(result.entries, want) {
		t.Fatalf("loaded entries = %#v, want %#v", result.entries, want)
	}
}

func TestSaveEntryDirsUsesIndependentLocksForDifferentTrees(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- SaveEntryDirs(firstDir, []testEntry{{Name: "first"}}, func(entry testEntry) map[string]string {
			close(firstStarted)
			<-releaseFirst
			return encodeTestEntry(entry)
		})
	}()
	<-firstStarted

	secondEncoded := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- SaveEntryDirs(secondDir, []testEntry{{Name: "second"}}, func(entry testEntry) map[string]string {
			close(secondEncoded)
			return encodeTestEntry(entry)
		})
	}()
	select {
	case <-secondEncoded:
	case <-time.After(time.Second):
		close(releaseFirst)
		t.Fatal("SaveEntryDirs for a different tree was blocked")
	}
	if err := <-secondDone; err != nil {
		close(releaseFirst)
		t.Fatalf("second SaveEntryDirs error: %v", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first SaveEntryDirs error: %v", err)
	}
}

func TestTransactEntryDirsPreservesIndependentCrossProcessUpdates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	if err := SaveEntryDirs(dir, []testEntry{{Name: "original", Value: "original"}}, encodeTestEntry); err != nil {
		t.Fatalf("initial SaveEntryDirs error: %v", err)
	}

	parentEntered := make(chan struct{})
	releaseParent := make(chan struct{})
	parentDone := make(chan error, 1)
	go func() {
		_, err := TransactEntryDirs(dir, decodeTestEntry, encodeTestEntry, func(entries []testEntry) ([]testEntry, error) {
			close(parentEntered)
			<-releaseParent
			entries[0].Name = "parent"
			return entries, nil
		})
		parentDone <- err
	}()
	<-parentEntered

	childStarted := filepath.Join(filepath.Dir(dir), "child-started")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTransactEntryDirsSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"SINGBOX_ENTRY_TRANSACTION_HELPER=1",
		"SINGBOX_ENTRY_TRANSACTION_DIR="+dir,
		"SINGBOX_ENTRY_TRANSACTION_STARTED="+childStarted,
	)
	if err := cmd.Start(); err != nil {
		close(releaseParent)
		<-parentDone
		t.Fatalf("start transaction helper: %v", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(childStarted); err == nil {
			break
		} else if !os.IsNotExist(err) {
			close(releaseParent)
			<-parentDone
			t.Fatalf("inspect helper start marker: %v", err)
		}
		if time.Now().After(deadline) {
			close(releaseParent)
			<-parentDone
			_ = cmd.Process.Kill()
			<-childDone
			t.Fatal("transaction helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The helper writes its marker immediately before entering the transaction.
	// It must remain blocked on the sibling flock until the parent commits.
	select {
	case err := <-childDone:
		close(releaseParent)
		<-parentDone
		t.Fatalf("cross-process transaction completed before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseParent)
	if err := <-parentDone; err != nil {
		_ = cmd.Process.Kill()
		<-childDone
		t.Fatalf("parent transaction: %v", err)
	}
	if err := <-childDone; err != nil {
		t.Fatalf("child transaction: %v", err)
	}

	got, err := LoadEntryDirs(dir, decodeTestEntry)
	if err != nil {
		t.Fatalf("LoadEntryDirs error: %v", err)
	}
	want := []testEntry{{Name: "parent", Value: "child"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries after cross-process transactions = %#v, want %#v", got, want)
	}
	assertNoEntryDirArtifacts(t, dir)
}

func TestTransactEntryDirsSubprocessHelper(t *testing.T) {
	if os.Getenv("SINGBOX_ENTRY_TRANSACTION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	dir := os.Getenv("SINGBOX_ENTRY_TRANSACTION_DIR")
	started := os.Getenv("SINGBOX_ENTRY_TRANSACTION_STARTED")
	if err := os.WriteFile(started, []byte("started"), 0o600); err != nil {
		t.Fatalf("write start marker: %v", err)
	}
	_, err := TransactEntryDirs(dir, decodeTestEntry, encodeTestEntry, func(entries []testEntry) ([]testEntry, error) {
		if len(entries) != 1 {
			return nil, errors.New("unexpected entry count")
		}
		entries[0].Value = "child"
		return entries, nil
	})
	if err != nil {
		t.Fatalf("TransactEntryDirs: %v", err)
	}
}

func TestTransactEntryDirsMutationErrorPreservesTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "entries")
	want := []testEntry{{Name: "preserved", Value: "old"}}
	if err := SaveEntryDirs(dir, want, encodeTestEntry); err != nil {
		t.Fatal(err)
	}
	injectedErr := errors.New("mutation failed")
	_, err := TransactEntryDirs(dir, decodeTestEntry, encodeTestEntry, func(entries []testEntry) ([]testEntry, error) {
		entries[0].Value = "must not persist"
		return entries, injectedErr
	})
	if !errors.Is(err, injectedErr) {
		t.Fatalf("TransactEntryDirs error = %v, want %v", err, injectedErr)
	}
	got, loadErr := LoadEntryDirs(dir, decodeTestEntry)
	if loadErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tree changed after aborted transaction: got=%#v err=%v", got, loadErr)
	}
}

func assertNoEntryDirArtifacts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("read entry tree parent: %v", err)
	}
	prefix := "." + filepath.Base(dir) + ".staging-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("temporary entry tree artifact remains: %s", entry.Name())
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
