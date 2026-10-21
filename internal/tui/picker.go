package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// The picker is one overlay over Favorites and History (spec 11): tab
// switches the scope, the search box is shared, a preview shows the SQL and
// its metadata. Opening a pick loads it into the editor.

const (
	scopeFavorites = iota
	scopeHistory
)

const historyPage = 100

type picker struct {
	scope int
	in    input

	favs   []api.Favorite
	favErr string
	fm     []api.FavoriteMatch

	hist     []api.HistoryGroup
	hq       api.ParsedQuery
	hmore    bool
	hseq     int
	hloading bool
	herr     string

	sel, off [2]int
	poff     int // preview scroll
}

type histMsg struct {
	seq    int
	page   api.HistoryPage
	err    error
	append bool
}

type reopenMsg struct {
	id  int64
	err error
	buf string
}

// loadReq is a pick waiting to be loaded into a buffer.
type loadReq struct {
	text   string
	path   string // a Favorite's file; "" loads a scratch buffer
	params map[string]string
	what   string // for the toast
}

func (m *Model) openPicker(scope int) tea.Cmd {
	switch {
	case m.pk == nil:
		m.pk = &picker{}
	case m.pk.scope != scope:
		// the search text is kept for reopening the same list only
		m.pk.in.set("")
		m.pk.sel, m.pk.off = [2]int{}, [2]int{}
	}
	m.pk.scope = scope
	m.ov, m.pendingKey, m.comp.open = ovPicker, "", false
	m.reloadFavorites()
	return m.searchHistory(false)
}

func (m *Model) reloadFavorites() {
	pk := m.pk
	favs, err := m.be.ListFavorites()
	pk.favs, pk.favErr = favs, ""
	if err != nil {
		pk.favErr = err.Error()
	}
	pk.fm = m.be.MatchFavorites(pk.favs, pk.in.value())
	pk.sel[scopeFavorites] = clamp(pk.sel[scopeFavorites], 0, max(0, len(pk.fm)-1))
}

func (m *Model) searchHistory(appendPage bool) tea.Cmd {
	pk := m.pk
	pk.hseq++
	pk.hloading = true
	pk.hq = m.be.ParseHistoryQuery(pk.in.value())
	seq, q, offset := pk.hseq, pk.in.value(), 0
	if appendPage {
		offset = len(pk.hist)
	}
	return func() tea.Msg {
		page, err := m.be.SearchHistory(m.ctx, q, historyPage, offset)
		return histMsg{seq, page, err, appendPage}
	}
}

func (m *Model) onHistory(msg histMsg) {
	pk := m.pk
	if pk == nil || msg.seq != pk.hseq {
		return // a newer search replaced this one
	}
	pk.hloading = false
	if msg.err != nil {
		pk.herr = msg.err.Error()
		return
	}
	pk.herr = ""
	if msg.append {
		// History may have changed between pages: skip groups already shown
		seen := map[string]bool{}
		for _, g := range pk.hist {
			seen[g.Key] = true
		}
		for _, g := range msg.page.Groups {
			if !seen[g.Key] {
				pk.hist = append(pk.hist, g)
			}
		}
	} else {
		pk.hist = msg.page.Groups
		pk.sel[scopeHistory], pk.off[scopeHistory] = 0, 0
	}
	pk.hmore = msg.page.More
}

func (pk *picker) count() int {
	if pk.scope == scopeFavorites {
		return len(pk.fm)
	}
	return len(pk.hist)
}

func (m *Model) pickedFavorite() (api.Favorite, bool) {
	pk := m.pk
	if pk == nil || pk.scope != scopeFavorites || pk.sel[0] >= len(pk.fm) {
		return api.Favorite{}, false
	}
	return pk.fm[pk.sel[0]].Favorite, true
}

func (m *Model) pickedHistory() (api.HistoryGroup, bool) {
	pk := m.pk
	if pk == nil || pk.scope != scopeHistory || pk.sel[1] >= len(pk.hist) {
		return api.HistoryGroup{}, false
	}
	return pk.hist[pk.sel[1]], true
}

func (m *Model) pickerRows() int { return max(3, m.h-2-2-5) }

