package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

var ctx = context.Background()

type fakeCols struct {
	cols map[int64][]api.ColumnMeta
}

func (f *fakeCols) EachColumn(_ context.Context, fn func(int64, string, string)) error {
	ids := make([]int64, 0, len(f.cols))
	for id := range f.cols {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	for _, id := range ids {
		for _, c := range f.cols[id] {
			fn(id, strings.ToLower(c.FieldPath), strings.ToLower(c.Description))
		}
	}
	return nil
}

func (f *fakeCols) ColumnsByTableID(_ context.Context, id int64) ([]api.ColumnMeta, error) {
	return f.cols[id], nil
}

type fixture struct {
	snap *store.IndexSnapshot
	cols *fakeCols
	next int64
	ds   map[string]int64
}

func newFixture() *fixture {
	return &fixture{snap: &store.IndexSnapshot{}, cols: &fakeCols{cols: map[int64][]api.ColumnMeta{}}, ds: map[string]int64{}}
}

func (f *fixture) dataset(project, name, loc, desc string) {
	found := false
	for _, p := range f.snap.Projects {
		found = found || p == project
	}
	if !found {
		f.snap.Projects = append(f.snap.Projects, project)
	}
	f.next++
	f.ds[project+"."+name] = f.next
	f.snap.Datasets = append(f.snap.Datasets, store.IndexDataset{ID: f.next, DatasetMeta: api.DatasetMeta{Project: project, Name: name, Location: loc, Description: desc}, Status: api.DatasetOK})
}

func (f *fixture) table(project, dataset, name string, typ api.TableType, desc string, cols ...string) *store.IndexTable {
	f.next++
	t := store.IndexTable{ID: f.next, DatasetID: f.ds[project+"."+dataset], TableMeta: api.TableMeta{Dataset: dataset, Name: name, Type: typ, Description: desc}}
	for _, c := range cols {
		path, d, _ := strings.Cut(c, "|")
		f.cols.cols[t.ID] = append(f.cols.cols[t.ID], api.ColumnMeta{FieldPath: path, Description: d, DataType: "STRING"})
	}
	f.snap.Tables = append(f.snap.Tables, t)
	return &f.snap.Tables[len(f.snap.Tables)-1]
}

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func (f *fixture) searcher() *Searcher {
	s := New(BuildIndex(f.snap))
	s.Columns = f.cols
	s.Now = func() time.Time { return now }
	return s
}

func standard() *fixture {
	f := newFixture()
	f.dataset("acme-prod", "sales", "EU", "Sales marts")
	f.dataset("acme-prod", "events", "EU", "")
	f.dataset("acme-prod", "tmp_scratch", "EU", "")
	f.dataset("acme-staging", "sales", "US", "")
	f.table("acme-prod", "sales", "orders", api.TypeTable, "All customer orders", "id", "customer_id", "address|Shipping address", "address.city")
	f.table("acme-prod", "sales", "order_items", api.TypeTable, "", "order_id", "sku")
	f.table("acme-prod", "sales", "reorders", api.TypeTable, "")
	f.table("acme-prod", "sales", "v_orders", api.TypeView, "")
	f.table("acme-prod", "sales", "customers", api.TypeTable, "Each row is one buyer placing orders", "id", "email")
	f.table("acme-prod", "sales", "refunds", api.TypeTable, "Money returned").Labels = map[string]string{"team": "finance"}
	for _, d := range []string{"20240101", "20240102", "20240103"} {
		f.table("acme-prod", "events", "events_"+d, api.TypeTable, "", "user_id").Shard = &api.ShardFamily{Prefix: "events_", First: "20240101", Last: "20240103", Count: 3}
	}
	f.table("acme-prod", "events", "clicks", api.TypeTable, "", "user_id", "url")
	f.table("acme-prod", "tmp_scratch", "orders_tmp", api.TypeTable, "")
	f.table("acme-staging", "sales", "orders", api.TypeTable, "", "id")
	ok := now.Add(-2 * time.Hour)
	old := now.Add(-50 * time.Hour)
	f.snap.Units = []api.UnitState{
		{UnitRef: api.UnitRef{Project: "acme-prod", Location: "EU"}, Status: api.UnitOK, LastSuccess: &ok},
		{UnitRef: api.UnitRef{Project: "acme-staging", Location: "US"}, Status: api.UnitOK, LastSuccess: &old},
	}
	return f
}

func names(t *testing.T, s *Searcher, q string, o api.SearchOptions) []string {
	t.Helper()
	r, err := s.Search(ctx, q, o)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(r.Results))
	for i, x := range r.Results {
		out[i] = x.Path.String()
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("\n got: %v\nwant: %v", got, want)
	}
}

