package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

var ctx = context.Background()

func openTemp(t *testing.T) *Catalog {
	t.Helper()
	c, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func unit(p, l string) api.UnitRef { return api.UnitRef{Project: p, Location: l} }

func okState(now time.Time) api.UnitState {
	return api.UnitState{Status: api.UnitOK, LastAttempt: &now, LastSuccess: &now}
}

func table(ds, name string, cols ...string) api.TableMeta {
	t := api.TableMeta{Dataset: ds, Name: name, Type: api.TypeTable, CreatedAt: t0}
	for i, c := range cols {
		t.Columns = append(t.Columns, api.ColumnMeta{FieldPath: c, Ordinal: i + 1, DataType: "STRING"})
	}
	return t
}

func dsApply(name, loc string, tables ...api.TableMeta) DatasetApply {
	return DatasetApply{Meta: api.DatasetMeta{Project: "p", Name: name, Location: loc, Labels: map[string]string{"env": "dev"}}, Tables: tables}
}

func apply(t *testing.T, c *Catalog, u api.UnitRef, now time.Time, ds ...DatasetApply) ApplyStats {
	t.Helper()
	st, err := c.ApplyUnit(ctx, ApplyRequest{Unit: u, Datasets: ds, State: okState(now)})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCatalogCreateAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Recreated() != "" {
		t.Fatal("fresh catalog is not 'recreated'")
	}
	if err := c.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Recreated() != "" {
		t.Fatalf("reopen must not recreate: %s", c.Recreated())
	}
	if v, ok, _ := c.Meta(ctx, "k"); !ok || v != "v" {
		t.Fatal("meta lost")
	}
}

func TestCatalogVersionMismatchRecreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, _ := OpenCatalog(path)
	c.SetMeta(ctx, "k", "v")
	c.db.Exec("PRAGMA user_version = 99")
	c.Close()
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Recreated() == "" {
		t.Fatal("expected a recreate notice")
	}
	if _, ok, _ := c.Meta(ctx, "k"); ok {
		t.Fatal("old data must be gone")
	}
}

func TestCatalogCorruptRecreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	if err := os.WriteFile(path, []byte("this is definitely not a sqlite database, just text padding padding padding padding"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatalf("corrupt catalog must be recreated silently: %v", err)
	}
	defer c.Close()
	if c.Recreated() == "" {
		t.Fatal("expected a recreate notice")
	}
	if _, err := c.Counts(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	if _, err := OpenCatalogReadOnly(path); err != ErrNoCatalog {
		t.Fatalf("missing: %v", err)
	}
	c, _ := OpenCatalog(path)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "t1", "a")))
	ro, err := OpenCatalogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	n, err := ro.Counts(ctx)
	if err != nil || n.Tables != 1 {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := ro.db.Exec("DELETE FROM tables"); err == nil {
		t.Fatal("read-only handle must refuse writes")
	}
	c.Close()
}