func (m *Model) pickerKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	pk := m.pk
	act := keys[ZonePicker][name]
	if act == "" && strings.HasPrefix(name, "ctrl+") {
		// the global ctrl keys (quit, help, cancel) still work; letters type
		if cmd, ok := m.globalAction(keys[ZoneGlobal][name]); ok {
			return cmd
		}
	}
	s := pk.scope
	move := func(d int) tea.Cmd {
		n := pk.count()
		pk.sel[s] = clamp(pk.sel[s]+d, 0, max(0, n-1))
		pk.poff = 0
		if s == scopeHistory && pk.hmore && !pk.hloading && pk.sel[s] >= n-10 {
			return m.searchHistory(true)
		}
		return nil
	}
	switch act {
	case "back":
		m.ov = ovNone
		return nil
	case "down":
		return move(1)
	case "up":
		return move(-1)
	case "pagedown":
		return move(m.pickerRows())
	case "pageup":
		return move(-m.pickerRows())
	case "scope":
		pk.scope = 1 - pk.scope
		pk.poff = 0
		return nil
	case "detaildown":
		pk.poff++
		return nil
	case "detailup":
		pk.poff = max(0, pk.poff-1)
		return nil
	case "open":
		return m.pickOpen()
	case "pickr":
		if s == scopeFavorites {
			if f, ok := m.pickedFavorite(); ok {
				fp, _ := m.be.FavoritePath(f.ID)
				m.openPrompt(promptState{kind: "favrename", title: "Rename favorite", labels: []string{"new name"}, back: ovPicker, favID: f.ID, favPath: fp,
					note: "a path under favorites_dir; subfolders are created · enter renames · esc cancels"}, strings.TrimSuffix(f.ID, ".sql"))
			}
			return nil
		}
		g, ok := m.pickedHistory()
		if !ok {
			return nil
		}
		if g.Latest.State != api.RunDone {
			m.toast = "this run " + string(g.Latest.State) + ": it has no result to show"
			return nil
		}
		m.ov = ovNone
		m.toast = "reading the result of the run from BigQuery…"
		entry, b := g.Latest.ID, m.buf().id
		return func() tea.Msg {
			id, err := m.be.ReopenResult(m.ctx, entry, b)
			return reopenMsg{id: id, err: err, buf: b}
		}
	case "delete":
		if f, ok := m.pickedFavorite(); ok {
			m.ov = ovNone
			fp, _ := m.be.FavoritePath(f.ID)
			m.askConfirm(confirmState{kind: "favdelete", favID: f.ID, favPath: fp, back: ovPicker,
				lines: []string{"Delete the favorite " + f.ID + "?", "The file is removed; there is no undo (keep favorites_dir in git as a safety net)."}})
		}
		return nil
	case "promote":
		if g, ok := m.pickedHistory(); ok {
			m.openPrompt(promptState{kind: "promote", title: "Save as favorite", labels: []string{"name", "description", "tags"}, back: ovPicker,
				entryID: g.Latest.ID, note: "the name is a path under favorites_dir (subfolders are created); tags are comma-separated · enter saves · esc cancels"})
		}
		return nil
	}
	if pk.in.handle(msg) {
		pk.poff = 0
		pk.fm = m.be.MatchFavorites(pk.favs, pk.in.value())
		pk.sel[scopeFavorites], pk.off[scopeFavorites] = 0, 0
		return m.searchHistory(false)
	}
	return nil
}

// pickOpen loads the selected Favorite or History entry into the editor.
func (m *Model) pickOpen() tea.Cmd {
	if f, ok := m.pickedFavorite(); ok {
		p, err := m.be.FavoritePath(f.ID)
		if err != nil {
			m.toast = err.Error()
			return nil
		}
		return m.loadPick(loadReq{text: f.Text, path: p, what: "favorite " + f.ID})
	}
	if g, ok := m.pickedHistory(); ok {
		params := map[string]string{}
		for _, p := range g.Latest.Params {
			v := p.Value
			if p.Null {
				v = "null"
			}
			if p.Type != "" {
				v = p.Type + ":" + v
			}
			params[p.Name] = v
		}
		return m.loadPick(loadReq{text: g.Latest.SQL, params: params, what: "the query from History"})
	}
	return nil
}

