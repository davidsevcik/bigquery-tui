package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// onOrders selects acme-prod.sales.orders in the columns style.
func (r *rig) onOrders() {
	r.t.Helper()
	r.press("l", "j", "j", "l", "j", "j")
	r.selected("acme-prod.sales.orders")
}

// line is the first screen row containing sub, or -1.
func (r *rig) line(sub string) int {
	for i, l := range strings.Split(r.screen(), "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func TestEnterOnATableGoesToTheSchemaPane(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.press("enter")
	if r.m.zone != ZoneSchema || r.m.maxed != "" {
		t.Fatalf("enter focuses the Schema pane in place: %s %q", r.m.zone, r.m.maxed)
	}
	r.contains("↑↓/jk scroll · p sample · esc back")
	r.press("j", "j")
	if r.m.detOff != 2 {
		t.Fatalf("j scrolls the schema: %d", r.m.detOff)
	}
	r.press("G")
	r.screen()
	if r.m.detOff <= 2 {
		t.Fatalf("G goes to the bottom: %d", r.m.detOff)
	}
	r.press("g", "g")
	if r.m.detOff != 0 {
		t.Fatalf("gg goes to the top: %d", r.m.detOff)
	}
	r.press("esc")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("esc goes back to the Explorer: %s", r.m.zone)
	}
	r.selected("acme-prod.sales.orders")
	r.press("ctrl+e", "ctrl+e")
	if r.m.detOff != 2 || r.m.zone != ZoneExplorer {
		t.Fatalf("ctrl+e scrolls the Schema from the Explorer: %d %s", r.m.detOff, r.m.zone)
	}
	// h and left go back too
	r.press("enter", "h")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("h: %s", r.m.zone)
	}
}

func TestEnterShowsTheSchemaInTheFocusLayout(t *testing.T) {
	r := newRig(t, 60, 20)
	r.contains("[Explorer]  Schema  Editor  Results   ctrl+o next")
	r.onOrders()
	r.press("enter")
	r.contains("Explorer  [Schema]  Editor  Results", "schema · 8 fields")
	r.lacks("Tables 3/4")
	r.press("esc")
	r.contains("[Explorer]", "Tables 3/4")
}

