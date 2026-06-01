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
	return os.WriteFile(path, []byte(value), perm)
}

// ReadString returns the contents of the named state file.
func (s Store) ReadString(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.Clean(name)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
