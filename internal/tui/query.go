package tui

import (
	"errors"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/export"
	"github.com/davidsevcik/bigquery-tui/internal/favorites"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// completion is the state of the completion popup.
type completion struct {
	open  bool
	cands []api.Candidate
	sel   int
	off   int
}

// promptState is a small form: parameter values, an export path, a file
// name or the find text.
type promptState struct {
	kind   string // params | export | saveas | find
	title  string
	labels []string
	fields []input
	idx    int
	note   string

	// params
	specs  []api.ParamSpec
	query  api.Favorite // the query that runs, typed by the buffer's header
	sql    string
	dry    bool
	line0  int
	col0   int
	format export.Format
	armed  bool // export: the row-count warning was shown; favorites: overwrite confirmed

	back    overlayKind // the overlay to return to (the picker)
	favID   string      // savefav: the Favorite the buffer already is
	entryID int64       // promote
	hoItem  handoffItem // handoff: what to write
	// favrename: the Favorite as shown (its file)
	fav api.Favorite
}

// confirmState is a yes/no question.
type confirmState struct {
	kind    string // run | quit | closebuf | load | favdelete
	runID   int64
	lines   []string
	back    overlayKind
	subject api.Favorite // favdelete: the Favorite as shown (favorites_dir may change meanwhile)
}

type (
	runMsg    struct{ u runs.Update }
	exportMsg struct {
		ev api.ExportEvent
		ok bool
	}
	pricingMsg struct{}
	tickMsg    struct{}
	clipMsg    struct {
		text  string
		toast string
		err   error
	}
	extEditMsg struct {
		buf  string
		path string
		err  error
	}
)

var inferParamFn = sqlx.InferParam

// exportAskRows is the row count above which an export asks first.
const exportAskRows = 1_000_000

// --- zones

func (m *Model) setZone(z Zone) {
	m.zone = z
	m.zoom = false
	m.comp.open = false
	m.pendingKey = ""
}

func (m *Model) zoneStep(d int) {
	order := []Zone{ZoneExplorer, ZoneEditor, ZoneResults}
	for i, z := range order {
		if z == m.zone {
			m.setZone(order[(i+d+len(order))%len(order)])
			return
		}
	}
	m.setZone(ZoneExplorer)
}

// --- editor zone

func (m *Model) editorKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	b := m.buf()
	if m.pendingKey != "" {
		chord := m.pendingKey + " " + name
		m.pendingKey = ""
		act := keys.action(ZoneEditor, chord)
		if act == "" {
			m.toast = chord + " is not bound"
			return nil
		}
		return m.editorAction(act)
	}
	if keys.chordPrefix(ZoneEditor, name) {
		m.pendingKey = name
		return nil
	}
	vimOn := m.vimOn()
	v := &b.vim
	if vimOn && v.mode != vimInsert {
		// Normal and Visual mode: the registry keeps its ctrl and function
		// keys, except the few vim claims (ctrl+r is redo, ctrl+d scrolls).
		if !vimClaims(name) {
			if act := keys.action(ZoneEditor, name); act != "" {
				return m.editorAction(act)
			}
		}
		if v.idle() && (name == "u" || name == "ctrl+r" || name == ".") {
			defer func() { v.tainted = true }()
		}
		return m.vimKey(msg, name)
	}
	if vimOn {
		if !v.replay {
			v.rec = append(v.rec, msg)
		}
		switch {
		case name == "esc":
			m.vimLeaveInsert(b)
			m.vimSettle(b)
			if m.o.NoBackground {
				m.saveDraft(b)
			}
			return nil
		case name == "tab" && m.comp.open:
			v.tainted = true // a completion cannot be replayed by "."
		case name == "ctrl+n" && !m.comp.open:
			m.updateCompletion(true)
			return nil
		}
	}
	if m.comp.open {
		switch name {
		case "tab":
			return m.acceptCompletion()
		case "down", "ctrl+n":
			m.comp.sel = min(m.comp.sel+1, len(m.comp.cands)-1)
			return nil
		case "up", "ctrl+p":
			m.comp.sel = max(0, m.comp.sel-1)
			return nil
		case "esc":
			m.comp.open = false
			return nil
		}
	}
	if act := keys.action(ZoneEditor, name); act != "" {
		if act != "complete" {
			m.comp.open = false
		}
		return m.editorAction(act)
	}
	changed, handled := b.ed.handle(msg, max(1, m.editorRows()-1))
	if !handled {
		return nil
	}
	if !changed {
		m.comp.open = false
		return nil
	}
	return m.afterEdit(true)
}

