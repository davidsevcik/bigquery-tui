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

// DoctorReport is the output of `bqt doctor`.
type DoctorReport struct {
	Version     string        `json:"version"`
	GoVersion   string        `json:"go_version"`
	Platform    string        `json:"platform"`
	ConfigPath  string        `json:"config_path"`
	ConfigOK    bool          `json:"config_ok"`
	ConfigError string        `json:"config_error,omitempty"`
	DataDir     string        `json:"data_dir,omitempty"`
	LogPath     string        `json:"log_path,omitempty"`
	Execution   string        `json:"execution_project,omitempty"`
	Projects    int           `json:"projects"`
	Auth        DoctorAuth    `json:"auth"`
	Catalog     DoctorCatalog `json:"catalog"`
	History     DoctorHistory `json:"history"`
	SyncRunning bool          `json:"sync_running"`
}

// DoctorAuth reports the credentials state without any secret.
type DoctorAuth struct {
	OK           bool   `json:"ok"`
	Type         string `json:"type,omitempty"`
	ClientEmail  string `json:"client_email,omitempty"`
	QuotaProject string `json:"quota_project,omitempty"`
	Error        string `json:"error,omitempty"`
}

// DoctorCatalog reports the catalog file.
type DoctorCatalog struct {
	Exists     bool       `json:"exists"`
	Tables     int        `json:"tables"`
	Datasets   int        `json:"datasets"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	Age        string     `json:"age,omitempty"`
	SyncNeeded bool       `json:"sync_needed"`
}

// DoctorHistory reports the history file.
type DoctorHistory struct {
	Exists bool `json:"exists"`
}
