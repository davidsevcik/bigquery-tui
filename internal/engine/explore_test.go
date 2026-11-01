package engine

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

func (e *env) openWithTables(readOnly bool) *Engine {
	e.t.Helper()
	eng, err := Open(ctx, Options{
		Config: e.cfg, ReadOnly: readOnly,
		Meta:    func(context.Context) (bq.Metadata, error) { return e.fake, nil },
		Tables:  func(context.Context) (bq.Tables, error) { return e.fake, nil },
		Jobs:    func(context.Context) (bq.Jobs, error) { return e.fake, nil },
		Now:     func() time.Time { return e.now },
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { eng.Close() })
	return eng
}

func synced(t *testing.T, selected ...string) (*env, *Engine) {
	t.Helper()
	e := newEnv(t, selected...)
	view := bqtest.Table("v_orders")
	view.Type, view.ViewSQL = api.TypeView, "SELECT * FROM orders"
	e.fake.Projects["proj-a"].Datasets[0].Tables = append(e.fake.Projects["proj-a"].Datasets[0].Tables, view,
		bqtest.Table("events_20240101", "u"), bqtest.Table("events_20240102", "u"))
	e.fake.TableData = map[string]bqtest.TableData{
		"proj-a.sales.orders": {
			Info: bq.TableInfo{Type: api.TypeTable, Bytes: 4096, Schema: []bq.Field{{Name: "id", Type: "INTEGER"}, {Name: "amount", Type: "STRING"}}},
			Rows: [][]any{{"1", "ten"}, {"2", nil}, {"3", "a very long value indeed"}},
		},
	}
	eng := e.openWithTables(false)
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReloadIndex(ctx); err != nil {
		t.Fatal(err)
	}
	return e, eng
}

func TestSearchAndBrowseAfterSync(t *testing.T) {
	_, eng := synced(t, "proj-a", "proj-b", "proj-never")
	e2 := eng
	r, err := e2.Search(ctx, "orders", api.SearchOptions{TablesOnly: true})
	if err != nil || len(r.Results) != 2 || r.Results[0].Path.String() != "proj-a.sales.orders" {
		t.Fatalf("%+v %v", r.Results, err)
	}
	r, _ = e2.Search(ctx, "col:amount", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].ColumnHits[0].FieldPath != "amount" {
		t.Fatalf("%+v", r.Results)
	}
	ps := e2.Projects()
	if len(ps) != 3 || ps[2].Name != "proj-never" {
		t.Fatalf("%+v", ps)
	}
	// proj-never does not exist in the fake: discovery failed and says so
	if ps[2].UnitStatus != api.UnitInaccessible || ps[2].Error == "" || !ps[2].Stale {
		t.Fatalf("unsynced project carries its discovery error: %+v", ps[2])
	}
	if ds := e2.Datasets("proj-a"); len(ds) != 2 || ds[0].Name != "sales" || ds[0].Children != 4 {
		t.Fatalf("%+v", ds)
	}
	ts := e2.Tables("proj-a", "sales")
	if len(ts) != 3 || ts[0].Kind != api.KindShards || ts[0].Children != 2 {
		t.Fatalf("shards collapse in browse: %+v", ts)
	}
	if m := e2.ShardMembers("proj-a", "sales", "events_"); len(m) != 2 {
		t.Fatalf("%+v", m)
	}
}

func TestTableDetailStatsAndPreview(t *testing.T) {
	e, eng := synced(t, "proj-a")
	p := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"}
	d, err := eng.TableDetail(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Location != "EU" || len(d.Table.Columns) != 2 || d.Status != api.UnitOK || d.Stale || d.SyncedAt == nil {
		t.Fatalf("%+v", d)
	}
	v, _ := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "v_orders"})
	if v.Table.ViewSQL != "SELECT * FROM orders" || v.Table.Type != api.TypeView {
		t.Fatalf("%+v", v.Table)
	}
	if _, err := eng.TableDetail(ctx, api.ObjectPath{Project: "x", Dataset: "y", Table: "z"}); !errors.Is(err, ErrUnknownTable) {
		t.Fatalf("%v", err)
	}

	st, err := eng.TableStats(ctx, p)
	if err != nil || st.Rows != 3 || st.Bytes != 4096 {
		t.Fatalf("%+v %v", st, err)
	}
	eng.TableStats(ctx, p)
	if n := len(e.fake.CallsTo("GetTable")); n != 1 {
		t.Fatalf("stats are cached, got %d calls", n)
	}

	page, err := eng.Preview(ctx, p, 0, 2, format.Options{MaxChars: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalRows != 3 || len(page.Rows) != 2 || page.Columns[0].Name != "id" || page.Rows[0][0].Text != "1" || !page.Rows[1][1].Null {
		t.Fatalf("%+v", page)
	}
	page, _ = eng.Preview(ctx, p, 2, 20, format.Options{MaxChars: 10})
	if len(page.Rows) != 1 || page.StartIndex != 2 || !page.Rows[0][1].Truncated || page.Rows[0][1].Text != "a very lon" {
		t.Fatalf("paging and truncation: %+v", page)
	}
	if _, err := eng.Preview(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "v_orders"}, 0, 5, format.Options{}); !errors.Is(err, bq.ErrNeedsQuery) {
		t.Fatalf("views need a query: %v", err)
	}
}

// BigQuery refuses tabledata.list on a materialized view ("Cannot list a
// table of type MATERIALIZED_VIEW", seen live 2026-10-31), so it needs a query
// like a view.
func TestPreviewOfAMaterializedViewNeedsAQuery(t *testing.T) {
	e := newEnv(t, "proj-a")
	mv := bqtest.Table("mv_totals", "n")
	mv.Type = api.TypeMView
	ds := &e.fake.Projects["proj-a"].Datasets[0]
	ds.Tables = append(ds.Tables, mv)
	e.fake.TableData = map[string]bqtest.TableData{
		// the catalog says table, BigQuery now says materialized view
		"proj-a.sales.orders": {Info: bq.TableInfo{Type: api.TypeMView, Schema: []bq.Field{{Name: "id", Type: "INTEGER"}}}},
	}
	eng := e.openWithTables(false)
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReloadIndex(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mv_totals", "orders"} {
		p := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: table}
		if _, err := eng.Preview(ctx, p, 0, 5, format.Options{}); !errors.Is(err, bq.ErrNeedsQuery) {
			t.Fatalf("%s: materialized views need a query: %v", table, err)
		}
	}
	if n := len(e.fake.CallsTo("ListRows")); n != 0 {
		t.Fatalf("tabledata.list called %d times", n)
	}
}