// afterEdit runs after every text change: completion, and the draft.
func (m *Model) afterEdit(typed bool) tea.Cmd {
	if typed {
		m.updateCompletion(false)
	} else {
		m.comp.open = false
	}
	if m.o.NoBackground {
		m.saveDraft(m.buf())
	}
	return nil
}

func (m *Model) updateCompletion(forced bool) {
	ed := m.buf().ed
	c := m.be.Complete(ed.text(), ed.offset(), forced)
	m.comp = completion{open: len(c) > 0, cands: c}
	if forced && len(c) == 0 {
		m.toast = "nothing to complete here"
	}
}

func (m *Model) acceptCompletion() tea.Cmd {
	if m.comp.sel >= len(m.comp.cands) {
		m.comp.open = false
		return nil
	}
	c := m.comp.cands[m.comp.sel]
	ed := m.buf().ed
	text := ed.text()
	ins := c.Insert
	after := ""
	if c.End < len(text) {
		after = text[c.End:]
	}
	again := false
	switch c.Kind {
	case api.CandProject, api.CandDataset:
		// continue with the next path segment
		if !strings.HasPrefix(after, ".") {
			ins += "."
		}
		again = true
	case api.CandTable:
		// close a backtick path the user opened
		if strings.Count(text[:c.Start], "`")%2 == 1 && !strings.HasPrefix(after, "`") && !strings.HasPrefix(ins, "`") {
			ins += "`"
		}
	}
	ed.replaceBytes(c.Start, c.End, ins)
	if again && strings.HasPrefix(after, ".") {
		ed.col++ // step over the dot that was already there
	}
	m.comp.open = false
	if again {
		m.updateCompletion(false)
	}
	if m.o.NoBackground {
		m.saveDraft(m.buf())
	}
	return nil
}

func (m *Model) editorAction(act string) tea.Cmd {
	if cmd, ok := m.globalAction(act); ok {
		return cmd
	}
	b := m.buf()
	switch act {
	case "run", "dryrun":
		return m.requestRun(act == "dryrun")
	case "complete":
		m.updateCompletion(true)
	case "edesc":
		switch {
		case m.comp.open:
			m.comp.open = false
		case b.ed.selOn:
			b.ed.selOn = false
		}
	case "save":
		if b.path == "" {
			m.openPrompt(promptState{kind: "saveas", title: "Save buffer as", labels: []string{"file"}, note: "enter saves · esc cancels"}, "")
			return nil
		}
		if err := m.saveFile(b); err != nil {
			m.toast = "save: " + err.Error()
		} else {
			m.toast = "saved " + b.path
		}
	case "exteditor":
		return m.externalEditor()
	case "runs":
		m.ov, m.ovSel, m.ovOff = ovRuns, 0, 0
	case "newbuf":
		m.addBuffer("", "", "")
		m.toast = fmt.Sprintf("new buffer (%d open)", len(m.bufs))
	case "buflist":
		m.scanOrphans()
		m.ov, m.ovSel, m.ovOff = ovBuffers, m.cur, 0
	case "closebuf":
		if b.modified() || (b.path == "" && !b.ed.empty()) {
			what := "discard this buffer? Its text is not saved to a file."
			if b.path != "" {
				what = "close " + b.title() + " without saving the changes?"
			}
			m.askConfirm(confirmState{kind: "closebuf", lines: []string{what}})
			return nil
		}
		m.closeBuffer()
	case "prevbuf":
		m.cur = (m.cur - 1 + len(m.bufs)) % len(m.bufs)
		return m.gridEnsure()
	case "nextbuf":
		m.cur = (m.cur + 1) % len(m.bufs)
		return m.gridEnsure()
	case "savefav":
		m.openSaveFavorite()
	case "selectall":
		b.ed.selectAll()
	case "copysel":
		if s := b.ed.selection(); s != "" {
			m.toast = fmt.Sprintf("copied %d characters", len([]rune(s)))
			return tea.SetClipboard(s)
		}
		m.toast = "nothing is selected (shift+arrows select)"
	}
	return nil
}

