package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

var ErrLocked = errors.New("already running")

var ErrUnlocked = errors.New("the lock is released")

type Lock struct {
	f *os.File
}

var betweenOpenAndFlock = func(string) {}

const lockAttempts = 10

var errPathChanged = errors.New("the lock file's path names another file")

func TryLock(path string) (*Lock, error) {
	for range lockAttempts {
		l, err := lockOnce(path)
		if !errors.Is(err, errPathChanged) {
			return l, err
		}
	}
	return nil, ErrLocked
}

func lockOnce(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lockfile: %w", err)
	}
	betweenOpenAndFlock(path)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	l := &Lock{f: f}
	if err := l.checkNamedBy(path); err != nil {
		l.Unlock()
		return nil, err
	}
	return l, nil
}

func (l *Lock) checkNamedBy(path string) error {
	held, err := l.f.Stat()
	if err != nil {
		return fmt.Errorf("stat lockfile: %w", err)
	}
	named, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errPathChanged
	}
	if err != nil {
		return fmt.Errorf("stat lockfile: %w", err)
	}
	if !os.SameFile(held, named) {
		return errPathChanged
	}
	return nil
}

func (l *Lock) Read() ([]byte, error) {
	if l == nil || l.f == nil {
		return nil, ErrUnlocked
	}
	info, err := l.f.Stat()
	if err != nil {
		return nil, err
	}
	content := make([]byte, info.Size())
	n, err := l.f.ReadAt(content, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return content[:n], nil
}

func (l *Lock) Rewrite(content []byte) error {
	if l == nil || l.f == nil {
		return ErrUnlocked
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt(content, 0)
	return err
}

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

func lockBlocking(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lockfile: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
