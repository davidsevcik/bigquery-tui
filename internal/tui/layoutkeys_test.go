package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
)

func (r *rig) tree(want string) {
	r.t.Helper()
	if got := r.m.panes.String(); got != want {
		r.t.Fatalf("tree %s, want %s", got, want)
	}
}

func (r *rig) palette(name string) {
	r.t.Helper()
	r.press("ctrl+p", name, "enter")
}

func TestCtrlWSpaceCyclesTheLayouts(t *testing.T) {
	r := newRig(t, 120, 36)
	for _, want := range []string{"query", "browse", "tall", "focus", "classic"} {
		r.press("ctrl+w", "space")
		r.contains("layout: " + want)
		if r.m.preset != want || isCustom(want, r.m.panes) {
			t.Fatalf("%s: preset %s tree %s", want, r.m.preset, r.m.panes)
		}
	}
	for _, want := range []string{"query", "browse", "tall", "focus", "classic"} {
		r.palette("Layout: " + want)
		r.contains("layout: " + want)
	}
	r.palette("Next layout")
	r.contains("layout: query")
	r.palette("Layout: classic")
	r.onOrders()
	r.press("enter", "ctrl+w", "space")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("focus %s", r.m.zone)
	}
	r.palette("Show Schema")
	if r.m.zone != ZoneSchema || r.m.panes.hiddenPane(ZoneSchema) {
		t.Fatalf("focus %s tree %s", r.m.zone, r.m.panes)
	}
	r.contains("layout: custom", " Schema · acme-prod.sales.ord")
	r.tree("row[col[E:60 S:40]:25 col[Q:50 R:50]:75]")
	r.press("ctrl+w", "c")
	r.contains("layout: query")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("hiding moves the focus to the most recent visible Pane: %s", r.m.zone)
	}
}

func TestEditingALayoutMakesItCustomUntilUndone(t *testing.T) {
	r := newRig(t, 120, 36)
	r.press("ctrl+w", "J")
	r.contains("layout: custom")
	r.tree("col[row[S:35 col[Q:50 R:50]:65]:67 E:33]")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("the focus stays on the moved Pane: %s", r.m.zone)
	}
	r.palette("Reset layout")
	r.contains("layout: classic")
	r.tree(classicStr)

	r.press("ctrl+w", "x")
	r.contains("layout: custom")
	r.tree("row[col[S:50 E:50]:35 col[Q:50 R:50]:65]")
	r.press("ctrl+w", "x")
	r.contains("layout: classic")

	r.press("ctrl+w", ">")
	r.tree("row[col[E:50 S:50]:40 col[Q:50 R:50]:60]")
	r.press("ctrl+w", "<")
	r.contains("layout: classic")
	r.press("ctrl+w", "+")
	r.tree("row[col[E:55 S:45]:35 col[Q:50 R:50]:65]")
	r.press("ctrl+w", "-")
	r.tree(classicStr)
	r.press("ctrl+w", "=")
	r.tree("row[col[E:50 S:50]:50 col[Q:50 R:50]:50]")
	r.press("ctrl+w", "r")
	r.tree("row[col[S:50 E:50]:50 col[Q:50 R:50]:50]")

	r.palette("Next layout")
	r.contains("layout: query")
	r.tree("row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]")
}

