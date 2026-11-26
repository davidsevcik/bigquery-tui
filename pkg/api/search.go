package api

import "time"

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ObjectPath struct {
	Project string `json:"project"`
	Dataset string `json:"dataset,omitempty"`
	Table   string `json:"table,omitempty"`
}

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

type FilterStatus string

const (
	FilterOK      FilterStatus = "ok"
	FilterPending FilterStatus = "pending"
	FilterInvalid FilterStatus = "invalid"
)

type QueryFilter struct {
	Key       string       `json:"key"`
	Values    []string     `json:"values"`
	Negated   bool         `json:"negated,omitempty"`
	Status    FilterStatus `json:"status"`
	Span      Span         `json:"span"`
	ValueSpan Span         `json:"value_span"`
}

type QueryTerm struct {
	Text string `json:"text"`
	Span Span   `json:"span"`
}

type ParsedQuery struct {
	Raw     string        `json:"raw"`
	Terms   []QueryTerm   `json:"terms,omitempty"`
	Filters []QueryFilter `json:"filters,omitempty"`
	Path    *ObjectPath   `json:"path,omitempty"`
}

func (q ParsedQuery) Empty() bool { return len(q.Terms) == 0 && len(q.Filters) == 0 && q.Path == nil }

type ResultKind string

const (
	KindProject ResultKind = "project"
	KindDataset ResultKind = "dataset"
	KindTable   ResultKind = "table"
	KindShards  ResultKind = "shard_family"
)

type ColumnHit struct {
	FieldPath   string `json:"field_path"`
	DataType    string `json:"data_type,omitempty"`
	Description string `json:"description,omitempty"`
	Spans       []Span `json:"spans,omitempty"`
	DescSpans   []Span `json:"desc_spans,omitempty"`
}

type SearchResult struct {
	Kind         ResultKind        `json:"kind"`
	Path         ObjectPath        `json:"path"`
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
	Pending      bool              `json:"pending,omitempty"`
	Syncing      bool              `json:"syncing,omitempty"`
	Stale        bool              `json:"stale,omitempty"`
	SyncedAt     *time.Time        `json:"synced_at,omitempty"`
	Selected     bool              `json:"selected"`
}

type SearchOptions struct {
	Limit          int         `json:"limit,omitempty"`
	Offset         int         `json:"offset,omitempty"`
	TablesOnly     bool        `json:"tables_only,omitempty"`
	Scope          *ObjectPath `json:"scope,omitempty"`
	Projects       []string    `json:"projects,omitempty"`
	AllWhenEmpty   bool        `json:"all_when_empty,omitempty"`
	ExpandShards   bool        `json:"expand_shards,omitempty"`
	SelectedOnly   bool        `json:"selected_only,omitempty"`
	PreferSelected bool        `json:"prefer_selected,omitempty"`
	Hidden         []string    `json:"-"`
	Prefer         []string    `json:"-"`
}

type FilterOptions struct {
	Scope        *ObjectPath  `json:"scope,omitempty"`
	Projects     []string     `json:"projects,omitempty"`
	Kinds        []ResultKind `json:"kinds,omitempty"`
	ExpandShards bool         `json:"expand_shards,omitempty"`
	SelectedOnly bool         `json:"selected_only,omitempty"`
	Hidden       []string     `json:"-"`
}

type FilterMatch struct {
	Kind  ResultKind   `json:"kind"`
	Path  ObjectPath   `json:"path"`
	Shard *ShardFamily `json:"shard_family,omitempty"`
}

type FilterResponse struct {
	Query   ParsedQuery   `json:"query"`
	Matches []FilterMatch `json:"matches"`
	TookMS  float64       `json:"took_ms"`
}

type SearchResponse struct {
	Query    ParsedQuery    `json:"query"`
	Results  []SearchResult `json:"results"`
	Total    int            `json:"total"`
	Recent   bool           `json:"recent,omitempty"`
	TookMS   float64        `json:"took_ms"`
	Warnings []string       `json:"warnings,omitempty"`
}

type BrowseNode struct {
	Kind         ResultKind        `json:"kind"`
	Path         ObjectPath        `json:"path"`
	Name         string            `json:"name"`
	Type         TableType         `json:"type,omitempty"`
	Description  string            `json:"description,omitempty"`
	Location     string            `json:"location,omitempty"`
	Children     int               `json:"children"`
	Shard        *ShardFamily      `json:"shard_family,omitempty"`
	FriendlyName string            `json:"friendly_name,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Status       DatasetStatus     `json:"status,omitempty"`
	Syncing      bool              `json:"syncing,omitempty"`
	NamesOnly    bool              `json:"names_only,omitempty"`
	Stale        bool              `json:"stale,omitempty"`
	SyncedAt     *time.Time        `json:"synced_at,omitempty"`
	UnitStatus   UnitStatus        `json:"unit_status,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type TableDetail struct {
	Path       ObjectPath   `json:"path"`
	Table      TableMeta    `json:"table"`
	Location   string       `json:"location"`
	SyncedAt   *time.Time   `json:"synced_at,omitempty"`
	Stale      bool         `json:"stale,omitempty"`
	Status     UnitStatus   `json:"status,omitempty"`
	Error      string       `json:"error,omitempty"`
	NamesOnly  bool         `json:"names_only,omitempty"`
	Shard      *ShardFamily `json:"shard_family,omitempty"`
	SchemaFrom string       `json:"schema_from,omitempty"`
}

type TableStats struct {
	Rows         int64      `json:"rows"`
	Bytes        int64      `json:"bytes"`
	LastModified *time.Time `json:"last_modified,omitempty"`
	Required     []string   `json:"required,omitempty"`
}

type Cell struct {
	Text      string `json:"text"`
	Null      bool   `json:"null,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type PreviewColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode,omitempty"`
}

type PreviewPage struct {
	Columns    []PreviewColumn `json:"columns"`
	Rows       [][]Cell        `json:"rows"`
	StartIndex int64           `json:"start_index"`
	TotalRows  int64           `json:"total_rows"`
}
