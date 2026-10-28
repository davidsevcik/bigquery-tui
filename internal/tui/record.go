package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// recordData is the row shown in the record popup: a row of the grid the
// Results show (a Run's or the Sample's). Grids hold clipped cells, so the
// row is read again in full through the grid's row source.
type recordData struct {
	g     *grid
	row   int64
	total int64 // rows n/N can reach
	name  string
	cols  []api.PreviewColumn
	cells []api.Cell
	busy  bool
	err   string
}

type recordMsg struct {
	g    *grid
	row  int64
	page api.PreviewPage
	err  error
}

// recordCellMax is the size above which a value is clipped in the record view.
const recordCellMax = 1 << 20

var recordFormat = format.Options{MaxChars: recordCellMax, KeepNewlines: true}

// openGridRecord shows the grid's cursor row as a full record.
func (m *Model) openGridRecord(g *grid) tea.Cmd {
	if g == nil || g.total == 0 {
		return nil
	}
	m.ov, m.ovOff = ovRecord, 0
	m.rec = &recordData{g: g, row: g.row, total: g.total, name: g.name, cols: g.cols, cells: g.cells(g.row), busy: true}
	row := g.row
	return func() tea.Msg {
		page, err := g.src(m.ctx, row, 1, recordFormat)
		return recordMsg{g: g, row: row, page: page, err: err}
	}
}

func (m *Model) onRecord(msg recordMsg) {
	r := m.rec
	if r == nil || r.g != msg.g || r.row != msg.row {
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

// recordStep moves the popup to the next or previous row.
func (m *Model) recordStep(d int) tea.Cmd {
	if m.rec == nil || m.rec.g == nil || !m.kept(m.rec.g) {
		return nil
	}
	g := m.rec.g
	before := g.row
	g.moveRow(int64(d))
	if g.row == before {
		return nil
	}
	return tea.Batch(m.openGridRecord(g), m.gridEnsure())
}

func (m *Model) recordLines(w int) (string, []string) {
	r := m.rec
	if r == nil {
		return "Record", []string{" " + sDim.Render("nothing to show")}
	}
	ttl := fmt.Sprintf("Record %s of %s · %s", format.Count(r.row+1), format.Count(r.total), r.name)
	var out []string
	switch {
	case r.busy:
		out = append(out, " "+sDim.Render("reading the full values…"), "")
	case r.err != "":
		out = append(out, " "+sWarn.Render("long values are clipped: "+r.err), "")
	}
	if r.cells == nil {
		return ttl, out
	}
	nameW := 4
	for _, c := range r.cols {
		nameW = max(nameW, wid(c.Name))
	}
	nameW = min(nameW, max(8, w/3))
	valW := max(8, w-nameW-3)
	for i, c := range r.cols {
		var cell api.Cell
		if i < len(r.cells) {
			cell = r.cells[i]
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
// JSON values are indented; text is wrapped, keeping its own line breaks.
func recordValue(c api.PreviewColumn, cell api.Cell, w int) []string {
	if cell.Null {
		return []string{sFaint.Render("NULL")}
	}
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
	if cell.Text == "" {
		return []string{sFaint.Render(`""`)}
	}
	var out []string
	for _, l := range strings.Split(cell.Text, "\n") {
		out = append(out, hardWrap(l, w)...)
	}
	if cell.Truncated {
		out = append(out, sWarn.Render("… clipped: the value is longer than what the record view loads"))
	}
	return out
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
