// Package engine is the facade every frontend uses. It wires the config, the
// store, the BigQuery ports and the sync engine, and exposes long operations
// as a handle plus a stream of plain-data events.
package engine

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
	"github.com/davidsevcik/bigquery-tui/internal/runs"
	"github.com/davidsevcik/bigquery-tui/internal/search"
	"github.com/davidsevcik/bigquery-tui/internal/selection"
	"github.com/davidsevcik/bigquery-tui/internal/store"
	catsync "github.com/davidsevcik/bigquery-tui/internal/sync"
)

// ErrSyncRunning is returned when another process holds the sync lock.
var ErrSyncRunning = errors.New("a sync is already running (in another bqt process)")

// ErrClosed is returned by operations that outlive Close.
var ErrClosed = errors.New("engine is closed")

// ErrReadOnly is returned when a write operation is attempted on a read-only engine.
var ErrReadOnly = errors.New("engine was opened read-only")

// Options configures Open.
type Options struct {
	Config *config.Config
	// ReadOnly opens the catalog without ever modifying it (CLI status, MCP).
	ReadOnly bool
	// Meta builds the BigQuery metadata client lazily, so commands that never
	// sync (status) need no credentials. nil uses the real client.
	Meta func(ctx context.Context) (bq.Metadata, error)
	// Tables builds the live table reader (stats and preview) lazily.
	Tables func(ctx context.Context) (bq.Tables, error)
	// Jobs builds the query client lazily.
	Jobs func(ctx context.Context) (bq.Jobs, error)
	// Client builds a real BigQuery client whose jobs run in exec ("": no
	// Execution project, for listing projects only). nil uses bq.NewClient.
	// Meta, Tables and Jobs win over it.
	Client func(ctx context.Context, exec string) (Client, error)
	// Source names the frontend on History entries and job labels (tui, cli, mcp).
	Source string
	// Execution, when set, fixes the Execution project for the engine's
	// lifetime (bqt mcp keeps the one it started with: ResolveExecution).
	Execution *PinnedExecution
	Now       func() time.Time
	Log       *slog.Logger

	// Test overrides for the sync engine.
	Parallelism int
	Retries     int
	Backoff     func(int) time.Duration
}

// Engine is the core facade.
type Engine struct {
	o       Options
	cfg     *config.Config
	paths   store.Paths
	catalog *store.Catalog
	notice  string
	sel     *selection.Store

	execMu   gosync.Mutex
	lastExec string // the Execution project last seen (pricing is per project)

	cfgMu  gosync.RWMutex
	search *search.Searcher
	runs   *runs.Manager

	colMu    gosync.Mutex
	colCache map[string][]api.ColumnMeta // completion's columns, per table

	mu      gosync.Mutex
	history *store.History
	clients map[string]Client // by Execution project ("": for listing)
	stats   map[string]statsEntry
	infos   map[string]bq.TableInfo
	closers []func() error
	closed  bool

	listed   []api.ListedProject // BigQuery's projects list, once fetched
	listedOK bool

	retainedDays int // history_retention_days last applied, and when
	retainedAt   time.Time
}