func TestEnterShowsASteppedDownSchemaMaximised(t *testing.T) {
	r := newRig(t, 80, 24)
	r.lacks("Schema ·")
	r.onOrders()
	r.press("enter")
	if r.m.zone != ZoneSchema || !r.m.arr().offLayout || r.m.maxed != "" {
		t.Fatalf("pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	r.contains("schema · 8 fields", "Explorer  [Schema]  Editor  Results   ctrl+o next")
	r.lacks("Tables 3/4", "SQL · query 1", "(maximised)")
	r.press("esc")
	if r.m.zone != ZoneExplorer || r.m.arr().offLayout {
		t.Fatalf("esc goes back: pane %s", r.m.zone)
	}
	r.contains("Tables 3/4", "SQL · query 1")
	r.lacks("schema · 8 fields")
	// ctrl+w z restores as well: back to a Pane the Layout has room for
	r.press("enter", "ctrl+w", "z")
	if r.m.zone != ZoneExplorer || r.m.maxed != "" {
		t.Fatalf("ctrl+w z restores: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
}

func TestCtrlWZMaximisesAnyPaneAndTheZoomFollowsFocus(t *testing.T) {
	r := newRig(t, 120, 30)
	r.onOrders()
	r.press("enter", "ctrl+w", "z")
	if r.m.zone != ZoneSchema || r.m.maxed != ZoneSchema {
		t.Fatalf("ctrl+w z maximises: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	r.lacks("Tables 3/4", "SQL · query 1")
	r.contains("layout: classic (maximised)", "Explorer  [Schema]  Editor  Results   ctrl+w z restore")
	if r.line(" Schema · acme-prod.sales.orders") != 1 {
		t.Fatalf("maximised, the Schema pane fills the screen under the pane bar:\n%s", r.screen())
	}
	r.press("ctrl+w", "z")
	if r.m.zone != ZoneSchema || r.m.maxed != "" {
		t.Fatalf("ctrl+w z again restores and keeps the focus: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	r.contains("Tables 3/4", "SQL · query 1")
	r.lacks("(maximised)")
	// the zoom follows ctrl+w l, found on the real Layout: right of the
	// Schema are the Results
	r.press("ctrl+w", "z", "ctrl+w", "l")
	if r.m.zone != ZoneResults || r.m.maxed != ZoneResults {
		t.Fatalf("ctrl+w l moves the zoom: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	r.contains("Explorer  Schema  Editor  [Results]", "(maximised)")
	// and ctrl+o, skipping a hidden Pane
	r.press("ctrl+w", "z", "ctrl+w", "k", "ctrl+w", "c", "ctrl+w", "j", "ctrl+w", "z")
	if r.m.zone != ZoneResults || r.m.maxed != ZoneResults || !r.m.panes.hiddenPane(ZoneEditor) {
		t.Fatalf("pane %s maxed %q tree %s", r.m.zone, r.m.maxed, r.m.panes)
	}
	r.press("ctrl+o")
	if r.m.zone != ZoneExplorer || r.m.maxed != ZoneExplorer {
		t.Fatalf("ctrl+o skips the hidden Editor: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	r.contains("[Explorer]  Schema  Results   ctrl+w z restore")
	// esc does not restore: it keeps its meaning in the Pane (here the
	// Schema's: back to the Explorer, which the zoom follows)
	r.press("ctrl+o", "esc")
	if r.m.zone != ZoneExplorer || r.m.maxed != ZoneExplorer {
		t.Fatalf("esc: pane %s maxed %q", r.m.zone, r.m.maxed)
	}
	// the palette names the Panes too
	r.press("ctrl+p", "Go to the Schema", "enter")
	if r.m.zone != ZoneSchema || r.m.maxed != ZoneSchema {
		t.Fatalf("the palette goes to the Schema pane: %s %q", r.m.zone, r.m.maxed)
	}
}

func TestPagingKeysMoveByHalfThePane(t *testing.T) {
	r := newRig(t, 120, 40) // a 38-row body: each Pane is 19 rows
	r.press("L", "L")       // the list style: ten tables
	r.press("ctrl+d")
	if want := (19 - 2) / 2; r.m.flat.cur != want {
		t.Fatalf("ctrl+d in the Explorer moves %d rows, want %d", r.m.flat.cur, want)
	}
	r.press("L", "ctrl+t", "orders", "enter") // back to the columns style, on a table
	r.selected("acme-prod.sales.orders")
	r.press("enter", "ctrl+d")
	if r.m.detOff != 19/2 {
		t.Fatalf("ctrl+d in the Schema pane scrolls %d, want %d", r.m.detOff, 19/2)
	}
	r.press("esc", "ctrl+w", "l")
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(200) }
	r.typeText("select many")
	r.press("ctrl+r", "ctrl+w", "j", "ctrl+d")
	if want := int64((19 - 3) / 2); r.m.grid().row != want || r.m.gridRows() != 16 {
		t.Fatalf("ctrl+d in the Results moves to row %d, want %d (grid rows %d)", r.m.grid().row, want, r.m.gridRows())
	}
}

func TestStatusLineNamesTheLayout(t *testing.T) {
	r := newRig(t, 120, 30)
	r.contains("exec acme-exec  layout: classic  columns")
	r.lacks("small terminal")
	r.press("ctrl+w", "l")
	r.contains("layout: classic  2 projects")
	r.press("ctrl+p", "Layout: focus", "enter")
	r.contains("layout: focus", "Explorer  Schema  [Editor]  Results")
	r.lacks("small terminal")
	r.press("ctrl+p", "Layout: classic", "enter")
	r.contains("layout: classic")

	small := newRig(t, 80, 24)
	small.contains("layout: classic (small terminal)")
}

func TestARunInTheFocusLayoutShowsTheResults(t *testing.T) {
	for _, c := range []struct {
		name   string
		w, h   int
		choose bool // choose focus in the palette
		want   Zone
	}{
		{"stepped down below 70 columns", 60, 20, false, ZoneResults},
		{"focus chosen", 120, 36, true, ZoneResults},
		{"classic", 120, 36, false, ZoneEditor},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := editorRig(t, c.w, c.h)
			if c.choose {
				r.press("ctrl+p", "Layout: focus", "enter")
			}
			r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
			r.typeText("select 1")
			r.press("ctrl+r")
			if r.m.zone != c.want {
				t.Fatalf("pane %s, want %s", r.m.zone, c.want)
			}
			if c.want == ZoneResults {
				r.contains("[Results]", "3 rows")
				r.press("ctrl+w", "k")
				r.contains("[Editor]", "select 1")
			}
		})
	}
}

func TestADryRunInTheFocusLayoutStaysInTheEditor(t *testing.T) {
	r := editorRig(t, 60, 20)
	r.typeText("select 1")
	r.press("ctrl+d")
	if r.m.zone != ZoneEditor {
		t.Fatalf("a dry-run has no result to show: pane %s", r.m.zone)
	}
	r.contains("[Editor]")
}

func TestLayoutIsRememberedAndTheStepDownIsNot(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "ctrl+p", "Layout: focus", "enter")
	r.contains("layout: focus")
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if m2.style != styleTree || m2.preset != "focus" {
		t.Fatalf("style %s layout %q", styleNames[m2.style], m2.preset)
	}

	small := newRig(t, 60, 20)
	small.press("L") // saves while the step-down applies
	b, err := os.ReadFile(small.m.be.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pane_layout":{"preset":"classic"}`) {
		t.Fatalf("the step-down is never saved: %s", b)
	}
	small.send(teaSize(120, 36))
	small.contains("layout: classic  tree", " SQL · query 1", " Results")
	small.lacks("small terminal", "ctrl+o next")
}

func TestViewsListTheirFieldsBeforeTheSQL(t *testing.T) {
	r := newRig(t, 120, 40)
	r.addViewWithFields()
	r.press("ctrl+t", "v_customers", "enter")
	r.selected("acme-prod.sales.v_customers")
	r.press("enter", "ctrl+w", "z")
	email := -1
	for i, l := range strings.Split(r.screen(), "\n") {
		if strings.Contains(l, " email ") && strings.Contains(l, "STRING") {
			email = i
		}
	}
	head, fields, sql, desc := r.line("view · EU"), r.line("── schema · 2 fields ──"), r.line("── view SQL ──"), r.line("Customers with an email")
	if head < 0 || fields <= head || email <= fields || sql <= email || desc <= sql {
		t.Fatalf("name and kind, then the fields, then the SQL, then the description (%d %d %d %d %d):\n%s", head, fields, email, sql, desc, r.screen())
	}
}

// addViewWithFields adds acme-prod.sales.v_customers, a view with columns, and
// syncs it (the shared v_orders fixture has none, and col: searches rely on that).
func (r *rig) addViewWithFields() {
	r.t.Helper()
	view := bqtest.Table("v_customers", "id", "email")
	view.Type, view.ViewSQL = api.TypeView, "SELECT id, email\nFROM `acme-prod.sales.customers`"
	view.Description = "Customers with an email"
	ds := &r.fake.Projects["acme-prod"].Datasets[0]
	ds.Tables = append(ds.Tables, view)
	r.press("R")
	r.contains("sync finished")
}

func TestLayoutGoldenScreens(t *testing.T) {
	r := newRig(t, 120, 36)
	r.addViewWithFields()
	r.press("ctrl+t", "v_customers", "enter")
	r.golden("classic_view_120x36")

	r = editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(12) }
	r.typeText("select n from numbers")
	r.press("ctrl+r")
	r.settle()
	r.press("ctrl+w", "h")
	r.onOrders()
	r.golden("classic_run_120x36")
	r.press("enter")
	r.golden("schema_focused_120x36")
	r.press("ctrl+w", "z")
	r.golden("schema_max_120x36")

	r = newRig(t, 80, 24)
	r.onOrders()
	r.golden("stepdown_80x24")
	r.press("enter")
	r.golden("stepdown_schema_80x24")

	r = newRig(t, 60, 20)
	r.golden("focus_60x20")
	r.press("ctrl+w", "l")
	r.golden("focus_editor_60x20")

	r = newRig(t, 120, 36)
	r.press("ctrl+p", "Layout: focus", "enter")
	r.golden("focus_chosen_120x36")
}