// loadPick puts a pick into the current buffer. A Favorite already open is
// switched to; a buffer with unsaved text asks first (y replaces it, n opens
// the pick in a new buffer).
func (m *Model) loadPick(req loadReq) tea.Cmd {
	m.ov = ovNone
	if req.path != "" {
		for i, b := range m.bufs {
			if b.path == req.path {
				m.cur = i
				m.setZone(ZoneEditor)
				m.toast = req.what + " is already open"
				return m.gridEnsure()
			}
		}
	}
	b := m.buf()
	dirty := b.modified() || (b.path == "" && !b.ed.empty())
	if dirty {
		m.pendingLoad = &req
		m.askConfirm(confirmState{kind: "load", lines: []string{
			"Replace the text of " + b.title() + " with " + req.what + "?",
			"Its text is not saved to a file. n opens " + req.what + " in a new buffer instead."}})
		return nil
	}
	return m.finishLoad(req, true)
}

// finishLoad loads the pick, replacing the current buffer or into a new one.
func (m *Model) finishLoad(req loadReq, replace bool) tea.Cmd {
	idx := m.cur
	var nb *buffer
	if req.path != "" {
		if err := m.openFile(req.path); err != nil {
			m.toast = "open " + req.path + ": " + err.Error()
			return nil
		}
		nb = m.buf()
	} else {
		nb = m.addBuffer(req.text, "", "")
		nb.draftVersion = -1 // write the draft on the next tick
	}
	for k, v := range req.params {
		nb.params[k] = v
	}
	if replace {
		// drop the old buffer and put the new one in its place
		m.cur = idx
		m.closeBuffer()
		last := len(m.bufs) - 1
		if i := min(idx, last); i != last {
			m.bufs = append(m.bufs[:i], append([]*buffer{m.bufs[last]}, m.bufs[i:last]...)...)
		}
		m.cur = min(idx, len(m.bufs)-1)
	}
	m.setZone(ZoneEditor)
	m.toast = "opened " + req.what
	return nil
}

func (m *Model) onReopen(msg reopenMsg) tea.Cmd {
	if msg.err != nil {
		m.toast = "show the result: " + msg.err.Error()
		return nil
	}
	b, _ := m.bufferOf(msg.buf)
	if b == nil {
		b = m.buf()
	}
	b.viewID = msg.id
	m.setZone(ZoneResults)
	m.toast = ""
	return m.followRun(msg.id)
}

// --- prompts and questions started from the picker

func (m *Model) promptFavorite(p *promptState) tea.Cmd {
	switch p.kind {
	case "favrename":
		oldPath, err := m.be.FavoritePath(p.favID)
		if err != nil || oldPath != p.favPath {
			p.note = sErr.Render("favorites_dir changed since this prompt opened: close it and pick the favorite again")
			return nil
		}
		id, err := m.be.RenameFavorite(p.favID, p.fields[0].value())
		if err != nil {
			p.note = sErr.Render(err.Error())
			return nil
		}
		if np, err := m.be.FavoritePath(id); err == nil {
			for _, b := range m.bufs {
				if b.path == oldPath {
					b.path = np
				}
			}
		}
		m.reloadFavorites()
		m.selectFavorite(id)
		m.ov = ovPicker
		m.toast = "renamed to " + id
	case "promote":
		tags := splitTags(p.fields[2].value())
		id, err := m.be.PromoteHistory(m.ctx, p.entryID, p.fields[0].value(), p.fields[1].value(), tags, p.armed)
		if engine.IsFavoriteExists(err) && !p.armed {
			p.armed = true
			p.note = sWarn.Render("a favorite with that name exists: press enter again to overwrite it")
			return nil
		}
		if err != nil && id == "" {
			p.note = sErr.Render(err.Error())
			return nil
		}
		m.reloadFavorites()
		m.ov = ovPicker
		m.toast = "saved as favorite " + id
		if err != nil {
			m.toast = err.Error()
		}
	case "savefav":
		return m.saveBufferAsFavorite(p)
	}
	return nil
}

