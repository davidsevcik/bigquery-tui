package tui

import (
	"testing"
)

func TestClassicArrangesFourPanes(t *testing.T) {
	a := arrange("classic", ZoneExplorer, "", 120, 34)
	want := map[Zone]rect{
		ZoneExplorer: {0, 0, 42, 17},
		ZoneSchema:   {0, 17, 42, 17},
		ZoneEditor:   {43, 0, 77, 17},
		ZoneResults:  {43, 17, 77, 17},
	}
	checkRects(t, a.rects, want)
	if len(a.seps) != 1 || a.seps[0] != (rect{42, 0, 1, 34}) {
		t.Fatalf("one separator between the columns: %v", a.seps)
	}
	if a.bar != nil || a.stepped {
		t.Fatalf("no pane bar, no step-down: bar %v stepped %v", a.bar, a.stepped)
	}
	checkRects(t, a.nav, want)
	checkInvariants(t, a, 120, 34)
}

func checkRects(t *testing.T, got, want map[Zone]rect) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("panes %v, want %v", got, want)
	}
	for z, r := range want {
		if got[z] != r {
			t.Errorf("%s: %v, want %v", z, got[z], r)
		}
	}
}

// checkInvariants holds for every arrangement: the drawn Panes, separators and
// pane bar tile the body exactly, each drawn Pane meets its minimum (unless
// the Layout is focus, which has no minimum but the body), and the cycle keeps
// the order Explorer, Schema, Editor, Results.
func checkInvariants(t *testing.T, a arrangement, w, h int) {
	t.Helper()
	cells := make([]int, w*h)
	cover := func(what string, r rect) {
		if r.x < 0 || r.y < 0 || r.w <= 0 || r.h <= 0 || r.x+r.w > w || r.y+r.h > h {
			t.Errorf("%s %v lies outside the %dx%d body", what, r, w, h)
			return
		}
		for y := r.y; y < r.y+r.h; y++ {
			for x := r.x; x < r.x+r.w; x++ {
				cells[y*w+x]++
			}
		}
	}
	for z, r := range a.rects {
		cover(string(z), r)
		if a.bar == nil && len(a.rects) > 1 {
			mn := paneMin[z]
			if r.w < mn.w || r.h < mn.h {
				t.Errorf("%s %v is below its minimum %dx%d", z, r, mn.w, mn.h)
			}
		}
	}
	for _, s := range a.seps {
		cover("separator", s)
	}
	if a.bar != nil {
		cover("pane bar", *a.bar)
	}
	for i, n := range cells {
		if n != 1 {
			t.Fatalf("cell %d,%d is covered %d times", i%w, i/w, n)
		}
	}
	order := map[Zone]int{ZoneExplorer: 0, ZoneSchema: 1, ZoneEditor: 2, ZoneResults: 3}
	for i := 1; i < len(a.cycle); i++ {
		if order[a.cycle[i-1]] >= order[a.cycle[i]] {
			t.Fatalf("cycle out of order: %v", a.cycle)
		}
	}
}

func TestSmallTerminalsStepDown(t *testing.T) {
	// the classic tree at that size, with nothing left out
	full := func(w, h int) map[Zone]rect { return classic(w, h, nil).rects }
	cases := []struct {
		name    string
		w, h    int
		focused Zone
		drawn   []Zone // the Panes drawn
		bar     bool
		stepped bool
		cycle   []Zone
	}{
		{"100 columns keep the Schema", 100, 22, ZoneExplorer, []Zone{ZoneExplorer, ZoneSchema, ZoneEditor, ZoneResults}, false, false, paneOrder},
		{"99 columns leave the Schema out", 99, 22, ZoneExplorer, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}, false, true, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}},
		{"70 columns still show two columns", 70, 22, ZoneEditor, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}, false, true, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}},
		{"69 columns show one Pane", 69, 22, ZoneEditor, []Zone{ZoneEditor}, true, true, paneOrder},
		{"a short body leaves the Schema out", 120, 8, ZoneExplorer, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}, false, true, []Zone{ZoneExplorer, ZoneEditor, ZoneResults}},
		{"a shorter body falls back to focus", 120, 6, ZoneResults, []Zone{ZoneResults}, true, true, paneOrder},
		{"a body shortened by the tutorial panel", 120, 27, ZoneExplorer, []Zone{ZoneExplorer, ZoneSchema, ZoneEditor, ZoneResults}, false, false, paneOrder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := arrange("classic", c.focused, "", c.w, c.h)
			checkInvariants(t, a, c.w, c.h)
			if len(a.rects) != len(c.drawn) {
				t.Fatalf("drawn %v, want %v", a.rects, c.drawn)
			}
			for _, z := range c.drawn {
				if _, ok := a.rects[z]; !ok {
					t.Fatalf("%s is not drawn: %v", z, a.rects)
				}
			}
			if (a.bar != nil) != c.bar || a.stepped != c.stepped {
				t.Fatalf("bar %v stepped %v, want bar %v stepped %v", a.bar, a.stepped, c.bar, c.stepped)
			}
			if !equalZones(a.cycle, c.cycle) {
				t.Fatalf("cycle %v, want %v", a.cycle, c.cycle)
			}
			if c.bar {
				checkRects(t, a.nav, full(c.w, c.h))
			} else {
				checkRects(t, a.nav, a.rects)
			}
		})
	}
	// without the Schema, the Explorer takes the left column's full height
	if r := arrange("classic", ZoneExplorer, "", 99, 22).rects[ZoneExplorer]; r != (rect{0, 0, 34, 22}) {
		t.Fatalf("explorer %v", r)
	}
}

