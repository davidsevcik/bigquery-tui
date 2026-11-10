package bq

import (
	"context"
	"fmt"
	"strings"
	"time"

	bqapi "cloud.google.com/go/bigquery"
	"golang.org/x/sync/errgroup"
	bqv2 "google.golang.org/api/bigquery/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// RegionTables implements Metadata with region-qualified views (phase 1).
func (c *Client) RegionTables(ctx context.Context, req RegionRequest) (*api.Snapshot, error) {
	sc := regionScope(req.Project, req.Location)
	params := []bqapi.QueryParameter{{Name: "ds", Value: nonNil(req.Datasets)}}
	snap, err := c.tables(ctx, sc, req.Location, params)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("region tables %s/%s", req.Project, req.Location), err)
	}
	// SCHEMATA_OPTIONS only lists datasets that carry options, so start from
	// the requested names and overlay what the view returned.
	byName := map[string]api.DatasetMeta{}
	for _, d := range snap.Datasets {
		byName[d.Name] = d
	}
	snap.Datasets = snap.Datasets[:0]
	for _, name := range req.Datasets {
		d := byName[name]
		d.Name, d.Project, d.Location = name, req.Project, req.Location
		snap.Datasets = append(snap.Datasets, d)
	}
	return snap, nil
}

// RegionColumns implements Metadata with region-qualified views (phase 2).
func (c *Client) RegionColumns(ctx context.Context, req RegionRequest, f ColumnFilter) ([]TableColumns, error) {
	sc := regionScope(req.Project, req.Location)
	params := append([]bqapi.QueryParameter{{Name: "ds", Value: nonNil(req.Datasets)}}, f.params()...)
	cols, err := c.columns(ctx, sc, req.Location, params, f)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("region columns %s/%s", req.Project, req.Location), err)
	}
	return cols, nil
}

// DatasetTables implements Metadata with dataset-qualified views (phase 1).
func (c *Client) DatasetTables(ctx context.Context, req DatasetRequest) (*api.Snapshot, error) {
	sc := datasetScope(req.Project, req.Dataset)
	snap, err := c.tables(ctx, sc, req.Location, nil)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("dataset tables %s.%s", req.Project, req.Dataset), err)
	}
	snap.Datasets = []api.DatasetMeta{{Project: req.Project, Name: req.Dataset, Location: req.Location}}
	for i := range snap.Tables {
		snap.Tables[i].Dataset = req.Dataset
	}
	return snap, nil
}

// DatasetColumns implements Metadata with dataset-qualified views (phase 2).
func (c *Client) DatasetColumns(ctx context.Context, req DatasetRequest, f ColumnFilter) ([]TableColumns, error) {
	sc := datasetScope(req.Project, req.Dataset)
	cols, err := c.columns(ctx, sc, req.Location, f.params(), f)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("dataset columns %s.%s", req.Project, req.Dataset), err)
	}
	for i := range cols {
		cols[i].Dataset = req.Dataset
	}
	return cols, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// params are the query parameters the filter's clause names.
func (f ColumnFilter) params() []bqapi.QueryParameter {
	switch {
	case len(f.Only) > 0:
		return []bqapi.QueryParameter{{Name: "only", Value: f.Only}}
	case f.Pattern != "":
		return []bqapi.QueryParameter{{Name: "shard_re", Value: f.Pattern}, {Name: "keep", Value: nonNil(f.Keep)}}
	}
	return nil
}

// clause is the WHERE condition that leaves collapsed shard members out on
// BigQuery's side.
func (f ColumnFilter) clause() string {
	const name = "CONCAT(table_schema, '.', table_name)"
	switch {
	case len(f.Only) > 0:
		return name + " IN UNNEST(@only)"
	case f.Pattern != "":
		return "(NOT REGEXP_CONTAINS(table_name, @shard_re) OR " + name + " IN UNNEST(@keep))"
	}
	return "TRUE"
}

type tkey struct{ ds, table string }