func TestRankingTiers(t *testing.T) {
	s := standard().searcher()
	// exact (shorter path first among equals), prefix, word-boundary, substring, description
	got := names(t, s, "orders", api.SearchOptions{TablesOnly: true})
	eq(t, got,
		"acme-prod.sales.orders", "acme-staging.sales.orders", // exact
		"acme-prod.tmp_scratch.orders_tmp", // prefix
		"acme-prod.sales.v_orders",         // word boundary
		"acme-prod.sales.reorders",         // substring
		"acme-prod.sales.order_items",      // fuzzy subsequence
		"acme-prod.sales.customers",        // description only
	)
}

func TestWordBoundaryAndFuzzy(t *testing.T) {
	s := standard().searcher()
	eq(t, names(t, s, "oi", api.SearchOptions{TablesOnly: true}), "acme-prod.sales.order_items")
	got := names(t, s, "ordit", api.SearchOptions{TablesOnly: true})
	if len(got) == 0 || got[0] != "acme-prod.sales.order_items" {
		t.Fatalf("%v", got)
	}
}

func TestColumnsAreNotSearchedByDefault(t *testing.T) {
	s := standard().searcher()
	if got := names(t, s, "email", api.SearchOptions{}); len(got) != 0 {
		t.Fatalf("a column name must not match a free term: %v", got)
	}
	r, err := s.Search(ctx, "col:email", api.SearchOptions{})
	if err != nil || len(r.Results) != 1 || r.Results[0].Path.Table != "customers" {
		t.Fatalf("%+v %v", r.Results, err)
	}
	if r.Results[0].MatchedField != "column" || len(r.Results[0].ColumnHits) != 1 || r.Results[0].ColumnHits[0].FieldPath != "email" {
		t.Fatalf("%+v", r.Results[0])
	}
}

func TestColFilterMatchesPathAndDescriptionWithHits(t *testing.T) {
	f := standard()
	wide := f.table("acme-prod", "sales", "wide", api.TypeTable, "")
	for _, c := range []string{"addr_1", "addr_2", "addr_3", "addr_4", "other"} {
		f.cols.cols[wide.ID] = append(f.cols.cols[wide.ID], api.ColumnMeta{FieldPath: c})
	}
	s := f.searcher()
	r, _ := s.Search(ctx, "col:addr", api.SearchOptions{})
	if len(r.Results) != 2 {
		t.Fatalf("%+v", r.Results)
	}
	var orders, w api.SearchResult
	for _, x := range r.Results {
		if x.Path.Table == "orders" {
			orders = x
		} else {
			w = x
		}
	}
	if len(orders.ColumnHits) != 2 || orders.ColumnHits[0].Spans[0] != (api.Span{Start: 0, End: 4}) {
		t.Fatalf("%+v", orders.ColumnHits)
	}
	if len(w.ColumnHits) != 3 || w.MoreColumns != 1 {
		t.Fatalf("at most 3 hits plus a count: %+v more=%d", w.ColumnHits, w.MoreColumns)
	}
	r, _ = s.Search(ctx, "col:shipping", api.SearchOptions{})
	if len(r.Results) != 1 || len(r.Results[0].ColumnHits[0].DescSpans) != 1 {
		t.Fatalf("description match: %+v", r.Results)
	}
	// negation and combination with a term
	eq(t, names(t, s, "orders -col:id", api.SearchOptions{TablesOnly: true}),
		"acme-prod.tmp_scratch.orders_tmp", "acme-prod.sales.v_orders", "acme-prod.sales.reorders")
}

