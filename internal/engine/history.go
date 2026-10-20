package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/favorites"
	"github.com/davidsevcik/bigquery-tui/internal/search"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
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

// MatchFavorites ranks Favorites for a query (in memory).
func (e *Engine) MatchFavorites(favs []api.Favorite, query string) []api.FavoriteMatch {
	return favorites.Search(favs, query)
}

// ReadFavorite reads one Favorite.
func (e *Engine) ReadFavorite(id string) (api.Favorite, error) { return e.FavoritesDir().Read(id) }

// FavoritePath returns the file of a Favorite.
func (e *Engine) FavoritePath(id string) (string, error) { return e.FavoritesDir().Path(id) }

// SaveFavorite writes a Favorite's full text (header and body) and returns
// its ID. Without overwrite an existing Favorite is an error.
func (e *Engine) SaveFavorite(id, text string, overwrite bool) (string, error) {
	return e.FavoritesDir().Write(id, text, overwrite)
}

// RenameFavorite renames a Favorite; the target must not exist.
func (e *Engine) RenameFavorite(from, to string) (string, error) {
	return e.FavoritesDir().Rename(from, to)
}

// DeleteFavorite removes a Favorite.
func (e *Engine) DeleteFavorite(id string) error { return e.FavoritesDir().Delete(id) }

// FavoriteFromID returns the Favorite ID of a file inside favorites_dir, or
// "" for a file elsewhere.
func (e *Engine) FavoriteFromID(file string) string {
	dir := e.Config().FavoritesDir
	if dir == "" || file == "" {
		return ""
	}
	real := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	rel, err := filepath.Rel(real(dir), real(file))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	id, err := favorites.CleanID(filepath.ToSlash(rel))
	if err != nil || !strings.EqualFold(filepath.Ext(rel), ".sql") {
		return ""
	}
	return id
}

// PromoteHistory saves a History entry as a Favorite: the entry's SQL, a
// header with the description and tags, and its parameters declared with
// the types and values it ran with (as defaults). The Favorite's ID is
// written back onto the entry.
func (e *Engine) PromoteHistory(ctx context.Context, entryID int64, name, description string, tags []string, overwrite bool) (string, error) {
	h, err := e.History()
	if err != nil {
		return "", err
	}
	entry, err := h.HistoryEntry(ctx, entryID)
	if err != nil {
		return "", err
	}
	// a Run from a Favorite's buffer carries that header in its SQL: start
	// from it, so the new header replaces it instead of stacking on it
	f := favorites.Parse(entry.SQL)
	f.Text, f.Problems = "", nil
	if strings.TrimSpace(description) != "" || len(f.Description) == 0 {
		f.Description = description
	}
	if len(tags) > 0 {
		f.Tags = tags
	}
	declared := map[string]bool{}
	for _, p := range f.Params {
		declared[strings.ToLower(p.Name)] = true
	}
	used := map[string]bool{}
	for _, n := range sqlx.Params(f.SQL) {
		used[strings.ToLower(n)] = true
	}
	for _, p := range entry.Params {
		k := strings.ToLower(p.Name)
		if !used[k] || declared[k] || !validParamType(p.Type) {
			continue
		}
		v := p.Value
		if p.Null {
			v = "null" // Bind reads it back as NULL
		}
		f.Params = append(f.Params, api.ParamSpec{Name: p.Name, Type: p.Type, Default: &v})
	}
	id, err := e.FavoritesDir().Write(name, favorites.Render(f), overwrite)
	if err != nil {
		return "", err
	}
	if err := h.SetFavoritePath(ctx, entryID, id); err != nil {
		return id, fmt.Errorf("saved %s, but could not link the History entry: %w", id, err)
	}
	return id, nil
}

func validParamType(t string) bool {
	for _, x := range favorites.Types {
		if x == t {
			return true
		}
	}
	return false
}

// QueryParams is the parameter schema of a query text: the parameters its
// body uses, typed by a Favorite header if the text has one.
func (e *Engine) QueryParams(text, body string) []api.ParamSpec {
	f := favorites.Parse(text)
	f.SQL = body
	return favorites.Schema(f)
}

// BindParam checks a value against its declared type.
func (e *Engine) BindParam(p api.ParamSpec, value string) (api.QueryParam, error) {
	return favorites.Bind(p, value)
}

// IsFavoriteExists reports an "already exists" error from the Favorite
// operations.
func IsFavoriteExists(err error) bool { return errors.Is(err, favorites.ErrExists) }

// ParseFavorite reads a Favorite header and body from text.
func (e *Engine) ParseFavorite(text string) api.Favorite { return favorites.Parse(text) }

// RenderFavorite writes a Favorite's header and body as file text.
func (e *Engine) RenderFavorite(f api.Favorite) string { return favorites.Render(f) }
