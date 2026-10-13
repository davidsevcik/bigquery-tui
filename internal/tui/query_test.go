package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// editorRig opens the UI with the editor focused.
func editorRig(t *testing.T, w, h int) *rig {
	t.Helper()
	r := newRig(t, w, h)
	r.press("f6")
	if r.m.zone != ZoneEditor {
		t.Fatalf("F6 goes to the editor, got %s", r.m.zone)
	}
	return r
}

func (r *rig) text() string { return r.m.buf().ed.text() }

func historyCount(t *testing.T, r *rig) int {
	t.Helper()
	h, err := r.eng.History()
	if err != nil {
		t.Fatal(err)
	}
	n, err := h.RunCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestZonesCycleAndChords(t *testing.T) {
	r := newRig(t, 120, 30)
	for _, want := range []Zone{ZoneEditor, ZoneResults, ZoneExplorer} {
		r.press("f6")
		if r.m.zone != want {
			t.Fatalf("zone %s, want %s", r.m.zone, want)
		}
	}
	r.press("shift+f6")
	if r.m.zone != ZoneResults {
		t.Fatalf("shift+F6 goes back: %s", r.m.zone)
	}
	r.press("ctrl+w", "k")
	if r.m.zone != ZoneEditor {
		t.Fatalf("ctrl+w k: %s", r.m.zone)
	}
	r.press("ctrl+w", "j")
	if r.m.zone != ZoneResults {
		t.Fatalf("ctrl+w j: %s", r.m.zone)
	}
	r.press("ctrl+w", "h")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("ctrl+w h: %s", r.m.zone)
	}
	r.press("ctrl+w", "l")
	if r.m.zone != ZoneEditor {
		t.Fatalf("ctrl+w l: %s", r.m.zone)
	}
	// printable keys type in the editor, including the explorer's commands
	r.typeText("q L ? /")
	if r.text() != "q L ? /" || r.m.zone != ZoneEditor {
		t.Fatalf("%q", r.text())
	}
}

func TestCompletionPopup(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.typeText("select * from `acme-p")
	if !r.m.comp.open || r.m.comp.cands[0].Label != "acme-prod" {
		t.Fatalf("popup after a backtick path: %+v", r.m.comp)
	}
	r.contains("acme-prod", "tab accept")
	r.press("tab") // project: the dot is added and the datasets follow
	if r.text() != "select * from `acme-prod." || !r.m.comp.open {
		t.Fatalf("%q open=%v", r.text(), r.m.comp.open)
	}
	r.press("down", "down", "tab") // events, finance, sales
	if r.text() != "select * from `acme-prod.sales." {
		t.Fatalf("%q", r.text())
	}
	r.typeText("ord")
	r.press("tab") // order_items sorts first: move to orders
	if !strings.HasSuffix(r.text(), "order_items`") {
		t.Fatalf("a table closes the backtick: %q", r.text())
	}
	r.press("ctrl+z")
	r.typeText("ers")
	if r.text() != "select * from `acme-prod.sales.orders" {
		t.Fatalf("%q", r.text())
	}

	// enter never accepts: it is a new line
	r.typeText("` o\nwhere o.cu")
	if !r.m.comp.open {
		t.Fatal("columns after alias dot")
	}
	r.press("enter")
	if !strings.HasSuffix(r.text(), "o.cu\n") || r.m.comp.open {
		t.Fatalf("%q", r.text())
	}
	r.press("backspace")
	// esc dismisses; ctrl+space brings it back; ctrl+p navigates while open
	r.typeText("s")
	r.press("backspace")
	if !r.m.comp.open {
		t.Fatal("popup follows typing")
	}
	r.press("esc")
	if r.m.comp.open || r.m.zone != ZoneEditor {
		t.Fatal("esc dismisses only the popup")
	}
	r.press("ctrl+space")
	if !r.m.comp.open {
		t.Fatal("ctrl+space forces completion")
	}
	r.press("ctrl+p")
	if r.m.ov != ovNone {
		t.Fatal("ctrl+p belongs to the popup while it is open")
	}
	r.press("tab")
	if !strings.HasSuffix(r.text(), "where o.customer_id") {
		t.Fatalf("%q", r.text())
	}
	r.press("ctrl+p")
	if r.m.ov != ovPalette {
		t.Fatal("ctrl+p opens the palette when the popup is closed")
	}
}