func TestApplyUnitRoundTrip(t *testing.T) {
	c := openTemp(t)
	exp := t0.Add(48 * time.Hour)
	tb := table("d1", "orders", "id", "addr", "addr.city", "addr.geo", "addr.geo.lat")
	tb.Description = "Orders"
	tb.Labels = map[string]string{"team": "x"}
	tb.ExpiresAt = &exp
	tb.Columns[0].Partitioning = true
	tb.Columns[1].ClusteringOrdinal = 2
	tb.Columns[0].Required = true
	view := api.TableMeta{Dataset: "d1", Name: "v", Type: api.TypeView, ViewSQL: "SELECT 1", CreatedAt: t0}
	st := apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", tb, view))
	if st.TablesAdded != 2 || st.DatasetsAdded != 1 {
		t.Fatalf("%+v", st)
	}
	tabs, err := c.Tables(ctx, "p", "d1")
	if err != nil || len(tabs) != 2 {
		t.Fatalf("%v %v", tabs, err)
	}
	got := tabs[0] // orders sorts before v
	if got.Name != "orders" || got.Description != "Orders" || got.Labels["team"] != "x" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || !got.CreatedAt.Equal(t0) {
		t.Fatalf("%+v", got)
	}
	if tabs[1].ViewSQL != "SELECT 1" || tabs[1].Type != api.TypeView {
		t.Fatalf("%+v", tabs[1])
	}
	cols, _ := c.Columns(ctx, "p", "d1", "orders")
	if len(cols) != 5 || cols[0].FieldPath != "id" || !cols[0].Partitioning || cols[1].ClusteringOrdinal != 2 || !cols[0].Required || cols[1].Required {
		t.Fatalf("%+v", cols)
	}
	// parent links resolved
	var parent int64
	if err := c.db.QueryRow(`SELECT parent_id FROM columns WHERE field_path='addr.geo.lat'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	var pp string
	c.db.QueryRow(`SELECT field_path FROM columns WHERE id=?`, parent).Scan(&pp)
	if pp != "addr.geo" {
		t.Fatalf("parent = %q", pp)
	}
	ds, _ := c.Datasets(ctx, "p")
	if len(ds) != 1 || ds[0].Tables != 2 || ds[0].Labels["env"] != "dev" || ds[0].Status != api.DatasetOK {
		t.Fatalf("%+v", ds)
	}
}

func TestApplyUnitDiff(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x"), table("d1", "b", "x"), table("d1", "c", "x")))

	changed := table("d1", "b", "x", "y")
	st := apply(t, c, unit("p", "EU"), t0.Add(time.Hour), dsApply("d1", "EU", table("d1", "a", "x"), changed, table("d1", "d", "z")))
	if st.TablesUnchanged != 1 || st.TablesUpdated != 1 || st.TablesAdded != 1 || st.TablesRemoved != 1 {
		t.Fatalf("%+v", st)
	}
	tabs, _ := c.Tables(ctx, "p", "d1")
	names := []string{}
	for _, tb := range tabs {
		names = append(names, tb.Name)
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "d" {
		t.Fatalf("%v", names)
	}
	cols, _ := c.Columns(ctx, "p", "d1", "b")
	if len(cols) != 2 {
		t.Fatalf("changed table columns replaced: %+v", cols)
	}
	// orphan columns must be gone (cascade)
	var n int
	c.db.QueryRow("SELECT COUNT(*) FROM columns").Scan(&n)
	if n != 1+2+1 {
		t.Fatalf("column rows = %d", n)
	}
}

func TestApplyUnitRemovesDatasetsAndCascades(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x")), dsApply("d2", "EU", table("d2", "b", "x")))
	st := apply(t, c, unit("p", "EU"), t0.Add(time.Hour), dsApply("d1", "EU", table("d1", "a", "x")))
	if st.DatasetsRemoved != 1 {
		t.Fatalf("%+v", st)
	}
	n, _ := c.Counts(ctx)
	if n.Datasets != 1 || n.Tables != 1 || n.Columns != 1 {
		t.Fatalf("%+v", n)
	}
}

func TestUnitsAreIndependent(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("eu_ds", "EU", table("eu_ds", "a", "x")))
	apply(t, c, unit("p", "US"), t0, dsApply("us_ds", "US", table("us_ds", "b", "x")))
	// Re-applying EU with nothing must not touch US.
	apply(t, c, unit("p", "EU"), t0.Add(time.Hour))
	n, _ := c.Counts(ctx)
	if n.Datasets != 1 {
		t.Fatalf("%+v", n)
	}
	ds, _ := c.Datasets(ctx, "p")
	if len(ds) != 1 || ds[0].Name != "us_ds" {
		t.Fatalf("%+v", ds)
	}
}

func TestKeepOldAndNamesOnly(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x", "y")))

	keep := DatasetApply{Meta: api.DatasetMeta{Project: "p", Name: "d1", Location: "EU"}, Mode: ModeKeepOld, Status: api.DatasetInaccessible}
	apply(t, c, unit("p", "EU"), t0.Add(time.Hour), keep)
	cols, _ := c.Columns(ctx, "p", "d1", "a")
	if len(cols) != 2 {
		t.Fatalf("KeepOld must preserve tables and columns: %+v", cols)
	}
	ds, _ := c.Datasets(ctx, "p")
	if ds[0].Status != api.DatasetInaccessible {
		t.Fatalf("%+v", ds)
	}
	units, _ := c.Units(ctx)
	if units[0].Inaccessible != 1 {
		t.Fatalf("%+v", units[0])
	}

	no := DatasetApply{Meta: api.DatasetMeta{Project: "p", Name: "d1", Location: "EU"}, Mode: ModeNamesOnly, Status: api.DatasetNamesOnly,
		Tables: []api.TableMeta{{Dataset: "d1", Name: "a", Type: api.TypeTable}, {Dataset: "d1", Name: "z", Type: api.TypeView}}}
	apply(t, c, unit("p", "EU"), t0.Add(2*time.Hour), no)
	cols, _ = c.Columns(ctx, "p", "d1", "a")
	if len(cols) != 0 {
		t.Fatalf("names-only replaces columns: %+v", cols)
	}
	tabs, _ := c.Tables(ctx, "p", "d1")
	if len(tabs) != 2 || !tabs[0].NamesOnly {
		t.Fatalf("%+v", tabs)
	}
	units, _ = c.Units(ctx)
	if units[0].NamesOnly != 1 {
		t.Fatalf("%+v", units[0])
	}
}

func TestShardFamilyStoredAndUpdated(t *testing.T) {
	c := openTemp(t)
	a := table("d1", "events_20240101", "x")
	a.Shard = &api.ShardFamily{Prefix: "events_", First: "20240101", Last: "20240102", Count: 2}
	b := table("d1", "events_20240102", "x")
	b.Shard = a.Shard
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", a, b))
	tabs, _ := c.Tables(ctx, "p", "d1")
	if tabs[0].Shard == nil || tabs[0].Shard.Count != 2 || tabs[0].Shard.Last != "20240102" {
		t.Fatalf("%+v", tabs[0].Shard)
	}
	// A new shard arrives: the family changes but the old tables are unchanged.
	fam := &api.ShardFamily{Prefix: "events_", First: "20240101", Last: "20240103", Count: 3}
	a2, b2, c2 := a, b, table("d1", "events_20240103", "x")
	a2.Shard, b2.Shard, c2.Shard = fam, fam, fam
	st := apply(t, c, unit("p", "EU"), t0.Add(time.Hour), dsApply("d1", "EU", a2, b2, c2))
	if st.TablesUnchanged != 2 || st.TablesAdded != 1 {
		t.Fatalf("%+v", st)
	}
	tabs, _ = c.Tables(ctx, "p", "d1")
	for _, tb := range tabs {
		if tb.Shard == nil || tb.Shard.Count != 3 {
			t.Fatalf("shard fields must refresh on unchanged tables: %+v", tb)
		}
	}
}

func TestRecordFailureKeepsSuccessAndData(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x")))
	later := t0.Add(time.Hour)
	if err := c.RecordUnitFailure(ctx, api.UnitState{UnitRef: unit("p", "EU"), Status: api.UnitInaccessible, Error: "boom", LastAttempt: &later}); err != nil {
		t.Fatal(err)
	}
	us, _ := c.Units(ctx)
	if len(us) != 1 || us[0].Status != api.UnitInaccessible || us[0].Error != "boom" {
		t.Fatalf("%+v", us)
	}
	if us[0].LastSuccess == nil || !us[0].LastSuccess.Equal(t0) || !us[0].LastAttempt.Equal(later) {
		t.Fatalf("last success must survive a failure: %+v", us[0])
	}
	if us[0].Tables != 1 {
		t.Fatalf("counts survive: %+v", us[0])
	}
	if n, _ := c.Counts(ctx); n.Tables != 1 {
		t.Fatal("data must survive")
	}
	// A project-level discovery unit that never succeeded.
	c.RecordUnitFailure(ctx, api.UnitState{UnitRef: unit("q", ""), Status: api.UnitInaccessible, Error: "nope", LastAttempt: &later})
	us, _ = c.Units(ctx)
	if len(us) != 2 || us[1].LastSuccess != nil {
		t.Fatalf("%+v", us)
	}
}

func TestRemoveUnitAndLocations(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("a", "EU", table("a", "t", "x")))
	apply(t, c, unit("p", "US"), t0, dsApply("b", "US", table("b", "t", "x")))
	locs, _ := c.Locations(ctx, "p")
	if len(locs) != 2 {
		t.Fatalf("%v", locs)
	}
	if err := c.RemoveUnit(ctx, unit("p", "US")); err != nil {
		t.Fatal(err)
	}
	locs, _ = c.Locations(ctx, "p")
	if len(locs) != 1 || locs[0] != "EU" {
		t.Fatalf("%v", locs)
	}
	if n, _ := c.Counts(ctx); n.Datasets != 1 {
		t.Fatalf("%+v", n)
	}
}

func TestPrune(t *testing.T) {
	c := openTemp(t)
	for _, p := range []string{"keep", "drop", "partial"} {
		c.ApplyUnit(ctx, ApplyRequest{Unit: unit(p, "EU"), State: okState(t0), Datasets: []DatasetApply{
			{Meta: api.DatasetMeta{Project: p, Name: "tmp_x", Location: "EU"}, Tables: []api.TableMeta{table("tmp_x", "t", "c")}},
			{Meta: api.DatasetMeta{Project: p, Name: "core", Location: "EU"}, Tables: []api.TableMeta{table("core", "keep_me", "c"), table("core", "tmp_table", "c")}},
		}})
	}
	allowed := map[string]bool{"keep": true, "partial": true}
	ignored := func(p, d, tb string) bool {
		if p == "partial" && d == "tmp_x" {
			return true
		}
		return p == "partial" && d == "core" && tb == "tmp_table"
	}
	st, err := c.Prune(ctx, allowed, ignored)
	if err != nil {
		t.Fatal(err)
	}
	if st.Projects != 1 || st.Datasets != 1 || st.Tables != 1 {
		t.Fatalf("%+v", st)
	}
	n, _ := c.Counts(ctx)
	if n.Projects != 2 || n.Datasets != 3 || n.Tables != 4 {
		t.Fatalf("%+v", n)
	}
	units, _ := c.Units(ctx)
	for _, u := range units {
		if u.Project == "drop" {
			t.Fatal("unit state of removed project must go")
		}
	}
}

func TestApplyUnitCancelledLeavesOldData(t *testing.T) {
	c := openTemp(t)
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x")))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := c.ApplyUnit(cctx, ApplyRequest{Unit: unit("p", "EU"), State: okState(t0), Datasets: []DatasetApply{dsApply("d1", "EU", table("d1", "zzz", "x"))}})
	if err == nil {
		t.Fatal("cancelled apply must fail")
	}
	tabs, _ := c.Tables(ctx, "p", "d1")
	if len(tabs) != 1 || tabs[0].Name != "a" {
		t.Fatalf("old snapshot must remain: %+v", tabs)
	}
}

func TestContentHashIgnoresShardAndColumnOrder(t *testing.T) {
	a := table("d", "t", "a", "b")
	b := a
	b.Columns = []api.ColumnMeta{a.Columns[1], a.Columns[0]}
	b.Shard = &api.ShardFamily{Prefix: "x", Count: 2}
	if a.ContentHash() != b.ContentHash() {
		t.Fatal("hash must ignore shard family and slice order")
	}
	b.Description = "changed"
	if a.ContentHash() == b.ContentHash() {
		t.Fatal("hash must see content changes")
	}
}

func TestPathsWithURICharactersStayInsideTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data#frag?q=1 %41 dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := PathsFor(dir)
	c, err := OpenCatalog(p.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	apply(t, c, unit("p", "EU"), t0, dsApply("d", "EU", table("d", "t", "c")))
	h, err := OpenHistory(p.History)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, f := range []string{p.Catalog, p.History} {
		if fi, err := os.Stat(f); err != nil || fi.Size() == 0 {
			t.Fatalf("%s must exist at the configured path: %v", f, err)
		}
	}
	// the two databases are different files with different schemas
	if _, err := h.DB().Exec("SELECT 1 FROM tables"); err == nil {
		t.Fatal("history and catalog resolved to the same file")
	}
	ro, err := OpenCatalogReadOnly(p.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if n, _ := ro.Counts(ctx); n.Tables != 1 {
		t.Fatalf("%+v", n)
	}
}

func TestConcurrentFirstOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			c, err := OpenCatalog(path)
			if err == nil {
				_, err = c.Counts(ctx)
				c.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < 12; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("simultaneous first launches must all succeed: %v", err)
		}
	}
}

func TestPruneKeepsReincludedDatasetOfIgnoredProject(t *testing.T) {
	c := openTemp(t)
	c.ApplyUnit(ctx, ApplyRequest{Unit: unit("p", "EU"), State: okState(t0), Datasets: []DatasetApply{
		{Meta: api.DatasetMeta{Project: "p", Name: "keep", Location: "EU"}, Tables: []api.TableMeta{table("keep", "t", "c")}},
		{Meta: api.DatasetMeta{Project: "p", Name: "other", Location: "EU"}, Tables: []api.TableMeta{table("other", "t", "c")}},
	}})
	// ignore = ["p", "!p.keep"]
	ignored := func(p, d, tb string) bool { return d != "keep" }
	st, err := c.Prune(ctx, map[string]bool{"p": true}, ignored)
	if err != nil || st.Projects != 0 || st.Datasets != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	ds, _ := c.Datasets(ctx, "p")
	if len(ds) != 1 || ds[0].Name != "keep" || ds[0].Tables != 1 {
		t.Fatalf("%+v", ds)
	}
}
