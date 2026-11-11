package engine

import (
	"context"
	"sort"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// A pending dataset is one BigQuery lists whose unit has never committed it
// (api.DatasetPending). What users see is derived: "syncing" while this
// process's sync is reading (or will read) its unit, "not synced" otherwise.
// Sync events reach only the process that runs the sync, so any other
// process shows every pending dataset as not synced.

// syncTrack follows this process's running sync.
type syncTrack struct {
	projects   map[string]bool // the run's projects, until discovery is done
	discovered bool
	// queued holds the discovered units that have not failed. A committed
	// unit stays: its datasets are pending in the search index until it
	// reloads, and they are syncing, not "not synced", until then.
	queued map[api.UnitRef]bool
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
		return t.projects[u.Project] // still listing: every unit may come
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

// unitSyncing reports whether this process's sync is reading, or will read,
// the unit.
func (e *Engine) unitSyncing(u api.UnitRef) bool {
	e.trackMu.Lock()
	defer e.trackMu.Unlock()
	return e.track != nil && e.track.syncing(u)
}

// markSyncing sets Syncing on the pending dataset nodes whose unit is in
// flight.
func (e *Engine) markSyncing(nodes []api.BrowseNode) []api.BrowseNode {
	for i, n := range nodes {
		if n.Kind == api.KindDataset && n.Status == api.DatasetPending {
			nodes[i].Syncing = e.unitSyncing(api.UnitRef{Project: n.Path.Project, Location: n.Location})
		}
	}
	return nodes
}

// pendingUnits counts the catalog's pending datasets per unit.
func (e *Engine) pendingUnits(ctx context.Context) (map[api.UnitRef]int, error) {
	if e.catalog == nil {
		return nil, nil
	}
	return e.catalog.PendingUnits(ctx)
}

// withPending folds the pending datasets into units for freshness (spec
// 7.2): a location that holds pending datasets but no unit state counts as a
// unit that never succeeded, and while a project has a pending dataset its
// discovery unit's success does not count.
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
