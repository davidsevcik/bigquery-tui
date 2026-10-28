package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// hitScene is one arrangement of a terminal: the body rows are the
// terminal's less the status line, the footer and (when docked) the tutorial
// panel, which follow the body.
type hitScene struct {
	name       string
	a          arrangement
	w, termH   int
	bodyH      int
	tutorialOn bool
}

func hitScenes() []hitScene {
	var out []hitScene
	sizes := [][2]int{{120, 36}, {80, 24}, {60, 20}, {101, 31}, {70, 14}}
	add := func(name, preset string, tree layoutNode, focused, zoom Zone, w, h int, tut bool) {
		bodyH := h - 2
		if tut {
			bodyH -= tutorialRows
		}
		a := arrange(preset, tree, focused, zoom, paneOrder, w, bodyH)
		out = append(out, hitScene{fmt.Sprintf("%s %s %dx%d tutorial=%v", name, focused, w, h, tut), a, w, h, bodyH, tut})
	}
	custom, _ := swapPane(presetTree("classic"), ZoneEditor)
	hidden, _ := hidePane(presetTree("classic"), ZoneSchema)
	for _, s := range sizes {
		for _, z := range paneOrder {
			for _, p := range presets {
				add(p.name, p.name, presetTree(p.name), z, "", s[0], s[1], false)
			}
			add("custom", "classic", custom, z, "", s[0], s[1], false)
			add("maximised", "classic", presetTree("classic"), z, z, s[0], s[1], false)
		}
		add("hidden", "classic", hidden, ZoneExplorer, "", s[0], s[1], false)
		add("tutorial", "classic", presetTree("classic"), ZoneExplorer, "", s[0], s[1]+tutorialRows, true)
	}
	add("browse shared slot", "browse", presetTree("browse"), ZoneResults, "", 100, 28, false)
	return out
}

func TestPaneAtHitsExactlyTheDrawnRectangles(t *testing.T) {
	for _, sc := range hitScenes() {
		a := sc.a
		owner := func(x, y int) (Zone, int) {
			var got Zone
			n := 0
			for z, r := range a.rects {
				if x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h {
					got, n = z, n+1
				}
			}
			return got, n
		}
		for y := -1; y <= sc.termH; y++ {
			for x := -1; x <= sc.w; x++ {
				want, n := owner(x, y)
				if n > 1 {
					t.Fatalf("%s: cell %d,%d lies in %d Panes", sc.name, x, y, n)
				}
				z, ok := paneAt(a.rects, x, y)
				if ok != (n == 1) || z != want {
					t.Fatalf("%s: cell %d,%d hits %q %v, want %q", sc.name, x, y, z, ok, want)
				}
				if y >= sc.bodyH && ok {
					t.Fatalf("%s: row %d (status, footer or tutorial panel) hits %s", sc.name, y, z)
				}
			}
		}
		for _, s := range a.seps {
			for y := s.y; y < s.y+s.h; y++ {
				if z, ok := paneAt(a.rects, s.x, y); ok {
					t.Fatalf("%s: separator cell %d,%d hits %s", sc.name, s.x, y, z)
				}
			}
		}
		if b := a.bar; b != nil {
			for x := b.x; x < b.x+b.w; x++ {
				if z, ok := paneAt(a.rects, x, b.y); ok {
					t.Fatalf("%s: pane bar cell %d,%d hits %s", sc.name, x, b.y, z)
				}
			}
		}
		for z, r := range a.rects {
			for _, c := range [][2]int{{r.x, r.y}, {r.x + r.w - 1, r.y}, {r.x, r.y + r.h - 1}, {r.x + r.w - 1, r.y + r.h - 1}} {
				if got, ok := paneAt(a.rects, c[0], c[1]); !ok || got != z {
					t.Fatalf("%s: corner %v of %s hits %q", sc.name, c, z, got)
				}
			}
			for _, c := range [][2]int{{r.x - 1, r.y}, {r.x + r.w, r.y}, {r.x, r.y - 1}, {r.x, r.y + r.h}} {
				if got, _ := paneAt(a.rects, c[0], c[1]); got == z {
					t.Fatalf("%s: cell %v just outside %s %v hits it", sc.name, c, z, r)
				}
			}
		}
	}
}