func TestPreviewFormatsCellsLikeARun(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.TableData["proj-a.sales.orders"] = bqtest.TableData{
		Info: bq.TableInfo{Type: api.TypeTable, Schema: []bq.Field{{Name: "note", Type: "STRING"}, {Name: "tags", Type: "STRING", Mode: "REPEATED"}}},
		Rows: [][]any{{"one\ntwo", []any{map[string]any{"v": "a"}, map[string]any{"v": "b"}}}},
	}
	p := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"}
	grid, err := eng.Preview(ctx, p, 0, 1, format.Options{MaxChars: 200, ArraySummary: true})
	if err != nil {
		t.Fatal(err)
	}
	full, err := eng.Preview(ctx, p, 0, 1, format.Options{MaxChars: 1 << 20, KeepNewlines: true})
	if err != nil {
		t.Fatal(err)
	}
	if g, f := grid.Rows[0], full.Rows[0]; strings.Contains(g[0].Text, "\n") || f[0].Text != "one\ntwo" || g[1].Text == f[1].Text {
		t.Fatalf("the options reach the cells: grid %+v, record %+v", g, f)
	}
}

func TestPreviewReadsPastShortPages(t *testing.T) {
	e, eng := synced(t, "proj-a")
	var rows [][]any
	for i := range 10 {
		rows = append(rows, []any{strconv.Itoa(i + 1), "x"})
	}
	td := e.fake.TableData["proj-a.sales.orders"]
	td.Rows = rows
	e.fake.TableData["proj-a.sales.orders"] = td
	e.fake.ListRowsLimit = 3 // BigQuery cuts responses of wide rows short
	p := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"}
	page, err := eng.Preview(ctx, p, 1, 5, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 5 || page.Rows[0][0].Text != "2" || page.Rows[4][0].Text != "6" || page.StartIndex != 1 || page.TotalRows != 10 {
		t.Fatalf("the requested rows, in order: %+v", page)
	}
	before := len(e.fake.CallsTo("ListRows"))
	page, _ = eng.Preview(ctx, p, 6, 50, format.Options{})
	if len(page.Rows) != 4 || page.Rows[3][0].Text != "10" {
		t.Fatalf("the rest of the table: %+v", page)
	}
	if n := len(e.fake.CallsTo("ListRows")) - before; n != 2 {
		t.Fatalf("rows 7-10 take two calls of 3, and the total ends it without a third: %d calls", n)
	}
}

func TestPreviewOfAShardFamilyReadsTheNewestShard(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.TableData["proj-a.sales.events_20240102"] = bqtest.TableData{
		Info: bq.TableInfo{Type: api.TypeTable, Schema: []bq.Field{{Name: "u", Type: "STRING"}}},
		Rows: [][]any{{"newest"}},
	}
	page, err := eng.Preview(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "events_YYYYMMDD"}, 0, 10, format.Options{})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "newest" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestTableDetailCarriesTheRequiredPartitionFilter(t *testing.T) {
	e := newEnv(t, "proj-a")
	tb := bqtest.Table("strict", "day")
	tb.RequirePartitionFilter = true
	e.fake.Projects["proj-a"].Datasets[0].Tables = append(e.fake.Projects["proj-a"].Datasets[0].Tables, tb)
	eng := e.openWithTables(false)
	fullSync(t, eng)
	if err := eng.ReloadIndex(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "strict"})
	if err != nil || !d.Table.RequirePartitionFilter {
		t.Fatalf("%+v %v", d.Table, err)
	}
	d, _ = eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"})
	if d.Table.RequirePartitionFilter {
		t.Fatal("a table without the option")
	}
}

