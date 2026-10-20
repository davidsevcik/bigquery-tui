// Package mcp is bqt's MCP server (spec 14.2): eight tools over stdio, each a
// thin wrapper over an engine call and its api type. Every query runs under
// the agent policy (spec 14.3) inside the engine; tool annotations are hints
// for the client, never the enforcement.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
)

// Defaults of the list tools.
const (
	defaultList = 20
	maxList     = 200
)

// Instructions are sent to the client at initialization. Claude Code cuts
// them (and tool descriptions) at 2,048 characters.
const Instructions = `bqt gives read-only access to the user's BigQuery projects through a local metadata catalog.

Workflow: search_catalog to find tables (filters p: d: type: col: desc: label:), get_schema for columns, dry_run to check cost, run_query to run a SELECT, get_results for more rows. Reuse saved queries with list_favorites and past runs with search_history.

Write table names in SQL as ` + "`project.dataset.table`" + `. A date-sharded family (shard_family) is queried as ` + "`project.dataset.prefix*`" + ` with _TABLE_SUFFIX.

Limits, enforced by bqt: only SELECT; at most the configured byte limit per query (checked on the dry-run); a timeout; up to 1000 rows per call, cells cut at 200 characters. A refusal says why; tool arguments can only lower limits. Tell the user when a query needs the bqt TUI or a config change.

The catalog is only as fresh as the last sync; this server cannot sync. If catalog_status says it is stale or a table is missing, ask the user to run ` + "`bqt sync`" + `.`

// Server serves the tools of one engine. When the server starts before the
// first sync, the engine is reopened once a catalog exists.
type Server struct {
	open func(context.Context) (*engine.Engine, error)

	mu   sync.Mutex
	eng  *engine.Engine
	old  []*engine.Engine // replaced engines, closed with the server
	wctx context.Context  // lives until Close: the catalog watchers
	stop context.CancelFunc
}

// New builds a server around an engine opener (read-only, source mcp).
func New(ctx context.Context, open func(context.Context) (*engine.Engine, error)) (*Server, error) {
	eng, err := open(ctx)
	if err != nil {
		return nil, err
	}
	wctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s := &Server{open: open, eng: eng, wctx: wctx, stop: stop}
	s.watch(eng)
	return s, nil
}

// watch keeps an engine's search index current: another process's sync
// changes the catalog under this long-lived server.
func (s *Server) watch(eng *engine.Engine) {
	ch := eng.WatchCatalog(s.wctx, 2*time.Second)
	go func() {
		for range ch {
		}
	}()
}

// Close releases the engines.
func (s *Server) Close() error {
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, e := range append(s.old, s.eng) {
		errs = append(errs, e.Close())
	}
	return errors.Join(errs...)
}

// engine returns the engine, reopening it when it was opened without a
// catalog and one exists now.
func (s *Server) engine(ctx context.Context) *engine.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eng.Catalog() != nil {
		return s.eng
	}
	if fresh, err := s.open(ctx); err == nil {
		if fresh.Catalog() == nil {
			fresh.Close()
			return s.eng
		}
		s.old = append(s.old, s.eng) // a query may still run on it
		s.eng = fresh
		s.watch(fresh)
	}
	return s.eng
}

// MCP builds the protocol server with the eight tools.
func (s *Server) MCP(version string) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "bqt", Title: "bqt (BigQuery)", Version: version},
		&sdk.ServerOptions{Instructions: Instructions})
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	bq := &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}

	sdk.AddTool(srv, &sdk.Tool{Name: "search_catalog", Annotations: ro, Description: `Search the local catalog of projects, datasets, tables and columns. Free terms match names (prefix, then substring, then description); filters: p:project d:dataset type:table|view|... col:column desc:text label:key:value; -filter negates, a,b is OR. A pasted project.dataset.table is an exact lookup. Paged: pass next_cursor as cursor.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in searchIn) (*sdk.CallToolResult, api.CatalogHits, error) {
			off, err := cursor(in.Cursor)
			if err != nil {
				return nil, api.CatalogHits{}, err
			}
			out, err := s.engine(ctx).CatalogSearch(ctx, in.Query, listLimit(in.Limit), off)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "get_schema", Annotations: ro, Description: `Columns (nested fields as dotted paths), types, descriptions, partitioning and clustering of a table or view, from the catalog. Accepts project.dataset.table, dataset.table (Execution project) or a shard family as prefix*.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in schemaIn) (*sdk.CallToolResult, api.TableSchema, error) {
			out, err := s.engine(ctx).Schema(ctx, in.Table)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "catalog_status", Annotations: ro, Description: `How fresh the local catalog is: per project the last sync and sync problems. When it is stale or a table is missing, ask the user to run bqt sync.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, api.CatalogFreshness, error) {
			out, err := s.engine(ctx).Freshness(ctx)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "dry_run", Annotations: bq, Description: `Validate a query without running it: statement type, bytes it would process, and whether run_query would allow it (allowed, refusal). Free. Use it before large queries.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in dryIn) (*sdk.CallToolResult, api.DryRunResult, error) {
			q, err := query(in.SQL, in.Params, 0, in.MaxBytes, 0)
			if err != nil {
				return nil, api.DryRunResult{}, err
			}
			out, err := s.engine(ctx).AgentDryRun(ctx, q)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "run_query", Annotations: bq, Description: `Run a read-only SELECT (dry-run first; refused over the byte limit or for any other statement). Returns statistics, columns and up to limit rows (default 100, max 1000; cells cut at 200 characters, NULL as null). When truncated, read more with get_results(result_id, next_cursor). Parameters: @name in the SQL, params {"name": "value"}; a TYPE: prefix forces the type (DATE:2024-01-31).`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in runIn) (*sdk.CallToolResult, api.QueryResult, error) {
			q, err := query(in.SQL, in.Params, in.Limit, in.MaxBytes, in.TimeoutSeconds)
			if err != nil {
				return nil, api.QueryResult{}, err
			}
			out, err := s.engine(ctx).RunAgentQuery(ctx, q)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "get_results", Annotations: bq, Description: `Read more rows of an earlier query by its result_id, from next_cursor (a row number). BigQuery keeps results for about 24 hours. Default limit 100, max 1000.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in resultsIn) (*sdk.CallToolResult, api.QueryResult, error) {
			off, err := cursor(in.Cursor)
			if err != nil {
				return nil, api.QueryResult{}, err
			}
			out, err := s.engine(ctx).AgentResults(ctx, in.ResultID, int64(off), in.Limit)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "list_favorites", Annotations: ro, Description: `The user's saved queries (Favorites): id, description, tags and parameters (name, type, default). query filters them (fuzzy over name, tags, description and SQL); include_sql adds the SQL. Read-only.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in favIn) (*sdk.CallToolResult, api.FavoriteList, error) {
			off, err := cursor(in.Cursor)
			if err != nil {
				return nil, api.FavoriteList{}, err
			}
			out, err := s.engine(ctx).FavoriteList(in.Query, listLimit(in.Limit), off, in.IncludeSQL)
			return nil, out, toolErr(err)
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "search_history", Annotations: ro, Description: `Queries run before (newest first, identical SQL grouped). Free terms match the SQL; filters status:done|failed|cancelled, source:tui|cli|mcp, project:, since:7d or since:2026-10-01. A done entry carries a result_id for get_results while BigQuery keeps it.`},
		func(ctx context.Context, _ *sdk.CallToolRequest, in historyIn) (*sdk.CallToolResult, api.HistoryList, error) {
			off, err := cursor(in.Cursor)
			if err != nil {
				return nil, api.HistoryList{}, err
			}
			out, err := s.engine(ctx).HistoryList(ctx, in.Query, listLimit(in.Limit), off)
			return nil, out, toolErr(err)
		})
	return srv
}

