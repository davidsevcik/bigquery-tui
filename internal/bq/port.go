// Package bq defines the BigQuery ports the core depends on and the real
// implementation behind them. Everything above this package talks to the
// interfaces, so tests substitute fakes (ticket 16).
package bq

import (
	"context"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// DatasetList is the result of enumerating a project's datasets.
type DatasetList struct {
	Datasets []api.DatasetMeta
	// Unreachable lists locations/resources BigQuery could not enumerate.
	Unreachable []string
}

// RegionRequest asks for everything in one (project, location) unit,
// restricted to the named datasets.
type RegionRequest struct {
	Project  string
	Location string
	Datasets []string
}

// DatasetRequest asks for one dataset using dataset-scope views.
type DatasetRequest struct {
	Project  string
	Location string
	Dataset  string
}

// Metadata reads catalog metadata. Implementations must set an explicit job
// location on every query and label their jobs.
type Metadata interface {
	// ListProjects lists the projects the credentials hold a project-level
	// role in (REST projects.list), with their friendly names. It needs no
	// Execution project.
	ListProjects(ctx context.Context) ([]api.ListedProject, error)

	// ListDatasets enumerates a project's datasets with their locations (REST
	// datasets.list). It is the only way to discover which regions to query.
	ListDatasets(ctx context.Context, project string) (DatasetList, error)

	// RegionSnapshot returns datasets (with description, friendly name and
	// labels), tables, views, and column field paths for the requested
	// datasets in one region, using region-qualified INFORMATION_SCHEMA views.
	RegionSnapshot(ctx context.Context, req RegionRequest) (*api.Snapshot, error)

	// DatasetSnapshot is RegionSnapshot restricted to one dataset, using
	// dataset-qualified views. It is the first fallback when the region views
	// are not accessible.
	DatasetSnapshot(ctx context.Context, req DatasetRequest) (*api.Snapshot, error)

	// ListTableNames returns table names and types only (REST tables.list).
	// It is the last fallback; the tables have NamesOnly set and no columns.
	ListTableNames(ctx context.Context, project, dataset string) ([]api.TableMeta, error)
}