func TestRunWithParametersShowsResults(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1234) }
	r.typeText("select n, label from t where n > @min and d = @day")
	r.press("ctrl+r")
	r.contains("Query parameters", "@min", "@day")
	r.typeText("10")
	r.contains("INT64")
	r.press("tab")
	r.typeText("2024-02-29")
	r.contains("DATE")
	r.press("enter")
	r.contains("#1 done", "1,234 rows", "row 1 of 1,234", "row 0", "on-demand")
	calls := r.fake.QueryCalls()
	last := calls[len(calls)-1].Req
	if len(last.Params) != 2 || last.Params[0] != (api.QueryParam{Name: "min", Type: "INT64", Value: "10"}) || last.Params[1].Type != "DATE" {
		t.Fatalf("%+v", last.Params)
	}
	if historyCount(t, r) != 1 {
		t.Fatal("the run is in History")
	}
	// the values are remembered for the next run of this buffer
	r.press("ctrl+r")
	r.contains("2024-02-29")
	r.press("esc")

	// results: cursor, paging across the 500-row page, record view
	r.press("f6", "G")
	r.contains("row 1,234 of 1,234", "row 1233")
	r.press("g", "g", "l", "enter")
	r.contains("Record 1 of 1,234", "label", "row 0")
	r.press("n", "n", "p")
	r.contains("Record 2 of 1,234", "row 1")
	r.press("esc")
	if g := r.m.grid(); g.row != 1 || g.col != 1 {
		t.Fatalf("the grid follows the record view: %d,%d", g.row, g.col)
	}
	r.press("y")
	r.contains("copied the cell")
	r.press("Y")
	r.contains("copied the row as JSON")
	r.press("g", "t")
	r.contains("as TSV")
}

func TestQueryErrorJumpsToThePosition(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Unrecognized name: nope at [2:3]")}
	}
	r.typeText("select 1;\n\nselect\nnope from t")
	// run only the second statement: the position is relative to the selection
	r.press("up", "home", "home", "shift+down", "shift+end", "ctrl+r")
	ed := r.m.buf().ed
	if ed.errLine != 4 || ed.errCol != 3 || ed.row != 3 || ed.col != 2 {
		t.Fatalf("error at %d:%d, cursor %d:%d", ed.errLine, ed.errCol, ed.row, ed.col)
	}
	r.contains("line 4, column 3", "Unrecognized name: nope", "✗", "#1 failed")
	if historyCount(t, r) != 0 {
		t.Fatal("a failed dry-run is not History")
	}
	r.typeText("x")
	if ed.errLine != 0 {
		t.Fatal("typing clears the mark")
	}
}

func TestDryRunOnlyEstimates(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{Bytes: 3 << 30, ReservationID: "admin:EU.res", Edition: "STANDARD"}
	}
	r.typeText("select 1")
	r.press("ctrl+d")
	r.contains("dry-run: 3.0 GiB scanned (not billed)", "capacity: EU.res (Standard)")
	if len(r.fake.CallsTo("StartQuery")) != 0 || historyCount(t, r) != 0 {
		t.Fatal("a dry-run starts nothing")
	}
	r.lacks("$")
}

func TestConfirmationForNonSelectAndLargeScans(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DROP_TABLE"} }
	r.typeText("drop table x")
	r.press("ctrl+r")
	if r.m.ov != ovConfirm {
		t.Fatal("a non-SELECT statement asks first")
	}
	r.contains("DROP_TABLE statement, not a SELECT")
	r.press("n")
	r.settle()
	if len(r.fake.CallsTo("StartQuery")) != 0 {
		t.Fatal("declined: nothing ran")
	}
	r.contains("cancelled")
	r.press("ctrl+r", "y")
	r.settle()
	r.contains("#2 done", "statement finished")
	if len(r.fake.CallsTo("StartQuery")) != 1 {
		t.Fatal("confirmed: it ran")
	}

	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(2); s.Bytes = 20 << 30; return s }
	r.press("ctrl+r")
	r.contains("scans 20.0 GiB ≈ $0.12", "over the confirmation threshold")
	r.press("enter")
	r.settle()
	r.contains("#3 done", "2 rows")
}

