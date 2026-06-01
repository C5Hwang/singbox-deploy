package state

import (
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
