package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/selection"
	catsync "github.com/davidsevcik/bigquery-tui/internal/sync"
)

// The Selected projects and the Execution project (the Projects dialog and
// `bqt projects`). They live in the selection state file, shared by every
// frontend and process, not in the config.

// ErrLastSelected is returned when a change would deselect the last Selected
// project.
var ErrLastSelected = selection.ErrLastSelected

// ErrNoExecution is returned when a job needs an Execution project and there
// is none.
var ErrNoExecution = errors.New("no Execution project: select a project in Projects (ctrl+x p) or with `bqt projects select`")

// ErrNotSelected is returned (as a *NotSelectedError) for a sync of a
// project that is neither selected nor cached.
var ErrNotSelected = errors.New("project is not selected")

// NotSelectedError names the project of an ErrNotSelected.
type NotSelectedError struct{ Project string }

func (e *NotSelectedError) Error() string {
	return "project " + e.Project + " is not selected (select it first: `bqt projects select " + e.Project + "`)"
}

func (e *NotSelectedError) Is(target error) bool { return target == ErrNotSelected }

func selectionPath(dataDir string) string {
	return filepath.Join(dataDir, "state", "projects.json")
}

// Selection returns the saved selection. A state file that cannot be read is
// logged and treated as empty.
func (e *Engine) Selection() selection.State {
	s, err := e.sel.Load()
	if err != nil {
		e.o.Log.Warn("read the project selection", "err", err)
	}
	return s
}

func (e *Engine) selected() []string { return e.Selection().IDs() }

func validIDs(ids []string) error {
	for _, id := range ids {
		if !config.ValidProjectID(id) {
			return fmt.Errorf("%q is not a valid project ID", id)
		}
	}
	return nil
}

func (e *Engine) updateSelection(fn func(*selection.State) error) error {
	_, err := e.sel.Update(fn)
	if err == nil {
		e.noticeExecution()
	}
	return err
}

// SelectProjects selects projects. A project already selected keeps its
// selection time (which decides the default Execution project). Nothing is
// synced here.
func (e *Engine) SelectProjects(ids ...string) error {
	if err := validIDs(ids); err != nil {
		return err
	}
	now := e.o.Now()
	return e.updateSelection(func(s *selection.State) error {
		s.Select(now, ids...)
		return nil
	})
}

// DeselectProjects deselects projects. Their cache stays. The last Selected
// project cannot be deselected (ErrLastSelected).
func (e *Engine) DeselectProjects(ids ...string) error {
	return e.updateSelection(func(s *selection.State) error { return s.Deselect(ids...) })
}

// SelectOnly leaves id as the one Selected project.
func (e *Engine) SelectOnly(id string) error {
	if err := validIDs([]string{id}); err != nil {
		return err
	}
	now := e.o.Now()
	return e.updateSelection(func(s *selection.State) error {
		s.Only(id, now)
		return nil
	})
}

// ChooseExecution makes id the Execution project. A chosen project can be
// replaced but not cleared.
func (e *Engine) ChooseExecution(id string) error {
	if err := validIDs([]string{id}); err != nil {
		return err
	}
	return e.updateSelection(func(s *selection.State) error {
		s.Execution = id
		return nil
	})
}

// ExecutionProject returns the effective Execution project and where it comes
// from: flag > environment > chosen > earliest Selected project > none.
func (e *Engine) ExecutionProject() (string, api.ExecutionSource) {
	if p := e.o.Execution; p != nil {
		return p.Project, p.Source
	}
	return resolveExecution(e.Config(), e.Selection())
}

// PinnedExecution is an Execution project fixed for an engine's lifetime.
type PinnedExecution struct {
	Project string
	Source  api.ExecutionSource
}

// ResolveExecution reads the effective Execution project for cfg without an
// engine (bqt mcp resolves it once, at startup).
func ResolveExecution(cfg *config.Config) (PinnedExecution, error) {
	s, err := selection.Open(selectionPath(cfg.DataDir)).Load()
	p, src := resolveExecution(cfg, s)
	return PinnedExecution{Project: p, Source: src}, err
}

func resolveExecution(cfg *config.Config, s selection.State) (string, api.ExecutionSource) {
	switch {
	case cfg.ExecutionOverride != "" && cfg.ExecutionOverrideFrom == "flag":
		return cfg.ExecutionOverride, api.ExecutionFlag
	case cfg.ExecutionOverride != "":
		return cfg.ExecutionOverride, api.ExecutionEnv
	}
	if s.Execution != "" {
		return s.Execution, api.ExecutionChosen
	}
	if p := s.Earliest(); p != "" {
		return p, api.ExecutionFirstSelected
	}
	return "", api.ExecutionNone
}

func (e *Engine) execProject() string {
	p, _ := e.ExecutionProject()
	return p
}

// noticeExecution forgets the learned pricing model when the effective
// Execution project changed.
func (e *Engine) noticeExecution() {
	p := e.execProject()
	e.execMu.Lock()
	changed := e.lastExec != p
	e.lastExec = p
	e.execMu.Unlock()
	if changed {
		e.runs.ForgetPricing()
	}
}

