package engine

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/search"
	"github.com/davidsevcik/bigquery-tui/internal/store"
	catsync "github.com/davidsevcik/bigquery-tui/internal/sync"
)

// ErrUnknownTable is returned for a path that is not in the catalog.
var ErrUnknownTable = errors.New("table is not in the catalog")

type statsEntry struct {
	stats api.TableStats
	at    time.Time
}

const statsTTL = 5 * time.Minute

// ReloadIndex rebuilds the in-memory search index from the catalog.
func (e *Engine) ReloadIndex(ctx context.Context) error {
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
			return nil, nil
		}
	}
	h, err := e.History()
	if err != nil {
		return nil, err
	}
	return h.Recent(ctx, n)
}

// Search runs a catalog search (spec 8.2).
func (e *Engine) Search(ctx context.Context, query string, o api.SearchOptions) (api.SearchResponse, error) {
	return e.search.Search(ctx, query, o)
}

// ParseQuery parses a query without running it (for input colouring and chips).
func (e *Engine) ParseQuery(query string) api.ParsedQuery { return search.Parse(query) }

// WarmSearch prepares the column data used by the col: filter.
func (e *Engine) WarmSearch(ctx context.Context) error { return e.search.WarmColumns(ctx) }

// Projects lists the configured projects (synced or not) with sync health.
func (e *Engine) Projects() []api.BrowseNode {
	have := map[string]api.BrowseNode{}
	for _, n := range e.search.Projects() {
		have[n.Name] = n
	}
	cfg := e.Config()
	names := append([]string(nil), cfg.Projects...)
	for n := range have {
		found := false
		for _, c := range cfg.Projects {
			found = found || c == n
		}
		if !found {
			names = append(names, n) // still in the catalog until the next prune
		}
	}
	sort.Strings(names)
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

// Datasets lists a project's datasets.
func (e *Engine) Datasets(project string) []api.BrowseNode { return e.search.Datasets(project) }

// Tables lists a dataset's tables with shard families collapsed.
func (e *Engine) Tables(project, dataset string) []api.BrowseNode {
	return e.search.Tables(project, dataset)
}

// ShardMembers lists the tables of a shard family, newest first.
func (e *Engine) ShardMembers(project, dataset, prefix string) []api.BrowseNode {
	return e.search.ShardMembers(project, dataset, prefix)
}

// TableDetail returns everything the schema pane shows, from the catalog.
func (e *Engine) TableDetail(ctx context.Context, p api.ObjectPath) (api.TableDetail, error) {
	ref, ok := e.search.Lookup(p)
	if !ok || e.catalog == nil {
		return api.TableDetail{}, ErrUnknownTable
	}
	t, err := e.catalog.TableByID(ctx, ref.ID)
	if err != nil {
		return api.TableDetail{}, err
	}
	d := api.TableDetail{Path: ref.Path, Table: t, Location: ref.Location, NamesOnly: ref.NamesOnly, Shard: t.Shard, Stale: true}
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
	st := api.TableStats{Rows: info.Rows, Bytes: info.Bytes}
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

// TableStats fetches the live row and byte counts (cached for a few minutes).
// It needs credentials and one API call, so frontends call it off the UI thread
// and treat a failure as "unknown".
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

// Preview reads rows straight from a table (no query, no cost). Views and
// external tables return bq.ErrNeedsQuery. maxChars truncates cell text.
func (e *Engine) Preview(ctx context.Context, p api.ObjectPath, startIndex int64, rows, maxChars int) (api.PreviewPage, error) {
	ref, ok := e.search.Lookup(p)
	if !ok {
		return api.PreviewPage{}, ErrUnknownTable
	}
	switch ref.Type {
	case api.TypeView, api.TypeExternal:
		return api.PreviewPage{}, bq.ErrNeedsQuery
	}
	info, err := e.tableInfo(ctx, ref)
	if err != nil {
		return api.PreviewPage{}, err
	}
	if info.Type == api.TypeView || info.Type == api.TypeExternal {
		return api.PreviewPage{}, bq.ErrNeedsQuery
	}
	tp, err := e.tables(ctx)
	if err != nil {
		return api.PreviewPage{}, err
	}
	raw, err := tp.ListRows(ctx, bq.TableRef{Project: ref.Path.Project, Dataset: ref.Path.Dataset, Table: ref.Path.Table}, startIndex, rows)
	if err != nil {
		return api.PreviewPage{}, err
	}
	page := api.PreviewPage{Columns: format.Columns(info.Schema), StartIndex: startIndex, TotalRows: raw.TotalRows, Rows: [][]api.Cell{}}
	for _, r := range raw.Rows {
		page.Rows = append(page.Rows, format.Row(info.Schema, r, format.Options{MaxChars: maxChars}))
	}
	return page, nil
}

// TouchRecent records that a table was opened (feeds the empty-query search).
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

// WatchCatalog reloads the search index whenever the catalog changes (through
// this process or another one) and signals each reload. The channel closes
// when ctx ends.
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
				continue // read-only engine opened before the first sync
			}
			if w == nil {
				nw, err := e.catalog.NewWatcher(ctx)
				if err != nil {
					continue
				}
				w = nw
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

// Reconcile applies a changed project/ignore filter to the catalog by pruning
// locally. It needs the sync lock; if another process is syncing it does
// nothing (that sync reconciles itself) and reports skipped.
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

// AutoRefresh starts the background sync a frontend wants at startup: a full
// sync when the catalog is empty, a refresh of the stale projects otherwise.
// It returns nil when nothing needs doing (or when a sync runs elsewhere). A
// "config changed" state is never synced automatically; the user decides.
func (e *Engine) AutoRefresh(ctx context.Context) (*SyncHandle, error) {
	st, err := e.Status(ctx)
	if err != nil {
		return nil, err
	}
	if st.SyncRunning || len(e.Config().Projects) == 0 {
		return nil, nil
	}
	var scope api.SyncScope
	if st.Tables+st.Datasets > 0 || len(st.Projects) == 0 {
		for _, p := range st.Projects {
			if p.Stale {
				scope.Projects = append(scope.Projects, p.Project)
			}
		}
		if len(scope.Projects) == 0 {
			return nil, nil
		}
		if len(scope.Projects) == len(st.Projects) {
			scope.Projects = nil // everything: a full sync also clears "sync needed"
		}
	}
	h, err := e.StartSync(ctx, scope)
	if errors.Is(err, ErrSyncRunning) {
		return nil, nil
	}
	return h, err
}

// SetConfig swaps in a reloaded configuration. Run-related and sync-related
// keys apply to operations started afterwards. A changed execution_project
// takes effect on the next BigQuery call (the client is rebuilt lazily).
// data_dir cannot change at run time; callers report it as restart-required.
func (e *Engine) SetConfig(cfg *config.Config) {
	e.cfgMu.Lock()
	e.cfg = cfg
	e.cfgMu.Unlock()
	e.search.SetTTL(cfg.SyncTTL)
	e.mu.Lock()
	e.stats, e.infos = map[string]statsEntry{}, map[string]bq.TableInfo{}
	e.mu.Unlock()
}