// Open prepares the engine. In read-write mode it creates the data directory
// and catalog; in read-only mode a missing catalog is not an error (Status
// reports it).
func Open(ctx context.Context, o Options) (*Engine, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	e := &Engine{o: o, cfg: o.Config, paths: store.PathsFor(o.Config.DataDir), stats: map[string]statsEntry{}, infos: map[string]bq.TableInfo{}}
	e.sel = selection.Open(selectionPath(o.Config.DataDir))
	e.search = search.New(nil)
	e.search.Now, e.search.TTL, e.search.Recent = o.Now, o.Config.SyncTTL, e.recent
	e.runs = runs.New(runs.Options{Jobs: e.jobs, Config: e.Config, Execution: e.execProject, Now: o.Now, Source: o.Source, Record: e.recordRun, Log: o.Log})
	e.lastExec = e.execProject()
	if o.ReadOnly {
		cat, err := store.OpenCatalogReadOnly(e.paths.Catalog)
		if err != nil && !errors.Is(err, store.ErrNoCatalog) {
			return nil, err
		}
		e.catalog = cat
		if err := e.ReloadIndex(ctx); err != nil {
			return nil, err
		}
		return e, nil
	}
	if err := e.paths.Ensure(); err != nil {
		return nil, err
	}
	cat, err := store.OpenCatalog(e.paths.Catalog)
	if err != nil {
		return nil, fmt.Errorf("open catalog: %w", err)
	}
	e.catalog = cat
	e.notice = cat.Recreated()
	if err := e.ReloadIndex(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// Config returns the configuration the engine was opened with.
func (e *Engine) Config() *config.Config {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg
}

// Paths returns the data directory layout.
func (e *Engine) Paths() store.Paths { return e.paths }

// Notice is a one-time message about the catalog (for example "recreated").
func (e *Engine) Notice() string { return e.notice }

// Catalog exposes the catalog for read access (search and completion in later
// phases). It may be nil for a read-only engine with no catalog yet.
func (e *Engine) Catalog() *store.Catalog { return e.catalog }

// History opens history.db on first use.
func (e *Engine) History() (*store.History, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrClosed
	}
	if e.history != nil {
		e.applyRetention(e.history)
		return e.history, nil
	}
	if err := e.paths.Ensure(); err != nil {
		return nil, err
	}
	h, err := store.OpenHistory(e.paths.History)
	if err != nil {
		return nil, err
	}
	e.history = h
	e.applyRetention(h)
	return h, nil
}

// Close releases every resource.
func (e *Engine) Close() error {
	e.runs.Close()
	// Operations still running (a late TouchRecent, a client being built) set
	// these fields under e.mu, so Close takes it too.
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	if e.catalog != nil {
		errs = append(errs, e.catalog.Close())
	}
	if e.history != nil {
		errs = append(errs, e.history.Close())
	}
	for _, c := range e.closers {
		errs = append(errs, c())
	}
	for _, c := range e.clients {
		errs = append(errs, c.Close())
	}
	e.history, e.clients, e.closers, e.closed = nil, nil, nil, true
	return errors.Join(errs...)
}

// Client is a real BigQuery client: every port, for one Execution project.
type Client interface {
	bq.Metadata
	bq.Tables
	bq.Jobs
	Close() error
}

func (e *Engine) meta(ctx context.Context) (bq.Metadata, error) {
	if e.o.Meta != nil {
		return e.o.Meta(ctx)
	}
	return e.execClient(ctx)
}

// lister is the metadata client for listing projects. It needs no Execution
// project and never uses one: the credentials' own quota project applies, so
// an unusable Execution project cannot hide the list to pick another from.
func (e *Engine) lister(ctx context.Context) (bq.Metadata, error) {
	if e.o.Meta != nil {
		return e.o.Meta(ctx)
	}
	return e.clientFor(ctx, "")
}

func (e *Engine) tables(ctx context.Context) (bq.Tables, error) {
	if e.o.Tables != nil {
		return e.o.Tables(ctx)
	}
	return e.execClient(ctx)
}

// jobs is the query client of exec (a Run keeps the project it started in).
func (e *Engine) jobs(ctx context.Context, exec string) (bq.Jobs, error) {
	if e.o.Jobs != nil {
		return e.o.Jobs(ctx)
	}
	if exec == "" {
		return nil, ErrNoExecution
	}
	return e.clientFor(ctx, exec)
}

// execClient is the client of the current Execution project.
func (e *Engine) execClient(ctx context.Context) (Client, error) {
	exec := e.execProject()
	if exec == "" {
		return nil, ErrNoExecution
	}
	e.noticeExecution()
	return e.clientFor(ctx, exec)
}

// clientFor returns the shared client whose jobs run in exec, building it on
// first use. Clients of earlier Execution projects stay open for the Runs
// that started in them.
func (e *Engine) clientFor(ctx context.Context, exec string) (Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrClosed
	}
	if c, ok := e.clients[exec]; ok {
		return c, nil
	}
	newClient := e.o.Client
	if newClient == nil {
		newClient = func(ctx context.Context, exec string) (Client, error) {
			return bq.NewClient(ctx, exec, bq.Options{Log: e.o.Log})
		}
	}
	c, err := newClient(ctx, exec)
	if err != nil {
		return nil, err
	}
	if e.clients == nil {
		e.clients = map[string]Client{}
	}
	e.clients[exec] = c
	return c, nil
}