func TestPaneAtSkipsSeparatorsInClassic(t *testing.T) {
	// classic at 120x34: the Explorer is 42 wide, then the separator at 42
	a := arrangeP("classic", ZoneExplorer, "", 120, 34)
	cases := []struct {
		x, y int
		want Zone
	}{
		{0, 0, ZoneExplorer}, {41, 16, ZoneExplorer}, {41, 17, ZoneSchema}, {42, 5, ""},
		{43, 0, ZoneEditor}, {119, 16, ZoneEditor}, {43, 17, ZoneResults}, {119, 33, ZoneResults},
		{0, 34, ""}, {120, 0, ""},
	}
	for _, c := range cases {
		if z, _ := paneAt(a.rects, c.x, c.y); z != c.want {
			t.Errorf("%d,%d: %q, want %q", c.x, c.y, z, c.want)
		}
	}
}

func TestPaneBarLabelsAreClickable(t *testing.T) {
	// " Explorer  Schema  [Editor]  Results   ctrl+o next"
	a := arrangeP("focus", ZoneEditor, "", 120, 34)
	cases := []struct {
		x, y int
		want Zone
	}{
		{0, 0, ""}, {1, 0, ZoneExplorer}, {8, 0, ZoneExplorer}, {9, 0, ""}, {10, 0, ""},
		{11, 0, ZoneSchema}, {16, 0, ZoneSchema}, {17, 0, ""},
		{19, 0, ZoneEditor}, {26, 0, ZoneEditor}, {29, 0, ZoneResults}, {35, 0, ZoneResults},
		{36, 0, ""}, {45, 0, ""}, {1, 1, ""},
	}
	for _, c := range cases {
		if z, _ := barAt(a, c.x, c.y); z != c.want {
			t.Errorf("%d,%d: %q, want %q", c.x, c.y, z, c.want)
		}
	}
	// browse's shared slot: the bar starts at its slot's row
	b := arrange("browse", presetTree("browse"), ZoneResults, "", []Zone{ZoneResults}, 100, 26)
	if z, ok := barAt(b, 1, 15); !ok || z != ZoneEditor {
		t.Fatalf("browse bar: %q %v", z, ok)
	}
	if z, ok := barAt(b, 12, 15); !ok || z != ZoneResults {
		t.Fatalf("browse bar: %q %v", z, ok)
	}
	// no bar, no labels
	if z, ok := barAt(arrangeP("classic", ZoneEditor, "", 120, 34), 1, 0); ok {
		t.Fatalf("classic has no pane bar: %q", z)
	}
}

// mouseOn turns the mouse on from the palette.
func (r *rig) mouseOn() {
	r.t.Helper()
	r.palette("Mouse: on/off")
	if !r.m.mouse {
		r.t.Fatal("the palette did not turn the mouse on")
	}
}

func TestTheMouseIsOffByDefaultAndTheToggleIsInThePalette(t *testing.T) {
	r := newRig(t, 120, 36)
	if got := r.m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("a fresh start asks for mouse mode %v", got)
	}
	r.press("ctrl+p", "mouse", "enter")
	if got := r.m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse on: mode %v", got)
	}
	r.contains("mouse on · Shift+drag selects text (Option+drag in iTerm2)")
	r.send(teaSize(80, 24))
	r.contains("mouse on · Shift+drag selects text (Option+drag in iTerm2)") // fits the 80-column footer
	r.press("ctrl+p", "mouse", "enter")
	if got := r.m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("mouse off: mode %v", got)
	}
	r.contains("mouse off · a plain drag selects text again")
}

func TestPopupsLeaveTheMouseToTheTerminal(t *testing.T) {
	r := newRig(t, 120, 36)
	r.mouseOn()
	open := map[string][]string{
		"palette": {"ctrl+p"}, "help": {"f1"}, "jump": {"ctrl+t"}, "run list": {"g", "r"},
		"buffers": {"ctrl+o", "ctrl+o", "ctrl+x", "b"}, "prompt": {"ctrl+o", "ctrl+o", "ctrl+s"},
	}
	for name, keys := range open {
		r.press(keys...)
		if r.m.ov == ovNone {
			t.Fatalf("%s: no popup open", name)
		}
		if got := r.m.View().MouseMode; got != tea.MouseModeNone {
			t.Fatalf("%s open: mode %v", name, got)
		}
		r.press("esc")
		if r.m.ov != ovNone {
			t.Fatalf("%s: esc left %v open", name, r.m.ov)
		}
		if got := r.m.View().MouseMode; got != tea.MouseModeCellMotion {
			t.Fatalf("%s closed: mode %v", name, got)
		}
		r.press("ctrl+w", "h", "ctrl+w", "k") // back to the Explorer
	}
}

