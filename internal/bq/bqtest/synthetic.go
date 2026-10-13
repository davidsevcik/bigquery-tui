package bqtest

import (
	"fmt"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// SyntheticSpec describes a generated catalog for scale tests and benchmarks.
type SyntheticSpec struct {
	Project          string
	Location         string
	Datasets         int
	TablesPerDataset int
	ColumnsPerTable  int
	// NestedPerTable adds this many STRUCT sub-fields to every table.
	NestedPerTable int
}

// Synthetic generates a deterministic snapshot. The default scale used by the
// spec's budgets is 50k tables with 1.25M field paths: 500 datasets x 100
// tables x (20 columns + 5 nested fields).
func Synthetic(s SyntheticSpec) *api.Snapshot {
	snap := &api.Snapshot{}
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < s.Datasets; d++ {
		dsName := fmt.Sprintf("dataset_%04d", d)
		snap.Datasets = append(snap.Datasets, api.DatasetMeta{
			Project: s.Project, Name: dsName, Location: s.Location,
			Description: fmt.Sprintf("Synthetic dataset number %d for scale tests", d),
		})
		for t := 0; t < s.TablesPerDataset; t++ {
			tb := api.TableMeta{
				Dataset: dsName, Name: fmt.Sprintf("table_%04d_%03d", d, t), Type: api.TypeTable, CreatedAt: created,
				Description: fmt.Sprintf("Synthetic table %d of dataset %d", t, d),
			}
			for c := 0; c < s.ColumnsPerTable; c++ {
				tb.Columns = append(tb.Columns, api.ColumnMeta{
					FieldPath: fmt.Sprintf("column_%02d", c), Ordinal: c + 1, DataType: "STRING",
					Description: fmt.Sprintf("column %d", c),
				})
			}
			if s.NestedPerTable > 0 {
				tb.Columns = append(tb.Columns, api.ColumnMeta{FieldPath: "payload", Ordinal: s.ColumnsPerTable + 1, DataType: "STRUCT<...>"})
				for n := 0; n < s.NestedPerTable; n++ {
					tb.Columns = append(tb.Columns, api.ColumnMeta{FieldPath: fmt.Sprintf("payload.field_%02d", n), Ordinal: n + 1, DataType: "STRING"})
				}
			}
			snap.Tables = append(snap.Tables, tb)
		}
	}
	return snap
}
