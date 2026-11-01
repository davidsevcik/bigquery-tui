package tui

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

const sampleTitle = "Sample · free read, no query job"

// onSalesTable selects a table of acme-prod.sales by its row in the tables column
// (customers, order_items, orders, v_orders).
func (r *rig) onSalesTable(row int, want string) {
	r.t.Helper()
	r.press("l", "j", "j", "l")
	for range row {
		r.press("j")
	}
	r.selected(want)
}

func (r *rig) calls(method string) int { return len(r.fake.CallsTo(method)) }

func TestPLoadsAFreeSampleIntoTheResults(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.press("p")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("the focus stays in the Explorer: %s", r.m.zone)
	}
	r.contains(sampleTitle+" · acme-prod.sales.orders · 45 rows", "cust-", "row 1 of 45")
	if len(r.fake.QueryCalls()) != 0 || len(r.m.runs.List()) != 0 || historyCount(t, r) != 0 {
		t.Fatal("a Sample is no query, no Run and no History entry")
	}
	r.press("g", "r")
	r.contains("no runs in this session yet")
	r.press("esc")

	// the same from the Schema pane, on another table
	r = newRig(t, 120, 30)
	wideRefunds(r)
	r.press("l", "j", "l", "j")
	r.selected("acme-prod.finance.refunds")
	r.press("enter", "p")
	if r.m.zone != ZoneSchema {
		t.Fatalf("the focus stays in the Schema pane: %s", r.m.zone)
	}
	r.contains(sampleTitle + " · acme-prod.finance.refunds · 3 rows")
}

func TestSamplePagesThroughTheWholeTableOnDemand(t *testing.T) {
	r := newRig(t, 120, 30)
	var rows [][]any
	for i := range 1200 {
		rows = append(rows, []any{strconv.Itoa(i + 1), "c" + strconv.Itoa(i+1) + "@example.com"})
	}
	r.fake.TableData["acme-prod.sales.customers"] = bqtest.TableData{
		Info: bq.TableInfo{Type: api.TypeTable, Schema: []bq.Field{{Name: "id", Type: "INTEGER"}, {Name: "email", Type: "STRING"}}},
		Rows: rows,
	}
	r.onSalesTable(0, "acme-prod.sales.customers")
	r.press("p", "ctrl+w", "l", "ctrl+w", "j")
	if r.m.zone != ZoneResults {
		t.Fatalf("precondition: %s", r.m.zone)
	}
	before := r.calls("ListRows")
	r.press("G")
	r.contains("row 1,200 of 1,200", "c1200@")
	if n := r.calls("ListRows") - before; n != 1 {
		t.Fatalf("G fetched %d pages, want only the last window", n)
	}
	r.press("g", "g")
	for range 300 {
		r.press("ctrl+d")
	}
	r.contains("row 1,200 of 1,200")
	loaded := r.calls("ListRows")
	r.press("G", "g", "g", "G")
	if r.calls("ListRows") != loaded {
		t.Fatal("no request once every row is loaded")
	}
}

