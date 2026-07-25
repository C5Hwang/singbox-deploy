package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

var entryDirLocks sync.Map

func entryDirLock(dir string) *sync.RWMutex {
	key, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		key = filepath.Clean(dir)
	}
	lock, _ := entryDirLocks.LoadOrStore(key, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

// LoadEntryDirs reads the numbered entry directories under dir in name order and
// decodes each into a value. A missing dir reads as an empty slice. Each entry
// directory holds one small state file per field; decode receives the entry
// directory path and reads whatever fields it needs.
func LoadEntryDirs[T any](dir string, decode func(root string) T) ([]T, error) {
	lock := entryDirLock(dir)
	lock.RLock()
	defer lock.RUnlock()
	fileLock, err := lockEntryTree(dir, false)
	if err != nil {
		return nil, err
	}
	if fileLock != nil {
		defer unlockEntryTree(fileLock)
	}
	return loadEntryDirsUnlocked(dir, decode)
}

func loadEntryDirsUnlocked[T any](dir string, decode func(root string) T) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []T
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		out = append(out, decode(filepath.Join(dir, entry.Name())))
	}
	return out, nil
}

// SaveEntryDirs replaces dir with one numbered directory of state files per
// item, as produced by encode. The complete replacement tree is staged beside
// dir before the old tree is moved aside and the new tree is renamed into
// place. A build or commit failure therefore leaves the previous tree intact.
func SaveEntryDirs[T any](dir string, items []T, encode func(T) map[string]string) error {
	return saveEntryDirs(dir, items, encode, os.Rename)
}

func saveEntryDirs[T any](dir string, items []T, encode func(T) map[string]string, rename func(string, string) error) error {
	lock := entryDirLock(dir)
	lock.Lock()
	defer lock.Unlock()

	fileLock, err := lockEntryTreeForWrite(dir)
	if err != nil {
		return err
	}
	defer unlockEntryTree(fileLock)
	return saveEntryDirsUnlocked(dir, items, encode, rename)
}

// TransactEntryDirs performs a read-modify-write transaction on one entry
// tree. The process-local exclusive lock and the sibling advisory flock are
// both held from the initial load until the atomically staged replacement has
// committed. mutate must derive its result from the supplied current value;
// an error aborts the transaction without changing the existing tree.
//
// This is the appropriate API for registries that are updated by multiple
// processes. A separate LoadEntryDirs followed by SaveEntryDirs protects each
// individual operation, but cannot prevent a stale load from overwriting an
// intervening update.
func TransactEntryDirs[T any](
	dir string,
	decode func(root string) T,
	encode func(T) map[string]string,
	mutate func(current []T) ([]T, error),
) ([]T, error) {
	lock := entryDirLock(dir)
	lock.Lock()
	defer lock.Unlock()

	fileLock, err := lockEntryTreeForWrite(dir)
	if err != nil {
		return nil, err
	}
	defer unlockEntryTree(fileLock)

	current, err := loadEntryDirsUnlocked(dir, decode)
	if err != nil {
		return nil, err
	}
	next, err := mutate(current)
	if err != nil {
		return nil, err
	}
	if err := saveEntryDirsUnlocked(dir, next, encode, os.Rename); err != nil {
		return nil, err
	}
	return next, nil
}

// ErrEntryNotFound reports that no entry in the tree satisfied the selector.
var ErrEntryNotFound = errors.New("entry not found")

