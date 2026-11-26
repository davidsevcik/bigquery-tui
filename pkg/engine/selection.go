package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/config"
	"github.com/davidsevcik/bigquery-tui/pkg/selection"
	catsync "github.com/davidsevcik/bigquery-tui/pkg/sync"
)

var ErrLastSelected = selection.ErrLastSelected

var ErrNoExecution = errors.New("no Execution project: select a project in Projects (ctrl+x p) or with `bqt projects select`")

var ErrNotSelected = errors.New("project is not selected")

type NotSelectedError struct{ Project string }

func (e *NotSelectedError) Error() string {
	return "project " + e.Project + " is not selected (select it first: `bqt projects select " + e.Project + "`)"
}

func (e *NotSelectedError) Is(target error) bool { return target == ErrNotSelected }

func selectionPath(dataDir string) string {
	return filepath.Join(dataDir, "state", "projects.json")
}

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

func (e *Engine) DeselectProjects(ids ...string) error {
	return e.updateSelection(func(s *selection.State) error { return s.Deselect(ids...) })
}

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

func (e *Engine) ChooseExecution(id string) error {
	if err := validIDs([]string{id}); err != nil {
		return err
	}
	return e.updateSelection(func(s *selection.State) error {
		s.Execution = id
		return nil
	})
}

func (e *Engine) ExecutionProject() (string, api.ExecutionSource) {
	if p := e.o.Execution; p != nil {
		return p.Project, p.Source
	}
	return resolveExecution(e.Config(), e.Selection())
}

type PinnedExecution struct {
	Project string
	Source  api.ExecutionSource
}

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
	pending, err := e.pendingUnits(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range withPending(units, pending) {
		out[u.Project] = append(out[u.Project], u)
	}
	return out, nil
}

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

func syncedAt(units []api.UnitState) (time.Time, bool) {
	var oldest time.Time
	var discovery *time.Time
	known := false
	for _, u := range units {
		if u.Location == "" {
			discovery = u.LastSuccess
			continue
		}
		if u.LastSuccess == nil {
			return time.Time{}, false
		}
		if !known || u.LastSuccess.Before(oldest) {
			oldest = *u.LastSuccess
		}
		known = true
	}
	if !known && discovery != nil {
		return *discovery, true
	}
	return oldest, known
}

func lastAttemptFailed(u api.UnitState) bool {
	if u.LastAttempt == nil || u.Status == api.UnitOK {
		return false
	}
	return u.LastSuccess == nil || u.LastAttempt.After(*u.LastSuccess)
}

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
		if l.FriendlyName != l.ID {
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
				p.Sync, p.Error = api.ProjectFailed, u.Error
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
