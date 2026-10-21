package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
)

const (
	gridPage     = runs.PageRows
	gridPages    = 10
	gridCellChar = 200
	gridMinW     = 4
	gridMaxW     = 40
)

var gridFormat = format.Options{MaxChars: gridCellChar, ArraySummary: true}

// grid is the results viewer of one Run: a window over server-side pages
// with a cell cursor. It never sorts or filters; the result is as BigQuery
// returned it.
type grid struct {
	run   int64
	cols  []api.PreviewColumn
	total int64

	widths []int
	hidden map[int]bool
	frozen bool

	row     int64
	col     int
	top     int64
	leftCol int

	pages   map[int64][][]api.Cell
	loading map[int64]bool
	err     string

	find string
}

type pageMsg struct {
	run   int64
	start int64
	page  api.PreviewPage
	err   error
}

func newGrid(info api.RunInfo) *grid {
	return &grid{run: info.ID, cols: info.Columns, total: info.TotalRows, hidden: map[int]bool{},
		pages: map[int64][][]api.Cell{}, loading: map[int64]bool{}}
}

func pageOf(row int64) int64 { return row - row%gridPage }

// cells returns a loaded row, or nil while its page is not loaded.
func (g *grid) cells(row int64) []api.Cell {
	p, ok := g.pages[pageOf(row)]
	if !ok {
		return nil
	}
	i := int(row - pageOf(row))
	if i >= len(p) {
		return nil
	}
	return p[i]
}

func shortType(c api.PreviewColumn) string {
	t := c.Type
	switch t {
	case "INTEGER", "INT64":
		t = "INT"
	case "STRING":
		t = "STR"
	case "FLOAT", "FLOAT64":
		t = "FLOAT"
	case "BOOLEAN":
		t = "BOOL"
	case "TIMESTAMP":
		t = "TS"
	case "RECORD", "STRUCT":
		t = "REC"
	case "NUMERIC":
		t = "NUM"
	case "DATETIME":
		t = "DT"
	}
	if c.Mode == "REPEATED" {
		t += "[]"
	}
	return t
}

// measure sets the column widths from the first loaded page.
func (g *grid) measure(rows [][]api.Cell) {
	g.widths = make([]int, len(g.cols))
	for i, c := range g.cols {
		w := wid(c.Name) + 1 + wid(shortType(c))
		for _, r := range rows {
			if i < len(r) {
				w = max(w, wid(cellText(r[i])))
			}
		}
		g.widths[i] = clamp(w, gridMinW, gridMaxW)
	}
}

func (g *grid) shownCols() []int {
	var out []int
	for i := range g.cols {
		if !g.hidden[i] {
			out = append(out, i)
		}
	}
	return out
}

// moveCol moves the cell cursor by d visible columns.
func (g *grid) moveCol(d int) {
	shown := g.shownCols()
	if len(shown) == 0 {
		return
	}
	at := 0
	for i, c := range shown {
		if c <= g.col {
			at = i
		}
	}
	g.col = shown[clamp(at+d, 0, len(shown)-1)]
}

func (g *grid) moveRow(d int64) {
	g.row = max(0, min(g.row+d, g.total-1))
}

// layout picks the columns that fit in w cells: the frozen first column, then
// whole columns from leftCol on, scrolled so the cursor column is visible.
func (g *grid) layout(w int) []int {
	shown := g.shownCols()
	if len(shown) == 0 || len(g.widths) != len(g.cols) {
		return nil
	}
	fits := func(left int) ([]int, bool) {
		var out []int
		used := 0
		start := 0
		if g.frozen {
			out = append(out, shown[0])
			used = g.widths[shown[0]] + 1
			start = 1
		}
		has := g.frozen && g.col == shown[0]
		for i := start; i < len(shown); i++ {
			c := shown[i]
			if c < left {
				continue
			}
			cw := g.widths[c] + 1
			if used+cw > w && len(out) > start {
				break
			}
			out = append(out, c)
			used += cw
			if c == g.col {
				has = true
			}
		}
		return out, has
	}
	if g.col < g.leftCol {
		g.leftCol = g.col
	}
	for {
		out, has := fits(g.leftCol)
		if has || g.leftCol >= g.col {
			return out
		}
		// scroll right by one whole column
		next := g.col
		for _, c := range shown {
			if c > g.leftCol {
				next = c
				break
			}
		}
		g.leftCol = next
	}
}

