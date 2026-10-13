package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

var ctx = context.Background()

func cfgFrom(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("execution_project = \"exec-proj\"\n"+extra), "t.toml", config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type harness struct {
	t      *testing.T
	fake   *bqtest.Fake
	cat    *store.Catalog
	cfg    *config.Config
	now    time.Time
	events []api.SyncEvent
}

func newHarness(t *testing.T, cfgExtra string) *harness {
	t.Helper()
	cat, err := store.OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	return &harness{t: t, fake: bqtest.New(), cat: cat, cfg: cfgFrom(t, cfgExtra), now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
}

func (h *harness) syncer() *Syncer {
	return New(Options{
		Config: h.cfg, Meta: h.fake, Catalog: h.cat,
		Now:     func() time.Time { return h.now },
		Backoff: func(int) time.Duration { return 0 },
	})
}

func (h *harness) run(scope api.SyncScope) api.SyncResult {
	h.t.Helper()
	h.events = nil
	var mu gosync.Mutex
	res, err := h.syncer().Run(ctx, scope, func(e api.SyncEvent) {
		mu.Lock()
		h.events = append(h.events, e)
		mu.Unlock()
	})
	if err != nil {
		h.t.Fatalf("Run: %v", err)
	}
	return res
}

func (h *harness) unit(project, loc string) api.UnitState {
	h.t.Helper()
	us, err := h.cat.Units(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, u := range us {
		if u.Project == project && u.Location == loc {
			return u
		}
	}
	h.t.Fatalf("no unit state for %s/%s in %+v", project, loc, us)
	return api.UnitState{}
}

func (h *harness) counts() store.Counts {
	n, err := h.cat.Counts(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func twoProjectFake(f *bqtest.Fake) {
	a := f.AddProject("proj-a")
	a.Datasets = []bqtest.Dataset{
		bqtest.DS("sales", "EU", bqtest.Table("orders", "id", "amount"), bqtest.Table("items", "sku")),
		bqtest.DS("logs", "EU", bqtest.Table("l1", "msg")),
		bqtest.DS("us_data", "US", bqtest.Table("u1", "x")),
	}
	b := f.AddProject("proj-b")
	b.Datasets = []bqtest.Dataset{bqtest.DS("core", "EU", bqtest.Table("t", "c"))}
}

func TestSyncBasicGroupsByProjectAndRegion(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	res := h.run(api.SyncScope{})

	if res.UnitsTotal != 3 || res.UnitsOK != 3 || res.UnitsFailed != 0 || res.Cancelled {
		t.Fatalf("%+v", res)
	}
	calls := h.fake.CallsTo("RegionSnapshot")
	if len(calls) != 3 {
		t.Fatalf("one region query per (project, location), got %d: %+v", len(calls), calls)
	}
	n := h.counts()
	if n.Projects != 2 || n.Datasets != 4 || n.Tables != 5 || n.Columns != 6 {
		t.Fatalf("%+v", n)
	}
	eu := h.unit("proj-a", "EU")
	if eu.Status != api.UnitOK || eu.Datasets != 2 || eu.Tables != 3 || eu.LastSuccess == nil {
		t.Fatalf("%+v", eu)
	}
	ds, _ := h.cat.Datasets(ctx, "proj-a")
	if ds[0].Description != "dataset logs" {
		t.Fatalf("description from the snapshot must overlay datasets.list: %+v", ds[0])
	}
}

func TestSyncEvents(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	ev := h.events
	if ev[0].Kind != api.EventRunStarted || ev[len(ev)-1].Kind != api.EventRunFinished {
		t.Fatalf("first/last: %v ... %v", ev[0].Kind, ev[len(ev)-1].Kind)
	}
	var started, done, discovered int
	lastDone := 0
	for _, e := range ev {
		switch e.Kind {
		case api.EventDiscovered:
			discovered++
			if e.UnitsTotal != 3 {
				t.Fatalf("discovered total %d", e.UnitsTotal)
			}
		case api.EventUnitStarted:
			started++
		case api.EventUnitDone:
			done++
			if e.UnitsDone != lastDone+1 || e.UnitsTotal != 3 {
				t.Fatalf("progress counters must advance one by one: %+v (last %d)", e, lastDone)
			}
			lastDone = e.UnitsDone
		}
	}
	if discovered != 1 || started != 3 || done != 3 {
		t.Fatalf("discovered %d started %d done %d", discovered, started, done)
	}
}

func TestSyncIgnoreRules(t *testing.T) {
	h := newHarness(t, `
projects = ["proj-a"]
ignore = ["proj-a.logs", "proj-a.sales.items"]
`)
	twoProjectFake(h.fake)
	h.fake.Projects["proj-a"].Leak = []bqtest.Dataset{bqtest.DS("ghost", "EU", bqtest.Table("g", "c"))}
	h.run(api.SyncScope{})

	for _, c := range h.fake.CallsTo("RegionSnapshot") {
		if c.Location == "EU" {
			if len(c.Datasets) != 1 || c.Datasets[0] != "sales" {
				t.Fatalf("ignored datasets must not be requested: %+v", c)
			}
		}
	}
	tabs, _ := h.cat.Tables(ctx, "proj-a", "sales")
	if len(tabs) != 1 || tabs[0].Name != "orders" {
		t.Fatalf("ignored table must be dropped: %+v", tabs)
	}
	ds, _ := h.cat.Datasets(ctx, "proj-a")
	for _, d := range ds {
		if d.Name == "logs" || d.Name == "ghost" {
			t.Fatalf("%s must not be in the catalog", d.Name)
		}
	}
}

func TestSyncPermissionLadder(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	p := h.fake.AddProject("proj-a")
	p.Datasets = []bqtest.Dataset{
		bqtest.DS("open", "EU", bqtest.Table("t1", "a", "b")),
		bqtest.DS("names", "EU", bqtest.Table("t2", "a"), bqtest.Table("t3", "a")),
		bqtest.DS("locked", "EU", bqtest.Table("t4", "a")),
	}
	h.run(api.SyncScope{}) // everything readable first, so "locked" has old data
	p.Datasets[1].Denied, p.Datasets[1].NamesOK = true, true
	p.Datasets[2].Denied = true
	h.now = h.now.Add(time.Hour)
	res := h.run(api.SyncScope{})

	u := h.unit("proj-a", "EU")
	if u.Status != api.UnitDegraded || u.NamesOnly != 1 || u.Inaccessible != 1 {
		t.Fatalf("%+v", u)
	}
	if res.UnitsDegraded != 1 || u.Error == "" {
		t.Fatalf("%+v %+v", res, u)
	}
	cols, _ := h.cat.Columns(ctx, "proj-a", "locked", "t4")
	if len(cols) != 1 {
		t.Fatalf("inaccessible dataset keeps its old data: %+v", cols)
	}
	cols, _ = h.cat.Columns(ctx, "proj-a", "names", "t2")
	if len(cols) != 0 {
		t.Fatalf("names-only dataset drops columns: %+v", cols)
	}
	tabs, _ := h.cat.Tables(ctx, "proj-a", "names")
	if len(tabs) != 2 || !tabs[0].NamesOnly {
		t.Fatalf("%+v", tabs)
	}
	ds, _ := h.cat.Datasets(ctx, "proj-a")
	st := map[string]api.DatasetStatus{}
	for _, d := range ds {
		st[d.Name] = d.Status
	}
	if st["open"] != api.DatasetOK || st["names"] != api.DatasetNamesOnly || st["locked"] != api.DatasetInaccessible {
		t.Fatalf("%v", st)
	}
	if len(h.fake.CallsTo("DatasetSnapshot")) < 3 {
		t.Fatal("ladder should query each dataset")
	}
}

func TestSyncAllDatasetsInaccessible(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	p := h.fake.AddProject("proj-a")
	p.Datasets = []bqtest.Dataset{bqtest.DS("a", "EU", bqtest.Table("t", "c")), bqtest.DS("b", "EU", bqtest.Table("t", "c"))}
	p.Datasets[0].Denied, p.Datasets[1].Denied = true, true
	res := h.run(api.SyncScope{})
	if res.UnitsFailed != 1 {
		t.Fatalf("%+v", res)
	}
	u := h.unit("proj-a", "EU")
	if u.Status != api.UnitInaccessible || u.LastSuccess != nil {
		t.Fatalf("%+v", u)
	}
	var failed bool
	for _, e := range h.events {
		if e.Kind == api.EventUnitFailed && e.Error != "" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("expected a unit_failed event carrying the error")
	}
}

func TestSyncTransientRetries(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.fake.Fail(bqtest.Failure{Method: "RegionSnapshot", Project: "proj-a", Location: "EU", Err: bqtest.TransientError("blip"), Times: 2})
	res := h.run(api.SyncScope{})
	if res.UnitsOK != 2 || res.UnitsFailed != 0 {
		t.Fatalf("two transient failures are retried: %+v", res)
	}
	var euCalls int
	for _, c := range h.fake.CallsTo("RegionSnapshot") {
		if c.Location == "EU" {
			euCalls++
		}
	}
	if euCalls != 3 {
		t.Fatalf("1 attempt + 2 retries, got %d", euCalls)
	}
}

func TestSyncTransientExhaustedKeepsOldData(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	before := h.counts()
	h.fake.Fail(bqtest.Failure{Method: "RegionSnapshot", Project: "proj-a", Location: "EU", Err: bqtest.TransientError("still down")})
	h.now = h.now.Add(time.Hour)
	res := h.run(api.SyncScope{})
	if res.UnitsFailed != 1 || res.UnitsOK != 1 {
		t.Fatalf("%+v", res)
	}
	if h.counts() != before {
		t.Fatal("a failed unit must keep its old data")
	}
	u := h.unit("proj-a", "EU")
	if u.Status != api.UnitDegraded || u.LastSuccess == nil || u.Error == "" {
		t.Fatalf("stale-but-present data is degraded: %+v", u)
	}
	if !u.LastAttempt.After(*u.LastSuccess) {
		t.Fatalf("last attempt must move past last success: %+v", u)
	}
}

func TestSyncDiscoveryFailureIsolated(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	h.fake.Projects["proj-b"].ListErr = bqtest.PermissionError("datasets.list")
	res := h.run(api.SyncScope{})
	if res.UnitsOK != 2 || res.UnitsFailed != 1 {
		t.Fatalf("%+v", res)
	}
	u := h.unit("proj-b", "")
	if u.Status != api.UnitInaccessible || u.Error == "" {
		t.Fatalf("%+v", u)
	}
	// recovery clears the discovery failure
	h.fake.Projects["proj-b"].ListErr = nil
	h.run(api.SyncScope{})
	us, _ := h.cat.Units(ctx)
	for _, x := range us {
		if x.Project == "proj-b" && x.Location == "" {
			t.Fatal("discovery failure must clear once the project is readable")
		}
	}
}

func TestSyncUnreachableWarning(t *testing.T) {
	h := newHarness(t, `projects = ["proj-b"]`)
	twoProjectFake(h.fake)
	h.fake.Projects["proj-b"].Unreachable = []string{"projects/proj-b/locations/europe-west9"}
	res := h.run(api.SyncScope{})
	if len(res.Warnings) != 1 {
		t.Fatalf("%v", res.Warnings)
	}
	u := h.unit("proj-b", "")
	if u.Status != api.UnitDegraded {
		t.Fatalf("%+v", u)
	}
}

func TestSyncRemovesVanishedLocationAndDatasets(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	p := h.fake.Projects["proj-a"]
	p.Datasets = p.Datasets[:2] // us_data disappears, the US location with it
	p.Datasets[1].Tables = nil  // logs loses its table
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})

	locs, _ := h.cat.Locations(ctx, "proj-a")
	if len(locs) != 1 || locs[0] != "EU" {
		t.Fatalf("%v", locs)
	}
	n := h.counts()
	if n.Datasets != 2 || n.Tables != 2 {
		t.Fatalf("%+v", n)
	}
}

func TestSyncCancelKeepsCommittedUnits(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	cctx, cancel := context.WithCancel(ctx)
	var regionCalls atomic.Int32
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionSnapshot" && regionCalls.Add(1) == 2 {
			cancel()
		}
	}
	s := New(Options{Config: h.cfg, Meta: h.fake, Catalog: h.cat, Parallelism: 1, Now: func() time.Time { return h.now }, Backoff: func(int) time.Duration { return 0 }})
	res, err := s.Run(cctx, api.SyncScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Cancelled {
		t.Fatalf("%+v", res)
	}
	n := h.counts()
	if n.Datasets == 0 {
		t.Fatal("the unit committed before the cancel must stay")
	}
	if n.Datasets >= 4 {
		t.Fatalf("not everything should have been synced: %+v", n)
	}
	if v, _, _ := h.cat.Meta(ctx, MetaSyncNeeded); v != "1" {
		t.Fatal("a cancelled sync must not clear sync_needed")
	}
}

func TestSyncRespectsParallelism(t *testing.T) {
	h := newHarness(t, `projects = ["p"]`)
	p := h.fake.AddProject("p")
	for i := 0; i < 8; i++ {
		p.Datasets = append(p.Datasets, bqtest.DS(fmt.Sprintf("ds%d", i), fmt.Sprintf("loc-%d", i), bqtest.Table("t", "c")))
	}
	var cur, max atomic.Int32
	h.fake.Delay = 15 * time.Millisecond
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method != "RegionSnapshot" {
			return
		}
		n := cur.Add(1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		time.AfterFunc(10*time.Millisecond, func() { cur.Add(-1) })
	}
	s := New(Options{Config: h.cfg, Meta: h.fake, Catalog: h.cat, Parallelism: 3, Now: time.Now, Backoff: func(int) time.Duration { return 0 }})
	if _, err := s.Run(ctx, api.SyncScope{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := max.Load(); got > 3 || got < 2 {
		t.Fatalf("max concurrency %d, want 2..3", got)
	}
	if h.counts().Datasets != 8 {
		t.Fatal("all units must complete")
	}
}

func TestSyncStoresShardFamilies(t *testing.T) {
	h := newHarness(t, `projects = ["p"]`)
	p := h.fake.AddProject("p")
	p.Datasets = []bqtest.Dataset{bqtest.DS("ev", "EU",
		bqtest.Table("events_20240101", "c"), bqtest.Table("events_20240103", "c"), bqtest.Table("events_20240102", "c"),
		bqtest.Table("lonely_20240101", "c"), bqtest.Table("plain", "c"))}
	h.run(api.SyncScope{})
	tabs, _ := h.cat.Tables(ctx, "p", "ev")
	got := map[string]*api.ShardFamily{}
	for _, tb := range tabs {
		got[tb.Name] = tb.Shard
	}
	f := got["events_20240102"]
	if f == nil || f.Prefix != "events_" || f.Count != 3 || f.First != "20240101" || f.Last != "20240103" {
		t.Fatalf("%+v", f)
	}
	if got["lonely_20240101"] != nil || got["plain"] != nil {
		t.Fatal("single shards and plain tables are not families")
	}
}

func TestScopeValidation(t *testing.T) {
	h := newHarness(t, ``)
	twoProjectFake(h.fake)
	if _, err := h.syncer().Run(ctx, api.SyncScope{}, nil); !errors.Is(err, ErrNoProjects) {
		t.Fatalf("%v", err)
	}
	h2 := newHarness(t, `projects = ["proj-a"]`)
	if _, err := h2.syncer().Run(ctx, api.SyncScope{Projects: []string{"other"}}, nil); err == nil {
		t.Fatal("unknown project in scope must be rejected")
	}
}

func TestPartialScopeDoesNotClearSyncNeeded(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{Projects: []string{"proj-a"}})
	if v, _, _ := h.cat.Meta(ctx, MetaSyncNeeded); v != "1" {
		t.Fatal("a partial sync leaves sync_needed set")
	}
	if h.counts().Projects != 1 {
		t.Fatalf("%+v", h.counts())
	}
	h.run(api.SyncScope{})
	if v, _, _ := h.cat.Meta(ctx, MetaSyncNeeded); v != "0" {
		t.Fatal("a full sync clears sync_needed")
	}
}

func TestReconcileConfigChanges(t *testing.T) {
	h := newHarness(t, `
projects = ["proj-a", "proj-b"]
ignore = ["proj-a.logs"]
`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	needed := func() bool { v, _, _ := h.cat.Meta(ctx, MetaSyncNeeded); return v == "1" }
	if needed() {
		t.Fatal("clean after a full sync")
	}

	// 1. same config: no change
	r, err := ReconcileConfig(ctx, h.cat, h.cfg)
	if err != nil || r.Changed || r.SyncNeeded {
		t.Fatalf("%+v %v", r, err)
	}

	// 2. tighten: append an ignore -> prune, no sync needed
	h.cfg = cfgFrom(t, "projects = [\"proj-a\", \"proj-b\"]\nignore = [\"proj-a.logs\", \"proj-a.sales\"]\n")
	r, err = ReconcileConfig(ctx, h.cat, h.cfg)
	if err != nil || !r.Changed || r.SyncNeeded || r.Pruned.Datasets != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if needed() {
		t.Fatal("tightening must not demand a sync")
	}
	ds, _ := h.cat.Datasets(ctx, "proj-a")
	for _, d := range ds {
		if d.Name == "sales" {
			t.Fatal("sales should be pruned")
		}
	}

	// 3. remove a project -> prune, no sync needed
	h.cfg = cfgFrom(t, "projects = [\"proj-a\"]\nignore = [\"proj-a.logs\", \"proj-a.sales\"]\n")
	r, _ = ReconcileConfig(ctx, h.cat, h.cfg)
	if r.Pruned.Projects != 1 || r.SyncNeeded {
		t.Fatalf("%+v", r)
	}

	// 4. loosen ignore -> sync needed
	h.cfg = cfgFrom(t, "projects = [\"proj-a\"]\nignore = [\"proj-a.logs\"]\n")
	r, _ = ReconcileConfig(ctx, h.cat, h.cfg)
	if !r.SyncNeeded || r.Reason != "ignore patterns changed" || !needed() {
		t.Fatalf("%+v", r)
	}

	// 5. add a project -> sync needed with reason
	h.cfg = cfgFrom(t, "projects = [\"proj-a\", \"proj-b\"]\nignore = [\"proj-a.logs\"]\n")
	r, _ = ReconcileConfig(ctx, h.cat, h.cfg)
	if !r.SyncNeeded || r.Reason != "projects added: proj-b" {
		t.Fatalf("%+v", r)
	}
	pending, reason, _ := PendingConfigChange(ctx, h.cat, h.cfg)
	if !pending || reason == "" {
		t.Fatal("read-only check must report the pending sync")
	}
}

func TestShardAssign(t *testing.T) {
	mk := func(names ...string) []api.TableMeta {
		var ts []api.TableMeta
		for _, n := range names {
			ts = append(ts, api.TableMeta{Name: n, Type: api.TypeTable})
		}
		return ts
	}
	ts := mk("a_20240101", "a_20240102", "a_20241301" /* bad month */, "b20240101", "b20240105", "20240101", "20240102", "c_2024010", "d_20240101_x")
	AssignShards(ts)
	want := map[string]string{"a_20240101": "a_", "a_20240102": "a_", "b20240101": "b", "b20240105": "b"}
	for _, tb := range ts {
		w, in := want[tb.Name]
		switch {
		case in && (tb.Shard == nil || tb.Shard.Prefix != w || tb.Shard.Count != 2):
			t.Errorf("%s: %+v", tb.Name, tb.Shard)
		case !in && tb.Shard != nil:
			t.Errorf("%s must not be sharded: %+v", tb.Name, tb.Shard)
		}
	}
	// same prefix but different types are different families
	mixed := []api.TableMeta{{Name: "x_20240101", Type: api.TypeTable}, {Name: "x_20240102", Type: api.TypeView}}
	AssignShards(mixed)
	if mixed[0].Shard != nil || mixed[1].Shard != nil {
		t.Fatal("tables and views do not share a family")
	}
}
