package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// selection is what the cursor is on, in any layout.
type selection struct {
	kind api.ResultKind // "" when nothing is selected
	path api.ObjectPath
	node api.BrowseNode
}

// detailKey identifies a table whose detail can be loaded ("" otherwise).
func (s selection) detailKey() string {
	if s.kind != api.KindTable && s.kind != api.KindShards {
		return ""
	}
	return s.path.Project + "\x00" + s.path.Dataset + "\x00" + s.path.Table
}

// --- Miller columns

type miller struct {
	cols   [3][]api.BrowseNode
	cur    [3]int
	off    [3]int
	col    int
	family *api.BrowseNode // set when the tables column shows a shard family's members
	famIdx int             // cursor to return to when the family is collapsed
	f      [3]listFilter   // each column's filter
	all    [3]int          // each column's length before its filter
}

func (ml *miller) at(c int) (api.BrowseNode, bool) {
	if ml.cur[c] < 0 || ml.cur[c] >= len(ml.cols[c]) {
		return api.BrowseNode{}, false
	}
	return ml.cols[c][ml.cur[c]], true
}

func indexOf(nodes []api.BrowseNode, name string) int {
	for i, n := range nodes {
		if n.Name == name {
			return i
		}
	}
	return -1
}

// milLoad refreshes column c and everything to its right, keeping the cursor
// on the same name where possible.
func (m *Model) milLoad(c int) {
	ml := &m.mil
	keep := func(c int, nodes []api.BrowseNode) {
		name := ""
		if n, ok := ml.at(c); ok {
			name = n.Name
		}
		ml.all[c] = len(nodes)
		f := &ml.f[c]
		if f.active() && f.ok {
			kept := make([]api.BrowseNode, 0, len(nodes))
			for _, n := range nodes {
				if f.passes(n) {
					kept = append(kept, n)
				}
			}
			nodes = kept
		}
		ml.cols[c] = nodes
		switch i := indexOf(nodes, name); {
		case i >= 0:
			ml.cur[c] = i
		case f.active() && f.bestIn(nodes) >= 0:
			ml.cur[c] = f.bestIn(nodes) // the selection was filtered out
		default:
			ml.cur[c] = clamp(ml.cur[c], 0, max(0, len(nodes)-1))
		}
	}
	if c <= 0 {
		keep(0, m.projects())
	}
	if c <= 1 {
		var ds []api.BrowseNode
		if p, ok := ml.at(0); ok {
			ds = m.be.Datasets(p.Name)
		}
		keep(1, ds)
	}
	var ts []api.BrowseNode
	if d, ok := ml.at(1); ok {
		// an open family belongs to one dataset: drop it when the dataset
		// changed under it (a project turned off, a config reload)
		if f := ml.family; f != nil && (f.Path.Project != d.Path.Project || f.Path.Dataset != d.Path.Dataset) {
			ml.family = nil
		}
		if ml.family != nil {
			ts = m.be.ShardMembers(d.Path.Project, d.Path.Dataset, ml.family.Shard.Prefix)
			if len(ts) == 0 {
				ml.family = nil
			}
		}
		if ml.family == nil {
			ts = m.be.Tables(d.Path.Project, d.Path.Dataset)
		}
	} else {
		ml.family = nil
	}
	keep(2, ts)
	// an empty column cannot hold the focus, unless only its filter emptied it
	for ml.col > 0 && len(ml.cols[ml.col]) == 0 && !(ml.f[ml.col].active() && ml.all[ml.col] > 0) {
		ml.col--
	}
}

// milDropHiding clears every column filter that would hide an object or one
// of its ancestors.
func (m *Model) milDropHiding(p api.ObjectPath, kind api.ResultKind) {
	ml := &m.mil
	levels := []api.BrowseNode{{Kind: api.KindProject, Path: api.ObjectPath{Project: p.Project}}}
	if p.Dataset != "" {
		levels = append(levels, api.BrowseNode{Kind: api.KindDataset, Path: api.ObjectPath{Project: p.Project, Dataset: p.Dataset}})
	}
	if p.Table != "" {
		k := api.KindTable
		if kind == api.KindShards {
			k = api.KindShards
		}
		levels = append(levels, api.BrowseNode{Kind: k, Path: p})
	}
	for c := range ml.f {
		f := &ml.f[c]
		if f.active() && f.ok && (c >= len(levels) || !f.set[openKey(levels[c])]) {
			f.clear()
		}
	}
}

