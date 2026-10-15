package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

func (r *rig) favDir() string { return r.m.be.Config().FavoritesDir }

func (r *rig) writeFav(id, text string) {
	r.t.Helper()
	p := filepath.Join(r.favDir(), id)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// runSQL replaces the buffer's text and runs it (plain editing).
func (r *rig) runSQL(sql string) {
	r.t.Helper()
	r.press("ctrl+x", "h")
	r.typeText(sql)
	r.press("ctrl+r")
}

func TestHistoryPickerSearchesLoadsAndShowsResults(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(sql string) bqtest.QueryScript {
		if strings.Contains(sql, "broken") {
			return bqtest.QueryScript{JobErr: bqtest.QueryError("boom")}
		}
		return bqtest.Numbers(7)
	}
	r.runSQL("select n from orders where d = @d")
	r.typeText("DATE:2024-01-01")
	r.press("enter")
	r.runSQL("select broken")
	r.runSQL("select  n from orders where d = @d")
	r.press("enter") // the remembered value

	r.press("ctrl+x", "r")
	if r.m.ov != ovPicker || r.m.pk.scope != scopeHistory {
		t.Fatal("ctrl+x r opens History")
	}
	r.contains("Favorites 0", "History 2", "✗", "select broken", "×2", "latest of 2 runs", "@d DATE = 2024-01-01", "tab switches")
	r.golden("picker_history_120x36")
	r.typeText("status:fail")
	r.contains("[status:fail]")
	if len(r.m.pk.hist) != 1 || r.m.pk.hist[0].Latest.State != api.RunFailed {
		t.Fatalf("%+v", r.m.pk.hist)
	}
	for range "status:fail" {
		r.press("backspace")
	}
	r.typeText("orders")
	if len(r.m.pk.hist) != 1 {
		t.Fatal("the term matches the SQL")
	}
	// show the result again: a new Run, not recorded twice
	before := historyCount(t, r)
	r.press("ctrl+r")
	if r.m.zone != ZoneResults || r.m.grid() == nil || r.m.grid().total != 7 {
		t.Fatalf("zone %s", r.m.zone)
	}
	if historyCount(t, r) != before {
		t.Fatal("reopening is not a new History entry")
	}
	// load the SQL into the editor: the buffer has text, so it asks
	r.press("ctrl+x", "r")
	if r.m.pk.in.value() != "orders" {
		t.Fatal("the search survives closing the picker")
	}
	r.press("enter")
	if r.m.ov != ovConfirm {
		t.Fatal("unsaved text: asks first")
	}
	r.contains("Replace the text of query 1")
	r.press("n") // into a new buffer
	if len(r.m.bufs) != 2 || r.text() != "select  n from orders where d = @d" || r.m.buf().params["d"] != "DATE:2024-01-01" {
		t.Fatalf("%d buffers, %q, %v", len(r.m.bufs), r.text(), r.m.buf().params)
	}
	r.press("ctrl+x", "r", "enter", "y") // replace this one
	if len(r.m.bufs) != 2 || r.m.cur != 1 {
		t.Fatalf("replaced in place: %d buffers, cur %d", len(r.m.bufs), r.m.cur)
	}
	// a failed run has no result to show
	r.press("ctrl+x", "r")
	for range "orders" {
		r.press("backspace")
	}
	r.typeText("broken")
	r.press("ctrl+r")
	r.contains("has no result")
}

func TestSaveBufferAsFavoriteAndTypedParameters(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
	r.typeText("-- param: start DATE = 2024-01-01\nselect n from t where d >= @start and x = @x")
	r.press("ctrl+x", "s")
	if r.m.ov != ovPrompt || r.m.prompt.kind != "savefav" {
		t.Fatal("ctrl+x s asks for the favorite's name")
	}
	r.typeText("sales/daily")
	r.press("down")
	r.typeText("Daily numbers")
	r.press("down")
	r.typeText("sales, daily")
	r.press("enter")
	want := filepath.Join(r.favDir(), "sales", "daily.sql")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "-- description: Daily numbers\n-- tags: sales, daily\n-- param: start DATE = 2024-01-01\nselect n") {
		t.Fatalf("%q", data)
	}
	b := r.m.buf()
	if b.path != want || b.modified() || !strings.HasPrefix(r.text(), "-- description: Daily numbers") {
		t.Fatalf("the buffer is the favorite now: %q %v %q", b.path, b.modified(), r.text())
	}
	if _, err := os.Stat(r.m.draftPath(b.id)); !os.IsNotExist(err) {
		t.Fatal("no draft for a file buffer")
	}
	// running it: the header types @start, @x is guessed
	r.press("ctrl+r")
	if r.m.ov != ovPrompt || r.m.prompt.fields[0].value() != "2024-01-01" {
		t.Fatalf("the default is prefilled: %+v", r.m.prompt)
	}
	r.contains("DATE", "types come from the -- param: header")
	r.press("ctrl+u")
	r.typeText("soon")
	r.press("down")
	r.typeText("42")
	r.press("enter")
	r.contains(`"soon" is not a valid DATE`)
	if r.m.ov != ovPrompt || r.m.prompt.idx != 0 {
		t.Fatal("an invalid value keeps the prompt on that field")
	}
	r.press("ctrl+u")
	r.typeText("2024-02-01")
	r.press("enter")
	calls := r.fake.QueryCalls()
	ps := calls[len(calls)-1].Req.Params
	if len(ps) != 2 || ps[0] != (api.QueryParam{Name: "start", Type: "DATE", Value: "2024-02-01"}) || ps[1].Type != "INT64" {
		t.Fatalf("%+v", ps)
	}
	page, _ := r.m.be.SearchHistory(t.Context(), "", 1, 0)
	if page.Groups[0].Latest.FavoritePath != "sales/daily.sql" {
		t.Fatalf("the Run carries its favorite: %+v", page.Groups[0].Latest)
	}
	// saving again under the same name overwrites without asking
	r.press("ctrl+x", "s")
	if r.m.prompt.fields[0].value() != "sales/daily" || r.m.prompt.fields[2].value() != "sales, daily" {
		t.Fatal("prefilled from the favorite")
	}
	r.press("down")
	r.press("ctrl+u")
	r.typeText("Daily")
	r.press("enter")
	if r.m.ov != ovNone {
		t.Fatal(r.m.prompt.note)
	}
	// another name that exists asks once
	r.writeFav("taken.sql", "select 0\n")
	r.press("ctrl+x", "s", "ctrl+u")
	r.typeText("taken")
	r.press("enter")
	r.contains("press enter again to overwrite")
	r.press("enter")
	if data, _ := os.ReadFile(filepath.Join(r.favDir(), "taken.sql")); !strings.Contains(string(data), "@start") {
		t.Fatal("overwritten after the second enter")
	}
}

