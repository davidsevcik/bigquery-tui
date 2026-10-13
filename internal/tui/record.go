package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// recordData is the row shown in the record popup. The preview holds clipped
// cells, so a row with clipped values is read again in full.
type recordData struct {
	key   string
	row   int
	cells []api.Cell // full values once read; nil shows the preview's cells
	busy  bool
	err   string
}

type recordMsg struct {
	key  string
	row  int
	page api.PreviewPage
	err  error
}

// previewRows returns the loaded preview of the selected table.
func (m *Model) previewRows() (string, *api.PreviewPage) {
	key := m.sel().detailKey()
	if d := m.details[key]; key != "" && d != nil && d.prev != nil && len(d.prev.Rows) > 0 {
		return key, d.prev
	}
	return key, nil
}

// openRecord shows the selected preview row as a full record.
func (m *Model) openRecord() tea.Cmd {
	if _, p := m.previewRows(); p == nil {
		m.toast = "load a preview first (p): J then shows the selected row as a full record"
		return nil
	}
	m.ov, m.tab = ovRecord, 1
	return m.loadRecord()
}

func (m *Model) loadRecord() tea.Cmd {
	key, p := m.previewRows()
	if p == nil {
		m.ov = ovNone
		return nil
	}
	m.prevRow = clamp(m.prevRow, 0, len(p.Rows)-1)
	row := m.prevRow
	m.rec, m.ovOff = &recordData{key: key, row: row}, 0
	clipped := false
	for _, c := range p.Rows[row] {
		clipped = clipped || c.Truncated
	}
	if !clipped {
		return nil
	}
	m.rec.busy = true
	path := m.sel().path
	return func() tea.Msg {
		page, err := m.be.Preview(m.ctx, path, p.StartIndex+int64(row), 1, 0)
		return recordMsg{key, row, page, err}
	}
}

func (m *Model) onRecord(msg recordMsg) {
	r := m.rec
	if r == nil || r.key != msg.key || r.row != msg.row {
		return
	}
	r.busy = false
	switch {
	case msg.err != nil:
		r.err = msg.err.Error()
	case len(msg.page.Rows) == 1:
		r.cells = msg.page.Rows[0]
	}
}

// recordStep moves the popup to the next or previous loaded row.
func (m *Model) recordStep(d int) tea.Cmd {
	_, p := m.previewRows()
	if p == nil {
		return nil
	}
	next := clamp(m.prevRow+d, 0, len(p.Rows)-1)
	if next == m.prevRow {
		if d > 0 {
			m.toast = "last loaded row: close and press p to load more"
		}
		return nil
	}
	m.prevRow = next
	return m.loadRecord()
}

func (m *Model) recordLines(w int) (string, []string) {
	_, p := m.previewRows()
	r := m.rec
	if p == nil || r == nil || r.row >= len(p.Rows) {
		return "Record", []string{" " + sDim.Render("the preview is no longer loaded")}
	}
	ttl := fmt.Sprintf("Record %d of %d · %s", r.row+1, len(p.Rows), m.sel().path.Table)
	cells := r.cells
	if cells == nil {
		cells = p.Rows[r.row]
	}
	var out []string
	switch {
	case r.busy:
		out = append(out, " "+sDim.Render("reading the full values…"), "")
	case r.err != "":
		out = append(out, " "+sWarn.Render("long values are clipped: "+r.err), "")
	}
	nameW := 4
	for _, c := range p.Columns {
		nameW = max(nameW, wid(c.Name))
	}
	nameW = min(nameW, max(8, w/3))
	valW := max(8, w-nameW-3)
	for i, c := range p.Columns {
		var cell api.Cell
		if i < len(cells) {
			cell = cells[i]
		}
		for li, l := range recordValue(c, cell, valW) {
			name := ""
			if li == 0 {
				name = c.Name
			}
			out = append(out, " "+sKey.Render(fit(name, nameW))+" "+l)
		}
	}
	return ttl, out
}

// recordValue lays one cell out over as many lines as it needs. Nested and
// JSON values are indented; everything else is wrapped.
func recordValue(c api.PreviewColumn, cell api.Cell, w int) []string {
	if cell.Null {
		return []string{sFaint.Render("NULL")}
	}
	text := cellText(cell)
	nested := c.Mode == "REPEATED" || c.Type == "RECORD" || c.Type == "STRUCT" || c.Type == "JSON"
	if nested && !cell.Truncated {
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(cell.Text), "", "  ") == nil {
			var out []string
			for _, l := range strings.Split(buf.String(), "\n") {
				trim := strings.TrimLeft(l, " ")
				indent := l[:len(l)-len(trim)]
				for wi, part := range hardWrap(trim, max(8, w-len(indent))) {
					if wi == 0 {
						part = colorJSON(part)
					}
					out = append(out, indent+part)
				}
			}
			return out
		}
	}
	if text == "" {
		return []string{sFaint.Render(`""`)}
	}
	return hardWrap(text, w)
}

func colorJSON(l string) string {
	if i := strings.Index(l, `": `); i > 0 && strings.HasPrefix(l, `"`) {
		return sAcc.Render(l[:i+1]) + ": " + l[i+3:]
	}
	return l
}

// hardWrap breaks text at w cells, at a space when the line has one.
func hardWrap(s string, w int) []string {
	var out []string
	for wid(s) > w {
		cut, cells, space := 0, 0, -1
		for i, r := range s {
			rw := wid(string(r))
			if cells+rw > w {
				break
			}
			cells += rw
			cut = i + len(string(r))
			if r == ' ' {
				space = cut
			}
		}
		if cut == 0 { // one rune wider than the pane
			cut = len(string([]rune(s)[:1]))
		} else if space > w/2 {
			cut = space
		}
		out = append(out, strings.TrimRight(s[:cut], " "))
		s = s[cut:]
	}
	return append(out, s)
}