func TestTheMouseSettingIsRemembered(t *testing.T) {
	r := newRig(t, 120, 36)
	r.press("ctrl+w", "space") // query: a phase 3 key in the file
	r.mouseOn()
	b, err := os.ReadFile(r.m.be.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"explorer_style":"columns","pane_layout":{"preset":"query"},"mouse":true}` {
		t.Fatalf("state file %s", got)
	}
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if !m2.mouse || m2.preset != "query" {
		t.Fatalf("restart: mouse %v layout %q", m2.mouse, m2.preset)
	}
	if m := r.restartWith(`{"explorer_style":"tree","pane_layout":{"preset":"browse"}}`); m.mouse || m.preset != "browse" || m.style != styleTree {
		t.Fatalf("a file without the key: mouse %v layout %q style %v", m.mouse, m.preset, m.style)
	}
	if m := r.restartWith(`{"explorer_style":"tree","mouse":"yes"}`); m.mouse || m.style != styleTree {
		t.Fatalf("a bad value is off and keeps the rest: mouse %v style %v", m.mouse, m.style)
	}
}

// pinned is what a click must not change: the focus, the selection, the
// Editor's cursor and text, and the Results' cell.
type pinned struct {
	zone          Zone
	sel           string
	row, col      int
	text          string
	gridRow, gCol int64
}

func (r *rig) pin() pinned {
	p := pinned{zone: r.m.zone, sel: r.m.sel().path.String(), row: r.m.buf().ed.row, col: r.m.buf().ed.col, text: r.text()}
	if g := r.m.grid(); g != nil {
		p.gridRow, p.gCol = g.row, int64(g.col)
	}
	return p
}

func (r *rig) unchanged(what string, before pinned) {
	r.t.Helper()
	if after := r.pin(); after != before {
		r.t.Fatalf("%s changed %+v to %+v", what, before, after)
	}
}

func TestAClickFocusesThePaneUnderIt(t *testing.T) {
	r := newRig(t, 120, 36)
	r.mouseOn()
	for _, z := range []Zone{ZoneResults, ZoneSchema, ZoneEditor, ZoneExplorer} {
		r.click(r.center(z))
		if r.m.zone != z {
			t.Fatalf("a click on the %s focuses %s", z, r.m.zone)
		}
		if r.m.mru[0] != z {
			t.Fatalf("the %s is not the most recently focused: %v", z, r.m.mru)
		}
	}
	// the title row is the Pane's too
	for _, z := range []Zone{ZoneEditor, ZoneSchema, ZoneResults, ZoneExplorer} {
		p := r.m.arr().rects[z]
		r.click(p.x+2, p.y)
		if r.m.zone != z {
			t.Fatalf("a click on the %s's title row focuses %s", z, r.m.zone)
		}
	}
	// the toast stays until the next key
	r.contains("mouse on · Shift+drag")
}

func TestAClickOnTheFocusedPaneOrOutsideThePanesChangesNothing(t *testing.T) {
	r := newRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(40) }
	r.onOrders()
	r.press("ctrl+o", "ctrl+o")
	r.typeText("select 1\nfrom t\nwhere x")
	r.press("ctrl+r")
	r.settle()
	r.press("ctrl+w", "j", "j", "j", "l")
	r.mouseOn()
	a := r.m.arr()
	before := r.pin()
	ed := a.rects[ZoneEditor]
	r.click(r.center(ZoneResults))
	r.unchanged("a click in the focused Results", before)
	sep := a.seps[0]
	r.click(sep.x, sep.y+3)
	r.click(5, r.m.termH-2) // the status line
	r.click(5, r.m.termH-1) // the footer
	r.unchanged("a click on a separator, the status line or the footer", before)
	for _, b := range []tea.MouseButton{tea.MouseRight, tea.MouseMiddle} {
		r.send(tea.MouseClickMsg{X: ed.x + 3, Y: ed.y + 2, Button: b})
	}
	r.send(tea.MouseReleaseMsg{X: ed.x + 3, Y: ed.y + 2, Button: tea.MouseLeft})
	r.send(tea.MouseMotionMsg{X: ed.x + 3, Y: ed.y + 2, Button: tea.MouseLeft})
	r.unchanged("a right or middle click, a release or a drag", before)
	// in the Editor, a click focuses it and never moves the cursor
	r.click(ed.x+ed.w-2, ed.y+1)
	if r.m.zone != ZoneEditor {
		t.Fatalf("the Editor is not focused: %s", r.m.zone)
	}
	before = r.pin()
	r.click(ed.x+10, ed.y+1)
	r.unchanged("a click in the focused Editor", before)

	// with the mouse off, or a popup open, a click does nothing
	r.palette("Mouse: on/off")
	before = r.pin()
	r.click(r.center(ZoneExplorer))
	r.unchanged("a click with the mouse off", before)
	r.palette("Mouse: on/off")
	r.press("f1")
	r.click(r.center(ZoneExplorer))
	if r.m.ov != ovHelp {
		t.Fatalf("the help closed: %v", r.m.ov)
	}
	r.unchanged("a click behind a popup", before)
}

func TestAClickClosesTheCompletionAndCancelsAChord(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.mouseOn()
	r.typeText("select * from `acme-p")
	if !r.m.comp.open {
		t.Fatal("precondition: the completion popup is open")
	}
	r.click(r.center(ZoneResults))
	if r.m.zone != ZoneResults || r.m.comp.open {
		t.Fatalf("zone %s, completion open %v", r.m.zone, r.m.comp.open)
	}
	r.lacks("tab accept")
	r.press("ctrl+w")
	r.click(r.center(ZoneExplorer))
	if r.m.zone != ZoneExplorer || r.m.pendingKey != "" {
		t.Fatalf("zone %s, pending %q", r.m.zone, r.m.pendingKey)
	}
	r.press("l") // a plain key again, not ctrl+w l
	if r.m.zone != ZoneExplorer {
		t.Fatalf("the chord survived the click: %s", r.m.zone)
	}
}

