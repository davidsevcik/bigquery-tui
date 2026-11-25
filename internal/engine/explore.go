package engine

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/search"
	"github.com/davidsevcik/bigquery-tui/internal/store"
	catsync "github.com/davidsevcik/bigquery-tui/internal/sync"
)

var ErrUnknownTable = errors.New("table is not in the catalog")

type statsEntry struct {
	stats api.TableStats
	at    time.Time
}

const statsTTL = 5 * time.Minute

func (e *Engine) ReloadIndex(ctx context.Context) error {
	e.colMu.Lock()
	e.colCache = nil
	e.colMu.Unlock()
	if e.catalog == nil {
		e.search.Swap(search.BuildIndex(&store.IndexSnapshot{}), nil)
		return nil
	}
	snap, err := e.catalog.LoadIndex(ctx)
	if err != nil {
		return err
	}
	e.search.Swap(search.BuildIndex(snap), e.catalog)
	return nil
}

func (e *Engine) recent(ctx context.Context, n int) ([]string, error) {
	if e.o.ReadOnly {
		if _, err := os.Stat(e.paths.History); err != nil {
			return nil, nil //nolint:nilerr // read-only must not create the history, and without one nothing is recent
		}
	}
	h, err := e.History()
	if err != nil {
		return nil, err
	}
	return h.Recent(ctx, n)
}

func (e *Engine) Search(ctx context.Context, query string, o api.SearchOptions) (api.SearchResponse, error) {
	if o.SelectedOnly {
		o.Hidden = append(o.Hidden, e.deselectedCached()...)
	}
	if o.PreferSelected {
		o.Prefer = e.selected()
	}
	resp, err := e.search.Search(ctx, query, o)
	sel := e.Selection()
	for i, r := range resp.Results {
		resp.Results[i].Selected = sel.Has(r.Path.Project)
		if r.Pending {
			if d, ok := e.search.Dataset(r.Path.Project, r.Path.Dataset); ok {
				resp.Results[i].Syncing = e.unitSyncing(api.UnitRef{Project: r.Path.Project, Location: d.Location})
			}
		}
	}
	return resp, err
}

func (e *Engine) Filter(ctx context.Context, query string, o api.FilterOptions) (api.FilterResponse, error) {
	if o.SelectedOnly {
		o.Hidden = append(o.Hidden, e.deselectedCached()...)
	}
	return e.search.Filter(ctx, query, o)
}

func (e *Engine) deselectedCached() []string {
	sel := e.Selection()
	var out []string
	for _, n := range e.search.Projects() {
		if !sel.Has(n.Name) {
			out = append(out, n.Name)
		}
	}
	return out
}

func (e *Engine) ParseQuery(query string) api.ParsedQuery { return search.Parse(query) }

func (e *Engine) WarmSearch(ctx context.Context) error { return e.search.WarmColumns(ctx) }

func (e *Engine) Projects() []api.BrowseNode {
	have := map[string]api.BrowseNode{}
	for _, n := range e.search.Projects() {
		have[n.Name] = n
	}
	names := e.selected()
	out := make([]api.BrowseNode, 0, len(names))
	for _, name := range names {
		if n, ok := have[name]; ok {
			out = append(out, n)
			continue
		}
		n := api.BrowseNode{Kind: api.KindProject, Path: api.ObjectPath{Project: name}, Name: name}
		e.search.ProjectHealth(name, &n)
		out = append(out, n)
	}
	return out
}

func (e *Engine) Datasets(project string) []api.BrowseNode {
	return e.markSyncing(e.search.Datasets(project))
}

func (e *Engine) Tables(project, dataset string) []api.BrowseNode {
	return e.search.Tables(project, dataset)
}

func (e *Engine) ShardMembers(project, dataset, family string, typ api.TableType) []api.BrowseNode {
	return e.search.ShardMembers(project, dataset, family, typ)
}

