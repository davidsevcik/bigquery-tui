package tui

import tea "charm.land/bubbletea/v2"

func (m *Model) toggleMouse() {
	m.mouse = !m.mouse
	m.saveState()
	if m.mouse {
		m.toast = "mouse on · Shift+drag selects text (Option+drag in iTerm2)"
	} else {
		m.toast = "mouse off · a plain drag selects text again"
	}
}

func (m *Model) mouseOK() bool {
	return m.mouse && m.ov == ovNone && !m.tooSmall()
}

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

const wheelStep = 3

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
			return nil
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
		m.scrollDetail(d)
		lines := len(m.schemaLines(m.sel(), r.w))
		m.detOff = min(m.detOff, max(0, lines-(r.h-1)))
	case ZoneEditor:
		m.comp.open = false
		b := m.buf()
		b.ed.scrollView(d, m.editorRows())
		if m.vimOn() && b.vim.mode != vimInsert {
			b.ed.clampNormal()
			b.vim.syncSelection(b.ed)
		}
	}
	return nil
}

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