func TestFilters(t *testing.T) {
	s := standard().searcher()
	o := api.SearchOptions{TablesOnly: true}
	eq(t, names(t, s, "orders p:staging", o), "acme-staging.sales.orders")
	eq(t, names(t, s, "orders -d:tmp -p:staging type:table", o), "acme-prod.sales.orders", "acme-prod.sales.reorders", "acme-prod.sales.order_items", "acme-prod.sales.customers")
	eq(t, names(t, s, "type:view", o), "acme-prod.sales.v_orders")
	eq(t, names(t, s, "type:v,snap p:prod", o), "acme-prod.sales.v_orders")
	eq(t, names(t, s, "label:finance", o), "acme-prod.sales.refunds")
	eq(t, names(t, s, "desc:buyer", o), "acme-prod.sales.customers")
	// dataset description counts for desc:
	if got := names(t, s, "desc:marts p:prod", o); len(got) != 6 {
		t.Fatalf("%v", got)
	}
	// pending and invalid filters are ignored until complete
	eq(t, names(t, s, "refunds d: type:zzz", o), "acme-prod.sales.refunds")
}

func TestFilterOnlyOrdersByPath(t *testing.T) {
	s := standard().searcher()
	got := names(t, s, "d:sales type:table", api.SearchOptions{})
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("not in path order: %v", got)
		}
	}
	if len(got) != 6 {
		t.Fatalf("%v", got)
	}
}

func TestShardFamiliesCollapse(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "events", api.SearchOptions{TablesOnly: true})
	// the family (a name match) comes before clicks, which only matches through its dataset name
	if len(r.Results) != 2 || r.Results[1].Path.Table != "clicks" || r.Results[1].MatchedField != "dataset" {
		t.Fatalf("%+v", r.Results)
	}
	fam := r.Results[0]
	if fam.Kind != api.KindShards || fam.Name != "events_YYYYMMDD" || fam.Shard == nil || fam.Shard.Count != 3 || fam.Path.Table != "events_YYYYMMDD" {
		t.Fatalf("%+v", fam)
	}
	if sp := fam.Spans["name"]; len(sp) != 1 || sp[0] != (api.Span{Start: 0, End: 6}) {
		t.Fatalf("spans stay inside the prefix: %+v", sp)
	}
	// a query that pins one shard returns that table, not the family
	r, _ = s.Search(ctx, "events_20240102", api.SearchOptions{TablesOnly: true})
	if len(r.Results) == 0 || r.Results[0].Kind != api.KindTable || r.Results[0].Path.Table != "events_20240102" {
		t.Fatalf("%+v", r.Results)
	}
}

func TestProjectAndDatasetResults(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "sales", api.SearchOptions{})
	var kinds []string
	for _, x := range r.Results {
		kinds = append(kinds, string(x.Kind)+":"+x.Path.String())
	}
	if kinds[0] != "dataset:acme-prod.sales" || kinds[1] != "dataset:acme-staging.sales" {
		t.Fatalf("exact dataset names rank first: %v", kinds)
	}
	r, _ = s.Search(ctx, "acme", api.SearchOptions{})
	if r.Results[0].Kind != api.KindProject {
		t.Fatalf("%+v", r.Results[0])
	}
	// other filters suppress project and dataset rows
	r, _ = s.Search(ctx, "sales type:table", api.SearchOptions{})
	for _, x := range r.Results {
		if x.Kind != api.KindTable {
			t.Fatalf("%+v", x)
		}
	}
}

func TestPathLookupAndFallback(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "`acme-prod.sales.orders`", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].Tier != "exact" || r.Results[0].Path.Table != "orders" {
		t.Fatalf("%+v", r.Results)
	}
	if r, _ := s.Search(ctx, "ACME-PROD.SALES.ORDERS", api.SearchOptions{}); len(r.Results) != 1 {
		t.Fatal("path lookup is case-insensitive")
	}
	if r, _ := s.Search(ctx, "no.such.path", api.SearchOptions{}); len(r.Results) != 0 {
		t.Fatalf("%+v", r.Results)
	}
}