// requestRun starts a Run of the selection, or of the whole buffer. A query
// with @parameters asks for their values first.
func (m *Model) requestRun(dry bool) tea.Cmd {
	b := m.buf()
	sql := b.ed.text()
	line0, col0 := 0, 0
	if r1, c1, _, _, ok := b.ed.selRange(); ok {
		sql, line0, col0 = b.ed.selection(), r1, c1
	}
	if strings.TrimSpace(sql) == "" {
		m.toast = "the buffer is empty"
		return nil
	}
	q := favorites.Query(b.ed.text(), sql)
	specs := favorites.Schema(q)
	if len(specs) == 0 {
		return m.startRun(b, sql, nil, dry, line0, col0)
	}
	names := make([]string, len(specs))
	vals := make([]string, len(specs))
	typed := false
	for i, s := range specs {
		names[i] = s.Name
		v, seen := b.params[s.Name]
		if !seen && s.Default != nil {
			v = *s.Default
		}
		vals[i] = v
		typed = typed || s.Type != ""
	}
	note := "the type is guessed from the value; force it with a prefix such as STRING:42 · enter runs · esc cancels"
	if typed {
		note = "types come from the -- param: header (others are guessed from the value) · null is NULL · enter runs · esc cancels"
	}
	m.openPrompt(promptState{kind: "params", title: "Query parameters", labels: names, specs: specs, query: q, sql: sql, dry: dry, line0: line0, col0: col0, note: note}, vals...)
	return nil
}

func (m *Model) startRun(b *buffer, sql string, params []api.QueryParam, dry bool, line0, col0 int) tea.Cmd {
	id := m.runs.Start(m.ctx, runs.Request{SQL: sql, Params: params, Tag: b.id, DryRunOnly: dry, Favorite: m.be.FavoriteFromID(b.path)},
		runs.Origin{Line: line0, Col: col0, TextVersion: b.ed.version})
	if !dry {
		// the result this buffer showed is replaced: free its loaded pages
		old := b.viewID
		shared := false
		for _, o := range m.bufs {
			shared = shared || (o != b && o.viewID == old)
		}
		if !shared {
			delete(m.grids, old)
		}
	}
	b.runID = id
	if !dry {
		b.viewID = id
	}
	b.ed.errLine, b.ed.errCol = 0, 0
	return m.followRun(id)
}

// followRun waits for the Run's next change. In tests (no background work) it
// waits until the Run needs the user or has finished, or its job is held
// running by the test (Options.JobHeld); a safety limit stops a stuck test.
func (m *Model) followRun(id int64) tea.Cmd {
	return func() tea.Msg {
		ctx := m.ctx
		var settled func(api.RunInfo) bool // nil: the next change
		if m.o.NoBackground {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(m.ctx, 10*time.Second)
			defer cancel()
			settled = func(info api.RunInfo) bool {
				held := info.Job != nil && m.o.JobHeld != nil && m.o.JobHeld(info.Job.ID)
				return info.State == api.RunNeedsOK || held
			}
		}
		if u, ok := m.runs.Wait(ctx, id, settled); ok {
			return runMsg{u}
		}
		return nil
	}
}

func (m *Model) bufferOf(tag string) (*buffer, int) {
	for i, b := range m.bufs {
		if b.id == tag {
			return b, i
		}
	}
	return nil, -1
}

func (m *Model) onRun(msg runMsg) tea.Cmd {
	info, news := m.runs.Apply(msg.u)
	b, bi := m.bufferOf(info.Tag)
	var cmds []tea.Cmd
	if !info.State.Finished() && !m.o.NoBackground {
		cmds = append(cmds, m.followRun(info.ID))
	}
	if !news {
		return tea.Batch(cmds...)
	}
	here := b != nil && bi == m.cur && m.zone != ZoneExplorer
	switch info.State {
	case api.RunNeedsOK:
		m.askConfirm(confirmState{kind: "run", runID: info.ID, lines: confirmLines(info)})
	case api.RunEstimated:
		m.toast = "dry-run: " + estimateText(info.Estimate)
	case api.RunFailed:
		if b != nil {
			if info.DryRunOnly || b.viewID == 0 {
				b.viewID = info.ID
			}
			// the position only means something in the text that was run
			if mk, ok := m.runs.ErrorMark(info.ID); ok && b.runID == info.ID && b.ed.version == mk.TextVersion {
				b.ed.moveTo(mk.Line, mk.Col)
				b.ed.errLine, b.ed.errCol = mk.Line, mk.Col
			}
		}
		if !here && info.Error != nil {
			m.toast = fmt.Sprintf("run #%d failed: %s", info.ID, info.Error.Message)
		}
	case api.RunCancelled:
		m.toast = fmt.Sprintf("run #%d cancelled", info.ID)
	case api.RunDone:
		if info.HasResult {
			g := newGrid(info)
			m.grids[info.ID] = g
			m.trimGrids()
			cmds = append(cmds, m.fetchPage(g, 0))
		}
		if !here {
			m.toast = fmt.Sprintf("run #%d finished: %s", info.ID, doneText(info))
		}
	}
	return tea.Batch(cmds...)
}

