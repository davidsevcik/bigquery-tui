// Package tui is bqt's terminal frontend. It only lays out what the core
// returns: every piece of product behaviour (search, ranking, sync, formatting)
// comes from the engine through the Backend interface.
package tui

import (
	"context"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

// SyncRun is a running sync.
type SyncRun interface {
	Events() <-chan api.SyncEvent
	Wait() (api.SyncResult, error)
	Cancel()
}

// Backend is the slice of the engine the TUI uses. Tests substitute an engine
// built on fakes.
type Backend interface {
	Config() *config.Config
	SetConfig(*config.Config)
	ConfigPath() string
	WatchConfig(ctx context.Context, kick <-chan struct{}) <-chan config.Event
	StatePath() string

	Projects() []api.BrowseNode
	Datasets(project string) []api.BrowseNode
	Tables(project, dataset string) []api.BrowseNode
	ShardMembers(project, dataset, prefix string) []api.BrowseNode
	ParseQuery(string) api.ParsedQuery
	Search(ctx context.Context, query string, o api.SearchOptions) (api.SearchResponse, error)
	WarmSearch(ctx context.Context) error

	TableDetail(ctx context.Context, p api.ObjectPath) (api.TableDetail, error)
	TableStats(ctx context.Context, p api.ObjectPath) (api.TableStats, error)
	Preview(ctx context.Context, p api.ObjectPath, startIndex int64, rows, maxChars int) (api.PreviewPage, error)
	TouchRecent(ctx context.Context, p api.ObjectPath) error

	Status(ctx context.Context) (api.CatalogStatus, error)
	StartSync(ctx context.Context, scope api.SyncScope) (SyncRun, error)
	AutoRefresh(ctx context.Context) (SyncRun, error)
	// Reconcile prunes the catalog after a config change and reports whether a
	// sync is now needed.
	Reconcile(ctx context.Context) (syncNeeded bool, reason string, err error)
	WatchCatalog(ctx context.Context) <-chan struct{}
}

// EngineBackend adapts *engine.Engine to Backend.
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

func (b EngineBackend) WatchCatalog(ctx context.Context) <-chan struct{} {
	return b.Engine.WatchCatalog(ctx, time.Second)
}
