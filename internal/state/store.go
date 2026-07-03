// Package state persists small, individually inspectable state files. Each
// piece of state is one file rather than one large JSON blob so an operator can
// read or edit values directly.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store reads and writes named state files under a single directory.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. The directory is created lazily on
// the first write.
func NewStore(dir string) Store {
	return Store{dir: dir}
}

// WriteString writes value to the named state file with the given permissions.
// The state directory is created if needed and its mode tightened to 0o700 even
// when it already exists, so permission fixes reach existing installs.
func (s Store) WriteString(name string, value string, perm os.FileMode) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, filepath.Clean(name))
	return WriteFileAtomic(path, []byte(value), perm)
}

// WriteFileAtomic writes data to path via a temp file + rename so readers (and
// crashes mid-write) never observe a truncated file. State files hold the only
// copy of keys and passwords, so a torn write is unrecoverable.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := stageFile(path, data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// WriteFilePair stages both files as temp siblings before renaming them
// back-to-back: a failure before the first rename leaves the existing pair
// untouched, and the mismatch window between the renames is minimal. Used for
// TLS certificate/key pairs whose halves must match on disk.
func WriteFilePair(aPath string, aData []byte, aPerm os.FileMode, bPath string, bData []byte, bPerm os.FileMode) error {
	aTmp, err := stageFile(aPath, aData, aPerm)
	if err != nil {
		return err
	}
	defer os.Remove(aTmp)
	bTmp, err := stageFile(bPath, bData, bPerm)
	if err != nil {
		return err
	}
	defer os.Remove(bTmp)
	if err := os.Rename(aTmp, aPath); err != nil {
		return err
	}
	return os.Rename(bTmp, bPath)
}

// stageFile writes data to a temp sibling of path, fully synced, ready to be
// renamed into place. The parent directory is created if missing.
func stageFile(path string, data []byte, perm os.FileMode) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// ReadString returns the contents of the named state file.
func (s Store) ReadString(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.Clean(name)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadValue returns the trimmed contents of the named state file. A missing
// file is an error only when the value is required; otherwise it reads as "".
func (s Store) ReadValue(name string, required bool) (string, error) {
	value, err := s.ReadString(name)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read state %s: %w", name, err)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("state %s is empty", name)
	}
	return value, nil
}
