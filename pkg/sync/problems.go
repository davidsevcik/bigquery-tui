package sync

import (
	"sort"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

// Problem is one sync problem of a Selected project: a unit that failed,
// is degraded or could read some datasets only by name, or a location whose
// Pending datasets no sync is reading ("not synced") without such a unit.
type Problem struct {
	Project  string
	Location string
	// Unit is the unit with the problem; nil for a location that is only
	// not synced (the sync stopped before it reached the unit).
	Unit *api.UnitState
	// NotSynced are the location's Pending datasets that no sync is reading.
	NotSynced []string
}

// Problems are what a frontend lists as sync problems.
type Problems struct {
	// List holds each Selected project's unit problems in unit order, then
	// its locations that are only not synced, A to Z.
	List []Problem
	// Warnings are the last sync's warnings that no unit shows already (an
	// incomplete dataset listing is both a warning and its project's unit
	// error).
	Warnings []string
}

// Count is how many problems there are.
func (p Problems) Count() int { return len(p.List) + len(p.Warnings) }

// unitProblem reports whether a unit counts as a sync problem: it is not
// OK, or some of its datasets were read by name only or not at all.
func unitProblem(u api.UnitState) bool {
	return u.Status != api.UnitOK || u.NamesOnly > 0 || u.Inaccessible > 0
}

// notSynced groups a project's Pending datasets that no sync is reading by
// location, from its datasets as the browse index lists them.
func notSynced(datasets []api.BrowseNode) map[string][]string {
	out := map[string][]string{}
	for _, d := range datasets {
		if d.Status == api.DatasetPending && !d.Syncing {
			out[d.Location] = append(out[d.Location], d.Name)
		}
	}
	return out
}

// FindProblems lists the sync problems from the catalog status (nil while
// unknown), each project's datasets and the last sync's warnings. A
// deselected project is not refreshed, so its problems are not listed.
func FindProblems(st *api.CatalogStatus, datasets func(project string) []api.BrowseNode, warnings []string) Problems {
	var out Problems
	shown := map[string]bool{}
	if st != nil {
		for _, p := range st.Projects {
			for _, u := range p.Units {
				if u.Error != "" {
					shown[p.Project+": "+u.Error] = true
				}
			}
			if p.Selected {
				out.List = append(out.List, projectProblems(p, datasets(p.Project))...)
			}
		}
	}
	for _, w := range warnings {
		if !shown[w] {
			out.Warnings = append(out.Warnings, w)
		}
	}
	return out
}

func projectProblems(p api.ProjectStatus, datasets []api.BrowseNode) []Problem {
	var out []Problem
	pending := notSynced(datasets)
	for _, u := range p.Units {
		if !unitProblem(u) {
			continue
		}
		unit := u
		out = append(out, Problem{Project: p.Project, Location: u.Location, Unit: &unit, NotSynced: pending[u.Location]})
		delete(pending, u.Location) // listed under the unit
	}
	locs := make([]string, 0, len(pending))
	for l := range pending {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	for _, l := range locs {
		out = append(out, Problem{Project: p.Project, Location: l, NotSynced: pending[l]})
	}
	return out
}