func (e *Engine) TableDetail(ctx context.Context, p api.ObjectPath) (api.TableDetail, error) {
	ref, ok := e.search.Lookup(p)
	if !ok || e.catalog == nil {
		return api.TableDetail{}, ErrUnknownTable
	}
	t, err := e.catalog.TableByID(ctx, ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.TableDetail{}, ErrUnknownTable
	}
	if err != nil {
		return api.TableDetail{}, err
	}
	if t.Name != ref.Path.Table || t.Dataset != ref.Path.Dataset {
		return api.TableDetail{}, ErrUnknownTable
	}
	d := api.TableDetail{Path: ref.Path, Table: t, Location: ref.Location, NamesOnly: ref.NamesOnly, Shard: t.Shard, Stale: true}
	switch {
	case ref.Family != nil:
		d.SchemaFrom = t.Name
	case t.Shard != nil && t.SharedSchema:
		fp := api.ObjectPath{Project: ref.Path.Project, Dataset: ref.Path.Dataset, Table: t.Shard.Newest}
		if nref, ok := e.search.Lookup(fp); ok && nref.ID != ref.ID {
			if newest, err := e.catalog.TableByID(ctx, nref.ID); err == nil && newest.Name == nref.Path.Table {
				d.Table.Columns, d.SchemaFrom = newest.Columns, newest.Name
			}
		}
	}
	if u, ok := e.search.Unit(ref.Path.Project, ref.Location); ok {
		d.Status, d.Error, d.SyncedAt = u.Status, u.Error, u.LastSuccess
		d.Stale = u.LastSuccess == nil || e.o.Now().Sub(*u.LastSuccess) > e.Config().SyncTTL
	}
	return d, nil
}

func (e *Engine) tableInfo(ctx context.Context, ref search.TableRef) (bq.TableInfo, error) {
	key := ref.Path.String()
	e.mu.Lock()
	info, ok := e.infos[key]
	se, fresh := e.stats[key]
	e.mu.Unlock()
	if ok && fresh && e.o.Now().Sub(se.at) < statsTTL {
		return info, nil
	}
	tp, err := e.tables(ctx)
	if err != nil {
		return bq.TableInfo{}, err
	}
	info, err = tp.GetTable(ctx, bq.TableRef{Project: ref.Path.Project, Dataset: ref.Path.Dataset, Table: ref.Path.Table})
	if err != nil {
		return bq.TableInfo{}, err
	}
	st := api.TableStats{Rows: info.Rows, Bytes: info.Bytes, Required: requiredPaths(nil, "", info.Schema)}
	if !info.LastModified.IsZero() {
		lm := info.LastModified
		st.LastModified = &lm
	}
	e.mu.Lock()
	e.infos[key] = info
	e.stats[key] = statsEntry{stats: st, at: e.o.Now()}
	e.mu.Unlock()
	return info, nil
}

func requiredPaths(out []string, prefix string, fields []bq.Field) []string {
	for _, f := range fields {
		if f.Mode == "REQUIRED" {
			out = append(out, prefix+f.Name)
		}
		out = requiredPaths(out, prefix+f.Name+".", f.Fields)
	}
	return out
}

