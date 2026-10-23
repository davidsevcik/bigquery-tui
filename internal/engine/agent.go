package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/favorites"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
	"github.com/davidsevcik/bigquery-tui/internal/search"
)

// The agent surface (spec 14): what `bqt query`, `bqt favorites run` and the
// MCP tools call. Every query here runs under the policy profile of the
// engine's Source (cli or mcp); nothing here asks a question.

// ErrBadResultID is returned for a result ID that is not project:LOCATION.job_id.
var ErrBadResultID = errors.New("invalid result_id (want project:LOCATION.job_id)")

// AgentQuery describes one agent query.
type AgentQuery struct {
	SQL    string
	Params []api.QueryParam
	// Favorite is the Favorite the SQL came from (History).
	Favorite string
	// Limit is the number of rows returned (default policy.DefaultRows, at
	// most policy.MaxRows).
	Limit int
	// MaxBytes and Timeout lower the profile's limits; higher values are
	// ignored.
	MaxBytes int64
	Timeout  time.Duration
}

func (e *Engine) agentSource() string {
	if e.o.Source == "mcp" {
		return "mcp"
	}
	return "cli"
}

// AgentProfile returns the policy profile agent queries run under, with the
// given limits lowered.
func (e *Engine) AgentProfile(maxBytes int64, timeout time.Duration) policy.Profile {
	return policy.For(e.Config(), e.agentSource()).Lower(maxBytes, timeout)
}

// QueryFailure is a failed dry-run or job, with BigQuery's message and the
// error position when there is one.
type QueryFailure = runs.QueryFailure

// AgentDryRun estimates a query and says whether the policy would run it.
func (e *Engine) AgentDryRun(ctx context.Context, q AgentQuery) (api.DryRunResult, error) {
	prof := e.AgentProfile(q.MaxBytes, q.Timeout)
	f := e.runs.Follow()
	info, err := f.Outcome(ctx, f.Start(ctx, runs.Request{SQL: q.SQL, Params: q.Params, DryRunOnly: true, Policy: &prof}, runs.Origin{}))
	if info.State != api.RunEstimated {
		return api.DryRunResult{}, err
	}
	est := info.Estimate
	return api.DryRunResult{StatementType: est.StatementType, Bytes: est.Bytes, Accuracy: est.Accuracy,
		Capacity: est.Capacity, ReservationID: est.ReservationID, ApproxUSD: est.ApproxUSD, Columns: est.Columns,
		Allowed: info.Refusal == nil, Refusal: info.Refusal, Limit: prof.MaxBytes}, nil
}

// RunAgentQuery runs a query under the policy and returns its first rows.
// A refusal is a *policy.Error, a failed query a *QueryFailure.
func (e *Engine) RunAgentQuery(ctx context.Context, q AgentQuery) (api.QueryResult, error) {
	prof := e.AgentProfile(q.MaxBytes, q.Timeout)
	// one deadline covers the Run and reading the rows returned
	deadline := time.Now().Add(prof.Timeout)
	f := e.runs.Follow()
	id := f.Start(ctx, runs.Request{SQL: q.SQL, Params: q.Params, Favorite: q.Favorite, Policy: &prof}, runs.Origin{})
	info, err := f.Outcome(ctx, id)
	if err != nil {
		return api.QueryResult{}, err
	}
	res := api.QueryResult{Columns: info.Columns, Rows: [][]*string{}, TotalRows: info.TotalRows}
	if res.Columns == nil {
		res.Columns = []api.PreviewColumn{}
	}
	if info.Job != nil {
		res.ResultID = ResultID(*info.Job)
	}
	if s := info.Stats; s != nil {
		res.StatementType = s.StatementType
		res.Stats = &api.QueryStats{BytesProcessed: s.BytesProcessed, SlotMs: s.SlotMs, CacheHit: s.CacheHit,
			Capacity: s.ReservationID != "", AffectedRows: s.AffectedRows}
		if !res.Stats.Capacity {
			res.Stats.BytesBilled = s.BytesBilled
		}
		if info.Started != nil && info.Ended != nil {
			res.Stats.DurationMs = info.Ended.Sub(*info.Started).Milliseconds()
		}
	}
	if info.ResultError != "" {
		return res, fmt.Errorf("the query ran, but its rows could not be read: %s", info.ResultError)
	}
	if !info.HasResult {
		res.ResultID = "" // nothing to page
		return res, nil
	}
	rctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	page, err := e.RunRows(rctx, id, 0, rowLimit(q.Limit), format.Options{MaxChars: policy.CellChars, KeepNewlines: true})
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return res, &QueryFailure{Err: api.QueryError{Kind: "timeout",
				Message: fmt.Sprintf("reading the rows took longer than %s (agent_timeout)", prof.Timeout)}}
		}
		return res, err
	}
	fillRows(&res, page.Rows, 0)
	return res, nil
}

