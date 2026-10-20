package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func watchSetup(t *testing.T, src string) (path string, cur *Config, events <-chan Event, kick chan struct{}) {
	t.Helper()
	return watchSetupStability(t, src, 30*time.Millisecond)
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
	t.Cleanup(cancel)
	kick = make(chan struct{}, 1)
	events = Watch(ctx, cur, WatchOptions{Load: opts, Interval: 10 * time.Millisecond, Stability: stability, Kick: kick})
	return
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

const baseCfg = "execution_project = \"exec-a\"\nprojects = [\"p-one\"]\n"

func TestWatchReportsChangedKeys(t *testing.T) {
	path, _, ev, _ := watchSetup(t, baseCfg)
	quiet(t, ev)
	os.WriteFile(path, []byte("execution_project = \"exec-b\"\nprojects = [\"p-one\", \"p-two\"]\nsync_ttl = \"1h\"\n"), 0o600)
	e := next(t, ev)
	if e.Err != nil || e.Config.ExecutionProject != "exec-b" {
		t.Fatalf("%+v", e)
	}
	if !reflect.DeepEqual(e.Changed, []string{"execution_project", "projects", "sync_ttl"}) || len(e.RestartRequired) != 0 {
		t.Fatalf("%v %v", e.Changed, e.RestartRequired)
	}
	quiet(t, ev)
}

func TestWatchInvalidThenFixed(t *testing.T) {
	path, _, ev, _ := watchSetup(t, baseCfg)
	os.WriteFile(path, []byte("ingore = 1\n"), 0o600)
	e := next(t, ev)
	if e.Err == nil || e.Config != nil {
		t.Fatalf("invalid config must come as an error event: %+v", e)
	}
	// restoring the original content clears the error with no changed keys
	os.WriteFile(path, []byte(baseCfg), 0o600)
	e = next(t, ev)
	if e.Err != nil || len(e.Changed) != 0 {
		t.Fatalf("%+v", e)
	}
	// a later change is diffed against the last good config
	os.WriteFile(path, []byte(baseCfg+"location = \"EU\"\n"), 0o600)
	e = next(t, ev)
	if !reflect.DeepEqual(e.Changed, []string{"location"}) {
		t.Fatalf("%v", e.Changed)
	}
}

func TestWatchRestartKeysAndRenameSave(t *testing.T) {
	path, _, ev, _ := watchSetup(t, baseCfg)
	// editors often save by writing a temp file and renaming it over the target
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte(baseCfg+"data_dir = \"/elsewhere\"\n"), 0o600)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	e := next(t, ev)
	if !reflect.DeepEqual(e.RestartRequired, []string{"data_dir"}) {
		t.Fatalf("%+v", e)
	}
}

func TestWatchWaitsForStability(t *testing.T) {
	// a wide window so a stalled CI runner does not let a write look stable
	path, _, ev, _ := watchSetupStability(t, baseCfg, 250*time.Millisecond)
	// keep rewriting faster than the stability window: nothing may be applied
	deadline := time.Now().Add(400 * time.Millisecond)
	i := 0
	for time.Now().Before(deadline) {
		i++
		os.WriteFile(path, []byte(baseCfg+"# edit "+string(rune('a'+i%26))+"\nlocation = \"L"+string(rune('a'+i%26))+"\"\n"), 0o600)
		select {
		case e := <-ev:
			t.Fatalf("applied a half-written config: %+v", e)
		case <-time.After(8 * time.Millisecond):
		}
	}
	e := next(t, ev)
	if e.Err != nil || e.Config.Location == "" {
		t.Fatalf("%+v", e)
	}
}

func TestWatchDeletedFileIsAnError(t *testing.T) {
	path, _, ev, _ := watchSetup(t, baseCfg)
	os.Remove(path)
	if e := next(t, ev); e.Err == nil {
		t.Fatalf("%+v", e)
	}
}

func TestDiffIgnoresCommentsAndOrder(t *testing.T) {
	a := mustParse(t, "execution_project = \"ok-project\"\nprojects = [\"b-proj\", \"a-proj\"]\n", LoadOptions{})
	b := mustParse(t, "# hi\nexecution_project = \"ok-project\"\nprojects = [\"a-proj\", \"b-proj\"]\n", LoadOptions{})
	if d := Diff(a, b); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}