func TestFavoritesPickerOpenRenameDeleteAndPromote(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
	r.writeFav("events/dau.sql", "-- description: Daily active users\n-- tags: events\nselect count(*) from sessions\n")
	r.writeFav("finance/revenue.sql", "-- param: n INTEGER\nselect revenue\n")
	r.press("ctrl+x", "f")
	if r.m.ov != ovPicker || r.m.pk.scope != scopeFavorites {
		t.Fatal("ctrl+x f opens Favorites")
	}
	r.contains("Favorites 2", "events/dau.sql", "finance/revenue.sql", "Daily active users", "⚠")
	r.golden("picker_favorites_120x36")
	r.press("down")
	r.contains("problem", "unknown type INTEGER")
	r.press("up")
	r.typeText("dau")
	if len(r.m.pk.fm) != 1 {
		t.Fatal("filtered")
	}
	r.press("enter") // the buffer is empty: no question
	if r.m.ov != ovNone || len(r.m.bufs) != 1 || !strings.HasSuffix(r.m.buf().path, filepath.Join("events", "dau.sql")) || r.m.zone != ZoneEditor {
		t.Fatalf("%d buffers, %q", len(r.m.bufs), r.m.buf().path)
	}
	r.press("ctrl+x", "f", "enter")
	r.contains("is already open")
	// rename: the open buffer follows
	r.press("ctrl+x", "f", "ctrl+r", "ctrl+u")
	r.typeText("metrics/dau")
	r.press("enter")
	if r.m.ov != ovPicker || !strings.HasSuffix(r.m.buf().path, filepath.Join("metrics", "dau.sql")) {
		t.Fatalf("back in the picker, buffer path %q", r.m.buf().path)
	}
	if _, err := os.Stat(filepath.Join(r.favDir(), "events")); !os.IsNotExist(err) {
		t.Fatal("the empty folder is gone")
	}
	// delete asks; n keeps it
	r.press("ctrl+d")
	r.contains("Delete the favorite metrics/dau.sql?")
	r.press("n")
	if r.m.ov != ovPicker {
		t.Fatal("back in the picker")
	}
	r.press("ctrl+d", "y")
	if _, err := os.Stat(filepath.Join(r.favDir(), "metrics", "dau.sql")); !os.IsNotExist(err) || len(r.m.pk.favs) != 1 {
		t.Fatal("deleted")
	}
	r.press("esc")
	// promote a History entry
	r.press("ctrl+x", "n")
	r.runSQL("select @k")
	r.typeText("7")
	r.press("enter")
	r.press("ctrl+x", "r", "ctrl+s")
	r.typeText("kept")
	r.press("down")
	r.typeText("A kept query")
	r.press("enter")
	data, err := os.ReadFile(filepath.Join(r.favDir(), "kept.sql"))
	if err != nil || string(data) != "-- description: A kept query\n-- param: k INT64 = 7\nselect @k\n" {
		t.Fatalf("%q %v", data, err)
	}
	if r.m.ov != ovPicker {
		t.Fatal("back in the picker")
	}
	r.press("tab")
	if r.m.pk.scope != scopeFavorites || len(r.m.pk.fm) != 2 {
		t.Fatalf("the new favorite is listed: %d", len(r.m.pk.fm))
	}
}

func TestPickerOpensFromVimAndExplorer(t *testing.T) {
	r := vimRig(t, 100, 30)
	r.typeText(" f")
	if r.m.ov != ovPicker || r.m.pk.scope != scopeFavorites {
		t.Fatal("space f")
	}
	r.contains("no favorites yet")
	r.press("esc")
	r.typeText(" h")
	if r.m.ov != ovPicker || r.m.pk.scope != scopeHistory {
		t.Fatal("space h")
	}
	r.typeText("q?") // letters type into the search
	if r.m.pk.in.value() != "q?" || r.m.ov != ovPicker {
		t.Fatal("printable keys are search text")
	}
	r.press("esc", "ctrl+w", "h", "ctrl+x", "r")
	if r.m.ov != ovPicker || r.m.zone != ZoneExplorer {
		t.Fatal("ctrl+x r from the Explorer")
	}
	r.golden("picker_empty_100x30")
}
