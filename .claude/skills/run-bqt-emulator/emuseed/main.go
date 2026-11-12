// emuseed syncs bqt's catalog from a BigQuery emulator whose INFORMATION_SCHEMA
// is too thin for bqt's views: the dataset snapshot is built from tables.get.
// Manual testing only: setup.sh builds and runs it with BQT_CONFIG and
// BQT_BIGQUERY_ENDPOINT set.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	bqv2 "google.golang.org/api/bigquery/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

type meta struct {
	*bq.Client
	svc *bqv2.Service
}

func cols(prefix string, fs []*bqv2.TableFieldSchema, part string, ord *int, out *[]api.ColumnMeta) {
	for _, f := range fs {
		path := prefix + f.Name
		typ := f.Type
		if typ == "RECORD" {
			typ = "STRUCT<…>"
		}
		if prefix == "" {
			*ord++
		}
		*out = append(*out, api.ColumnMeta{FieldPath: path, Ordinal: *ord, DataType: typ, Description: f.Description,
			Partitioning: prefix == "" && path == part, Required: prefix == "" && f.Mode == "REQUIRED"})
		if len(f.Fields) > 0 {
			cols(path+".", f.Fields, part, ord, out)
		}
	}
}

// snapshot is a dataset's tables with their columns, from tables.get.
func (m meta) snapshot(ctx context.Context, req bq.DatasetRequest) (*api.Snapshot, error) {
	snap := &api.Snapshot{Datasets: []api.DatasetMeta{{Project: req.Project, Name: req.Dataset, Location: req.Location}}}
	list, err := m.svc.Tables.List(req.Project, req.Dataset).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	for _, t := range list.Tables {
		g, err := m.svc.Tables.Get(req.Project, req.Dataset, t.TableReference.TableId).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		tm := api.TableMeta{Dataset: req.Dataset, Name: g.TableReference.TableId, Type: api.TypeTable, Description: g.Description,
			Labels: g.Labels, RequirePartitionFilter: g.RequirePartitionFilter}
		if g.Type == "VIEW" {
			tm.Type = api.TypeView
			if g.View != nil {
				tm.ViewSQL = g.View.Query
			}
		}
		part := ""
		if g.TimePartitioning != nil {
			part = g.TimePartitioning.Field
		}
		if g.Schema != nil {
			ord := 0
			cols("", g.Schema.Fields, part, &ord, &tm.Columns)
		}
		snap.Tables = append(snap.Tables, tm)
	}
	return snap, nil
}

// ListDatasets fills in the location the emulator leaves out: bqt keeps
// the empty location for a project's discovery state.
func (m meta) ListDatasets(ctx context.Context, project string) (bq.DatasetList, error) {
	l, err := m.Client.ListDatasets(ctx, project)
	for i := range l.Datasets {
		if l.Datasets[i].Location == "" {
			l.Datasets[i].Location = "US"
		}
	}
	return l, err
}

// region is every requested dataset's snapshot from tables.get.
func (m meta) region(ctx context.Context, req bq.RegionRequest) (*api.Snapshot, error) {
	out := &api.Snapshot{}
	for _, d := range req.Datasets {
		s, err := m.snapshot(ctx, bq.DatasetRequest{Project: req.Project, Location: req.Location, Dataset: d})
		if err != nil {
			return nil, err
		}
		out.Datasets = append(out.Datasets, s.Datasets...)
		out.Tables = append(out.Tables, s.Tables...)
	}
	return out, nil
}

// tablesOnly drops the columns (phase 1).
func tablesOnly(s *api.Snapshot, err error) (*api.Snapshot, error) {
	if err != nil {
		return nil, err
	}
	for i := range s.Tables {
		s.Tables[i].Columns = nil
	}
	return s, nil
}

// columnsOnly applies the phase 2 filter in Go, as BigQuery would.
func columnsOnly(s *api.Snapshot, err error, f bq.ColumnFilter) ([]bq.TableColumns, error) {
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if f.Pattern != "" {
		if re, err = regexp.Compile(f.Pattern); err != nil {
			return nil, err
		}
	}
	keep, only := map[string]bool{}, map[string]bool{}
	for _, k := range f.Keep {
		keep[k] = true
	}
	for _, k := range f.Only {
		only[k] = true
	}
	var out []bq.TableColumns
	for _, t := range s.Tables {
		k := t.Dataset + "." + t.Name
		switch {
		case len(only) > 0 && !only[k]:
			continue
		case len(only) == 0 && re != nil && re.MatchString(t.Name) && !keep[k]:
			continue
		}
		out = append(out, bq.TableColumns{Dataset: t.Dataset, Table: t.Name, Columns: t.Columns})
	}
	return out, nil
}

func (m meta) RegionTables(ctx context.Context, req bq.RegionRequest) (*api.Snapshot, error) {
	return tablesOnly(m.region(ctx, req))
}

func (m meta) RegionColumns(ctx context.Context, req bq.RegionRequest, f bq.ColumnFilter) ([]bq.TableColumns, error) {
	s, err := m.region(ctx, req)
	return columnsOnly(s, err, f)
}

func (m meta) DatasetTables(ctx context.Context, req bq.DatasetRequest) (*api.Snapshot, error) {
	return tablesOnly(m.snapshot(ctx, req))
}

func (m meta) DatasetColumns(ctx context.Context, req bq.DatasetRequest, f bq.ColumnFilter) ([]bq.TableColumns, error) {
	s, err := m.snapshot(ctx, req)
	return columnsOnly(s, err, f)
}

func main() {
	ctx := context.Background()
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c, err := bq.NewClient(ctx, "test-proj", bq.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	svc, err := bqv2.NewService(ctx, bq.EndpointOptions()...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, Meta: func(context.Context) (bq.Metadata, error) { return meta{c, svc}, nil }})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer eng.Close()
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for range h.Events() {
	}
	res, err := h.Wait()
	fmt.Println(strings.TrimSpace(fmt.Sprintf("ok=%d degraded=%d failed=%d err=%v", res.UnitsOK, res.UnitsDegraded, res.UnitsFailed, err)))
	for _, u := range res.Units {
		fmt.Println(u.Project, u.Status, u.Tables, u.Error)
	}
}