func TestEmptyQueryRecentsAndAll(t *testing.T) {
	s := standard().searcher()
	s.Recent = func(context.Context, int) ([]string, error) {
		return []string{"acme-prod.events.clicks", "gone.ds.tbl", "acme-prod.sales.orders"}, nil
	}
	r, _ := s.Search(ctx, "  ", api.SearchOptions{})
	if !r.Recent || len(r.Results) != 2 || r.Results[0].Path.Table != "clicks" || r.Results[0].Tier != "recent" {
		t.Fatalf("%+v", r)
	}
	r, _ = s.Search(ctx, "", api.SearchOptions{AllWhenEmpty: true, Scope: &api.ObjectPath{Project: "acme-prod", Dataset: "events"}})
	if r.Recent || len(r.Results) != 2 || r.Results[0].Path.Table != "clicks" || r.Results[1].Kind != api.KindShards {
		t.Fatalf("%+v", r.Results)
	}
}

func TestScopeLimitOffsetTotal(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "orders", api.SearchOptions{TablesOnly: true, Limit: 2, Offset: 1})
	if r.Total != 7 || len(r.Results) != 2 || r.Results[0].Path.String() != "acme-staging.sales.orders" {
		t.Fatalf("%d %+v", r.Total, r.Results)
	}
	eq(t, names(t, s, "orders", api.SearchOptions{TablesOnly: true, Scope: &api.ObjectPath{Project: "acme-staging"}}), "acme-staging.sales.orders")
}

func TestHiddenProjects(t *testing.T) {
	s := standard().searcher()
	s.Recent = func(context.Context, int) ([]string, error) {
		return []string{"acme-prod.events.clicks", "acme-staging.sales.orders"}, nil
	}
	hide := []string{"acme-prod"}
	for _, q := range []string{"orders", "acme", "sales", ""} {
		r, err := s.Search(ctx, q, api.SearchOptions{Hidden: hide})
		if err != nil || (q != "" && len(r.Results) == 0) {
			t.Fatalf("%q: %v %+v", q, err, r)
		}
		for _, x := range r.Results {
			if x.Path.Project == "acme-prod" {
				t.Fatalf("%q: hidden project in results: %+v", q, x)
			}
		}
	}
	if r, _ := s.Search(ctx, "acme-prod.sales.orders", api.SearchOptions{Hidden: hide}); len(r.Results) != 1 {
		t.Fatal("a pasted path is an exact lookup even in a hidden project")
	}
}

func TestSpansAndFreshness(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "items d:sal p:prod", api.SearchOptions{TablesOnly: true})
	x := r.Results[0]
	if x.Path.Table != "order_items" || x.MatchedField != "name" || x.Tier != "word" {
		t.Fatalf("%+v", x)
	}
	if x.Spans["name"][0] != (api.Span{Start: 6, End: 11}) || x.Spans["dataset"][0] != (api.Span{Start: 0, End: 3}) || x.Spans["project"][0] != (api.Span{Start: 5, End: 9}) {
		t.Fatalf("%+v", x.Spans)
	}
	if x.Stale || x.SyncedAt == nil {
		t.Fatalf("fresh unit: %+v", x)
	}
	r, _ = s.Search(ctx, "orders p:staging", api.SearchOptions{TablesOnly: true})
	if !r.Results[0].Stale {
		t.Fatal("a unit synced 50h ago is stale at a 24h TTL")
	}
	r, _ = s.Search(ctx, "buyer", api.SearchOptions{TablesOnly: true})
	if r.Results[0].MatchedField != "description" || len(r.Results[0].Spans["description"]) != 1 {
		t.Fatalf("%+v", r.Results[0])
	}
}