func TestAClickOnTheResultsLoadsWhatTheKeysWould(t *testing.T) {
	loads := func(focus func(r *rig)) int {
		r := editorRig(t, 120, 30)
		r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(40) }
		r.typeText("select 1")
		r.press("ctrl+r")
		r.settle()
		r.mouseOn()
		r.m.grid().pages = map[int64][][]api.Cell{} // dropped while away
		focus(r)
		if r.m.zone != ZoneResults {
			t.Fatalf("precondition: %s", r.m.zone)
		}
		r.contains("row 1 of 40", "row 9")
		return len(r.m.grid().pages)
	}
	keys := loads(func(r *rig) { r.press("ctrl+w", "j") })
	click := loads(func(r *rig) { r.click(r.center(ZoneResults)) })
	if keys != 1 || click != keys {
		t.Fatalf("the click loaded %d pages, the keys %d", click, keys)
	}
}

func TestAClickOnAPaneBarNameSwitchesToIt(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {60, 20}} {
		r := newRig(t, size[0], size[1])
		if size[0] == 120 {
			r.palette("Layout: focus")
		}
		r.mouseOn()
		for _, z := range []Zone{ZoneResults, ZoneSchema, ZoneEditor, ZoneExplorer} {
			bar := strings.Split(r.screen(), "\n")[0]
			x := strings.Index(bar, string(z))
			if x < 0 {
				t.Fatalf("%dx%d: the pane bar lacks %s: %q", size[0], size[1], z, bar)
			}
			r.click(x, 0)
			if r.m.zone != z {
				t.Fatalf("%dx%d: a click on %s in the bar focuses %s", size[0], size[1], z, r.m.zone)
			}
		}
		// beside the names, the bar does nothing
		before := r.pin()
		r.click(0, 0)
		r.click(size[0]-1, 0)
		r.unchanged("a click on the bar beside the names", before)
	}
}

func TestAClickThatReachesTheEditorAdvancesTheTutorial(t *testing.T) {
	r := newRig(t, 120, 36)
	r.mouseOn()
	r.press("ctrl+g")
	for tutSteps[r.m.tut.step].title != "Panes" {
		r.press("ctrl+g", "n")
	}
	panes := r.m.tut.step
	r.click(5, r.m.h-1+3) // the tutorial panel is below the body
	r.tutStep(panes)
	r.click(r.center(ZoneEditor))
	r.tutStep(panes + 1)
}

