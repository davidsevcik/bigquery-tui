package api

import "time"

type ProjectStatus struct {
	Project  string      `json:"project"`
	Selected bool        `json:"selected"`
	Units    []UnitState `json:"units"`
	SyncedAt *time.Time  `json:"synced_at,omitempty"`
	Stale    bool        `json:"stale,omitempty"`
	Pending  int         `json:"pending_datasets,omitempty"`
}

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

type DoctorReport struct {
	Version         string          `json:"version"`
	GoVersion       string          `json:"go_version"`
	Platform        string          `json:"platform"`
	ConfigPath      string          `json:"config_path"`
	ConfigOK        bool            `json:"config_ok"`
	ConfigError     string          `json:"config_error,omitempty"`
	DataDir         string          `json:"data_dir,omitempty"`
	LogPath         string          `json:"log_path,omitempty"`
	LogMissing      bool            `json:"log_missing,omitempty"`
	LogError        string          `json:"log_error,omitempty"`
	Execution       string          `json:"execution_project,omitempty"`
	ExecutionSource ExecutionSource `json:"execution_source,omitempty"`
	Projects        int             `json:"projects"`
	Auth            DoctorAuth      `json:"auth"`
	Catalog         DoctorCatalog   `json:"catalog"`
	History         DoctorHistory   `json:"history"`
	SyncRunning     bool            `json:"sync_running"`
}

type DoctorAuth struct {
	OK           bool   `json:"ok"`
	Type         string `json:"type,omitempty"`
	ClientEmail  string `json:"client_email,omitempty"`
	QuotaProject string `json:"quota_project,omitempty"`
	Error        string `json:"error,omitempty"`
}

type DoctorCatalog struct {
	Exists     bool       `json:"exists"`
	Tables     int        `json:"tables"`
	Datasets   int        `json:"datasets"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	Age        string     `json:"age,omitempty"`
	SyncNeeded bool       `json:"sync_needed"`
}

type DoctorHistory struct {
	Exists bool `json:"exists"`
}

type ExecutionSource string

const (
	ExecutionNone          ExecutionSource = ""
	ExecutionFlag          ExecutionSource = "flag"
	ExecutionEnv           ExecutionSource = "env"
	ExecutionChosen        ExecutionSource = "chosen"
	ExecutionFirstSelected ExecutionSource = "first_selected"
)

type ListedProject struct {
	ID           string `json:"id"`
	FriendlyName string `json:"friendly_name,omitempty"`
}

type ProjectSync string

const (
	ProjectSynced    ProjectSync = "synced"
	ProjectNotSynced ProjectSync = "not_synced"
	ProjectFailed    ProjectSync = "failed"
)

type ProjectInfo struct {
	ID           string      `json:"id"`
	FriendlyName string      `json:"friendly_name,omitempty"`
	Selected     bool        `json:"selected"`
	Listed       bool        `json:"listed"`
	Accessible   bool        `json:"accessible"`
	Sync         ProjectSync `json:"sync"`
	Datasets     int         `json:"datasets"`
	SyncedAt     *time.Time  `json:"synced_at,omitempty"`
	Error        string      `json:"error,omitempty"`
}

type ProjectsReport struct {
	ExecutionProject string          `json:"execution_project,omitempty"`
	ExecutionSource  ExecutionSource `json:"execution_source,omitempty"`
	Projects         []ProjectInfo   `json:"projects"`
	ListError        string          `json:"list_error,omitempty"`
}