func rowLimit(n int) int {
	switch {
	case n <= 0:
		return policy.DefaultRows
	case n > policy.MaxRows:
		return policy.MaxRows
	}
	return n
}

// fillRows sets the rows, the truncation counts and the cursor of a page.
func fillRows(res *api.QueryResult, cells [][]api.Cell, start int64) {
	res.StartIndex = start
	res.Rows = make([][]*string, 0, len(cells))
	for _, row := range cells {
		out := make([]*string, len(row))
		for i, c := range row {
			if c.Null {
				continue
			}
			t := c.Text
			if c.Truncated {
				t += "…"
				res.TruncatedCells++
			}
			out[i] = &t
		}
		res.Rows = append(res.Rows, out)
	}
	if next := start + int64(len(cells)); next < res.TotalRows && len(cells) > 0 {
		res.Truncated, res.NextCursor = true, strconv.FormatInt(next, 10)
	}
}

// ResultID names a job's result for later paging: project:LOCATION.job_id,
// the form BigQuery itself uses.
func ResultID(ref api.JobRef) string { return ref.Project + ":" + ref.Location + "." + ref.ID }

// ParseResultID reads a ResultID back. A project ID can carry a domain
// ("example.com:proj"), so the last colon separates the project.
func ParseResultID(id string) (api.JobRef, error) {
	i := strings.LastIndexByte(id, ':')
	if i <= 0 {
		return api.JobRef{}, ErrBadResultID
	}
	loc, job, ok := strings.Cut(id[i+1:], ".")
	if !ok || loc == "" || job == "" || strings.ContainsAny(job, ".:/ ") {
		return api.JobRef{}, ErrBadResultID
	}
	return api.JobRef{Project: id[:i], Location: loc, ID: job}, nil
}

// AgentResults reads rows of an earlier query's result by its result ID.
func (e *Engine) AgentResults(ctx context.Context, resultID string, start int64, limit int) (api.QueryResult, error) {
	ref, err := ParseResultID(resultID)
	if err != nil {
		return api.QueryResult{}, err
	}
	page, err := e.runs.ReadResult(ctx, ref, start, rowLimit(limit))
	if err != nil {
		return api.QueryResult{}, err
	}
	res := api.QueryResult{ResultID: resultID, Columns: format.Columns(page.Schema), TotalRows: page.TotalRows}
	cells := make([][]api.Cell, len(page.Rows))
	for i, raw := range page.Rows {
		cells[i] = format.Row(page.Schema, raw, format.Options{MaxChars: policy.CellChars, KeepNewlines: true})
	}
	fillRows(&res, cells, start)
	return res, nil
}

// ParsePath reads a table path: project.dataset.table, optionally in
// backticks, or dataset.table in the Execution project. A shard family can be
// named prefix* or prefixYYYYMMDD (its newest member answers).
func (e *Engine) ParsePath(s string) (api.ObjectPath, bool) {
	s = strings.TrimSpace(s)
	if p, ok := search.SplitPath(s); ok {
		return wildcard(p), true
	}
	parts := strings.Split(strings.Trim(s, "`"), ".")
	if exec := e.execProject(); len(parts) == 2 && parts[0] != "" && parts[1] != "" && exec != "" {
		return wildcard(api.ObjectPath{Project: exec, Dataset: parts[0], Table: parts[1]}), true
	}
	return api.ObjectPath{}, false
}

func wildcard(p api.ObjectPath) api.ObjectPath {
	if strings.HasSuffix(p.Table, "*") {
		p.Table = strings.TrimSuffix(p.Table, "*") + "YYYYMMDD"
	}
	return p
}

// Schema returns a table's schema from the catalog.
func (e *Engine) Schema(ctx context.Context, path string) (api.TableSchema, error) {
	p, ok := e.ParsePath(path)
	if !ok {
		return api.TableSchema{}, fmt.Errorf("%q is not a table path (want project.dataset.table)", path)
	}
	d, err := e.TableDetail(ctx, p)
	if errors.Is(err, ErrUnknownTable) {
		return api.TableSchema{}, fmt.Errorf("%w: %s is not in the catalog (check the name with search, or sync)", ErrUnknownTable, p)
	}
	if err != nil {
		return api.TableSchema{}, err
	}
	sc := SchemaOf(d)
	sc.Selected = e.Selection().Has(d.Path.Project)
	sc.Warnings, err = e.StaleWarnings(ctx, []string{d.Path.Project})
	return sc, err
}

