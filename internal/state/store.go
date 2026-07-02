// Package state persists small, individually inspectable state files. Each
// piece of state is one file rather than one large JSON blob so an operator can
// read or edit values directly.
package state

import (
	"os"
	"path/filepath"
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

// WriteString writes value to the named state file with the given permissions,
// creating the state directory if needed.
func (s Store) WriteString(name string, value string, perm os.FileMode) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, filepath.Clean(name))
	return WriteFileAtomic(path, []byte(value), perm)
}

// WriteFileAtomic writes data to path via a temp file + rename so readers (and
// crashes mid-write) never observe a truncated file. State files hold the only
// copy of keys and passwords, so a torn write is unrecoverable.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadString returns the contents of the named state file.
func (s Store) ReadString(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.Clean(name)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
