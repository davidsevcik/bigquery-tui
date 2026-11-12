package bqtest

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// ColumnReads lists every "project.dataset.table" whose columns a column call
// returned, in call order (a table read twice is listed twice).
func (f *Fake) ColumnReads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.columnReads...)
}

// regionDatasets returns the datasets a region request covers: the
// requested ones in the location, plus any leaked ones. A denied dataset
// fails the whole request, as a region view does.
func (f *Fake) regionDatasets(req bq.RegionRequest) ([]Dataset, error) {
	p, err := f.project(req.Project)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, d := range req.Datasets {
		want[d] = true
	}
	var out []Dataset
	add := func(d Dataset, requestedOnly bool) error {
		if !strings.EqualFold(d.Meta.Location, req.Location) {
			return nil
		}
		if requestedOnly && !want[d.Meta.Name] {
			return nil
		}
		if d.Denied {
			return PermissionError(fmt.Sprintf("region view for %s.%s", req.Project, d.Meta.Name))
		}
		out = append(out, d)
		return nil
	}
	for _, d := range p.Datasets {
		if err := add(d, true); err != nil {
			return nil, err
		}
	}
	for _, d := range p.Leak {
		if err := add(d, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (f *Fake) dataset(project, dataset string) (Dataset, error) {
	p, err := f.project(project)
	if err != nil {
		return Dataset{}, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != dataset {
			continue
		}
		if d.Denied {
			return Dataset{}, PermissionError(fmt.Sprintf("dataset %s.%s", project, dataset))
		}
		return d, nil
	}
	return Dataset{}, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("dataset %s.%s not found", project, dataset)}
}

// withoutColumns is a table as phase 1 reports it.
func withoutColumns(dataset string, t api.TableMeta) api.TableMeta {
	t.Dataset, t.Columns, t.Shard = dataset, nil, nil
	t.SharedSchema, t.OwnSchema = false, false
	return t
}

// RegionTables implements bq.Metadata.
func (f *Fake) RegionTables(ctx context.Context, req bq.RegionRequest) (*api.Snapshot, error) {
	if err := f.begin(ctx, Call{Method: "RegionTables", Project: req.Project, Location: req.Location, Datasets: append([]string(nil), req.Datasets...)}); err != nil {
		return nil, err
	}
	ds, err := f.regionDatasets(req)
	if err != nil {
		return nil, err
	}
	snap := &api.Snapshot{}
	for _, d := range ds {
		m := d.Meta
		m.Project = req.Project
		snap.Datasets = append(snap.Datasets, m)
		for _, t := range d.Tables {
			snap.Tables = append(snap.Tables, withoutColumns(d.Meta.Name, t))
		}
	}
	return snap, nil
}

// RegionColumns implements bq.Metadata, filtering with Go's regexp the way
// BigQuery's REGEXP_CONTAINS does.
func (f *Fake) RegionColumns(ctx context.Context, req bq.RegionRequest, flt bq.ColumnFilter) ([]bq.TableColumns, error) {
	if err := f.begin(ctx, Call{Method: "RegionColumns", Project: req.Project, Location: req.Location, Datasets: append([]string(nil), req.Datasets...), Filter: flt}); err != nil {
		return nil, err
	}
	ds, err := f.regionDatasets(req)
	if err != nil {
		return nil, err
	}
	return f.columns(req.Project, ds, flt)
}

// DatasetTables implements bq.Metadata.
func (f *Fake) DatasetTables(ctx context.Context, req bq.DatasetRequest) (*api.Snapshot, error) {
	if err := f.begin(ctx, Call{Method: "DatasetTables", Project: req.Project, Location: req.Location, Dataset: req.Dataset}); err != nil {
		return nil, err
	}
	d, err := f.dataset(req.Project, req.Dataset)
	if err != nil {
		return nil, err
	}
	// SCHEMATA_OPTIONS has no dataset scope: no description, friendly name
	// or labels.
	m := api.DatasetMeta{Project: req.Project, Name: d.Meta.Name, Location: d.Meta.Location}
	snap := &api.Snapshot{Datasets: []api.DatasetMeta{m}}
	for _, t := range d.Tables {
		snap.Tables = append(snap.Tables, withoutColumns(d.Meta.Name, t))
	}
	return snap, nil
}

// DatasetColumns implements bq.Metadata.
func (f *Fake) DatasetColumns(ctx context.Context, req bq.DatasetRequest, flt bq.ColumnFilter) ([]bq.TableColumns, error) {
	if err := f.begin(ctx, Call{Method: "DatasetColumns", Project: req.Project, Location: req.Location, Dataset: req.Dataset, Filter: flt}); err != nil {
		return nil, err
	}
	d, err := f.dataset(req.Project, req.Dataset)
	if err != nil {
		return nil, err
	}
	return f.columns(req.Project, []Dataset{d}, flt)
}

func (f *Fake) columns(project string, ds []Dataset, flt bq.ColumnFilter) ([]bq.TableColumns, error) {
	var re *regexp.Regexp
	if flt.Pattern != "" && len(flt.Only) == 0 {
		var err error
		if re, err = regexp.Compile(flt.Pattern); err != nil {
			return nil, &bq.Error{Kind: bq.KindOther, Op: "fake", Err: fmt.Errorf("invalid regular expression: %w", err)}
		}
	}
	set := func(names []string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	keep, only := set(flt.Keep), set(flt.Only)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []bq.TableColumns
	for _, d := range ds {
		for _, t := range d.Tables {
			name := d.Meta.Name + "." + t.Name
			switch {
			case len(only) > 0:
				if !only[name] {
					continue
				}
			case re != nil && re.MatchString(t.Name) && !keep[name]:
				continue
			}
			full := project + "." + name
			if len(only) == 0 && flt.Pattern != "" && f.MissColumns[full] {
				continue
			}
			if len(t.Columns) == 0 {
				continue // the views have no rows for a table without columns
			}
			f.columnReads = append(f.columnReads, full)
			out = append(out, bq.TableColumns{Dataset: d.Meta.Name, Table: t.Name, Columns: append([]api.ColumnMeta(nil), t.Columns...)})
		}
	}
	return out, nil
}

// GetTableMeta implements bq.Metadata.
func (f *Fake) GetTableMeta(ctx context.Context, project, dataset, table string) (api.TableMeta, error) {
	if err := f.begin(ctx, Call{Method: "GetTableMeta", Project: project, Dataset: dataset, Table: table}); err != nil {
		return api.TableMeta{}, err
	}
	p, err := f.project(project)
	if err != nil {
		return api.TableMeta{}, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != dataset {
			continue
		}
		for _, t := range d.Tables {
			if t.Name == table {
				out := withoutColumns(dataset, t)
				out.Columns = append([]api.ColumnMeta(nil), t.Columns...)
				return out, nil
			}
		}
	}
	return api.TableMeta{}, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("table %s.%s.%s not found", project, dataset, table)}
}
