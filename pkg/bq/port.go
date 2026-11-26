// Package bq defines the BigQuery ports the core depends on and the real
// implementation behind them. Everything above this package talks to the
// interfaces, so tests substitute fakes.
package bq

import (
	"context"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

type DatasetList struct {
	Datasets    []api.DatasetMeta
	Unreachable []string
}

type RegionRequest struct {
	Project  string
	Location string
	Datasets []string
}

type DatasetRequest struct {
	Project  string
	Location string
	Dataset  string
}

type ColumnFilter struct {
	Pattern string
	Keep    []string
	Only    []string
}

type TableColumns struct {
	Dataset string
	Table   string
	Columns []api.ColumnMeta
}

type Metadata interface {
	ListProjects(ctx context.Context) ([]api.ListedProject, error)

	ListDatasets(ctx context.Context, project string) (DatasetList, error)

	GetDataset(ctx context.Context, project, dataset string) (api.DatasetMeta, error)

	RegionTables(ctx context.Context, req RegionRequest) (*api.Snapshot, error)

	RegionColumns(ctx context.Context, req RegionRequest, f ColumnFilter) ([]TableColumns, error)

	DatasetTables(ctx context.Context, req DatasetRequest) (*api.Snapshot, error)
	DatasetColumns(ctx context.Context, req DatasetRequest, f ColumnFilter) ([]TableColumns, error)

	GetTableMeta(ctx context.Context, project, dataset, table string) (api.TableMeta, error)

	ListTableNames(ctx context.Context, project, dataset string) ([]api.TableMeta, error)
}

type warnKey struct{}

func WithWarnings(ctx context.Context, fn func(msg string)) context.Context {
	return context.WithValue(ctx, warnKey{}, fn)
}

func Warn(ctx context.Context, msg string) {
	if fn, ok := ctx.Value(warnKey{}).(func(string)); ok && fn != nil {
		fn(msg)
	}
}
