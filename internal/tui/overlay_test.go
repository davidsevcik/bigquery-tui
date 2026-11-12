package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// shownFavorites counts the Favorites the picker's list draws: rows that
// start with a favorite's file name right after the popup's border.
func (r *rig) shownFavorites() int {
	r.t.Helper()
	row := regexp.MustCompile(`(?m)^.{0,4}│ fav\d\d\.sql`)
	return len(row.FindAllString(r.screen(), -1))
}

// The picker pages by the rows its list shows: at the side of the preview
// (wide) and above it (narrow), page down and page up move the cursor by
// exactly what is visible.
func TestPickerPagesByTheRowsItShows(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 36}, {80, 30}, {120, 20}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			r := editorRig(t, size.w, size.h)
			for i := range 60 {
				r.writeFav(fmt.Sprintf("fav%02d.sql", i), fmt.Sprintf("select %d\n", i))
			}
			r.press("ctrl+x", "f")
			shown := r.shownFavorites()
			if shown < 3 {
				t.Fatalf("the list shows %d favorites:\n%s", shown, r.screen())
			}
			r.press("pgdown")
			if got := r.m.pk.sel[scopeFavorites]; got != shown {
				t.Fatalf("page down moved %d rows, the list shows %d:\n%s", got, shown, r.screen())
			}
			r.contains(fmt.Sprintf("│ fav%02d.sql", shown))
			r.press("pgdown", "pgup")
			if got := r.m.pk.sel[scopeFavorites]; got != shown {
				t.Fatalf("page up moved back to %d, want %d", got, shown)
			}
		})
	}
}

// boxRows is the number of rows between the top and bottom border of the
// popup titled ttl, as drawn.
func (r *rig) boxRows(ttl string) int {
	r.t.Helper()
	lines := strings.Split(r.screen(), "\n")
	top := -1
	for i, l := range lines {
		switch {
		case top < 0 && strings.Contains(l, "╭─ "+ttl):
			top = i
		case top >= 0 && strings.Contains(l, "╰"):
			return i - top - 1
		}
	}
	r.t.Fatalf("no popup %q on the screen:\n%s", ttl, r.screen())
	return 0
}

// Jump to table pages by half the results its popup shows, not by the
// Explorer's list behind it.
func TestJumpPagesByHalfItsList(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 36}, {120, 20}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			r := newRig(t, size.w, size.h)
			r.press("ctrl+t")
			r.typeText("p:acme")
			n := len(r.m.jump.results)
			rows := r.boxRows("Jump to table") - 2 // the input and the chips
			r.press("pgdown")
			if want := min(max(1, rows/2), n-1); r.m.jump.cur != want {
				t.Fatalf("page down went to result %d, want %d (half of %d rows, %d results)", r.m.jump.cur, want, rows, n)
			}
		})
	}
}

// The list popups move the cursor half a page by half the rows they show.
func TestListPopupsPageByHalfTheirRows(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 36}, {80, 24}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			r := newRig(t, size.w, size.h)
			for i := range 40 {
				r.fake.Listed = append(r.fake.Listed, api.ListedProject{ID: fmt.Sprintf("acme-x%02d", i)})
			}
			r.press("ctrl+x", "p")
			f := r.m.overlayFrame(r.m.overlay())
			half := max(1, f.rows/2)
			r.press("ctrl+d")
			if r.m.pd.sel != half {
				t.Fatalf("ctrl+d moved to row %d, want %d (half of %d rows)", r.m.pd.sel, half, f.rows)
			}
			r.press("ctrl+d", "ctrl+u")
			if r.m.pd.sel != half {
				t.Fatalf("ctrl+u went back to row %d, want %d", r.m.pd.sel, half)
			}
		})
	}
}

// c in the run list cancels the Run under the cursor.
func TestRunListCancelsTheSelectedRun(t *testing.T) {
	r := editorRig(t, 120, 30)
	block := make(chan struct{})
	defer close(block)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		s := bqtest.Numbers(1)
		s.Block = block
		return s
	}
	r.typeText("select 1")
	r.press("ctrl+r", "ctrl+r", "ctrl+x", "l", "down", "c")
	r.contains("cancel requested for run #1 (it may still be billed)")
	if r.m.ov != ovRuns {
		t.Fatal("the run list stays open")
	}
}

// c in the run list on a Run that has finished cancels nothing and says so.
func TestRunListDoesNotCancelAFinishedRun(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	r.typeText("select 1")
	r.press("ctrl+r", "ctrl+r")
	r.settle()
	r.press("ctrl+x", "l", "down", "c")
	r.contains("run #1 already finished")
	r.lacks("cancel requested")
	if info, _ := r.m.runs.Info(1); info.State != api.RunDone {
		t.Fatalf("the Run stays %s", info.State)
	}
}

// lines is a scrolling popup of n numbered rows for the tests, with a
// cursor when cursor is set.
type lines struct {
	textPopup
	n      int
	cursor bool
	sel    int
}

func (l *lines) page(*Model, int, int) scrollPage {
	p := scrollPage{title: "Lines", cur: -1}
	for i := range l.n {
		p.rows = append(p.rows, fmt.Sprintf(" row %d", i))
	}
	if l.cursor {
		p.cur, p.sel, p.items = l.sel, &l.sel, l.n
	}
	return p
}

// scrollRig is a bare model with the scrolling popup k open on a w×h
// terminal. It has no backend, so only the popup's own keys may be pressed.
func scrollRig(k scrollKind, w, h int) (*Model, scrollOverlay) {
	return &Model{w: w, h: h, ov: ovHelp}, scrollOverlay{k}
}

