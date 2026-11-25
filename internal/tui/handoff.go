package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/handoff"
)

// Hand-off (spec 14.4): text for an agent about the selected table or the
// focused Run, copied to the clipboard (OSC 52) or written to a file. The
// core renders the text; this file only picks what and where.

// handoffItem is a hand-off on offer (handoff.Item), with its palette
// action and how to render it.
type handoffItem struct {
	id    string // the palette action
	label string
	path  string // default file (handoff.Item.DefaultPath)
	make  func(ctx context.Context) (string, error)
}

// handoffActions are the palette actions of the hand-off kinds.
var handoffActions = map[handoff.Kind]string{
	handoff.KindSchemaMarkdown: "hoschemamd", handoff.KindSchemaJSON: "hoschemajson", handoff.KindQueryError: "hoerror",
	handoff.KindRowsMarkdown: "hosamplemd", handoff.KindRowsTSV: "hosampletsv", handoff.KindRowsNDJSON: "hosamplendjson",
	handoff.KindRun: "horun",
}

type handoffMsg struct {
	text, label, path string
	err               error
}

// focusedRun is the Run a hand-off is about: the one shown in the Results
// when they have the focus, else the buffer's latest Run.
func (m *Model) focusedRun() (api.RunInfo, bool) {
	if len(m.bufs) == 0 {
		return api.RunInfo{}, false
	}
	ids := []int64{m.buf().runID, m.buf().viewID}
	if m.zone == ZoneResults {
		ids = []int64{m.buf().viewID, m.buf().runID}
	}
	for _, id := range ids {
		if r, ok := m.runs.Info(id); ok && id != 0 {
			return r, true
		}
	}
	return api.RunInfo{}, false
}

// handoffItems lists what can be handed off now, in menu order
// (handoff.Items).
func (m *Model) handoffItems() []handoffItem {
	var sit handoff.Situation
	if s := m.sel(); s.kind == api.KindTable || s.kind == api.KindShards {
		p := s.path
		sit.Table = &p
	}
	if r, ok := m.focusedRun(); ok {
		sit.Run = &r
	}
	var items []handoffItem
	for _, it := range handoff.Items(sit) {
		items = append(items, handoffItem{handoffActions[it.Kind], it.Label, it.DefaultPath(), func(ctx context.Context) (string, error) {
			return m.be.Handoff(ctx, it)
		}})
	}
	return items
}

func (m *Model) openHandoff() {
	m.hoItems = m.handoffItems()
	m.ov, m.ovSel, m.ovOff = ovHandoff, 0, 0
}

// handoffDirect runs a palette command such as "Hand-off: copy run".
func (m *Model) handoffDirect(id string) tea.Cmd {
	for _, it := range m.handoffItems() {
		if it.id == id {
			return m.handoffCopy(it)
		}
	}
	kind := handoff.KindRun
	for k, a := range handoffActions {
		if a == id {
			kind = k
		}
	}
	switch handoff.Missing(kind) {
	case handoff.NeedTable:
		m.toast = "select a table in the explorer first"
	case handoff.NeedRunError:
		m.toast = "the focused run has no error"
	case handoff.NeedRunRows:
		m.toast = "the focused run has no result rows"
	default:
		m.toast = "no run yet"
	}
	return nil
}

func (m *Model) handoffCopy(it handoffItem) tea.Cmd {
	m.toast = "preparing " + it.label + "…"
	ctx := m.ctx
	return func() tea.Msg {
		text, err := it.make(ctx)
		return handoffMsg{text: text, label: it.label, err: err}
	}
}

// handoffWrite renders the item and writes it. Without overwrite the file
// is created exclusively: one that appeared meanwhile is not replaced.
func (m *Model) handoffWrite(it handoffItem, path string, overwrite bool) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		text, err := it.make(ctx)
		if err == nil {
			err = writeHandoff(path, text, overwrite)
		}
		return handoffMsg{label: it.label, path: path, err: err}
	}
}

// writeHandoff writes the text (handoff.Write): without overwrite, a file
// that appeared meanwhile is not replaced.
func writeHandoff(path, text string, overwrite bool) error {
	err := handoff.Write(path, text, overwrite)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s was created meanwhile; write again to overwrite it", path)
	}
	return err
}

func (m *Model) onHandoff(msg handoffMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.toast = "hand-off failed: " + msg.err.Error()
	case msg.path != "":
		m.toast = "wrote " + msg.path
	default:
		m.toast = "copied " + strings.ToLower(msg.label[:1]) + msg.label[1:]
		return tea.SetClipboard(msg.text)
	}
	return nil
}

// handoffPopup lists what can be handed off to an agent (ctrl+x y).
type handoffPopup struct{}

func (handoffPopup) width(w int) int { return clamp(w-8, 30, 84) }

func (handoffPopup) page(m *Model, inner, _ int) scrollPage {
	n := len(m.hoItems)
	sel := clamp(m.ovSel, 0, max(0, n-1))
	rows, top := m.handoffLines(inner, sel)
	return scrollPage{title: "Hand-off to an agent", rows: rows, cur: sel + top, sel: &m.ovSel, items: n}
}

// handoffLines are the popup's rows, the cursor on entry sel, and the row
// of the first entry.
func (m *Model) handoffLines(w, sel int) (rows []string, top int) {
	var out []string
	z := ZoneOverlay
	hint := joinHints(keyHint(z, "copies to the clipboard", "open"), keyHint(z, "writes a file", "write"), keyHint(z, "closes", "back"))
	for _, l := range wrap(hint, w-2) {
		out = append(out, " "+sDim.Render(l))
	}
	out = append(out, "")
	top = len(out)
	if len(m.hoItems) == 0 {
		return append(out, " "+sDim.Render("nothing to hand off: select a table in the explorer, or run a query")), top
	}
	for i, it := range m.hoItems {
		out = append(out, newRB(i == sel).add(" "+it.label, sBase).line(w))
	}
	return out, top
}

func (handoffPopup) act(m *Model, act string) (tea.Cmd, bool) {
	if act != "open" && act != "copy" && act != "write" {
		return nil, false
	}
	if m.ovSel >= len(m.hoItems) {
		return nil, true
	}
	it := m.hoItems[m.ovSel]
	if act == "write" {
		m.openPrompt(promptState{
			kind: "handoff", title: "Write " + it.label, labels: []string{"file"},
			note: "enter writes · esc cancels", hoItem: it,
		}, it.path)
		return nil, true
	}
	m.ov = ovNone
	return m.handoffCopy(it), true
}

func (handoffPopup) hints(*Model) string { return footerHints(ZoneOverlay) }

// handoffSubmit writes the prompt's file (asking once before overwriting).
func (m *Model) handoffSubmit(p *promptState) tea.Cmd {
	path := expandHome(strings.TrimSpace(p.fields[0].value()))
	if path == "" {
		return nil
	}
	if st, err := os.Stat(path); err == nil && !p.armed {
		if st.IsDir() {
			p.note = sErr.Render("that is a folder: give a file name")
			return nil
		}
		p.armed = true
		p.note = sWarn.Render("the file exists: press enter again to overwrite it")
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		p.note = sErr.Render(err.Error())
		return nil
	}
	m.ov = ovNone
	return m.handoffWrite(p.hoItem, path, p.armed)
}