func TestPaletteRunsTheWindowCommands(t *testing.T) {
	r := newRig(t, 120, 36)
	for _, c := range []struct{ name, tree string }{
		{"Move pane to the far left", "row[E:33 S:23 col[Q:50 R:50]:43]"},
		{"Move pane to the far right", "row[S:23 col[Q:50 R:50]:43 E:33]"},
		{"Move pane to the top", "col[E:33 row[S:35 col[Q:50 R:50]:65]:67]"},
		{"Move pane to the bottom", "col[row[S:35 col[Q:50 R:50]:65]:67 E:33]"},
		{"Swap pane with the next", "row[col[S:50 E:50]:35 col[Q:50 R:50]:65]"},
		{"Rotate panes", "row[col[S:50 E:50]:35 col[Q:50 R:50]:65]"},
		{"Wider pane", "row[col[E:50 S:50]:40 col[Q:50 R:50]:60]"},
		{"Narrower pane", "row[col[E:50 S:50]:30 col[Q:50 R:50]:70]"},
		{"Taller pane", "row[col[E:55 S:45]:35 col[Q:50 R:50]:65]"},
		{"Shorter pane", "row[col[E:45 S:55]:35 col[Q:50 R:50]:65]"},
		{"Equalise panes", "row[col[E:50 S:50]:50 col[Q:50 R:50]:50]"},
		{"Hide pane", "row[col[(E):50 S:50]:35 col[Q:50 R:50]:65]"},
	} {
		r.palette("Reset layout")
		r.press("ctrl+w", "k")
		r.palette(c.name)
		if got := r.m.panes.String(); got != c.tree {
			t.Errorf("%s: %s, want %s", c.name, got, c.tree)
		}
	}
	r.palette("Show Explorer")
	if r.m.zone != ZoneExplorer || r.m.panes.hiddenPane(ZoneExplorer) {
		t.Fatalf("Show Explorer: %s %s", r.m.zone, r.m.panes)
	}
	r.palette("Maximise or restore pane")
	if r.m.maxed != ZoneExplorer {
		t.Fatalf("maximise: %q", r.m.maxed)
	}
	r.palette("Show Results")
	if r.m.zone != ZoneResults || r.m.maxed != ZoneResults {
		t.Fatalf("Show moves the zoom: %s %q", r.m.zone, r.m.maxed)
	}
	r.palette("Maximise or restore pane")
	if r.m.maxed != "" {
		t.Fatalf("restore: %q", r.m.maxed)
	}
}

func TestTheWindowCommandsSayWhyTheyDoNothing(t *testing.T) {
	r := newRig(t, 120, 36)
	for _, k := range []string{"c", "c", "c"} {
		r.press("ctrl+w", k)
	}
	if got := r.m.panes.visiblePanes(); len(got) != 1 {
		t.Fatalf("three hidden: %v", got)
	}
	r.press("ctrl+w", "c")
	r.contains("the last visible pane cannot be hidden")
	r.press("ctrl+w", "x")
	r.contains("nothing to swap with")

	r = newRig(t, 120, 36)
	r.palette("Layout: focus")
	before := r.m.panes.String()
	for _, k := range []string{"H", "x", "r", "<", "+", "=", "z", "c"} {
		r.press("ctrl+w", k)
		r.contains("one pane at a time in the focus layout: ctrl+w space for another")
		if r.m.panes.String() != before || r.m.maxed != "" {
			t.Fatalf("ctrl+w %s changed the focus layout: %s %q", k, r.m.panes, r.m.maxed)
		}
	}

	r = newRig(t, 60, 20)
	for _, k := range []string{"J", "x", "r", ">", "-", "=", "z", "c"} {
		r.press("ctrl+w", k)
		r.contains("the terminal is too small to arrange panes")
	}
	if r.m.layoutName() != "classic" {
		t.Fatalf("layout %s", r.m.layoutName())
	}
	r.press("ctrl+w", "space")
	if r.m.layoutName() != "query" {
		t.Fatalf("layout %s", r.m.layoutName())
	}
	r.palette("Show Schema")
	if r.m.zone != ZoneSchema {
		t.Fatalf("Show Schema: %s", r.m.zone)
	}

	r = newRig(t, 120, 36)
	r.press("ctrl+w", "H", "ctrl+w", "H")
	r.contains("Explorer is already at the far left")
	r = newRig(t, 100, 36)
	r.press("ctrl+w", "<", "ctrl+w", "<")
	r.contains("Schema is at its minimum size")
}