func (s scrollOverlay) press(m *Model, names ...string) {
	for _, n := range names {
		s.key(m, edKey(n), n, m.overlayFrame(s))
	}
}

// A scrolling popup's frame shows the rows from the offset on and changes
// no state. The keys stop at the end however far they go, so the next key
// up moves at once.
func TestScrollPopupStopsAtTheEnd(t *testing.T) {
	m, s := scrollRig(&lines{n: 50}, 80, 24)
	f := m.overlayFrame(s)
	if f.rows != 24-2-4 || f.off != 0 {
		t.Fatalf("%d rows from %d", f.rows, f.off)
	}
	if !strings.Contains(f.lines[0], fmt.Sprintf("(1-%d/50)", f.rows)) || !strings.Contains(f.lines[1], "row 0") {
		t.Fatalf("drawn: %q %q", f.lines[0], f.lines[1])
	}
	for range 100 {
		s.press(m, "j")
	}
	last := 50 - f.rows
	if m.ovOff != last {
		t.Fatalf("scrolled to %d, the end is %d", m.ovOff, last)
	}
	m.ovOff = 1 << 30 // the sync log opens at its end
	if f := m.overlayFrame(s); f.off != last || m.ovOff != 1<<30 {
		t.Fatalf("framed from %d, the offset is now %d", f.off, m.ovOff)
	}
	s.press(m, "k")
	if m.ovOff != last-1 {
		t.Fatalf("k from the end went to %d", m.ovOff)
	}
	s.press(m, "ctrl+u")
	if m.ovOff != last-1-f.rows/2 {
		t.Fatalf("ctrl+u went to %d", m.ovOff)
	}
	s.press(m, "esc")
	if m.ov != ovNone {
		t.Fatal("esc closes")
	}
}

// With a cursor, the keys move it and the window follows, keeping its place
// while the cursor moves inside it.
func TestScrollPopupFollowsItsCursor(t *testing.T) {
	l := &lines{n: 50, cursor: true}
	m, s := scrollRig(l, 80, 24)
	rows := m.overlayFrame(s).rows
	s.press(m, "ctrl+d")
	if l.sel != rows/2 || m.ovOff != 0 {
		t.Fatalf("ctrl+d: cursor %d, window from %d", l.sel, m.ovOff)
	}
	for range rows {
		s.press(m, "j")
	}
	if want := l.sel - rows + 1; m.ovOff != want {
		t.Fatalf("the window starts at %d, want %d (cursor %d)", m.ovOff, want, l.sel)
	}
	before := m.ovOff
	s.press(m, "k")
	if m.ovOff != before {
		t.Fatalf("k inside the window moved it to %d", m.ovOff)
	}
	for range 100 {
		s.press(m, "j")
	}
	f := m.overlayFrame(s)
	if l.sel != 49 || f.off != 50-rows {
		t.Fatalf("cursor %d, window from %d", l.sel, f.off)
	}
	if !strings.Contains(f.lines[len(f.lines)-2], "row 49") {
		t.Fatalf("the last row is drawn last: %q", f.lines[len(f.lines)-2])
	}
}

// Every overlay kind has its module.
func TestEveryOverlayKindHasAModule(t *testing.T) {
	if overlays[ovNone] != nil {
		t.Fatal("ovNone is no overlay")
	}
	for k := ovNone + 1; int(k) < len(overlays); k++ {
		if overlays[k] == nil {
			t.Errorf("overlay kind %d has no module", k)
		}
	}
	if len(overlays) != int(ovHandoff)+1 {
		t.Errorf("%d modules for %d kinds", len(overlays)-1, ovHandoff)
	}
}

// A question closes on an answer only, and a "close the buffer?" no keeps
// the buffer.
func TestQuestionsCloseOnAnAnswer(t *testing.T) {
	for name, want := range map[string]answer{"y": answerYes, "enter": answerYes, "N": answerNo, "q": answerDismiss, "ctrl+c": answerDismiss, "x": noAnswer} {
		if got := answerOf(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	m := &Model{ov: ovConfirm, confirm: confirmState{kind: "closebuf"}}
	m.confirmKey("x")
	if m.ov != ovConfirm {
		t.Fatal("x is no answer")
	}
	m.confirmKey("esc")
	if m.ov != ovNone {
		t.Fatal("esc answers no")
	}
}

// The overlays' footers name the keys the Keymap binds.
func TestOverlayHintsComeFromTheKeymap(t *testing.T) {
	m := &Model{pk: &picker{}}
	for kind, want := range map[overlayKind]string{
		ovRuns:    "↑↓ select · enter show the result · c cancel the run · esc close",
		ovBuffers: "↑↓ select · enter open or restore · esc close",
		ovRecord:  "n/N next/previous row · j/k scroll · y copy as JSON · esc close",
		ovJump:    "type to search · ↑↓ select · enter open · esc close",
		ovPicker:  "type to search · ↑↓ select · enter open · tab favorites/history · ctrl+r rename · ctrl+d delete · ctrl+e/y scroll · esc close",
		ovHelp:    "esc close",
		ovPrompt:  "",
	} {
		if got := overlays[kind].hints(m); got != want {
			t.Errorf("overlay %d: %q, want %q", kind, got, want)
		}
	}
	m.pk.scope = scopeHistory
	if got := overlays[ovPicker].hints(m); !strings.Contains(got, "ctrl+r show result · ctrl+s save as favorite") {
		t.Errorf("History: %q", got)
	}
	if got := keyHint(ZoneEditor, "buffers", "prevbuf", "nextbuf"); got != "ctrl+x [/ctrl+x ] buffers" {
		t.Errorf("chords are written in full: %q", got)
	}
}
