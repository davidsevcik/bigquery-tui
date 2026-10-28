package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

func ago(now time.Time, t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(*t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m *Model) health(n api.BrowseNode, long bool) (string, lipgloss.Style) {
	a := ago(m.o.Now(), n.SyncedAt)
	switch {
	case n.UnitStatus == api.UnitInaccessible:
		if long {
			return "✗ inaccessible · synced " + a, sErr
		}
		return "✗ " + a, sErr
	case n.UnitStatus == api.UnitDegraded:
		if long {
			return "⚠ degraded · synced " + a, sWarn
		}
		return "⚠ " + a, sWarn
	case n.SyncedAt == nil:
		if long {
			return "● never synced", sWarn
		}
		return "● never", sWarn
	case n.Stale:
		if long {
			return "● stale · synced " + a + " ago", sWarn
		}
		return "● " + a, sWarn
	}
	if long {
		return "✓ synced " + a + " ago", sOk
	}
	return "✓ " + a, sOk
}

// tooSmall reports that the screen only says "bqt needs a larger terminal".
func (m *Model) tooSmall() bool { return m.w < 20 || m.h < 6 }

// render builds the whole screen as lines.
func (m *Model) render() []string {
	w, h := m.w, m.h
	if m.tooSmall() {
		return []string{"bqt needs a larger terminal"}
	}
	bodyH := h - 2
	body := m.panesBody(w, bodyH)

	switch m.ov {
	case ovPicker:
		bw := clamp(w-4, 40, 170)
		bh := max(8, bodyH-2)
		body = overlay(body, m.pickerBox(bw, bh), (w-bw)/2, 1, w)
	case ovJump:
		bw := clamp(w-8, 30, 96)
		bh := clamp(bodyH-4, 8, 24)
		body = overlay(body, m.jumpBox(bw, bh), (w-bw)/2, max(0, (bodyH-bh)/3), w)
	case ovPalette:
		bw := clamp(w-12, 30, 70)
		bh := clamp(bodyH-4, 6, 18)
		body = overlay(body, m.paletteBox(bw, bh), (w-bw)/2, max(0, (bodyH-bh)/3), w)
	case ovPrompt:
		bw := clamp(w-8, 30, 90)
		box := m.promptBox(bw)
		body = overlay(body, box, (w-bw)/2, max(0, (bodyH-len(box))/3), w)
	case ovConfirm:
		bw := clamp(w-8, 30, 80)
		box := m.confirmBox(bw)
		body = overlay(body, box, (w-bw)/2, max(0, (bodyH-len(box))/3), w)
	case ovHelp, ovProblems, ovSync, ovRecord, ovRuns, ovBuffers, ovProjects, ovHandoff:
		bw := clamp(w-6, 30, 96)
		if m.ov == ovProjects || m.ov == ovHandoff {
			bw = clamp(w-8, 30, 84)
		}
		if m.ov == ovRuns || m.ov == ovBuffers {
			bw = clamp(w-4, 30, 140)
		}
		box := m.infoBox(bw, bodyH-2)
		body = overlay(body, box, (w-bw)/2, max(0, (bodyH-len(box))/2), w)
	}
	if m.tut != nil {
		body = append(body, m.tutorialPanel(w)...)
	}
	return append(body, m.statusLine(w), m.footerLine(w))
}

func (m *Model) statusLine(w int) string {
	b := &rb{bg: lipgloss.Color("236")}
	exec, _ := m.be.ExecutionProject()
	if exec == "" {
		exec = "none"
	}
	b.add(" bqt ", sBold).add("exec ", sDim).add(exec, sAcc)
	a := m.arr()
	b.add("  layout: "+m.layoutName(), sDim)
	if a.zoom != "" {
		b.add(" (maximised)", sDim)
	}
	if a.stepped {
		b.add(" (small terminal)", sDim)
	}
	if m.zone == ZoneExplorer {
		b.add("  "+styleNames[m.style], sDim)
	}
	ps := m.projects()
	switch len(ps) {
	case 0:
		b.add("  no projects selected", sWarn)
	case 1:
		b.add("  project ", sDim).add(ps[0].Name, sAcc)
	}
	if pt := pricingText(m.runs.Pricing()); pt != "" {
		b.add("  "+pt, sDim)
	}
	if act := m.activeRuns(); len(act) > 0 {
		b.add(fmt.Sprintf("  ▶ %d run(s)", len(act)), sWarn)
	}
	if m.draftErr != "" {
		b.add("  draft not saved (retrying): "+m.draftErr, sErr)
	}
	if m.exp != nil {
		b.add(fmt.Sprintf("  export %s/%s rows (ctrl+c cancels)", format.Count(m.expEv.Rows), format.Count(m.expEv.Total)), sWarn)
	}
	switch {
	case m.cfgErr != "":
		b.add("  config error (using the last good config): "+m.cfgErr, sErr)
	case m.syncing:
		b.add(fmt.Sprintf("  ⟳ sync %d/%d units", m.syncDone, m.syncTotal), sWarn)
	case m.syncNeeded:
		b.add("  sync needed: "+m.syncReason+" (R syncs)", sWarn)
	default:
		if n := m.problemCount(); n > 0 {
			b.add(fmt.Sprintf("  ⚠ %d sync problem(s) (gp)", n), sWarn)
		}
	}
	if len(ps) > 1 {
		// after the messages: a config error or a sync state matters more
		b.add(fmt.Sprintf("  %d projects", len(ps)), sDim)
	}
	if m.status != nil {
		b.addR(fmt.Sprintf("%s datasets · %s tables ", format.Count(int64(m.status.Datasets)), format.Count(int64(m.status.Tables))), sDim)
	}
	return b.line(w)
}

func (m *Model) problemCount() int {
	if m.status == nil {
		return 0
	}
	n := 0
	for _, p := range m.status.Projects {
		if !p.Selected {
			continue // deselected: not refreshed, so its problems are not shown
		}
		for _, u := range p.Units {
			if u.Status != api.UnitOK || u.NamesOnly > 0 || u.Inaccessible > 0 {
				n++
			}
		}
	}
	return n
}

func (m *Model) footerLine(w int) string {
	if m.toast != "" {
		return fit(" "+sWarn.Render(m.toast), w)
	}
	var hint string
	switch {
	case m.ov == ovJump:
		hint = "type to search · ↑↓ select · " + footerHints(ZoneSearch)
	case m.ov == ovRecord:
		hint = "n/N next/previous row · j/k scroll · y copy as JSON · " + footerHints(ZoneOverlay)
	case m.ov == ovRuns:
		hint = "↑↓ select · enter show the result · c cancel the run · esc close"
	case m.ov == ovBuffers:
		hint = "↑↓ select · enter open or restore · esc close"
	case m.ov == ovPrompt || m.ov == ovConfirm:
		hint = ""
	case m.ov == ovPicker:
		hint = "type to search · ↑↓ select · enter open · tab favorites/history · "
		if m.pk.scope == scopeFavorites {
			hint += "ctrl+r rename · ctrl+d delete"
		} else {
			hint += "ctrl+r show result · ctrl+s save as favorite"
		}
		hint += " · ctrl+e/y scroll · esc close"
	case m.ov == ovNone && m.zone == ZoneEditor:
		hint = footerHints(ZoneEditor) + " · ctrl+x l runs · " + footerHints(ZoneGlobal)
		if m.vimOn() {
			switch m.buf().vim.mode {
			case vimInsert:
				hint = "esc normal mode · ctrl+r run · ctrl+d dry-run · tab complete · ctrl+n force completion"
			case vimNormal:
				hint = "i insert · enter run · space d dry-run · v select · / find · u undo · : commands · ? help · ctrl+o pane"
			default:
				hint = "enter run the selection · d delete · y yank · c change · > < indent · esc normal mode"
			}
		}
		if m.comp.open {
			hint = "tab accept · ↑↓ select · esc dismiss · enter is a new line"
		}
	case m.ov == ovNone && m.zone == ZoneResults:
		hint = footerHints(ZoneResults) + " · " + footerHints(ZoneGlobal)
	case m.ov != ovNone:
		hint = footerHints(ZoneOverlay)
	case m.zone == ZoneSchema:
		hint = footerHints(ZoneSchema)
	case m.style == styleList && m.flat.typing:
		hint = "type to filter · enter/esc back to the list"
	case m.typingFilter() != nil:
		hint = "type to filter · enter/esc back to the list · esc there clears the filter"
	default:
		hint = footerHints(ZoneExplorer, ZoneGlobal)
	}
	if m.pendingKey != "" {
		hint = m.pendingKey + " …"
	}
	return fit(" "+sFaint.Render(hint), w)
}

// panesBody draws every Pane of the arrangement into its rectangle, with the
// separators and the pane bar, and joins them row by row.
func (m *Model) panesBody(w, h int) []string {
	a := m.arr()
	type piece struct {
		r     rect
		lines []string
	}
	var pieces []piece
	for _, z := range paneOrder {
		if r, ok := a.rects[z]; ok {
			pieces = append(pieces, piece{r, fillTo(m.paneLines(z, r.w, r.h), r.w, r.h)})
		}
	}
	for _, s := range a.seps {
		pieces = append(pieces, piece{s, vsep(s.h)})
	}
	if a.bar != nil {
		pieces = append(pieces, piece{*a.bar, []string{paneBar(a, a.bar.w)}})
	}
	slices.SortFunc(pieces, func(p, q piece) int { return p.r.x - q.r.x })
	out := make([]string, h)
	for y := range out {
		var sb strings.Builder
		for _, p := range pieces {
			if y >= p.r.y && y < p.r.y+p.r.h {
				sb.WriteString(fit(p.lines[y-p.r.y], p.r.w))
			}
		}
		out[y] = sb.String()
	}
	if r, ok := a.rects[ZoneEditor]; ok {
		out = m.withCompletion(out, r, w)
	}
	return out
}

// paneLines draws one Pane in exactly w×h cells.
func (m *Model) paneLines(z Zone, w, h int) []string {
	switch z {
	case ZoneExplorer:
		return m.explorerPane(w, h)
	case ZoneSchema:
		return m.schemaPane(m.sel(), w, h)
	case ZoneEditor:
		return m.editorPane(w, h)
	case ZoneResults:
		return m.resultsPane(w, h)
	}
	return nil
}

// explorerPane draws the Explorer in its style, with the filter header while
// a filter is being typed.
func (m *Model) explorerPane(w, h int) []string {
	list := m.millerList
	switch m.style {
	case styleList:
		return m.searchBody(m.flat, w, h, " filter ▸ ")
	case styleTree:
		list = m.treeList
	}
	if f := m.typingFilter(); f != nil && h > 4 {
		hdr := m.filterHeader(f, w, m.filterWhat())
		return append(hdr, list(w, h-len(hdr))...)
	}
	return list(w, h)
}

// paneBar is the row above a Pane shown on its own (the focus Layout, a
// maximised Pane, browse's shared Editor and Results slot): the Panes it
// stands for, the drawn one in brackets.
func paneBar(a arrangement, w int) string {
	b := &rb{bg: lipgloss.Color("236")}
	at := a.bar.x
	for _, l := range barLabels(a) {
		b.add(strings.Repeat(" ", l.x-at), sDim)
		if l.drawn {
			b.add(l.text, sTitleF)
		} else {
			b.add(l.text, sDim)
		}
		at = l.x + l.w
	}
	b.add("   "+a.barHint, sFaint)
	return b.line(w)
}

// --- lists

func (m *Model) millerList(w, h int) []string {
	ml := &m.mil
	first := m.milFirst()
	n := clamp(w/24, 1, 3-first)
	start := clamp(ml.col-(n-1), first, 3-n)
	avail := w - (n - 1)
	var blocks [][]string
	used := 0
	// Dataset and table names are long; project ids are few and short.
	weight := []int{3, 5, 5}
	sum := 0
	for i := 0; i < n; i++ {
		sum += weight[start+i]
	}
	for i := 0; i < n; i++ {
		cw := avail * weight[start+i] / sum
		if i == n-1 {
			cw = avail - used
		}
		used += cw
		blocks = append(blocks, m.millerCol(start+i, cw, h))
		if i < n-1 {
			blocks = append(blocks, vsep(h))
		}
	}
	out := make([]string, h)
	for r := 0; r < h; r++ {
		var sb strings.Builder
		for _, bl := range blocks {
			sb.WriteString(bl[r])
		}
		out[r] = sb.String()
	}
	return out
}

func (m *Model) millerCol(c, w, h int) []string {
	ml := &m.mil
	nodes := ml.cols[c]
	total := len(nodes)
	name := []string{"Projects", "Datasets", "Tables"}[c]
	if c == 2 && ml.family != nil {
		name = ml.family.Name
	}
	cur := ml.cur[c]
	focused := c == ml.col && m.zone == ZoneExplorer
	hdr := fmt.Sprintf(" %s %s/%s", name, format.Count(int64(min(cur+1, total))), format.Count(int64(total)))
	f := &ml.f[c]
	if f.active() {
		hdr += fmt.Sprintf(" of %s ▸ %s", format.Count(int64(ml.unfiltered[c])), f.in.value())
	}
	if c > ml.col {
		hdr = fmt.Sprintf(" %s (%s)", name, format.Count(int64(total)))
	}
	out := []string{title(hdr, w, focused)}
	if total == 0 {
		msg := "empty"
		switch {
		case f.active() && ml.unfiltered[c] > 0:
			msg = "no match for “" + f.in.value() + "”"
		case c == 0:
			msg = "no projects selected: ctrl+x p picks them"
		}
		out = append(out, " "+sDim.Render(msg))
		return fillTo(out, w, h)
	}
	st, en := window(cur, &ml.off[c], total, h-1)
	for i := st; i < en; i++ {
		selected := i == cur && c <= ml.col
		b := newRB(selected)
		if selected && c < ml.col {
			b.bg = selBG2
		}
		m.nodeRow(b, nodes[i], c == 0)
		out = append(out, b.line(w))
	}
	return fillTo(out, w, h)
}

// nodeRow draws one browse node (shared by Miller columns and the tree).
func (m *Model) nodeRow(b *rb, n api.BrowseNode, bold bool) {
	ns := sBase
	if n.Stale {
		ns = sDim
	}
	switch n.Kind {
	case api.KindProject:
		b.add(n.Name, sBold)
		t, s := m.health(n, false)
		b.addR(t+" ", s)
	case api.KindDataset:
		b.add(n.Name, ns)
		switch n.Status {
		case api.DatasetNamesOnly:
			b.addR("names only ", sWarn)
		case api.DatasetInaccessible:
			b.addR("no access ", sErr)
		default:
			b.addR(format.Count(int64(n.Children))+" ", sFaint)
		}
	case api.KindShards:
		b.add(typeLetter(n.Type)+" ", typeStyle(n.Type)).add(n.Name, ns)
		b.addR(fmt.Sprintf("×%s ", format.Count(int64(n.Children))), sAcc)
	default:
		b.add(typeLetter(n.Type)+" ", typeStyle(n.Type)).add(n.Name, ns)
		if n.NamesOnly {
			b.addR("names only ", sWarn)
		}
	}
}

func (m *Model) treeList(w, h int) []string {
	t := &m.tree
	hdr := fmt.Sprintf(" Explorer  %s/%s", format.Count(int64(min(t.cur+1, len(t.rows)))), format.Count(int64(len(t.rows))))
	if t.f.active() {
		hdr += " ▸ " + t.f.in.value()
		if t.f.answered {
			hdr += " · " + plural(t.f.count, "match", "matches")
		}
	}
	out := []string{title(hdr, w, m.zone == ZoneExplorer)}
	if len(t.rows) == 0 {
		msg := "no projects selected: ctrl+x p picks them"
		if t.f.active() {
			msg = "no match for “" + t.f.in.value() + "”"
		}
		out = append(out, " "+sDim.Render(msg))
		return fillTo(out, w, h)
	}
	st, en := window(t.cur, &t.off, len(t.rows), h-1)
	for i := st; i < en; i++ {
		r := t.rows[i]
		b := newRB(i == t.cur)
		b.add(strings.Repeat("  ", r.depth), sBase)
		switch r.node.Kind {
		case api.KindProject, api.KindDataset, api.KindShards:
			arrow := "▸ "
			if t.isOpen(r.node) {
				arrow = "▾ "
			}
			b.add(arrow, sDim)
		default:
			b.add("  ", sBase)
		}
		m.nodeRow(b, r.node, r.depth == 0)
		out = append(out, b.line(w))
	}
	return fillTo(out, w, h)
}

// filterWhat names the list the filter line narrows.
func (m *Model) filterWhat() string {
	if m.style == styleTree {
		return "tree"
	}
	return []string{"projects", "datasets", "tables"}[m.mil.col]
}

func scopeLabel(st *searchState) string {
	if st.scope == nil {
		return ""
	}
	return st.scope.String()
}

func (m *Model) searchBody(st *searchState, w, h int, prompt string) []string {
	b := newRB(false)
	b.add(prompt, sAcc)
	b.l.WriteString(st.in.render(st.parsed, st.typing, "type a name, or p:prod d:events type:view col:user.id"))
	b.addR(m.searchInfo(st)+" ", sDim)
	hdr := []string{b.line(w), fit(" "+chips(st.parsed, w-1, scopeLabel(st)), w)}
	return append(hdr, m.resultsList(st, w, h-2, true)...)
}

func (m *Model) searchInfo(st *searchState) string {
	switch {
	case st.err != "":
		return "error: " + st.err
	case st.recent:
		return "recently opened"
	}
	return fmt.Sprintf("%s/%s · %.1fms", format.Count(int64(min(st.cur+1, st.total))), format.Count(int64(st.total)), st.tookMS)
}

// resultsList draws one line per result; the selected result expands to show
// its description and column hits when rich is set.
func (m *Model) resultsList(st *searchState, w, h int, rich bool) []string {
	n := len(st.results)
	if n == 0 {
		msg := "no matches"
		if st.recent {
			msg = "nothing opened yet: type to search"
		}
		out := []string{" " + sWarn.Render(msg), " " + sDim.Render("terms AND together; commas are OR; -d:tmp excludes; columns only with col:")}
		return fillTo(out, w, h)
	}
	st.cur = clamp(st.cur, 0, n-1)
	extra := 0
	if rich {
		extra = len(m.resultRows(st, st.cur, true, w, true)) - 1
	}
	s, e := window(st.cur, &st.off, n, max(1, h-extra))
	var out []string
	for i := s; i < e && len(out) < h; i++ {
		out = append(out, m.resultRows(st, i, i == st.cur, w, rich && i == st.cur)...)
	}
	return fillTo(out, w, h)
}

func (m *Model) resultRows(st *searchState, i int, selected bool, w int, expanded bool) []string {
	r := st.results[i]
	b := newRB(selected)
	mark := " "
	if selected {
		mark = "▌"
	}
	b.add(mark, sAcc)
	ns := sBase
	if r.Stale {
		ns = sDim
	}
	switch r.Kind {
	case api.KindProject:
		b.add("P ", fg("81")).spans(r.Name, r.Spans["name"], sBold)
	case api.KindDataset:
		b.add("D ", fg("114")).spans(r.Name, r.Spans["name"], ns)
		b.add("  ", sDim).spans(r.Path.Project, r.Spans["project"], sDim)
	default:
		b.add(typeLetter(r.Type)+" ", typeStyle(r.Type)).spans(r.Name, r.Spans["name"], ns)
		if r.Kind == api.KindShards && r.Shard != nil {
			b.add(fmt.Sprintf(" ×%s", format.Count(int64(r.Shard.Count))), sAcc)
		}
		b.add("  ", sDim).spans(r.Path.Project, r.Spans["project"], sDim).add(".", sDim).spans(r.Path.Dataset, r.Spans["dataset"], sDim)
	}
	if len(r.ColumnHits) > 0 && !expanded {
		b.addR("↳"+r.ColumnHits[0].FieldPath+" ", sAcc)
	}
	tag := r.Tier
	if r.MatchedField != "" && r.MatchedField != "name" && r.Tier != "" && r.Tier != "recent" {
		tag = r.MatchedField + "·" + r.Tier
	}
	b.addR(tag+" ", sFaint)
	if r.NamesOnly {
		b.addR("names only ", sWarn)
	}
	fresh := sOk
	if r.Stale {
		fresh = sWarn
	}
	if r.Kind != api.KindProject {
		b.addR(ago(m.o.Now(), r.SyncedAt)+" ", fresh)
	}
	rows := []string{b.line(w)}
	if !expanded {
		return rows
	}
	if r.Description != "" {
		d := newRB(false)
		d.add("    ", sFaint).spans(r.Description, r.Spans["description"], sDim)
		rows = append(rows, d.line(w))
	}
	for _, c := range r.ColumnHits {
		d := newRB(false)
		d.add("    ↳ ", sFaint).spans(c.FieldPath, c.Spans, sAcc).add("  "+c.DataType, sFaint)
		if c.Description != "" {
			d.add("  ", sFaint).spans(c.Description, c.DescSpans, sFaint)
		}
		rows = append(rows, d.line(w))
	}
	if r.MoreColumns > 0 {
		rows = append(rows, sFaint.Render(fit(fmt.Sprintf("    + %d more matching columns", r.MoreColumns), w)))
	}
	return rows
}

// --- the Schema pane

// schemaPane draws the Schema pane: the Explorer's selection, scrolled.
func (m *Model) schemaPane(s selection, w, h int) []string {
	lines := m.schemaLines(s, w)
	body := h - 1
	m.detOff = clamp(m.detOff, 0, max(0, len(lines)-body))
	name := s.path.String()
	if more := len(lines) - body - m.detOff; more > 0 {
		name += fmt.Sprintf(" ↓%d", more)
	}
	out := []string{title(" Schema · "+name, w, m.zone == ZoneSchema)}
	for i := m.detOff; i < len(lines) && len(out) < h; i++ {
		out = append(out, fit(lines[i], w))
	}
	return fillTo(out, w, h)
}

func (m *Model) schemaLines(s selection, w int) []string {
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	wrapAdd := func(text string, st lipgloss.Style) {
		for _, l := range wrap(text, w-3) {
			add(st.Render(l))
		}
	}
	n := s.node
	switch s.kind {
	case "":
		return []string{sDim.Render(" nothing selected")}
	case api.KindProject:
		add(sBold.Render(n.Name) + sDim.Render("  project"))
		add(fmt.Sprintf("%s datasets", format.Count(int64(n.Children))))
		t, st := m.health(n, true)
		add(st.Render(t))
		if n.Error != "" {
			wrapAdd(n.Error, sWarn)
		}
		out = append(out, "")
		if exec, _ := m.be.ExecutionProject(); exec != n.Name {
			wrapAdd("Queries and sync jobs run in the Execution project "+exec+", not here.", sDim)
		}
		return out
	case api.KindDataset:
		add(sBold.Render(n.Path.String()) + sDim.Render("  dataset"))
		add(fmt.Sprintf("%s · %s tables", n.Location, format.Count(int64(n.Children))))
		t, st := m.health(n, true)
		add(st.Render(t))
		switch n.Status {
		case api.DatasetNamesOnly:
			wrapAdd("names only: no metadata access, so table names without columns", sWarn)
		case api.DatasetInaccessible:
			wrapAdd("could not be read at the last sync; showing the previous data", sErr)
		}
		wrapAdd(n.Description, sDim)
		return out
	}

	d := m.details[s.detailKey()]
	if s.kind == api.KindShards && n.Shard != nil {
		add(sBold.Render(n.Path.String()))
		add(sAcc.Render(fmt.Sprintf("shard family ×%s", format.Count(int64(n.Shard.Count)))) + sDim.Render("  "+n.Shard.First+" → "+n.Shard.Last))
		add(sDim.Render("schema of the newest shard · enter lists the shards"))
	}
	if d == nil || (d.det == nil && d.detErr == "") {
		add(sDim.Render("loading…"))
		return out
	}
	if d.detErr != "" {
		wrapAdd(d.detErr, sErr)
		return out
	}
	det := d.det
	t := det.Table
	if s.kind != api.KindShards {
		add(sDim.Render(det.Path.Project+"."+det.Path.Dataset+".") + sBold.Render(t.Name))
	}
	meta := typeStyle(t.Type).Render(string(t.Type)) + sDim.Render(" · "+det.Location)
	switch {
	case d.stats != nil:
		meta += sDim.Render(fmt.Sprintf(" · %s rows · %s", format.Count(d.stats.Rows), format.Bytes(d.stats.Bytes)))
	case !d.statsErr && t.Type == api.TypeTable:
		meta += sFaint.Render(" · …")
	}
	add(meta)
	info := func(descFirst bool) {
		var part, clus []string
		for _, c := range t.Columns {
			if c.Partitioning {
				part = append(part, c.FieldPath)
			}
		}
		for ord := 1; ord <= 4; ord++ {
			for _, c := range t.Columns {
				if c.ClusteringOrdinal == ord {
					clus = append(clus, c.FieldPath)
				}
			}
		}
		if len(part)+len(clus) > 0 {
			l := ""
			if len(part) > 0 {
				l += "partition " + strings.Join(part, ", ")
			}
			if len(clus) > 0 {
				if l != "" {
					l += "  "
				}
				l += "cluster " + strings.Join(clus, ", ")
			}
			add(sDim.Render(l))
		}
		if t.RequirePartitionFilter {
			add(sWarn.Render("partition filter required"))
		}
		if len(t.Labels) > 0 {
			var ls []string
			for k, v := range t.Labels {
				ls = append(ls, k+":"+v)
			}
			add(sAcc.Render("labels " + strings.Join(sortStrings(ls), " ")))
		}
		if t.ExpiresAt != nil {
			add(sWarn.Render("expires " + t.ExpiresAt.Format("2006-01-02 15:04 UTC")))
		}
		fresh := api.BrowseNode{SyncedAt: det.SyncedAt, Stale: det.Stale, UnitStatus: det.Status}
		ft, fs := m.health(fresh, true)
		if descFirst {
			wrapAdd(t.Description, sDim)
			add(fs.Render(ft))
		} else {
			add(fs.Render(ft))
			wrapAdd(t.Description, sDim)
		}
	}
	viewSQL := func() {
		out = append(out, "")
		add(sFaint.Render("── view SQL ──"))
		for i, l := range strings.Split(strings.TrimSpace(t.ViewSQL), "\n") {
			if i >= 40 {
				add(sFaint.Render("…"))
				break
			}
			add(strings.ReplaceAll(l, "\t", "  "))
		}
	}
	if t.ViewSQL != "" {
		// a view lists its fields before its SQL, so they are in sight while
		// writing a query
		m.fieldLines(&out, d, t, det.NamesOnly, w)
		viewSQL()
		out = append(out, "")
		info(true)
		return out
	}
	info(false)
	m.fieldLines(&out, d, t, det.NamesOnly, w)
	return out
}

// fieldLines appends the field list of a table (or the notice that it has
// none) to out, after a blank line.
func (m *Model) fieldLines(out *[]string, d *detailData, t api.TableMeta, namesOnly bool, w int) {
	add := func(l string) { *out = append(*out, " "+l) }
	*out = append(*out, "")
	if len(t.Columns) == 0 {
		if namesOnly || t.NamesOnly {
			for _, l := range wrap("names only: columns are not available without metadata access.", w-3) {
				add(sWarn.Render(l))
			}
		} else {
			add(sDim.Render("no columns"))
		}
		return
	}
	add(sFaint.Render(fmt.Sprintf("── schema · %d fields ──", len(t.Columns))))
	type row struct {
		name, typ, desc string
		required        bool
	}
	required := map[string]bool{}
	if d.stats != nil {
		for _, p := range d.stats.Required {
			required[p] = true
		}
	}
	rows := make([]row, 0, len(t.Columns))
	nameW := 8
	for _, c := range orderColumns(t.Columns) {
		depth := strings.Count(c.FieldPath, ".")
		leaf := c.FieldPath[strings.LastIndex(c.FieldPath, ".")+1:]
		nm := strings.Repeat("  ", depth) + leaf
		nameW = max(nameW, wid(nm))
		rows = append(rows, row{nm, c.DataType, c.Description, required[c.FieldPath] || c.Required})
	}
	nameW = min(nameW, max(10, (w-2)*45/100))
	typeW := 14
	for _, r := range rows {
		b := newRB(false)
		typ := r.typ
		if i := strings.IndexAny(typ, "<("); i > 0 && wid(typ) > typeW {
			typ = typ[:i] + "<…>"
		}
		b.add(" ", sBase).add(fit(r.name, nameW), sBase).add(" ", sBase).add(fit(typ, typeW), dataTypeStyle(r.typ))
		if r.required {
			b.add(" required", sWarn)
		}
		if r.desc != "" {
			b.add(" "+r.desc, sFaint)
		}
		*out = append(*out, b.line(w))
	}
}

// orderColumns arranges field paths depth-first so nested fields follow their parent.
func orderColumns(cols []api.ColumnMeta) []api.ColumnMeta {
	children := map[string][]api.ColumnMeta{}
	for _, c := range cols {
		children[c.ParentPath()] = append(children[c.ParentPath()], c)
	}
	var out []api.ColumnMeta
	var walk func(parent string)
	walk = func(parent string) {
		kids := children[parent]
		for i := 1; i < len(kids); i++ {
			for j := i; j > 0 && kids[j-1].Ordinal > kids[j].Ordinal; j-- {
				kids[j-1], kids[j] = kids[j], kids[j-1]
			}
		}
		for _, k := range kids {
			out = append(out, k)
			walk(k.FieldPath)
		}
	}
	walk("")
	if len(out) != len(cols) { // orphans: fall back to the stored order
		return cols
	}
	return out
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
	return s
}

func cellText(c api.Cell) string {
	if c.Null {
		return "NULL"
	}
	if c.Truncated {
		return c.Text + "…"
	}
	return c.Text
}

// --- popups

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

func (m *Model) jumpBox(bw, bh int) []string {
	inner := bw - 2
	st := m.jump
	b := newRB(false)
	b.add(" ⌕ ", sAcc)
	b.l.WriteString(st.in.render(st.parsed, true, "table name, p:prod, d:events …"))
	b.addR(m.searchInfo(st)+" ", sDim)
	lines := []string{b.line(inner), " " + chips(st.parsed, inner-1, "")}
	lines = append(lines, m.resultsList(st, inner, bh-4, false)...)
	return popup("Jump to table", lines, bw)
}

func (m *Model) infoBox(bw, maxH int) []string {
	var ttl string
	var head, lines []string
	body := max(1, maxH-2)
	cur := -1 // the line the window follows; -1 scrolls freely
	switch m.ov {
	case ovHelp:
		ttl, lines = "Help", helpLines(bw-2)
	case ovProblems:
		ttl, lines = "Sync problems", m.problemLines(bw-4)
	case ovSync:
		ttl, lines = "Sync progress", m.syncLines()
	case ovRecord:
		ttl, lines = m.recordLines(bw - 2)
	case ovRuns:
		ttl, lines, cur = "Runs", m.runsLines(bw-2), m.ovSel
	case ovBuffers:
		ttl, lines, cur = "Buffers", m.buffersLines(bw-2), m.ovSel
		if cur >= len(m.bufs) {
			cur += 2 // the "Restore drafts" heading
		}
	case ovProjects:
		ttl = "Projects"
		head, lines = m.projectsLines(bw-2, body)
		if m.pd != nil {
			cur = m.pd.sel
		}
	case ovHandoff:
		ttl, lines, cur = "Hand-off to an agent", m.handoffLines(bw-2), m.ovSel+m.hoTop
	}
	return m.scrollBox(ttl, head, lines, cur, bw, body)
}

// scrollBox is a popup of body lines: head stays put and rows scroll below
// it, following row cur (cur < 0: scrolled by the keys alone).
func (m *Model) scrollBox(ttl string, head, rows []string, cur, bw, body int) []string {
	h := max(1, body-len(head))
	if cur >= 0 {
		window(cur, &m.ovOff, len(rows), h)
	}
	m.ovOff = clamp(m.ovOff, 0, max(0, len(rows)-h))
	end := min(len(rows), m.ovOff+h)
	if len(rows) > h {
		ttl += fmt.Sprintf("  (%d-%d/%d)", m.ovOff+1, end, len(rows))
	}
	return popup(ttl, append(slices.Clip(head), rows[m.ovOff:end]...), bw)
}

func (m *Model) syncLines() []string {
	var out []string
	switch {
	case m.syncing:
		out = append(out, " "+sWarn.Render(fmt.Sprintf("running: %d of %d units done", m.syncDone, m.syncTotal))+sDim.Render("  ctrl+c cancels (finished units are kept)"))
	case len(m.syncLog) == 0:
		out = append(out, " "+sDim.Render("no sync has run in this session. r refreshes the selected project, R everything."))
	default:
		out = append(out, " "+sDim.Render("last sync in this session:"))
	}
	out = append(out, "")
	for _, l := range m.syncLog {
		out = append(out, " "+l)
	}
	return out
}

func (m *Model) problemLines(w int) []string {
	var out []string
	if m.status != nil {
		for _, p := range m.status.Projects {
			if !p.Selected {
				continue
			}
			for _, u := range p.Units {
				if u.Status == api.UnitOK && u.NamesOnly == 0 && u.Inaccessible == 0 {
					continue
				}
				loc := u.Location
				if loc == "" {
					loc = "listing datasets"
				}
				st := sWarn
				if u.Status == api.UnitInaccessible {
					st = sErr
				}
				out = append(out, " "+st.Render("⚠ ")+sBold.Render(p.Project)+sDim.Render(" / "+loc+"  "+string(u.Status)+" · last success "+ago(m.o.Now(), u.LastSuccess)))
				for _, l := range wrap(u.Error, w-4) {
					out = append(out, "    "+st.Render(l))
				}
				out = append(out, "")
			}
		}
	}
	if len(out) == 0 {
		out = append(out, " "+sOk.Render("no sync problems"))
	}
	return append(out, " "+sFaint.Render("Failed units keep their previous data. r retries the selected project."))
}