func TestRecentsFeedTheEmptyQuery(t *testing.T) {
	_, eng := synced(t, "proj-a")
	r, _ := eng.Search(ctx, "", api.SearchOptions{})
	if !r.Recent || len(r.Results) != 0 {
		t.Fatalf("%+v", r)
	}
	if err := eng.TouchRecent(ctx, api.ObjectPath{Project: "proj-a", Dataset: "us_data", Table: "u1"}); err != nil {
		t.Fatal(err)
	}
	r, _ = eng.Search(ctx, "", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].Path.Table != "u1" {
		t.Fatalf("%+v", r.Results)
	}
}

func TestWatchCatalogReloadsAfterAnotherProcessSyncs(t *testing.T) {
	e := newEnv(t, "proj-a")
	reader := e.openWithTables(false)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	changed := reader.WatchCatalog(wctx, 10*time.Millisecond)
	time.Sleep(40 * time.Millisecond) // let the watcher take its baseline

	writer := e.openWithTables(false) // "another process"
	h, _ := writer.StartSync(ctx, api.SyncScope{})
	h.Wait()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("the reader never saw the sync")
		}
		if r, _ := reader.Search(ctx, "orders", api.SearchOptions{TablesOnly: true}); len(r.Results) == 1 {
			return
		}
	}
}

func TestReconcilePrunesWithoutBigQuery(t *testing.T) {
	e, eng := synced(t, "proj-a", "proj-b")
	eng.DeselectProjects("proj-b")
	calls := len(e.fake.Calls)
	eng.SetConfig(e.config(`ignore = ["proj-a.us_data"]`))
	rec, skipped, err := eng.Reconcile(ctx)
	// only the ignore pattern prunes: the deselected project keeps its cache
	if err != nil || skipped || !rec.Changed || rec.Pruned.Projects != 0 || rec.Pruned.Datasets != 1 {
		t.Fatalf("%+v skipped=%v err=%v", rec, skipped, err)
	}
	if len(e.fake.Calls) != calls {
		t.Fatal("pruning must not call BigQuery")
	}
	if _, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-b", Dataset: "core", Table: "t"}); err != nil {
		t.Fatalf("the deselected project lost its cache: %v", err)
	}
	if ds := eng.Datasets("proj-a"); len(ds) != 1 {
		t.Fatalf("%+v", ds)
	}
}