func estimateText(e *api.Estimate) string {
	if e == nil {
		return ""
	}
	s := format.Bytes(e.Bytes)
	switch e.Accuracy {
	case "LOWER_BOUND":
		s = "at least " + s
	case "UPPER_BOUND":
		s = "at most " + s
	case "UNKNOWN":
		s += " (estimate unknown)"
	}
	if e.Capacity {
		s += " scanned (not billed)"
	} else if e.ApproxUSD != nil {
		s += " ≈ " + usd(*e.ApproxUSD)
	}
	if e.StatementType != "" && e.StatementType != "SELECT" {
		s += " · " + e.StatementType
	}
	return s
}

func usd(v float64) string {
	if v > 0 && v < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

func doneText(info api.RunInfo) string {
	if info.HasResult {
		return format.Count(info.TotalRows) + " rows"
	}
	if info.Stats != nil && info.Stats.AffectedRows > 0 {
		return format.Count(info.Stats.AffectedRows) + " rows affected"
	}
	return "no result rows"
}

func confirmLines(info api.RunInfo) []string {
	c, e := info.Confirmation, info.Estimate
	var out []string
	if c == nil {
		return out
	}
	for _, r := range c.Reasons {
		switch r {
		case "statement":
			out = append(out, "This is a "+c.StatementType+" statement, not a SELECT: it can change data.")
		case "bytes":
			out = append(out, fmt.Sprintf("This query scans %s, over the confirmation threshold of %s.", estimateText(e), format.Bytes(c.Threshold)))
		}
	}
	if len(c.Reasons) == 1 && c.Reasons[0] == "statement" && e != nil {
		out = append(out, "Estimate: "+estimateText(e))
	}
	return out
}

// --- prompts

func (m *Model) openPrompt(p promptState, values ...string) {
	p.fields = make([]input, len(p.labels))
	for i := range p.fields {
		if i < len(values) {
			p.fields[i].set(values[i])
		}
	}
	m.prompt = p
	m.ov = ovPrompt
	m.comp.open = false
}

func (m *Model) promptKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	p := &m.prompt
	switch name {
	case "esc":
		m.ov = p.back
		return nil
	case "down", "ctrl+n":
		p.idx = (p.idx + 1) % len(p.fields)
		return nil
	case "up", "ctrl+p", "shift+tab":
		p.idx = (p.idx - 1 + len(p.fields)) % len(p.fields)
		return nil
	case "tab":
		if p.kind == "export" {
			for i, f := range export.Formats {
				if f == p.format {
					p.format = export.Formats[(i+1)%len(export.Formats)]
					break
				}
			}
			path := p.fields[0].value()
			p.fields[0].set(strings.TrimSuffix(path, filepath.Ext(path)) + "." + string(p.format))
			p.armed = false
			return nil
		}
		p.idx = (p.idx + 1) % len(p.fields)
		return nil
	case "enter":
		return m.promptSubmit()
	}
	if p.fields[p.idx].handle(msg) {
		if p.armed && (p.kind == "promote" || p.kind == "savefav") {
			p.note = ""
		}
		p.armed = false
		if p.kind == "export" {
			p.format = export.FormatOf(p.fields[0].value())
		}
	}
	return nil
}