func TestPOnAViewLikeObjectOnlyPointsToS(t *testing.T) {
	r := newRig(t, 120, 30)
	ext := bqtest.Table("ext_orders", "id")
	ext.Type = api.TypeExternal
	mv := bqtest.Table("mv_totals", "id")
	mv.Type = api.TypeMView
	ds := &r.fake.Projects["acme-prod"].Datasets[0]
	ds.Tables = append(ds.Tables, ext, mv)
	hd, err := r.eng.StartSync(context.Background(), api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	r.m.reloadBrowse()
	for _, c := range []struct{ name, path, toast string }{
		{"v_orders", "acme-prod.sales.v_orders", "views have no free sample; press s for a SELECT (billed)"},
		{"ext_orders", "acme-prod.sales.ext_orders", "external tables have no free sample; press s for a SELECT (billed)"},
		{"mv_totals", "acme-prod.sales.mv_totals", "materialized views have no free sample; press s for a SELECT (billed)"},
	} {
		r.press("ctrl+t", c.name, "enter")
		r.selected(c.path)
		gets, lists := r.calls("GetTable"), r.calls("ListRows")
		r.press("p")
		r.contains(c.toast, "no run yet")
		r.lacks(sampleTitle)
		if r.calls("GetTable") != gets || r.calls("ListRows") != lists {
			t.Fatalf("%s: no backend call", c.path)
		}
	}
}

func TestATableTheCatalogThinksIsATableButIsAView(t *testing.T) {
	r := newRig(t, 120, 30)
	td := r.fake.TableData["acme-prod.sales.orders"]
	r.fake.TableData["acme-prod.sales.customers"] = bqtest.TableData{Info: bq.TableInfo{Type: api.TypeView, Schema: td.Info.Schema}}
	r.onOrders()
	r.press("p", "k", "k")
	r.selected("acme-prod.sales.customers")
	r.press("p")
	r.contains("views have no free sample", sampleTitle+" · acme-prod.sales.orders")
}

func TestBracketsSwitchBetweenTheSampleAndTheRun(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(12) }
	r.typeText("select n from numbers")
	r.press("ctrl+r")
	r.settle()
	r.press("ctrl+w", "h")
	r.onOrders()
	r.press("p")
	r.contains(sampleTitle, "] shows run #1")
	r.press("ctrl+w", "l", "ctrl+w", "j")
	lists := r.calls("ListRows")
	r.press("[")
	r.contains("Results · #1 done · 12 rows")
	r.lacks(sampleTitle)
	r.press("]")
	r.contains(sampleTitle)
	if r.calls("ListRows") != lists {
		t.Fatal("switching back reuses the loaded Sample")
	}
	// p on the table in the Sample shows it again as it was
	r.press("j", "j", "[", "ctrl+w", "k", "ctrl+w", "h", "p")
	r.contains(sampleTitle, "row 3 of 45")
	if r.calls("ListRows") != lists {
		t.Fatal("p on the same table does not read it again")
	}
	// starting a Run shows the Run
	r.press("ctrl+w", "l", "ctrl+r")
	r.settle()
	r.contains("Results · #2 done")
	r.lacks(sampleTitle)
	// so does opening a Run from the run list
	r.press("ctrl+w", "h", "p")
	r.contains(sampleTitle)
	r.press("g", "r", "enter")
	r.contains("Results · #")
	r.lacks(sampleTitle)
	// switching buffers does not change it
	r.press("ctrl+w", "h", "p", "ctrl+w", "l", "ctrl+x", "n")
	r.contains(sampleTitle)
}

func TestHistoryShowTheResultSwitchesBackToTheRun(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(7) }
	r.runSQL("select n from orders")
	r.settle()
	r.press("ctrl+w", "h")
	r.onOrders()
	r.press("p")
	r.contains(sampleTitle)
	r.press("ctrl+x", "r", "ctrl+r")
	r.settle()
	r.lacks(sampleTitle)
	r.contains("7 rows")
}

func TestNoSampleToSwitchTo(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+w", "l", "ctrl+w", "j", "[")
	r.contains("no Sample: p on a table loads one")
}

func TestTheRecordViewAndCopyReadTheFullSampleRow(t *testing.T) {
	r := newRig(t, 120, 40)
	wideRefunds(r)
	r.press("l", "j", "l", "j")
	r.selected("acme-prod.finance.refunds")
	r.press("p")
	r.lacks("THE-END") // the grid clips long values
	r.press("ctrl+w", "l", "ctrl+w", "j", "enter")
	r.contains("Record 1 of 3 · refunds", "THE-END", `"city": "Brno"`, `"tags": [`)
	r.press("n")
	r.contains("Record 2 of 3")
	r.press("esc")
	if g := r.m.grid(); g == nil || g.row != 1 {
		t.Fatal("the grid follows the record view")
	}
	msg, ok := r.m.resultsAction("copyrow")().(clipMsg)
	if !ok || !strings.Contains(msg.text, "THE-END") {
		t.Fatalf("Y copies the full row: %+v", msg)
	}
}

func TestASampleCannotBeExported(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.press("p", "ctrl+w", "l", "ctrl+w", "j", "e")
	r.contains("a Sample cannot be exported: s writes a SELECT whose result can be (billed)")
	if r.m.ov != ovNone {
		t.Fatal("no prompt opens")
	}
}

func TestPagesForAReplacedSampleAreDropped(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	first := r.m.sampleAction() // its first page is not delivered yet
	r.press("k", "k", "p")
	r.selected("acme-prod.sales.customers")
	r.contains(sampleTitle + " · acme-prod.sales.customers")
	r.run(first)
	r.contains(sampleTitle + " · acme-prod.sales.customers")
	// a later page for a replaced grid is ignored too
	r.press("j", "j", "p")
	old := r.m.sample.g
	r.press("k", "k", "p")
	r.send(pageMsg{g: old, start: 0, page: api.PreviewPage{TotalRows: 999, Rows: [][]api.Cell{{{Text: "stale"}}}}})
	r.lacks("stale", "999")
}

func TestPSwitchesToTheResultsInTheFocusLayout(t *testing.T) {
	for _, c := range []struct {
		name   string
		w, h   int
		choose bool
	}{{"chosen", 120, 36, true}, {"stepped down", 60, 20, false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.w, c.h)
			if c.choose {
				r.press("ctrl+p", "Layout: focus", "enter")
			}
			r.onOrders()
			r.press("p")
			if r.m.zone != ZoneResults {
				t.Fatalf("pane %s", r.m.zone)
			}
			r.contains("[Results]", "Sample · free read")
		})
	}
}

