package tui

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

func TestKeymapHasNoDuplicateBindings(t *testing.T) {
	if d := duplicateBindings(Keymap); len(d) != 0 {
		t.Fatalf("duplicate bindings: %v", d)
	}
	for _, b := range Keymap {
		if b.Action == "" || b.Help == "" || len(b.Keys) == 0 {
			t.Errorf("incomplete binding: %+v", b)
		}
	}
	if !strings.Contains(footerHints(ZoneExplorer), "↑↓←→/hjkl move") {
		t.Fatal("the footer must spell out the movement keys")
	}
}

func TestEveryPaletteCommandHasAnAction(t *testing.T) {
	r := newRig(t, 120, 30)
	for _, c := range paletteCommands() {
		if c.action == "quit" {
			continue
		}
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("command %q panicked: %v", c.name, p)
				}
			}()
			if c.action == "config" {
				return // spawns an editor
			}
			r.run(r.m.explorerAction(c.action))
			r.press("esc")
			r.m.ov = ovNone
		}()
	}
}

func TestGoldenScreens(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 36}, {80, 24}, {60, 20}}
	for _, sz := range sizes {
		name := func(s string) string { return s + "_" + itoa(sz.w) + "x" + itoa(sz.h) }
		r := newRig(t, sz.w, sz.h)
		r.golden(name("miller_projects"))
		r.press("l", "j", "j", "l")
		r.golden(name("miller_table"))
		r.press("p")
		r.golden(name("miller_preview"))
		r.press("L")
		r.golden(name("tree"))
		r.press("L")
		r.golden(name("flat"))
		r.press("L", "/", "ord col:amount")
		r.golden(name("search"))
		r.press("esc", "ctrl+t", "ev")
		r.golden(name("jump"))
		r.press("esc", "?")
		r.golden(name("help"))
		r.press("esc", "ctrl+p", "sync")
		r.golden(name("palette"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestMillerNavigationAndDetail(t *testing.T) {
	r := newRig(t, 120, 30)
	r.contains("Projects 1/2", "acme-prod", "✓ 3h", "↑↓←→/hjkl move")
	r.press("l") // datasets
	r.contains("Datasets 1/4")
	r.press("j", "j") // sales
	r.press("l")      // tables
	r.contains("Tables 1/4", "customers")
	r.press("j", "j") // orders
	if s := r.m.sel(); s.path.String() != "acme-prod.sales.orders" {
		t.Fatalf("selection %v", s.path)
	}
	r.contains("All customer orders", "45 rows", "5.0 MiB", "partition created_at", "labels team:sales", "schema · 8 fields", "  city")
	r.press("h", "h")
	r.contains("Projects 1/2")
	r.press("G")
	if r.m.sel().path.Project != "acme-staging" {
		t.Fatal("G goes to the last row")
	}
	r.press("g", "g")
	if r.m.sel().path.Project != "acme-prod" {
		t.Fatal("gg goes to the first row")
	}
}

func TestShardFamilyExpandsAndCollapses(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "l", "j") // events dataset, the family row
	if s := r.m.sel(); s.kind != api.KindShards {
		t.Fatalf("%+v", s)
	}
	r.contains("events_YYYYMMDD", "×4", "shard family ×4", "20240101 → 20240104", "schema of the newest shard")
	r.press("enter")
	r.contains("events_20240104", "events_20240101")
	if s := r.m.sel(); s.kind != api.KindTable || s.path.Table != "events_20240104" {
		t.Fatalf("newest shard first: %+v", s.path)
	}
	r.press("esc") // one level back: collapse
	if s := r.m.sel(); s.kind != api.KindShards {
		t.Fatalf("esc collapses the family and returns to its row: %+v", s)
	}
	r.press("enter", "h")
	if s := r.m.sel(); s.kind != api.KindShards {
		t.Fatal("h collapses too")
	}
}

func TestLayoutCycleKeepsSelection(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "j", "j") // acme-prod.sales.orders
	want := "acme-prod.sales.orders"
	for _, layout := range []string{"tree", "flat", "miller"} {
		r.press("L")
		if got := r.m.sel().path.String(); got != want {
			t.Fatalf("after switching to %s the selection is %q", layout, got)
		}
		r.contains(" " + layout)
	}
	// a shard member survives the round trip as well
	r.press("h", "k", "k", "l", "j", "enter", "j")
	member := r.m.sel().path.String()
	if !strings.HasPrefix(member, "acme-prod.events.events_2024") {
		t.Fatalf("precondition: %s", member)
	}
	r.press("L")
	if got := r.m.sel().path.String(); got != member {
		t.Fatalf("tree: %s", got)
	}
}

func TestLayoutIsRemembered(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "P")
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if m2.layout != layoutTree || m2.place != placeRight {
		t.Fatalf("layout %v panes %v", m2.layout, m2.place)
	}
}

