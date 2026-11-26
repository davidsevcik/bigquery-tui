package engine

import (
	"context"
	"sort"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

type syncTrack struct {
	projects   map[string]bool
	discovered bool
	queued     map[api.UnitRef]bool
}

func newSyncTrack(projects []string) *syncTrack {
	t := &syncTrack{projects: map[string]bool{}, queued: map[api.UnitRef]bool{}}
	for _, p := range projects {
		t.projects[p] = true
	}
	return t
}

func (t *syncTrack) observe(ev api.SyncEvent) {
	switch ev.Kind {
	case api.EventDiscovered:
		t.discovered = true
		for _, u := range ev.Units {
			t.queued[u] = true
		}
	case api.EventUnitFailed:
		delete(t.queued, api.UnitRef{Project: ev.Project, Location: ev.Location})
	}
}

func (t *syncTrack) syncing(u api.UnitRef) bool {
	if !t.discovered {
		return t.projects[u.Project]
	}
	return t.queued[u]
}

func (e *Engine) startTrack(projects []string) *syncTrack {
	t := newSyncTrack(projects)
	e.trackMu.Lock()
	e.track = t
	e.trackMu.Unlock()
	return t
}

func (e *Engine) observeSync(t *syncTrack, ev api.SyncEvent) {
	e.trackMu.Lock()
	t.observe(ev)
	e.trackMu.Unlock()
}

func (e *Engine) endTrack(t *syncTrack) {
	e.trackMu.Lock()
	if e.track == t {
		e.track = nil
	}
	e.trackMu.Unlock()
}

func (e *Engine) unitSyncing(u api.UnitRef) bool {
	e.trackMu.Lock()
	defer e.trackMu.Unlock()
	return e.track != nil && e.track.syncing(u)
}

func (e *Engine) markSyncing(nodes []api.BrowseNode) []api.BrowseNode {
	for i, n := range nodes {
		if n.Kind == api.KindDataset && n.Status == api.DatasetPending {
			nodes[i].Syncing = e.unitSyncing(api.UnitRef{Project: n.Path.Project, Location: n.Location})
		}
	}
	return nodes
}

func (e *Engine) pendingUnits(ctx context.Context) (map[api.UnitRef]int, error) {
	if e.catalog == nil {
		return nil, nil
	}
	return e.catalog.PendingUnits(ctx)
}

func withPending(units []api.UnitState, pending map[api.UnitRef]int) []api.UnitState {
	if len(pending) == 0 {
		return units
	}
	has := map[string]bool{}
	for ref := range pending {
		has[ref.Project] = true
	}
	seen := map[api.UnitRef]bool{}
	out := make([]api.UnitState, 0, len(units)+len(pending))
	for _, u := range units {
		seen[u.UnitRef] = true
		if u.Location == "" && has[u.Project] {
			u.LastSuccess = nil
		}
		out = append(out, u)
	}
	var extra []api.UnitRef
	for ref := range pending {
		if !seen[ref] {
			extra = append(extra, ref)
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		if extra[i].Project != extra[j].Project {
			return extra[i].Project < extra[j].Project
		}
		return extra[i].Location < extra[j].Location
	})
	for _, ref := range extra {
		out = append(out, api.UnitState{UnitRef: ref})
	}
	return out
}
