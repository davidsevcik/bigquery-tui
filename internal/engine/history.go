package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/favorites"
	"github.com/davidsevcik/bigquery-tui/internal/search"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// retentionEvery is how often History applies history_retention_days.
const retentionEvery = time.Hour

// applyRetention deletes entries older than history_retention_days, at most
// once an hour and again whenever the setting changes. The caller holds e.mu.
func (e *Engine) applyRetention(h *store.History) {
	days := e.Config().HistoryRetentionDays
	now := e.o.Now()
	if days <= 0 || (days == e.retainedDays && now.Sub(e.retainedAt) < retentionEvery) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := h.Prune(ctx, now.AddDate(0, 0, -days)) // AddDate cannot overflow into the future
	if err != nil {
		e.o.Log.Warn("apply history_retention_days", "err", err)
		return
	}
	if n > 0 {
		e.o.Log.Info("history retention", "deleted", n, "days", days)
	}
	e.retainedDays, e.retainedAt = days, now
}

// ParseHistoryQuery parses a History search (for chips).
func (e *Engine) ParseHistoryQuery(raw string) api.ParsedQuery { return search.ParseHistory(raw) }

// SearchHistory searches History (spec 11): free terms over the SQL plus
// status:, project:, source: and since:, one group per SQL text.
func (e *Engine) SearchHistory(ctx context.Context, raw string, limit, offset int) (api.HistoryPage, error) {
	q := search.ParseHistory(raw)
	page := api.HistoryPage{Query: q, Groups: []api.HistoryGroup{}}
	h, err := e.History()
	if err != nil {
		return page, err
	}
	gs, more, total, err := h.SearchHistory(ctx, search.HistoryQuery(q, e.o.Now()), limit, offset)
	if err != nil {
		return page, err
	}
	page.Groups, page.More, page.Total = gs, more, total
	return page, nil
}

// HistoryEntry returns one History entry.
func (e *Engine) HistoryEntry(ctx context.Context, id int64) (api.HistoryEntry, error) {
	h, err := e.History()
	if err != nil {
		return api.HistoryEntry{}, err
	}
	return h.HistoryEntry(ctx, id)
}

// PruneHistory deletes the entries created before t (bqt history prune).
func (e *Engine) PruneHistory(ctx context.Context, before time.Time) (int64, error) {
	h, err := e.History()
	if err != nil {
		return 0, err
	}
	return h.Prune(ctx, before)
}

// ReopenResult shows the result of a History entry's job as a finished Run
// and returns its ID (spec 10.4: BigQuery keeps results about 24 hours).
func (e *Engine) ReopenResult(ctx context.Context, entryID int64, tag string) (int64, error) {
	entry, err := e.HistoryEntry(ctx, entryID)
	if err != nil {
		return 0, err
	}
	if entry.State != api.RunDone {
		return 0, fmt.Errorf("this run %s: it has no result", entry.State)
	}
	r, err := e.runs.Reopen(ctx, entry, tag)
	if err != nil {
		return 0, err
	}
	return r.ID(), nil
}

// FavoritesDir is the favorites directory of the current config.
func (e *Engine) FavoritesDir() favorites.Dir { return favorites.Dir(e.Config().FavoritesDir) }

// ListFavorites reads every Favorite.
func (e *Engine) ListFavorites() ([]api.Favorite, error) { return e.FavoritesDir().List() }

// ReadFavorite reads one Favorite.
func (e *Engine) ReadFavorite(id string) (api.Favorite, error) { return e.FavoritesDir().Read(id) }

// SaveFavorite saves a text as a Favorite with a fresh header.
func (e *Engine) SaveFavorite(r favorites.SaveRequest) (favorites.Saved, error) {
	return e.FavoritesDir().Save(r)
}

// RenameFavorite renames a Favorite as it was read; the target must not
// exist, and a reload of favorites_dir since then refuses it.
func (e *Engine) RenameFavorite(f api.Favorite, to string) (api.Favorite, error) {
	return e.FavoritesDir().Rename(f, to)
}

// DeleteFavorite removes a Favorite as it was read; a reload of
// favorites_dir since then refuses it.
func (e *Engine) DeleteFavorite(f api.Favorite) error { return e.FavoritesDir().Delete(f) }

// FavoriteFromID returns the Favorite ID of a file inside favorites_dir, or
// "" for a file elsewhere.
func (e *Engine) FavoriteFromID(file string) string { return e.FavoritesDir().IDOf(file) }

// PromoteHistory saves a History entry as a Favorite (favorites.FromHistory)
// and writes the Favorite's ID back onto the entry.
func (e *Engine) PromoteHistory(ctx context.Context, entryID int64, name, description string, tags []string, overwrite bool) (string, error) {
	h, err := e.History()
	if err != nil {
		return "", err
	}
	entry, err := h.HistoryEntry(ctx, entryID)
	if err != nil {
		return "", err
	}
	id, err := e.FavoritesDir().Write(name, favorites.FromHistory(entry.SQL, entry.Params, description, tags), overwrite)
	if err != nil {
		return "", err
	}
	if err := h.SetFavoritePath(ctx, entryID, id); err != nil {
		return id, fmt.Errorf("saved %s, but could not link the History entry: %w", id, err)
	}
	return id, nil
}