func TestTreeExpandCollapse(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L")
	r.press("l") // open project
	r.contains("▾ acme-prod", "▸ events")
	r.press("j", "l") // open events
	r.contains("▾ events", "events_YYYYMMDD", "×4")
	r.press("j", "j", "l") // open the family
	r.contains("events_20240104")
	r.press("h") // close family
	r.lacks("events_20240104")
	r.press("h", "h") // parent dataset, then collapse it
	r.lacks("clicks")
}

func TestSearchOverlayFindsAndReveals(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/")
	r.contains("search ▸", "nothing opened yet")
	r.press("oi")
	r.contains("order_items", "word")
	r.press("enter")
	if r.m.ov != ovNone {
		t.Fatal("enter closes the overlay")
	}
	if got := r.m.sel().path.String(); got != "acme-prod.sales.order_items" {
		t.Fatalf("revealed %s", got)
	}
	// the table is now recently opened
	r.press("ctrl+t")
	r.contains("Jump to table", "recently opened", "order_items")
	r.press("esc")
	if r.m.ov != ovNone {
		t.Fatal("esc closes the jump popup")
	}
}

func TestSearchChipsAndColumnHits(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "type:foo d: -p:staging col:amount")
	r.contains("[type:foo]", "[d:…]", "[-p:staging]", "[col:amount]")
	r.contains("↳ amount")
	r.press("ctrl+u")
	r.press("email")
	r.contains("no matches") // columns are not searched without col:
	r.press("ctrl+u", "events")
	r.contains("events_YYYYMMDD", "×4")
	r.press("down", "enter") // the family result (after the dataset row)
	if s := r.m.sel(); !strings.HasPrefix(s.path.Table, "events_2024") {
		t.Fatalf("revealing a family lists its shards: %+v", s.path)
	}
}

func TestTypingNeverTriggersCommands(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "qLPrRy?gg")
	if r.m.ov != ovSearch || r.m.search.in.value() != "qLPrRy?gg" {
		t.Fatalf("printable keys must type: ov=%v text=%q", r.m.ov, r.m.search.in.value())
	}
	if r.m.syncing || r.m.layout != layoutMiller {
		t.Fatal("no command may run while typing")
	}
}

func TestStaleSearchResultsAreDropped(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/")
	st := r.m.search
	st.in.set("ord")
	first := r.m.runSearch(st) // not delivered yet
	st.in.set("refunds")
	second := r.m.runSearch(st)
	r.send(second())
	r.send(first()) // the older answer arrives late and must be ignored
	if len(st.results) != 1 || st.results[0].Path.Table != "refunds" {
		t.Fatalf("%+v", st.results)
	}
}

func TestFlatLayoutFilterScopeAndEsc(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "L")
	r.contains("filter ▸", "1/10")
	r.press("/", "ord", "enter")
	r.contains("1/5")
	r.press("j")
	r.press("S") // scope to the selected result's project
	r.contains("⊂ acme-staging", "1/1")
	r.press("S")
	r.press("esc") // clears the filter
	if r.m.flat.in.value() != "" {
		t.Fatal("esc clears the flat filter")
	}
}

func TestPreviewPagesAndStops(t *testing.T) {
	r := newRig(t, 120, 40)
	r.press("l", "j", "j", "l", "j", "j", "p")
	r.contains("20 of 45 rows", "p loads more", "cust-")
	r.press("p")
	r.contains("40 of 45 rows")
	r.press("p")
	r.contains("45 of 45 rows")
	r.lacks("p loads more")
	calls := len(r.fake.CallsTo("ListRows"))
	r.press("p")
	if len(r.fake.CallsTo("ListRows")) != calls {
		t.Fatal("no request once every row is loaded")
	}
	// a view cannot be previewed
	r.press("j", "p")
	r.contains("needs a query")
}

