package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		if b.Action == "" || b.Help == "" || (len(b.Keys) == 0 && b.Command == "") {
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
		r.golden(name("columns_projects"))
		r.press("l", "j", "j", "l")
		r.golden(name("columns_table"))
		r.press("p")
		r.golden(name("columns_preview"))
		r.press("L")
		r.golden(name("tree"))
		r.press("L")
		r.golden(name("list"))
		r.press("L", "/", "ord") // columns again, in the tables of sales
		r.golden(name("columns_filter_typing"))
		r.press("enter")
		r.golden(name("columns_filter_tables"))
		r.press("h", "/", "s", "enter")
		r.golden(name("columns_filter_datasets"))
		r.press("esc", "L", "/", "ord", "enter")
		r.golden(name("tree_filter"))
		r.press("esc", "L", "/", "ord col:amount")
		r.golden(name("list_filter"))
		r.press("esc", "esc", "L", "ctrl+t", "ev")
		r.golden(name("jump"))
		r.press("esc", "?")
		r.golden(name("help"))
		r.press("esc", "ctrl+p", "sync")
		r.golden(name("palette"))
	}
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

func TestExplorerStyleCycleKeepsSelection(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "j", "j") // acme-prod.sales.orders
	want := "acme-prod.sales.orders"
	for _, style := range []string{"tree", "list", "columns"} {
		r.press("L")
		if got := r.m.sel().path.String(); got != want {
			t.Fatalf("after switching to %s the selection is %q", style, got)
		}
		r.contains("  " + style)
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

func TestExplorerStyleIsRemembered(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L")
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if m2.style != styleTree {
		t.Fatalf("style %v", m2.style)
	}
}

// writeState replaces state/tui.json and starts a second model on it.
func (r *rig) restartWith(state string) *Model {
	r.t.Helper()
	p := r.m.be.StatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(state), 0o600); err != nil {
		r.t.Fatal(err)
	}
	m2 := New(r.m.be, Options{NoBackground: true})
	r.t.Cleanup(m2.Close)
	return m2
}

func TestLegacyStateFileKeepsTheExplorerStyle(t *testing.T) {
	r := newRig(t, 120, 30)
	m2 := r.restartWith(`{"layout":"flat","panes":"bottom","zoom_stacked":true}`)
	if m2.style != styleList || m2.preset != "classic" {
		t.Fatalf("style %v layout %q", styleNames[m2.style], m2.preset)
	}
	for legacy, want := range map[string]explorerStyle{"miller": styleColumns, "tree": styleTree, "list": styleList, "nonsense": styleColumns} {
		if m := r.restartWith(`{"layout":"` + legacy + `"}`); m.style != want {
			t.Errorf("%s: %s", legacy, styleNames[m.style])
		}
	}
	if m := r.restartWith(`{"explorer_style":"tree","layout":"flat"}`); m.style != styleTree {
		t.Errorf("explorer_style wins over the legacy key: %s", styleNames[m.style])
	}
}

func TestMalformedPaneLayoutKeepsTheExplorerStyle(t *testing.T) {
	r := newRig(t, 120, 30)
	for _, pl := range []string{`"focus"`, `{"preset":42}`, `{"preset":"nonsense"}`, `[1]`} {
		m2 := r.restartWith(`{"explorer_style":"list","pane_layout":` + pl + `}`)
		if m2.style != styleList || m2.preset != "classic" {
			t.Errorf("%s: style %s layout %q", pl, styleNames[m2.style], m2.preset)
		}
	}
	m2 := r.restartWith(`{"explorer_style":"tree","pane_layout":{"preset":"focus","tree":{"later":true}}}`)
	if m2.style != styleTree || m2.preset != "focus" {
		t.Fatalf("members a later bqt adds are ignored: style %s layout %q", styleNames[m2.style], m2.preset)
	}
}

func TestSavedStateHasOnlyTheNewKeys(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "L", "L")
	b, err := os.ReadFile(r.m.be.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"explorer_style":"columns","pane_layout":{"preset":"classic"}}` {
		t.Fatalf("state file %s", got)
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

func TestSlashFiltersInPlaceInEveryExplorerStyle(t *testing.T) {
	r := newRig(t, 120, 30)
	for _, style := range []string{"columns", "tree", "list"} {
		r.press("/")
		if r.m.ov != ovNone || !r.m.filterOn() {
			t.Fatalf("%s: / opens a popup (%v) or no filter input", style, r.m.ov)
		}
		r.contains("  "+style, "filter ")
		r.lacks("search ▸")
		r.press("esc", "L")
	}
}

func TestJumpListsRecentlyOpenedTables(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+t")
	r.contains("Jump to table", "nothing opened yet")
	r.press("oi", "enter")
	r.selected("acme-prod.sales.order_items")
	r.press("ctrl+t")
	r.contains("Jump to table", "recently opened", "order_items")
	r.press("esc")
	if r.m.ov != ovNone {
		t.Fatal("esc closes the jump popup")
	}
}

func TestListStyleChipsAndColumnHits(t *testing.T) {
	r := newRig(t, 140, 30) // an Explorer wide enough for the chips
	r.press("L", "L", "/", "type:foo d: -p:staging col:amount")
	r.contains("[type:foo]", "[d:…]", "[-p:staging]", "[col:amount]")
	r.press("ctrl+u", "col:amount")
	r.contains("↳ amount")
	r.press("ctrl+u", "email")
	r.contains("no matches") // columns are not searched without col:
}

func TestTypingInTheJumpPopupNeverTriggersCommands(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+t", "qLPrRy?gg")
	if r.m.ov != ovJump || r.m.jump.in.value() != "qLPrRy?gg" {
		t.Fatalf("printable keys must type: ov=%v text=%q", r.m.ov, r.m.jump.in.value())
	}
	if r.m.syncing || r.m.style != styleColumns {
		t.Fatal("no command may run while typing")
	}
}

func TestStaleSearchResultsAreDropped(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+t")
	st := r.m.jump
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

func TestListStyleFilterScopeAndEsc(t *testing.T) {
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
	r := newRig(t, 160, 30) // room for the error in the status line
	closed := make(chan config.Event)
	close(closed)
	bad := config.Event{Err: errFake("line 3: ingore: unknown key")}
	r.send(configWait{ev: bad, ch: closed})
	r.contains("config error (using the last good config)", "ingore")
	r.contains("acme-staging") // still running on the last good config

	src := "data_dir = \"" + r.dir + "/data\"\nignore = [\"acme-staging\"]\n"
	os.WriteFile(r.cfg, []byte(src), 0o600)
	cfg, err := config.Load(config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	r.send(configWait{ev: config.Event{Config: cfg, Changed: []string{"ignore"}}, ch: closed})
	r.lacks("config error")
	r.contains("config reloaded: ignore")
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
	if err := r.eng.SelectProjects("acme-empty"); err != nil {
		t.Fatal(err)
	}
	r.m.reloadBrowse()
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

func TestListStyleExpandsShardFamilies(t *testing.T) {
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

func TestListStyleRestoredAtStartupIsNotEmpty(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "L") // saves "list"
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
	r.press("z") // schema and preview side by side
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