func TestCancelRunningRunAndQuitAsks(t *testing.T) {
	r := editorRig(t, 120, 30)
	block := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	r.typeText("select slow")
	r.press("ctrl+r")
	r.contains("running… ctrl+c cancels", "▶ 1 run(s)")
	r.press("ctrl+q")
	if r.m.ov != ovConfirm {
		t.Fatal("quitting with a run in flight asks")
	}
	r.contains("1 run(s) are still in flight")
	r.press("n")
	r.press("ctrl+c")
	r.contains("cancel requested for run #1 (it may still be billed)")
	r.settle()
	r.contains("#1 cancelled")
	if len(r.fake.CallsTo("CancelJob")) != 1 {
		t.Fatal("the job was not cancelled")
	}
	r.press("ctrl+c")
	r.contains("nothing is running")
	close(block)
}

func TestBuffersAndRunList(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(sql string) bqtest.QueryScript {
		if strings.Contains(sql, "two") {
			return bqtest.Numbers(22)
		}
		return bqtest.Numbers(11)
	}
	r.typeText("select one")
	r.press("ctrl+r")
	r.contains("11 rows")
	r.press("ctrl+x", "n")
	if len(r.m.bufs) != 2 || r.text() != "" {
		t.Fatal("ctrl+x n opens an empty buffer")
	}
	r.contains("[2/2]", "no run yet")
	r.typeText("select two")
	r.press("ctrl+r")
	r.contains("22 rows")
	r.press("ctrl+x", "[")
	r.contains("[1/2]", "11 rows", "select one")
	r.press("ctrl+x", "]")
	r.contains("[2/2]", "22 rows")

	// the run list is global; enter shows a run and switches to its buffer
	r.press("ctrl+x", "l")
	r.contains("Runs", "#2", "#1", "select one", "query 1")
	r.press("down", "enter")
	if r.m.cur != 0 || r.m.zone != ZoneResults {
		t.Fatalf("buffer %d zone %s", r.m.cur, r.m.zone)
	}
	r.contains("#1 done", "11 rows")

	// closing a buffer with text asks; an empty one does not
	r.press("f6", "f6", "ctrl+x", "w")
	if r.m.ov != ovConfirm {
		t.Fatalf("zone %s ov %v", r.m.zone, r.m.ov)
	}
	r.press("n")
	if len(r.m.bufs) != 2 {
		t.Fatal("declined")
	}
	r.press("ctrl+x", "w", "y")
	if len(r.m.bufs) != 1 || r.text() != "select two" {
		t.Fatalf("%d buffers, %q", len(r.m.bufs), r.text())
	}
	r.press("ctrl+x", "b")
	r.contains("Buffers", "query 2", "select two")
}

func TestDraftsSurviveAndAreOfferedForRestore(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.typeText("select 'kept'")
	dir := r.m.be.DraftDir()
	files, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	if len(files) != 1 {
		t.Fatalf("the buffer autosaves to a draft: %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "select 'kept'" {
		t.Fatalf("%q", b)
	}

	// a second instance while the first is alive: the draft is locked, not offered
	m2 := New(r.m.be, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }})
	if len(m2.orphans) != 0 {
		t.Fatal("a draft held by a running instance must not be offered")
	}
	m2.Close()

	// the first instance quits: the next one offers the draft
	r.m.Close()
	m3 := New(r.m.be, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }})
	defer m3.Close()
	if len(m3.orphans) != 1 || m3.orphans[0].first != "select 'kept'" {
		t.Fatalf("%+v", m3.orphans)
	}
	r3 := &rig{t: t, m: m3, fake: r.fake, eng: r.eng, now: r.now}
	r3.send(teaSize(120, 30))
	r3.contains("1 draft(s) from earlier sessions")
	r3.press("f6", "ctrl+x", "b")
	r3.contains("Restore drafts", "select 'kept'")
	r3.press("down", "enter")
	if len(m3.bufs) != 1 || m3.buf().ed.text() != "select 'kept'" {
		t.Fatalf("%d buffers %q", len(m3.bufs), m3.buf().ed.text())
	}
	m3.scanOrphans()
	if len(m3.orphans) != 0 {
		t.Fatal("a restored draft is owned again")
	}

	// drafts older than 30 days are deleted
	old := filepath.Join(dir, "deadbeef0000.sql")
	os.WriteFile(old, []byte("select 'old'"), 0o600)
	past := r.now.Add(-31 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	m3.scanOrphans()
	if _, err := os.Stat(old); !os.IsNotExist(err) || len(m3.orphans) != 0 {
		t.Fatal("an old draft must be removed")
	}
}

