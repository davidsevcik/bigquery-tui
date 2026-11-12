package api

import "time"

// UnitStatus is the health of one (project, location) sync unit.
type UnitStatus string

const (
	UnitOK           UnitStatus = "ok"
	UnitDegraded     UnitStatus = "degraded"
	UnitInaccessible UnitStatus = "inaccessible"
)

// UnitRef identifies a sync unit. Location "" is the project-level discovery
// unit (datasets.list), used when a project cannot be enumerated completely,
// and to record the sync of a project that has no datasets (and so no other
// unit).
type UnitRef struct {
	Project  string `json:"project"`
	Location string `json:"location"`
}

// UnitState is the persisted sync state of a unit.
type UnitState struct {
	UnitRef
	LastAttempt  *time.Time `json:"last_attempt,omitempty"`
	LastSuccess  *time.Time `json:"last_success,omitempty"`
	Status       UnitStatus `json:"status"`
	Error        string     `json:"error,omitempty"`
	Datasets     int        `json:"datasets"`
	Tables       int        `json:"tables"`
	NamesOnly    int        `json:"names_only_datasets,omitempty"`
	Inaccessible int        `json:"inaccessible_datasets,omitempty"`
	Stale        bool       `json:"stale,omitempty"`
}

// SyncEventKind enumerates the structured events a sync emits.
type SyncEventKind string

const (
	EventRunStarted  SyncEventKind = "run_started"
	EventDiscovered  SyncEventKind = "discovered"
	EventUnitStarted SyncEventKind = "unit_started"
	EventUnitCounted SyncEventKind = "unit_counted"
	EventUnitDone    SyncEventKind = "unit_done"
	EventUnitFailed  SyncEventKind = "unit_failed"
	EventWarning     SyncEventKind = "warning"
	EventRunFinished SyncEventKind = "run_finished"
)

// SyncEvent is one progress event. Fields not relevant to a kind are zero.
type SyncEvent struct {
	Kind       SyncEventKind `json:"kind"`
	Time       time.Time     `json:"time"`
	Project    string        `json:"project,omitempty"`
	Location   string        `json:"location,omitempty"`
	Status     UnitStatus    `json:"status,omitempty"`
	Datasets   int           `json:"datasets,omitempty"`
	Tables     int           `json:"tables,omitempty"`
	UnitsDone  int           `json:"units_done"`
	UnitsTotal int           `json:"units_total"`
	Message    string        `json:"message,omitempty"`
	Error      string        `json:"error,omitempty"`
	// Units lists the units the run will process (discovered only).
	Units []UnitRef `json:"units,omitempty"`
}

// SyncScope selects what a sync covers. An empty Projects list means every
// Selected project. Table, when set, syncs that one table's row and schema
// instead (gs, bqt sync --table), and Projects is ignored.
type SyncScope struct {
	Projects []string    `json:"projects,omitempty"`
	Table    *ObjectPath `json:"table,omitempty"`
	// Family is set with Table when the path named a shard family, which
	// syncs its newest shard: that refreshes the family's schema and marks
	// nothing. A member named itself gets the own-schema mark.
	Family bool `json:"family,omitempty"`
}

// SyncResult summarises a finished (or cancelled) sync run.
type SyncResult struct {
	Started       time.Time   `json:"started"`
	Finished      time.Time   `json:"finished"`
	Cancelled     bool        `json:"cancelled,omitempty"`
	UnitsTotal    int         `json:"units_total"`
	UnitsOK       int         `json:"units_ok"`
	UnitsDegraded int         `json:"units_degraded"`
	UnitsFailed   int         `json:"units_failed"`
	Units         []UnitState `json:"units"`
	Warnings      []string    `json:"warnings,omitempty"`
}
