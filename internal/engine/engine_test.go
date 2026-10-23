package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/selection"
)

var ctx = context.Background()

type env struct {
	t    *testing.T
	dir  string
	fake *bqtest.Fake
	now  time.Time
	cfg  *config.Config
}

// newEnv builds an engine environment over a fake with proj-a and proj-b.
// The given projects start selected, with exec-proj as the chosen Execution
// project; with none, nothing is selected or chosen.
func newEnv(t *testing.T, selected ...string) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, fake: bqtest.New(), now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	e.cfg = e.config("")
	if len(selected) > 0 {
		st := selection.State{Execution: "exec-proj"}
		st.Select(e.now.Add(-time.Hour), selected...)
		if err := selection.Save(selectionPath(dir), st); err != nil {
			t.Fatal(err)
		}
	}
	a := e.fake.AddProject("proj-a")
	a.Datasets = []bqtest.Dataset{
		bqtest.DS("sales", "EU", bqtest.Table("orders", "id", "amount")),
		bqtest.DS("us_data", "US", bqtest.Table("u1", "x")),
	}
	e.fake.AddProject("proj-b").Datasets = []bqtest.Dataset{bqtest.DS("core", "EU", bqtest.Table("t", "c"))}
	return e
}

// config parses a config for this environment's data_dir plus extra lines.
func (e *env) config(extra string) *config.Config {
	e.t.Helper()
	cfg, err := config.Parse([]byte("data_dir = \""+e.dir+"\"\n"+extra), "t.toml", config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func (e *env) open(readOnly bool) *Engine {
	e.t.Helper()
	eng, err := Open(ctx, Options{
		Config: e.cfg, ReadOnly: readOnly,
		Meta:    func(context.Context) (bq.Metadata, error) { return e.fake, nil },
		Now:     func() time.Time { return e.now },
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { eng.Close() })
	return eng
}

func TestStatusBeforeAnyCatalog(t *testing.T) {
	e := newEnv(t, "proj-a")
	eng := e.open(true)
	st, err := eng.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.SyncNeeded || st.SyncRunning || len(st.Projects) != 1 || !st.Projects[0].Stale || st.Projects[0].SyncedAt != nil {
		t.Fatalf("%+v", st)
	}
}

func TestSyncThenStatusAndStaleness(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	rw := e.open(false)
	h, err := rw.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []api.SyncEventKind
	for ev := range h.Events() {
		kinds = append(kinds, ev.Kind)
	}
	res, err := h.Wait()
	if err != nil || res.UnitsOK != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if kinds[0] != api.EventRunStarted || kinds[len(kinds)-1] != api.EventRunFinished {
		t.Fatalf("%v", kinds)
	}

	st, _ := rw.Status(ctx)
	if st.SyncNeeded || st.Tables != 3 || st.Datasets != 3 || st.Columns != 4 {
		t.Fatalf("%+v", st)
	}
	if len(st.Projects) != 2 || st.Projects[0].Project != "proj-a" || len(st.Projects[0].Units) != 2 || st.Projects[0].Stale || st.Projects[0].SyncedAt == nil {
		t.Fatalf("%+v", st.Projects)
	}

	// a read-only engine over the same files sees the same
	ro := e.open(true)
	st2, _ := ro.Status(ctx)
	if st2.Tables != 3 {
		t.Fatalf("%+v", st2)
	}

	// 25 hours later everything is stale
	e.now = e.now.Add(25 * time.Hour)
	st3, _ := ro.Status(ctx)
	for _, p := range st3.Projects {
		if !p.Stale {
			t.Fatalf("project %s should be stale", p.Project)
		}
		for _, u := range p.Units {
			if !u.Stale {
				t.Fatalf("unit %+v should be stale", u)
			}
		}
	}
}

func TestStatusReportsConfigChange(t *testing.T) {
	e := newEnv(t, "proj-a")
	e.cfg = e.config(`ignore = ["proj-a.us_data"]`)
	rw := e.open(false)
	h, _ := rw.StartSync(ctx, api.SyncScope{})
	h.Wait()

	// the user removes an ignore pattern
	e.cfg = e.config("")
	ro := e.open(true)
	st, _ := ro.Status(ctx)
	if !st.SyncNeeded || st.SyncReason == "" {
		t.Fatalf("%+v", st)
	}
}

func TestSecondSyncIsRefusedWhileRunning(t *testing.T) {
	e := newEnv(t, "proj-a")
	e.fake.Delay = 150 * time.Millisecond
	one := e.open(false)
	h, err := one.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	// a different process (second engine on the same data dir)
	two := e.open(false)
	if _, err := two.StartSync(ctx, api.SyncScope{}); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("want ErrSyncRunning, got %v", err)
	}
	if st, _ := two.Status(ctx); !st.SyncRunning {
		t.Fatal("status must report the running sync")
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	h2, err := two.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatalf("lock must be free after the first sync: %v", err)
	}
	h2.Wait()
}

func TestWaitWithoutReadingEvents(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	h, _ := eng.StartSync(ctx, api.SyncScope{})
	done := make(chan struct{})
	go func() { h.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait must not depend on a consumer draining Events")
	}
}

func TestCancelThroughHandle(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	e.fake.Delay = 2 * time.Second
	eng := e.open(false)
	h, _ := eng.StartSync(ctx, api.SyncScope{})
	time.AfterFunc(50*time.Millisecond, h.Cancel)
	start := time.Now()
	res, err := h.Wait()
	if err != nil || !res.Cancelled {
		t.Fatalf("%+v %v", res, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel must interrupt in-flight calls")
	}
}

func TestReadOnlyAndNoProjects(t *testing.T) {
	e := newEnv(t, "proj-a")
	e.open(false) // creates the catalog
	if _, err := e.open(true).StartSync(ctx, api.SyncScope{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("%v", err)
	}
	e2 := newEnv(t)
	if _, err := e2.open(false).StartSync(ctx, api.SyncScope{}); err == nil {
		t.Fatal("no projects must be an error")
	}
	st, _ := e2.open(true).Status(ctx)
	if !st.SyncNeeded {
		t.Fatalf("%+v", st)
	}
}