func TestARunRestoresAMaximisedLayout(t *testing.T) {
	for _, dry := range []bool{false, true} {
		r := editorRig(t, 120, 36)
		r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
		r.typeText("select 1")
		r.press("ctrl+w", "j", "ctrl+w", "c", "ctrl+w", "z")
		if r.m.maxed != ZoneEditor || !r.m.panes.hiddenPane(ZoneResults) {
			t.Fatalf("maxed %q tree %s", r.m.maxed, r.m.panes)
		}
		r.contains("layout: custom (maximised)")
		if dry {
			r.press("ctrl+d")
		} else {
			r.press("ctrl+r")
		}
		if r.m.maxed != "" || r.m.panes.hiddenPane(ZoneResults) || r.m.zone != ZoneEditor {
			t.Fatalf("dry %v: maxed %q tree %s focus %s", dry, r.m.maxed, r.m.panes, r.m.zone)
		}
		r.contains("layout: classic", " SQL · query 1", "Projects 1/2")
	}
	r := editorRig(t, 120, 36)
	r.typeText("select @x")
	r.press("ctrl+w", "z", "ctrl+r")
	r.contains("Query parameters")
	r.press("esc")
	if r.m.maxed != ZoneEditor {
		t.Fatalf("maxed %q", r.m.maxed)
	}
	r.press("ctrl+r")
	r.typeText("1")
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	r.press("enter")
	if r.m.maxed != "" {
		t.Fatalf("maxed %q", r.m.maxed)
	}
}

func TestContentBringsItsPaneBack(t *testing.T) {
	r := newRig(t, 120, 36)
	r.press("ctrl+w", "l", "ctrl+w", "j", "ctrl+w", "c")
	if !r.m.panes.hiddenPane(ZoneResults) || r.m.zone != ZoneEditor {
		t.Fatalf("tree %s focus %s", r.m.panes, r.m.zone)
	}
	r.press("ctrl+w", "h")
	r.onOrders()
	r.press("p")
	if r.m.panes.hiddenPane(ZoneResults) || r.m.zone != ZoneExplorer {
		t.Fatalf("p shows the Results, the focus stays: tree %s focus %s", r.m.panes, r.m.zone)
	}
	r.contains(sampleTitle, "layout: classic")
	r.press("ctrl+w", "z", "p")
	if r.m.maxed != "" || r.m.zone != ZoneExplorer {
		t.Fatalf("p clears the zoom over the Results: %q %s", r.m.maxed, r.m.zone)
	}
	r.press("ctrl+w", "l", "ctrl+w", "j", "ctrl+w", "z")
	r.m.zone = ZoneExplorer
	r.m.maxed = ZoneResults
	r.run(r.m.sampleAction())
	if r.m.maxed != ZoneResults {
		t.Fatalf("the target itself maximised keeps the zoom: %q", r.m.maxed)
	}

	r = newRig(t, 100, 28)
	r.onOrders()
	r.palette("Layout: browse")
	r.contains("[Editor]  Results")
	r.press("p")
	if r.m.zone != ZoneExplorer {
		t.Fatalf("focus %s", r.m.zone)
	}
	r.contains("Editor  [Results]", sampleTitle)

	for _, key := range []string{"s", "i"} {
		r := newRig(t, 120, 36)
		r.press("ctrl+w", "l", "ctrl+w", "c", "ctrl+w", "h")
		if !r.m.panes.hiddenPane(ZoneEditor) {
			t.Fatalf("tree %s", r.m.panes)
		}
		r.onOrders()
		r.press(key)
		if r.m.panes.hiddenPane(ZoneEditor) || r.m.zone != ZoneEditor {
			t.Fatalf("%s shows the Editor: tree %s focus %s", key, r.m.panes, r.m.zone)
		}
		r.contains("orders")
	}

	r = newRig(t, 120, 36)
	r.press("ctrl+w", "space", "ctrl+e")
	r.contains("the Schema pane is hidden (palette: Show Schema)")

	r = editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(2) }
	r.typeText("select 2")
	r.press("ctrl+r")
	r.settle()
	r.press("ctrl+w", "j", "ctrl+w", "c", "ctrl+x", "l", "enter")
	if r.m.panes.hiddenPane(ZoneResults) || r.m.zone != ZoneResults {
		t.Fatalf("tree %s focus %s", r.m.panes, r.m.zone)
	}
}

