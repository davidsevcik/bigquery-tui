package api

import "time"

// ProjectStatus groups the units of one configured project.
type ProjectStatus struct {
	Project string      `json:"project"`
	Units   []UnitState `json:"units"`
	// SyncedAt is the oldest successful unit sync, nil if any unit never succeeded.
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
}

// CatalogStatus is the freshness report behind `bqt status`.
type CatalogStatus struct {
	Now         time.Time       `json:"now"`
	SyncNeeded  bool            `json:"sync_needed"`
	SyncReason  string          `json:"sync_reason,omitempty"`
	SyncRunning bool            `json:"sync_running"`
	SyncTTL     string          `json:"sync_ttl"`
	Projects    []ProjectStatus `json:"projects"`
	Datasets    int             `json:"datasets"`
	Tables      int             `json:"tables"`
	Columns     int             `json:"columns"`
}