// SchemaOf is the compact schema of a table detail.
func SchemaOf(d api.TableDetail) api.TableSchema {
	t := d.Table
	s := api.TableSchema{Table: d.Path.String(), Type: t.Type, Location: d.Location, Description: t.Description,
		Labels: t.Labels, Shard: d.Shard, ViewSQL: t.ViewSQL, Columns: []api.SchemaColumn{},
		NamesOnly: d.NamesOnly || t.NamesOnly, SyncedAt: d.SyncedAt, Stale: d.Stale}
	cols := append([]api.ColumnMeta(nil), t.Columns...)
	sort.SliceStable(cols, func(i, j int) bool { return cols[i].Ordinal < cols[j].Ordinal })
	for _, c := range cols {
		s.Columns = append(s.Columns, api.SchemaColumn{Name: c.FieldPath, Type: c.DataType, Description: c.Description,
			Required: c.Required, Partition: c.Partitioning, Clustering: c.ClusteringOrdinal})
	}
	return s
}

// CatalogSearch is the compact catalog search of the agent surface.
func (e *Engine) CatalogSearch(ctx context.Context, query string, limit, offset int) (api.CatalogHits, error) {
	if e.catalog == nil {
		return api.CatalogHits{}, ErrNoCatalog
	}
	resp, err := e.Search(ctx, query, api.SearchOptions{Limit: limit, Offset: offset, PreferSelected: true})
	if err != nil {
		return api.CatalogHits{}, err
	}
	sel := e.Selection()
	var touched []string
	out := api.CatalogHits{Hits: []api.CatalogHit{}, Total: resp.Total}
	for _, f := range resp.Query.Filters {
		if f.Status != api.FilterOK {
			out.Ignored = append(out.Ignored, f.Key+": ("+string(f.Status)+")")
		}
	}
	for _, r := range resp.Results {
		h := api.CatalogHit{Path: r.Path.String(), Kind: r.Kind, Type: r.Type, Description: r.Description,
			MoreColumns: r.MoreColumns, Stale: r.Stale, Selected: sel.Has(r.Path.Project)}
		touched = append(touched, r.Path.Project)
		if r.Kind == api.KindShards && r.Shard != nil {
			p := r.Path
			p.Table = r.Shard.Prefix + "*"
			h.Path = p.String()
			h.Shard = &api.ShardInfo{First: r.Shard.First, Last: r.Shard.Last, Count: r.Shard.Count}
		}
		for _, c := range r.ColumnHits {
			h.Columns = append(h.Columns, c.FieldPath+" "+c.DataType)
		}
		out.Hits = append(out.Hits, h)
	}
	if next := offset + len(resp.Results); next < resp.Total {
		out.Truncated, out.NextCursor = true, strconv.Itoa(next)
	}
	out.Warnings, err = e.StaleWarnings(ctx, touched)
	return out, err
}

// StaleWarnings warns about each project (once, A–Z) whose catalog is older
// than sync_ttl, saying when it is not selected (so nothing refreshes it).
func (e *Engine) StaleWarnings(ctx context.Context, projects []string) ([]string, error) {
	cached, err := e.cachedProjects(ctx)
	if err != nil {
		return nil, err
	}
	sel := e.Selection()
	now, ttl := e.o.Now(), e.Config().SyncTTL
	seen := map[string]bool{}
	var ps []string
	for _, p := range projects {
		if !seen[p] {
			seen[p] = true
			ps = append(ps, p)
		}
	}
	sort.Strings(ps)
	var out []string
	for _, p := range ps {
		at, ok := syncedAt(cached[p])
		if ok && now.Sub(at) <= ttl {
			continue
		}
		msg := "catalog for " + p + " is incomplete (a sync never finished)"
		if ok {
			msg = fmt.Sprintf("catalog for %s is %s old (sync_ttl %s)", p, format.Age(now.Sub(at)), format.Age(ttl))
		}
		if !sel.Has(p) {
			msg += "; it is not selected, so it is not refreshed"
		}
		out = append(out, msg+": `bqt sync --project "+p+"` refreshes it")
	}
	return out, nil
}

// ErrNoCatalog is returned when there is no catalog yet.
var ErrNoCatalog = errors.New("no catalog yet: run `bqt sync`")

