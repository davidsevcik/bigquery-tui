package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func watchSetup(t *testing.T) (path string, cur *Config, events <-chan Event, kick chan struct{}) {
	t.Helper()
	return watchSetupStability(t, baseCfg, 30*time.Millisecond)
}

func watchSetupStability(t *testing.T, src string, stability time.Duration) (path string, cur *Config, events <-chan Event, kick chan struct{}) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }}
	cur, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	kick = make(chan struct{}, 1)
	ch := Watch(ctx, cur, WatchOptions{Load: opts, Interval: 10 * time.Millisecond, Stability: stability, Kick: kick})
	t.Cleanup(func() {
		cancel()
		for range ch {
		}
	})
	return path, cur, ch, kick
}

func next(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no config event")
		return Event{}
	}
}

func quiet(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected event: %+v", e)
	case <-time.After(120 * time.Millisecond):
	}
}

func save(t *testing.T, path, src string) {
	t.Helper()
	tmp := path + ".save"
	if err := os.WriteFile(tmp, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

const baseCfg = "sync_ttl = \"2h\"\nignore = [\"p-one\"]\n"

func TestWatchReportsChangedKeys(t *testing.T) {
	path, _, ev, _ := watchSetup(t)
	quiet(t, ev)
	save(t, path, "sync_ttl = \"1h\"\nignore = [\"p-one\", \"p-two\"]\nlocation = \"EU\"\n")
	e := next(t, ev)
	if e.Err != nil || e.Config.Location != "EU" {
		t.Fatalf("%+v", e)
	}
	if !reflect.DeepEqual(e.Changed, []string{"ignore", "location", "sync_ttl"}) || len(e.RestartRequired) != 0 {
		t.Fatalf("%v %v", e.Changed, e.RestartRequired)
	}
	quiet(t, ev)
}

func TestWatchInvalidThenFixed(t *testing.T) {
	path, _, ev, _ := watchSetup(t)
	save(t, path, "ingore = 1\n")
	e := next(t, ev)
	if e.Err == nil || e.Config != nil {
		t.Fatalf("invalid config must come as an error event: %+v", e)
	}
	save(t, path, baseCfg)
	e = next(t, ev)
	if e.Err != nil || len(e.Changed) != 0 {
		t.Fatalf("%+v", e)
	}
	save(t, path, baseCfg+"location = \"EU\"\n")
	e = next(t, ev)
	if !reflect.DeepEqual(e.Changed, []string{"location"}) {
		t.Fatalf("%v", e.Changed)
	}
}

func TestWatchRestartKeysAndRenameSave(t *testing.T) {
	path, _, ev, _ := watchSetup(t)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(baseCfg+"data_dir = \"/elsewhere\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	e := next(t, ev)
	if !reflect.DeepEqual(e.RestartRequired, []string{"data_dir"}) {
		t.Fatalf("%+v", e)
	}
}

func TestWatchWaitsForStability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(baseCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }}
	cur, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	const changing = 30
	var reads atomic.Int32
	readFile = func(p string) ([]byte, error) {
		n := reads.Add(1)
		switch {
		case n == 1:
			return os.ReadFile(p)
		case n <= changing:
			return []byte(baseCfg + "location = \"L" + strconv.Itoa(int(n)) + "\"\n"), nil
		default:
			return []byte(baseCfg + "location = \"EU\"\n"), nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ev := Watch(ctx, cur, WatchOptions{Load: opts, Interval: 10 * time.Millisecond, Stability: 50 * time.Millisecond})
	t.Cleanup(func() {
		cancel()
		for range ev {
		}
		readFile = os.ReadFile
	})
	e := next(t, ev)
	if e.Err != nil || e.Config == nil || e.Config.Location != "EU" {
		t.Fatalf("applied a config that was still changing: %+v", e)
	}
	if n := reads.Load(); n <= changing {
		t.Fatalf("applied after %d reads, before the content settled", n)
	}
}

func TestWatchDeletedFileIsAnError(t *testing.T) {
	path, _, ev, _ := watchSetup(t)
	os.Remove(path)
	if e := next(t, ev); e.Err == nil {
		t.Fatalf("%+v", e)
	}
}

func TestDiffIgnoresCommentsAndOrder(t *testing.T) {
	a := mustParse(t, "location = \"EU\"\nignore = [\"b-proj\"]\n", LoadOptions{})
	b := mustParse(t, "# hi\nignore = [\"b-proj\"]\nlocation = \"EU\"\n", LoadOptions{})
	if d := Diff(a, b); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}

func TestWatchReadsOncePerCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(baseCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }}
	cur, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	readFile = func(p string) ([]byte, error) {
		if n := reads.Add(1); n == 2 || n == 3 {
			return nil, os.ErrNotExist
		}
		return os.ReadFile(p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	kick := make(chan struct{}, 1)
	ev := Watch(ctx, cur, WatchOptions{Load: opts, Interval: time.Hour, Stability: time.Nanosecond, Kick: kick})
	kick <- struct{}{}
	e := next(t, ev)
	cancel()
	for range ev {
	}
	readFile = os.ReadFile
	var nc *ErrNoConfig
	if !errors.As(e.Err, &nc) || e.Config != nil {
		t.Fatalf("the event must describe the missing file the check saw: %+v", e)
	}
}
