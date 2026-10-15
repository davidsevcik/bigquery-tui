package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

var hbase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func addRun(t *testing.T, h *History, job, sql string, state api.RunState, source string, at time.Time, billed int64, reservation string) {
	t.Helper()
	ended := at.Add(time.Second)
	r := api.RunInfo{SQL: sql, State: state, Source: source, ExecutionProject: "exec-" + source, Created: at, Started: &at, Ended: &ended,
		Job:   &api.JobRef{Project: "exec-" + source, Location: "EU", ID: job},
		Stats: &api.RunStats{BytesBilled: billed, SlotMs: 1000, ReservationID: reservation}}
	if err := h.InsertRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func historyFixture(t *testing.T) *History {
	t.Helper()
	h, err := OpenHistory(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	addRun(t, h, "j1", "SELECT revenue\nFROM sales.orders;", api.RunDone, "tui", hbase, 100, "")
	addRun(t, h, "j2", "select revenue from sales.orders", api.RunDone, "tui", hbase.Add(time.Hour), 50, "")
	addRun(t, h, "j3", "SELECT   revenue   FROM sales.orders", api.RunDone, "cli", hbase.Add(2*time.Hour), 7, "res-1")
	addRun(t, h, "j4", "SELECT customer_id FROM sales.customers", api.RunFailed, "mcp", hbase.Add(3*time.Hour), 0, "")
	addRun(t, h, "j5", "SELECT 1", api.RunCancelled, "tui", hbase.Add(-48*time.Hour), 0, "")
	return h
}

func groupSQL(gs []api.HistoryGroup) []string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Latest.SQL)
	}
	return out
}

func TestHistorySearchGroupsIdenticalSQL(t *testing.T) {
	h := historyFixture(t)
	gs, more, err := h.SearchHistory(ctx, HistoryQuery{}, 10, 0)
	if err != nil || more {
		t.Fatal(err, more)
	}
	// "select revenue from sales.orders" differs in case, so it is its own group
	want := []string{"SELECT customer_id FROM sales.customers", "SELECT   revenue   FROM sales.orders", "select revenue from sales.orders", "SELECT 1"}
	if got := groupSQL(gs); len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("%q", got)
	}
	g := gs[1]
	if g.Runs != 2 || g.Latest.Job.ID != "j3" || g.BytesBilled != 100 || g.SlotMs != 1000 || !g.Latest.Capacity() {
		t.Fatalf("grouped: %+v", g)
	}
	if g.Latest.Started == nil || g.Latest.DurationMs == nil || *g.Latest.DurationMs != 1000 {
		t.Fatalf("times: %+v", g.Latest)
	}
	// paging
	gs, more, _ = h.SearchHistory(ctx, HistoryQuery{}, 3, 0)
	if len(gs) != 3 || !more {
		t.Fatal("first page")
	}
	gs, more, _ = h.SearchHistory(ctx, HistoryQuery{}, 3, 3)
	if len(gs) != 1 || more {
		t.Fatal("last page")
	}
}

