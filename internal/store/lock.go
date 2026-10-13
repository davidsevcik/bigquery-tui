package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("already running")

// Lock is an advisory, process-wide lock backed by flock(2). It is released
// when Unlock is called or the process exits.
type Lock struct {
	f *os.File
}

// TryLock takes the lock at path without blocking. It returns ErrLocked when
// another holder has it.
func TryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lockfile: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
