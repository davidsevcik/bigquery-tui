package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileLoggerWritesJSONAtLevel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l, closeFn, err := New(Options{Dir: dir, Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("hidden")
	l.Info("hello", "k", "v")
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("debug must be filtered: %q", b)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil || rec["msg"] != "hello" || rec["k"] != "v" {
		t.Fatalf("%v %v", rec, err)
	}
}

func TestStderrOnlyWarnAndAbove(t *testing.T) {
	var stderr bytes.Buffer
	dir := t.TempDir()
	l, closeFn, err := New(Options{Dir: dir, Level: "debug", Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
	l.Info("quiet")
	l.Warn("loud")
	if strings.Contains(stderr.String(), "quiet") || !strings.Contains(stderr.String(), "loud") {
		t.Fatalf("stderr: %q", stderr.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "quiet") || !strings.Contains(string(b), "loud") {
		t.Fatalf("file gets everything at debug: %q", b)
	}
}

func TestNoSinksIsSilent(t *testing.T) {
	l, closeFn, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
	l.Error("nobody hears this")
}

func TestParseLevel(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "debug": true, "INFO": true, "warn": true, "error": true, "loud": false} {
		_, err := ParseLevel(in)
		if (err == nil) != ok {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, _, err := New(Options{Level: "loud"}); err == nil {
		t.Fatal("bad level must fail")
	}
}

// The log file exists as soon as the logger does, even if nothing reaches
// its level (#92: a sync that went well left no bqt.log at all).
func TestFileExistsBeforeAnyRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	_, closeFn, err := New(Options{Dir: dir, Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("no log file before the first record: %v", err)
	}
}

// With a command, the first line is the start record, at any level, and it
// stays out of stderr.
func TestStartRecord(t *testing.T) {
	var stderr bytes.Buffer
	dir := filepath.Join(t.TempDir(), "logs")
	_, closeFn, err := New(Options{Dir: dir, Level: "error", Stderr: &stderr, Command: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.SplitN(b, []byte("\n"), 2)[0], &rec); err != nil {
		t.Fatalf("%q: %v", b, err)
	}
	if rec["msg"] != "bqt started" || rec["command"] != "sync" || rec["level"] != "INFO" || rec["log_level"] != "ERROR" || rec["version"] == "" || rec["log_dir"] != dir {
		t.Fatalf("start record: %v", rec)
	}
	if stderr.Len() != 0 {
		t.Fatalf("the start record reached stderr: %q", stderr.String())
	}
}

// A log file that cannot be written does not stop the command (#92 review:
// read-only commands on a read-only data_dir must still work).
func TestUnwritableLogFileIsNotFatal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	l, closeFn, err := New(Options{Dir: dir, Level: "info", Command: "tui"})
	if err != nil {
		t.Fatalf("an unwritable log file stopped the command: %v", err)
	}
	l.Info("lost")
	_ = closeFn()
}

// A log directory that cannot be created does not stop the command either.
func TestUncreatableLogDirIsNotFatal(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	var stderr bytes.Buffer
	l, closeFn, err := New(Options{Dir: filepath.Join(parent, "logs"), Level: "info", Stderr: &stderr, Command: "sync"})
	if err != nil {
		t.Fatalf("an uncreatable log dir stopped the command: %v", err)
	}
	defer func() { _ = closeFn() }()
	l.Warn("still on stderr")
	if !strings.Contains(stderr.String(), "still on stderr") {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// A handler that fails does not keep the record from the others.
func TestFanoutReachesEveryHandler(t *testing.T) {
	var stderr bytes.Buffer
	failing := slog.NewJSONHandler(failWriter{}, nil)
	l := slog.New(fanout{failing, slog.NewTextHandler(&stderr, nil)})
	l.Warn("both")
	if !strings.Contains(stderr.String(), "both") {
		t.Fatalf("the second handler missed the record: %q", stderr.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrPermission }

// A log directory and file an older bqt left readable to others are
// tightened, and the old records stay.
func TestNewTightensExistingModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, closeFn, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", p, got, want)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "old\n" {
		t.Errorf("existing log changed: %q", b)
	}
}