func (m *Model) promptSubmit() tea.Cmd {
	p := &m.prompt
	b := m.buf()
	switch p.kind {
	case "favrename", "promote", "savefav":
		return m.promptFavorite(p)
	case "handoff":
		return m.handoffSubmit(p)
	case "params":
		answers := map[string]string{}
		for i, s := range p.specs {
			answers[s.Name] = p.fields[i].value()
		}
		params, err := favorites.Bind(p.query, favorites.Prompted(answers))
		if err != nil {
			var pe *favorites.ParamError
			if errors.As(err, &pe) {
				for i, s := range p.specs {
					if s.Name == pe.Name {
						p.idx = i
					}
				}
			}
			p.note = sErr.Render(err.Error())
			return nil
		}
		for k, v := range answers {
			b.params[k] = v
		}
		m.ov = ovNone
		return m.startRun(b, p.sql, params, p.dry, p.line0, p.col0)
	case "saveas":
		path := expandHome(strings.TrimSpace(p.fields[0].value()))
		if path == "" {
			return nil
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		old := *b
		b.path = path
		if err := m.saveFile(b); err != nil {
			b.path = old.path
			p.note = sErr.Render(err.Error())
			return nil
		}
		// the buffer now lives in the file: its draft is gone
		if b.lock != nil {
			b.lock.Unlock()
			b.lock = nil
		}
		m.removeDraft(b)
		m.ov = ovNone
		m.toast = "saved " + path
	case "edfind":
		m.ov = ovNone
		b.vim.search = p.fields[0].value()
		if r, c, ok := b.ed.searchFrom(b.vim.search, 1); ok {
			b.ed.row, b.ed.col, b.ed.want = r, c, c
		} else if b.vim.search != "" {
			m.toast = "“" + b.vim.search + "” is not in the buffer"
		}
	case "find":
		m.ov = ovNone
		g := m.grid()
		if g == nil {
			return nil
		}
		g.find = p.fields[0].value()
		return m.findNext(1)
	case "export":
		g := m.grid()
		if g == nil {
			m.ov = ovNone
			return nil
		}
		path := expandHome(strings.TrimSpace(p.fields[0].value()))
		if path == "" {
			return nil
		}
		if g.total > exportAskRows && !p.armed {
			p.armed = true
			p.note = sWarn.Render(fmt.Sprintf("%s rows: press enter again to export them all", format.Count(g.total)))
			return nil
		}
		if _, err := os.Stat(path); err == nil && !p.armed {
			p.armed = true
			p.note = sWarn.Render("the file exists: press enter again to overwrite it")
			return nil
		}
		if m.exp != nil {
			p.note = sWarn.Render("an export is already running")
			return nil
		}
		ex, err := m.be.StartExport(m.ctx, g.run, path, p.format)
		if err != nil {
			p.note = sErr.Render(err.Error())
			return nil
		}
		m.ov = ovNone
		m.exp, m.expEv = ex, api.ExportEvent{Path: path, Total: g.total}
		return m.waitExport(ex)
	}
	return nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (m *Model) waitExport(ex Export) tea.Cmd {
	return func() tea.Msg {
		if m.o.NoBackground {
			return exportMsg{ex.Wait(), true}
		}
		ev, ok := <-ex.Events()
		return exportMsg{ev, ok}
	}
}

func (m *Model) onExport(msg exportMsg) tea.Cmd {
	if !msg.ok || m.exp == nil {
		return nil
	}
	m.expEv = msg.ev
	if !msg.ev.Done {
		return m.waitExport(m.exp)
	}
	m.exp = nil
	switch msg.ev.Error {
	case "":
		m.toast = fmt.Sprintf("exported %s rows to %s", format.Count(msg.ev.Rows), msg.ev.Path)
	case "cancelled":
		m.toast = "export cancelled (the partial file was removed)"
	default:
		m.toast = "export failed: " + msg.ev.Error
	}
	return nil
}

// --- confirmations

// askConfirm shows a question now, or after the popup that is open.
func (m *Model) askConfirm(c confirmState) {
	if m.ov != ovNone {
		m.confirmQ = append(m.confirmQ, c)
		return
	}
	m.confirm, m.ov = c, ovConfirm
}

// nextConfirm shows a waiting question once nothing else is open. A Run's
// question is dropped if the Run no longer waits for an answer.
func (m *Model) nextConfirm() {
	for m.ov == ovNone && len(m.confirmQ) > 0 {
		c := m.confirmQ[0]
		m.confirmQ = m.confirmQ[1:]
		if c.kind == "run" {
			if r, ok := m.runs.Info(c.runID); !ok || r.State != api.RunNeedsOK {
				continue
			}
		}
		m.confirm, m.ov = c, ovConfirm
	}
}

func (m *Model) confirmKey(name string) tea.Cmd {
	c := m.confirm
	switch c.kind {
	case "load":
		req := m.pendingLoad
		if req == nil {
			m.ov = ovNone
			return nil
		}
		switch name {
		case "y", "Y", "enter":
			m.ov, m.pendingLoad = ovNone, nil
			return m.finishLoad(*req, true)
		case "n", "N":
			m.ov, m.pendingLoad = ovNone, nil
			return m.finishLoad(*req, false)
		case "esc", "q", "ctrl+c":
			m.ov, m.pendingLoad = ovNone, nil
		}
		return nil
	case "favdelete":
		switch name {
		case "y", "Y", "enter":
			m.ov = c.back
			switch err := m.be.DeleteFavorite(c.subject); {
			case errors.Is(err, favorites.ErrMoved):
				m.toast = "favorites_dir changed since the question: nothing was deleted"
			case err != nil:
				m.toast = "delete: " + err.Error()
			default:
				m.toast = "deleted favorite " + c.subject.ID
			}
			if m.pk != nil {
				m.reloadFavorites()
			}
		case "n", "N", "esc", "q", "ctrl+c":
			m.ov = c.back
		}
		return nil
	}
	switch name {
	case "y", "Y", "enter":
		m.ov = ovNone
		switch c.kind {
		case "run":
			m.runs.Confirm(c.runID)
			if m.o.NoBackground {
				return m.followRun(c.runID)
			}
		case "quit":
			m.runs.CancelAll()
			return m.quit()
		case "closebuf":
			m.closeBuffer()
		}
	case "n", "N", "esc", "q", "ctrl+c":
		m.ov = ovNone
		if c.kind == "run" {
			m.runs.Cancel(c.runID)
			if m.o.NoBackground {
				return m.followRun(c.runID)
			}
		}
	}
	return nil
}

func (m *Model) quit() tea.Cmd {
	m.releaseBuffers()
	m.cancel()
	return tea.Quit
}

func (m *Model) activeRuns() []api.RunInfo {
	var out []api.RunInfo
	for _, r := range m.runs.List() {
		if !r.State.Finished() {
			out = append(out, r)
		}
	}
	return out
}

// cancelFocused implements ctrl+c: the export, then the current buffer's Run,
// then any other Run; reports whether something was there to cancel.
func (m *Model) cancelFocused() bool {
	if m.exp != nil {
		m.exp.Cancel()
		m.toast = "cancelling the export…"
		return true
	}
	target := int64(0)
	if len(m.bufs) > 0 {
		// the focused Run: the one shown in Results, the buffer's latest in the editor
		ids := []int64{m.buf().runID, m.buf().viewID}
		if m.zone == ZoneResults {
			ids = []int64{m.buf().viewID, m.buf().runID}
		}
		for _, id := range ids {
			if r, ok := m.runs.Info(id); ok && !r.State.Finished() {
				target = r.ID
				break
			}
		}
	}
	if target == 0 {
		if act := m.activeRuns(); len(act) > 0 {
			target = act[0].ID
		}
	}
	if target == 0 {
		return false
	}
	m.runs.Cancel(target)
	m.toast = fmt.Sprintf("cancel requested for run #%d", target)
	if !m.runs.Pricing().Capacity {
		m.toast += " (it may still be billed)"
	}
	return true
}

// --- external editor

func (m *Model) externalEditor() tea.Cmd {
	b := m.buf()
	if m.o.Getenv("VISUAL") == "" && m.o.Getenv("EDITOR") == "" {
		// the OS opener returns at once, so there would be nothing to read back
		m.toast = "set $EDITOR (or $VISUAL) to a terminal editor to use the hand-off"
		return nil
	}
	f, err := os.CreateTemp("", "bqt-*.sql")
	if err != nil {
		m.toast = "editor: " + err.Error()
		return nil
	}
	f.WriteString(b.ed.text())
	f.Close()
	argv := append(config.ResolveEditor(m.o.Getenv, runtime.GOOS), f.Name())
	id, path := b.id, f.Name()
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return extEditMsg{id, path, err} })
}

