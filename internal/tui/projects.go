package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/search"
)

// The Projects dialog (ctrl+x p) chooses the Selected projects (what is
// synced and browsed) and the Execution project. Every key applies at once
// through the engine; closing the dialog starts one sync of the projects
// selected during the visit that have no cache or a stale one.

// projDialog is the open dialog.
type projDialog struct {
	// group is each row's group, frozen when the dialog opened (Selected
	// first): toggling a project changes its checkbox, never its place.
	group   map[string]bool
	infos   map[string]api.ProjectInfo
	order   []string // rows: the Selected group A–Z, then the rest A–Z
	loading bool
	listErr string
	added   map[string]bool // selected during this visit
	filter  input
	typing  bool
	sel     int
	auto    bool // opened by itself (nothing selected yet)
}

// projListMsg carries BigQuery's projects list.
type projListMsg struct {
	list []api.ListedProject
	err  error
}

// projects lists the Selected projects (the Explorer's top level).
func (m *Model) projects() []api.BrowseNode { return m.be.Projects() }

// milFirst is the first Miller column shown: with a single project selected,
// the project column is left out.
func (m *Model) milFirst() int {
	// one project selected, not one left by the projects column's filter
	if m.mil.unfiltered[0] == 1 && len(m.mil.cols[0]) == 1 && len(m.mil.cols[1]) > 0 {
		return 1
	}
	return 0
}

// openProjects opens the dialog and fetches BigQuery's projects list unless
// this session has it already.
func (m *Model) openProjects() tea.Cmd {
	m.ov, m.ovOff = ovProjects, 0
	pd := &projDialog{group: map[string]bool{}, added: map[string]bool{}}
	m.pd = pd
	for _, id := range m.be.Selection().IDs() {
		pd.group[id] = true
	}
	m.refreshProjects()
	pd.sel = 0
	if n, ok := m.mil.at(0); ok {
		for i, id := range pd.order {
			if id == n.Name {
				pd.sel = i
			}
		}
	}
	if m.listed != nil {
		return nil
	}
	pd.loading = true
	return m.fetchProjects(false)
}

func (m *Model) fetchProjects(refresh bool) tea.Cmd {
	return func() tea.Msg {
		list, err := m.be.AccessibleProjects(m.ctx, refresh)
		return projListMsg{list, err}
	}
}

func (m *Model) onProjectList(msg projListMsg) {
	if msg.err == nil {
		if msg.list == nil {
			msg.list = []api.ListedProject{}
		}
		m.listed = msg.list
	}
	pd := m.pd
	if pd == nil {
		return
	}
	pd.loading = false
	pd.listErr = ""
	if msg.err != nil {
		pd.listErr = msg.err.Error()
	}
	cur := pd.current()
	m.refreshProjects()
	pd.focus(cur)
}

// refreshProjects reloads the rows' data, keeping their order.
func (m *Model) refreshProjects() {
	pd := m.pd
	if pd == nil {
		return
	}
	infos, err := m.be.ProjectInfos(m.ctx, m.listed)
	if err != nil {
		m.toast = "projects: " + err.Error()
	}
	pd.infos = map[string]api.ProjectInfo{}
	for _, p := range infos {
		pd.infos[p.ID] = p
	}
	pd.order = pd.order[:0]
	for id := range pd.infos {
		pd.order = append(pd.order, id)
	}
	sort.Slice(pd.order, func(i, j int) bool {
		a, b := pd.order[i], pd.order[j]
		if pd.group[a] != pd.group[b] {
			return pd.group[a]
		}
		return a < b
	})
}

// visible are the rows the filter keeps.
func (pd *projDialog) visible() []string {
	q := strings.TrimSpace(pd.filter.value())
	if q == "" {
		return pd.order
	}
	var out []string
	for _, id := range pd.order {
		if search.ProjectMatches(id, pd.infos[id].FriendlyName, q) {
			out = append(out, id)
		}
	}
	return out
}

// addRow is the project ID the "add" row would select: the filter text when
// it matches no row and looks like a project ID.
func (pd *projDialog) addRow() string {
	q := strings.TrimSpace(pd.filter.value())
	if q == "" || len(pd.visible()) > 0 || !config.ValidProjectID(q) {
		return ""
	}
	return q
}

func (pd *projDialog) current() string {
	if id := pd.addRow(); id != "" {
		return ""
	}
	vis := pd.visible()
	if pd.sel >= 0 && pd.sel < len(vis) {
		return vis[pd.sel]
	}
	return ""
}

func (pd *projDialog) focus(id string) {
	for i, v := range pd.visible() {
		if v == id {
			pd.sel = i
			return
		}
	}
	pd.sel = clamp(pd.sel, 0, max(0, len(pd.visible())-1))
}

func (pd *projDialog) rows() int {
	if pd.addRow() != "" {
		return 1
	}
	return len(pd.visible())
}

