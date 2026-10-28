package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// editorRows is the number of text lines the editor shows: its Pane less
// the title.
func (m *Model) editorRows() int {
	return max(1, m.paneRect(ZoneEditor).h-1)
}

// gridRows is the number of result rows the grid shows: its Pane less the
// title, the column header and the status line.
func (m *Model) gridRows() int {
	return max(1, m.paneRect(ZoneResults).h-3)
}

func stateStyle(s api.RunState) lipgloss.Style {
	switch s {
	case api.RunDone, api.RunEstimated:
		return sOk
	case api.RunFailed:
		return sErr
	case api.RunCancelled:
		return sDim
	}
	return sWarn
}

func stateWord(r api.RunInfo) string {
	switch {
	case r.CancelRequested && !r.State.Finished():
		return "cancel requested"
	case r.State == api.RunNeedsOK:
		return "waiting for confirmation"
	case r.State == api.RunEstimating:
		return "dry-run"
	case r.State == api.RunEstimated:
		return "dry-run ok"
	}
	return string(r.State)
}

func (m *Model) elapsed(r api.RunInfo) string {
	if m.o.StableOutput || r.Started == nil {
		return ""
	}
	end := m.o.Now()
	if r.Ended != nil {
		end = *r.Ended
	}
	d := end.Sub(*r.Started)
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// runSummary is the one-line description of a Run's outcome.
func (m *Model) runSummary(r api.RunInfo) string {
	parts := []string{fmt.Sprintf("#%d %s", r.ID, stateWord(r))}
	switch {
	case r.State == api.RunDone && r.HasResult:
		parts = append(parts, format.Count(r.TotalRows)+" rows")
	case r.State == api.RunDone:
		parts = append(parts, doneText(r))
	}
	if st := r.Stats; st != nil && r.State == api.RunDone {
		switch {
		case st.CacheHit:
			parts = append(parts, "cache hit")
		case st.ReservationID != "":
			parts = append(parts, format.Bytes(st.BytesProcessed)+" scanned (not billed)")
			if st.SlotMs > 0 {
				parts = append(parts, fmt.Sprintf("slot %.1fs", float64(st.SlotMs)/1000))
			}
		default:
			parts = append(parts, format.Bytes(st.BytesBilled)+" billed")
		}
	} else if r.Estimate != nil && !r.State.Finished() {
		parts = append(parts, "est "+estimateText(r.Estimate))
	}
	if e := m.elapsed(r); e != "" {
		parts = append(parts, e)
	}
	return strings.Join(parts, " · ")
}

// editorPane draws the Editor: its title and the buffer's text. It records
// where it drew the cursor, relative to the Pane, for the completion popup.
func (m *Model) editorPane(w, h int) []string {
	b := m.buf()

	name := b.title()
	if b.modified() {
		name += " ●"
	}
	ttl := fmt.Sprintf(" SQL · %s", name)
	vimOn := m.vimOn()
	placeholder := "type SQL · ctrl+r runs · ctrl+d dry-runs · tab completes"
	b.ed.insertCursor = false
	if vimOn {
		mode := b.vim.mode.String()
		pending := ""
		if b.vim.hasCount {
			pending += itoa(b.vim.count)
		}
		if b.vim.opCount > 0 {
			pending += itoa(b.vim.opCount)
		}
		pending += b.vim.op
		if b.vim.wait != "" && b.vim.wait != "leader" {
			pending += b.vim.wait
		} else if b.vim.wait == "leader" {
			pending += "␣"
		}
		ttl = fmt.Sprintf(" %s · %s", mode, name)
		if pending != "" {
			ttl += "  " + pending
		}
		placeholder = "i inserts · enter runs · space d dry-runs · : commands · ? help"
		b.ed.insertCursor = b.vim.mode == vimInsert
		if b.vim.mode != vimVisual && b.vim.mode != vimVisualLine {
			b.ed.vis = nil
		}
	} else {
		b.ed.vis = nil
	}
	if len(m.bufs) > 1 {
		ttl += fmt.Sprintf("  [%d/%d]", m.cur+1, len(m.bufs))
	}
	if r, ok := m.runs.Info(b.runID); ok {
		ttl += "  " + stateWord(r)
	}
	ttl += fmt.Sprintf("   %d:%d", b.ed.row+1, b.ed.col+1)
	out := []string{title(ttl, w, m.zone == ZoneEditor)}
	lines, cx, cy := b.ed.view(w, h-1, m.zone == ZoneEditor && m.ov == ovNone, m.be.Tokenize, placeholder)
	m.edX, m.edY = cx, cy+1
	out = append(out, lines...)
	for i := range out {
		out[i] = fit(out[i], w)
	}
	return fillTo(out, w, h)
}

// withCompletion draws the completion popup over the composed body, at the
// Editor's cursor; it may overlap a neighbouring Pane.
func (m *Model) withCompletion(body []string, ed rect, w int) []string {
	if !m.comp.open || m.zone != ZoneEditor || m.ov != ovNone {
		return body
	}
	box := m.completionBox(w)
	if len(box) == 0 {
		return body
	}
	cx, cy := ed.x+m.edX, ed.y+m.edY
	y := cy + 1
	if y+len(box) > len(body) {
		y = max(0, cy-len(box))
	}
	x := clamp(cx-2, 0, max(0, w-wid(box[0])))
	return overlay(body, box, x, y, w)
}

func (m *Model) resultsPane(w, h int) []string {
	focused := m.zone == ZoneResults
	r, ok := m.shownRun()
	if !ok {
		out := []string{title(" Results", w, focused)}
		hint := "no run yet: ctrl+r runs the buffer (or the selection), ctrl+d only estimates it"
		if m.vimOn() {
			hint = "no run yet: enter in Normal mode runs the buffer (or the Visual selection), space d only estimates it"
		}
		out = append(out, fit(" "+sDim.Render(hint), w))
		return fillTo(out, w, h)
	}
	g := m.grids[r.ID]
	out := []string{title(" Results · "+m.runSummary(r), w, focused)}
	add := func(text string, st lipgloss.Style) {
		for _, l := range wrap(text, w-2) {
			out = append(out, " "+st.Render(l))
		}
	}
	switch {
	case r.State == api.RunFailed && r.Error != nil:
		e := r.Error
		if mk, ok := m.runs.ErrorMark(r.ID); ok {
			add(fmt.Sprintf("line %d, column %d", mk.Line, mk.Col), sWarn)
		}
		add(e.Message, sErr)
		if e.Hint != "" {
			add(e.Hint, sDim)
		}
	case r.State == api.RunCancelled:
		add("cancelled", sDim)
	case r.State == api.RunEstimated:
		add("dry-run: "+estimateText(r.Estimate), sOk)
	case !r.State.Finished():
		line := stateWord(r)
		switch {
		case r.CancelRequested && !m.runs.Pricing().Capacity:
			line = "cancel requested: waiting for BigQuery to confirm (the query may still be billed)"
		case r.CancelRequested:
			line = "cancel requested: waiting for BigQuery to confirm"
		case r.State == api.RunQueued:
			line = "queued: waiting for a free slot (max_concurrent_queries)"
		case r.State == api.RunRunning && r.CancelFailed:
			line = "still running: the cancel request did not reach BigQuery. ctrl+c tries again"
		case r.State == api.RunRunning:
			line = "running… ctrl+c cancels"
		}
		add(line, sWarn)
		if r.Estimate != nil {
			add("estimate: "+estimateText(r.Estimate), sDim)
		}
	case r.ResultError != "":
		add("the query finished, but its rows could not be read: "+r.ResultError, sErr)
	case !r.HasResult:
		add("statement finished: "+doneText(r), sOk)
	case g == nil:
		add("loading…", sDim)
	case g.total == 0:
		hb := " "
		for _, c := range g.cols {
			hb += sBold.Render(c.Name) + " " + sFaint.Render(shortType(c)) + "  "
		}
		out = append(out, fit(hb, w), " "+sDim.Render("the query returned no rows"))
	default:
		if g.err != "" {
			add(g.err, sErr)
		}
		out = append(out, g.view(w, h-len(out)-1, focused)...)
		out = append(out, sFaint.Render(fit(" "+m.gridStatus(g), w)))
	}
	for i := range out {
		out[i] = fit(out[i], w)
	}
	return fillTo(out, w, h)
}

var candLetters = map[api.CandidateKind]string{
	api.CandProject: "P", api.CandDataset: "D", api.CandTable: "T", api.CandColumn: "c",
	api.CandCTE: "W", api.CandAlias: "a", api.CandFunction: "ƒ", api.CandKeyword: "k",
}

func (m *Model) completionBox(w int) []string {
	c := &m.comp
	rows := min(8, len(c.cands))
	s, e := window(c.sel, &c.off, len(c.cands), rows)
	labelW := 8
	for i := s; i < e; i++ {
		labelW = max(labelW, wid(c.cands[i].Label))
	}
	labelW = min(labelW, 40)
	bw := min(w, labelW+34)
	var out []string
	for i := s; i < e; i++ {
		cand := c.cands[i]
		r := newRB(i == c.sel)
		if r.bg == nil {
			r.bg = lipgloss.Color("235")
		}
		r.add(" "+candLetters[cand.Kind]+" ", sAcc).add(fit(cand.Label, labelW), sBase).add(" ", sBase)
		detail := cand.Detail
		if rest := bw - labelW - 5; wid(detail) > rest {
			detail = fit(detail, max(0, rest))
		}
		r.add(detail, sFaint)
		out = append(out, r.line(bw))
	}
	return out
}

// --- overlays of the query side

func (m *Model) promptBox(bw int) []string {
	p := &m.prompt
	inner := bw - 2
	labelW := 4
	for _, l := range p.labels {
		labelW = max(labelW, wid(l)+1)
	}
	labelW = min(labelW, inner/2)
	var lines []string
	for i := range p.fields {
		b := newRB(false)
		label := p.labels[i]
		if p.kind == "params" {
			label = "@" + label
		}
		b.add(" "+fit(label, labelW)+" ", sKey)
		b.l.WriteString(p.fields[i].render(api.ParsedQuery{}, i == p.idx, ""))
		if p.kind == "params" {
			if i < len(p.specs) && p.specs[i].Type != "" {
				b.addR(p.specs[i].Type+" ", sAcc)
			} else {
				b.addR(inferLabel(p.labels[i], p.fields[i].value())+" ", sFaint)
			}
		}
		if p.kind == "export" {
			b.addR(string(p.format)+" ", sAcc)
		}
		lines = append(lines, b.line(inner))
	}
	if p.note != "" {
		lines = append(lines, "")
		for _, l := range wrap(p.note, inner-2) {
			lines = append(lines, " "+sFaint.Render(l))
		}
	}
	return popup(p.title, lines, bw)
}

func (m *Model) confirmBox(bw int) []string {
	var lines []string
	for _, l := range m.confirm.lines {
		for _, wl := range wrap(l, bw-4) {
			lines = append(lines, " "+wl)
		}
	}
	yes := "run it"
	switch m.confirm.kind {
	case "quit":
		yes = "quit"
	case "closebuf":
		yes = "discard"
	}
	lines = append(lines, "", " "+sKey.Render("y")+sDim.Render(" / enter  "+yes+"     ")+sKey.Render("n")+sDim.Render(" / esc  cancel"))
	return popup("Confirm", lines, bw)
}

func (m *Model) runsLines(w int) []string {
	list := m.runs.List()
	if len(list) == 0 {
		return []string{" " + sDim.Render("no runs in this session yet")}
	}
	m.ovSel = clamp(m.ovSel, 0, len(list)-1)
	var out []string
	for i, r := range list {
		b := newRB(i == m.ovSel)
		b.add(fmt.Sprintf(" #%-3d ", r.ID), sDim).add(fit(stateWord(r), 17), stateStyle(r.State))
		size := ""
		switch {
		case r.Stats != nil:
			size = format.Bytes(r.Stats.BytesProcessed)
		case r.Estimate != nil:
			size = "~" + format.Bytes(r.Estimate.Bytes)
		}
		rows := ""
		if r.HasResult {
			rows = format.Count(r.TotalRows) + " rows"
		}
		b.add(fit(size, 11), sBase).add(fit(rows, 14), sBase)
		if e := m.elapsed(r); e != "" {
			b.add(fit(e, 8), sDim)
		}
		name := ""
		if buf, _ := m.bufferOf(r.Tag); buf != nil {
			name = buf.title()
		}
		b.addR(name+" ", sFaint)
		b.add(strings.Join(strings.Fields(r.SQL), " "), sFaint)
		out = append(out, b.line(w))
	}
	return out
}

func (m *Model) buffersLines(w int) []string {
	var out []string
	m.ovSel = clamp(m.ovSel, 0, max(0, len(m.bufs)+len(m.orphans)-1))
	for i, b := range m.bufs {
		r := newRB(i == m.ovSel)
		mark := "  "
		if i == m.cur {
			mark = "▸ "
		}
		r.add(" "+mark+fit(b.title(), 22)+" ", sBase)
		if run, ok := m.runs.Info(b.runID); ok {
			r.add(fit(stateWord(run), 18), stateStyle(run.State))
		} else {
			r.add(fit("", 18), sBase)
		}
		first := ""
		for _, l := range b.ed.lines {
			if strings.TrimSpace(string(l)) != "" {
				first = strings.TrimSpace(string(l))
				break
			}
		}
		r.add(first, sFaint)
		if b.modified() {
			r.addR("modified ", sWarn)
		}
		out = append(out, r.line(w))
	}
	if len(m.orphans) > 0 {
		out = append(out, "", " "+sBold.Render("Restore drafts")+sDim.Render("  from earlier sessions (kept 30 days)"))
		for i, o := range m.orphans {
			r := newRB(len(m.bufs)+i == m.ovSel)
			r.add("   "+fit(ago(m.o.Now(), &o.modTime)+" ago", 12), sDim).add(fit(fmt.Sprintf("%d lines", o.lines), 12), sDim).add(o.first, sFaint)
			out = append(out, r.line(w))
		}
	}
	return out
}

func inferLabel(name, value string) string {
	p := inferParamFn(name, value)
	if p.Null {
		return "NULL " + p.Type
	}
	return p.Type
}

// pricingText describes the detected pricing model for the status line.
func pricingText(p api.PricingModel) string {
	switch {
	case !p.Known:
		return ""
	case p.Capacity:
		res := p.ReservationID
		if i := strings.LastIndex(res, ":"); i >= 0 {
			res = res[i+1:]
		}
		ed := ""
		if p.Edition != "" {
			ed = " (" + strings.ToUpper(p.Edition[:1]) + strings.ToLower(p.Edition[1:]) + ")"
		}
		return "capacity: " + res + ed
	}
	return "on-demand"
}
