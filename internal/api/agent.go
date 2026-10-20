package api

import "time"

// The types in this file are the agent surface's results: what the JSON CLI
// prints and what the MCP tools return (spec 14). Both frontends use the same
// shapes, so a script written against `bqt query` reads `run_query` too.
// They are compact on purpose: an agent pays for every token.

// Refusal is the safety policy saying no (spec 14.3). A refused query never
// starts a job.
type Refusal struct {
	// Code is "not_read_only" (the statement is not a SELECT),
	// "unknown_estimate" (the dry-run could not bound the bytes),
	// "over_limit" (the estimate is over agent_max_bytes, or a lower limit
	// asked for) or "needs_confirmation" (the estimate is over confirm_bytes,
	// which only the TUI can confirm).
	Code          string `json:"code"`
	Message       string `json:"message"`
	StatementType string `json:"statement_type,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	Limit         int64  `json:"limit,omitempty"`
}

// Refusal codes.
const (
	RefuseNotReadOnly       = "not_read_only"
	RefuseUnknownEstimate   = "unknown_estimate"
	RefuseOverLimit         = "over_limit"
	RefuseNeedsConfirmation = "needs_confirmation"
)

// DryRunResult is the estimate of a query plus the policy's verdict on it.
type DryRunResult struct {
	StatementType string `json:"statement_type"`
	Bytes         int64  `json:"bytes"`
	// Accuracy is PRECISE, LOWER_BOUND, UPPER_BOUND or UNKNOWN.
	Accuracy string `json:"accuracy,omitempty"`
	// Capacity is set when the job would run on a reservation (bytes are not
	// billed); ApproxUSD is the on-demand price otherwise.
	Capacity      bool            `json:"capacity"`
	ReservationID string          `json:"reservation_id,omitempty"`
	ApproxUSD     *float64        `json:"approx_usd,omitempty"`
	Columns       []PreviewColumn `json:"columns,omitempty"`
	// Allowed tells whether run_query / bqt query would run it; Refusal says
	// why not.
	Allowed bool     `json:"allowed"`
	Refusal *Refusal `json:"refusal,omitempty"`
	// Limit is the agent byte limit the estimate was checked against.
	Limit int64 `json:"limit"`
}

// QueryResult is a finished query's statistics and one page of its rows.
type QueryResult struct {
	// ResultID reads further pages (get_results, bqt results) for about 24
	// hours, while BigQuery keeps the result. It is the job's
	// project:LOCATION.job_id.
	ResultID      string          `json:"result_id,omitempty"`
	StatementType string          `json:"statement_type,omitempty"`
	Stats         *QueryStats     `json:"stats,omitempty"`
	Columns       []PreviewColumn `json:"columns"`
	// Rows hold each cell as text (INT64 and NUMERIC exact, STRUCT and JSON
	// as compact JSON) or null. Cells longer than the cell limit are cut and
	// end with "…"; TruncatedCells counts them.
	Rows           [][]*string `json:"rows"`
	StartIndex     int64       `json:"start_index"`
	TotalRows      int64       `json:"total_rows"`
	TruncatedCells int         `json:"truncated_cells,omitempty"`
	// Truncated is set when more rows follow; NextCursor reads them.
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// QueryStats are the job statistics an agent needs.
type QueryStats struct {
	BytesProcessed int64 `json:"bytes_processed"`
	// BytesBilled is left out under capacity pricing, where it is no charge.
	BytesBilled  int64 `json:"bytes_billed,omitempty"`
	SlotMs       int64 `json:"slot_ms,omitempty"`
	CacheHit     bool  `json:"cache_hit,omitempty"`
	Capacity     bool  `json:"capacity,omitempty"`
	AffectedRows int64 `json:"affected_rows,omitempty"`
	DurationMs   int64 `json:"duration_ms,omitempty"`
}

// SchemaColumn is one field path of a table's schema.
type SchemaColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Partition   bool   `json:"partitioning,omitempty"`
	Clustering  int    `json:"clustering,omitempty"`
}

// TableSchema is a table's schema from the catalog (get_schema, bqt schema,
// the TUI's "copy schema as JSON").
type TableSchema struct {
	Table       string            `json:"table"`
	Type        TableType         `json:"type"`
	Location    string            `json:"location,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	// Shard is set for a date-sharded family; the schema is its newest
	// member's.
	Shard   *ShardFamily   `json:"shard_family,omitempty"`
	ViewSQL string         `json:"view_sql,omitempty"`
	Columns []SchemaColumn `json:"columns"`
	// NamesOnly means sync could read the table's name but not its columns.
	NamesOnly bool       `json:"names_only,omitempty"`
	SyncedAt  *time.Time `json:"synced_at,omitempty"`
	Stale     bool       `json:"stale,omitempty"`
}

