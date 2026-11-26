package api

import "time"

type UnitStatus string

const (
	UnitOK           UnitStatus = "ok"
	UnitDegraded     UnitStatus = "degraded"
	UnitInaccessible UnitStatus = "inaccessible"
)

type UnitRef struct {
	Project  string `json:"project"`
	Location string `json:"location"`
}

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
	Units      []UnitRef     `json:"units,omitempty"`
}

type SyncScope struct {
	Projects []string    `json:"projects,omitempty"`
	Table    *ObjectPath `json:"table,omitempty"`
	Family   bool        `json:"family,omitempty"`
}

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
