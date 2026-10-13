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
		ml.cols[c] = nodes
		if i := indexOf(nodes, name); i >= 0 {
			ml.cur[c] = i
		} else {
			ml.cur[c] = clamp(ml.cur[c], 0, max(0, len(nodes)-1))
		}
	}
	if c <= 0 {
		keep(0, m.be.Projects())
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
	for ml.col > 0 && len(ml.cols[ml.col]) == 0 {
		ml.col--
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
}

func openKey(n api.BrowseNode) string { return string(n.Kind) + "\x00" + n.Path.String() }

func (m *Model) treeBuild() {
	t := &m.tree
	var keep string
	if t.cur >= 0 && t.cur < len(t.rows) {
		keep = openKey(t.rows[t.cur].node)
	}
	t.rows = t.rows[:0]
	for _, p := range m.be.Projects() {
		t.rows = append(t.rows, treeRow{0, p})
		if !t.open[openKey(p)] {
			continue
		}
		for _, d := range m.be.Datasets(p.Name) {
			t.rows = append(t.rows, treeRow{1, d})
			if !t.open[openKey(d)] {
				continue
			}
			for _, tb := range m.be.Tables(p.Name, d.Name) {
				t.rows = append(t.rows, treeRow{2, tb})
				if tb.Kind == api.KindShards && t.open[openKey(tb)] {
					for _, mem := range m.be.ShardMembers(p.Name, d.Name, tb.Shard.Prefix) {
						t.rows = append(t.rows, treeRow{3, mem})
					}
				}
			}
		}
	}
	if keep != "" {
		for i, r := range t.rows {
			if openKey(r.node) == keep {
				t.cur = i
				break
			}
		}
	}
	t.cur = clamp(t.cur, 0, max(0, len(t.rows)-1))
}

// --- shared navigation

func (m *Model) reloadBrowse() {
	m.milLoad(0)
	m.treeBuild()
}

func (m *Model) rerunSearches() tea.Cmd {
	var cmds []tea.Cmd
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
	case m.ov == ovSearch:
		return m.search
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
		ml.col = max(0, ml.col-1)
	case layoutTree:
		t := &m.tree
		if t.cur >= len(t.rows) {
			return
		}
		r := t.rows[t.cur]
		if t.open[openKey(r.node)] {
			delete(t.open, openKey(r.node))
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
		if expandable && !t.open[openKey(n)] {
			t.open[openKey(n)] = true
			m.treeBuild()
			return nil
		}
		if expandable && t.cur+1 < len(t.rows) && t.rows[t.cur+1].depth > t.rows[t.cur].depth {
			t.cur++
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
		if m.layout == layoutFlat {
			return m.reveal(s.path, s.kind)
		}
		return m.right()
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
			m.tree.open[openKey(s.node)] = !m.tree.open[openKey(s.node)]
			m.treeBuild()
		case layoutFlat:
			m.flat.in.set("d:" + s.path.Dataset + " p:" + s.path.Project + " " + strings.TrimSuffix(s.path.Table, "YYYYMMDD"))
			m.flat.expand = true
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

// reveal closes any overlay and shows the object in the explorer.
func (m *Model) reveal(p api.ObjectPath, kind api.ResultKind) tea.Cmd {
	m.ov = ovNone
	if m.layout == layoutFlat {
		// the flat list cannot show containers: fall back to Miller columns
		m.layout = layoutMiller
	}
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
		return func() tea.Msg { _ = m.be.TouchRecent(m.ctx, p); return nil }
	}
	return nil
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
