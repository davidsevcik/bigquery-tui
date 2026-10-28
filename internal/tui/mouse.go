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