func TestThePreviewKeysAreGone(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	for _, z := range []string{"explorer", "schema"} {
		if z == "schema" {
			r.press("enter")
		}
		before := r.screen()
		for _, k := range []string{"J", "tab"} {
			r.press(k)
			if r.m.ov != ovNone || r.m.toast != "" || r.screen() != before {
				t.Fatalf("%s in the %s does something:\n%s", k, z, r.screen())
			}
		}
	}
	help := strings.Join(helpLines(120), "\n")
	for _, gone := range []string{"preview row", "schema and preview", "load 20"} {
		if strings.Contains(help, gone) {
			t.Fatalf("the help still mentions %q", gone)
		}
	}
}

func TestSchemaFlagsARequiredPartitionFilter(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.contains("partition created_at", "partition filter required")
	r.press("k", "k")
	r.lacks("partition filter required")
}

// --- s

func (r *rig) bufNames() []string {
	var out []string
	for _, b := range r.m.bufs {
		out = append(out, b.title())
	}
	return out
}

func TestSOpensASelectForTheTableInANewBuffer(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onSalesTable(0, "acme-prod.sales.customers")
	r.press("s")
	want := "SELECT *\nFROM `acme-prod.sales.customers`\nWHERE true\nLIMIT 100"
	if r.text() != want {
		t.Fatalf("text:\n%s", r.text())
	}
	if b := r.m.buf(); b.ed.row != 2 || b.ed.col != 6 {
		t.Fatalf("the cursor is on the condition: %d:%d", b.ed.row, b.ed.col)
	}
	if got := strings.Join(r.bufNames(), ","); got != "query 1,customers" || r.m.zone != ZoneEditor {
		t.Fatalf("buffers %s, pane %s", got, r.m.zone)
	}
	if len(r.fake.QueryCalls()) != 0 || len(r.m.runs.List()) != 0 {
		t.Fatal("s never runs the query")
	}
	r.contains("new buffer customers: not run, billed when run (ctrl+d estimates)")
	// the draft is written on the next tick
	b := r.m.buf()
	r.m.saveDrafts() // what the next tick does
	if got, err := os.ReadFile(r.m.draftPath(b.id)); err != nil || string(got) != want {
		t.Fatalf("draft %q %v", got, err)
	}
	r.press("ctrl+w", "h", "s")
	if got := strings.Join(r.bufNames(), ","); got != "query 1,customers,customers 2" {
		t.Fatalf("buffers %s", got)
	}
}

func TestSWritesTheFilterTheTableNeeds(t *testing.T) {
	cases := []struct {
		name  string
		pick  func(r *rig)
		from  string
		where string
	}{
		{"required partition filter", func(r *rig) { r.onOrders() }, "`acme-prod.sales.orders`",
			"created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"},
		{"shard family", func(r *rig) {
			r.press("l", "l", "j")
			r.selected("acme-prod.events.events_YYYYMMDD")
		}, "`acme-prod.events.events_*`", "_TABLE_SUFFIX = '20240104'"},
		{"view", func(r *rig) { r.onSalesTable(3, "acme-prod.sales.v_orders") }, "`acme-prod.sales.v_orders`", "true"},
		{"from the Schema pane", func(r *rig) { r.onOrders(); r.press("enter") }, "`acme-prod.sales.orders`",
			"created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, 120, 30)
			c.pick(r)
			r.press("s")
			if want := "SELECT *\nFROM " + c.from + "\nWHERE " + c.where + "\nLIMIT 100"; r.text() != want {
				t.Fatalf("text:\n%s", r.text())
			}
			if r.m.zone != ZoneEditor {
				t.Fatalf("pane %s", r.m.zone)
			}
		})
	}
	r := newRig(t, 120, 30)
	r.press("l", "l", "j", "s")
	if got := r.m.buf().title(); got != "events_*" {
		t.Fatalf("a family's buffer: %q", got)
	}
}