func TestNarrowTerminalCollapsesAndZooms(t *testing.T) {
	r := newRig(t, 60, 20)
	r.lacks("Schema ·")
	r.press("l", "j", "j", "l", "j", "j", "enter")
	if !r.m.zoom {
		t.Fatal("enter zooms the detail when the pane is collapsed")
	}
	r.contains("schema · 8 fields")
	r.press("esc")
	if r.m.zoom {
		t.Fatal("esc leaves the zoom")
	}
	r.contains("Tables 3/4")
}

func TestPanePlacementByWidth(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want placement
	}{{120, 30, placeRight}, {100, 24, placeRight}, {90, 30, placeBottom}, {90, 24, placeHidden}, {60, 40, placeHidden}} {
		r := newRig(t, c.w, c.h)
		if got := r.m.effPlacement(); got != c.want {
			t.Errorf("%dx%d: %v, want %v", c.w, c.h, placementNames[got], placementNames[c.want])
		}
	}
}

func TestSyncFromTheUI(t *testing.T) {
	r := newRig(t, 120, 30)
	r.fake.AddProject("acme-prod").Datasets[2].Tables = append(r.fake.Projects["acme-prod"].Datasets[2].Tables, nested("ledger"))
	r.press("R")
	r.contains("sync finished: 3 ok")
	r.press("l", "j", "l")
	r.contains("ledger")
	r.press("g", "s")
	r.contains("Sync progress", "found 3 unit(s)", "finished:")
	r.press("esc")
	// a failing project shows up as a problem, without a modal
	r.fake.Projects["acme-staging"].ListErr = errFake("boom")
	r.press("R")
	if r.m.ov != ovNone {
		t.Fatal("failures never open a modal")
	}
	r.contains("sync problem(s) (gp)")
	r.press("g", "p")
	r.contains("Sync problems", "acme-staging", "boom")
}

type errFake string

func (e errFake) Error() string { return string(e) }

func TestConfigReloadEvents(t *testing.T) {
	r := newRig(t, 120, 30)
	closed := make(chan config.Event)
	close(closed)
	bad := config.Event{Err: errFake("line 3: ingore: unknown key")}
	r.send(configWait{ev: bad, ch: closed})
	r.contains("config error (using the last good config)", "ingore")
	r.contains("acme-staging") // still running on the last good config

	src := "execution_project = \"other-exec\"\ndata_dir = \"" + r.dir + "/data\"\nprojects = [\"acme-prod\"]\n"
	os.WriteFile(r.cfg, []byte(src), 0o600)
	cfg, err := config.Load(config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	r.send(configWait{ev: config.Event{Config: cfg, Changed: []string{"execution_project", "projects"}}, ch: closed})
	r.lacks("config error", "acme-staging")
	r.contains("exec other-exec")
	if len(r.fake.CallsTo("ListDatasets")) != 2 {
		t.Fatal("a config change prunes locally; it must not sync on its own")
	}
}

func TestQuitAndCancelKeys(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+c")
	r.contains("nothing is running (ctrl+q quits)")
	_, cmd := r.m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+q must quit")
	}
}

func TestTinyTerminal(t *testing.T) {
	r := newRig(t, 10, 3)
	r.contains("larger terminal")
}

func TestEnterOnEmptyContainersInTreeDoesNotRecurse(t *testing.T) {
	r := newRig(t, 120, 30)
	r.fake.AddProject("acme-empty")
	src := "execution_project = \"acme-exec\"\ndata_dir = \"" + r.dir + "/data\"\nprojects = [\"acme-empty\", \"acme-prod\", \"acme-staging\"]\n"
	os.WriteFile(r.cfg, []byte(src), 0o600)
	cfg, err := config.Load(config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan config.Event)
	close(closed)
	r.send(configWait{ev: config.Event{Config: cfg, Changed: []string{"projects"}}, ch: closed})
	r.press("L") // tree
	r.press("g", "g")
	if r.m.sel().path.Project != "acme-empty" {
		t.Fatalf("precondition: %+v", r.m.sel().path)
	}
	r.press("enter", "enter", "l", "l", "enter") // used to overflow the stack
	r.press("L", "g", "g", "enter", "l")         // Miller: an empty project has no next column
	if r.m.mil.col != 0 {
		t.Fatalf("col %d", r.m.mil.col)
	}
}