func (m *Model) milCollapseFamily() {
	ml := &m.mil
	if ml.family == nil {
		return
	}
	name := ml.family.Name
	ml.family = nil
	ml.cur[2], ml.off[2] = 0, 0
	m.milLoad(2)
	if i := indexOf(ml.cols[2], name); i >= 0 {
		ml.cur[2] = i
	}
}

// --- tree

type treeRow struct {
	depth int
	node  api.BrowseNode
}

type tree struct {
	rows     []treeRow
	cur, off int
	open     map[string]bool
	f        listFilter
	// fexp is the expansion while the filter is on, kept apart from open so
	// that clearing the filter restores the rows the user had opened. A row
	// missing from it is open when it holds a match (shard families stay
	// closed).
	fexp map[string]bool
}

func openKey(n api.BrowseNode) string { return string(n.Kind) + "\x00" + n.Path.String() }

func (t *tree) filtering() bool { return t.f.active() && t.f.ok }

func (t *tree) isOpen(n api.BrowseNode) bool {
	k := openKey(n)
	if !t.filtering() {
		return t.open[k]
	}
	if v, ok := t.fexp[k]; ok {
		return v
	}
	return t.f.under[k]
}

func (t *tree) setOpen(n api.BrowseNode, v bool) {
	k := openKey(n)
	switch {
	case t.filtering():
		if t.fexp == nil {
			t.fexp = map[string]bool{}
		}
		t.fexp[k] = v
	case v:
		t.open[k] = true
	default:
		delete(t.open, k)
	}
}

func (m *Model) treeBuild() {
	t := &m.tree
	var keep string
	if t.cur >= 0 && t.cur < len(t.rows) {
		keep = openKey(t.rows[t.cur].node)
	}
	t.rows = t.rows[:0]
	f := &t.f
	on := t.filtering()
	// shown: under the filter, a row is a match or holds one; below a
	// container that matched only by its own name, every row is shown
	shown := func(n api.BrowseNode, all bool) bool {
		return !on || all || f.set[openKey(n)] || f.under[openKey(n)]
	}
	for _, p := range m.projects() {
		if !shown(p, false) {
			continue
		}
		t.rows = append(t.rows, treeRow{0, p})
		if !t.isOpen(p) {
			continue
		}
		allP := on && !f.under[openKey(p)]
		for _, d := range m.be.Datasets(p.Name) {
			if !shown(d, allP) {
				continue
			}
			t.rows = append(t.rows, treeRow{1, d})
			if !t.isOpen(d) {
				continue
			}
			allD := allP || on && !f.under[openKey(d)]
			for _, tb := range m.be.Tables(p.Name, d.Name) {
				if !shown(tb, allD) {
					continue
				}
				t.rows = append(t.rows, treeRow{2, tb})
				if tb.Kind == api.KindShards && t.isOpen(tb) {
					for _, mem := range m.be.ShardMembers(p.Name, d.Name, tb.Shard.Prefix) {
						if shown(mem, allD) {
							t.rows = append(t.rows, treeRow{3, mem})
						}
					}
				}
			}
		}
	}
	found := false
	if keep != "" {
		for i, r := range t.rows {
			if openKey(r.node) == keep {
				t.cur, found = i, true
				break
			}
		}
	}
	if !found && on {
		if i := m.treeBest(); i >= 0 {
			t.cur = i // the selection was filtered out
		}
	}
	t.cur = clamp(t.cur, 0, max(0, len(t.rows)-1))
}

// treeBest is the row of the best-ranked match the tree shows, or -1.
func (m *Model) treeBest() int {
	nodes := make([]api.BrowseNode, len(m.tree.rows))
	for i, r := range m.tree.rows {
		nodes[i] = r.node
	}
	return m.tree.f.bestIn(nodes)
}