func TestAutoRefresh(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.openWithTables(false)
	// empty catalog: full sync
	h, err := eng.AutoRefresh(ctx)
	if err != nil || h == nil {
		t.Fatalf("%v %v", h, err)
	}
	res, _ := h.Wait()
	if res.UnitsOK != 3 {
		t.Fatalf("%+v", res)
	}
	// fresh: nothing to do
	if h, err := eng.AutoRefresh(ctx); err != nil || h != nil {
		t.Fatalf("fresh catalog must not sync: %v %v", h, err)
	}
	// one project goes stale
	e.fake.Calls = nil
	e.now = e.now.Add(25 * time.Hour)
	h2, _ := eng.StartSync(ctx, api.SyncScope{Projects: []string{"proj-b"}})
	h2.Wait()
	e.fake.Calls = nil
	h, err = eng.AutoRefresh(ctx)
	if err != nil || h == nil {
		t.Fatalf("%v %v", h, err)
	}
	h.Wait()
	for _, c := range e.fake.CallsTo("ListDatasets") {
		if c.Project != "proj-a" {
			t.Fatalf("only the stale project is refreshed: %+v", c)
		}
	}
}

func TestSetConfigChangesTTL(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.now = e.now.Add(2 * time.Hour)
	if eng.Projects()[0].Stale {
		t.Fatal("fresh at a 24h TTL")
	}
	eng.SetConfig(e.config(`sync_ttl = "1h"`))
	if !eng.Projects()[0].Stale {
		t.Fatal("stale once the TTL drops to 1h")
	}
}

func TestAutoRefreshSyncsANewlySelectedProject(t *testing.T) {
	e := newEnv(t, "proj-a")
	eng := e.openWithTables(false)
	h, _ := eng.AutoRefresh(ctx)
	h.Wait()
	// the user selects another project: background refresh syncs just it
	if err := eng.SelectProjects("proj-b"); err != nil {
		t.Fatal(err)
	}
	e.fake.Calls = nil
	h, err := eng.AutoRefresh(ctx)
	if err != nil || h == nil {
		t.Fatalf("%v %v", h, err)
	}
	h.Wait()
	for _, c := range e.fake.CallsTo("ListDatasets") {
		if c.Project != "proj-b" {
			t.Fatalf("only the new project is synced: %+v", c)
		}
	}
	if st, _ := eng.Status(ctx); st.SyncNeeded {
		t.Fatalf("both projects are synced: %+v", st)
	}
}

func TestUnreadSyncEventsDoNotLeakThePump(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.openWithTables(false)
	h, _ := eng.StartSync(ctx, api.SyncScope{})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-h.Events():
		if ok {
			// a queued event may still be handed over; the channel must then close
			for range h.Events() {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the event channel must close once Wait has returned")
	}
}

func TestOperationsAfterCloseFailCleanly(t *testing.T) {
	e := newEnv(t, "proj-a")
	eng := e.openWithTables(false)
	eng.Close()
	if err := eng.TouchRecent(ctx, api.ObjectPath{Project: "p", Dataset: "d", Table: "t"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
}

func TestDetailRejectsAStaleIndexEntry(t *testing.T) {
	e, eng := synced(t, "proj-a")
	// the table disappears from BigQuery and the catalog, but this engine's
	// index is deliberately left stale
	e.fake.Projects["proj-a"].Datasets[1].Tables = nil
	other := e.openWithTables(false)
	h, _ := other.StartSync(ctx, api.SyncScope{})
	h.Wait()
	if _, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "us_data", Table: "u1"}); !errors.Is(err, ErrUnknownTable) {
		t.Fatalf("%v", err)
	}
}