func TestFileBufferSavesBack(t *testing.T) {
	r := newRig(t, 120, 30)
	path := filepath.Join(r.dir, "q.sql")
	os.WriteFile(path, []byte("select 1\n"), 0o644)
	m := New(r.m.be, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }, File: path})
	defer m.Close()
	rf := &rig{t: t, m: m, fake: r.fake, eng: r.eng, now: r.now}
	rf.send(teaSize(120, 30))
	if m.zone != ZoneEditor || !strings.HasPrefix(m.buf().ed.text(), "select 1") {
		t.Fatalf("zone %s text %q", m.zone, m.buf().ed.text())
	}
	rf.contains("q.sql")
	rf.lacks("●")
	rf.typeText("-- note\n")
	rf.contains("q.sql ●")
	rf.press("ctrl+s")
	rf.lacks("●")
	if b, _ := os.ReadFile(path); string(b) != "-- note\nselect 1\n" {
		t.Fatalf("%q", b)
	}
	if files, _ := filepath.Glob(filepath.Join(m.be.DraftDir(), m.buf().id+"*")); len(files) != 0 {
		t.Fatalf("a file buffer has no draft: %v", files)
	}

	// a scratch buffer asks for a name, then lives in that file
	rf.press("ctrl+x", "n")
	rf.typeText("select 2")
	rf.press("ctrl+s")
	rf.contains("Save buffer as")
	target := filepath.Join(r.dir, "new.sql")
	rf.typeText(target)
	rf.press("enter")
	if b, _ := os.ReadFile(target); string(b) != "select 2\n" {
		t.Fatalf("%q", b)
	}
	rf.contains("new.sql")
}

func wideScript() bqtest.QueryScript {
	s := bqtest.QueryScript{Bytes: 1000}
	for _, n := range []string{"id", "customer", "country", "city", "amount", "note"} {
		s.Schema = append(s.Schema, bq.Field{Name: n, Type: "STRING"})
	}
	s.Schema[0].Type = "INTEGER"
	s.Schema = append(s.Schema, bq.Field{Name: "tags", Type: "STRING", Mode: "REPEATED"})
	for i := 0; i < 40; i++ {
		var note any = strings.Repeat("long note ", 12)
		if i%5 == 0 {
			note = nil
		}
		s.Rows = append(s.Rows, []any{itoa(i), "customer-" + itoa(i), "CZ", "Brno", itoa(i * 10), note,
			[]any{map[string]any{"v": "a"}, map[string]any{"v": "b"}}})
	}
	s.Rows[17][3] = "Needle City"
	return s
}