// --- shared navigation

func (m *Model) reloadBrowse() {
	m.milLoad(0)
	m.treeBuild()
}

func (m *Model) rerunSearches() tea.Cmd {
	var cmds []tea.Cmd
	for c := range m.mil.f {
		if m.mil.f[c].active() {
			cmds = append(cmds, m.refilter(&m.mil.f[c]))
		}
	}
	if m.tree.f.active() {
		cmds = append(cmds, m.refilter(&m.tree.f))
	}
	if m.layout == layoutFlat {
		cmds = append(cmds, m.runSearch(m.flat))
	}
	if st := m.typingState(); st != nil && st != m.flat {
		cmds = append(cmds, m.runSearch(st))
	}
	return tea.Batch(cmds...)
}

func (m *Model) runSearchIfFlat() tea.Cmd {
	if m.layout == layoutFlat {
		return m.runSearch(m.flat)
	}
	return nil
}

func (m *Model) sel() selection {
	if st := m.activeResults(); st != nil {
		return st.selection()
	}
	switch m.layout {
	case layoutMiller:
		if n, ok := m.mil.at(m.mil.col); ok {
			return selection{kind: n.Kind, path: n.Path, node: n}
		}
	case layoutTree:
		if m.tree.cur < len(m.tree.rows) {
			n := m.tree.rows[m.tree.cur].node
			return selection{kind: n.Kind, path: n.Path, node: n}
		}
	}
	return selection{}
}

// activeResults is the result list the cursor is in, if any.
func (m *Model) activeResults() *searchState {
	switch {
	case m.ov == ovJump:
		return m.jump
	case m.ov == ovNone && m.layout == layoutFlat:
		return m.flat
	}
	return nil
}

func (m *Model) listHeight() int { return max(1, m.h-4) }

func (m *Model) move(d int) {
	switch m.layout {
	case layoutMiller:
		ml := &m.mil
		c := ml.col
		old := ml.cur[c]
		ml.cur[c] = clamp(ml.cur[c]+d, 0, max(0, len(ml.cols[c])-1))
		if ml.cur[c] != old && c < 2 {
			for i := c + 1; i < 3; i++ {
				ml.cur[i], ml.off[i] = 0, 0
			}
			ml.family = nil
			m.milLoad(c + 1)
		}
	case layoutTree:
		m.tree.cur = clamp(m.tree.cur+d, 0, max(0, len(m.tree.rows)-1))
	case layoutFlat:
		m.flat.move(d)
	}
}

func (m *Model) bottom() tea.Cmd {
	if m.layout == layoutFlat {
		if m.flat.total > len(m.flat.results) {
			m.flat.wantEnd = true
			return m.fetchAll(m.flat)
		}
	}
	m.move(1 << 30)
	return nil
}

func (m *Model) left() {
	switch m.layout {
	case layoutMiller:
		ml := &m.mil
		if ml.col == 2 && ml.family != nil {
			m.milCollapseFamily()
			return
		}
		ml.col = max(m.milFirst(), ml.col-1)
	case layoutTree:
		t := &m.tree
		if t.cur >= len(t.rows) {
			return
		}
		r := t.rows[t.cur]
		if t.isOpen(r.node) {
			t.setOpen(r.node, false)
			m.treeBuild()
			return
		}
		for i := t.cur - 1; i >= 0; i-- { // go to the parent
			if t.rows[i].depth < r.depth {
				t.cur = i
				return
			}
		}
	}
}

func (m *Model) right() tea.Cmd {
	switch m.layout {
	case layoutMiller:
		ml := &m.mil
		if ml.col < 2 && len(ml.cols[ml.col+1]) > 0 {
			ml.col++
			return nil
		}
		if ml.col == 2 {
			return m.open()
		}
	case layoutTree:
		t := &m.tree
		if t.cur >= len(t.rows) {
			return nil
		}
		n := t.rows[t.cur].node
		expandable := n.Kind == api.KindProject || n.Kind == api.KindDataset || n.Kind == api.KindShards
		if expandable && !t.isOpen(n) {
			t.setOpen(n, true)
			m.treeBuild()
			return nil
		}
		if expandable {
			// already open: step into the first child, if there is one (an empty
			// project or dataset has none, and must not recurse through open)
			if t.cur+1 < len(t.rows) && t.rows[t.cur+1].depth > t.rows[t.cur].depth {
				t.cur++
			}
			return nil
		}
		return m.open()
	case layoutFlat:
		return m.open()
	}
	return nil
}

