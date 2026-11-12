package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

var ctx = context.Background()

var projectsLine = regexp.MustCompile(`(?m)^projects = \[(.*)\]\n?`)

// splitProjects takes a `projects = [...]` line (the Selected projects, as
// these tests write them) out of a config snippet.
func splitProjects(extra string) (selected []string, rest string) {
	if m := projectsLine.FindStringSubmatch(extra); m != nil {
		for _, p := range strings.Split(m[1], ",") {
			if p = strings.Trim(strings.TrimSpace(p), `"`); p != "" {
				selected = append(selected, p)
			}
		}
	}
	return selected, projectsLine.ReplaceAllString(extra, "")
}

func cfgFrom(t *testing.T, extra string) *config.Config {
	t.Helper()
	_, rest := splitProjects(extra)
	c, err := config.Parse([]byte(rest), "t.toml", config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type harness struct {
	t        *testing.T
	selected []string
	fake     *bqtest.Fake
	cat      *store.Catalog
	cfg      *config.Config
	now      time.Time
	events   []api.SyncEvent
	logs     *logBuf
}

func newHarness(t *testing.T, cfgExtra string) *harness {
	t.Helper()
	cat, err := store.OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	selected, _ := splitProjects(cfgExtra)
	return &harness{t: t, selected: selected, fake: bqtest.New(), cat: cat, cfg: cfgFrom(t, cfgExtra), now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), logs: &logBuf{}}
}

func (h *harness) syncer() *Syncer {
	return New(Options{
		Config: h.cfg, Selected: h.selected, Meta: h.fake, Catalog: h.cat,
		Now:     func() time.Time { return h.now },
		Backoff: func(int) time.Duration { return 0 },
		Log:     h.logs.logger(),
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

// statuses maps "project.dataset" to its catalog status.
func (h *harness) statuses(projects ...string) map[string]api.DatasetStatus {
	h.t.Helper()
	out := map[string]api.DatasetStatus{}
	for _, p := range projects {
		ds, err := h.cat.Datasets(ctx, p)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, d := range ds {
			out[p+"."+d.Name] = d.Status
		}
	}
	return out
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
	calls := h.fake.CallsTo("RegionTables")
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
	if n := len(h.fake.CallsTo("GetDataset")); n != 0 {
		t.Fatalf("the region path needs no datasets.get, got %d calls", n)
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

	for _, c := range h.fake.CallsTo("RegionTables") {
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
	if len(h.fake.CallsTo("DatasetTables")) < 3 {
		t.Fatal("ladder should query each dataset")
	}
}

// Dataset-level grants refuse every region view, and SCHEMATA_OPTIONS has no
// dataset scope, so the fallback reads dataset options from datasets.get.
func TestSyncDatasetScopeFallbackKeepsDatasetOptions(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	p := h.fake.AddProject("proj-a")
	granted := bqtest.DS("granted", "US", bqtest.Table("t1", "a"))
	granted.Meta.Description = "granted to bqt-restricted"
	granted.Meta.FriendlyName = "Granted"
	granted.Meta.Labels = map[string]string{"team": "data"}
	names := bqtest.DS("names", "US", bqtest.Table("t2", "a"))
	names.Denied, names.NamesOK = true, true
	p.Datasets = []bqtest.Dataset{granted, bqtest.DS("broken", "US", bqtest.Table("t3", "a")), names}
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Err: bqtest.PermissionError("region-us")})
	h.fake.Fail(bqtest.Failure{Method: "GetDataset", Dataset: "broken", Err: bqtest.TransientError("datasets.get")})

	h.run(api.SyncScope{})

	if u := h.unit("proj-a", "US"); u.Status != api.UnitDegraded || u.NamesOnly != 1 || u.Inaccessible != 0 {
		t.Fatalf("a failed datasets.get must not fail the dataset: %+v", u)
	}
	ds, _ := h.cat.Datasets(ctx, "proj-a")
	byName := map[string]store.DatasetRow{}
	for _, d := range ds {
		byName[d.Name] = d
	}
	g := byName["granted"]
	if g.Description != "granted to bqt-restricted" || g.FriendlyName != "Granted" || g.Labels["team"] != "data" {
		t.Fatalf("dataset options must reach the catalog: %+v", g)
	}
	if b := byName["broken"]; b.Status != api.DatasetOK || b.Description != "" {
		t.Fatalf("%+v", b)
	}
	if n := byName["names"]; n.Status != api.DatasetNamesOnly || n.Description != "dataset names" {
		t.Fatalf("the names-only rung keeps dataset options too: %+v", n)
	}
	if got := len(h.fake.CallsTo("GetDataset")); got < 3 {
		t.Fatalf("GetDataset calls = %d, want one per dataset (plus retries)", got)
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
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Location: "EU", Err: bqtest.TransientError("blip"), Times: 2})
	res := h.run(api.SyncScope{})
	if res.UnitsOK != 2 || res.UnitsFailed != 0 {
		t.Fatalf("two transient failures are retried: %+v", res)
	}
	var euCalls int
	for _, c := range h.fake.CallsTo("RegionTables") {
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
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Location: "EU", Err: bqtest.TransientError("still down")})
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

// warningMeta reports a client warning (bq.Warn) on every dataset listing,
// as the real client does on every query once it reads through REST paging.
type warningMeta struct{ *bqtest.Fake }

func (m warningMeta) ListDatasets(ctx context.Context, project string) (bq.DatasetList, error) {
	bq.Warn(ctx, "sync reads results through REST paging")
	return m.Fake.ListDatasets(ctx, project)
}

func TestSyncAddsAClientWarningOncePerRun(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	s := New(Options{
		Config: h.cfg, Selected: h.selected, Meta: warningMeta{h.fake}, Catalog: h.cat,
		Now: func() time.Time { return h.now }, Backoff: func(int) time.Duration { return 0 },
	})
	for i := 1; i <= 2; i++ {
		var events []api.SyncEvent
		var mu gosync.Mutex
		res, err := s.Run(ctx, api.SyncScope{}, func(e api.SyncEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || res.Warnings[0] != "sync reads results through REST paging" {
			t.Fatalf("run %d: warnings %q", i, res.Warnings)
		}
		n := 0
		for _, e := range events {
			if e.Kind == api.EventWarning {
				n++
			}
		}
		if n != 1 || res.UnitsOK != 3 {
			t.Fatalf("run %d: %d warning events, %d units ok", i, n, res.UnitsOK)
		}
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

// Issue 33: a project with no datasets has no (project, location) unit, so
// its discovery unit records the successful sync. It gives way to real units
// once datasets appear, and comes back when they are all gone.
func TestSyncRecordsAnEmptyProjectOnItsDiscoveryUnit(t *testing.T) {
	h := newHarness(t, `projects = ["proj-e"]`)
	p := h.fake.AddProject("proj-e")
	first := h.now
	if res := h.run(api.SyncScope{}); res.UnitsTotal != 0 || res.UnitsFailed != 0 {
		t.Fatalf("%+v", res)
	}
	if u := h.unit("proj-e", ""); u.Status != api.UnitOK || u.Error != "" || u.LastSuccess == nil || !u.LastSuccess.Equal(first) {
		t.Fatalf("%+v", u)
	}

	// A failed listing keeps the earlier success, like any failed unit.
	p.ListErr = bqtest.TransientError("boom")
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if u := h.unit("proj-e", ""); u.Status != api.UnitInaccessible || u.LastSuccess == nil || !u.LastSuccess.Equal(first) {
		t.Fatalf("%+v", u)
	}

	// Datasets appear: the real unit holds the sync, the discovery unit goes.
	p.ListErr = nil
	p.Datasets = []bqtest.Dataset{bqtest.DS("core", "EU", bqtest.Table("t", "c"))}
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if u := h.unit("proj-e", "EU"); u.LastSuccess == nil || !u.LastSuccess.Equal(h.now) {
		t.Fatalf("%+v", u)
	}
	us, _ := h.cat.Units(ctx)
	for _, u := range us {
		if u.Location == "" {
			t.Fatalf("discovery unit kept beside a real one: %+v", u)
		}
	}

	// They are all deleted: empty again, and synced now.
	p.Datasets = nil
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if u := h.unit("proj-e", ""); u.Status != api.UnitOK || u.LastSuccess == nil || !u.LastSuccess.Equal(h.now) {
		t.Fatalf("%+v", u)
	}
	if locs, _ := h.cat.Locations(ctx, "proj-e"); len(locs) != 0 {
		t.Fatalf("%v", locs)
	}
}

// Issue 33: a project whose listing never succeeded records no success.
func TestSyncAFailedListingRecordsNoSuccess(t *testing.T) {
	h := newHarness(t, `projects = ["proj-e"]`)
	h.fake.AddProject("proj-e").ListErr = bqtest.TransientError("boom")
	h.run(api.SyncScope{})
	if u := h.unit("proj-e", ""); u.Status != api.UnitInaccessible || u.LastSuccess != nil {
		t.Fatalf("%+v", u)
	}
}

func TestSyncCancelKeepsCommittedUnits(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	cctx, cancel := context.WithCancel(ctx)
	var regionCalls atomic.Int32
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionTables" && regionCalls.Add(1) == 2 {
			cancel()
		}
	}
	s := New(Options{Config: h.cfg, Selected: h.selected, Meta: h.fake, Catalog: h.cat, Parallelism: 1, Now: func() time.Time { return h.now }, Backoff: func(int) time.Duration { return 0 }})
	res, err := s.Run(cctx, api.SyncScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Cancelled {
		t.Fatalf("%+v", res)
	}
	st := h.statuses("proj-a", "proj-b")
	committed, pending := 0, 0
	for _, s := range st {
		switch s {
		case api.DatasetOK:
			committed++
		case api.DatasetPending:
			pending++
		}
	}
	if committed == 0 {
		t.Fatalf("the unit committed before the cancel must stay: %v", st)
	}
	if pending == 0 || committed+pending != 4 {
		t.Fatalf("the cancelled units' datasets stay listed, pending: %v", st)
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
		if c.Method != "RegionTables" {
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
	s := New(Options{Config: h.cfg, Selected: h.selected, Meta: h.fake, Catalog: h.cat, Parallelism: 3, Now: time.Now, Backoff: func(int) time.Duration { return 0 }})
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
	h := newHarness(t, "projects = [\"p\"]\nshard_patterns = ['.+(\\d{8})']\n")
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
	if f == nil || f.Prefix != "events_" || !f.Date || f.Name != "events_YYYYMMDD" || f.Count != 3 || f.First != "20240101" || f.Last != "20240103" || f.Newest != "events_20240103" {
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
	// an explicit scope may name a project that is not selected (the
	// engine decides which are allowed); it is not a full sync
	h2 := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h2.fake)
	h2.run(api.SyncScope{Projects: []string{"proj-b"}})
	if h2.counts().Projects != 1 {
		t.Fatalf("%+v", h2.counts())
	}
	if v, _, _ := h2.cat.Meta(ctx, MetaSyncNeeded); v == "0" {
		t.Fatal("syncing another project is not a full sync")
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

	// 3. loosen ignore -> sync needed; every project keeps its data (the
	// selection never prunes)
	h.cfg = cfgFrom(t, "ignore = [\"proj-a.logs\"]\n")
	r, _ = ReconcileConfig(ctx, h.cat, h.cfg)
	if !r.SyncNeeded || r.Reason != "ignore patterns changed" || !needed() || r.Pruned.Projects != 0 || h.counts().Projects != 2 {
		t.Fatalf("%+v", r)
	}
	pending, reason, _ := PendingConfigChange(ctx, h.cat, h.cfg)
	if !pending || reason == "" {
		t.Fatal("read-only check must report the pending sync")
	}
}

func TestACatalogFromBeforeTheProjectsDialogNeedsNoSync(t *testing.T) {
	h := newHarness(t, "projects = [\"proj-a\"]\nignore = [\"proj-a.logs\"]\n")
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	// the stored filter also had the old `projects` list
	if err := h.cat.SetMeta(ctx, MetaConfigHash, "an older hash"); err != nil {
		t.Fatal(err)
	}
	if err := h.cat.SetMeta(ctx, MetaFilter, `{"projects":["proj-a"],"ignore":["proj-a.logs"]}`); err != nil {
		t.Fatal(err)
	}
	if pending, reason, _ := PendingConfigChange(ctx, h.cat, h.cfg); pending {
		t.Fatalf("nothing that matters changed: %s", reason)
	}
}

func TestUnreachableLocationKeepsItsData(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	before := h.counts()
	// BigQuery cannot reach the US location: datasets.list omits it and says so
	p := h.fake.Projects["proj-a"]
	p.Datasets = p.Datasets[:2]
	p.Unreachable = []string{"us"}
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if h.counts() != before {
		t.Fatalf("a skipped location must keep its cached data: %+v -> %+v", before, h.counts())
	}
	locs, _ := h.cat.Locations(ctx, "proj-a")
	if len(locs) != 2 {
		t.Fatalf("%v", locs)
	}
}

// Issue 82: the listed datasets are in the catalog, pending, before any unit
// is read; each committed unit replaces its pending rows.
func TestSyncListedDatasetsArePendingUntilTheirUnitCommits(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	var first gosync.Once
	var mid map[string]api.DatasetStatus
	var midRows []store.DatasetRow
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionTables" {
			first.Do(func() {
				mid = h.statuses("proj-a", "proj-b")
				midRows, _ = h.cat.Datasets(ctx, "proj-a")
			})
		}
	}
	h.fake.Projects["proj-a"].Datasets[2].Meta.FriendlyName = "US data"
	h.fake.Projects["proj-a"].Datasets[2].Meta.Labels = map[string]string{"team": "us"}
	h.run(api.SyncScope{})

	want := map[string]api.DatasetStatus{"proj-a.sales": api.DatasetPending, "proj-a.logs": api.DatasetPending, "proj-a.us_data": api.DatasetPending, "proj-b.core": api.DatasetPending}
	if fmt.Sprint(mid) != fmt.Sprint(want) {
		t.Fatalf("before the first unit: %v", mid)
	}
	for _, d := range midRows {
		if d.Tables != 0 {
			t.Fatalf("a pending dataset has no tables: %+v", d)
		}
		if d.Name == "us_data" && (d.Location != "US" || d.FriendlyName != "US data" || d.Labels["team"] != "us") {
			t.Fatalf("a pending row carries the listing's location, friendly name and labels: %+v", d)
		}
	}
	for k, s := range h.statuses("proj-a", "proj-b") {
		if s != api.DatasetOK {
			t.Fatalf("%s: a committed unit clears pending, got %s", k, s)
		}
	}
	var discovered []api.UnitRef
	for _, e := range h.events {
		if e.Kind == api.EventDiscovered {
			discovered = e.Units
		}
	}
	if len(discovered) != 3 || discovered[0] != (api.UnitRef{Project: "proj-a", Location: "EU"}) {
		t.Fatalf("the discovered event names the units: %v", discovered)
	}
}

func TestSyncFailedUnitLeavesItsDatasetsPending(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Location: "US", Err: bqtest.TransientError("still down")})
	res := h.run(api.SyncScope{})
	if res.UnitsFailed != 1 || res.UnitsOK != 1 {
		t.Fatalf("%+v", res)
	}
	st := h.statuses("proj-a")
	if st["proj-a.us_data"] != api.DatasetPending || st["proj-a.sales"] != api.DatasetOK {
		t.Fatalf("%v", st)
	}
	if u := h.unit("proj-a", "US"); u.Status != api.UnitInaccessible || u.Error == "" || u.LastSuccess != nil {
		t.Fatalf("the failed unit says why: %+v", u)
	}

	// The next sync that reaches the unit clears it.
	h.fake.Failures = nil
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if st := h.statuses("proj-a"); st["proj-a.us_data"] != api.DatasetOK {
		t.Fatalf("%v", st)
	}
}

func TestSyncExistingDatasetsKeepTheirTablesWhileTheirUnitRefreshes(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	p := h.fake.Projects["proj-a"]
	p.Datasets = append(p.Datasets, bqtest.DS("added", "EU", bqtest.Table("a1", "x")))
	var mid []store.DatasetRow
	var once gosync.Once
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionTables" {
			once.Do(func() { mid, _ = h.cat.Datasets(ctx, "proj-a") })
		}
	}
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	got := map[string]string{}
	for _, d := range mid {
		got[d.Name] = fmt.Sprintf("%s/%d", d.Status, d.Tables)
	}
	want := map[string]string{"added": "pending/0", "logs": "ok/1", "sales": "ok/2", "us_data": "ok/1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("during the refresh: %v", got)
	}
}

func TestSyncCompleteListingRemovesVanishedPendingDatasets(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	p := h.fake.Projects["proj-a"]
	all := p.Datasets
	p.Datasets = append(append([]bqtest.Dataset(nil), all...), bqtest.DS("fresh", "EU", bqtest.Table("f", "x")), bqtest.DS("asia_ds", "asia-east1", bqtest.Table("a", "x")))
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Err: bqtest.TransientError("down")})
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if st := h.statuses("proj-a"); st["proj-a.fresh"] != api.DatasetPending || st["proj-a.asia_ds"] != api.DatasetPending {
		t.Fatalf("%v", st)
	}

	// An incomplete listing (unreachable locations) removes nothing.
	p.Datasets = all
	p.Unreachable = []string{"asia-east1"}
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	if st := h.statuses("proj-a"); st["proj-a.fresh"] != api.DatasetPending || st["proj-a.asia_ds"] != api.DatasetPending {
		t.Fatalf("an incomplete listing keeps pending rows: %v", st)
	}

	// A complete listing removes both: one in a location still listed
	// (whose unit fails again), one in a location gone.
	p.Unreachable = nil
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	st := h.statuses("proj-a")
	if _, ok := st["proj-a.fresh"]; ok {
		t.Fatalf("%v", st)
	}
	if _, ok := st["proj-a.asia_ds"]; ok {
		t.Fatalf("%v", st)
	}
	if st["proj-a.sales"] != api.DatasetOK || len(st) != 3 {
		t.Fatalf("the synced datasets stay: %v", st)
	}
}

// A dataset recreated in another location is a new, unsynced dataset there.
// Its row moves with the listing, so it stays visible when the new location's
// unit fails: whether the old location is gone (fresh) or still listed (logs),
// and whether the old row was pending (fresh) or synced (logs).
func TestSyncDatasetRecreatedElsewhereStaysListedWhenItsUnitFails(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a"]`)
	twoProjectFake(h.fake)
	p := h.fake.Projects["proj-a"]
	p.Datasets = append(p.Datasets, bqtest.DS("fresh", "asia-east1", bqtest.Table("f", "x")))
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Location: "asia-east1", Err: bqtest.TransientError("down")})
	h.run(api.SyncScope{})
	if st := h.statuses("proj-a"); st["proj-a.fresh"] != api.DatasetPending || st["proj-a.logs"] != api.DatasetOK {
		t.Fatalf("%v", st)
	}

	p.Datasets = []bqtest.Dataset{
		bqtest.DS("sales", "EU", bqtest.Table("orders", "id", "amount"), bqtest.Table("items", "sku")),
		bqtest.DS("logs", "US", bqtest.Table("l2", "msg")),
		bqtest.DS("us_data", "US", bqtest.Table("u1", "x")),
		bqtest.DS("fresh", "US", bqtest.Table("f", "x")),
	}
	h.fake.Failures = nil
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "proj-a", Location: "US", Err: bqtest.TransientError("down")})
	h.now = h.now.Add(time.Hour)
	h.run(api.SyncScope{})
	rows, err := h.cat.Datasets(ctx, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range rows {
		got[d.Name] = d.Location + " " + string(d.Status)
	}
	for name, want := range map[string]string{"fresh": "US pending", "logs": "US pending", "sales": "EU ok", "us_data": "US ok"} {
		if got[name] != want {
			t.Fatalf("%s: got %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
}
