// Package state persists small, individually inspectable state files. Each
// piece of state is one file rather than one large JSON blob so an operator can
// read or edit values directly.
package state

import (
	"errors"
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
// back-to-back. If replacing the second file fails, it restores the first file
// so an ordinary filesystem error does not leave a mismatched pair. The two
// renames are not one atomic filesystem transaction, so a process or machine
// failure between them can still leave a mismatched pair. Used for TLS
// certificate/key pairs whose halves must match on disk.
func WriteFilePair(aPath string, aData []byte, aPerm os.FileMode, bPath string, bData []byte, bPerm os.FileMode) error {
	return writeFilePair(aPath, aData, aPerm, bPath, bData, bPerm, os.Rename)
}

type renameFileFunc func(oldPath, newPath string) error

func writeFilePair(aPath string, aData []byte, aPerm os.FileMode, bPath string, bData []byte, bPerm os.FileMode, renameFile renameFileFunc) error {
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

	// Keep a staged snapshot of the first file until the second rename commits.
	// It lets us atomically restore the old first half if that commit fails.
	var (
		aRollback      string
		aExisted       bool
		removeRollback bool
	)
	oldA, err := os.ReadFile(aPath)
	switch {
	case err == nil:
		info, statErr := os.Stat(aPath)
		if statErr != nil {
			return statErr
		}
		aRollback, err = stageFile(aPath, oldA, info.Mode().Perm())
		if err != nil {
			return err
		}
		removeRollback = true
		defer func() {
			if removeRollback {
				_ = os.Remove(aRollback)
			}
		}()
		aExisted = true
	case !os.IsNotExist(err):
		return err
	}

	if err := renameFile(aTmp, aPath); err != nil {
		return err
	}
	if err := renameFile(bTmp, bPath); err != nil {
		commitErr := fmt.Errorf("replace %s: %w", bPath, err)
		var rollbackErr error
		if aExisted {
			rollbackErr = renameFile(aRollback, aPath)
		} else if removeErr := os.Remove(aPath); removeErr != nil && !os.IsNotExist(removeErr) {
			rollbackErr = removeErr
		}
		if rollbackErr != nil {
			if aExisted {
				removeRollback = false
				rollbackErr = fmt.Errorf("%w; previous contents retained at %s", rollbackErr, aRollback)
			}
			return errors.Join(commitErr, fmt.Errorf("restore %s: %w", aPath, rollbackErr))
		}
		return commitErr
	}
	return nil
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