func (m *Model) onExtEdit(msg extEditMsg) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.toast = "editor: " + msg.err.Error()
		return
	}
	b, _ := m.bufferOf(msg.buf)
	data, err := os.ReadFile(msg.path)
	if b == nil || err != nil {
		return
	}
	text := strings.TrimRight(string(data), "\n")
	if text != b.ed.text() {
		b.ed.setText(text)
		m.saveDraft(b)
	}
}

// --- results zone

func (m *Model) resultsAction(act string) tea.Cmd {
	if cmd, ok := m.globalAction(act); ok {
		return cmd
	}
	switch act {
	case "runs":
		m.ov, m.ovSel, m.ovOff = ovRuns, 0, 0
		return nil
	case "back":
		if g := m.grid(); g != nil && g.find != "" {
			g.find = ""
			return nil
		}
		m.setZone(ZoneEditor)
		return nil
	}
	g := m.grid()
	if g == nil {
		if act != "" {
			m.toast = "no result to work with yet (ctrl+r in the editor runs the query)"
		}
		return nil
	}
	half := int64(max(1, m.gridRows()/2))
	switch act {
	case "down":
		g.moveRow(1)
	case "up":
		g.moveRow(-1)
	case "left":
		g.moveCol(-1)
	case "right":
		g.moveCol(1)
	case "halfdown":
		g.moveRow(half)
	case "halfup":
		g.moveRow(-half)
	case "pagedown":
		g.moveRow(2 * half)
	case "pageup":
		g.moveRow(-2 * half)
	case "top":
		g.row = 0
	case "bottom":
		g.row = max(0, g.total-1)
	case "firstcol":
		g.col = 0
		g.moveCol(0)
		g.leftCol = 0
	case "lastcol":
		g.moveCol(len(g.cols))
	case "record":
		return m.openRunRecord()
	case "freeze":
		g.frozen = !g.frozen
	case "hide":
		if len(g.shownCols()) > 1 {
			h := g.col
			g.moveCol(1)
			if g.col == h {
				g.moveCol(-1)
			}
			g.hidden[h] = true
			if g.leftCol == h {
				g.leftCol = g.col
			}
		}
	case "unhide":
		g.hidden = map[int]bool{}
	case "find":
		m.openPrompt(promptState{kind: "find", title: "Find in the loaded rows", labels: []string{"text"},
			note: fmt.Sprintf("searches the %d loaded page(s) of %d · n / N next and previous", len(g.pages), (g.total+gridPage-1)/gridPage)}, g.find)
		return nil
	case "findnext":
		return m.findNext(1)
	case "findprev":
		return m.findNext(-1)
	case "copy":
		if cells := g.cells(g.row); cells != nil && g.col < len(cells) {
			col := g.col // the cursor may move while the row is read
			return m.copyRow(g, func(cols []api.PreviewColumn, full []api.Cell) (string, string) {
				if col >= len(full) || full[col].Null {
					return "", "copied NULL as an empty string"
				}
				return full[col].Text, "copied the cell"
			})
		}
	case "copyrow":
		return m.copyRow(g, func(cols []api.PreviewColumn, full []api.Cell) (string, string) {
			return format.RowJSON(cols, full), "copied the row as JSON"
		})
	case "copycol":
		var vals []string
		for _, r := range g.pages[pageOf(g.row)] {
			if g.col < len(r) && !r[g.col].Null {
				vals = append(vals, r[g.col].Text)
			} else {
				vals = append(vals, "")
			}
		}
		m.toast = fmt.Sprintf("copied %d values of %s (the loaded page)", len(vals), g.cols[g.col].Name)
		return tea.SetClipboard(strings.Join(vals, "\n"))
	case "copypath":
		m.toast = "copied " + g.cols[g.col].Name
		return tea.SetClipboard(g.cols[g.col].Name)
	case "copytsv":
		var rows [][]api.Cell
		for r := g.top; r < g.top+int64(m.gridRows()) && r < g.total; r++ {
			if c := g.cells(r); c != nil {
				rows = append(rows, c)
			}
		}
		m.toast = fmt.Sprintf("copied %d rows as TSV", len(rows))
		return tea.SetClipboard(export.TSV(g.cols, rows))
	case "export":
		if m.exp != nil {
			m.toast = "an export is already running (ctrl+c cancels it)"
			return nil
		}
		path := m.be.DefaultExportPath(export.CSV)
		m.openPrompt(promptState{kind: "export", title: fmt.Sprintf("Export %s rows", format.Count(g.total)), labels: []string{"file"}, format: export.CSV,
			note: "tab changes the format (csv, json, ndjson) · enter exports · esc cancels"}, path)
		return nil
	}
	return m.gridEnsure()
}