// UpdateEntryFields rewrites only the changed field files of the single entry
// selected by match, in place, without restaging the tree. It takes the same
// exclusive locks as TransactEntryDirs, so it cannot interleave with a full
// replacement.
//
// This trades the whole-tree atomicity of TransactEntryDirs for a bounded
// amount of disk work, and is therefore appropriate only for independent
// observational fields where a crash between two field writes leaves no
// inconsistent state. Anything the entry's other fields depend on must go
// through TransactEntryDirs instead.
func UpdateEntryFields[T any](
	dir string,
	decode func(root string) T,
	encode func(T) map[string]string,
	match func(T) bool,
	mutate func(*T) error,
) (T, error) {
	var zero T
	lock := entryDirLock(dir)
	lock.Lock()
	defer lock.Unlock()

	fileLock, err := lockEntryTreeForWrite(dir)
	if err != nil {
		return zero, err
	}
	defer unlockEntryTree(fileLock)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, ErrEntryNotFound
		}
		return zero, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		current := decode(root)
		if !match(current) {
			continue
		}
		next := current
		if err := mutate(&next); err != nil {
			return zero, err
		}
		before, after := encode(current), encode(next)
		for name, value := range after {
			if !validEntryFieldName(name) {
				return zero, fmt.Errorf("update entry %s: invalid field name %q", entry.Name(), name)
			}
			if previous, ok := before[name]; ok && previous == value {
				continue
			}
			if err := WriteFileAtomic(filepath.Join(root, name), []byte(value+"\n"), 0o600); err != nil {
				return zero, fmt.Errorf("update entry %s field %q: %w", entry.Name(), name, err)
			}
		}
		return next, nil
	}
	return zero, ErrEntryNotFound
}

func lockEntryTreeForWrite(dir string) (*os.File, error) {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, fmt.Errorf("create entry tree parent: %w", err)
	}
	return lockEntryTree(dir, true)
}

func saveEntryDirsUnlocked[T any](dir string, items []T, encode func(T) map[string]string, rename func(string, string) error) error {
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)

	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".staging-")
	if err != nil {
		return fmt.Errorf("create staged entry tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	for i, item := range items {
		entryDir := filepath.Join(staging, fmt.Sprintf("%03d", i+1))
		if err := os.MkdirAll(entryDir, 0o700); err != nil {
			return fmt.Errorf("create staged entry %03d: %w", i+1, err)
		}
		for name, value := range encode(item) {
			if !validEntryFieldName(name) {
				return fmt.Errorf("write staged entry %03d: invalid field name %q", i+1, name)
			}
			if err := WriteFileAtomic(filepath.Join(entryDir, name), []byte(value+"\n"), 0o600); err != nil {
				return fmt.Errorf("write staged entry %03d field %q: %w", i+1, name, err)
			}
		}
	}

	backup := staging + ".backup"
	cleanupBackup := true
	defer func() {
		if cleanupBackup {
			_ = os.RemoveAll(backup)
		}
	}()
	hadPrevious := false
	if _, err := os.Lstat(dir); err == nil {
		hadPrevious = true
		if err := rename(dir, backup); err != nil {
			return fmt.Errorf("move previous entry tree aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect previous entry tree: %w", err)
	}

	if err := rename(staging, dir); err != nil {
		commitErr := fmt.Errorf("commit staged entry tree: %w", err)
		if !hadPrevious {
			return commitErr
		}
		if restoreErr := rename(backup, dir); restoreErr != nil {
			// Keep the backup when restoration itself fails: it is now the only
			// remaining copy of the previous tree and must not be cleaned up.
			cleanupBackup = false
			return errors.Join(commitErr, fmt.Errorf("restore previous entry tree from %s: %w", backup, restoreErr))
		}
		return commitErr
	}
	if hadPrevious {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove previous entry tree backup: %w", err)
		}
	}
	return nil
}

// lockEntryTree complements the process-local RWMutex with an advisory flock:
// the TUI, monitor daemon, and certificate timer are separate processes and
// may all update the node/certificate registries. The sibling lock survives
// atomic directory renames and therefore guards the complete transaction.
func lockEntryTree(dir string, exclusive bool) (*os.File, error) {
	parent := filepath.Dir(filepath.Clean(dir))
	if !exclusive {
		if _, err := os.Stat(parent); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
	}
	f, err := os.OpenFile(filepath.Clean(dir)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open entry tree lock: %w", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock entry tree: %w", err)
	}
	return f, nil
}

func unlockEntryTree(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func validEntryFieldName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && filepath.Clean(name) == name
}

// ReadEntryValue returns the trimmed contents of an entry field file, or
// fallback when the file is missing or empty.
func ReadEntryValue(root, name, fallback string) string {
	if !validEntryFieldName(name) {
		return fallback
	}
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return fallback
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return fallback
	}
	return value
}
