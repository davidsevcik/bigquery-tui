package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) arr() arrangement {
	return arrange(m.preset, m.panes, m.zone, m.maxed, m.mru, m.w, m.bodyH())
}

func (m *Model) bodyH() int { return max(1, m.h-2) }

func (m *Model) paneRect(z Zone) rect {
	a := m.arr()
	if r, ok := a.rects[z]; ok {
		return r
	}
	if r, ok := a.nav[z]; ok {
		return r
	}
	return lay(showPane(m.panes, z), rect{0, 0, m.w, m.bodyH()}, nil).rects[z]
}

func (m *Model) setZone(z Zone) {
	if z != m.zone {
		m.mru = append([]Zone{z}, slices.DeleteFunc(m.mru, func(o Zone) bool { return o == z })...)
	}
	if m.panes.hiddenPane(z) {
		m.setPanes(showPane(m.panes, z))
	}
	m.zone = z
	if m.maxed != "" {
		m.maxed = z
	}
	m.comp.open = false
	m.pendingKey = ""
}

func (m *Model) setPanes(t layoutNode) {
	m.panes = t
	m.saveState()
}

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

func (m *Model) paneMove(d direction) tea.Cmd {
	if z, ok := neighbour(m.arr().nav, m.zone, d, m.mru); ok {
		m.setZone(z)
	}
	return m.gridEnsure()
}

func (m *Model) focusMRU() {
	cycle := m.arr().cycle
	if slices.Contains(cycle, m.zone) {
		return
	}
	for _, z := range m.mru {
		if slices.Contains(cycle, z) {
			m.setZone(z)
			return
		}
	}
}

func (m *Model) layoutName() string {
	if isCustom(m.preset, m.panes) {
		return "custom"
	}
	return m.preset
}

func (m *Model) usePreset(name string) {
	m.preset, m.maxed = name, ""
	m.setPanes(presetTree(name))
	m.focusMRU()
	m.toast = "layout: " + name
}

func (m *Model) arrangeRefusal() string {
	if m.preset == presetFocus {
		return "one pane at a time in the focus layout: ctrl+w space for another"
	}
	if m.arr().single {
		return "the terminal is too small to arrange panes"
	}
	return ""
}

func (m *Model) editPanes(op func(t layoutNode) (layoutNode, string)) {
	if why := m.arrangeRefusal(); why != "" {
		m.toast = why
		return
	}
	// ADR-0011
	m.maxed = ""
	t, why := op(m.panes)
	if why != "" {
		m.toast = why
		return
	}
	m.setPanes(t)
	m.focusMRU()
}

func (m *Model) resize(dir splitDir, grow bool) {
	m.editPanes(func(t layoutNode) (layoutNode, string) {
		a := m.arr()
		omit := map[Zone]bool{}
		for _, z := range paneOrder {
			if !slices.Contains(a.cycle, z) {
				omit[z] = true
			}
		}
		body := rect{0, 0, m.w, m.bodyH()}
		lay := func(n layoutNode) map[Zone]rect { return rawRects(n, body, omit) }
		shared := !a.single && len(a.barPanes) == 2
		if shared {
			shown := ZoneEditor
			if _, ok := a.rects[ZoneResults]; ok {
				shown = ZoneResults
			}
			other := map[Zone]Zone{ZoneEditor: ZoneResults, ZoneResults: ZoneEditor}[shown]
			lay = func(n layoutNode) map[Zone]rect {
				rs := rawRects(mergeInto(n, other, shown), body, omit)
				rs[other] = rs[shown]
				return rs
			}
		}
		out, why := resizePane(t, m.zone, dir, grow, lay)
		if shared && why != "" && dir == splitCol && (m.zone == ZoneEditor || m.zone == ZoneResults) {
			why = "the Editor and the Results share one slot at this height"
		}
		return out, why
	})
}

func (m *Model) maximise() {
	if why := m.arrangeRefusal(); why != "" {
		m.toast = why
		return
	}
	switch {
	case m.maxed != "":
		m.maxed = ""
		m.focusDrawn()
	case m.arr().offLayout:
		m.focusDrawn()
	default:
		m.maxed = m.zone
	}
}

func (m *Model) focusDrawn() {
	a := m.arr()
	if !a.offLayout {
		return
	}
	for _, z := range m.mru {
		if z != m.zone && slices.Contains(a.cycle, z) {
			m.setZone(z)
			return
		}
	}
}

func (m *Model) hide() {
	z := m.zone
	m.editPanes(func(t layoutNode) (layoutNode, string) { return hidePane(t, z) })
}

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
	case "goexplorer", "showexplorer":
		m.setZone(ZoneExplorer)
	case "goschema", "showschema":
		m.focusSchema()
	case "goeditor", "showeditor":
		m.setZone(ZoneEditor)
	case "goresults", "showresults":
		m.setZone(ZoneResults)
	case "layoutclassic", "layoutquery", "layoutbrowse", "layouttall", "layoutfocus":
		m.usePreset(act[len("layout"):])
	case "layoutnext":
		m.usePreset(nextPreset(m.preset))
	case "layoutreset":
		m.usePreset(m.preset)
	case "panefarleft", "panefarbottom", "panefartop", "panefarright":
		d := map[string]direction{"panefarleft": dirLeft, "panefarbottom": dirDown, "panefartop": dirUp, "panefarright": dirRight}[act]
		z := m.zone
		m.editPanes(func(t layoutNode) (layoutNode, string) { return moveToEdge(t, z, d) })
	case "paneswap":
		z := m.zone
		m.editPanes(func(t layoutNode) (layoutNode, string) { return swapPane(t, z) })
	case "panerotate":
		z := m.zone
		m.editPanes(func(t layoutNode) (layoutNode, string) { return rotatePanes(t, z) })
	case "panenarrower", "panewider":
		m.resize(splitRow, act == "panewider")
	case "paneshorter", "panetaller":
		m.resize(splitCol, act == "panetaller")
	case "paneequal":
		m.editPanes(func(t layoutNode) (layoutNode, string) { return equalise(t), "" })
	case "panemax":
		m.maximise()
	case "panehide":
		m.hide()
	default:
		return nil, false
	}
	return m.gridEnsure(), true
}

func (m *Model) focusSchema() {
	m.setZone(ZoneSchema)
}

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
	case "synctable":
		return m.syncTableAction()
	case "back":
		m.setZone(ZoneExplorer)
	}
	return nil
}