// copyRow reads the cursor row in full and copies what pick returns.
func (m *Model) copyRow(g *grid, pick func(cols []api.PreviewColumn, full []api.Cell) (text, toast string)) tea.Cmd {
	id, row, cols := g.run, g.row, g.cols
	return func() tea.Msg {
		page, err := m.be.RunRows(m.ctx, id, row, 1, format.Options{})
		if err != nil || len(page.Rows) != 1 {
			return clipMsg{err: fmt.Errorf("could not read the row: %v", err)}
		}
		text, toast := pick(cols, page.Rows[0])
		return clipMsg{text: text, toast: toast}
	}
}

func (m *Model) findNext(dir int) tea.Cmd {
	g := m.grid()
	if g == nil || g.find == "" {
		m.toast = "nothing to find: press / first"
		return nil
	}
	found, hits := g.search(dir)
	pages := (g.total + gridPage - 1) / gridPage
	if !found {
		m.toast = fmt.Sprintf("“%s” is not in the %d loaded page(s) of %d", g.find, len(g.pages), pages)
		return nil
	}
	m.toast = fmt.Sprintf("%d matching cells in the %d loaded page(s) of %d", hits, len(g.pages), pages)
	return m.gridEnsure()
}

// --- list overlays: runs and buffers

func (m *Model) runsOverlayAction(act string, name string) tea.Cmd {
	list := m.runs.List()
	switch act {
	case "down":
		m.ovSel = min(m.ovSel+1, max(0, len(list)-1))
	case "up":
		m.ovSel = max(0, m.ovSel-1)
	case "back":
		m.ov = ovNone
	case "open":
		m.ov = ovNone
		if m.ovSel < len(list) {
			r := list[m.ovSel]
			if r.State == api.RunNeedsOK {
				m.askConfirm(confirmState{kind: "run", runID: r.ID, lines: confirmLines(r)})
				return nil
			}
			if _, i := m.bufferOf(r.Tag); i >= 0 {
				m.cur = i
			}
			m.buf().viewID = r.ID
			if r.HasResult && m.grids[r.ID] == nil {
				m.grids[r.ID] = newGrid(r)
			}
			m.setZone(ZoneResults)
			return m.gridEnsure()
		}
	default:
		if name == "c" && m.ovSel < len(list) {
			m.runs.Cancel(list[m.ovSel].ID)
			m.toast = fmt.Sprintf("cancel requested for run #%d", list[m.ovSel].ID)
		}
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

func (m *Model) buffersOverlayAction(act string) tea.Cmd {
	n := len(m.bufs) + len(m.orphans)
	switch act {
	case "down":
		m.ovSel = min(m.ovSel+1, max(0, n-1))
	case "up":
		m.ovSel = max(0, m.ovSel-1)
	case "back":
		m.ov = ovNone
	case "open":
		m.ov = ovNone
		switch {
		case m.ovSel < len(m.bufs):
			m.cur = m.ovSel
		case m.ovSel < n:
			o := m.orphans[m.ovSel-len(m.bufs)]
			if err := m.restoreOrphan(o); err != nil {
				m.toast = err.Error()
				return nil
			}
			m.toast = "draft restored"
		}
		m.setZone(ZoneEditor)
		return m.gridEnsure()
	default:
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

// insertPath puts a qualified table name into the editor (the explorer's i).
func (m *Model) insertPath(p api.ObjectPath) {
	if p.Project == "" {
		return
	}
	name := "`" + p.String() + "`"
	if strings.HasSuffix(p.Table, "YYYYMMDD") {
		name = "`" + p.Project + "." + p.Dataset + "." + strings.TrimSuffix(p.Table, "YYYYMMDD") + "*`"
	}
	m.buf().ed.insert(name)
	m.setZone(ZoneEditor)
	if m.o.NoBackground {
		m.saveDraft(m.buf())
	}
}