func TestTheWheelMovesTheExplorerUnderThePointer(t *testing.T) {
	r := newRig(t, 120, 36)
	r.press("L", "l", "j", "j", "j", "l", "g", "g") // the tree, acme-prod and sales open
	r.press("ctrl+w", "l")
	r.mouseOn()
	x, y := r.center(ZoneExplorer)
	r.wheel(x, y, tea.MouseWheelDown)
	r.selected("acme-prod.sales")
	r.wheel(x, y, tea.MouseWheelDown)
	r.selected("acme-prod.sales.orders")
	if r.m.zone != ZoneEditor {
		t.Fatalf("the wheel moved the focus to %s", r.m.zone)
	}
	r.contains("Schema · acme-prod.sales.orders", "partition filter required")
	r.wheel(x, y, tea.MouseWheelUp)
	r.wheel(x, y, tea.MouseWheelUp)
	r.selected("acme-prod")
	before := r.pin()
	r.wheel(x, y, tea.MouseWheelUp)
	r.unchanged("the wheel at the first row", before)
	for range 10 {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	r.selected("acme-staging")
	before = r.pin()
	r.wheel(x, y, tea.MouseWheelDown)
	r.unchanged("the wheel at the last row", before)
}

func TestTheWheelMovesTheColumnUnderThePointer(t *testing.T) {
	r := newRig(t, 120, 36)
	r.onOrders()
	r.press("ctrl+w", "z") // the Explorer maximised: all three columns
	r.mouseOn()
	// the datasets column is the middle one, after the projects column
	header := strings.Split(r.screen(), "\n")[1]
	cell := func(sub string) int {
		i := strings.Index(header, sub)
		if i < 0 {
			t.Fatalf("no %s column: %q", sub, header)
		}
		return utf8.RuneCountInString(header[:i])
	}
	x := cell("Datasets")
	r.wheel(x, 6, tea.MouseWheelUp)
	if r.m.mil.col != 2 || r.m.zone != ZoneExplorer {
		t.Fatalf("the focused column changed: column %d, zone %s", r.m.mil.col, r.m.zone)
	}
	if got := r.m.sel().path; got.Dataset != "events" {
		t.Fatalf("the tables column follows the dataset: %s", got)
	}
	// the wheel over the focused column moves its cursor
	r.wheel(cell("Tables")+2, 6, tea.MouseWheelDown)
	if r.m.mil.cur[2] != 1 || r.m.sel().path.Dataset != "events" {
		t.Fatalf("tables cursor %d, selection %s", r.m.mil.cur[2], r.m.sel().path)
	}
}

func TestTheWheelLoadsTheNextPageOfTheList(t *testing.T) {
	r := newRig(t, 120, 36)
	var many []api.TableMeta
	for i := range searchPage + 50 {
		many = append(many, bqtest.Table(fmt.Sprintf("t%04d", i), "id"))
	}
	p := r.fake.AddProject("acme-prod")
	p.Datasets = append(p.Datasets, bqtest.DS("wide", "EU", many...))
	r.press("R")
	r.press("L", "L") // the list
	r.press("ctrl+w", "l")
	r.mouseOn()
	if n, total := len(r.m.flat.results), r.m.flat.total; n != searchPage || total <= n {
		t.Fatalf("precondition: %d of %d results loaded", n, total)
	}
	x, y := r.center(ZoneExplorer)
	for range searchPage / wheelStep {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	if n, total := len(r.m.flat.results), r.m.flat.total; n != total {
		t.Fatalf("%d of %d results loaded after wheeling to the end", n, total)
	}
	if r.m.zone != ZoneEditor {
		t.Fatalf("focus %s", r.m.zone)
	}
}

func TestTheWheelScrollsTheSchemaPane(t *testing.T) {
	r := newRig(t, 120, 24)
	r.onOrders()
	r.mouseOn()
	x, y := r.center(ZoneSchema)
	r.wheel(x, y, tea.MouseWheelDown)
	if r.m.detOff != 3 || r.m.zone != ZoneExplorer {
		t.Fatalf("offset %d, zone %s", r.m.detOff, r.m.zone)
	}
	r.wheel(x, y, tea.MouseWheelUp)
	r.wheel(x, y, tea.MouseWheelUp)
	if r.m.detOff != 0 {
		t.Fatalf("offset %d at the top", r.m.detOff)
	}
	for range 30 {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	end := r.m.detOff
	r.lacks("orders ↓")
	r.wheel(x, y, tea.MouseWheelUp)
	if end == 0 || r.m.detOff != end-3 {
		t.Fatalf("one step up from the end (%d) is at %d", end, r.m.detOff)
	}
}

func TestTheWheelScrollsTheEditorWithoutEditing(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.mouseOn()
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("select %d", i))
	}
	r.typeText(strings.Join(lines, "\n") + "\nselect * from `acme-p")
	if !r.m.comp.open {
		t.Fatal("precondition: the completion popup is open")
	}
	e := r.m.buf().ed
	h := r.m.editorRows()
	text, undo, version := r.text(), len(e.undo), e.version
	r.screen() // the view follows the cursor when drawn
	top := e.top
	x, y := r.center(ZoneEditor)
	r.wheel(x, y, tea.MouseWheelUp)
	r.screen()
	if e.top != top-3 || r.m.comp.open {
		t.Fatalf("top %d (was %d), completion open %v", e.top, top, r.m.comp.open)
	}
	if e.row < e.top || e.row >= e.top+h {
		t.Fatalf("the cursor row %d left the view [%d, %d)", e.row, e.top, e.top+h)
	}
	if r.text() != text || len(e.undo) != undo || e.version != version {
		t.Fatal("the wheel edited the buffer")
	}
	// unfocused, the cursor stays where it is while it is in view
	r.press("ctrl+w", "h")
	for range 30 {
		r.wheel(x, y, tea.MouseWheelUp)
	}
	r.screen()
	if e.top != 0 || e.row != h-1 || r.m.zone != ZoneExplorer {
		t.Fatalf("top %d row %d zone %s", e.top, e.row, r.m.zone)
	}
	r.wheel(x, y, tea.MouseWheelDown)
	r.screen()
	if e.top != 3 || e.row != h-1 {
		t.Fatalf("top %d row %d: the cursor moved while in view", e.top, e.row)
	}
	for range 40 {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	r.screen()
	if want := len(e.lines) - h; e.top != want {
		t.Fatalf("top %d, want %d at the end", e.top, want)
	}
}

func TestTheWheelMovesTheResultsUnderThePointer(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(gridPage + 100) }
	r.typeText("select many")
	r.press("ctrl+r")
	r.settle()
	r.mouseOn()
	x, y := r.center(ZoneResults)
	r.wheel(x, y, tea.MouseWheelUp)
	g := r.m.grid()
	if g.row != 0 {
		t.Fatalf("row %d after the wheel at the first row", g.row)
	}
	r.wheel(x, y, tea.MouseWheelDown)
	if g.row != 3 || r.m.zone != ZoneEditor {
		t.Fatalf("row %d, zone %s", g.row, r.m.zone)
	}
	for range gridPage / wheelStep {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	if _, ok := g.pages[gridPage]; !ok {
		t.Fatal("the next page was not loaded")
	}
	r.contains(fmt.Sprintf("row %d of", g.row+1))
	r.wheel(x, y, tea.MouseWheelRight)
	if g.col != 1 {
		t.Fatalf("column %d after the sideways wheel", g.col)
	}
	r.wheel(x, y, tea.MouseWheelLeft)
	r.wheel(x, y, tea.MouseWheelLeft)
	if g.col != 0 {
		t.Fatalf("column %d", g.col)
	}
}

func TestTheWheelPagesThroughASample(t *testing.T) {
	r := newRig(t, 120, 30)
	var rows [][]any
	for i := range gridPage + 100 {
		rows = append(rows, []any{fmt.Sprint(i + 1), "x"})
	}
	r.fake.TableData["acme-prod.sales.customers"] = bqtest.TableData{
		Info: bq.TableInfo{Type: api.TypeTable, Schema: []bq.Field{{Name: "id", Type: "INTEGER"}, {Name: "email", Type: "STRING"}}},
		Rows: rows,
	}
	r.onSalesTable(0, "acme-prod.sales.customers")
	r.press("p")
	r.mouseOn()
	before := r.calls("ListRows")
	x, y := r.center(ZoneResults)
	for range gridPage/wheelStep + 1 {
		r.wheel(x, y, tea.MouseWheelDown)
	}
	if n := r.calls("ListRows") - before; n != 1 {
		t.Fatalf("%d page reads, want the next page once", n)
	}
	r.contains(fmt.Sprintf("row %d of 600", (gridPage/wheelStep+1)*wheelStep+1))
	if r.m.zone != ZoneExplorer {
		t.Fatalf("focus %s", r.m.zone)
	}
}

func TestTheWheelIsQuietWhereItHasNothingToDo(t *testing.T) {
	r := newRig(t, 120, 36)
	r.mouseOn()
	r.press("j") // clears the toast
	before := r.pin()
	x, y := r.center(ZoneResults)
	r.wheel(x, y, tea.MouseWheelDown)
	r.wheel(x, y, tea.MouseWheelRight)
	r.unchanged("the wheel over empty Results", before)
	if r.m.toast != "" {
		t.Fatalf("toast %q", r.m.toast)
	}
	sep := r.m.arr().seps[0]
	r.wheel(sep.x, sep.y+2, tea.MouseWheelDown)
	r.wheel(3, r.m.termH-1, tea.MouseWheelDown)
	r.unchanged("the wheel on a separator or the footer", before)
	// off, or behind a popup
	ex, ey := r.center(ZoneExplorer)
	r.press("f1")
	r.wheel(ex, ey, tea.MouseWheelDown)
	r.press("esc")
	r.unchanged("the wheel behind a popup", before)
	r.palette("Mouse: on/off")
	r.wheel(ex, ey, tea.MouseWheelDown)
	r.unchanged("the wheel with the mouse off", before)
}

func TestTheWheelKeepsVimsCursorOnACharacter(t *testing.T) {
	rigEditing = "vim"
	defer func() { rigEditing = "plain" }()
	r := newRig(t, 120, 30)
	r.mouseOn()
	r.press("ctrl+w", "l")
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("select %d", i))
	}
	r.send(tea.PasteMsg{Content: strings.Join(lines, "\n")})
	r.press("v") // a Visual selection follows the cursor
	r.screen()
	x, y := r.center(ZoneEditor)
	for range 5 {
		r.wheel(x, y, tea.MouseWheelUp)
	}
	r.screen()
	e := r.m.buf().ed
	if r.m.buf().vim.mode != vimVisual || e.row != e.top+r.m.editorRows()-1 || e.col > len(e.lines[e.row])-1 {
		t.Fatalf("mode %v, cursor %d:%d, top %d", r.m.buf().vim.mode, e.row, e.col, e.top)
	}
	if sel := e.selection(); strings.Count(sel, "\n") != 59-e.row || !strings.HasSuffix(sel, "select 59") {
		t.Fatalf("the Visual selection does not follow the cursor: %q", sel)
	}
}

