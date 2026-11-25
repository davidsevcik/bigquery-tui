package api

import "time"

type Refusal struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	StatementType string `json:"statement_type,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	Limit         int64  `json:"limit,omitempty"`
}

const (
	RefuseNotReadOnly       = "not_read_only"
	RefuseUnknownEstimate   = "unknown_estimate"
	RefuseOverLimit         = "over_limit"
	RefuseNeedsConfirmation = "needs_confirmation"
)

type DryRunResult struct {
	StatementType string          `json:"statement_type"`
	Bytes         int64           `json:"bytes"`
	Accuracy      string          `json:"accuracy,omitempty"`
	Capacity      bool            `json:"capacity"`
	ReservationID string          `json:"reservation_id,omitempty"`
	ApproxUSD     *float64        `json:"approx_usd,omitempty"`
	Columns       []PreviewColumn `json:"columns,omitempty"`
	Allowed       bool            `json:"allowed"`
	Refusal       *Refusal        `json:"refusal,omitempty"`
	Limit         int64           `json:"limit"`
}

type QueryResult struct {
	ResultID       string          `json:"result_id,omitempty"`
	StatementType  string          `json:"statement_type,omitempty"`
	Stats          *QueryStats     `json:"stats,omitempty"`
	Columns        []PreviewColumn `json:"columns"`
	Rows           [][]*string     `json:"rows"`
	StartIndex     int64           `json:"start_index"`
	TotalRows      int64           `json:"total_rows"`
	TruncatedCells int             `json:"truncated_cells,omitempty"`
	Truncated      bool            `json:"truncated"`
	NextCursor     string          `json:"next_cursor,omitempty"`
}

type QueryStats struct {
	BytesProcessed int64 `json:"bytes_processed"`
	BytesBilled    int64 `json:"bytes_billed,omitempty"`
	SlotMs         int64 `json:"slot_ms,omitempty"`
	CacheHit       bool  `json:"cache_hit,omitempty"`
	Capacity       bool  `json:"capacity,omitempty"`
	AffectedRows   int64 `json:"affected_rows,omitempty"`
	DurationMs     int64 `json:"duration_ms,omitempty"`
}

type SchemaColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Partition   bool   `json:"partitioning,omitempty"`
	Clustering  int    `json:"clustering,omitempty"`
}

type TableSchema struct {
	Table       string            `json:"table"`
	Type        TableType         `json:"type"`
	Location    string            `json:"location,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Shard       *ShardFamily      `json:"shard_family,omitempty"`
	SchemaFrom  string            `json:"schema_from,omitempty"`
	ViewSQL     string            `json:"view_sql,omitempty"`
	Columns     []SchemaColumn    `json:"columns"`
	NamesOnly   bool              `json:"names_only,omitempty"`
	SyncedAt    *time.Time        `json:"synced_at,omitempty"`
	Stale       bool              `json:"stale,omitempty"`
	Selected    bool              `json:"selected"`
	Warnings    []string          `json:"warnings,omitempty"`
}

type CatalogHit struct {
	Path        string     `json:"path"`
	Kind        ResultKind `json:"kind"`
	Type        TableType  `json:"type,omitempty"`
	Description string     `json:"description,omitempty"`
	Shard       *ShardInfo `json:"shard_family,omitempty"`
	Columns     []string   `json:"columns,omitempty"`
	MoreColumns int        `json:"more_columns,omitempty"`
	Pending     bool       `json:"pending,omitempty"`
	Stale       bool       `json:"stale,omitempty"`
	Selected    bool       `json:"selected"`
}

type ShardInfo struct {
	Wildcard string `json:"wildcard"`
	Newest   string `json:"newest"`
	First    string `json:"first,omitempty"`
	Last     string `json:"last,omitempty"`
	Count    int    `json:"count"`
}

type CatalogHits struct {
	Hits       []CatalogHit `json:"hits"`
	Ignored    []string     `json:"ignored_filters,omitempty"`
	Total      int          `json:"total"`
	Truncated  bool         `json:"truncated"`
	NextCursor string       `json:"next_cursor,omitempty"`
	Warnings   []string     `json:"warnings,omitempty"`
}

type FavoriteSummary struct {
	ID          string      `json:"id"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Params      []ParamSpec `json:"params,omitempty"`
	SQL         string      `json:"sql,omitempty"`
	Problems    []string    `json:"problems,omitempty"`
}

type FavoriteList struct {
	Favorites  []FavoriteSummary `json:"favorites"`
	Total      int               `json:"total"`
	Truncated  bool              `json:"truncated"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type HistoryItem struct {
	ID             int64     `json:"id"`
	SQL            string    `json:"sql"`
	SQLTruncated   bool      `json:"sql_truncated,omitempty"`
	State          RunState  `json:"state"`
	Error          string    `json:"error,omitempty"`
	Created        time.Time `json:"created"`
	Source         string    `json:"source"`
	Favorite       string    `json:"favorite,omitempty"`
	Runs           int       `json:"runs"`
	BytesProcessed *int64    `json:"bytes_processed,omitempty"`
	ResultID       string    `json:"result_id,omitempty"`
}

type HistoryList struct {
	Items      []HistoryItem `json:"items"`
	Ignored    []string      `json:"ignored_filters,omitempty"`
	Total      int           `json:"total"`
	Truncated  bool          `json:"truncated"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type CatalogFreshness struct {
	SyncNeeded  bool               `json:"sync_needed"`
	SyncReason  string             `json:"sync_reason,omitempty"`
	SyncRunning bool               `json:"sync_running"`
	SyncTTL     string             `json:"sync_ttl"`
	Datasets    int                `json:"datasets"`
	Tables      int                `json:"tables"`
	Projects    []ProjectFreshness `json:"projects"`
	Advice      string             `json:"advice,omitempty"`
}

type ProjectFreshness struct {
	Project  string     `json:"project"`
	Selected bool       `json:"selected"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
	Problems []string   `json:"problems,omitempty"`
	Pending  int        `json:"pending_datasets,omitempty"`
}
