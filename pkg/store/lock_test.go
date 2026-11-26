package store

import (
	"errors"
	"os"
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
	if err := b.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := (*Lock)(nil).Unlock(); err != nil {
		t.Fatal("nil Unlock is a no-op")
	}
}

func TestRewriteWritesTheLockedFileEvenAfterItsPathIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Rewrite([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := l.Rewrite([]byte("b\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "b\n" {
		t.Fatalf("a rewrite replaces the whole content: %q %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("someone else\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Rewrite([]byte("mine\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "someone else\n" {
		t.Fatalf("a rewrite never touches a file it does not hold: %q %v", got, err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := l.Rewrite([]byte("late\n")); !errors.Is(err, ErrUnlocked) {
		t.Fatalf("a released lock refuses a rewrite: %v", err)
	}
}
