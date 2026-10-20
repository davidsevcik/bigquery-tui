package tui

import (
	"fmt"
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Projects can be turned off in the TUI (ctrl+x p): the explorer, search and
// jump leave them out. The catalog still syncs them. The set is kept in the
// TUI state file, so a project added to the config later starts on.

// projects lists the projects that are turned on. When every project is off
// (the last one on was removed from the config) all of them show.
func (m *Model) projects() []api.BrowseNode {
	all := m.be.Projects()
	var on []api.BrowseNode
	for _, p := range all {
		if !m.hidden[p.Name] {
			on = append(on, p)
		}
	}
	if len(on) == 0 {
		return all
	}
	return on
}

// hiddenProjects is the search filter matching projects().
func (m *Model) hiddenProjects() []string {
	all := m.be.Projects()
	var out []string
	for _, p := range all {
		if m.hidden[p.Name] {
			out = append(out, p.Name)
		}
	}
	if len(out) == len(all) {
		return nil
	}
	return out
}

func (m *Model) setHidden(hidden map[string]bool) tea.Cmd {
	m.hidden = hidden
	m.saveState()
	m.reloadBrowse()
	return m.rerunSearches()
}

// milFirst is the first Miller column shown: with a single project on, the
// project column is left out.
func (m *Model) milFirst() int {
	// one project on, not one left by the projects column's filter
	if m.mil.all[0] == 1 && len(m.mil.cols[0]) == 1 && len(m.mil.cols[1]) > 0 {
		return 1
	}
	return 0
}

func (m *Model) openProjects() {
	m.ov, m.ovSel, m.ovOff = ovProjects, 0, 0
	for i, p := range m.be.Projects() {
		if n, ok := m.mil.at(0); ok && n.Name == p.Name {
			m.ovSel = i
		}
	}
}

func (m *Model) projectsLines(w int) []string {
	all := m.be.Projects()
	m.ovSel = clamp(m.ovSel, 0, max(0, len(all)-1))
	var out []string
	for _, l := range wrap("space turns a project on or off · o shows only it · a shows all · esc closes", w-2) {
		out = append(out, " "+sDim.Render(l))
	}
	out = append(out, "")
	m.projTop = len(out)
	if len(all) == 0 {
		return append(out, " "+sDim.Render("no projects: add them to projects in the config (g c)"))
	}
	shown := m.projects()
	on := map[string]bool{}
	for _, p := range shown {
		on[p.Name] = true
	}
	for i, p := range all {
		r := newRB(i == m.ovSel)
		box, st := "[ ] ", sDim
		if on[p.Name] {
			box, st = "[x] ", sAcc
		}
		r.add(" "+box, st).add(p.Name, sBase)
		if p.Children > 0 {
			unit := " datasets "
			if p.Children == 1 {
				unit = " dataset "
			}
			r.addR(fmt.Sprint(p.Children)+unit, sDim)
		}
		out = append(out, r.line(w))
	}
	return out
}

func (m *Model) projectsOverlayAction(act string) tea.Cmd {
	all := m.be.Projects()
	cur := ""
	if m.ovSel < len(all) {
		cur = all[m.ovSel].Name
	}
	switch act {
	case "down":
		m.ovSel = min(m.ovSel+1, max(0, len(all)-1))
	case "up":
		m.ovSel = max(0, m.ovSel-1)
	case "back", "open":
		m.ov = ovNone
	case "toggle", "only", "all":
		if cur == "" {
			return nil
		}
		h, ok := nextHidden(all, m.hidden, cur, act)
		if !ok {
			m.toast = "at least one project stays on (o shows only this one)"
			return nil
		}
		return m.setHidden(h)
	default:
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

// nextHidden applies a dialog action ("toggle" or "only" on cur, or "all")
// to the projects that are on, as projects() shows them. Entries for
// projects missing from the config are kept, so they come back off. ok is
// false when the action would turn off the last project on.
func nextHidden(all []api.BrowseNode, hidden map[string]bool, cur, act string) (map[string]bool, bool) {
	present := map[string]bool{}
	on := map[string]bool{}
	for _, p := range all {
		present[p.Name] = true
		if !hidden[p.Name] {
			on[p.Name] = true
		}
	}
	if len(on) == 0 { // every project shows when none is on
		for _, p := range all {
			on[p.Name] = true
		}
	}
	switch act {
	case "toggle":
		if on[cur] {
			delete(on, cur)
		} else {
			on[cur] = true
		}
	case "only":
		on = map[string]bool{cur: true}
	case "all":
		on = present
	}
	if len(on) == 0 {
		return nil, false
	}
	out := map[string]bool{}
	for k, v := range hidden {
		if v && !present[k] {
			out[k] = true
		}
	}
	for _, p := range all {
		if !on[p.Name] {
			out[p.Name] = true
		}
	}
	return out, true
}

// projectsInfo is the status line note naming the projects shown: the one
// project when only one is on, otherwise "N of M" while some are off.
func (m *Model) projectsInfo() string {
	on := m.projects()
	if len(on) == 1 {
		return on[0].Name
	}
	hid := m.hiddenProjects()
	if len(hid) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d projects", len(on), len(on)+len(hid))
}

// showProject turns p on if it is off, for opening a pasted path in it.
func (m *Model) showProject(p string) tea.Cmd {
	if !m.hidden[p] {
		return nil
	}
	h := map[string]bool{}
	for k, v := range m.hidden {
		if v && k != p {
			h[k] = true
		}
	}
	m.toast = "turned project " + p + " on"
	return m.setHidden(h)
}

func sortedKeys(s map[string]bool) []string {
	var out []string
	for k, v := range s {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
