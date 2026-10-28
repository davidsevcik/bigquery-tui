package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// The Model's side of the Layout: which Pane has focus, the arrangement for
// the current size, and the keys that move between Panes.

// arr arranges the Panes for the current state. Sizes are never cached: a
// resize, a Layout change, a focus change or the tutorial panel all change it.
func (m *Model) arr() arrangement {
	return arrange(m.preset, m.zone, m.maxed, m.w, max(1, m.h-2))
}

// paneRect is the Pane's rectangle: where it is drawn, or where it would be
// (so that paging stays sensible while it is not on screen).
func (m *Model) paneRect(z Zone) rect {
	a := m.arr()
	if r, ok := a.rects[z]; ok {
		return r
	}
	if r, ok := a.nav[z]; ok {
		return r
	}
	return classic(m.w, max(1, m.h-2), nil).rects[z]
}

// setZone moves the focus to a Pane. Moving it restores a maximised Pane.
func (m *Model) setZone(z Zone) {
	if z != m.zone {
		m.mru = append([]Zone{z}, slices.DeleteFunc(m.mru, func(o Zone) bool { return o == z })...)
	}
	m.zone = z
	m.maxed = ""
	m.comp.open = false
	m.pendingKey = ""
}

// zoneStep moves the focus d Panes along the cycle, skipping the Panes the
// step-down left out.
func (m *Model) zoneStep(d int) {
	cycle := m.arr().cycle
	i := slices.Index(paneOrder, m.zone)
	for range paneOrder {
		i = (i + d + len(paneOrder)) % len(paneOrder)
		if slices.Contains(cycle, paneOrder[i]) {
			m.setZone(paneOrder[i])
			return
		}
	}
}

// paneMove is ctrl+w h/j/k/l: the Pane on that side of the focused one.
// With none there nothing happens, as in Vim.
func (m *Model) paneMove(d direction) tea.Cmd {
	if z, ok := neighbour(m.arr().nav, m.zone, d, m.mru); ok {
		m.setZone(z)
	}
	return m.gridEnsure()
}

// paneAction handles the global Pane keys and commands.
func (m *Model) paneAction(act string) (tea.Cmd, bool) {
	switch act {
	case "panenext":
		m.zoneStep(1)
	case "paneprev":
		m.zoneStep(-1)
	case "paneleft":
		return m.paneMove(dirLeft), true
	case "panedown":
		return m.paneMove(dirDown), true
	case "paneup":
		return m.paneMove(dirUp), true
	case "paneright":
		return m.paneMove(dirRight), true
	case "goexplorer":
		m.setZone(ZoneExplorer)
	case "goschema":
		m.focusSchema(false)
	case "goeditor":
		m.setZone(ZoneEditor)
	case "goresults":
		m.setZone(ZoneResults)
	case "layoutclassic", "layoutfocus":
		m.preset = presetClassic
		if act == "layoutfocus" {
			m.preset = presetFocus
		}
		m.saveState()
		m.toast = "layout: " + m.preset
	default:
		return nil, false
	}
	return m.gridEnsure(), true
}

// focusSchema sends the focus to the Schema pane explicitly (Enter on a
// table, z, the palette). A Schema the step-down left out is shown
// maximised.
func (m *Model) focusSchema(maximise bool) {
	m.setZone(ZoneSchema)
	if maximise || !slices.Contains(m.arr().cycle, ZoneSchema) {
		m.maxed = ZoneSchema
	}
}

// leaveSchema restores a maximised Schema and gives the focus back to the
// Explorer.
func (m *Model) leaveSchema() {
	m.setZone(ZoneExplorer)
}

// schemaAction handles a key in the Schema pane: the movement keys scroll it,
// and p and s work on its table as in the Explorer.
func (m *Model) schemaAction(act string) tea.Cmd {
	if cmd, ok := m.globalAction(act); ok {
		return cmd
	}
	half := max(1, m.paneRect(ZoneSchema).h/2)
	switch act {
	case "down":
		return m.scrollDetail(1)
	case "up":
		return m.scrollDetail(-1)
	case "halfdown":
		return m.scrollDetail(half)
	case "halfup":
		return m.scrollDetail(-half)
	case "top":
		return m.scrollDetail(-1 << 30)
	case "bottom":
		return m.scrollDetail(1 << 30)
	case "sample":
		return m.sampleAction()
	case "select":
		return m.selectAction()
	case "zoom":
		switch {
		case m.maxed == "" && slices.Contains(m.arr().cycle, ZoneSchema):
			m.maxed = ZoneSchema
		case slices.Contains(m.arr().cycle, ZoneSchema):
			m.maxed = ""
		default:
			m.leaveSchema() // the Layout has no room for it
		}
	case "back":
		// one level at a time: a maximised Schema that fits the Layout is
		// restored first and keeps the focus
		if m.maxed == ZoneSchema && slices.Contains(m.arr().cycle, ZoneSchema) {
			m.maxed = ""
		} else {
			m.leaveSchema()
		}
	}
	return nil
}
