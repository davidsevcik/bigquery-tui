package api

import "time"

// RunState is the lifecycle state of a Run.
type RunState string

const (
	RunQueued     RunState = "queued"
	RunEstimating RunState = "estimating" // dry-run in progress
	RunNeedsOK    RunState = "needs_confirmation"
	RunRunning    RunState = "running"
	RunDone       RunState = "done"
	RunFailed     RunState = "failed"
	RunCancelled  RunState = "cancelled"
	// RunEstimated is the end state of a dry-run-only Run.
	RunEstimated RunState = "estimated"
	// RunRefused is the end state of a Run the safety policy stopped after
	// its dry-run (agent entry points only); no job was started.
	RunRefused RunState = "refused"
)

// Finished reports whether the state is final.
func (s RunState) Finished() bool {
	switch s {
	case RunDone, RunFailed, RunCancelled, RunEstimated, RunRefused:
		return true
	}
	return false
}

// Estimate is what the dry-run of a Run reported.
type Estimate struct {
	Bytes int64 `json:"bytes"`
	// Accuracy is PRECISE, LOWER_BOUND, UPPER_BOUND or UNKNOWN.
	Accuracy      string `json:"accuracy,omitempty"`
	StatementType string `json:"statement_type,omitempty"`
	// ReservationID and Edition are set when the job would run on a
	// reservation (capacity pricing); both are empty for on-demand.
	ReservationID string `json:"reservation_id,omitempty"`
	Edition       string `json:"edition,omitempty"`
	Capacity      bool   `json:"capacity"`
	// ApproxUSD is the on-demand price of Bytes; nil under capacity pricing.
	ApproxUSD *float64        `json:"approx_usd,omitempty"`
	Columns   []PreviewColumn `json:"columns,omitempty"`
}

// Confirmation says why a Run waits for the user.
type Confirmation struct {
	// Reasons holds "bytes" (the estimate is over confirm_bytes) and/or
	// "statement" (the statement is not a SELECT).
	Reasons       []string `json:"reasons"`
	Bytes         int64    `json:"bytes"`
	Threshold     int64    `json:"threshold,omitempty"`
	StatementType string   `json:"statement_type,omitempty"`
}

// QueryError is a failed dry-run or job. Line and Col are 1-based and 0 when
// BigQuery gave no position.
type QueryError struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	// Kind is the failure class: "query", "over_limit", "auth", "permission",
	// "not_found", "transient", "cancelled" or "other".
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

// JobRef identifies a BigQuery job.
type JobRef struct {
	Project  string `json:"project"`
	Location string `json:"location"`
	ID       string `json:"job_id"`
}

// RunStats are the job statistics of a finished Run.
type RunStats struct {
	BytesProcessed int64 `json:"bytes_processed"`
	// BytesBilled is what BigQuery reports; under capacity pricing it is not a
	// charge.
	BytesBilled   int64  `json:"bytes_billed"`
	SlotMs        int64  `json:"slot_ms"`
	CacheHit      bool   `json:"cache_hit,omitempty"`
	AffectedRows  int64  `json:"affected_rows,omitempty"`
	StatementType string `json:"statement_type,omitempty"`
	ReservationID string `json:"reservation_id,omitempty"`
	Edition       string `json:"edition,omitempty"`
	Destination   string `json:"destination,omitempty"`
}

// RunInfo is a snapshot of a Run.
type RunInfo struct {
	ID int64 `json:"id"`
	// Tag is opaque to the core; a frontend uses it to tie a Run to a buffer.
	Tag        string       `json:"tag,omitempty"`
	SQL        string       `json:"sql"`
	Params     []QueryParam `json:"params,omitempty"`
	State      RunState     `json:"state"`
	DryRunOnly bool         `json:"dry_run_only,omitempty"`
	Source     string       `json:"source"`
	// Favorite is the ID of the Favorite the SQL came from, if any.
	Favorite string `json:"favorite,omitempty"`
	// FromHistory is the History entry a reopened Run shows the result of;
	// such a Run ran earlier and is not recorded again.
	FromHistory int64 `json:"from_history,omitempty"`

	ExecutionProject string `json:"execution_project"`
	Location         string `json:"location,omitempty"`

	Estimate     *Estimate     `json:"estimate,omitempty"`
	Confirmation *Confirmation `json:"confirmation,omitempty"`
	// Refusal is the safety policy's verdict for a Run under a policy: set
	// on a refused Run, and on a dry-run-only Run the policy would refuse.
	Refusal *Refusal    `json:"refusal,omitempty"`
	Job     *JobRef     `json:"job,omitempty"`
	Error   *QueryError `json:"error,omitempty"`
	Stats   *RunStats   `json:"stats,omitempty"`
	// CancelRequested is set between the cancel request and the job's
	// confirmation; the Run may still be billed.
	CancelRequested bool `json:"cancel_requested,omitempty"`
	// CancelFailed is set when a cancel request could not be delivered; the
	// job is still running and the cancel can be asked for again.
	CancelFailed bool `json:"cancel_failed,omitempty"`

	// HasResult is set when rows can be paged; TotalRows and Columns describe them.
	HasResult bool  `json:"has_result,omitempty"`
	TotalRows int64 `json:"total_rows"`
	// ResultError is set when the job succeeded but its rows could not be read.
	ResultError string          `json:"result_error,omitempty"`
	Columns     []PreviewColumn `json:"columns,omitempty"`

	Created time.Time  `json:"created"`
	Started *time.Time `json:"started,omitempty"`
	Ended   *time.Time `json:"ended,omitempty"`
}

// RunEvent is emitted on every state change of a Run.
type RunEvent struct {
	Run RunInfo `json:"run"`
}

// PricingModel is what a dry-run revealed about how jobs are billed.
type PricingModel struct {
	Known         bool   `json:"known"`
	Capacity      bool   `json:"capacity"`
	ReservationID string `json:"reservation_id,omitempty"`
	Edition       string `json:"edition,omitempty"`
}

// ExportEvent reports export progress; Done is set on the last event.
type ExportEvent struct {
	Rows  int64  `json:"rows"`
	Total int64  `json:"total"`
	Path  string `json:"path"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}
