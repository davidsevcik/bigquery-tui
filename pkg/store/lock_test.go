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

func TestRewriteAndReadUseTheLockedFileEvenAfterItsPathIsReplaced(t *testing.T) {
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
	if got, err := l.Read(); err != nil || string(got) != "mine\n" {
		t.Fatalf("a read returns the held file, not the one at the path: %q %v", got, err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := l.Rewrite([]byte("late\n")); !errors.Is(err, ErrUnlocked) {
		t.Fatalf("a released lock refuses a rewrite: %v", err)
	}
	if _, err := l.Read(); !errors.Is(err, ErrUnlocked) {
		t.Fatalf("a released lock refuses a read: %v", err)
	}
}

func onceBetweenOpenAndFlock(t *testing.T, step func(path string)) {
	t.Helper()
	fired := false
	betweenOpenAndFlock = func(path string) {
		if fired {
			return
		}
		fired = true
		step(path)
	}
	t.Cleanup(func() { betweenOpenAndFlock = func(string) {} })
}

func wantTheOnlyHolderOf(t *testing.T, path string, l *Lock) {
	t.Helper()
	held, err := l.f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	named, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the path must name the held file: %v", err)
	}
	if !os.SameFile(held, named) {
		t.Fatal("the lock must hold the file its path names")
	}
	other, err := TryLock(path)
	if !errors.Is(err, ErrLocked) {
		other.Unlock()
		t.Fatalf("a second TryLock of a held path must get ErrLocked, got %v", err)
	}
}

func TestTryLockHoldsTheFileThePathNamesAfterTheFlock(t *testing.T) {
	for _, c := range []struct {
		name    string
		between func(t *testing.T, path string)
	}{
		{"path removed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"path replaced", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("another file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.lock")
			onceBetweenOpenAndFlock(t, func(path string) { c.between(t, path) })
			l, err := TryLock(path)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Unlock()
			wantTheOnlyHolderOf(t, path, l)
		})
	}
}

func TestAHolderThatRemovesThePathThenUnlocksLeavesOneHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	onceBetweenOpenAndFlock(t, func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := a.Unlock(); err != nil {
			t.Fatal(err)
		}
	})
	b, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unlock()
	wantTheOnlyHolderOf(t, path, b)
}

func TestTryLockGivesUpWhenThePathKeepsChanging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	attempts := 0
	betweenOpenAndFlock = func(path string) {
		attempts++
		os.Remove(path)
	}
	t.Cleanup(func() { betweenOpenAndFlock = func(string) {} })
	if _, err := TryLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	if attempts != lockAttempts {
		t.Fatalf("want %d attempts, got %d", lockAttempts, attempts)
	}
}

func TestTryLockReportsAPathItCannotCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "drafts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.lock")
	onceBetweenOpenAndFlock(t, func(string) {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if l, err := TryLock(path); err == nil || errors.Is(err, ErrLocked) {
		l.Unlock()
		t.Fatalf("a path that cannot be checked is an error, not a held lock: %v", err)
	}
}