func TestGridColumnsFreezeHideAndFind(t *testing.T) {
	r := editorRig(t, 70, 24)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return wideScript() }
	r.typeText("select wide")
	r.press("ctrl+r", "f6")
	g := r.m.grid()
	if g == nil || len(g.widths) != 7 {
		t.Fatalf("%+v", g)
	}
	if g.widths[5] != gridMaxW || g.widths[2] != len("country STR") {
		t.Fatalf("widths come from the first page, 4 to 40 cells: %v", g.widths)
	}
	r.contains("id INT", "customer-0")
	r.lacks("tags")
	// moving right scrolls by whole columns
	r.press("$")
	r.contains("tags STR[]", "[2] a, b", "NULL", "long note long note")
	r.lacks("customer-0")
	// freezing keeps the first column in view
	r.press("f")
	r.contains("first column frozen", "tags STR[]")
	if !strings.Contains(r.screen(), " 0 ") {
		t.Fatalf("frozen id column:\n%s", r.screen())
	}
	// hiding removes the cursor's column for the session
	r.press("0", "l", "H")
	r.lacks("customer-0")
	r.contains("1 hidden", "country")
	r.press("U")
	r.contains("customer-0")

	// find within the loaded page
	r.press("/")
	r.typeText("needle")
	r.press("enter")
	if g.row != 17 || g.col != 3 {
		t.Fatalf("find moved to %d,%d", g.row, g.col)
	}
	r.contains("1 matching cells in the 1 loaded page(s) of 1")
	r.press("/", "ctrl+u")
	r.typeText("absent")
	r.press("enter")
	r.contains("is not in the 1 loaded page(s)")
	r.press("esc")
	if g.find != "" || r.m.zone != ZoneResults {
		t.Fatal("esc clears the find text first")
	}
	r.press("esc")
	if r.m.zone != ZoneEditor {
		t.Fatal("then goes back to the editor")
	}
}

func TestExportFromTheGrid(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1203) }
	r.typeText("select many")
	r.press("ctrl+r", "f6", "e")
	r.contains("Export 1,203 rows", "bqt-", ".csv")
	target := filepath.Join(r.dir, "out.csv")
	r.press("ctrl+u")
	r.typeText(target)
	r.press("tab") // json
	if got := r.m.prompt.fields[0].value(); !strings.HasSuffix(got, "out.json") {
		t.Fatalf("tab changes the extension: %q", got)
	}
	r.press("tab", "tab") // ndjson, csv
	r.press("enter")
	r.contains("exported 1,203 rows to")
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 1204 || lines[0] != "n,label" {
		t.Fatalf("%d lines", len(lines))
	}
	// an existing file is not overwritten silently
	r.press("e", "ctrl+u")
	r.typeText(target)
	r.press("enter")
	r.contains("the file exists")
	r.press("esc")
}

func TestExplorerInsertsTableNamesIntoTheEditor(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "j", "j", "i")
	if r.m.zone != ZoneEditor || r.text() != "`acme-prod.sales.orders`" {
		t.Fatalf("zone %s text %q", r.m.zone, r.text())
	}
	// jump-to-table from the editor inserts too
	r.typeText(" join ")
	r.press("ctrl+t")
	r.typeText("refunds")
	r.press("enter")
	if r.m.zone != ZoneEditor || r.text() != "`acme-prod.sales.orders` join `acme-prod.finance.refunds`" {
		t.Fatalf("zone %s text %q", r.m.zone, r.text())
	}
	// a shard family is inserted as a wildcard table
	r.press("ctrl+w", "h", "h", "k", "k", "l", "j", "i")
	if !strings.HasSuffix(r.text(), "`acme-prod.events.events_*`") {
		t.Fatalf("%q", r.text())
	}
}

func TestQueryGoldenScreens(t *testing.T) {
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {60, 20}} {
		r := editorRig(t, sz[0], sz[1])
		r.fake.OnQuery = func(string) bqtest.QueryScript { return wideScript() }
		name := func(s string) string { return s + "_" + itoa(sz[0]) + "x" + itoa(sz[1]) }
		r.typeText("-- orders by customer\nSELECT o.customer_id, SUM(o.amount) AS total\nFROM `acme-prod.sales.orders` AS o\nWHERE o.cr")
		r.golden(name("editor_completion"))
		r.press("tab")
		r.typeText(" > @since")
		r.press("ctrl+r")
		r.golden(name("params_prompt"))
		r.typeText("2024-01-01")
		r.press("enter")
		r.golden(name("editor_with_result"))
		r.press("f6", "j", "j", "l")
		r.golden(name("results"))
		r.press("enter")
		r.golden(name("result_record"))
		r.press("esc", "g", "r")
		r.golden(name("runs"))
	}
}
