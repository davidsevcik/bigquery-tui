package tui

import tea "charm.land/bubbletea/v2"

// The mouse, off by default (the palette's "Mouse: on/off"): a left click
// focuses the Pane under the pointer, and the wheel scrolls it without
// moving the focus. Both read the rectangles the Layout draws (layout.go).
// Popups take no mouse: View asks for none while one is open.

// toggleMouse is the palette's "Mouse: on/off".
func (m *Model) toggleMouse() {
	m.mouse = !m.mouse
	m.saveState()
	if m.mouse {
		m.toast = "mouse on · Shift+drag selects text (Option+drag in iTerm2)"
	} else {
		m.toast = "mouse off · a plain drag selects text again"
	}
}

// mouseOK reports whether a mouse message may act now. A message can arrive
// after the mode changed (a popup opened, the mouse was turned off), so this
// is checked as well as View's mode.
func (m *Model) mouseOK() bool {
	return m.mouse && m.ov == ovNone && !m.tooSmall()
}

// mouseClick is a left click: on a Pane other than the focused one it moves
// the focus there, as ctrl+w h/j/k/l would, and on a name in the pane bar it
// switches to that Pane. It never moves a cursor inside a Pane.
func (m *Model) mouseClick(ev tea.Mouse) tea.Cmd {
	if !m.mouseOK() || ev.Button != tea.MouseLeft {
		return nil
	}
	a := m.arr()
	z, ok := paneAt(a.rects, ev.X, ev.Y)
	if !ok {
		z, ok = barAt(a, ev.X, ev.Y)
	}
	if !ok || z == m.zone {
		return nil
	}
	m.setZone(z)
	return m.gridEnsure()
}

// wheelStep is the rows one wheel step scrolls.
const wheelStep = 3

// mouseWheel scrolls the Pane under the pointer, focused or not; the focus
// stays. The Explorer and the Results move their cursor through the same
// code as j and k, so the Schema follows and further pages load; the Schema
// and the Editor scroll their text.
func (m *Model) mouseWheel(ev tea.Mouse) tea.Cmd {
	if !m.mouseOK() {
		return nil
	}
	d, side := 0, 0
	switch ev.Button {
	case tea.MouseWheelUp:
		d = -wheelStep
	case tea.MouseWheelDown:
		d = wheelStep
	case tea.MouseWheelLeft:
		side = -1
	case tea.MouseWheelRight:
		side = 1
	default:
		return nil
	}
	a := m.arr()
	z, ok := paneAt(a.rects, ev.X, ev.Y)
	if !ok {
		return nil
	}
	r := a.rects[z]
	switch z {
	case ZoneResults:
		g := m.grid()
		if g == nil {
			return nil // unlike the keys, no toast
		}
		if d != 0 {
			g.moveRow(int64(d))
		} else {
			g.moveCol(side)
		}
		return m.gridEnsure()
	}
	if d == 0 {
		return nil
	}
	switch z {
	case ZoneExplorer:
		return m.wheelExplorer(ev.X-r.x, d)
	case ZoneSchema:
		// clamped now, as drawing does: several steps can come between frames
		m.scrollDetail(d)
		lines := len(m.schemaLines(m.sel(), r.w))
		m.detOff = min(m.detOff, max(0, lines-(r.h-1)))
	case ZoneEditor:
		m.comp.open = false
		b := m.buf()
		b.ed.scrollView(d, m.editorRows())
		if m.vimOn() && b.vim.mode != vimInsert {
			b.ed.clampNormal()
			b.vim.syncSelection(b.ed) // a Visual selection follows the cursor
		}
	}
	return nil
}

// wheelExplorer moves the Explorer's cursor by d rows, x cells into the
// Pane. In the columns style it moves the column under the pointer, which
// need not be the focused one.
func (m *Model) wheelExplorer(x, d int) tea.Cmd {
	if m.style == styleColumns {
		for _, c := range m.millerSpans(m.paneRect(ZoneExplorer).w) {
			if x >= c.x && x < c.x+c.w {
				m.milMove(c.col, d)
			}
		}
		return nil
	}
	m.move(d)
	if d > 0 {
		return m.moreIfFlat()
	}
	return nil
}