func TestACustomLayoutIsRememberedWithoutTheZoom(t *testing.T) {
	r := newRig(t, 120, 36)
	r.press("ctrl+w", "space", "ctrl+w", "space")
	r.press("ctrl+w", "L", "ctrl+w", "j", "ctrl+w", "c", "ctrl+w", "z")
	want := r.m.panes.String()
	if !isCustom("browse", r.m.panes) || r.m.maxed == "" {
		t.Fatalf("tree %s maxed %q", want, r.m.maxed)
	}
	b, err := os.ReadFile(r.m.be.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pane_layout":{"preset":"browse","tree":{`) || !strings.Contains(string(b), `"hidden":true`) || strings.Contains(string(b), "zoom") || strings.Contains(string(b), "max") {
		t.Fatalf("state file %s", b)
	}
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if m2.preset != "browse" || m2.panes.String() != want || m2.maxed != "" {
		t.Fatalf("restart: preset %s tree %s maxed %q", m2.preset, m2.panes, m2.maxed)
	}
	m3 := r.restartWith(`{"explorer_style":"columns","pane_layout":{"preset":"classic","tree":{"split":"row","weights":[1,1],"kids":[{"pane":"explorer","hidden":true},{"split":"col","weights":[1,1,1],"kids":[{"pane":"schema"},{"pane":"editor"},{"pane":"results"}]}]}}}`)
	if m3.zone == ZoneExplorer || m3.layoutName() != "custom" {
		t.Fatalf("focus %s layout %s", m3.zone, m3.layoutName())
	}
	for state, want := range map[string]string{
		`{"pane_layout":{"preset":"wide","tree":{"pane":"explorer"}}}`: classicStr,
		`{"pane_layout":{"preset":"tall","tree":{"split":"row","weights":[1,1,1,1],"kids":[{"pane":"explorer","hidden":true},{"pane":"schema","hidden":true},{"pane":"editor","hidden":true},{"pane":"results","hidden":true}]}}}`: presetTree("tall").String(),
		`{"pane_layout":{"preset":"query","tree":{"split":"row","weights":[1],"kids":[{"pane":"explorer"}]}}}`:                                                                                                                     presetTree("query").String(),
		`{"pane_layout":{"preset":"browse","tree":7}}`: presetTree("browse").String(),
	} {
		if m := r.restartWith(state); m.panes.String() != want {
			t.Errorf("%s: %s, want %s", state, m.panes, want)
		}
	}
}

func TestWindowCommandsDoNotClashWithPaneKeys(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
	r.typeText("select n")
	r.press("ctrl+r")
	r.settle()
	r.press("ctrl+w", "j", "ctrl+w", "H")
	if len(r.m.grid().hidden) != 0 || r.m.zone != ZoneResults {
		t.Fatalf("hidden columns %v focus %s", r.m.grid().hidden, r.m.zone)
	}
	r.tree("row[R:33 col[E:50 S:50]:23 Q:43]")
	r = newRig(t, 120, 36)
	syncs := len(r.fake.Calls)
	r.press("ctrl+w", "L", "ctrl+w", "r")
	if r.m.style != styleColumns || len(r.fake.Calls) != syncs || r.m.syncPhase != syncIdle {
		t.Fatalf("style %s, %d calls, syncing %v", styleNames[r.m.style], len(r.fake.Calls)-syncs, r.m.syncPhase != syncIdle)
	}
	r.tree("row[E:23 S:43 col[Q:50 R:50]:33]")
}

