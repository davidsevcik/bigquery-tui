package api

import "time"

type RunState string

const (
	RunQueued     RunState = "queued"
	RunEstimating RunState = "estimating"
	RunNeedsOK    RunState = "needs_confirmation"
	RunRunning    RunState = "running"
	RunDone       RunState = "done"
	RunFailed     RunState = "failed"
	RunCancelled  RunState = "cancelled"
	RunEstimated  RunState = "estimated"
	RunRefused    RunState = "refused"
)

func (s RunState) Finished() bool {
	switch s {
	case RunDone, RunFailed, RunCancelled, RunEstimated, RunRefused:
		return true
	}
	return false
}

type Estimate struct {
	Bytes         int64           `json:"bytes"`
	Accuracy      string          `json:"accuracy,omitempty"`
	StatementType string          `json:"statement_type,omitempty"`
	ReservationID string          `json:"reservation_id,omitempty"`
	Edition       string          `json:"edition,omitempty"`
	Capacity      bool            `json:"capacity"`
	ApproxUSD     *float64        `json:"approx_usd,omitempty"`
	Columns       []PreviewColumn `json:"columns,omitempty"`
}

type Confirmation struct {
	Reasons       []string `json:"reasons"`
	Bytes         int64    `json:"bytes"`
	Threshold     int64    `json:"threshold,omitempty"`
	StatementType string   `json:"statement_type,omitempty"`
}

type QueryError struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	Kind    string `json:"kind"`
	Hint    string `json:"hint,omitempty"`
}

type JobRef struct {
	Project  string `json:"project"`
	Location string `json:"location"`
	ID       string `json:"job_id"`
}

type RunStats struct {
	BytesProcessed int64  `json:"bytes_processed"`
	BytesBilled    int64  `json:"bytes_billed"`
	SlotMs         int64  `json:"slot_ms"`
	CacheHit       bool   `json:"cache_hit,omitempty"`
	AffectedRows   int64  `json:"affected_rows,omitempty"`
	StatementType  string `json:"statement_type,omitempty"`
	ReservationID  string `json:"reservation_id,omitempty"`
	Edition        string `json:"edition,omitempty"`
	Destination    string `json:"destination,omitempty"`
}

type RunInfo struct {
	ID          int64        `json:"id"`
	Tag         string       `json:"tag,omitempty"`
	SQL         string       `json:"sql"`
	Params      []QueryParam `json:"params,omitempty"`
	State       RunState     `json:"state"`
	DryRunOnly  bool         `json:"dry_run_only,omitempty"`
	Source      string       `json:"source"`
	Favorite    string       `json:"favorite,omitempty"`
	FromHistory int64        `json:"from_history,omitempty"`

	ExecutionProject string `json:"execution_project"`
	Location         string `json:"location,omitempty"`

	Estimate        *Estimate     `json:"estimate,omitempty"`
	Confirmation    *Confirmation `json:"confirmation,omitempty"`
	Refusal         *Refusal      `json:"refusal,omitempty"`
	Job             *JobRef       `json:"job,omitempty"`
	Error           *QueryError   `json:"error,omitempty"`
	Stats           *RunStats     `json:"stats,omitempty"`
	CancelRequested bool          `json:"cancel_requested,omitempty"`
	CancelFailed    bool          `json:"cancel_failed,omitempty"`

	HasResult   bool            `json:"has_result,omitempty"`
	TotalRows   int64           `json:"total_rows"`
	ResultError string          `json:"result_error,omitempty"`
	Columns     []PreviewColumn `json:"columns,omitempty"`

	Created time.Time  `json:"created"`
	Started *time.Time `json:"started,omitempty"`
	Ended   *time.Time `json:"ended,omitempty"`
}

type RunEvent struct {
	Run RunInfo `json:"run"`
}

type PricingModel struct {
	Known         bool   `json:"known"`
	Capacity      bool   `json:"capacity"`
	ReservationID string `json:"reservation_id,omitempty"`
	Edition       string `json:"edition,omitempty"`
}

type ExportEvent struct {
	Rows  int64  `json:"rows"`
	Total int64  `json:"total"`
	Path  string `json:"path"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}