// Status reports catalog freshness and sync health from the local catalog.
func (e *Engine) Status(ctx context.Context) (api.CatalogStatus, error) {
	now := e.o.Now()
	cfg := e.Config()
	st := api.CatalogStatus{Now: now, SyncTTL: cfg.SyncTTL.String(), Projects: []api.ProjectStatus{}}
	st.SyncRunning = e.syncRunning()

	var units []api.UnitState
	if e.catalog != nil {
		var err error
		if units, err = e.catalog.Units(ctx); err != nil {
			return st, err
		}
		n, err := e.catalog.Counts(ctx)
		if err != nil {
			return st, err
		}
		st.Datasets, st.Tables, st.Columns = n.Datasets, n.Tables, n.Columns
		pending, reason, err := catsync.PendingConfigChange(ctx, e.catalog, cfg)
		if err != nil {
			return st, err
		}
		st.SyncNeeded, st.SyncReason = pending, reason
	} else {
		st.SyncNeeded, st.SyncReason = true, "no catalog yet (run `bqt sync`)"
	}

	byProject := map[string][]api.UnitState{}
	for _, u := range units {
		u.Stale = isStale(u, now, cfg.SyncTTL)
		byProject[u.Project] = append(byProject[u.Project], u)
	}
	st.ExecutionProject, st.ExecutionSource = e.ExecutionProject()
	selected := e.selected()
	isSel := map[string]bool{}
	for _, p := range selected {
		isSel[p] = true
	}
	// Selected projects first, then the cached deselected ones.
	projects := append([]string(nil), selected...)
	var others []string
	for p := range byProject {
		if !isSel[p] {
			others = append(others, p)
		}
	}
	for _, n := range e.search.Projects() {
		if _, ok := byProject[n.Name]; !ok && !isSel[n.Name] {
			others = append(others, n.Name)
		}
	}
	sort.Strings(others)
	projects = append(projects, others...)
	for _, p := range projects {
		ps := api.ProjectStatus{Project: p, Selected: isSel[p], Units: byProject[p]}
		if ps.Units == nil {
			ps.Units = []api.UnitState{}
		}
		var oldest *time.Time
		known := false
		for _, u := range ps.Units {
			if u.Location == "" {
				continue // discovery state carries no data of its own
			}
			known = true
			if u.LastSuccess == nil {
				oldest = nil
				ps.Stale = true
				break
			}
			if oldest == nil || u.LastSuccess.Before(*oldest) {
				t := *u.LastSuccess
				oldest = &t
			}
			if u.Stale {
				ps.Stale = true
			}
		}
		if !known {
			ps.Stale = true
		} else {
			ps.SyncedAt = oldest
		}
		st.Projects = append(st.Projects, ps)
	}
	if len(selected) == 0 && !st.SyncNeeded {
		st.SyncNeeded, st.SyncReason = true, "no projects selected"
	}
	if !st.SyncNeeded {
		for _, p := range st.Projects {
			if p.Selected && p.SyncedAt == nil {
				st.SyncNeeded, st.SyncReason = true, "project "+p.Project+" has never been synced"
				break
			}
		}
	}
	return st, nil
}

func isStale(u api.UnitState, now time.Time, ttl time.Duration) bool {
	if u.LastSuccess == nil {
		return true
	}
	return now.Sub(*u.LastSuccess) > ttl
}

