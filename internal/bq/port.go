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

// ColumnFilter says which tables' columns phase 2 reads (spec section 7.1).
// The zero value reads every table.
type ColumnFilter struct {
	// Pattern is the combined shard patterns, anchored to the whole name, in
	// the RE2 dialect BigQuery's REGEXP_CONTAINS shares. A table it matches
	// is left out unless Keep lists it. "" reads every table.
	Pattern string
	// Keep lists "dataset.table" names read although Pattern matches them:
	// each family's newest shard, matching tables in no family, and members
	// synced on their own.
	Keep []string
	// Only, when set, reads exactly these "dataset.table" names, ignoring
	// Pattern and Keep (the safety net's re-read).
	Only []string
}

// TableColumns is one table's columns, as phase 2 returns them.
type TableColumns struct {
	Dataset string
	Table   string
	Columns []api.ColumnMeta
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

	// GetDataset reads one dataset's description, friendly name, labels and
	// location (REST datasets.get, free). The dataset-scope fallback uses it
	// for what SCHEMATA_OPTIONS gives at region scope only.
	GetDataset(ctx context.Context, project, dataset string) (api.DatasetMeta, error)

	// RegionTables is phase 1 of a region unit: datasets (with description,
	// friendly name and labels), tables, table options and views for the
	// requested datasets in one region, without columns, using
	// region-qualified INFORMATION_SCHEMA views (SCHEMATA_OPTIONS, TABLES,
	// TABLE_OPTIONS, VIEWS).
	RegionTables(ctx context.Context, req RegionRequest) (*api.Snapshot, error)

	// RegionColumns is phase 2: the column field paths (COLUMNS and
	// COLUMN_FIELD_PATHS) of the tables the filter keeps, which it leaves out
	// on BigQuery's side. Tables without columns are absent.
	RegionColumns(ctx context.Context, req RegionRequest, f ColumnFilter) ([]TableColumns, error)

	// DatasetTables and DatasetColumns are the two phases restricted to one
	// dataset, using dataset-qualified views. They are the first fallback
	// when the region views are not accessible.
	DatasetTables(ctx context.Context, req DatasetRequest) (*api.Snapshot, error)
	DatasetColumns(ctx context.Context, req DatasetRequest, f ColumnFilter) ([]TableColumns, error)

	// GetTableMeta reads one table's row and columns (REST tables.get, free,
	// no job), in the shape the views report them: gs syncs one table this
	// way.
	GetTableMeta(ctx context.Context, project, dataset, table string) (api.TableMeta, error)

	// ListTableNames returns table names and types only (REST tables.list).
	// It is the last fallback; the tables have NamesOnly set and no columns.
	ListTableNames(ctx context.Context, project, dataset string) ([]api.TableMeta, error)
}

type warnKey struct{}

// WithWarnings returns ctx carrying fn, which a Metadata implementation calls
// (through Warn) with a warning about the work done under ctx: something the
// user should know that is not an error, such as a slower read path. The sync
// run adds it to its warnings. fn may be called from several goroutines and
// more than once with the same message. The Metadata port stays as it is: a
// detail of one implementation does not widen it.
func WithWarnings(ctx context.Context, fn func(msg string)) context.Context {
	return context.WithValue(ctx, warnKey{}, fn)
}

// Warn passes msg to the function WithWarnings put in ctx, if any.
func Warn(ctx context.Context, msg string) {
	if fn, ok := ctx.Value(warnKey{}).(func(string)); ok && fn != nil {
		fn(msg)
	}
}
