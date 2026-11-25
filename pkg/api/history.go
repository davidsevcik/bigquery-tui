package api

import "time"

// HistoryEntry is one recorded Run.
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

// Capacity reports whether the Run executed on a reservation.
func (e HistoryEntry) Capacity() bool { return e.ReservationID != "" }

// HistoryGroup is the Runs of one SQL text (whitespace-normalised), shown as
// its latest Run.
type HistoryGroup struct {
	Latest HistoryEntry `json:"latest"`
	Runs   int          `json:"runs"`
	// BytesBilled sums the on-demand Runs; SlotMs sums the Runs on a
	// reservation (their billed bytes are not a charge).
	BytesBilled int64 `json:"bytes_billed"`
	SlotMs      int64 `json:"slot_ms"`
	// Spans mark the matched free terms in Latest.SQL.
	Spans []Span `json:"spans,omitempty"`
	// Key identifies the group (the normalised SQL's hash).
	Key string `json:"key"`
}

// HistoryPage is one page of History search results.
type HistoryPage struct {
	Query  ParsedQuery    `json:"query"`
	Groups []HistoryGroup `json:"groups"`
	// More is set when another page follows.
	More bool `json:"more,omitempty"`
	// Total counts the groups that match (0 for a page past the end).
	Total int `json:"total"`
}

// Favorite is a saved query: a .sql file under favorites_dir whose relative
// path (with forward slashes) is its ID.
type Favorite struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Params      []ParamSpec `json:"params,omitempty"`
	// SQL is the body below the header; Text is the whole file.
	SQL  string `json:"sql"`
	Text string `json:"-"`
	// Path is the file the Favorite was read from. A rename or delete acts
	// only on that file.
	Path    string    `json:"-"`
	ModTime time.Time `json:"mod_time"`
	// Problems lists header lines that could not be read.
	Problems []string `json:"problems,omitempty"`
}

// ParamSpec is a declared (or, without a declaration, detected) query
// parameter of a Favorite.
type ParamSpec struct {
	Name string `json:"name"`
	// Type is empty for a parameter used in the body but not declared: its
	// type is then guessed from the value.
	Type    string  `json:"type,omitempty"`
	Default *string `json:"default,omitempty"`
}

// FavoriteMatch is a Favorite search result.
type FavoriteMatch struct {
	Favorite Favorite `json:"favorite"`
	Score    int      `json:"score"`
	// NameSpans mark the match in the Favorite's ID.
	NameSpans []Span `json:"name_spans,omitempty"`
}