// FavoriteList lists (or, with a query, searches) the Favorites.
func (e *Engine) FavoriteList(query string, limit, offset int, withSQL bool) (api.FavoriteList, error) {
	favs, err := e.ListFavorites()
	if err != nil {
		return api.FavoriteList{}, err
	}
	if strings.TrimSpace(query) != "" {
		ms := favorites.Search(favs, query)
		favs = favs[:0]
		for _, m := range ms {
			favs = append(favs, m.Favorite)
		}
	}
	out := api.FavoriteList{Favorites: []api.FavoriteSummary{}, Total: len(favs)}
	if offset > len(favs) {
		offset = len(favs)
	}
	end := offset + limit
	if limit <= 0 || end > len(favs) {
		end = len(favs)
	}
	for _, f := range favs[offset:end] {
		s := api.FavoriteSummary{ID: f.ID, Description: f.Description, Tags: f.Tags, Params: favorites.Schema(f), Problems: f.Problems}
		if withSQL {
			s.SQL = f.SQL
		}
		out.Favorites = append(out.Favorites, s)
	}
	if end < len(favs) {
		out.Truncated, out.NextCursor = true, strconv.Itoa(end)
	}
	return out, nil
}

// ReadFavoriteIn reads a Favorite under cfg's favorites_dir without an engine.
func ReadFavoriteIn(cfg *config.Config, id string) (api.Favorite, error) {
	return favorites.Dir(cfg.FavoritesDir).Read(id)
}

// historySQLChars is where a History item's SQL is cut.
const historySQLChars = 2000

// HistoryList searches History (spec 11 syntax) and returns compact items.
func (e *Engine) HistoryList(ctx context.Context, query string, limit, offset int) (api.HistoryList, error) {
	page, err := e.SearchHistory(ctx, query, limit, offset)
	if err != nil {
		return api.HistoryList{}, err
	}
	out := api.HistoryList{Items: []api.HistoryItem{}, Total: page.Total}
	for _, f := range page.Query.Filters {
		if f.Status != api.FilterOK {
			out.Ignored = append(out.Ignored, f.Key+": ("+string(f.Status)+")")
		}
	}
	for _, g := range page.Groups {
		l := g.Latest
		it := api.HistoryItem{ID: l.ID, SQL: l.SQL, State: l.State, Error: l.Error, Created: l.Created, Source: l.Source,
			Favorite: l.FavoritePath, Runs: g.Runs, BytesProcessed: l.BytesProcessed}
		if c := format.Cell(bq.Field{Type: "STRING"}, l.SQL, format.Options{MaxChars: historySQLChars, KeepNewlines: true}); c.Truncated {
			it.SQL, it.SQLTruncated = c.Text, true
		}
		if l.State == api.RunDone && l.Job.ID != "" {
			it.ResultID = ResultID(l.Job)
		}
		out.Items = append(out.Items, it)
	}
	if page.More {
		out.Truncated, out.NextCursor = true, strconv.Itoa(offset+len(page.Groups))
	}
	return out, nil
}

// Freshness is the compact catalog status of the agent surface. The agent
// surface never syncs: the advice tells the person how to.
func (e *Engine) Freshness(ctx context.Context) (api.CatalogFreshness, error) {
	st, err := e.Status(ctx)
	if err != nil {
		return api.CatalogFreshness{}, err
	}
	out := api.CatalogFreshness{SyncNeeded: st.SyncNeeded, SyncReason: st.SyncReason, SyncRunning: st.SyncRunning,
		SyncTTL: st.SyncTTL, Datasets: st.Datasets, Tables: st.Tables, Projects: []api.ProjectFreshness{}}
	stale := false
	for _, p := range st.Projects {
		pf := api.ProjectFreshness{Project: p.Project, Selected: p.Selected, SyncedAt: p.SyncedAt, Stale: p.Stale}
		stale = stale || (p.Stale && p.Selected)
		for _, u := range p.Units {
			if u.Status != api.UnitOK && u.Status != "" {
				where := u.Location
				if where == "" {
					where = "discovery"
				}
				pf.Problems = append(pf.Problems, fmt.Sprintf("%s: %s %s", where, u.Status, u.Error))
			}
		}
		out.Projects = append(out.Projects, pf)
	}
	switch {
	case st.SyncRunning:
		out.Advice = "a sync is running in another bqt process; results fill in as it commits"
	case st.SyncNeeded || stale:
		out.Advice = "ask the user to run `bqt sync` (or open the bqt TUI, which refreshes in the background); this server cannot sync"
	}
	return out, nil
}