func (e *Engine) TableStats(ctx context.Context, p api.ObjectPath) (api.TableStats, error) {
	ref, ok := e.search.Lookup(p)
	if !ok {
		return api.TableStats{}, ErrUnknownTable
	}
	if _, err := e.tableInfo(ctx, ref); err != nil {
		return api.TableStats{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats[ref.Path.String()].stats, nil
}

func (e *Engine) Preview(ctx context.Context, p api.ObjectPath, startIndex int64, rows int, o format.Options) (api.PreviewPage, error) {
	ref, ok := e.search.Lookup(p)
	if !ok {
		return api.PreviewPage{}, ErrUnknownTable
	}
	switch ref.Type {
	case api.TypeView, api.TypeMView, api.TypeExternal:
		return api.PreviewPage{}, bq.ErrNeedsQuery
	}
	info, err := e.tableInfo(ctx, ref)
	if err != nil {
		return api.PreviewPage{}, err
	}
	if info.Type == api.TypeView || info.Type == api.TypeMView || info.Type == api.TypeExternal {
		return api.PreviewPage{}, bq.ErrNeedsQuery
	}
	tp, err := e.tables(ctx)
	if err != nil {
		return api.PreviewPage{}, err
	}
	tr := bq.TableRef{Project: ref.Path.Project, Dataset: ref.Path.Dataset, Table: ref.Path.Table}
	page := api.PreviewPage{Columns: format.Columns(info.Schema), StartIndex: startIndex, Rows: [][]api.Cell{}}
	for next := startIndex; len(page.Rows) < rows; {
		raw, err := tp.ListRows(ctx, tr, next, rows-len(page.Rows))
		if err != nil {
			return api.PreviewPage{}, err
		}
		page.TotalRows = raw.TotalRows
		for _, r := range raw.Rows {
			page.Rows = append(page.Rows, format.Row(info.Schema, r, o))
		}
		next += int64(len(raw.Rows))
		if len(raw.Rows) == 0 || next >= raw.TotalRows {
			break
		}
	}
	return page, nil
}

func (e *Engine) TouchRecent(ctx context.Context, p api.ObjectPath) error {
	if e.o.ReadOnly {
		return ErrReadOnly
	}
	h, err := e.History()
	if err != nil {
		return err
	}
	kind := "table"
	if p.Table == "" {
		kind = "dataset"
	}
	return h.TouchRecent(ctx, p.String(), kind, e.o.Now())
}

func (e *Engine) WatchCatalog(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = time.Second
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		var w *store.Watcher
		defer func() {
			if w != nil {
				w.Close()
			}
		}()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if e.catalog == nil {
				continue
			}
			if w == nil {
				nw, err := e.catalog.NewWatcher(ctx)
				if err != nil {
					continue
				}
				w = nw
				if err := e.ReloadIndex(ctx); err == nil {
					select {
					case out <- struct{}{}:
					default:
					}
				}
				continue
			}
			changed, err := w.Changed(ctx)
			if err != nil || !changed {
				continue
			}
			if err := e.ReloadIndex(ctx); err != nil {
				e.o.Log.Warn("reload search index", "err", err)
				continue
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}

func (e *Engine) Reconcile(ctx context.Context) (rec catsync.Reconciliation, skipped bool, err error) {
	if e.o.ReadOnly {
		return rec, true, ErrReadOnly
	}
	lock, err := store.TryLock(e.paths.Lock)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return rec, true, nil
		}
		return rec, false, err
	}
	defer lock.Unlock()
	rec, err = catsync.ReconcileConfig(ctx, e.catalog, e.Config())
	if err != nil {
		return rec, false, err
	}
	if rec.Changed {
		err = e.ReloadIndex(ctx)
	}
	return rec, false, err
}

func (e *Engine) AutoRefresh(ctx context.Context) (*SyncHandle, error) {
	st, err := e.Status(ctx)
	if err != nil {
		return nil, err
	}
	if st.SyncRunning || st.ExecutionProject == "" {
		return nil, nil
	}
	var scope api.SyncScope
	selected := 0
	for _, p := range st.Projects {
		if !p.Selected {
			continue
		}
		selected++
		if p.Stale {
			scope.Projects = append(scope.Projects, p.Project)
		}
	}
	if len(scope.Projects) == 0 {
		return nil, nil
	}
	if len(scope.Projects) == selected && !st.SyncNeeded {
		scope.Projects = nil
	}
	h, err := e.StartSync(ctx, scope)
	if errors.Is(err, ErrSyncRunning) {
		return nil, nil
	}
	return h, err
}

func (e *Engine) SetConfig(cfg *config.Config) {
	e.cfgMu.Lock()
	changed := e.cfg.Location != cfg.Location
	e.cfg = cfg
	e.cfgMu.Unlock()
	if changed {
		e.runs.ForgetPricing()
	}
	e.noticeExecution()
	e.search.SetTTL(cfg.SyncTTL)
	e.mu.Lock()
	e.stats, e.infos = map[string]statsEntry{}, map[string]bq.TableInfo{}
	e.mu.Unlock()
}
