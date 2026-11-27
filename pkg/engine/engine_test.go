package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq"
	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/pkg/config"
	"github.com/davidsevcik/bigquery-tui/pkg/selection"
)

var ctx = context.Background()

type env struct {
	t    *testing.T
	dir  string
	fake *bqtest.Fake
	now  time.Time
	cfg  *config.Config
}

func newEnv(t *testing.T, selected ...string) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, fake: bqtest.New(), now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	e.cfg = e.config(shardDates)
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

const shardDates = "shard_patterns = ['.+(\\d{8})']\n"

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

	ro := e.open(true)
	st2, _ := ro.Status(ctx)
	if st2.Tables != 3 {
		t.Fatalf("%+v", st2)
	}

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
	e.cfg = e.config(shardDates + `ignore = ["proj-a.us_data"]`)
	rw := e.open(false)
	h, _ := rw.StartSync(ctx, api.SyncScope{})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}

	e.cfg = e.config(shardDates)
	ro := e.open(true)
	st, _ := ro.Status(ctx)
	if !st.SyncNeeded || st.SyncReason == "" {
		t.Fatalf("%+v", st)
	}
}

func TestSecondSyncIsRefusedWhileRunning(t *testing.T) {
	e := newEnv(t, "proj-a")
	reached, release := make(chan struct{}), make(chan struct{})
	var listed atomic.Int32
	e.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "ListDatasets" && listed.Add(1) == 1 {
			close(reached)
			<-release
		}
	}
	one := e.open(false)
	h, err := one.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	two := e.open(false)
	releaseOne := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOne)
	<-reached
	if _, err := two.StartSync(ctx, api.SyncScope{}); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("want ErrSyncRunning, got %v", err)
	}
	if st, _ := two.Status(ctx); !st.SyncRunning {
		t.Fatal("status must report the running sync")
	}
	releaseOne()
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	h2, err := two.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatalf("lock must be free after the first sync: %v", err)
	}
	if _, err := h2.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitWithoutReadingEvents(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	h, _ := eng.StartSync(ctx, api.SyncScope{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := h.Wait(); err != nil {
			t.Error(err)
		}
	}()
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

type closeGate struct {
	held, cancelled, release chan struct{}
	heldOnce, cancelledOnce  sync.Once
}

func newCloseGate() *closeGate {
	return &closeGate{held: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
}

func (g *closeGate) hold(c context.Context) {
	g.heldOnce.Do(func() { close(g.held) })
	<-c.Done()
	g.cancelledOnce.Do(func() { close(g.cancelled) })
	<-g.release
}

func (g *closeGate) closeWhileHeld(t *testing.T, eng *Engine) {
	t.Helper()
	<-g.held
	closed := make(chan error, 1)
	go func() { closed <- eng.Close() }()
	select {
	case <-g.cancelled:
	case err := <-closed:
		t.Fatalf("Close returned while a call was still held (err %v)", err)
	}
	close(g.release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

type heldRegionTables struct {
	*bqtest.Fake
	gate *closeGate
}

func (m heldRegionTables) RegionTables(c context.Context, req bq.RegionRequest) (*api.Snapshot, error) {
	m.gate.hold(c)
	return m.Fake.RegionTables(c, req)
}

func TestCloseCancelsARunningSyncAndWaitsForIt(t *testing.T) {
	e := newEnv(t, "proj-a")
	gate := newCloseGate()
	eng, err := Open(ctx, Options{
		Config:  e.cfg,
		Meta:    func(context.Context) (bq.Metadata, error) { return heldRegionTables{e.fake, gate}, nil },
		Now:     func() time.Time { return e.now },
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	gate.closeWhileHeld(t, eng)
	select {
	case <-h.done:
	default:
		t.Fatal("Close returned before the sync finished")
	}
	res, err := h.Wait()
	if err != nil || !res.Cancelled {
		t.Fatalf("%+v %v", res, err)
	}
	if eng.syncRunning() {
		t.Fatal("the sync lock outlived Close")
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

func TestReadOnlyAndNoProjects(t *testing.T) {
	e := newEnv(t, "proj-a")
	e.open(false)
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
