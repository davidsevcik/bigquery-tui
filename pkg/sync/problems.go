package sync

import (
	"sort"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

type Problem struct {
	Project   string
	Location  string
	Unit      *api.UnitState
	NotSynced []string
}

type Problems struct {
	List     []Problem
	Warnings []string
}

func (p Problems) Count() int { return len(p.List) + len(p.Warnings) }

func unitProblem(u api.UnitState) bool {
	return u.Status != api.UnitOK || u.NamesOnly > 0 || u.Inaccessible > 0
}

func notSynced(datasets []api.BrowseNode) map[string][]string {
	out := map[string][]string{}
	for _, d := range datasets {
		if d.Status == api.DatasetPending && !d.Syncing {
			out[d.Location] = append(out[d.Location], d.Name)
		}
	}
	return out
}

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
		delete(pending, u.Location)
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