func TestFlatLayoutExpandsShardFamilies(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "L", "/", "events_", "enter")
	if s := r.m.sel(); s.kind != api.KindShards {
		t.Fatalf("precondition: %+v", s)
	}
	r.press("enter")
	if s := r.m.sel(); s.kind != api.KindTable || !strings.HasPrefix(s.path.Table, "events_2024") {
		t.Fatalf("enter lists the shards: %+v", s)
	}
	r.contains("1/4")
	r.press("esc") // back to the unfiltered, collapsed list
	r.contains("events_YYYYMMDD", "1/10")
}

func TestFlatLayoutRestoredAtStartupIsNotEmpty(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "L") // saves "flat"
	m2 := New(r.m.be, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }})
	defer m2.Close()
	r2 := &rig{t: t, m: m2, fake: r.fake, now: r.now}
	r2.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	r2.run(m2.Init())
	r2.contains("filter ▸", "1/10", "customers")
}

func TestPreviewContinuesAfterAShortPage(t *testing.T) {
	r := newRig(t, 120, 40)
	r.press("l", "j", "j", "l", "j", "j")
	key := r.m.sel().detailKey()
	// BigQuery returned only 7 of the 20 requested rows (wide rows), 45 exist
	page := api.PreviewPage{Columns: []api.PreviewColumn{{Name: "id"}}, TotalRows: 45}
	for i := 0; i < 7; i++ {
		page.Rows = append(page.Rows, []api.Cell{{Text: "x"}})
	}
	r.send(previewMsg{key: key, page: page})
	if r.m.details[key].prevEnd {
		t.Fatal("a short page is not the end while the total says more rows exist")
	}
	r.contains("7 of 45 rows", "p loads more")
}

func TestPreviewScrollsIndependentlyOfTheSchema(t *testing.T) {
	r := newRig(t, 120, 30) // schema and preview both visible, preview is short
	r.press("l", "j", "j", "l", "j", "j", "p", "p", "p")
	r.contains("45 of 45 rows")
	first := r.screen()
	for i := 0; i < 30; i++ {
		r.press("ctrl+e")
	}
	if r.screen() == first {
		t.Fatal("the preview did not move")
	}
	if r.m.prevRow != 30 || r.m.prevOff == 0 {
		t.Fatalf("ctrl+e must move the preview cursor after p focused it, and the pane follows: row=%d off=%d", r.m.prevRow, r.m.prevOff)
	}
	if r.m.detOff != 0 {
		t.Fatal("the schema keeps its own offset")
	}
}

// wideRefunds gives finance.refunds rows with a long text and a nested value.
func wideRefunds(r *rig) {
	rows := [][]any{}
	for i := 0; i < 3; i++ {
		rows = append(rows, []any{
			strconv.Itoa(i + 1),
			strings.Repeat("long text ", 40) + "THE-END",
			map[string]any{"f": []any{map[string]any{"v": "Brno"}, map[string]any{"v": []any{map[string]any{"v": "a"}, map[string]any{"v": "b"}}}}},
		})
	}
	r.fake.TableData["acme-prod.finance.refunds"] = bqtest.TableData{
		Info: bq.TableInfo{Type: api.TypeTable, Schema: []bq.Field{
			{Name: "id", Type: "INTEGER"}, {Name: "note", Type: "STRING"},
			{Name: "address", Type: "RECORD", Fields: []bq.Field{{Name: "city", Type: "STRING"}, {Name: "tags", Type: "STRING", Mode: "REPEATED"}}},
		}},
		Rows: rows,
	}
}

func TestRecordPopupShowsTheFullRow(t *testing.T) {
	r := newRig(t, 120, 40)
	wideRefunds(r)
	r.press("l", "j", "l", "j")
	if s := r.m.sel(); s.path.Table != "refunds" {
		t.Fatalf("precondition: %+v", s)
	}
	r.press("J")
	r.contains("load a preview first")
	if r.m.ov != ovNone {
		t.Fatal("no popup without a preview")
	}
	r.press("p")
	r.lacks("THE-END") // the preview clips long values
	r.press("J")
	// the clipped value was read again in full, the nested value is indented
	r.contains("Record 1 of 3 · refunds", "THE-END", `"city": "Brno"`, `"tags": [`)
	r.press("n")
	r.contains("Record 2 of 3")
	r.press("n", "n")
	r.contains("Record 3 of 3", "last loaded row")
	r.press("N")
	r.contains("Record 2 of 3")
	r.press("esc")
	if r.m.ov != ovNone || r.m.prevRow != 1 {
		t.Fatalf("esc closes and the preview cursor follows the popup: ov=%v row=%d", r.m.ov, r.m.prevRow)
	}
}

