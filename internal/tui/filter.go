package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// listFilter narrows one Explorer list in place: a Miller column or the tree.
// The flat layout keeps its own searchState.
type listFilter struct {
	in     input
	parsed api.ParsedQuery
	typing bool

	answered bool            // set and rank answer the current text
	set      map[string]bool // openKey of every match
	under    map[string]bool // openKey of every project and dataset that holds a match
	rank     []string        // openKeys, best first
	count    int             // matches the list shows (a family once, not with its members)
	err      string
	// best moves the cursor to the best match when the next answer arrives
	// (set while typing; a reload keeps the cursor where it is).
	best bool

	seq    int
	cancel context.CancelFunc
}

func (f *listFilter) active() bool { return strings.TrimSpace(f.in.value()) != "" }

// passes reports whether the list shows n. Until the first answer for the
// current text arrives the list stays as it was.
func (f *listFilter) passes(n api.BrowseNode) bool {
	return !f.active() || !f.answered || f.set[openKey(n)]
}

func (f *listFilter) clear() {
	if f.cancel != nil {
		f.cancel()
	}
	*f = listFilter{seq: f.seq + 1}
}

// bestIn returns the index of the best-ranked match among nodes, or -1.
func (f *listFilter) bestIn(nodes []api.BrowseNode) int {
	at := make(map[string]int, len(nodes))
	for i, n := range nodes {
		at[openKey(n)] = i
	}
	for _, k := range f.rank {
		if i, ok := at[k]; ok {
			return i
		}
	}
	return -1
}

type filterMsg struct {
	f     *listFilter
	seq   int
	scope string // the parent the query was scoped to (Miller columns)
	resp  api.FilterResponse
	err   error
}

// filterQuery says what a list's filter asks the core for.
func (m *Model) filterQuery(f *listFilter) (opts api.FilterOptions, scope string) {
	opts.Hidden = m.hiddenProjects()
	if f == &m.tree.f {
		return opts, "" // the whole tree, every kind
	}
	for c := range m.mil.f {
		if f != &m.mil.f[c] {
			continue
		}
		switch c {
		case 0:
			opts.Kinds = []api.ResultKind{api.KindProject}
		case 1:
			opts.Kinds = []api.ResultKind{api.KindDataset}
			if p, ok := m.mil.at(0); ok {
				opts.Scope = &api.ObjectPath{Project: p.Path.Project}
			}
		case 2:
			opts.Kinds = []api.ResultKind{api.KindTable, api.KindShards}
			if d, ok := m.mil.at(1); ok {
				opts.Scope = &api.ObjectPath{Project: d.Path.Project, Dataset: d.Path.Dataset}
			}
		}
		if opts.Scope != nil {
			scope = opts.Scope.String()
		} else if c > 0 {
			scope = "\x00none" // no parent: nothing to filter
		}
	}
	return opts, scope
}

// refilter runs the filter for its current text, cancelling the previous run.
// An empty filter is applied at once.
func (m *Model) refilter(f *listFilter) tea.Cmd {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.seq++
	f.parsed = m.be.ParseQuery(f.in.value())
	if !f.active() {
		f.answered, f.set, f.under, f.rank, f.count, f.err = false, nil, nil, nil, 0, ""
		m.applyFilter(f)
		return nil
	}
	query, seq := f.in.value(), f.seq
	opts, scope := m.filterQuery(f)
	ctx, cancel := context.WithCancel(m.ctx)
	f.cancel = cancel
	return func() tea.Msg {
		resp, err := m.be.Filter(ctx, query, opts)
		return filterMsg{f: f, seq: seq, scope: scope, resp: resp, err: err}
	}
}

func (m *Model) onFilter(msg filterMsg) {
	f := msg.f
	if msg.seq != f.seq {
		return // superseded by a newer keystroke
	}
	if _, scope := m.filterQuery(f); scope != msg.scope {
		return // the column's parent changed while the query ran
	}
	if msg.err != nil {
		if msg.err != context.Canceled {
			f.err = msg.err.Error()
		}
		return
	}
	f.err, f.answered = "", true
	f.set, f.under = map[string]bool{}, map[string]bool{}
	f.rank = make([]string, 0, len(msg.resp.Matches))
	for _, fm := range msg.resp.Matches {
		k := nodeKey(fm.Kind, fm.Path)
		f.set[k] = true
		f.rank = append(f.rank, k)
		p := fm.Path
		if p.Dataset != "" {
			f.under[openKey(api.BrowseNode{Kind: api.KindProject, Path: api.ObjectPath{Project: p.Project}})] = true
		}
		if p.Table != "" {
			f.under[openKey(api.BrowseNode{Kind: api.KindDataset, Path: api.ObjectPath{Project: p.Project, Dataset: p.Dataset}})] = true
		}
	}
	f.count = countMatches(msg.resp.Matches, f.under)
	m.applyFilter(f)
}

// countMatches counts what a list shows: a container that holds a match is
// counted through that match, and a shard member through its family.
func countMatches(ms []api.FilterMatch, under map[string]bool) int {
	fams := map[string]bool{} // dataset path + family prefix
	for _, fm := range ms {
		if fm.Kind == api.KindShards {
			fams[fm.Path.Project+"."+fm.Path.Dataset+"\x00"+strings.TrimSuffix(fm.Path.Table, "YYYYMMDD")] = true
		}
	}
	n := 0
	for _, fm := range ms {
		if under[nodeKey(fm.Kind, fm.Path)] {
			continue
		}
		if t := fm.Path.Table; fm.Kind == api.KindTable && len(t) > 8 && fams[fm.Path.Project+"."+fm.Path.Dataset+"\x00"+t[:len(t)-8]] {
			continue
		}
		n++
	}
	return n
}

