package api

import "time"

type HistoryEntry struct {
	ID               int64        `json:"id"`
	Job              JobRef       `json:"job"`
	ExecutionProject string       `json:"execution_project"`
	SQL              string       `json:"sql"`
	StatementType    string       `json:"statement_type,omitempty"`
	Destination      string       `json:"destination,omitempty"`
	State            RunState     `json:"state"`
	Error            string       `json:"error,omitempty"`
	BytesProcessed   *int64       `json:"bytes_processed,omitempty"`
	BytesBilled      *int64       `json:"bytes_billed,omitempty"`
	CacheHit         bool         `json:"cache_hit,omitempty"`
	Created          time.Time    `json:"created"`
	Started          *time.Time   `json:"started,omitempty"`
	Ended            *time.Time   `json:"ended,omitempty"`
	DurationMs       *int64       `json:"duration_ms,omitempty"`
	SlotMs           *int64       `json:"slot_ms,omitempty"`
	ReservationID    string       `json:"reservation_id,omitempty"`
	Edition          string       `json:"edition,omitempty"`
	Source           string       `json:"source"`
	FavoritePath     string       `json:"favorite_path,omitempty"`
	Params           []QueryParam `json:"params,omitempty"`
}

func (e HistoryEntry) Capacity() bool { return e.ReservationID != "" }

type HistoryGroup struct {
	Latest      HistoryEntry `json:"latest"`
	Runs        int          `json:"runs"`
	BytesBilled int64        `json:"bytes_billed"`
	SlotMs      int64        `json:"slot_ms"`
	Spans       []Span       `json:"spans,omitempty"`
	Key         string       `json:"key"`
}

type HistoryPage struct {
	Query  ParsedQuery    `json:"query"`
	Groups []HistoryGroup `json:"groups"`
	More   bool           `json:"more,omitempty"`
	Total  int            `json:"total"`
}

type Favorite struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Params      []ParamSpec `json:"params,omitempty"`
	SQL         string      `json:"sql"`
	Text        string      `json:"-"`
	Path        string      `json:"-"`
	ModTime     time.Time   `json:"mod_time"`
	Problems    []string    `json:"problems,omitempty"`
}

type ParamSpec struct {
	Name    string  `json:"name"`
	Type    string  `json:"type,omitempty"`
	Default *string `json:"default,omitempty"`
}

type FavoriteMatch struct {
	Favorite  Favorite `json:"favorite"`
	Score     int      `json:"score"`
	NameSpans []Span   `json:"name_spans,omitempty"`
}