func TestZoomMovementKeysStayInTheDetailPane(t *testing.T) {
	r := newRig(t, 80, 14)
	r.press("l", "j", "j", "l", "j", "j")
	sel := r.m.sel()
	if sel.path.Table != "orders" {
		t.Fatalf("precondition: %+v", sel)
	}
	r.press("z", "j", "j", "down")
	if !r.m.zoom || r.m.sel().path != sel.path || r.m.detOff != 3 {
		t.Fatalf("down scrolls the zoomed schema: zoom=%v sel=%+v off=%d", r.m.zoom, r.m.sel(), r.m.detOff)
	}
	r.press("right", "l")
	if !r.m.zoom || r.m.sel().path != sel.path {
		t.Fatal("right does nothing in the zoom")
	}
	r.press("left")
	if r.m.zoom || r.m.sel().path != sel.path {
		t.Fatalf("left leaves the zoom and keeps the table selected: zoom=%v sel=%+v", r.m.zoom, r.m.sel())
	}
	// zoomed preview: the keys move the row cursor, enter shows the record
	r.press("z", "p", "j", "j", "enter")
	r.contains("Record 3 of 20 · orders")
	r.press("esc", "G", "j")
	r.contains("40 of 45 rows") // moving past the last loaded row loads the next page
	r.press("esc")
	if r.m.zoom || r.m.sel().path != sel.path {
		t.Fatal("esc leaves the zoom")
	}
}

func TestSchemaFlagsRequiredColumns(t *testing.T) {
	r := newRig(t, 120, 30)
	wideRefunds(r)
	td := r.fake.TableData["acme-prod.finance.refunds"]
	td.Info.Schema = []bq.Field{{Name: "id", Type: "STRING", Mode: "REQUIRED"}, {Name: "amount", Type: "STRING"}}
	r.fake.TableData["acme-prod.finance.refunds"] = td
	r.press("l", "j", "l", "j")
	var id, amount string
	for _, l := range strings.Split(r.screen(), "\n") {
		if i := strings.Index(l, "── schema"); i >= 0 {
			id, amount = "", ""
		}
		if strings.Contains(l, " id ") && strings.Contains(l, "STRING") {
			id = l
		}
		if strings.Contains(l, " amount ") && strings.Contains(l, "STRING") {
			amount = l
		}
	}
	if !strings.Contains(id, "STRING") || !strings.Contains(id, "required") || strings.Contains(amount, "required") || amount == "" {
		t.Fatalf("only the REQUIRED field is flagged:\n%s", r.screen())
	}
}

func TestSchemaFlagsColumnsStoredAsRequiredWithoutTheLiveTable(t *testing.T) {
	r := newRig(t, 120, 30)
	r.fake.Projects["acme-prod"].Datasets[2].Tables[1].Columns[0].Required = true
	hd, err := r.eng.StartSync(context.Background(), api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	r.fake.Fail(bqtest.Failure{Method: "GetTable", Err: errors.New("offline")})
	r.m.reloadBrowse()
	r.press("l", "j", "l")
	s := r.m.sel()
	r.contains("required")
	if d := r.m.details[s.detailKey()]; d == nil || d.stats != nil {
		t.Fatalf("precondition: the flag must come from the catalog, not the live table: %+v %+v", s, d)
	}
}

func TestZoomSplitSwitchesBetweenSideBySideAndStacked(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "j", "j", "z")
	line := func(sub string) int {
		for i, l := range strings.Split(r.screen(), "\n") {
			if strings.Contains(l, sub) {
				return i
			}
		}
		return -1
	}
	if line(" Schema ") != line(" Preview ") {
		t.Fatalf("side by side first:\n%s", r.screen())
	}
	place := r.m.place
	r.press("P")
	if s, p := line(" Schema "), line(" Preview "); s < 0 || p <= s {
		t.Fatalf("P stacks the schema above the preview:\n%s", r.screen())
	}
	if r.m.place != place || !r.m.zoom {
		t.Fatal("P in the zoom leaves the normal pane placement alone")
	}
	// remembered across restarts
	m2 := New(r.m.be, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }})
	defer m2.Close()
	if !m2.zoomStacked {
		t.Fatal("the zoom split is saved in the state file")
	}
	r.press("P")
	if line(" Schema ") != line(" Preview ") {
		t.Fatal("P again puts them side by side")
	}
}
