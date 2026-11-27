package logging

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxFileSize      = 5 << 20
	maxBackups       = 3
	backupTimeFormat = "2006-01-02T15-04-05.000"
)

type rotatingFile struct {
	path       string
	maxSize    int64
	maxBackups int
	now        func() time.Time

	mu   sync.Mutex
	file *os.File
	held os.FileInfo
}

func newRotatingFile(path string, maxSize int64, backups int) *rotatingFile {
	return &rotatingFile{path: path, maxSize: maxSize, maxBackups: backups, now: time.Now}
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	size, err := w.follow()
	if err == nil && w.full(size, len(p)) {
		err = w.rotate(len(p))
	}
	if w.file == nil {
		return 0, err
	}
	n, werr := w.file.Write(p)
	return n, errors.Join(werr, err)
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file, w.held = nil, nil
	return err
}

func (w *rotatingFile) full(size int64, next int) bool {
	return size > 0 && size+int64(next) > w.maxSize
}

func (w *rotatingFile) follow() (int64, error) {
	if w.file != nil {
		if fi, err := os.Stat(w.path); err == nil && os.SameFile(fi, w.held) {
			return fi.Size(), nil
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, err
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	w.file, w.held = f, fi
	return fi.Size(), nil
}

func (w *rotatingFile) rotate(next int) error {
	unlock, err := lockFile(w.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var pruneErr error
	if fi, err := os.Stat(w.path); err == nil && w.full(fi.Size(), next) {
		backups, err := w.backups()
		if err != nil {
			return err
		}
		at := w.backupTime(backups)
		if err := os.Rename(w.path, w.backupPath(at)); err != nil {
			return err
		}
		pruneErr = w.prune(append(backups, backup{w.backupPath(at), at}))
	}
	_, err = w.follow()
	return errors.Join(err, pruneErr)
}

type backup struct {
	path string
	at   time.Time
}

func (w *rotatingFile) backupNameParts() (prefix, ext string) {
	base := filepath.Base(w.path)
	ext = filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "-", ext
}

func (w *rotatingFile) backupPath(at time.Time) string {
	prefix, ext := w.backupNameParts()
	return filepath.Join(filepath.Dir(w.path), prefix+at.Format(backupTimeFormat)+ext)
}

func (w *rotatingFile) backups() ([]backup, error) {
	dir := filepath.Dir(w.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefix, ext := w.backupNameParts()
	var out []backup
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.Type().IsRegular() {
			continue
		}
		stamp, ok = strings.CutSuffix(stamp, ext)
		if !ok {
			continue
		}
		if at, err := time.Parse(backupTimeFormat, stamp); err == nil {
			out = append(out, backup{filepath.Join(dir, e.Name()), at})
		}
	}
	slices.SortFunc(out, func(a, b backup) int { return a.at.Compare(b.at) })
	return out, nil
}

func (w *rotatingFile) backupTime(backups []backup) time.Time {
	at := w.now().UTC().Truncate(time.Millisecond)
	if n := len(backups); n > 0 && !at.After(backups[n-1].at) {
		at = backups[n-1].at.Add(time.Millisecond)
	}
	return at
}

func (w *rotatingFile) prune(backups []backup) error {
	var errs []error
	for len(backups) > w.maxBackups {
		if err := os.Remove(backups[0].path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		backups = backups[1:]
	}
	return errors.Join(errs...)
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