// open is Enter: expand a container or shard family; on a table, record it as
// recently opened and zoom the detail when the pane is collapsed.
func (m *Model) open() tea.Cmd {
	s := m.sel()
	switch s.kind {
	case api.KindProject, api.KindDataset:
		switch m.layout {
		case layoutFlat:
			return m.reveal(s.path, s.kind)
		case layoutTree:
			m.tree.setOpen(s.node, !m.tree.isOpen(s.node))
			m.treeBuild()
			return nil
		}
		if m.mil.col < 2 && len(m.mil.cols[m.mil.col+1]) > 0 {
			m.mil.col++
		}
		return nil
	case api.KindShards:
		switch m.layout {
		case layoutMiller:
			ml := &m.mil
			if ml.col < 2 {
				ml.col = 2
				return nil
			}
			fam := s.node
			ml.family, ml.famIdx = &fam, ml.cur[2]
			ml.cur[2], ml.off[2] = 0, 0
			m.milLoad(2)
		case layoutTree:
			m.tree.setOpen(s.node, !m.tree.isOpen(s.node))
			m.treeBuild()
		case layoutFlat:
			m.flat.in.set("d:" + s.path.Dataset + " p:" + s.path.Project + " " + strings.TrimSuffix(s.path.Table, "YYYYMMDD"))
			m.flat.expand, m.flat.expandShards = true, true
			m.flat.cur, m.flat.off = 0, 0
			return m.runSearch(m.flat)
		}
	case api.KindTable:
		if m.layout == layoutMiller && m.mil.col < 2 {
			m.mil.col = 2
			return nil
		}
		p := s.path
		touch := func() tea.Msg { _ = m.be.TouchRecent(m.ctx, p); return nil }
		if m.effPlacement() == placeHidden {
			m.zoom = true
		}
		return touch
	}
	return nil
}

func (m *Model) setLayout(l layoutKind) {
	s := m.sel()
	m.layout = l
	m.zoom = false
	if s.kind != "" {
		m.locate(s.path, s.kind)
	}
}

// locate moves the current layout's cursor to an object.
func (m *Model) locate(p api.ObjectPath, kind api.ResultKind) {
	switch m.layout {
	case layoutMiller:
		ml := &m.mil
		ml.family = nil
		m.milDropHiding(p, kind)
		m.milLoad(0)
		i := indexOf(ml.cols[0], p.Project)
		if i < 0 {
			return
		}
		ml.cur[0], ml.col = i, 0
		ml.cur[1], ml.cur[2] = 0, 0
		m.milLoad(1)
		if p.Dataset == "" {
			return
		}
		if i = indexOf(ml.cols[1], p.Dataset); i < 0 {
			return
		}
		ml.cur[1], ml.col = i, 1
		ml.cur[2] = 0
		m.milLoad(2)
		if p.Table == "" {
			return
		}
		if i = indexOf(ml.cols[2], p.Table); i >= 0 {
			ml.cur[2], ml.col = i, 2
			return
		}
		// a member of a collapsed shard family
		for fi, n := range ml.cols[2] {
			if n.Kind == api.KindShards && strings.HasPrefix(p.Table, n.Shard.Prefix) {
				fam := n
				ml.family, ml.famIdx = &fam, fi
				ml.cur[2] = 0
				m.milLoad(2)
				if i = indexOf(ml.cols[2], p.Table); i >= 0 {
					ml.cur[2], ml.col = i, 2
					return
				}
				ml.family = nil
				m.milLoad(2)
			}
		}
	case layoutTree:
		t := &m.tree
		if t.filtering() && !m.treeLocateFiltered(p, kind) {
			// the filter hides the target: drop it
			t.f.clear()
			t.fexp = nil
		}
		if t.filtering() {
			return
		}
		pn := api.BrowseNode{Kind: api.KindProject, Path: api.ObjectPath{Project: p.Project}}
		if p.Dataset != "" {
			t.open[openKey(pn)] = true
		}
		dn := api.BrowseNode{Kind: api.KindDataset, Path: api.ObjectPath{Project: p.Project, Dataset: p.Dataset}}
		if p.Table != "" {
			t.open[openKey(dn)] = true
		}
		m.treeBuild()
		find := func() bool {
			for i, r := range t.rows {
				if r.node.Path == p && (r.node.Kind == kind || kind == "") {
					t.cur = i
					return true
				}
			}
			return false
		}
		if find() || p.Table == "" {
			return
		}
		for _, r := range t.rows { // open the family that holds the table
			if r.node.Kind == api.KindShards && r.node.Path.Project == p.Project && r.node.Path.Dataset == p.Dataset && strings.HasPrefix(p.Table, r.node.Shard.Prefix) {
				t.open[openKey(r.node)] = true
			}
		}
		m.treeBuild()
		kind = api.KindTable
		find()
	case layoutFlat:
		m.flat.pending = &p
	}
}