func TestCancelledContext(t *testing.T) {
	s := standard().searcher()
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Search(c, "orders", api.SearchOptions{}); err == nil {
		t.Fatal("a cancelled search must return an error")
	}
}

func TestSetIndexSwapsAndDropsColumns(t *testing.T) {
	f := standard()
	s := f.searcher()
	if r, _ := s.Search(ctx, "col:email", api.SearchOptions{}); len(r.Results) != 1 {
		t.Fatal("precondition")
	}
	g := newFixture()
	g.dataset("p", "d", "EU", "")
	g.table("p", "d", "only", api.TypeTable, "", "email")
	s.Columns = g.cols
	s.SetIndex(BuildIndex(g.snap))
	r, _ := s.Search(ctx, "col:email", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].Path.Table != "only" {
		t.Fatalf("column data must be rebuilt for the new index: %+v", r.Results)
	}
}

func TestBrowse(t *testing.T) {
	s := standard().searcher()
	ps := s.Projects()
	if len(ps) != 2 || ps[0].Name != "acme-prod" || ps[0].Children != 3 || ps[0].Stale || !ps[1].Stale {
		t.Fatalf("%+v", ps)
	}
	ds := s.Datasets("acme-prod")
	if len(ds) != 3 || ds[0].Location != "EU" {
		t.Fatalf("%+v", ds)
	}
	ts := s.Tables("acme-prod", "events")
	if len(ts) != 2 || ts[0].Name != "clicks" || ts[1].Kind != api.KindShards || ts[1].Children != 3 || ts[1].Name != "events_YYYYMMDD" {
		t.Fatalf("%+v", ts)
	}
	m := s.ShardMembers("acme-prod", "events", "events_")
	if len(m) != 3 || m[0].Name != "events_20240103" {
		t.Fatalf("newest first: %+v", m)
	}
	ref, ok := s.Lookup(api.ObjectPath{Project: "acme-prod", Dataset: "events", Table: "events_YYYYMMDD"})
	if !ok || ref.Path.Table != "events_20240103" || ref.Location != "EU" {
		t.Fatalf("a family path resolves to its newest member: %+v", ref)
	}
	if _, ok := s.Lookup(api.ObjectPath{Project: "x", Dataset: "y", Table: "z"}); ok {
		t.Fatal("unknown path")
	}
	if s.Tables("nope", "nope") != nil {
		t.Fatal("unknown dataset")
	}
}

func TestCaseSensitiveIdentity(t *testing.T) {
	f := newFixture()
	f.dataset("p", "d", "EU", "")
	upper := f.table("p", "d", "Orders", api.TypeTable, "upper", "a")
	lower := f.table("p", "d", "orders", api.TypeTable, "lower", "b")
	s := f.searcher()
	u, ok := s.Lookup(api.ObjectPath{Project: "p", Dataset: "d", Table: "Orders"})
	l, ok2 := s.Lookup(api.ObjectPath{Project: "p", Dataset: "d", Table: "orders"})
	if !ok || !ok2 || u.ID != upper.ID || l.ID != lower.ID {
		t.Fatalf("names differing only in case are different tables: %+v %+v", u, l)
	}
	// an exact pasted path picks exactly that table
	r, _ := s.Search(ctx, "p.d.Orders", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].Path.Table != "Orders" {
		t.Fatalf("%+v", r.Results)
	}
	// a wrongly cased path is ambiguous here, so it falls back to a term search
	r, _ = s.Search(ctx, "p.d.ORDERS", api.SearchOptions{})
	for _, x := range r.Results {
		if x.Tier == "exact" && x.MatchedField == "name" && len(r.Results) == 1 {
			t.Fatalf("ambiguous case-insensitive path must not pick one silently: %+v", r.Results)
		}
	}
}

func TestExpandShardsOption(t *testing.T) {
	s := standard().searcher()
	r, _ := s.Search(ctx, "d:events events_", api.SearchOptions{TablesOnly: true, ExpandShards: true})
	if len(r.Results) != 3 || r.Results[0].Kind != api.KindTable {
		t.Fatalf("%+v", r.Results)
	}
}