func TestSWaitsForTheTableDetailOrFallsBackToTrue(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.m.details = map[string]*detailData{} // the detail is still on its way
	r.press("s")
	if !strings.Contains(r.text(), "WHERE created_at >=") {
		t.Fatalf("the detail is read first:\n%s", r.text())
	}
	r.press("ctrl+w", "h")
	r.send(selectDetailMsg{path: r.m.sel().path, key: r.m.sel().detailKey(), err: os.ErrNotExist}) // the read failed
	if !strings.Contains(r.text(), "WHERE true") {
		t.Fatalf("without the detail:\n%s", r.text())
	}
}

func TestSStartsInNormalModeInVim(t *testing.T) {
	rigEditing = "vim"
	defer func() { rigEditing = "plain" }()
	r := newRig(t, 120, 30)
	r.onSalesTable(0, "acme-prod.sales.customers")
	r.press("s")
	if r.m.buf().vim.mode != vimNormal {
		t.Fatalf("mode %v", r.m.buf().vim.mode)
	}
	r.contains("(space d estimates)")
	r.press("c", "w")
	r.typeText("id > 3")
	if !strings.Contains(r.text(), "WHERE id > 3\n") {
		t.Fatalf("cw replaces the condition:\n%s", r.text())
	}
}

// Codex review of phase 2: the message orders and states below went wrong.

func TestAFamilySampleKeepsReadingTheShardItStartedWith(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "l", "j")
	r.selected("acme-prod.events.events_YYYYMMDD")
	r.press("p")
	// later pages must not follow the family to a newer shard synced meanwhile
	if got := r.m.sample.path.String(); got != "acme-prod.events.events_20240104" {
		t.Fatalf("the Sample reads %s, want the shard it was loaded from", got)
	}
}

func TestARestoredSampleCanStillLoadItsPages(t *testing.T) {
	r := newRig(t, 120, 30)
	td := r.fake.TableData["acme-prod.sales.orders"]
	r.fake.TableData["acme-prod.sales.customers"] = bqtest.TableData{Info: bq.TableInfo{Type: api.TypeView, Schema: td.Info.Schema}}
	r.onOrders()
	r.press("p")
	a := r.m.sample.g
	page := r.m.fetchPage(a, gridPage) // A's next page is on its way
	r.press("k", "k")
	r.selected("acme-prod.sales.customers")
	b := r.m.sampleAction()
	r.run(page) // arrives while B is the Sample
	r.run(b)    // B turns out to be a view: A comes back
	if r.m.sample == nil || r.m.sample.g != a {
		t.Fatal("the earlier Sample is not restored")
	}
	if a.loading[gridPage] {
		t.Fatal("the page that arrived meanwhile stays loading for ever")
	}
}

func TestAFailedSampleDoesNotUndoSwitchingToTheRun(t *testing.T) {
	r := newRig(t, 120, 30)
	td := r.fake.TableData["acme-prod.sales.orders"]
	r.fake.TableData["acme-prod.sales.customers"] = bqtest.TableData{Info: bq.TableInfo{Type: api.TypeView, Schema: td.Info.Schema}}
	r.onOrders()
	r.press("p", "k", "k")
	b := r.m.sampleAction()
	r.m.showRun() // a Run is started or picked before B answers
	r.run(b)
	if r.m.sampleShown() {
		t.Fatal("the failed Sample switched the Results back to the earlier Sample")
	}
}

func TestPBringsTheResultsBackFromAMaximisedSchema(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
		want Zone // where the focus ends up
	}{{"classic", 120, 30, ZoneSchema}, {"stepped down", 80, 24, ZoneResults}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.w, c.h)
			r.onOrders()
			r.press("enter")
			if c.want == ZoneSchema {
				r.press("ctrl+w", "z")
			}
			if _, ok := r.m.arr().rects[ZoneResults]; ok {
				t.Fatalf("the Schema is not maximised: %v", r.m.arr().rects)
			}
			r.press("p")
			if _, ok := r.m.arr().rects[ZoneResults]; !ok {
				t.Fatalf("the Sample loaded off screen (maxed %q)", r.m.maxed)
			}
			if r.m.zone != c.want {
				t.Fatalf("pane %s, want %s", r.m.zone, c.want)
			}
			r.contains(sampleTitle)
		})
	}
}
