package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/handoff"
)

// Hand-off (spec 14.4): text for an agent about the selected table or the
// focused Run, copied to the clipboard (OSC 52) or written to a file. The
// core renders the text; this file only picks what and where.

// handoffDir is where hand-off files go by default (relative to the
// working directory, so an agent started there finds them).
const handoffDir = ".bqt-handoff"

type handoffItem struct {
	id    string // the palette action
	label string
	file  string // default file name
	make  func(ctx context.Context) (string, error)
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
		if r, ok := m.runs[id]; ok && id != 0 {
			return r, true
		}
	}
	return api.RunInfo{}, false
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// handoffItems lists what can be handed off now, in menu order.
func (m *Model) handoffItems() []handoffItem {
	var items []handoffItem
	if s := m.sel(); (s.kind == api.KindTable || s.kind == api.KindShards) && s.path.Table != "" {
		p := s.path
		base := unsafeFile.ReplaceAllString(p.String(), "_")
		schema := func(ctx context.Context) (api.TableSchema, error) {
			d, err := m.be.TableDetail(ctx, p)
			if err != nil {
				return api.TableSchema{}, err
			}
			return engine.SchemaOf(d), nil
		}
		items = append(items,
			handoffItem{"hoschemamd", "Schema of " + p.String() + " (markdown)", base + ".md", func(ctx context.Context) (string, error) {
				s, err := schema(ctx)
				return handoff.SchemaMarkdown(s), err
			}},
			handoffItem{"hoschemajson", "Schema of " + p.String() + " (JSON, as bqt schema)", base + ".json", func(ctx context.Context) (string, error) {
				s, err := schema(ctx)
				return handoff.SchemaJSON(s), err
			}})
	}
	r, ok := m.focusedRun()
	if !ok {
		return items
	}
	if r.Error != nil {
		info := r
		items = append(items, handoffItem{"hoerror", fmt.Sprintf("Query and error of run #%d", r.ID), "query-error.md", func(context.Context) (string, error) {
			return handoff.QueryError(info.SQL, *info.Error, info.ExecutionProject, info.Location), nil
		}})
	}
	if r.HasResult {
		for _, f := range []struct {
			id  string
			f   handoff.SampleFormat
			ext string
		}{{"hosamplemd", handoff.Markdown, "md"}, {"hosampletsv", handoff.TSV, "tsv"}, {"hosamplendjson", handoff.NDJSON, "ndjson"}} {
			id, cols := r.ID, r.Columns
			items = append(items, handoffItem{f.id, fmt.Sprintf("First %d rows of run #%d (%s)", handoff.SampleRows, r.ID, f.ext), fmt.Sprintf("sample-%d.%s", r.ID, f.ext),
				func(ctx context.Context) (string, error) {
					page, err := m.be.RunRows(ctx, id, 0, handoff.SampleRows, format.Options{})
					if err != nil {
						return "", err
					}
					if len(page.Columns) > 0 {
						cols = page.Columns
					}
					return handoff.Sample(cols, page.Rows, f.f), nil
				}})
		}
	}
	info := r
	items = append(items, handoffItem{"horun", fmt.Sprintf("Run #%d: SQL, estimate, state, outcome", r.ID), fmt.Sprintf("run-%d.md", r.ID), func(context.Context) (string, error) {
		return handoff.Run(info), nil
	}})
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
	switch id {
	case "hoschemamd", "hoschemajson":
		m.toast = "select a table in the explorer first"
	case "hoerror":
		m.toast = "the focused run has no error"
	case "hosamplemd", "hosampletsv", "hosamplendjson":
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

func (m *Model) handoffWrite(it handoffItem, path string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		text, err := it.make(ctx)
		if err == nil {
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.WriteFile(path, []byte(text), 0o644)
			}
		}
		return handoffMsg{label: it.label, path: path, err: err}
	}
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

func (m *Model) handoffLines(w int) []string {
	var out []string
	for _, l := range wrap("enter copies to the clipboard · w writes a file · esc closes", w-2) {
		out = append(out, " "+sDim.Render(l))
	}
	out = append(out, "")
	m.hoTop = len(out)
	if len(m.hoItems) == 0 {
		return append(out, " "+sDim.Render("nothing to hand off: select a table in the explorer, or run a query"))
	}
	for i, it := range m.hoItems {
		out = append(out, newRB(i == m.ovSel).add(" "+it.label, sBase).line(w))
	}
	return out
}

func (m *Model) handoffOverlayAction(act string) tea.Cmd {
	n := len(m.hoItems)
	switch act {
	case "down":
		m.ovSel = min(m.ovSel+1, max(0, n-1))
	case "up":
		m.ovSel = max(0, m.ovSel-1)
	case "back":
		m.ov = ovNone
	case "open", "copy":
		if m.ovSel < n {
			m.ov = ovNone
			return m.handoffCopy(m.hoItems[m.ovSel])
		}
	case "write":
		if m.ovSel < n {
			it := m.hoItems[m.ovSel]
			m.openPrompt(promptState{kind: "handoff", title: "Write " + it.label, labels: []string{"file"},
				note: "enter writes · esc cancels", hoItem: it}, filepath.Join(".", handoffDir, it.file))
		}
	default:
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

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
	return m.handoffWrite(p.hoItem, path)
}
