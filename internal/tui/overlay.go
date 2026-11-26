package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
)

type overlay interface {
	frame(m *Model, w, h int) ovFrame
	key(m *Model, k tea.KeyPressMsg, name string, f ovFrame) tea.Cmd
	hints(m *Model) string
}

type ovFrame struct {
	x, y      int
	lines     []string
	rows, off int
}

var overlays = [...]overlay{
	ovJump:     jumpOverlay{},
	ovPalette:  paletteOverlay{},
	ovPrompt:   promptOverlay{},
	ovConfirm:  confirmOverlay{},
	ovPicker:   pickerOverlay{},
	ovHelp:     scrollOverlay{helpPopup{}},
	ovProblems: scrollOverlay{problemsPopup{}},
	ovSync:     scrollOverlay{syncPopup{}},
	ovRecord:   scrollOverlay{recordPopup{}},
	ovRuns:     scrollOverlay{runsPopup{}},
	ovBuffers:  scrollOverlay{buffersPopup{}},
	ovProjects: projectsOverlay{scrollOverlay{projectsPopup{}}},
	ovHandoff:  scrollOverlay{handoffPopup{}},
}

func (m *Model) overlay() overlay {
	if int(m.ov) >= len(overlays) {
		return nil
	}
	return overlays[m.ov]
}

func (m *Model) overlayFrame(o overlay) ovFrame { return o.frame(m, m.w, m.h-2) }

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

type scrollPage struct {
	title      string
	head, rows []string
	cur        int
	sel        *int
	items      int
}

type scrollKind interface {
	width(w int) int
	page(m *Model, inner, body int) scrollPage
	act(m *Model, act string) (tea.Cmd, bool)
	hints(m *Model) string
}

type scrollOverlay struct{ kind scrollKind }

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

func (s scrollOverlay) common(m *Model, act string, p scrollPage, f ovFrame) tea.Cmd {
	half := max(1, f.rows/2)
	d := map[string]int{"down": 1, "up": -1, "halfdown": half, "halfup": -half}[act]
	switch {
	case act == "back" || act == "open":
		m.ov = ovNone
	case d != 0 && p.sel != nil:
		*p.sel = clamp(*p.sel+d, 0, max(0, p.items-1))
	case d != 0:
		m.ovOff = max(0, m.ovOff+d)
	default:
		cmd, _ := m.globalAction(act)
		return cmd
	}
	return nil
}

func (s scrollOverlay) hints(m *Model) string { return s.kind.hints(m) }

type textPopup struct{}

func (textPopup) width(w int) int                    { return clamp(w-6, 30, 96) }
func (textPopup) act(*Model, string) (tea.Cmd, bool) { return nil, false }
func (textPopup) hints(*Model) string                { return footerHints(ZoneOverlay) }

type helpPopup struct{ textPopup }

func (helpPopup) page(_ *Model, inner, _ int) scrollPage {
	return scrollPage{title: "Help", rows: helpLines(inner), cur: -1}
}

type problemsPopup struct{ textPopup }

func (problemsPopup) page(m *Model, inner, _ int) scrollPage {
	return scrollPage{title: "Sync problems", rows: m.problemLines(inner - 2), cur: -1}
}

type syncPopup struct{ textPopup }

func (syncPopup) page(m *Model, _, _ int) scrollPage {
	return scrollPage{title: "Sync progress", rows: m.syncLines(), cur: -1}
}