// checkScope allows an explicit sync of Selected projects and of cached
// deselected ones (refreshing a cache does not select the project).
func (e *Engine) checkScope(ctx context.Context, scope api.SyncScope, selected []string) error {
	if len(scope.Projects) == 0 {
		return nil
	}
	isSel := map[string]bool{}
	for _, p := range selected {
		isSel[p] = true
	}
	cached, err := e.cachedProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range scope.Projects {
		if _, ok := cached[p]; !ok && !isSel[p] {
			return &NotSelectedError{Project: p}
		}
	}
	return nil
}

// cachedProjects are the projects the catalog holds data or sync state for.
func (e *Engine) cachedProjects(ctx context.Context) (map[string][]api.UnitState, error) {
	out := map[string][]api.UnitState{}
	for _, n := range e.search.Projects() {
		out[n.Name] = nil
	}
	if e.catalog == nil {
		return out, nil
	}
	units, err := e.catalog.Units(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		out[u.Project] = append(out[u.Project], u)
	}
	return out, nil
}

// NeedsSync returns the projects among ids with no cache, or a cache older
// than sync_ttl.
func (e *Engine) NeedsSync(ctx context.Context, ids []string) ([]string, error) {
	cached, err := e.cachedProjects(ctx)
	if err != nil {
		return nil, err
	}
	now, ttl := e.o.Now(), e.Config().SyncTTL
	var out []string
	for _, id := range ids {
		if at, ok := syncedAt(cached[id]); !ok || now.Sub(at) > ttl {
			out = append(out, id)
		}
	}
	return out, nil
}

// syncedAt is a project's oldest successful unit sync; ok is false when a
// unit never succeeded or there is none.
func syncedAt(units []api.UnitState) (time.Time, bool) {
	var oldest time.Time
	known := false
	for _, u := range units {
		if u.Location == "" {
			continue // discovery state carries no data of its own
		}
		if u.LastSuccess == nil {
			return time.Time{}, false
		}
		if !known || u.LastSuccess.Before(oldest) {
			oldest = *u.LastSuccess
		}
		known = true
	}
	return oldest, known
}

// lastAttemptFailed reports whether a unit's latest sync attempt failed: it
// never succeeded, or it failed after an earlier success (the cache stays).
func lastAttemptFailed(u api.UnitState) bool {
	if u.LastAttempt == nil || u.Status == api.UnitOK {
		return false
	}
	return u.LastSuccess == nil || u.LastAttempt.After(*u.LastSuccess)
}

// AccessibleProjects lists the projects BigQuery returns for the credentials,
// sorted by ID. The list is kept for the engine's lifetime; refresh fetches
// it again.
func (e *Engine) AccessibleProjects(ctx context.Context, refresh bool) ([]api.ListedProject, error) {
	e.mu.Lock()
	list, ok := e.listed, e.listedOK
	e.mu.Unlock()
	if ok && !refresh {
		return list, nil
	}
	md, err := e.lister(ctx)
	if err != nil {
		return nil, err
	}
	list, err = md.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	e.mu.Lock()
	e.listed, e.listedOK = list, true
	e.mu.Unlock()
	return list, nil
}

// ProjectInfos merges the listed projects (nil when no list could be
// fetched: every project then counts as listed), the Selected ones and the
// cached ones, sorted by ID.
func (e *Engine) ProjectInfos(ctx context.Context, listed []api.ListedProject) ([]api.ProjectInfo, error) {
	cached, err := e.cachedProjects(ctx)
	if err != nil {
		return nil, err
	}
	sel := e.Selection()
	byID := map[string]*api.ProjectInfo{}
	add := func(id string) *api.ProjectInfo {
		if p := byID[id]; p != nil {
			return p
		}
		p := &api.ProjectInfo{ID: id, Listed: listed == nil, Accessible: true, Sync: api.ProjectNotSynced}
		byID[id] = p
		return p
	}
	for _, l := range listed {
		p := add(l.ID)
		p.Listed = true
		if l.FriendlyName != l.ID { // often just the ID again
			p.FriendlyName = l.FriendlyName
		}
	}
	for _, id := range sel.IDs() {
		add(id).Selected = true
	}
	children := map[string]int{}
	for _, n := range e.search.Projects() {
		children[n.Name] = n.Children
	}
	for id, units := range cached {
		p := add(id)
		p.Datasets = children[id]
		if at, ok := syncedAt(units); ok {
			p.Sync, p.SyncedAt = api.ProjectSynced, &at
		}
		for _, u := range units {
			switch {
			case u.Location == "" && catsync.DeniedUnit(u):
				p.Accessible, p.Error = false, u.Error
			case u.Location == "" && u.Status == api.UnitInaccessible, u.Location != "" && lastAttemptFailed(u):
				p.Sync, p.Error = api.ProjectFailed, u.Error // SyncedAt keeps the cache's age
			}
		}
	}
	out := make([]api.ProjectInfo, 0, len(byID))
	for _, p := range byID {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ProjectsReport is `bqt projects`: the project view with a fresh list from
// BigQuery (a failed list is reported, not fatal).
func (e *Engine) ProjectsReport(ctx context.Context) (api.ProjectsReport, error) {
	var rep api.ProjectsReport
	rep.ExecutionProject, rep.ExecutionSource = e.ExecutionProject()
	listed, err := e.AccessibleProjects(ctx, true)
	if err != nil {
		rep.ListError = err.Error()
		listed = nil
	} else if listed == nil {
		listed = []api.ListedProject{}
	}
	infos, err := e.ProjectInfos(ctx, listed)
	if err != nil {
		return rep, err
	}
	rep.Projects = infos
	return rep, nil
}
