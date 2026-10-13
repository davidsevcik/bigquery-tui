package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	defer closeFn()
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
	defer closeFn()
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
