// Package tui is bqt's terminal frontend. It only lays out what the core
// returns: every piece of product behaviour (search, ranking, sync, formatting)
// comes from the engine through the Backend interface, or, where no state is
// involved (a Favorite's header, matching Favorites, binding parameters),
// from the core package that owns the rule.
package tui

import (
	"context"
	"path/filepath"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/export"
	"github.com/davidsevcik/bigquery-tui/internal/favorites"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/handoff"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
	projsel "github.com/davidsevcik/bigquery-tui/internal/selection"
)

type SyncRun interface {
	Events() <-chan api.SyncEvent
	Wait() (api.SyncResult, error)
	Cancel()
}

type Backend interface {
	Config() *config.Config
	SetConfig(*config.Config)
	ConfigPath() string
	WatchConfig(ctx context.Context, kick <-chan struct{}) <-chan config.Event
	StatePath() string

	ExecutionProject() (string, api.ExecutionSource)
	Selection() projsel.State
	SelectProjects(ids ...string) error
	DeselectProjects(ids ...string) error
	SelectOnly(id string) error
	ChooseExecution(id string) error
	AccessibleProjects(ctx context.Context, refresh bool) ([]api.ListedProject, error)
	ProjectInfos(ctx context.Context, listed []api.ListedProject) ([]api.ProjectInfo, error)
	NeedsSync(ctx context.Context, ids []string) ([]string, error)

	Projects() []api.BrowseNode
	Datasets(project string) []api.BrowseNode
	Tables(project, dataset string) []api.BrowseNode
	ShardMembers(project, dataset, family string, typ api.TableType) []api.BrowseNode
	ParseQuery(string) api.ParsedQuery
	Search(ctx context.Context, query string, o api.SearchOptions) (api.SearchResponse, error)
	Filter(ctx context.Context, query string, o api.FilterOptions) (api.FilterResponse, error)
	WarmSearch(ctx context.Context) error

	TableDetail(ctx context.Context, p api.ObjectPath) (api.TableDetail, error)
	TableStats(ctx context.Context, p api.ObjectPath) (api.TableStats, error)
	Preview(ctx context.Context, p api.ObjectPath, startIndex int64, rows int, o format.Options) (api.PreviewPage, error)
	TouchRecent(ctx context.Context, p api.ObjectPath) error

	Status(ctx context.Context) (api.CatalogStatus, error)
	StartSync(ctx context.Context, scope api.SyncScope) (SyncRun, error)
	AutoRefresh(ctx context.Context) (SyncRun, error)
	Reconcile(ctx context.Context) (syncNeeded bool, reason string, err error)
	WatchCatalog(ctx context.Context) <-chan struct{}

	Tokenize(text string) []api.Token
	Complete(text string, cursor int, forced bool) []api.Candidate

	FollowRuns() *runs.Follower
	RunRows(ctx context.Context, id int64, startIndex int64, n int, o format.Options) (api.PreviewPage, error)
	RunSample(ctx context.Context, id int64, f handoff.SampleFormat) (string, error)
	LearnPricing(ctx context.Context) error
	StartExport(ctx context.Context, runID int64, path string, f export.Format) (Export, error)
	DefaultExportPath(f export.Format) string
	DraftDir() string

	ParseHistoryQuery(raw string) api.ParsedQuery
	SearchHistory(ctx context.Context, raw string, limit, offset int) (api.HistoryPage, error)
	HistoryEntry(ctx context.Context, id int64) (api.HistoryEntry, error)
	ReopenResult(ctx context.Context, entryID int64, tag string) (int64, error)
	PromoteHistory(ctx context.Context, entryID int64, name, description string, tags []string, overwrite bool) (string, error)
	ListFavorites() ([]api.Favorite, error)
	FavoriteFromID(file string) string
	SaveFavorite(r favorites.SaveRequest) (favorites.Saved, error)
	RenameFavorite(f api.Favorite, to string) (api.Favorite, error)
	DeleteFavorite(f api.Favorite) error
}

type Export interface {
	Events() <-chan api.ExportEvent
	Wait() api.ExportEvent
	Cancel()
}

type EngineBackend struct {
	*engine.Engine
	Load config.LoadOptions
}

func (b EngineBackend) ConfigPath() string { return config.ResolvePath(b.Load) }

func (b EngineBackend) StatePath() string { return b.Paths().State + "/tui.json" }

func (b EngineBackend) WatchConfig(ctx context.Context, kick <-chan struct{}) <-chan config.Event {
	return config.Watch(ctx, b.Config(), config.WatchOptions{Load: b.Load, Kick: kick})
}

func (b EngineBackend) StartSync(ctx context.Context, scope api.SyncScope) (SyncRun, error) {
	h, err := b.Engine.StartSync(ctx, scope)
	if err != nil || h == nil {
		return nil, err
	}
	return h, nil
}

func (b EngineBackend) AutoRefresh(ctx context.Context) (SyncRun, error) {
	h, err := b.Engine.AutoRefresh(ctx)
	if err != nil || h == nil {
		return nil, err
	}
	return h, nil
}

func (b EngineBackend) Reconcile(ctx context.Context) (bool, string, error) {
	rec, _, err := b.Engine.Reconcile(ctx)
	if err != nil {
		return false, "", err
	}
	return rec.SyncNeeded, rec.Reason, nil
}

func (b EngineBackend) StartExport(ctx context.Context, runID int64, path string, f export.Format) (Export, error) {
	h, err := b.Engine.StartExport(ctx, runID, path, f)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (b EngineBackend) DraftDir() string { return filepath.Join(b.Paths().Drafts, "tui") }

func (b EngineBackend) WatchCatalog(ctx context.Context) <-chan struct{} {
	return b.Engine.WatchCatalog(ctx, time.Second)
}