// Run serves the tools over stdio until the client disconnects or ctx ends.
func (s *Server) Run(ctx context.Context, version string) error {
	return s.MCP(version).Run(ctx, &sdk.StdioTransport{})
}

type searchIn struct {
	Query  string `json:"query" jsonschema:"search text and filters; empty lists recently opened tables"`
	Limit  int    `json:"limit,omitempty" jsonschema:"results per page (default 20, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

type schemaIn struct {
	Table string `json:"table" jsonschema:"project.dataset.table"`
}

type dryIn struct {
	SQL      string            `json:"sql" jsonschema:"GoogleSQL query"`
	Params   map[string]string `json:"params,omitempty" jsonschema:"values of @name parameters"`
	MaxBytes int64             `json:"max_bytes,omitempty" jsonschema:"a lower byte limit to check against"`
}

type runIn struct {
	SQL            string            `json:"sql" jsonschema:"GoogleSQL SELECT"`
	Params         map[string]string `json:"params,omitempty" jsonschema:"values of @name parameters"`
	Limit          int               `json:"limit,omitempty" jsonschema:"rows to return (default 100, max 1000)"`
	MaxBytes       int64             `json:"max_bytes,omitempty" jsonschema:"a lower byte limit for this query"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"a shorter timeout for this query"`
}

type resultsIn struct {
	ResultID string `json:"result_id" jsonschema:"result_id from run_query or search_history"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"row to start at (next_cursor)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"rows to return (default 100, max 1000)"`
}

type favIn struct {
	Query      string `json:"query,omitempty" jsonschema:"filter text"`
	IncludeSQL bool   `json:"include_sql,omitempty" jsonschema:"include each Favorite's SQL"`
	Limit      int    `json:"limit,omitempty" jsonschema:"results per page (default 20, max 200)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

type historyIn struct {
	Query  string `json:"query,omitempty" jsonschema:"terms and filters"`
	Limit  int    `json:"limit,omitempty" jsonschema:"results per page (default 20, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

func ptr[T any](v T) *T { return &v }

func listLimit(n int) int {
	switch {
	case n <= 0:
		return defaultList
	case n > maxList:
		return maxList
	}
	return n
}

func cursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cursor %q: pass next_cursor as it was returned", s)
	}
	return n, nil
}

func query(sql string, params map[string]string, limit int, maxBytes int64, timeoutSec int) (engine.AgentQuery, error) {
	ps, err := engine.BindParams(sql, nil, params)
	if err != nil {
		return engine.AgentQuery{}, err
	}
	if maxBytes < 0 || timeoutSec < 0 || limit < 0 {
		return engine.AgentQuery{}, errors.New("limit, max_bytes and timeout_seconds must not be negative")
	}
	return engine.AgentQuery{SQL: sql, Params: ps, Limit: limit, MaxBytes: maxBytes, Timeout: time.Duration(timeoutSec) * time.Second}, nil
}

// toolErr words an error for the agent: a refusal says what to do instead.
func toolErr(err error) error {
	var pe *policy.Error
	if errors.As(err, &pe) {
		return fmt.Errorf("refused (%s): %s", pe.Refusal.Code, pe.Refusal.Message)
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("cancelled")
	}
	return err
}