// execText is the header naming the Execution project and why it is that one.
func execText(p string, src api.ExecutionSource) string {
	why := map[api.ExecutionSource]string{
		api.ExecutionChosen: "chosen", api.ExecutionFirstSelected: "first selected",
		api.ExecutionFlag: "--execution-project", api.ExecutionEnv: "BQT_EXECUTION_PROJECT",
	}[src]
	if p == "" {
		return "none: select a project, or press e on one"
	}
	return p + " (" + why + ")"
}

func (m *Model) projectsLines(w int) []string {
	pd := m.pd
	if pd == nil {
		return nil
	}
	exec, src := m.be.ExecutionProject()
	out := []string{" " + sDim.Render("Execution project: ") + sAcc.Render(execText(exec, src))}
	for _, l := range wrap("space select · o only this · e execution · / filter · r refresh · esc close", w-2) {
		out = append(out, " "+sDim.Render(l))
	}
	sel := 0
	for _, p := range pd.infos {
		if p.Selected {
			sel++
		}
	}
	status := fmt.Sprintf("%d selected", sel)
	switch {
	case pd.loading:
		status += " · loading projects…"
	case pd.listErr != "":
		status += " · could not list your projects: " + pd.listErr
	}
	out = append(out, " "+sDim.Render(status))
	if pd.typing || pd.filter.value() != "" {
		b := newRB(false)
		b.add(" / ", sAcc)
		b.l.WriteString(pd.filter.render(api.ParsedQuery{}, pd.typing, "project ID or name"))
		out = append(out, b.line(w))
	}
	out = append(out, "")
	m.projTop = len(out)
	if id := pd.addRow(); id != "" {
		r := newRB(true)
		r.add(" + ", sAcc).add("add "+id, sBase).addR("not in your projects list ", sDim)
		return append(out, r.line(w))
	}
	vis := pd.visible()
	if len(vis) == 0 {
		msg := "no projects: check your credentials (`bqt doctor`)"
		if pd.filter.value() != "" {
			msg = "no project matches"
		}
		return append(out, " "+sDim.Render(msg))
	}
	pd.sel = clamp(pd.sel, 0, len(vis)-1)
	for i, id := range vis {
		p := pd.infos[id]
		r := newRB(i == pd.sel)
		box, st := "[ ] ", sDim
		if p.Selected {
			box, st = "[x] ", sAcc
		}
		r.add(" "+box, st).add(id, sBase)
		if p.FriendlyName != "" {
			r.add("  "+p.FriendlyName, sDim)
		}
		if right := m.projectState(p); right != "" {
			r.addR(right+" ", sDim)
		}
		out = append(out, r.line(w))
	}
	return out
}

// projectState is the right-hand text of a row.
func (m *Model) projectState(p api.ProjectInfo) string {
	var parts []string
	if !p.Listed {
		parts = append(parts, "not listed")
	}
	if !p.Accessible {
		parts = append(parts, "not accessible")
	}
	switch {
	case m.projectSyncing(p):
		parts = append(parts, "syncing…")
	case p.Sync == api.ProjectFailed:
		parts = append(parts, "sync failed")
	case p.Sync == api.ProjectNotSynced && p.Selected:
		parts = append(parts, "not synced yet")
	}
	if p.SyncedAt != nil {
		parts = append(parts, plural(p.Datasets, "dataset", "datasets"), format.Age(m.o.Now().Sub(*p.SyncedAt))+" ago")
	}
	return strings.Join(parts, " · ")
}

// projectSyncing reports whether this session's running sync covers p.
func (m *Model) projectSyncing(p api.ProjectInfo) bool {
	if !m.syncing {
		return false
	}
	if m.syncScope == nil {
		return p.Selected
	}
	for _, s := range m.syncScope {
		if s == p.ID {
			return true
		}
	}
	return false
}

func (m *Model) projectsKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	pd := m.pd
	switch keys.action(ZoneSearch, name) {
	case "back", "open":
		pd.typing = false
		return nil
	case "down":
		pd.sel = min(pd.sel+1, max(0, pd.rows()-1))
		return nil
	case "up":
		pd.sel = max(0, pd.sel-1)
		return nil
	}
	if pd.filter.handle(msg) {
		pd.sel = 0
	}
	return nil
}