type (
	dsOptRow struct{ name, opt, val string }
	tableRow struct {
		key     tkey
		typ     string
		created time.Time
	}
	optRow struct {
		key      tkey
		opt, val string
	}
	viewRow struct {
		key tkey
		sql string
	}
	colRow struct {
		key        tkey
		col        string
		ordinal    int
		partition  bool
		clustering int
		required   bool
	}
	fieldRow struct {
		key            tkey
		col, path, typ string
		desc           string
	}
)

// group runs INFORMATION_SCHEMA queries concurrently, each producing its own
// typed rows. On a 1,200-dataset region this halves the wall time of a
// sequential run.
type group struct {
	c        *Client
	g        *errgroup.Group
	ctx      context.Context
	location string
	params   []bqapi.QueryParameter
}

func (c *Client) group(ctx context.Context, location string, params []bqapi.QueryParameter) *group {
	g, gctx := errgroup.WithContext(ctx)
	return &group{c: c, g: g, ctx: gctx, location: location, params: params}
}

func (g *group) run(name, sql string, fn func(r []bqapi.Value)) {
	g.g.Go(func() error {
		start := time.Now()
		n := 0
		err := g.c.query(g.ctx, g.location, sql, g.params, func(r []bqapi.Value) error {
			fn(r)
			n++
			return nil
		})
		g.c.log.Debug("information_schema query", "view", name, "location", g.location, "rows", n, "took", time.Since(start).Round(time.Millisecond), "err", err)
		return err
	})
}

// tables is phase 1: datasets, tables, table options and views, no columns.
func (c *Client) tables(ctx context.Context, sc scope, location string, params []bqapi.QueryParameter) (*api.Snapshot, error) {
	var (
		dsOpts []dsOptRow
		tables []tableRow
		opts   []optRow
		views  []viewRow
	)
	g := c.group(ctx, location, params)
	if q, ok := sc.datasetsSQL(); ok {
		g.run("SCHEMATA_OPTIONS", q, func(r []bqapi.Value) { dsOpts = append(dsOpts, dsOptRow{str(r[0]), str(r[1]), str(r[2])}) })
	}
	g.run("TABLES", sc.tablesSQL(), func(r []bqapi.Value) {
		t, _ := r[3].(time.Time)
		tables = append(tables, tableRow{tkey{str(r[0]), str(r[1])}, str(r[2]), t})
	})
	g.run("TABLE_OPTIONS", sc.tableOptionsSQL(), func(r []bqapi.Value) {
		opts = append(opts, optRow{tkey{str(r[0]), str(r[1])}, str(r[2]), str(r[3])})
	})
	g.run("VIEWS", sc.viewsSQL(), func(r []bqapi.Value) { views = append(views, viewRow{tkey{str(r[0]), str(r[1])}, str(r[2])}) })
	if err := g.g.Wait(); err != nil {
		return nil, err
	}
	return mergeTables(dsOpts, tables, opts, views), nil
}

// columns is phase 2: COLUMNS and COLUMN_FIELD_PATHS for the tables the
// filter keeps.
func (c *Client) columns(ctx context.Context, sc scope, location string, params []bqapi.QueryParameter, f ColumnFilter) ([]TableColumns, error) {
	var (
		cols   []colRow
		fields []fieldRow
	)
	g := c.group(ctx, location, params)
	where := f.clause()
	g.run("COLUMNS", sc.columnsSQL(where), func(r []bqapi.Value) {
		cr := colRow{key: tkey{str(r[0]), str(r[1])}, col: str(r[2]), ordinal: int(num(r[3])), partition: str(r[4]) == "YES"}
		if r[5] != nil {
			cr.clustering = int(num(r[5]))
		}
		// REPEATED columns are also reported as not nullable
		cr.required = str(r[6]) == "NO" && !strings.HasPrefix(str(r[7]), "ARRAY<")
		cols = append(cols, cr)
	})
	g.run("COLUMN_FIELD_PATHS", sc.fieldPathsSQL(where), func(r []bqapi.Value) {
		fields = append(fields, fieldRow{key: tkey{str(r[0]), str(r[1])}, col: str(r[2]), path: str(r[3]), typ: str(r[4]), desc: str(r[5])})
	})
	if err := g.g.Wait(); err != nil {
		return nil, err
	}
	return mergeColumns(cols, fields), nil
}