func TestWindowCommandsInEveryEditingMode(t *testing.T) {
	type mode struct {
		name  string
		vim   bool
		enter []string
		want  vimMode
	}
	for _, md := range []mode{
		{"normal", true, nil, vimNormal},
		{"insert", true, []string{"A"}, vimInsert},
		{"visual", true, []string{"v"}, vimVisual},
		{"plain", false, nil, vimNormal},
	} {
		t.Run(md.name, func(t *testing.T) {
			var r *rig
			if md.vim {
				r = vimRig(t, 120, 36)
				r.press("i")
				r.typeText("select xr")
				r.press("esc")
			} else {
				r = editorRig(t, 120, 36)
				r.typeText("select xr")
			}
			r.press(md.enter...)
			text := r.text()
			for _, c := range []struct{ key, tree string }{
				{"x", "row[col[E:50 S:50]:35 col[R:50 Q:50]:65]"},
				{"r", classicStr},
				{"<", "row[col[E:50 S:50]:40 col[Q:50 R:50]:60]"},
				{"c", "row[col[E:50 S:50]:40 col[(Q):50 R:50]:60]"},
			} {
				r.press("ctrl+w", c.key)
				if r.m.panes.String() != c.tree {
					t.Fatalf("ctrl+w %s: %s, want %s", c.key, r.m.panes, c.tree)
				}
				if c.key == "c" {
					r.palette("Show Editor")
				}
				if r.text() != text || r.m.zone != ZoneEditor || (md.vim && r.m.buf().vim.mode != md.want) {
					t.Fatalf("ctrl+w %s: text %q mode %s focus %s", c.key, r.text(), r.m.buf().vim.mode, r.m.zone)
				}
			}
			r.press("ctrl+w", "space")
			r.contains("layout: query")
			if r.text() != text || r.m.ov != ovNone || (md.vim && (r.m.buf().vim.mode != md.want || r.m.buf().vim.wait != "")) {
				t.Fatalf("ctrl+w space: text %q mode %s overlay %v", r.text(), r.m.buf().vim.mode, r.m.ov)
			}
		})
	}
}

func TestPresetGoldenScreens(t *testing.T) {
	for _, c := range []struct {
		preset string
		sizes  [][2]int
	}{
		{"query", [][2]int{{120, 36}, {80, 24}, {60, 20}}},
		{"browse", [][2]int{{120, 36}, {80, 24}, {60, 20}}},
		{"tall", [][2]int{{120, 36}, {90, 40}, {80, 24}, {60, 20}}},
	} {
		for _, sz := range c.sizes {
			r := newRig(t, sz[0], sz[1])
			r.onOrders()
			r.palette("Layout: " + c.preset)
			r.golden("layout_" + c.preset + "_" + itoa(sz[0]) + "x" + itoa(sz[1]))
		}
	}
	r := newRig(t, 100, 28)
	r.onOrders()
	r.palette("Layout: browse")
	r.golden("layout_browse_short_100x28")
	r.press("ctrl+w", "j", "ctrl+w", "j")
	r.golden("layout_browse_short_results_100x28")

	r = editorRig(t, 120, 36)
	r.typeText("select n from numbers")
	r.press("ctrl+w", "z")
	r.golden("layout_maximised_120x36")

	r = newRig(t, 120, 36)
	r.onOrders()
	r.press("enter", "ctrl+w", "c")
	r.golden("layout_hidden_schema_120x36")

	r = newRig(t, 120, 36)
	r.onOrders()
	r.press("ctrl+w", "J")
	r.golden("layout_custom_120x36")
}

func TestResizingBetweenTheSharedEditorAndResultsIsRefused(t *testing.T) {
	r := newRig(t, 100, 28)
	r.press("ctrl+p", "Layout: browse", "enter", "ctrl+w", "j")
	if r.m.zone != ZoneEditor {
		t.Fatalf("pane %s", r.m.zone)
	}
	r.press("ctrl+w", "+")
	r.contains("layout: browse")
	r.lacks("custom")
	if !strings.Contains(r.m.toast, "share") {
		t.Fatalf("toast %q", r.m.toast)
	}
}