func (m *Model) selectFavorite(id string) {
	pk := m.pk
	for i, f := range pk.fm {
		if f.Favorite.ID == id {
			pk.sel[scopeFavorites] = i
		}
	}
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// openSaveFavorite asks for the name, description and tags of the buffer as
// a Favorite, prefilled from its header (and its name, if it is one).
func (m *Model) openSaveFavorite() {
	b := m.buf()
	f := m.be.ParseFavorite(b.ed.text())
	name := strings.TrimSuffix(m.be.FavoriteFromID(b.path), ".sql")
	m.openPrompt(promptState{kind: "savefav", title: "Save buffer as favorite", labels: []string{"name", "description", "tags"},
		favID: m.be.FavoriteFromID(b.path),
		note:  "the name is a path under favorites_dir (subfolders are created); tags are comma-separated; -- param: lines in the buffer are kept · enter saves · esc cancels"},
		name, f.Description, strings.Join(f.Tags, ", "))
}

// saveBufferAsFavorite writes the buffer with a fresh header; the buffer then
// is that Favorite's file (ctrl+s saves back to it).
func (m *Model) saveBufferAsFavorite(p *promptState) tea.Cmd {
	b := m.buf()
	text := b.ed.text()
	f := m.be.ParseFavorite(text)
	f.Description, f.Tags = strings.TrimSpace(p.fields[1].value()), splitTags(p.fields[2].value())
	out := m.be.RenderFavorite(f)
	same := p.favID != "" && strings.TrimSuffix(p.favID, ".sql") == strings.TrimSuffix(strings.TrimSpace(p.fields[0].value()), ".sql")
	id, err := m.be.SaveFavorite(p.fields[0].value(), out, same || p.armed)
	if engine.IsFavoriteExists(err) && !p.armed {
		p.armed = true
		p.note = sWarn.Render("a favorite with that name exists: press enter again to overwrite it")
		return nil
	}
	if err != nil {
		p.note = sErr.Render(err.Error())
		return nil
	}
	path, err := m.be.FavoritePath(id)
	if err != nil {
		p.note = sErr.Render(err.Error())
		return nil
	}
	// the buffer takes the new header (one undo step), keeping the cursor
	oldHead := len(text) - len(f.SQL)
	newHead := len(out) - len(f.SQL)
	if !strings.HasSuffix(f.SQL, "\n") && f.SQL != "" {
		newHead-- // Render added the final newline
	}
	if text[:oldHead] != out[:newHead] {
		row, col := b.ed.row, b.ed.col
		delta := strings.Count(out[:newHead], "\n") - strings.Count(text[:oldHead], "\n")
		b.ed.replaceBytes(0, oldHead, out[:newHead])
		b.ed.row = clamp(row+delta, 0, len(b.ed.lines)-1)
		b.ed.col = clamp(col, 0, len(b.ed.lines[b.ed.row]))
		b.ed.want = b.ed.col
	}
	b.path = path
	b.savedVersion = b.ed.version
	if b.lock != nil {
		b.lock.Unlock()
		b.lock = nil
	}
	m.removeDraft(b)
	m.ov = ovNone
	m.toast = "saved favorite " + id + " (ctrl+s saves it again)"
	if m.pk != nil {
		m.reloadFavorites()
	}
	return nil
}

// --- rendering

func (m *Model) pickerBox(w, h int) []string {
	pk := m.pk
	inner := w - 2
	tab := func(label string, n int, on bool) string {
		t := fmt.Sprintf(" %s %d ", label, n)
		if on {
			return lipgloss.NewStyle().Reverse(true).Bold(true).Render(t)
		}
		return sDim.Render(t)
	}
	hn := fmt.Sprint(len(pk.hist))
	if pk.hmore {
		hn += "+"
	}
	head := " " + tab("Favorites", len(pk.fm), pk.scope == scopeFavorites) + " " + strings.Replace(tab("History", 0, pk.scope == scopeHistory), " 0 ", " "+hn+" ", 1) +
		sFaint.Render("  tab switches")
	q := pk.hq
	placeholder := "search history: words of the SQL, status: project: source: since:7d"
	if pk.scope == scopeFavorites {
		q = favQuery(pk.in.value())
		placeholder = "search favorites: name, tags, description, SQL"
	}
	lines := []string{head, " " + sAcc.Render("▸ ") + pk.in.render(q, true, placeholder)}
	if pk.scope == scopeHistory {
		lines = append(lines, " "+chipsWith(q, inner-2, "", historyHint))
	} else {
		lines = append(lines, "")
	}
	lines = append(lines, sFaint.Render(strings.Repeat("─", inner)))
	bodyH := max(3, h-2-len(lines))
	listW := inner
	side := inner >= 90
	if side {
		listW = inner * 45 / 100
	}
	listH := bodyH
	if !side {
		listH = max(3, bodyH/2)
	}
	list := m.pickerList(listW, listH)
	prevW := inner
	if side {
		prevW = inner - listW - 1
	}
	prev := m.pickerPreview(prevW, bodyH-listH*b2i(!side))
	if side {
		sep := vsep(bodyH)
		prev = fillTo(prev, prevW, bodyH)
		for i := 0; i < bodyH; i++ {
			lines = append(lines, fit(list[i], listW)+sep[i]+fit(prev[i], prevW))
		}
	} else {
		lines = append(lines, list...)
		lines = append(lines, fillTo(prev, prevW, bodyH-listH)...)
	}
	return popup("Favorites and History", lines, w)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// favQuery marks every word of a Favorites search as a term, for colour.
func favQuery(raw string) api.ParsedQuery {
	q := api.ParsedQuery{Raw: raw}
	i := 0
	for _, f := range strings.Fields(raw) {
		j := strings.Index(raw[i:], f) + i
		q.Terms = append(q.Terms, api.QueryTerm{Text: f, Span: api.Span{Start: j, End: j + len(f)}})
		i = j + len(f)
	}
	return q
}

func (m *Model) pickerList(w, h int) []string {
	pk := m.pk
	s := pk.scope
	n := pk.count()
	var out []string
	switch {
	case s == scopeFavorites && pk.favErr != "":
		return fillTo([]string{" " + sErr.Render(pk.favErr)}, w, h)
	case s == scopeHistory && pk.herr != "":
		return fillTo([]string{" " + sErr.Render(pk.herr)}, w, h)
	case n == 0 && s == scopeFavorites && len(pk.favs) == 0:
		return fillTo([]string{" " + sDim.Render("no favorites yet: ctrl+x s in the editor saves the buffer"), " " + sDim.Render("as one, ctrl+s here saves a History entry")}, w, h)
	case n == 0 && s == scopeHistory && pk.hloading:
		return fillTo([]string{" " + sDim.Render("searching…")}, w, h)
	case n == 0:
		return fillTo([]string{" " + sDim.Render("nothing matches")}, w, h)
	}
	from, to := window(pk.sel[s], &pk.off[s], n, h)
	now := m.o.Now()
	for i := from; i < to; i++ {
		b := newRB(i == pk.sel[s])
		if s == scopeFavorites {
			fm := pk.fm[i]
			b.add(" ", sBase).spans(fm.Favorite.ID, fm.NameSpans, sBase)
			if len(fm.Favorite.Tags) > 0 {
				b.add("  "+strings.Join(fm.Favorite.Tags, " "), sFaint)
			}
			if len(fm.Favorite.Problems) > 0 {
				b.add(" ⚠", sWarn)
			}
		} else {
			g := pk.hist[i]
			glyph, st := stateGlyph(g.Latest.State)
			b.add(" "+glyph+" ", st)
			b.add(pad(shortAgo(now, g.Latest.Created), 4)+" ", sFaint)
			if g.Runs > 1 {
				b.addR(fmt.Sprintf(" ×%d ", g.Runs), sDim)
			}
			b.add(oneLine(g.Latest.SQL), sBase)
		}
		out = append(out, b.line(w))
	}
	return fillTo(out, w, h)
}

func stateGlyph(s api.RunState) (string, lipgloss.Style) {
	switch s {
	case api.RunDone:
		return "✓", sOk
	case api.RunFailed:
		return "✗", sErr
	case api.RunCancelled:
		return "⊘", sWarn
	}
	return "·", sDim
}

func shortAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("Jan06")
}

func oneLine(sql string) string { return strings.Join(strings.Fields(sql), " ") }

func (m *Model) pickerPreview(w, h int) []string {
	pk := m.pk
	var meta []string
	var sql string
	var spans []api.Span
	kv := func(k, v string, st lipgloss.Style) {
		if v != "" {
			meta = append(meta, " "+sDim.Render(pad(k, 11))+st.Render(v))
		}
	}
	if f, ok := m.pickedFavorite(); ok {
		kv("favorite", f.ID, sBold)
		kv("about", f.Description, sBase)
		kv("tags", strings.Join(f.Tags, ", "), sAcc)
		for _, p := range f.Params {
			d := ""
			if p.Default != nil {
				d = " = " + *p.Default
			}
			kv("param", "@"+p.Name+" "+p.Type+d, sBase)
		}
		for _, pr := range f.Problems {
			kv("problem", pr, sErr)
		}
		if a := shortAgo(m.o.Now(), f.ModTime); a == "now" {
			kv("changed", "just now", sFaint)
		} else {
			kv("changed", a+" ago", sFaint)
		}
		sql = f.SQL
	} else if g, ok := m.pickedHistory(); ok {
		e := g.Latest
		_, st := stateGlyph(e.State)
		runs := ""
		if g.Runs > 1 {
			runs = fmt.Sprintf("   (latest of %d runs)", g.Runs)
		}
		kv("state", string(e.State)+runs, st)
		kv("error", e.Error, sErr)
		kv("ran", e.Created.In(m.o.Location).Format("2006-01-02 15:04")+durationText(e.DurationMs), sBase)
		if e.BytesProcessed != nil {
			v := format.Bytes(*e.BytesProcessed) + " processed"
			if e.CacheHit {
				v += " (cache hit)"
			}
			kv("scanned", v, sBase)
		}
		if e.Capacity() {
			if e.SlotMs != nil {
				kv("slot time", fmt.Sprintf("%.1f slot-s on %s", float64(*e.SlotMs)/1000, e.ReservationID), sBase)
			}
			if g.Runs > 1 {
				kv("all runs", fmt.Sprintf("%.1f slot-s", float64(g.SlotMs)/1000), sFaint)
			}
		} else if e.BytesBilled != nil {
			kv("billed", format.Bytes(*e.BytesBilled), sBase)
			if g.Runs > 1 {
				kv("all runs", format.Bytes(g.BytesBilled)+" billed", sFaint)
			}
		}
		kv("from", e.Source+" · "+e.ExecutionProject+" · "+e.Job.Location, sFaint)
		kv("favorite", e.FavoritePath, sAcc)
		for _, p := range e.Params {
			v := p.Value
			if p.Null {
				v = "NULL"
			}
			kv("param", "@"+p.Name+" "+p.Type+" = "+v, sBase)
		}
		sql, spans = e.SQL, g.Spans
	} else {
		return nil
	}
	lines := append(meta, sFaint.Render(" "+strings.Repeat("─", max(0, w-2))))
	body := sqlLines(sql, m.be.Tokenize(sql), spans, w-1)
	pk.poff = clamp(pk.poff, 0, max(0, len(body)-1))
	for _, l := range body[pk.poff:] {
		lines = append(lines, " "+l)
	}
	return lines
}

func durationText(ms *int64) string {
	if ms == nil {
		return ""
	}
	return " · " + (time.Duration(*ms) * time.Millisecond).Round(100*time.Millisecond).String()
}

// sqlLines highlights SQL with the tokenizer's kinds and marks the matched
// spans (byte offsets) reversed. Lines are cut at w cells.
func sqlLines(sql string, toks []api.Token, marks []api.Span, w int) []string {
	kind := make([]api.TokenKind, len(sql))
	for _, t := range toks {
		for i := max(0, t.Start); i < t.End && i < len(sql); i++ {
			kind[i] = t.Kind
		}
	}
	mark := make([]bool, len(sql))
	for _, s := range marks {
		for i := max(0, s.Start); i < s.End && i < len(sql); i++ {
			mark[i] = true
		}
	}
	var out []string
	var sb strings.Builder
	cells := 0
	off := 0
	for _, line := range strings.Split(sql, "\n") {
		sb.Reset()
		cells = 0
		for i, r := range line {
			at := off + i
			st, ok := tokenStyles[kind[at]]
			if !ok {
				st = sBase
			}
			if mark[at] {
				st = st.Reverse(true)
			}
			if r == '\t' {
				r = ' '
			}
			if cells >= w {
				break
			}
			sb.WriteString(st.Render(string(r)))
			cells++
		}
		out = append(out, sb.String())
		off += len(line) + 1
	}
	return out
}