func TestTheHelpExplainsTheMouse(t *testing.T) {
	var text []string
	for _, l := range helpLines(90) {
		text = append(text, strings.TrimSpace(ansi.Strip(l)))
	}
	at := func(s string) int {
		for i, l := range text {
			if strings.HasPrefix(l, s) {
				return i
			}
		}
		t.Fatalf("the help lacks %q", s)
		return -1
	}
	if !(at("Results") < at("Mouse") && at("Mouse") < at("Filter and jump")) {
		t.Fatal("the Mouse section is not after the Pane sections")
	}
	all := strings.Join(text, " ")
	for _, want := range []string{"Mouse: on/off", "off by default", "the wheel scrolls the Pane under the pointer", "Shift+drag", "Option+drag in iTerm2", "Popups leave the mouse to the terminal"} {
		if !strings.Contains(all, want) {
			t.Errorf("the help lacks %q", want)
		}
	}
	if !strings.Contains(tutSteps[len(tutSteps)-1].text(nil), `"Mouse: on/off"`) {
		t.Error("the tutorial's last step does not mention the mouse")
	}
}

// Codex review of phase 4: a view scrolled further than the Editor's new
// height allows (it was grown or maximised) must not jump back up on a
// wheel-down.
func TestTheWheelNeverScrollsAgainstItsDirection(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "select " + strconv.Itoa(i)
	}
	e := newEditor(strings.Join(lines, "\n"))
	e.top, e.row = 90, 99 // scrolled in a 10-row view
	e.scrollView(3, 30)   // the view is 30 rows now
	if e.top < 90 {
		t.Fatalf("a wheel-down moved the view up to line %d", e.top)
	}
	e.scrollView(-3, 30)
	if e.top > 90 {
		t.Fatalf("a wheel-up moved the view down to line %d", e.top)
	}
}