func mergeTables(dsOpts []dsOptRow, tables []tableRow, opts []optRow, views []viewRow) *api.Snapshot {
	snap := &api.Snapshot{}

	meta := map[string]*api.DatasetMeta{}
	for _, r := range dsOpts {
		m := meta[r.name]
		if m == nil {
			m = &api.DatasetMeta{Name: r.name}
			meta[r.name] = m
		}
		switch r.opt {
		case "description":
			m.Description = parseStringOption(r.val)
		case "friendly_name":
			m.FriendlyName = parseStringOption(r.val)
		case "labels":
			m.Labels = parseLabelsOption(r.val)
		}
	}
	for _, m := range meta {
		snap.Datasets = append(snap.Datasets, *m)
	}

	byKey := make(map[tkey]*api.TableMeta, len(tables))
	order := make([]tkey, 0, len(tables))
	for _, r := range tables {
		t := &api.TableMeta{Dataset: r.key.ds, Name: r.key.table, Type: api.ParseTableType(r.typ)}
		if !r.created.IsZero() {
			t.CreatedAt = r.created.UTC()
		}
		byKey[r.key] = t
		order = append(order, r.key)
	}
	for _, r := range opts {
		t := byKey[r.key]
		if t == nil {
			continue
		}
		switch r.opt {
		case "description":
			t.Description = parseStringOption(r.val)
		case "labels":
			t.Labels = parseLabelsOption(r.val)
		case "expiration_timestamp":
			if ts, ok := parseTimestampOption(r.val); ok {
				t.ExpiresAt = &ts
			}
		case "require_partition_filter":
			t.RequirePartitionFilter = parseBoolOption(r.val)
		}
	}
	for _, r := range views {
		if t := byKey[r.key]; t != nil {
			t.ViewSQL = r.sql
		}
	}
	snap.Tables = make([]api.TableMeta, 0, len(order))
	for _, k := range order {
		snap.Tables = append(snap.Tables, *byKey[k])
	}
	return snap
}

func mergeColumns(cols []colRow, fields []fieldRow) []TableColumns {
	type colInfo struct {
		ordinal      int
		partitioning bool
		clustering   int
		required     bool
	}
	top := map[tkey]map[string]colInfo{}
	for _, r := range cols {
		m := top[r.key]
		if m == nil {
			m = map[string]colInfo{}
			top[r.key] = m
		}
		m[r.col] = colInfo{r.ordinal, r.partition, r.clustering, r.required}
	}
	at := map[tkey]int{}
	var out []TableColumns
	nested := map[tkey]map[string]int{}
	for _, r := range fields {
		i, ok := at[r.key]
		if !ok {
			i = len(out)
			at[r.key] = i
			out = append(out, TableColumns{Dataset: r.key.ds, Table: r.key.table})
		}
		cm := api.ColumnMeta{FieldPath: r.path, DataType: r.typ, Description: r.desc}
		if cm.TopLevel() {
			ci := top[r.key][r.col]
			cm.Ordinal, cm.Partitioning, cm.ClusteringOrdinal, cm.Required = ci.ordinal, ci.partitioning, ci.clustering, ci.required
		} else {
			n := nested[r.key]
			if n == nil {
				n = map[string]int{}
				nested[r.key] = n
			}
			n[cm.ParentPath()]++
			cm.Ordinal = n[cm.ParentPath()]
		}
		out[i].Columns = append(out[i].Columns, cm)
	}
	return out
}

