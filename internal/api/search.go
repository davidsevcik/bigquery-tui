package api

import "time"

// Span is a half-open range of UTF-8 byte offsets [Start, End) into the text
// of the field it belongs to. Frontends that count in other units (UTF-16 in
// a JavaScript shell) convert themselves.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ObjectPath identifies a project, a dataset (Table empty) or a table.
type ObjectPath struct {
	Project string `json:"project"`
	Dataset string `json:"dataset,omitempty"`
	Table   string `json:"table,omitempty"`
}

// String renders the dotted path.
func (p ObjectPath) String() string {
	s := p.Project
	if p.Dataset != "" {
		s += "." + p.Dataset
		if p.Table != "" {
			s += "." + p.Table
		}
	}
	return s
}

// FilterStatus says whether a typed filter is usable yet.
type FilterStatus string

const (
	FilterOK      FilterStatus = "ok"
	FilterPending FilterStatus = "pending" // key typed, no value yet
	FilterInvalid FilterStatus = "invalid" // e.g. type:foo
)

// QueryFilter is one `key:value` token of a search query.
type QueryFilter struct {
	Key     string       `json:"key"`
	Values  []string     `json:"values"`
	Negated bool         `json:"negated,omitempty"`
	Status  FilterStatus `json:"status"`
	// Span is the token's position in the raw query; ValueSpan its value part.
	Span      Span `json:"span"`
	ValueSpan Span `json:"value_span"`
}

// QueryTerm is one free term of a search query.
type QueryTerm struct {
	Text string `json:"text"`
	Span Span   `json:"span"`
}

// ParsedQuery is the structured form of a search query, enough for a frontend
// to render filter chips and colour the input.
type ParsedQuery struct {
	Raw     string        `json:"raw"`
	Terms   []QueryTerm   `json:"terms,omitempty"`
	Filters []QueryFilter `json:"filters,omitempty"`
	// Path is set when the query is a pasted project.dataset.table.
	Path *ObjectPath `json:"path,omitempty"`
}

// Empty reports whether the query has no terms, filters or path.
func (q ParsedQuery) Empty() bool { return len(q.Terms) == 0 && len(q.Filters) == 0 && q.Path == nil }

// ResultKind is the kind of object a search result refers to.
type ResultKind string

const (
	KindProject ResultKind = "project"
	KindDataset ResultKind = "dataset"
	KindTable   ResultKind = "table"
	KindShards  ResultKind = "shard_family"
)

// ColumnHit is a column that explains why a table matched a `col:` filter.
type ColumnHit struct {
	FieldPath   string `json:"field_path"`
	DataType    string `json:"data_type,omitempty"`
	Description string `json:"description,omitempty"`
	Spans       []Span `json:"spans,omitempty"`      // into FieldPath
	DescSpans   []Span `json:"desc_spans,omitempty"` // into Description
}

// SearchResult is one ranked result. Spans are keyed by the field they index
// into: "name" (the object's own name), "dataset", "project", "description".
type SearchResult struct {
	Kind ResultKind `json:"kind"`
	Path ObjectPath `json:"path"`
	// Name is the object's own name (for a shard family: prefix + "YYYYMMDD").
	Name         string            `json:"name"`
	Type         TableType         `json:"type,omitempty"`
	Tier         string            `json:"tier,omitempty"`
	MatchedField string            `json:"matched_field,omitempty"`
	Spans        map[string][]Span `json:"spans,omitempty"`
	Description  string            `json:"description,omitempty"`
	ColumnHits   []ColumnHit       `json:"column_hits,omitempty"`
	MoreColumns  int               `json:"more_columns,omitempty"`
	Shard        *ShardFamily      `json:"shard_family,omitempty"`
	NamesOnly    bool              `json:"names_only,omitempty"`
	Stale        bool              `json:"stale,omitempty"`
	SyncedAt     *time.Time        `json:"synced_at,omitempty"`
}

// SearchOptions bounds a search.
type SearchOptions struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
	// TablesOnly drops project and dataset results (jump-to-table).
	TablesOnly bool `json:"tables_only,omitempty"`
	// Scope restricts results to a project or dataset (flat layout).
	Scope *ObjectPath `json:"scope,omitempty"`
	// AllWhenEmpty lists the whole (scoped) catalog for an empty query
	// instead of the recently opened tables.
	AllWhenEmpty bool `json:"all_when_empty,omitempty"`
}

// SearchResponse is a page of results.
type SearchResponse struct {
	Query   ParsedQuery    `json:"query"`
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
	// Recent is set when the results are the recently opened tables.
	Recent bool    `json:"recent,omitempty"`
	TookMS float64 `json:"took_ms"`
}

// BrowseNode is one row of the explorer's project / dataset / table lists.
type BrowseNode struct {
	Kind        ResultKind    `json:"kind"`
	Path        ObjectPath    `json:"path"`
	Name        string        `json:"name"`
	Type        TableType     `json:"type,omitempty"`
	Description string        `json:"description,omitempty"`
	Location    string        `json:"location,omitempty"`
	Children    int           `json:"children"`
	Shard       *ShardFamily  `json:"shard_family,omitempty"`
	Status      DatasetStatus `json:"status,omitempty"`
	NamesOnly   bool          `json:"names_only,omitempty"`
	Stale       bool          `json:"stale,omitempty"`
	SyncedAt    *time.Time    `json:"synced_at,omitempty"`
	// UnitStatus and Error describe sync health (projects and datasets).
	UnitStatus UnitStatus `json:"unit_status,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// TableDetail is everything the schema pane shows for a table.
type TableDetail struct {
	Path      ObjectPath   `json:"path"`
	Table     TableMeta    `json:"table"`
	Location  string       `json:"location"`
	SyncedAt  *time.Time   `json:"synced_at,omitempty"`
	Stale     bool         `json:"stale,omitempty"`
	Status    UnitStatus   `json:"status,omitempty"`
	Error     string       `json:"error,omitempty"`
	NamesOnly bool         `json:"names_only,omitempty"`
	Shard     *ShardFamily `json:"shard_family,omitempty"`
}

// TableStats are the live size figures fetched on demand (not in the catalog).
type TableStats struct {
	Rows         int64      `json:"rows"`
	Bytes        int64      `json:"bytes"`
	LastModified *time.Time `json:"last_modified,omitempty"`
}

// Cell is one formatted value. Text never contains control characters or ANSI.
type Cell struct {
	Text      string `json:"text"`
	Null      bool   `json:"null,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// PreviewColumn is a column header of a preview page.
type PreviewColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode,omitempty"`
}

// PreviewPage is a page of table rows fetched without running a query.
type PreviewPage struct {
	Columns    []PreviewColumn `json:"columns"`
	Rows       [][]Cell        `json:"rows"`
	StartIndex int64           `json:"start_index"`
	TotalRows  int64           `json:"total_rows"`
}