func (m *Model) projectsOverlayAction(act string) tea.Cmd {
	pd := m.pd
	cur := pd.current()
	switch act {
	case "down":
		pd.sel = min(pd.sel+1, max(0, pd.rows()-1))
	case "up":
		pd.sel = max(0, pd.sel-1)
	case "halfdown":
		pd.sel = min(pd.sel+10, max(0, pd.rows()-1))
	case "halfup":
		pd.sel = max(0, pd.sel-10)
	case "filter":
		pd.typing = true
	case "refresh":
		pd.loading = true
		return m.fetchProjects(true)
	case "back":
		if pd.filter.value() != "" {
			pd.filter.set("")
			pd.focus(cur)
			return nil
		}
		return m.closeProjects()
	case "open":
		if id := pd.addRow(); id != "" {
			return m.projectChange(func() error { return m.be.SelectProjects(id) }, id)
		}
		return m.closeProjects()
	case "toggle":
		if id := pd.addRow(); id != "" {
			return m.projectChange(func() error { return m.be.SelectProjects(id) }, id)
		}
		if cur == "" {
			return nil
		}
		if pd.infos[cur].Selected {
			return m.projectChange(func() error { return m.be.DeselectProjects(cur) }, "")
		}
		return m.projectChange(func() error { return m.be.SelectProjects(cur) }, cur)
	case "only":
		if cur == "" {
			return nil
		}
		return m.projectChange(func() error { return m.be.SelectOnly(cur) }, cur)
	case "exec":
		if cur == "" {
			return nil
		}
		if p, _ := m.be.ExecutionProject(); p == cur {
			return nil
		}
		return m.projectChange(func() error { return m.be.ChooseExecution(cur) }, "")
	default:
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

// projectChange applies one change at once and refreshes what depends on
// the selection. added is a project the change selects (synced on close).
func (m *Model) projectChange(change func() error, added string) tea.Cmd {
	pd := m.pd
	cur := pd.current()
	wasSel := added != "" && pd.infos[added].Selected
	execBefore, _ := m.be.ExecutionProject()
	if err := change(); err != nil {
		if errors.Is(err, engine.ErrLastSelected) {
			m.toast = "the last selected project stays selected (o selects only another one)"
		} else {
			m.toast = "projects: " + err.Error()
		}
		return nil
	}
	if added != "" && !wasSel {
		pd.added[added] = true
	}
	m.refreshProjects()
	if cur == "" {
		cur = added
	}
	pd.focus(cur)
	return m.afterSelectionChange(execBefore)
}

// afterSelectionChange reloads the Explorer, searches and status after the
// selection or the Execution project changed (here or in another process).
func (m *Model) afterSelectionChange(execBefore string) tea.Cmd {
	m.reloadBrowse()
	cmds := []tea.Cmd{m.rerunSearches(), m.statusCmd()}
	// the engine forgot the pricing model of the old project: learn the new one
	if exec, _ := m.be.ExecutionProject(); exec != execBefore && !m.o.NoBackground {
		cmds = append(cmds, m.learnPricing())
	}
	return tea.Batch(cmds...)
}

// closeProjects closes the dialog and starts one sync of the projects
// selected during the visit that have no cache or a stale one.
func (m *Model) closeProjects() tea.Cmd {
	pd := m.pd
	m.ov, m.pd = ovNone, nil
	if pd == nil {
		return nil
	}
	if pd.auto && m.toast == "" {
		m.toast = "new to bqt? ctrl+g starts a short tutorial of the navigation"
		m.saveState() // the hint shows once
	}
	var ids []string
	sel := m.be.Selection()
	for id := range pd.added {
		if sel.Has(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return m.syncIfNeeded(ids)
}

// syncIfNeeded starts a sync of the projects among ids that need one.
func (m *Model) syncIfNeeded(ids []string) tea.Cmd {
	if len(ids) == 0 {
		return nil
	}
	need, err := m.be.NeedsSync(m.ctx, ids)
	if err != nil || len(need) == 0 {
		return nil
	}
	if m.syncing {
		m.toast = "a sync is running: " + strings.Join(need, ", ") + " sync with the next refresh (R syncs everything)"
		return nil
	}
	return m.startSync(api.SyncScope{Projects: need}, false)
}

// selectForPath selects p for opening a pasted path in it, syncing it when
// its cache is missing or stale.
func (m *Model) selectForPath(p string) tea.Cmd {
	if p == "" || m.be.Selection().Has(p) {
		return nil
	}
	execBefore, _ := m.be.ExecutionProject()
	if err := m.be.SelectProjects(p); err != nil {
		m.toast = "projects: " + err.Error()
		return nil
	}
	m.toast = "selected project " + p
	return tea.Batch(m.afterSelectionChange(execBefore), m.syncIfNeeded([]string{p}))
}

// selectionKey fingerprints the selection and the Execution project.
func selectionKey(be Backend) string {
	exec, _ := be.ExecutionProject()
	return strings.Join(be.Selection().IDs(), ",") + "|" + exec
}

// selectionChanged applies a selection changed by another process (bqt
// projects in another terminal). Checked once a second.
func (m *Model) selectionChanged() tea.Cmd {
	key := selectionKey(m.be)
	if key == m.selSeen {
		return nil
	}
	before := m.selSeen[strings.LastIndex(m.selSeen, "|")+1:]
	m.selSeen = key
	m.refreshProjects()
	return m.afterSelectionChange(before)
}