// GetTableMeta implements Metadata with REST tables.get.
func (c *Client) GetTableMeta(ctx context.Context, project, dataset, table string) (api.TableMeta, error) {
	t, err := c.svc.Tables.Get(project, dataset, table).Context(ctx).Do()
	if err != nil {
		return api.TableMeta{}, Wrap(fmt.Sprintf("get table %s.%s.%s", project, dataset, table), err)
	}
	return tableMetaOf(dataset, t), nil
}

// tableMetaOf converts a REST table into the catalog's shape, the way the
// INFORMATION_SCHEMA views report it: standard SQL type names, one column
// per field path, partitioning and clustering on the top-level columns, and
// REQUIRED on top-level columns only.
func tableMetaOf(dataset string, t *bqv2.Table) api.TableMeta {
	name := table(t)
	m := api.TableMeta{Dataset: dataset, Name: name, Type: api.ParseTableType(t.Type), Description: t.Description, Labels: t.Labels}
	if len(m.Labels) == 0 {
		m.Labels = nil
	}
	if t.CreationTime > 0 {
		m.CreatedAt = time.UnixMilli(t.CreationTime).UTC()
	}
	if t.ExpirationTime > 0 {
		e := time.UnixMilli(t.ExpirationTime).UTC()
		m.ExpiresAt = &e
	}
	switch {
	case t.View != nil:
		m.ViewSQL = t.View.Query
	case t.MaterializedView != nil:
		m.ViewSQL = t.MaterializedView.Query
	}
	part := ""
	switch {
	case t.TimePartitioning != nil:
		part = t.TimePartitioning.Field
		m.RequirePartitionFilter = t.RequirePartitionFilter || t.TimePartitioning.RequirePartitionFilter
	case t.RangePartitioning != nil:
		part = t.RangePartitioning.Field
		m.RequirePartitionFilter = t.RequirePartitionFilter
	default:
		m.RequirePartitionFilter = t.RequirePartitionFilter
	}
	clus := map[string]int{}
	if t.Clustering != nil {
		for i, f := range t.Clustering.Fields {
			clus[f] = i + 1
		}
	}
	if t.Schema != nil {
		for i, f := range t.Schema.Fields {
			m.Columns = append(m.Columns, api.ColumnMeta{
				FieldPath: f.Name, Ordinal: i + 1, DataType: sqlType(f), Description: f.Description,
				Partitioning: part != "" && f.Name == part, ClusteringOrdinal: clus[f.Name], Required: f.Mode == "REQUIRED",
			})
			m.Columns = appendNested(m.Columns, f.Name, f.Fields)
		}
	}
	return m
}

func table(t *bqv2.Table) string {
	if t.TableReference == nil {
		return ""
	}
	return t.TableReference.TableId
}

func appendNested(out []api.ColumnMeta, parent string, fs []*bqv2.TableFieldSchema) []api.ColumnMeta {
	for i, f := range fs {
		path := parent + "." + f.Name
		out = append(out, api.ColumnMeta{FieldPath: path, Ordinal: i + 1, DataType: sqlType(f), Description: f.Description})
		out = appendNested(out, path, f.Fields)
	}
	return out
}

// sqlType writes a REST field's type as COLUMN_FIELD_PATHS.data_type does.
func sqlType(f *bqv2.TableFieldSchema) string {
	var s string
	switch strings.ToUpper(f.Type) {
	case "INTEGER", "INT64":
		s = "INT64"
	case "FLOAT", "FLOAT64":
		s = "FLOAT64"
	case "BOOLEAN", "BOOL":
		s = "BOOL"
	case "RECORD", "STRUCT":
		parts := make([]string, len(f.Fields))
		for i, sub := range f.Fields {
			parts[i] = sub.Name + " " + sqlType(sub)
		}
		s = "STRUCT<" + strings.Join(parts, ", ") + ">"
	default:
		s = strings.ToUpper(f.Type)
	}
	if f.Mode == "REPEATED" {
		s = "ARRAY<" + s + ">"
	}
	return s
}