// treeLocateFiltered moves the cursor to an object the tree filter shows,
// opening its ancestors (and its shard family) in the filter's expansion.
func (m *Model) treeLocateFiltered(p api.ObjectPath, kind api.ResultKind) bool {
	t := &m.tree
	find := func(k api.ResultKind) bool {
		for i, r := range t.rows {
			if r.node.Path == p && (r.node.Kind == k || k == "") {
				t.cur = i
				return true
			}
		}
		return false
	}
	if p.Dataset != "" {
		t.setOpen(api.BrowseNode{Kind: api.KindProject, Path: api.ObjectPath{Project: p.Project}}, true)
	}
	if p.Table != "" {
		t.setOpen(api.BrowseNode{Kind: api.KindDataset, Path: api.ObjectPath{Project: p.Project, Dataset: p.Dataset}}, true)
	}
	m.treeBuild()
	if find(kind) {
		return true
	}
	if p.Table == "" {
		return false
	}
	for _, r := range t.rows {
		if r.node.Kind == api.KindShards && r.node.Path.Project == p.Project && r.node.Path.Dataset == p.Dataset && strings.HasPrefix(p.Table, r.node.Shard.Prefix) {
			t.setOpen(r.node, true)
		}
	}
	m.treeBuild()
	return find(api.KindTable)
}

// reveal closes any overlay and shows the object in the explorer.
func (m *Model) reveal(p api.ObjectPath, kind api.ResultKind) tea.Cmd {
	m.ov = ovNone
	if m.layout == layoutFlat {
		// the flat list cannot show containers: fall back to Miller columns
		m.layout = layoutMiller
	}
	// a pasted path can name a project that is turned off
	show := m.showProject(p.Project)
	m.locate(p, kind)
	if kind == api.KindShards && m.layout == layoutMiller {
		if n, ok := m.mil.at(2); ok && n.Kind == api.KindShards {
			fam := n
			m.mil.family, m.mil.famIdx = &fam, m.mil.cur[2]
			m.mil.cur[2] = 0
			m.milLoad(2)
		}
	}
	if kind == api.KindTable {
		return tea.Batch(show, func() tea.Msg { _ = m.be.TouchRecent(m.ctx, p); return nil })
	}
	return show
}

func (m *Model) cycleScope() {
	st := m.flat
	s := st.selection()
	switch {
	case st.scope == nil && s.path.Project != "":
		st.scope = &api.ObjectPath{Project: s.path.Project}
	case st.scope != nil && st.scope.Dataset == "" && s.path.Dataset != "":
		st.scope = &api.ObjectPath{Project: st.scope.Project, Dataset: s.path.Dataset}
	default:
		st.scope = nil
	}
	st.cur, st.off = 0, 0
}