// need returns the page starts the window [top, top+h) spans that are
// neither loaded nor loading.
func (g *grid) need(h int) []int64 {
	var out []int64
	if g.total == 0 {
		return nil
	}
	// Scroll first: after a jump (G) the window is where the cursor is, not
	// where it was, and only that window is fetched: at most three pages,
	// never the pages in between.
	g.scroll(h)
	last := min(g.top+int64(max(h, 1))-1, g.total-1)
	for _, p := range []int64{pageOf(g.top), pageOf(last), pageOf(g.row)} {
		if _, ok := g.pages[p]; ok || g.loading[p] {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == p
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}

func (g *grid) store(start int64, rows [][]api.Cell) {
	delete(g.loading, start)
	g.pages[start] = rows
	if g.widths == nil {
		g.measure(rows)
	}
	// keep the pages nearest the cursor
	for len(g.pages) > gridPages {
		var far int64 = -1
		var dist int64 = -1
		for p := range g.pages {
			d := p - pageOf(g.row)
			if d < 0 {
				d = -d
			}
			if d > dist {
				far, dist = p, d
			}
		}
		delete(g.pages, far)
	}
}

// scroll keeps the cursor row inside h visible rows.
func (g *grid) scroll(h int) {
	if h < 1 {
		h = 1
	}
	if g.row < g.top {
		g.top = g.row
	}
	if g.row >= g.top+int64(h) {
		g.top = g.row - int64(h) + 1
	}
	g.top = max(0, min(g.top, max(0, g.total-int64(h))))
}

// search finds the next (dir 1) or previous (dir -1) cell containing the find
// text in the loaded pages, starting after the cursor. It reports the number
// of matching cells in the loaded pages.
func (g *grid) search(dir int) (found bool, hits int) {
	q := strings.ToLower(g.find)
	if q == "" {
		return false, 0
	}
	type pos struct {
		row int64
		col int
	}
	var starts []int64
	for p := range g.pages {
		starts = append(starts, p)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	var all []pos
	for _, p := range starts {
		for ri, r := range g.pages[p] {
			for ci, c := range r {
				if !g.hidden[ci] && !c.Null && strings.Contains(strings.ToLower(c.Text), q) {
					all = append(all, pos{p + int64(ri), ci})
				}
			}
		}
	}
	if len(all) == 0 {
		return false, 0
	}
	after := func(a pos) bool { return a.row > g.row || (a.row == g.row && a.col > g.col) }
	pick := all[0]
	if dir > 0 {
		for _, a := range all {
			if after(a) {
				pick = a
				break
			}
		}
	} else {
		pick = all[len(all)-1]
		for i := len(all) - 1; i >= 0; i-- {
			a := all[i]
			if a.row < g.row || (a.row == g.row && a.col < g.col) {
				pick = a
				break
			}
		}
	}
	g.row, g.col = pick.row, pick.col
	return true, len(all)
}

// view draws the header and the rows into w x h cells.
func (g *grid) view(w, h int, focused bool) []string {
	if h < 2 {
		return fillTo(nil, w, h)
	}
	body := h - 1
	g.scroll(body)
	numW := len(format.Count(max(g.total, 1))) + 1
	cols := g.layout(max(1, w-numW-1))
	var out []string

	hb := &strings.Builder{}
	hb.WriteString(sFaint.Render(pad("", numW) + " "))
	for _, c := range cols {
		name, typ := g.cols[c].Name, shortType(g.cols[c])
		cw := g.widths[c]
		st := sBold
		if c == g.col && focused {
			st = st.Foreground(lipgloss.Color("81"))
		}
		text := name
		rest := cw - wid(name)
		if rest > wid(typ) {
			hb.WriteString(st.Render(name) + strings.Repeat(" ", rest-wid(typ)) + sFaint.Render(typ) + " ")
			continue
		}
		hb.WriteString(st.Render(fit(text, cw)) + " ")
	}
	out = append(out, fit(hb.String(), w))

	for y := 0; y < body; y++ {
		row := g.top + int64(y)
		if row >= g.total {
			break
		}
		cells := g.cells(row)
		onRow := row == g.row
		var sb strings.Builder
		ns := sFaint
		if onRow {
			ns = sDim.Background(selBG2)
		}
		sb.WriteString(ns.Render(pad(strings.Repeat(" ", numW-len(format.Count(row+1)))+format.Count(row+1), numW) + " "))
		if cells == nil {
			st := sFaint
			if onRow {
				st = st.Background(selBG2)
			}
			sb.WriteString(st.Render("…"))
			out = append(out, fit(sb.String(), w))
			continue
		}
		for _, c := range cols {
			var cell api.Cell
			if c < len(cells) {
				cell = cells[c]
			}
			st := sBase
			if cell.Null {
				st = sFaint
			}
			switch {
			case onRow && c == g.col && focused:
				st = st.Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255"))
			case onRow:
				st = st.Background(selBG2)
			}
			sep := sBase
			if onRow {
				sep = sep.Background(selBG2)
			}
			sb.WriteString(st.Render(fit(cellText(cell), g.widths[c])) + sep.Render(" "))
		}
		out = append(out, fit(sb.String(), w))
	}
	return fillTo(out, w, h)
}

// --- model side

func (m *Model) grid() *grid {
	if len(m.bufs) == 0 {
		return nil
	}
	return m.grids[m.buf().viewID]
}

// shownRun is the Run the results pane shows.
func (m *Model) shownRun() (api.RunInfo, bool) {
	if len(m.bufs) == 0 {
		return api.RunInfo{}, false
	}
	info, ok := m.runs.Info(m.buf().viewID)
	return info, ok
}

func (m *Model) fetchPage(g *grid, start int64) tea.Cmd {
	g.loading[start] = true
	id := g.run
	return func() tea.Msg {
		page, err := m.be.RunRows(m.ctx, id, start, gridPage, gridFormat)
		return pageMsg{id, start, page, err}
	}
}

// gridEnsure loads the pages the visible window needs.
func (m *Model) gridEnsure() tea.Cmd {
	g := m.grid()
	if g == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, p := range g.need(m.gridRows()) {
		cmds = append(cmds, m.fetchPage(g, p))
	}
	return tea.Batch(cmds...)
}

func (m *Model) onPage(msg pageMsg) {
	g := m.grids[msg.run]
	if g == nil {
		return
	}
	if msg.err != nil {
		delete(g.loading, msg.start)
		g.err = msg.err.Error()
		return
	}
	g.err = ""
	g.total = msg.page.TotalRows
	g.store(msg.start, msg.page.Rows)
}

// maxGrids bounds the result grids kept in memory. A grid that was dropped is
// rebuilt from the Run when its result is opened again.
const maxGrids = 8

// trimGrids drops the grids no buffer shows, oldest Run first, down to maxGrids.
func (m *Model) trimGrids() {
	shown := map[int64]bool{}
	for _, b := range m.bufs {
		shown[b.viewID] = true
	}
	for len(m.grids) > maxGrids {
		var victim int64 = -1
		for id := range m.grids {
			if !shown[id] && (victim < 0 || id < victim) {
				victim = id
			}
		}
		if victim < 0 {
			return
		}
		delete(m.grids, victim)
	}
}

func (m *Model) gridStatus(g *grid) string {
	if g.total == 0 {
		return "no rows"
	}
	s := fmt.Sprintf("row %s of %s", format.Count(g.row+1), format.Count(g.total))
	if g.col < len(g.cols) {
		c := g.cols[g.col]
		s += " · " + c.Name + " " + c.Type
		if c.Mode == "REPEATED" {
			s += " REPEATED"
		}
	}
	if n := len(g.hidden); n > 0 {
		s += fmt.Sprintf(" · %d hidden (U shows)", n)
	}
	if g.frozen {
		s += " · first column frozen"
	}
	return s
}
