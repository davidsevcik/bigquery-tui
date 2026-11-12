package logging

import (
	"bytes"
	"encoding/json"
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

func TestNewCreatesFileBeforeAnyWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	logPath := filepath.Join(dir, FileName)
	_, closeFn, err := New(Options{Dir: dir, Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file to exist immediately after New: %v", err)
	}
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

// A log directory and file an older bqt left readable to others are tightened.
func TestNewTightensExistingModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, FileName)
	if err := os.WriteFile(logPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, closeFn, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	for path, want := range map[string]os.FileMode{dir: 0o700, logPath: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
	if b, _ := os.ReadFile(logPath); string(b) != "old\n" {
		t.Errorf("existing log changed: %q", b)
	}
}
