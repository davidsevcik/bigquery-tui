package api

import "time"

// ProjectStatus groups the units of one project: a Selected one, or a cached
// one that is no longer selected (Selected false; it never makes a sync
// needed).
type ProjectStatus struct {
	Project  string      `json:"project"`
	Selected bool        `json:"selected"`
	Units    []UnitState `json:"units"`
	// SyncedAt is the oldest successful unit sync, nil if any unit never succeeded.
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
}

// CatalogStatus is the freshness report behind `bqt status`.
type CatalogStatus struct {
	Now              time.Time       `json:"now"`
	ExecutionProject string          `json:"execution_project,omitempty"`
	ExecutionSource  ExecutionSource `json:"execution_source,omitempty"`
	SyncNeeded       bool            `json:"sync_needed"`
	SyncReason       string          `json:"sync_reason,omitempty"`
	SyncRunning      bool            `json:"sync_running"`
	SyncTTL          string          `json:"sync_ttl"`
	Projects         []ProjectStatus `json:"projects"`
	Datasets         int             `json:"datasets"`
	Tables           int             `json:"tables"`
	Columns          int             `json:"columns"`
}

// DoctorReport is the output of `bqt doctor`.
type DoctorReport struct {
	Version     string `json:"version"`
	GoVersion   string `json:"go_version"`
	Platform    string `json:"platform"`
	ConfigPath  string `json:"config_path"`
	ConfigOK    bool   `json:"config_ok"`
	ConfigError string `json:"config_error,omitempty"`
	DataDir     string `json:"data_dir,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	Execution   string `json:"execution_project,omitempty"`
	// ExecutionSource says where Execution comes from (api.ExecutionSource).
	ExecutionSource ExecutionSource `json:"execution_source,omitempty"`
	// Projects counts the Selected projects.
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

// ExecutionSource says where the effective Execution project comes from.
type ExecutionSource string

const (
	ExecutionNone          ExecutionSource = ""
	ExecutionFlag          ExecutionSource = "flag"
	ExecutionEnv           ExecutionSource = "env"
	ExecutionChosen        ExecutionSource = "chosen"
	ExecutionFirstSelected ExecutionSource = "first_selected"
)

// ListedProject is one project BigQuery's projects list returns for the
// credentials (an Accessible project).
type ListedProject struct {
	ID           string `json:"id"`
	FriendlyName string `json:"friendly_name,omitempty"`
}

// ProjectSync is the sync state of a project as the Projects dialog shows it.
type ProjectSync string

const (
	ProjectSynced    ProjectSync = "synced"     // has a cache
	ProjectNotSynced ProjectSync = "not_synced" // no cache and no attempt
	ProjectFailed    ProjectSync = "failed"     // the last attempt failed (not a permission error)
)

// ProjectInfo is one row of the Projects dialog and of `bqt projects`: the
// union of the Accessible, Selected and Cached projects.
type ProjectInfo struct {
	ID           string `json:"id"`
	FriendlyName string `json:"friendly_name,omitempty"`
	Selected     bool   `json:"selected"`
	// Listed is false when BigQuery's projects list (as last fetched) does
	// not return the project. It is true when no list could be fetched.
	Listed bool `json:"listed"`
	// Accessible is false after a sync failed with a permission error.
	Accessible bool        `json:"accessible"`
	Sync       ProjectSync `json:"sync"`
	Datasets   int         `json:"datasets"`
	SyncedAt   *time.Time  `json:"synced_at,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// ProjectsReport is the output of `bqt projects`.
type ProjectsReport struct {
	ExecutionProject string          `json:"execution_project,omitempty"`
	ExecutionSource  ExecutionSource `json:"execution_source,omitempty"`
	Projects         []ProjectInfo   `json:"projects"`
	// ListError is set when BigQuery's projects list could not be fetched.
	ListError string `json:"list_error,omitempty"`
}
