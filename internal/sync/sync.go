// Package sync keeps the local catalog in line with BigQuery (spec section 7).
//
// The unit of work is a (project, location) pair. Each unit is fetched with
// region-qualified INFORMATION_SCHEMA views and applied to the catalog in one
// transaction. When the region views are not accessible the unit degrades
// through a ladder: per-dataset views, then table names only, then "keep the
// old data and mark the dataset inaccessible".
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	gosync "sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// ErrNoProjects is returned when there is nothing to sync.
var ErrNoProjects = errors.New("no projects configured: add them to `projects` in the config file")

// Options configures a Syncer.
type Options struct {
	Config  *config.Config
	Meta    bq.Metadata
	Catalog *store.Catalog
	Now     func() time.Time
	// Parallelism bounds concurrent units (default 4).
	Parallelism int
	// Retries is the number of retries for transient errors (default 2).
	Retries int
	// Backoff returns the wait before retry number attempt (1-based).
	Backoff func(attempt int) time.Duration
	Log     *slog.Logger
}

// Syncer runs syncs. It does not take the process lock; the caller must.
type Syncer struct {
	o Options
}

// New returns a Syncer with defaults applied.
func New(o Options) *Syncer {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Parallelism <= 0 {
		o.Parallelism = 4
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = 2
	}
	if o.Backoff == nil {
		o.Backoff = func(attempt int) time.Duration {
			if attempt <= 1 {
				return time.Second
			}
			return 3 * time.Second
		}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Syncer{o: o}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type unitJob struct {
	ref      api.UnitRef
	datasets []api.DatasetMeta
}

type discovery struct {
	project     string
	units       []unitJob
	state       *api.UnitState // set when discovery failed or was partial
	failed      bool
	unreachable []string
}

type run struct {
	s      *Syncer
	emit   func(api.SyncEvent)
	mu     gosync.Mutex
	done   int
	total  int
	states []api.UnitState
	failed map[api.UnitRef]bool
	warns  []string
}

func (r *run) event(e api.SyncEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Time = r.s.o.Now()
	e.UnitsDone, e.UnitsTotal = r.done, r.total
	if r.emit != nil {
		r.emit(e)
	}
}

// finishUnit records a finished unit and emits its event with updated counters.
func (r *run) finishUnit(st api.UnitState, kind api.SyncEventKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	r.states = append(r.states, st)
	if kind == api.EventUnitFailed {
		if r.failed == nil {
			r.failed = map[api.UnitRef]bool{}
		}
		r.failed[st.UnitRef] = true
	}
	if r.emit != nil {
		r.emit(api.SyncEvent{
			Kind: kind, Time: r.s.o.Now(), Project: st.Project, Location: st.Location,
			Status: st.Status, Datasets: st.Datasets, Tables: st.Tables, Error: st.Error,
			UnitsDone: r.done, UnitsTotal: r.total,
		})
	}
}

func (r *run) warn(project, msg string) {
	r.mu.Lock()
	r.warns = append(r.warns, project+": "+msg)
	r.mu.Unlock()
	r.event(api.SyncEvent{Kind: api.EventWarning, Project: project, Message: msg})
}

// Run syncs the scope and emits structured events. It returns early with
// Cancelled set when ctx is cancelled; committed units stay.
func (s *Syncer) Run(ctx context.Context, scope api.SyncScope, emit func(api.SyncEvent)) (api.SyncResult, error) {
	res := api.SyncResult{Started: s.o.Now()}
	projects, full, err := s.resolveScope(scope)
	if err != nil {
		return res, err
	}
	r := &run{s: s, emit: emit}
	r.event(api.SyncEvent{Kind: api.EventRunStarted, Message: fmt.Sprintf("%d project(s)", len(projects))})

	rec, err := ReconcileConfig(ctx, s.o.Catalog, s.o.Config)
	if err != nil {
		return res, fmt.Errorf("reconcile config: %w", err)
	}
	if rec.Pruned.Projects+rec.Pruned.Datasets+rec.Pruned.Tables > 0 {
		r.warn("", fmt.Sprintf("config changed: pruned %d project(s), %d dataset(s), %d table(s) from the catalog", rec.Pruned.Projects, rec.Pruned.Datasets, rec.Pruned.Tables))
	}

	// Phase A: discover datasets and group them into units.
	disc := make([]discovery, len(projects))
	s.parallel(ctx, len(projects), func(i int) { disc[i] = s.discover(ctx, projects[i], r) })
	var jobs []unitJob
	var failed []api.UnitState
	for _, d := range disc {
		jobs = append(jobs, d.units...)
		if d.failed && d.state != nil {
			failed = append(failed, *d.state)
		}
	}
	r.mu.Lock()
	r.total = len(jobs) + len(failed)
	r.mu.Unlock()
	r.event(api.SyncEvent{Kind: api.EventDiscovered, Message: fmt.Sprintf("%d unit(s)", len(jobs))})
	for _, st := range failed {
		r.finishUnit(st, api.EventUnitFailed)
	}

	// Phase B: process units.
	s.parallel(ctx, len(jobs), func(i int) { s.processUnit(ctx, jobs[i], r) })

	res.Finished = s.o.Now()
	res.Cancelled = ctx.Err() != nil
	res.UnitsTotal = r.total
	res.Units = r.states
	sort.Slice(res.Units, func(i, j int) bool {
		a, b := res.Units[i], res.Units[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Location < b.Location
	})
	for _, u := range res.Units {
		switch {
		case r.failed[u.UnitRef]:
			res.UnitsFailed++
		case u.Status == api.UnitOK:
			res.UnitsOK++
		default:
			res.UnitsDegraded++
		}
	}
	res.Warnings = r.warns

	if !res.Cancelled && full {
		cctx := context.WithoutCancel(ctx)
		if err := setSyncNeeded(cctx, s.o.Catalog, false, ""); err != nil {
			return res, err
		}
	}
	r.event(api.SyncEvent{Kind: api.EventRunFinished, Message: fmt.Sprintf("%d ok, %d degraded, %d failed", res.UnitsOK, res.UnitsDegraded, res.UnitsFailed)})
	return res, nil
}

func (s *Syncer) resolveScope(scope api.SyncScope) (projects []string, full bool, err error) {
	configured := s.o.Config.Projects
	if len(configured) == 0 {
		return nil, false, ErrNoProjects
	}
	if len(scope.Projects) == 0 {
		return append([]string(nil), configured...), true, nil
	}
	in := map[string]bool{}
	for _, p := range configured {
		in[p] = true
	}
	for _, p := range scope.Projects {
		if !in[p] {
			return nil, false, fmt.Errorf("project %q is not in the configured `projects`", p)
		}
		projects = append(projects, p)
	}
	return projects, len(projects) == len(configured), nil
}

// parallel runs fn(0..n-1) with the configured bound, stopping to start new
// work once ctx is cancelled.
func (s *Syncer) parallel(ctx context.Context, n int, fn func(i int)) {
	sem := make(chan struct{}, s.o.Parallelism)
	var wg gosync.WaitGroup
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			fn(i)
		}(i)
	}
	wg.Wait()
}

func (s *Syncer) discover(ctx context.Context, project string, r *run) discovery {
	d := discovery{project: project}
	now := s.o.Now()
	list, err := retry(ctx, s, "list datasets "+project, func() (bq.DatasetList, error) {
		return s.o.Meta.ListDatasets(ctx, project)
	})
	if err != nil {
		if ctx.Err() != nil {
			return d
		}
		st := api.UnitState{UnitRef: api.UnitRef{Project: project}, Status: api.UnitInaccessible, Error: errText(err), LastAttempt: &now}
		if rerr := s.o.Catalog.RecordUnitFailure(ctx, st); rerr != nil {
			s.o.Log.Error("record discovery failure", "project", project, "err", rerr)
		}
		d.failed, d.state = true, &st
		return d
	}

	byLoc := map[string][]api.DatasetMeta{}
	for _, ds := range list.Datasets {
		if s.o.Config.Matcher().Ignored(project, ds.Name, "") {
			continue
		}
		byLoc[ds.Location] = append(byLoc[ds.Location], ds)
	}
	locs := make([]string, 0, len(byLoc))
	for l := range byLoc {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	for _, l := range locs {
		ds := byLoc[l]
		sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
		d.units = append(d.units, unitJob{ref: api.UnitRef{Project: project, Location: l}, datasets: ds})
	}

	// Locations the catalog knows but BigQuery no longer lists for this project.
	known, err := s.o.Catalog.Locations(ctx, project)
	if err == nil {
		for _, l := range known {
			if _, ok := byLoc[l]; !ok {
				if err := s.o.Catalog.RemoveUnit(ctx, api.UnitRef{Project: project, Location: l}); err != nil {
					s.o.Log.Error("remove vanished unit", "project", project, "location", l, "err", err)
				}
			}
		}
	}

	if len(list.Unreachable) > 0 {
		msg := fmt.Sprintf("BigQuery could not enumerate: %v", list.Unreachable)
		r.warn(project, msg)
		st := api.UnitState{UnitRef: api.UnitRef{Project: project}, Status: api.UnitDegraded, Error: msg, LastAttempt: &now}
		if rerr := s.o.Catalog.RecordUnitFailure(ctx, st); rerr != nil {
			s.o.Log.Error("record unreachable", "project", project, "err", rerr)
		}
		d.unreachable = list.Unreachable
	} else if err := s.o.Catalog.RemoveUnit(ctx, api.UnitRef{Project: project}); err != nil {
		s.o.Log.Error("clear discovery state", "project", project, "err", err)
	}
	return d
}

func (s *Syncer) processUnit(ctx context.Context, job unitJob, r *run) {
	now := s.o.Now()
	names := make([]string, len(job.datasets))
	for i, d := range job.datasets {
		names[i] = d.Name
	}
	r.event(api.SyncEvent{Kind: api.EventUnitStarted, Project: job.ref.Project, Location: job.ref.Location, Datasets: len(names)})

	snap, err := retry(ctx, s, "region snapshot "+job.ref.Project+"/"+job.ref.Location, func() (*api.Snapshot, error) {
		return s.o.Meta.RegionSnapshot(ctx, bq.RegionRequest{Project: job.ref.Project, Location: job.ref.Location, Datasets: names})
	})

	var applies []store.DatasetApply
	switch {
	case err == nil:
		applies = s.fromSnapshot(job, snap)
	case ctx.Err() != nil:
		return
	case bq.KindOf(err) == bq.KindPermission:
		s.o.Log.Info("region views not accessible, falling back to per-dataset", "project", job.ref.Project, "location", job.ref.Location, "err", err)
		applies = s.ladder(ctx, job)
		if ctx.Err() != nil {
			return
		}
	default:
		s.failUnit(ctx, job, now, err, r)
		return
	}

	status, summary := summarize(applies)
	var refreshed bool
	for _, a := range applies {
		if a.Mode != store.ModeKeepOld {
			refreshed = true
		}
	}
	if len(applies) == 0 {
		refreshed = true
	}
	state := api.UnitState{UnitRef: job.ref, Status: status, Error: summary, LastAttempt: &now}
	if refreshed {
		state.LastSuccess = &now
	}
	if _, err := s.o.Catalog.ApplyUnit(ctx, store.ApplyRequest{Unit: job.ref, Datasets: applies, State: state}); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.failUnit(ctx, job, now, fmt.Errorf("store: %w", err), r)
		return
	}
	state.Datasets = len(applies)
	for _, a := range applies {
		state.Tables += len(a.Tables)
		switch a.Status {
		case api.DatasetNamesOnly:
			state.NamesOnly++
		case api.DatasetInaccessible:
			state.Inaccessible++
		}
	}
	kind := api.EventUnitDone
	if status == api.UnitInaccessible {
		kind = api.EventUnitFailed
	}
	r.finishUnit(state, kind)
}

// failUnit records a whole-unit failure, keeping the old data.
func (s *Syncer) failUnit(ctx context.Context, job unitJob, now time.Time, err error, r *run) {
	status := api.UnitInaccessible
	var lastSuccess *time.Time
	if units, uerr := s.o.Catalog.Units(ctx); uerr == nil {
		for _, u := range units {
			if u.Project == job.ref.Project && u.Location == job.ref.Location && u.LastSuccess != nil {
				status = api.UnitDegraded // stale but still holds data
				lastSuccess = u.LastSuccess
			}
		}
	}
	st := api.UnitState{UnitRef: job.ref, Status: status, Error: errText(err), LastAttempt: &now, LastSuccess: lastSuccess, Datasets: len(job.datasets)}
	if rerr := s.o.Catalog.RecordUnitFailure(ctx, st); rerr != nil {
		s.o.Log.Error("record unit failure", "project", job.ref.Project, "location", job.ref.Location, "err", rerr)
	}
	r.finishUnit(st, api.EventUnitFailed)
}

// fromSnapshot turns a full region snapshot into dataset applies, dropping
// datasets that were not requested (region views can leak deleted and
// temporary datasets) and tables the ignore rules exclude.
func (s *Syncer) fromSnapshot(job unitJob, snap *api.Snapshot) []store.DatasetApply {
	meta := map[string]api.DatasetMeta{}
	for _, d := range job.datasets {
		meta[d.Name] = d
	}
	for _, d := range snap.Datasets {
		if base, ok := meta[d.Name]; ok {
			if d.Description != "" {
				base.Description = d.Description
			}
			if d.FriendlyName != "" {
				base.FriendlyName = d.FriendlyName
			}
			if len(d.Labels) > 0 {
				base.Labels = d.Labels
			}
			meta[d.Name] = base
		}
	}
	byDS := map[string][]api.TableMeta{}
	for _, t := range snap.Tables {
		if _, ok := meta[t.Dataset]; !ok {
			continue
		}
		if s.o.Config.Matcher().Ignored(job.ref.Project, t.Dataset, t.Name) {
			continue
		}
		byDS[t.Dataset] = append(byDS[t.Dataset], t)
	}
	applies := make([]store.DatasetApply, 0, len(job.datasets))
	for _, d := range job.datasets {
		tables := byDS[d.Name]
		sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
		AssignShards(tables)
		m := meta[d.Name]
		m.Project = job.ref.Project
		m.Location = job.ref.Location
		applies = append(applies, store.DatasetApply{Meta: m, Mode: store.ModeReplace, Status: api.DatasetOK, Tables: tables})
	}
	return applies
}

// ladder handles a unit whose region views are not accessible: per dataset it
// tries dataset-scope views, then table names only, then keeps old data.
func (s *Syncer) ladder(ctx context.Context, job unitJob) []store.DatasetApply {
	out := make([]store.DatasetApply, len(job.datasets))
	keep := make([]bool, len(job.datasets))
	s.parallel(ctx, len(job.datasets), func(i int) {
		a, ok := s.fallbackDataset(ctx, job, job.datasets[i])
		out[i], keep[i] = a, ok
	})
	res := out[:0]
	for i, a := range out {
		if keep[i] {
			res = append(res, a)
		}
	}
	return res
}

func (s *Syncer) fallbackDataset(ctx context.Context, job unitJob, d api.DatasetMeta) (store.DatasetApply, bool) {
	proj := job.ref.Project
	m := d
	m.Project, m.Location = proj, job.ref.Location
	keepOld := func(why error) (store.DatasetApply, bool) {
		s.o.Log.Warn("dataset could not be refreshed", "project", proj, "dataset", d.Name, "err", why)
		return store.DatasetApply{Meta: m, Mode: store.ModeKeepOld, Status: api.DatasetInaccessible}, true
	}

	snap, err := retry(ctx, s, "dataset snapshot "+proj+"."+d.Name, func() (*api.Snapshot, error) {
		return s.o.Meta.DatasetSnapshot(ctx, bq.DatasetRequest{Project: proj, Location: job.ref.Location, Dataset: d.Name})
	})
	if err == nil {
		sub := s.fromSnapshot(unitJob{ref: job.ref, datasets: []api.DatasetMeta{d}}, snap)
		if len(sub) == 1 {
			return sub[0], true
		}
		return keepOld(errors.New("empty dataset snapshot"))
	}
	if ctx.Err() != nil {
		return store.DatasetApply{}, false
	}
	switch bq.KindOf(err) {
	case bq.KindNotFound:
		return store.DatasetApply{}, false // deleted since datasets.list
	case bq.KindPermission:
	default:
		return keepOld(err)
	}

	names, nerr := retry(ctx, s, "list tables "+proj+"."+d.Name, func() ([]api.TableMeta, error) {
		return s.o.Meta.ListTableNames(ctx, proj, d.Name)
	})
	if nerr == nil {
		var kept []api.TableMeta
		for _, t := range names {
			if s.o.Config.Matcher().Ignored(proj, d.Name, t.Name) {
				continue
			}
			t.Dataset, t.NamesOnly, t.Columns = d.Name, true, nil
			kept = append(kept, t)
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
		AssignShards(kept)
		return store.DatasetApply{Meta: m, Mode: store.ModeNamesOnly, Status: api.DatasetNamesOnly, Tables: kept}, true
	}
	if ctx.Err() != nil {
		return store.DatasetApply{}, false
	}
	if bq.KindOf(nerr) == bq.KindNotFound {
		return store.DatasetApply{}, false
	}
	return keepOld(nerr)
}

// summarize derives the unit status and a short error summary.
func summarize(applies []store.DatasetApply) (api.UnitStatus, string) {
	var namesOnly, inaccessible []string
	for _, a := range applies {
		switch a.Status {
		case api.DatasetNamesOnly:
			namesOnly = append(namesOnly, a.Meta.Name)
		case api.DatasetInaccessible:
			inaccessible = append(inaccessible, a.Meta.Name)
		}
	}
	if len(namesOnly)+len(inaccessible) == 0 {
		return api.UnitOK, ""
	}
	msg := ""
	if len(inaccessible) > 0 {
		msg += fmt.Sprintf("%d of %d datasets could not be read: %s", len(inaccessible), len(applies), sample(inaccessible))
	}
	if len(namesOnly) > 0 {
		if msg != "" {
			msg += "; "
		}
		msg += fmt.Sprintf("%d datasets have table names only (no metadata access): %s", len(namesOnly), sample(namesOnly))
	}
	if len(inaccessible) == len(applies) {
		return api.UnitInaccessible, msg
	}
	return api.UnitDegraded, msg
}

func sample(names []string) string {
	sort.Strings(names)
	if len(names) <= 5 {
		return joinNames(names)
	}
	return joinNames(names[:5]) + fmt.Sprintf(" and %d more", len(names)-5)
}

func joinNames(n []string) string {
	out := ""
	for i, s := range n {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func errText(err error) string {
	var be *bq.Error
	if errors.As(err, &be) {
		return fmt.Sprintf("%s: %v", be.Kind, be.Err)
	}
	return err.Error()
}

// retry runs fn, retrying transient failures with backoff.
func retry[T any](ctx context.Context, s *Syncer, op string, fn func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil || !bq.Retryable(err) || attempt >= s.o.Retries {
			return zero, bq.Wrap(op, err)
		}
		wait := s.o.Backoff(attempt + 1)
		s.o.Log.Warn("transient error, retrying", "op", op, "attempt", attempt+1, "wait", wait, "err", err)
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return zero, bq.Wrap(op, ctx.Err())
			}
		}
	}
}
