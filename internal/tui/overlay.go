package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
)

// An overlay is one kind of popup drawn over the Panes: Jump to table, the
// palette, the picker, a prompt or a question, or one of the scrolling
// popups (help, the run list…). Each kind is one module behind this
// interface, and render, key and footerLine each make one call through it.
//
// As for the Panes (ADR 0002), one function lays a popup out and both
// drawing and the keys use it: frame is where the popup goes, what it draws
// and the window of its list, so the keys page by the rows that are shown.
// frame changes no state; the keys keep the window where it was drawn.
type overlay interface {
	// frame lays the popup out over a body of w×h cells.
	frame(m *Model, w, h int) ovFrame
	// key handles a key pressed while the popup is open; f is the frame as
	// drawn now.
	key(m *Model, k tea.KeyPressMsg, name string, f ovFrame) tea.Cmd
	// hints is the footer line while the popup is open.
	hints(m *Model) string
}

// ovFrame is an overlay as drawn: its lines, border included, at (x, y) of
// the body, and the window of its list or text: rows rows from off on.
type ovFrame struct {
	x, y      int
	lines     []string
	rows, off int
}

// overlays are the overlay kinds' modules (none for ovNone).
var overlays = [...]overlay{
	ovHelp:     scrollOverlay{helpPopup{}},
	ovProblems: scrollOverlay{problemsPopup{}},
	ovSync:     scrollOverlay{syncPopup{}},
	ovRecord:   scrollOverlay{recordPopup{}},
	ovRuns:     scrollOverlay{runsPopup{}},
	ovBuffers:  scrollOverlay{buffersPopup{}},
	ovProjects: projectsOverlay{scrollOverlay{projectsPopup{}}},
	ovHandoff:  scrollOverlay{handoffPopup{}},
}

// overlay is the open overlay's module, nil when none is open.
func (m *Model) overlay() overlay {
	if int(m.ov) >= len(overlays) {
		return nil
	}
	return overlays[m.ov]
}

// overlayFrame lays the open overlay out over the body: the rows between the
// top of the screen and the status line.
func (m *Model) overlayFrame(o overlay) ovFrame { return o.frame(m, m.w, m.h-2) }

// chordAction resolves a key in zone z, the global chords included: a key
// that starts a chord waits for the next one (ok is false meanwhile).
func (m *Model) chordAction(z Zone, name string) (act string, ok bool) {
	if m.pendingKey != "" {
		name = m.pendingKey + " " + name
		m.pendingKey = ""
	} else if keys.chordPrefix(z, name) {
		m.pendingKey = name
		return "", false
	}
	return keys.action(z, name), true
}

// --- scrolling popups

// scrollPage is what a scrolling popup shows: a head that stays put, and
// rows that scroll below it. The window follows row cur, the row of the
// cursor sel; without a cursor (sel nil, cur -1) the keys scroll the rows.
type scrollPage struct {
	title      string
	head, rows []string
	cur        int
	sel        *int // the cursor the moves move
	items      int  // how many entries the cursor moves over
}

// scrollKind is one scrolling popup: its width, its page, the actions of its
// own and its footer.
type scrollKind interface {
	// width is the popup's width on a body w cells wide.
	width(w int) int
	// page is the popup's content for inner cells inside the border and body
	// rows of head and rows together.
	page(m *Model, inner, body int) scrollPage
	// act handles a Popups action of its own; false leaves it to the moves,
	// closing, and the global actions.
	act(m *Model, act string) (tea.Cmd, bool)
	hints(m *Model) string
}

// scrollOverlay is the overlay of a scrolling popup. Every one moves and
// pages the same way, by the rows it shows.
type scrollOverlay struct{ kind scrollKind }