// CatalogHit is one catalog search result.
type CatalogHit struct {
	Path string `json:"path"`
	// Kind is project, dataset, table or shard_family (date-sharded tables:
	// Path is then the wildcard table prefix*, which queries every shard).
	Kind        ResultKind `json:"kind"`
	Type        TableType  `json:"type,omitempty"`
	Description string     `json:"description,omitempty"`
	Shard       *ShardInfo `json:"shard_family,omitempty"`
	// Columns are the matched columns (col: filters and column-name hits).
	Columns     []string `json:"columns,omitempty"`
	MoreColumns int      `json:"more_columns,omitempty"`
	Stale       bool     `json:"stale,omitempty"`
}

// ShardInfo summarises a shard family in a search hit.
type ShardInfo struct {
	First string `json:"first"`
	Last  string `json:"last"`
	Count int    `json:"count"`
}

// CatalogHits is a page of catalog search results.
type CatalogHits struct {
	Hits []CatalogHit `json:"hits"`
	// Ignored lists filters that were not understood and were left out.
	Ignored    []string `json:"ignored_filters,omitempty"`
	Total      int      `json:"total"`
	Truncated  bool     `json:"truncated"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// FavoriteSummary is a Favorite in a list.
type FavoriteSummary struct {
	ID          string      `json:"id"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Params      []ParamSpec `json:"params,omitempty"`
	// SQL is the body (without the header), only when asked for.
	SQL      string   `json:"sql,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// FavoriteList is a page of Favorites.
type FavoriteList struct {
	Favorites  []FavoriteSummary `json:"favorites"`
	Total      int               `json:"total"`
	Truncated  bool              `json:"truncated"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// HistoryItem is one History group in a list: its newest Run.
type HistoryItem struct {
	// ID is the newest Run's History entry.
	ID int64 `json:"id"`
	// SQL is cut at about 2,000 characters (SQLTruncated set).
	SQL          string    `json:"sql"`
	SQLTruncated bool      `json:"sql_truncated,omitempty"`
	State        RunState  `json:"state"`
	Error        string    `json:"error,omitempty"`
	Created      time.Time `json:"created"`
	Source       string    `json:"source"`
	Favorite     string    `json:"favorite,omitempty"`
	// Runs counts the Runs of this SQL text that match the filters.
	Runs           int    `json:"runs"`
	BytesProcessed *int64 `json:"bytes_processed,omitempty"`
	// ResultID reads the newest Run's result while BigQuery keeps it.
	ResultID string `json:"result_id,omitempty"`
}

// HistoryList is a page of History search results.
type HistoryList struct {
	Items      []HistoryItem `json:"items"`
	Ignored    []string      `json:"ignored_filters,omitempty"`
	Truncated  bool          `json:"truncated"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// CatalogFreshness is the catalog_status tool's compact freshness report.
type CatalogFreshness struct {
	SyncNeeded  bool               `json:"sync_needed"`
	SyncReason  string             `json:"sync_reason,omitempty"`
	SyncRunning bool               `json:"sync_running"`
	SyncTTL     string             `json:"sync_ttl"`
	Datasets    int                `json:"datasets"`
	Tables      int                `json:"tables"`
	Projects    []ProjectFreshness `json:"projects"`
	// Advice says what to do when the catalog is missing or stale.
	Advice string `json:"advice,omitempty"`
}

// ProjectFreshness is one configured project in CatalogFreshness.
type ProjectFreshness struct {
	Project  string     `json:"project"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
	// Problems lists the regions that failed to sync, with the error.
	Problems []string `json:"problems,omitempty"`
}
