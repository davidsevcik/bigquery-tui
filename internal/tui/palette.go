package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// paletteOverlay is the command palette (ctrl+p, :): the Keymap's commands,
// narrowed as the user types.
type paletteOverlay struct{}

func (paletteOverlay) frame(m *Model, w, h int) ovFrame {
	bw := clamp(w-12, 30, 70)
	bh := clamp(h-4, 6, 18)
	return ovFrame{x: (w - bw) / 2, y: max(0, (h-bh)/3), lines: m.paletteBox(bw, bh), rows: bh - 3}
}

func (paletteOverlay) key(m *Model, k tea.KeyPressMsg, name string, _ ovFrame) tea.Cmd {
	return m.paletteKey(k, name)
}

func (paletteOverlay) hints(*Model) string { return footerHints(ZoneOverlay) }

func (m *Model) paletteBox(bw, bh int) []string {
	inner := bw - 2
	b := newRB(false)
	b.add(" > ", sAcc)
	b.l.WriteString(m.pal.render(api.ParsedQuery{}, true, "type a command"))
	lines := []string{b.line(inner)}
	off := 0
	s, e := window(m.palSel, &off, len(m.palCmds), bh-3)
	for i := s; i < e; i++ {
		c := m.palCmds[i]
		r := newRB(i == m.palSel)
		r.add(" "+c.name, sBase).addR(c.key+" ", sFaint)
		lines = append(lines, r.line(inner))
	}
	if len(m.palCmds) == 0 {
		lines = append(lines, sDim.Render(" no command matches"))
	}
	return popup("Commands", lines, bw)
}

func (m *Model) filterPalette() {
	q := strings.ToLower(m.pal.value())
	m.palCmds = m.palCmds[:0]
	for _, c := range paletteCommands() {
		if q == "" || fuzzyContains(strings.ToLower(c.name), q) {
			m.palCmds = append(m.palCmds, c)
		}
	}
	m.palSel = clamp(m.palSel, 0, max(0, len(m.palCmds)-1))
}

func fuzzyContains(s, q string) bool {
	qr := []rune(q)
	i := 0
	for _, r := range s {
		if i < len(qr) && r == qr[i] {
			i++
		}
	}
	return i == len(qr)
}

func (m *Model) paletteKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	switch keys.action(ZoneSearch, name) {
	case "back":
		m.ov = ovNone
		return nil
	case "down":
		m.palSel = min(m.palSel+1, max(0, len(m.palCmds)-1))
		return nil
	case "up":
		m.palSel = max(0, m.palSel-1)
		return nil
	case "open":
		m.ov = ovNone
		if m.palSel < len(m.palCmds) {
			c := m.palCmds[m.palSel]
			switch c.zone {
			case ZoneEditor:
				m.setZone(ZoneEditor)
				return m.editorAction(c.action)
			case ZoneResults:
				m.setZone(ZoneResults)
				return m.resultsAction(c.action)
			case ZoneGlobal:
				cmd, _ := m.globalAction(c.action)
				return cmd
			}
			return m.explorerAction(c.action)
		}
		return nil
	}
	if m.pal.handle(msg) {
		m.palSel = 0
		m.filterPalette()
	}
	return nil
}
