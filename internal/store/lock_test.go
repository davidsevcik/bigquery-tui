package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bqt.lock")
	a, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second holder must get ErrLocked, got %v", err)
	}
	if err := a.Unlock(); err != nil {
		t.Fatal(err)
	}
	b, err := TryLock(path)
	if err != nil {
		t.Fatalf("lock must be free after Unlock: %v", err)
	}
	b.Unlock()
	if err := (*Lock)(nil).Unlock(); err != nil {
		t.Fatal("nil Unlock is a no-op")
	}
}