// applyFilter shows a filter's answer in its list.
func (m *Model) applyFilter(f *listFilter) {
	best := f.best && f.answered
	f.best = false
	if t := &m.tree; f == &t.f {
		var prev *api.BrowseNode
		if t.cur < len(t.rows) {
			n := t.rows[t.cur].node
			prev = &n
		}
		if !f.active() {
			t.filterOpen = nil
		}
		m.treeBuild()
		switch {
		case best:
			if i := m.treeBest(); i >= 0 {
				t.cur = i
			}
		case !f.active() && prev != nil:
			m.treeSelectNearest(prev.Path)
		}
		return
	}
	for c := range m.mil.f {
		if f != &m.mil.f[c] {
			continue
		}
		ml := &m.mil
		m.milLoad(c)
		if best {
			if i := f.bestIn(ml.cols[c]); i >= 0 && i != ml.cur[c] {
				ml.cur[c] = i
				for d := c + 1; d < 3; d++ {
					ml.cur[d], ml.off[d] = 0, 0
				}
				if c < 2 {
					ml.family = nil
					m.milLoad(c + 1)
				}
			}
		}
	}
}

// typingFilter is the Miller or tree filter whose input has the focus.
func (m *Model) typingFilter() *listFilter {
	if m.ov != ovNone || m.zone != ZoneExplorer {
		return nil
	}
	if f := m.layoutFilter(); f != nil && f.typing {
		return f
	}
	return nil
}

// layoutFilter is the filter `/` edits in the current layout (nil in flat).
func (m *Model) layoutFilter() *listFilter {
	switch m.layout {
	case layoutMiller:
		return &m.mil.f[m.mil.col]
	case layoutTree:
		return &m.tree.f
	}
	return nil
}

// filterKey handles a key while a filter input has the focus.
func (m *Model) filterKey(f *listFilter, msg tea.KeyPressMsg, name string) tea.Cmd {
	half := max(1, m.listHeight()/2)
	isText := msg.Key().Text != "" && msg.Key().Mod&^tea.ModShift == 0
	if !isText {
		switch keys[ZoneSearch][name] {
		case "down":
			m.move(1)
			return nil
		case "up":
			m.move(-1)
			return nil
		case "halfdown":
			m.move(half)
			return nil
		case "halfup":
			m.move(-half)
			return nil
		case "detaildown":
			return m.scrollDetail(1)
		case "detailup":
			return m.scrollDetail(-1)
		case "back", "open":
			f.typing = false
			return nil
		}
		if a := keys[ZoneGlobal][name]; a != "" && a != "palette" {
			cmd, _ := m.globalAction(a)
			return cmd
		}
	}
	if f.in.handle(msg) {
		f.best = true
		if f == &m.tree.f {
			m.tree.filterOpen = nil // a new query starts from the automatic expansion
		}
		return m.refilter(f)
	}
	return nil
}

// dropDeeperFilters keeps the Miller rule: a column's filter lives only while
// the focus is in that column or one to its right.
func (m *Model) dropDeeperFilters() {
	ml := &m.mil
	for c := ml.col + 1; c < 3; c++ {
		if ml.f[c].active() {
			ml.f[c].clear()
			m.milLoad(c)
		}
	}
}

// filterHeader is the input line and chip row shown above a list while its
// filter is being typed.
func (m *Model) filterHeader(f *listFilter, w int, what string) []string {
	b := newRB(false)
	b.add(" filter "+what+" ▸ ", sAcc)
	b.l.WriteString(f.in.render(f.parsed, true, "type a name, or type:view col:user.id label:team"))
	info := ""
	switch {
	case f.err != "":
		info = "error: " + f.err
	case f.active() && f.answered:
		info = plural(f.count, "match", "matches")
	}
	b.addR(info+" ", sDim)
	return []string{b.line(w), fit(" "+chips(f.parsed, w-1, ""), w)}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

// filterActive reports whether the current layout's list is filtered.
func (m *Model) filterActive() bool {
	switch m.layout {
	case layoutFlat:
		return m.flat.in.value() != ""
	case layoutMiller:
		for c := range m.mil.f {
			if m.mil.f[c].active() {
				return true
			}
		}
		return false
	}
	return m.tree.f.active()
}

// treeSelectNearest puts the cursor on the object, or on its nearest
// ancestor the tree shows (the filter is cleared without opening rows).
func (m *Model) treeSelectNearest(p api.ObjectPath) {
	t := &m.tree
	keys := []string{nodeKey(api.KindProject, api.ObjectPath{Project: p.Project})}
	if p.Dataset != "" {
		keys = append(keys, nodeKey(api.KindDataset, api.ObjectPath{Project: p.Project, Dataset: p.Dataset}))
	}
	if p.Table != "" {
		keys = append(keys, nodeKey(api.KindTable, p), nodeKey(api.KindShards, p))
	}
	at := map[string]int{}
	for i, r := range t.rows {
		at[openKey(r.node)] = i
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if row, ok := at[keys[i]]; ok {
			t.cur = row
			return
		}
	}
}

// filterOn reports whether the current layout's list is filtered (or its
// filter input is open).
func (m *Model) filterOn() bool {
	if m.layout == layoutFlat {
		return m.flat.typing || m.flat.in.value() != ""
	}
	if m.layout == layoutMiller {
		for c := range m.mil.f {
			if m.mil.f[c].active() || m.mil.f[c].typing {
				return true
			}
		}
		return false
	}
	return m.tree.f.active() || m.tree.f.typing
}