func (e *Engine) syncRunning() bool {
	l, err := store.TryLock(e.paths.Lock)
	if errors.Is(err, store.ErrLocked) {
		return true
	}
	if err == nil {
		l.Unlock()
	}
	return false
}

// SyncHandle is a running sync.
type SyncHandle struct {
	cancel context.CancelFunc
	out    chan api.SyncEvent
	done   chan struct{}

	mu      gosync.Mutex
	queue   []api.SyncEvent
	wake    chan struct{}
	end     bool
	abandon chan struct{} // closed by Wait: nobody will read further events
	once    gosync.Once

	result api.SyncResult
	err    error
}

// Events streams the sync's events. The channel is closed after the last event.
// Reading it is optional: events queue internally, and events still unread
// when Wait returns are dropped (read Events to the end first if you want them
// all).
func (h *SyncHandle) Events() <-chan api.SyncEvent { return h.out }

// Cancel requests cancellation. Committed units stay; in-flight units roll back.
func (h *SyncHandle) Cancel() { h.cancel() }

// Wait blocks until the sync has finished and returns its result.
func (h *SyncHandle) Wait() (api.SyncResult, error) {
	<-h.done
	h.once.Do(func() { close(h.abandon) }) // release the pump if nobody reads
	return h.result, h.err
}

func (h *SyncHandle) push(e api.SyncEvent) {
	h.mu.Lock()
	h.queue = append(h.queue, e)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *SyncHandle) pump() {
	defer close(h.out)
	for {
		h.mu.Lock()
		var next *api.SyncEvent
		if len(h.queue) > 0 {
			e := h.queue[0]
			h.queue = h.queue[1:]
			next = &e
		}
		ended := h.end
		h.mu.Unlock()
		if next != nil {
			select {
			case h.out <- *next:
			case <-h.abandon:
				return
			}
			continue
		}
		if ended {
			return
		}
		select {
		case <-h.wake:
		case <-h.abandon:
			return
		}
	}
}

func (h *SyncHandle) finish(res api.SyncResult, err error) {
	h.result, h.err = res, err
	h.mu.Lock()
	h.end = true
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
	close(h.done)
}

// StartSync begins a sync in the background. It fails fast with
// ErrSyncRunning if another process is syncing.
func (e *Engine) StartSync(ctx context.Context, scope api.SyncScope) (*SyncHandle, error) {
	if e.o.ReadOnly {
		return nil, ErrReadOnly
	}
	cfg := e.Config()
	selected := e.selected()
	if len(scope.Projects) == 0 && len(selected) == 0 {
		return nil, catsync.ErrNoProjects
	}
	if err := e.checkScope(ctx, scope, selected); err != nil {
		return nil, err
	}
	if e.execProject() == "" {
		return nil, ErrNoExecution
	}
	lock, err := store.TryLock(e.paths.Lock)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return nil, ErrSyncRunning
		}
		return nil, err
	}
	md, err := e.meta(ctx)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	h := &SyncHandle{cancel: cancel, out: make(chan api.SyncEvent), done: make(chan struct{}), wake: make(chan struct{}, 1), abandon: make(chan struct{})}
	go h.pump()
	s := catsync.New(catsync.Options{
		Config: cfg, Selected: selected, Meta: md, Catalog: e.catalog, Now: e.o.Now, Log: e.o.Log,
		Parallelism: e.o.Parallelism, Retries: e.o.Retries, Backoff: e.o.Backoff,
	})
	go func() {
		defer cancel()
		res, err := s.Run(rctx, scope, h.push)
		lock.Unlock()
		// Make the result searchable before anyone is told the sync is done
		// (the catalog watcher covers progressive updates and other processes).
		if rerr := e.ReloadIndex(context.WithoutCancel(ctx)); rerr != nil {
			e.o.Log.Warn("reload search index after sync", "err", rerr)
		}
		h.finish(res, err)
	}()
	return h, nil
}