func TestFocusLayoutDrawsTheFocusedPaneUnderABar(t *testing.T) {
	for _, z := range paneOrder {
		a := arrange("focus", z, "", 120, 34)
		checkInvariants(t, a, 120, 34)
		checkRects(t, a.rects, map[Zone]rect{z: {0, 1, 120, 33}})
		if a.bar == nil || *a.bar != (rect{0, 0, 120, 1}) || a.stepped {
			t.Fatalf("%s: bar %v stepped %v", z, a.bar, a.stepped)
		}
		checkRects(t, a.nav, arrange("classic", z, "", 120, 34).rects)
		if !equalZones(a.cycle, paneOrder) {
			t.Fatalf("every Pane is reachable in focus: %v", a.cycle)
		}
	}
	// the focus preset never steps down
	if a := arrange("focus", ZoneEditor, "", 60, 18); a.stepped || len(a.rects) != 1 {
		t.Fatalf("%+v", a)
	}
}

func TestMaximisedSchemaFillsTheBody(t *testing.T) {
	for _, w := range []int{120, 80} {
		a := arrange("classic", ZoneSchema, ZoneSchema, w, 22)
		checkInvariants(t, a, w, 22)
		checkRects(t, a.rects, map[Zone]rect{ZoneSchema: {0, 0, w, 22}})
		if a.bar != nil || len(a.seps) != 0 {
			t.Fatalf("nothing else is drawn: %+v", a)
		}
		checkRects(t, a.nav, classic(w, 22, nil).rects) // with nothing left out
		if a.stepped != (w < 100) {
			t.Fatalf("%d: stepped %v", w, a.stepped)
		}
	}
	// the focused Pane is always on screen: a Schema the step-down left out
	// is shown maximised
	a := arrange("classic", ZoneSchema, "", 80, 22)
	checkRects(t, a.rects, map[Zone]rect{ZoneSchema: {0, 0, 80, 22}})
}

func equalZones(a, b []Zone) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNeighbourFollowsTheScreen(t *testing.T) {
	start := []Zone{ZoneExplorer, ZoneSchema, ZoneEditor, ZoneResults}
	classicNav := arrange("classic", ZoneExplorer, "", 120, 34).nav
	noSchema := arrange("classic", ZoneExplorer, "", 80, 22).nav
	focusNav := arrange("focus", ZoneExplorer, "", 60, 18).nav
	cases := []struct {
		name string
		nav  map[Zone]rect
		from Zone
		dir  direction
		mru  []Zone
		want Zone // "" for none
	}{
		{"classic: right of the Explorer is the Editor", classicNav, ZoneExplorer, dirRight, start, ZoneEditor},
		{"classic: left of the Editor is the Explorer", classicNav, ZoneEditor, dirLeft, start, ZoneExplorer},
		{"classic: left of the Results is the Schema", classicNav, ZoneResults, dirLeft, start, ZoneSchema},
		{"classic: above the Schema is the Explorer", classicNav, ZoneSchema, dirUp, start, ZoneExplorer},
		{"classic: below the Explorer is the Schema", classicNav, ZoneExplorer, dirDown, start, ZoneSchema},
		{"classic: nothing left of the Explorer", classicNav, ZoneExplorer, dirLeft, start, ""},
		{"classic: below the Editor are the Results", classicNav, ZoneEditor, dirDown, start, ZoneResults},
		{"no Schema: the most recently focused of Editor and Results", noSchema, ZoneExplorer, dirRight, []Zone{ZoneExplorer, ZoneResults, ZoneEditor}, ZoneResults},
		{"no Schema: the Editor at the start", noSchema, ZoneExplorer, dirRight, start, ZoneEditor},
		{"no Schema, neither focused yet: the larger overlap, then the cycle order", noSchema, ZoneExplorer, dirRight, []Zone{ZoneExplorer}, ZoneEditor},
		{"no Schema: left of the Results is the Explorer", noSchema, ZoneResults, dirLeft, start, ZoneExplorer},
		{"focus resolves on the classic rectangles", focusNav, ZoneExplorer, dirRight, start, ZoneEditor},
		{"focus: below the Editor are the Results", focusNav, ZoneEditor, dirDown, start, ZoneResults},
		{"a Pane not in nav has no neighbour", noSchema, ZoneSchema, dirUp, start, ""},
	}
	for _, c := range cases {
		got, ok := neighbour(c.nav, c.from, c.dir, c.mru)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
	// the larger overlap wins when no candidate was focused
	nav := map[Zone]rect{ZoneExplorer: {0, 0, 30, 23}, ZoneEditor: {31, 0, 49, 10}, ZoneResults: {31, 10, 49, 13}}
	if got, _ := neighbour(nav, ZoneExplorer, dirRight, nil); got != ZoneResults {
		t.Errorf("larger overlap: %s", got)
	}
}
