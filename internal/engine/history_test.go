package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
)

func TestHistorySearchPromoteAndReopen(t *testing.T) {
	e, eng := synced(t, `["proj-a"]`)
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(30) }
	eng.StartRun(ctx, runs.Request{SQL: "select n from orders where d >= @start",
		Params: []api.QueryParam{{Name: "start", Type: "DATE", Value: "2024-01-01"}, {Name: "x", Null: true, Type: "STRING"}}}).Wait()
	e.now = e.now.Add(time.Minute)
	eng.StartRun(ctx, runs.Request{SQL: "select  n from orders where d >= @start"}).Wait()
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{JobErr: bqtest.QueryError("boom")} }
	eng.StartRun(ctx, runs.Request{SQL: "select broken"}).Wait()

	page, err := eng.SearchHistory(ctx, "orders", 10, 0)
	if err != nil || len(page.Groups) != 1 || page.Groups[0].Runs != 2 || len(page.Query.Terms) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	if page, _ := eng.SearchHistory(ctx, "status:failed", 10, 0); len(page.Groups) != 1 || page.Groups[0].Latest.Error == "" {
		t.Fatalf("%+v", page)
	}
	if page, _ := eng.SearchHistory(ctx, "since:30s", 10, 0); len(page.Groups) != 2 || page.Query.Filters[0].Status != api.FilterInvalid {
		t.Fatal("since: takes no seconds, so the filter is invalid and ignored")
	}
	latest := page.Groups[0].Latest

	// promote: the parameters are declared with the values they ran with
	e.cfg.FavoritesDir = filepath.Join(e.dir, "favs")
	id, err := eng.PromoteHistory(ctx, latest.ID, "sales/orders by day", "Orders since a date", []string{"sales"}, false)
	if err != nil || id != "sales/orders by day.sql" {
		t.Fatal(id, err)
	}
	f, err := eng.ReadFavorite(id)
	if err != nil || f.Description != "Orders since a date" || len(f.Params) != 0 || !strings.HasPrefix(f.SQL, "select  n") {
		t.Fatalf("the latest run had no parameters: %+v %v", f, err)
	}
	h, _ := eng.History()
	var firstID int64
	h.DB().QueryRow("SELECT MIN(id) FROM history").Scan(&firstID)
	id2, err := eng.PromoteHistory(ctx, firstID, "dated", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = eng.ReadFavorite(id2)
	if len(f.Params) != 1 || f.Params[0].Type != "DATE" || *f.Params[0].Default != "2024-01-01" {
		t.Fatalf("%+v", f.Params)
	}
	if entry, _ := eng.HistoryEntry(ctx, firstID); entry.FavoritePath != "dated.sql" {
		t.Fatalf("the Favorite is written back: %+v", entry)
	}
	if _, err := eng.PromoteHistory(ctx, firstID, "dated", "", nil, false); !IsFavoriteExists(err) {
		t.Fatalf("%v", err)
	}
	if eng.FavoriteFromID(filepath.Join(e.cfg.FavoritesDir, "dated.sql")) != "dated.sql" || eng.FavoriteFromID(filepath.Join(e.dir, "x.sql")) != "" {
		t.Fatal("FavoriteFromID")
	}
	specs := eng.QueryParams(f.Text, f.SQL)
	if len(specs) != 1 || specs[0].Type != "DATE" {
		t.Fatalf("%+v", specs)
	}

	// reopen the result of a History entry
	id3, err := eng.ReopenResult(ctx, latest.ID, "b1")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := eng.RunRows(ctx, id3, 0, 5, format.Options{})
	if err != nil || len(rows.Rows) != 5 {
		t.Fatalf("%v %v", rows, err)
	}
	failed, _ := eng.SearchHistory(ctx, "status:failed", 1, 0)
	if _, err := eng.ReopenResult(ctx, failed.Groups[0].Latest.ID, ""); err == nil {
		t.Fatal("a failed run has no result")
	}
	if n, _ := h.RunCount(ctx); n != 3 {
		t.Fatalf("reopening records nothing: %d", n)
	}
}

func TestHistoryRetention(t *testing.T) {
	e, eng := synced(t, `["proj-a"]`)
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	eng.StartRun(ctx, runs.Request{SQL: "select old"}).Wait()
	e.now = e.now.Add(10 * 24 * time.Hour)
	eng.StartRun(ctx, runs.Request{SQL: "select new"}).Wait()
	h, _ := eng.History()
	if n, _ := h.RunCount(ctx); n != 2 {
		t.Fatal("retention is off by default")
	}
	cfg := *e.cfg
	cfg.HistoryRetentionDays = 7
	eng.SetConfig(&cfg)
	h, _ = eng.History()
	if n, _ := h.RunCount(ctx); n != 1 {
		t.Fatalf("the old entry is pruned: %d", n)
	}
	if n, err := eng.PruneHistory(ctx, e.now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("manual prune: %d %v", n, err)
	}
}

func TestRunCarriesItsFavorite(t *testing.T) {
	e, eng := synced(t, `["proj-a"]`)
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	eng.StartRun(ctx, runs.Request{SQL: "select 1", Favorite: "a/b.sql"}).Wait()
	page, _ := eng.SearchHistory(ctx, "", 1, 0)
	if page.Groups[0].Latest.FavoritePath != "a/b.sql" {
		t.Fatalf("%+v", page.Groups[0].Latest)
	}
}