func TestHistorySearchTermsAndFilters(t *testing.T) {
	h := historyFixture(t)
	q := func(hq HistoryQuery) []string {
		t.Helper()
		gs, _, err := h.SearchHistory(ctx, hq, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		return groupSQL(gs)
	}
	terms := func(ts ...string) []api.QueryTerm {
		var out []api.QueryTerm
		for _, s := range ts {
			out = append(out, api.QueryTerm{Text: s})
		}
		return out
	}
	if got := q(HistoryQuery{Terms: terms("rev")}); len(got) != 2 {
		t.Fatalf("prefix term: %q", got)
	}
	if got := q(HistoryQuery{Terms: terms("customer_id")}); len(got) != 1 {
		t.Fatalf("a term with punctuation is a phrase: %q", got)
	}
	if got := q(HistoryQuery{Terms: terms("revenue", "customers")}); len(got) != 0 {
		t.Fatalf("terms AND: %q", got)
	}
	if got := q(HistoryQuery{Terms: terms(`"`, "*")}); len(got) != 4 {
		t.Fatalf("punctuation-only terms are ignored: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "state", Values: []string{"failed", "cancelled"}}}}); len(got) != 2 {
		t.Fatalf("state: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "state", Values: []string{"done"}, Negated: true}}}); len(got) != 2 {
		t.Fatalf("negated state: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "source", Values: []string{"cli"}}}}); len(got) != 1 {
		t.Fatalf("source: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "project", Values: []string{"exec-m"}}}}); len(got) != 1 {
		t.Fatalf("project: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "project", Values: []string{"%"}}}}); len(got) != 0 {
		t.Fatalf("project values are literal: %q", got)
	}
	if got := q(HistoryQuery{Conds: []HistoryCond{{Field: "since", Since: hbase.Add(-time.Minute)}}}); len(got) != 3 {
		t.Fatalf("since: %q", got)
	}
	// a filtered group counts only the matching Runs
	gs, _, _ := h.SearchHistory(ctx, HistoryQuery{Conds: []HistoryCond{{Field: "source", Values: []string{"tui"}}}, Terms: terms("revenue")}, 10, 0)
	if len(gs) != 2 || gs[1].Runs != 1 {
		t.Fatalf("%+v", gs)
	}
	if sp := gs[0].Spans; len(sp) != 1 || gs[0].Latest.SQL[sp[0].Start:sp[0].End] != "revenue" {
		t.Fatalf("spans %+v", sp)
	}
	if _, _, err := h.SearchHistory(ctx, HistoryQuery{Conds: []HistoryCond{{Field: "nope"}}}, 10, 0); err == nil {
		t.Fatal("an unknown field is an error")
	}
}

func TestHistoryEntryFavoriteAndPrune(t *testing.T) {
	h := historyFixture(t)
	gs, _, _ := h.SearchHistory(ctx, HistoryQuery{}, 10, 0)
	id := gs[0].Latest.ID
	if err := h.SetFavoritePath(ctx, id, "sales/customers.sql"); err != nil {
		t.Fatal(err)
	}
	e, err := h.HistoryEntry(ctx, id)
	if err != nil || e.FavoritePath != "sales/customers.sql" || e.State != api.RunFailed || e.Source != "mcp" {
		t.Fatalf("%+v %v", e, err)
	}
	if err := h.SetFavoritePath(ctx, 999, "x.sql"); err == nil {
		t.Fatal("an unknown entry is an error")
	}
	n, err := h.Prune(ctx, hbase)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	var fts int
	h.DB().QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH '"1"'`).Scan(&fts)
	if fts != 0 {
		t.Fatal("the search index follows the delete")
	}
}

func TestHistoryRunFavoriteAndParamsRoundTrip(t *testing.T) {
	h := historyFixture(t)
	at := hbase.Add(5 * time.Hour)
	r := api.RunInfo{SQL: "SELECT @n", State: api.RunDone, Source: "tui", Favorite: "dau.sql", Created: at,
		Params: []api.QueryParam{{Name: "n", Type: "INT64", Value: "3"}}, Job: &api.JobRef{Project: "p", Location: "EU", ID: "jp"}}
	if err := h.InsertRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	gs, _, _ := h.SearchHistory(ctx, HistoryQuery{}, 1, 0)
	e := gs[0].Latest
	if e.FavoritePath != "dau.sql" || len(e.Params) != 1 || e.Params[0].Type != "INT64" {
		t.Fatalf("%+v", e)
	}
}

// Rows written by the v1 schema get their grouping key on open.
func TestHistoryKeysAreFilledAfterMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	h, err := openHistory(path, historyMigrations[:1], time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"SELECT  1", "SELECT 1;"} {
		if _, err := h.DB().Exec(`INSERT INTO history(project,location,job_id,execution_project,sql,state,created_at,source) VALUES('p','EU',?,'exec',?,'done',?,'tui')`, i, s, i); err != nil {
			t.Fatal(err)
		}
	}
	h.Close()
	h, err = OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	gs, _, err := h.SearchHistory(ctx, HistoryQuery{}, 10, 0)
	if err != nil || len(gs) != 1 || gs[0].Runs != 2 {
		t.Fatalf("%+v %v", gs, err)
	}
}

// Codex review 4, finding 7: whitespace inside literals and comments is
// part of the query.
func TestNormalizeSQL(t *testing.T) {
	same := [][2]string{
		{"SELECT  1;\n", "SELECT 1"},
		{"select\ta\nfrom t ;", "select a from t"},
		{"select 'it''s'  ,  x", "select 'it''s' , x"},
	}
	for _, c := range same {
		if SQLKey(c[0]) != SQLKey(c[1]) {
			t.Errorf("%q and %q should group: %q %q", c[0], c[1], NormalizeSQL(c[0]), NormalizeSQL(c[1]))
		}
	}
	differ := [][2]string{
		{"SELECT 'a  b'", "SELECT 'a b'"},
		{"SELECT \"a  b\"", "SELECT \"a b\""},
		{"SELECT `a  b`", "SELECT `a b`"},
		{"SELECT 'a\\'  b'", "SELECT 'a\\' b'"},
		{"SELECT 1 -- a  b\n", "SELECT 1 -- a b\n"},
		{"SELECT /* a  b */ 1", "SELECT /* a b */ 1"},
	}
	for _, c := range differ {
		if SQLKey(c[0]) == SQLKey(c[1]) {
			t.Errorf("%q and %q must not group (%q)", c[0], c[1], NormalizeSQL(c[0]))
		}
	}
	if NormalizeSQL("SELECT 1 -- note;") != "SELECT 1 -- note;" {
		t.Error("a trailing ; inside a comment stays")
	}
}

// Codex review 4, finding 6: a group shows its newest Run even when an
// older one finished (and was recorded) later.
func TestHistoryGroupShowsTheNewestRun(t *testing.T) {
	h := historyFixture(t)
	addRun(t, h, "slow", "select z", api.RunFailed, "tui", hbase.Add(10*time.Hour), 0, "")
	addRun(t, h, "fast", "select z", api.RunDone, "tui", hbase.Add(11*time.Hour), 0, "")
	addRun(t, h, "older-but-later", "select z", api.RunCancelled, "tui", hbase.Add(9*time.Hour), 0, "")
	gs, _, err := h.SearchHistory(ctx, HistoryQuery{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gs[0].Latest.Job.ID != "fast" || gs[0].Runs != 3 || gs[0].Key == "" {
		t.Fatalf("%+v", gs[0])
	}
}