// layout is the page and its frame.
func (s scrollOverlay) layout(m *Model, w, h int) (scrollPage, ovFrame) {
	bw := s.kind.width(w)
	body := max(1, h-4)
	p := s.kind.page(m, bw-2, body)
	rows := max(1, body-len(p.head))
	off := m.ovOff
	if p.cur >= 0 {
		window(p.cur, &off, len(p.rows), rows)
	}
	off = clamp(off, 0, max(0, len(p.rows)-rows))
	end := min(len(p.rows), off+rows)
	ttl := p.title
	if len(p.rows) > rows {
		ttl += fmt.Sprintf("  (%d-%d/%d)", off+1, end, len(p.rows))
	}
	lines := popup(ttl, append(slices.Clip(p.head), p.rows[off:end]...), bw)
	return p, ovFrame{x: (w - bw) / 2, y: max(0, (h-len(lines))/2), lines: lines, rows: rows, off: off}
}

func (s scrollOverlay) frame(m *Model, w, h int) ovFrame {
	_, f := s.layout(m, w, h)
	return f
}

func (s scrollOverlay) key(m *Model, _ tea.KeyPressMsg, name string, f ovFrame) tea.Cmd {
	act, ok := m.chordAction(ZoneOverlay, name)
	if !ok {
		return nil
	}
	return s.keep(m, f, func(p scrollPage) tea.Cmd {
		if cmd, done := s.kind.act(m, act); done {
			return cmd
		}
		return s.common(m, act, p, f)
	})
}

// keep runs a key's work on the popup as drawn (frame f) and keeps the
// window where the next frame draws it, so the window moves only with the
// cursor, as it was drawn.
func (s scrollOverlay) keep(m *Model, f ovFrame, work func(p scrollPage) tea.Cmd) tea.Cmd {
	kind := m.ov
	p, _ := s.layout(m, m.w, m.h-2)
	m.ovOff = f.off
	if p.sel != nil {
		*p.sel = clamp(*p.sel, 0, max(0, p.items-1))
	}
	cmd := work(p)
	if m.ov == kind {
		_, after := s.layout(m, m.w, m.h-2)
		m.ovOff = after.off
	}
	return cmd
}

// common handles the actions every scrolling popup shares: closing, the
// moves (the cursor's, or the rows' when there is none) and the global
// actions.
func (s scrollOverlay) common(m *Model, act string, p scrollPage, f ovFrame) tea.Cmd {
	half := max(1, f.rows/2)
	d := map[string]int{"down": 1, "up": -1, "halfdown": half, "halfup": -half}[act]
	switch {
	case act == "back" || act == "open":
		m.ov = ovNone
	case p.sel != nil && (act == "down" || act == "up"):
		*p.sel = clamp(*p.sel+d, 0, max(0, p.items-1))
	case p.sel == nil && d != 0:
		m.ovOff = max(0, m.ovOff+d)
	default:
		cmd, _ := m.globalAction(act)
		return cmd
	}
	return nil
}

func (s scrollOverlay) hints(m *Model) string { return s.kind.hints(m) }

// textPopup is the default of a popup of text: as wide as help, no actions
// of its own, and the Popups zone's hints.
type textPopup struct{}

func (textPopup) width(w int) int                    { return clamp(w-6, 30, 96) }
func (textPopup) act(*Model, string) (tea.Cmd, bool) { return nil, false }
func (textPopup) hints(*Model) string                { return footerHints(ZoneOverlay) }

// helpPopup is the key reference (F1, ?).
type helpPopup struct{ textPopup }

func (helpPopup) page(_ *Model, inner, _ int) scrollPage {
	return scrollPage{title: "Help", rows: helpLines(inner), cur: -1}
}

// problemsPopup lists the sync problems (gp).
type problemsPopup struct{ textPopup }

func (problemsPopup) page(m *Model, inner, _ int) scrollPage {
	return scrollPage{title: "Sync problems", rows: m.problemLines(inner - 2), cur: -1}
}

// syncPopup is the sync's progress log (gs).
type syncPopup struct{ textPopup }

func (syncPopup) page(m *Model, _, _ int) scrollPage {
	return scrollPage{title: "Sync progress", rows: m.syncLines(), cur: -1}
}
