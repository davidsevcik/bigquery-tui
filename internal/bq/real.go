package bq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	bqapi "cloud.google.com/go/bigquery"
	"golang.org/x/sync/errgroup"
	bqv2 "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// SyncMaxBytesBilled is the safety cap on every sync query (10 GiB). An
// INFORMATION_SCHEMA query bills the 10 MiB minimum, so this only guards
// against bugs.
const SyncMaxBytesBilled = 10 << 30

// Client is the real bq.Metadata backed by BigQuery.
type Client struct {
	bq          *bqapi.Client
	svc         *bqv2.Service
	execProject string
	log         *slog.Logger
}

// Options configures NewClient.
type Options struct {
	// ExtraClientOptions are appended (tests, custom endpoints).
	ExtraClientOptions []option.ClientOption
	Log                *slog.Logger
}

// NewClient creates a client whose jobs run in executionProject using
// Application Default Credentials, with executionProject as the quota project.
func NewClient(ctx context.Context, executionProject string, o Options) (*Client, error) {
	opts := append([]option.ClientOption{option.WithQuotaProject(executionProject)}, o.ExtraClientOptions...)
	c, err := bqapi.NewClient(ctx, executionProject, opts...)
	if err != nil {
		return nil, Wrap("create BigQuery client", err)
	}
	svc, err := bqv2.NewService(ctx, opts...)
	if err != nil {
		c.Close()
		return nil, Wrap("create BigQuery REST service", err)
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &Client{bq: c, svc: svc, execProject: executionProject, log: log}, nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Close releases the clients.
func (c *Client) Close() error { return c.bq.Close() }

// ListDatasets implements Metadata using REST datasets.list, which (unlike the
// Go iterator) returns each dataset's location.
func (c *Client) ListDatasets(ctx context.Context, project string) (DatasetList, error) {
	var out DatasetList
	seenUnreachable := map[string]bool{}
	err := c.svc.Datasets.List(project).MaxResults(1000).Context(ctx).Pages(ctx, func(r *bqv2.DatasetList) error {
		for _, d := range r.Datasets {
			if d.DatasetReference == nil {
				continue
			}
			out.Datasets = append(out.Datasets, api.DatasetMeta{
				Project:      d.DatasetReference.ProjectId,
				Name:         d.DatasetReference.DatasetId,
				Location:     d.Location,
				FriendlyName: d.FriendlyName,
				Labels:       d.Labels,
			})
		}
		for _, u := range r.Unreachable {
			if !seenUnreachable[u] {
				seenUnreachable[u] = true
				out.Unreachable = append(out.Unreachable, u)
			}
		}
		return nil
	})
	if err != nil {
		return DatasetList{}, Wrap("list datasets of "+project, err)
	}
	return out, nil
}

// RegionSnapshot implements Metadata with region-qualified views.
func (c *Client) RegionSnapshot(ctx context.Context, req RegionRequest) (*api.Snapshot, error) {
	sc := regionScope(req.Project, req.Location)
	params := []bqapi.QueryParameter{{Name: "ds", Value: req.Datasets}}
	snap, err := c.snapshot(ctx, sc, req.Location, params)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("region snapshot %s/%s", req.Project, req.Location), err)
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

// DatasetSnapshot implements Metadata with dataset-qualified views.
func (c *Client) DatasetSnapshot(ctx context.Context, req DatasetRequest) (*api.Snapshot, error) {
	sc := datasetScope(req.Project, req.Dataset)
	snap, err := c.snapshot(ctx, sc, req.Location, nil)
	if err != nil {
		return nil, Wrap(fmt.Sprintf("dataset snapshot %s.%s", req.Project, req.Dataset), err)
	}
	snap.Datasets = []api.DatasetMeta{{Project: req.Project, Name: req.Dataset, Location: req.Location}}
	for i := range snap.Tables {
		snap.Tables[i].Dataset = req.Dataset
	}
	return snap, nil
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

// snapshot runs the independent INFORMATION_SCHEMA queries concurrently, each
// producing its own typed rows, then merges them into one api.Snapshot. On a
// 1,200-dataset region this halves the wall time of a sequential run.
func (c *Client) snapshot(ctx context.Context, sc scope, location string, params []bqapi.QueryParameter) (*api.Snapshot, error) {
	var (
		dsOpts []dsOptRow
		tables []tableRow
		opts   []optRow
		views  []viewRow
		cols   []colRow
		fields []fieldRow
	)
	g, gctx := errgroup.WithContext(ctx)
	run := func(name, sql string, fn func(r []bqapi.Value)) {
		g.Go(func() error {
			start := time.Now()
			n := 0
			err := c.query(gctx, location, sql, params, func(r []bqapi.Value) error {
				fn(r)
				n++
				return nil
			})
			c.log.Debug("information_schema query", "view", name, "location", location, "rows", n, "took", time.Since(start).Round(time.Millisecond), "err", err)
			return err
		})
	}
	if q, ok := sc.datasetsSQL(); ok {
		run("SCHEMATA_OPTIONS", q, func(r []bqapi.Value) { dsOpts = append(dsOpts, dsOptRow{str(r[0]), str(r[1]), str(r[2])}) })
	}
	run("TABLES", sc.tablesSQL(), func(r []bqapi.Value) {
		t, _ := r[3].(time.Time)
		tables = append(tables, tableRow{tkey{str(r[0]), str(r[1])}, str(r[2]), t})
	})
	run("TABLE_OPTIONS", sc.tableOptionsSQL(), func(r []bqapi.Value) {
		opts = append(opts, optRow{tkey{str(r[0]), str(r[1])}, str(r[2]), str(r[3])})
	})
	run("VIEWS", sc.viewsSQL(), func(r []bqapi.Value) { views = append(views, viewRow{tkey{str(r[0]), str(r[1])}, str(r[2])}) })
	run("COLUMNS", sc.columnsSQL(), func(r []bqapi.Value) {
		cr := colRow{key: tkey{str(r[0]), str(r[1])}, col: str(r[2]), ordinal: int(num(r[3])), partition: str(r[4]) == "YES"}
		if r[5] != nil {
			cr.clustering = int(num(r[5]))
		}
		// REPEATED columns are also reported as not nullable
		cr.required = str(r[6]) == "NO" && !strings.HasPrefix(str(r[7]), "ARRAY<")
		cols = append(cols, cr)
	})
	run("COLUMN_FIELD_PATHS", sc.fieldPathsSQL(), func(r []bqapi.Value) {
		fields = append(fields, fieldRow{key: tkey{str(r[0]), str(r[1])}, col: str(r[2]), path: str(r[3]), typ: str(r[4]), desc: str(r[5])})
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return mergeSnapshot(dsOpts, tables, opts, views, cols, fields), nil
}

func mergeSnapshot(dsOpts []dsOptRow, tables []tableRow, opts []optRow, views []viewRow, cols []colRow, fields []fieldRow) *api.Snapshot {
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
		}
	}
	for _, r := range views {
		if t := byKey[r.key]; t != nil {
			t.ViewSQL = r.sql
		}
	}

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
	nested := map[tkey]map[string]int{}
	for _, r := range fields {
		t := byKey[r.key]
		if t == nil {
			continue
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
		t.Columns = append(t.Columns, cm)
	}

	snap.Tables = make([]api.TableMeta, 0, len(order))
	for _, k := range order {
		snap.Tables = append(snap.Tables, *byKey[k])
	}
	return snap
}

// query runs one labelled, capped, explicitly located query and streams rows.
func (c *Client) query(ctx context.Context, location, sql string, params []bqapi.QueryParameter, fn func(row []bqapi.Value) error) error {
	q := c.bq.Query(sql)
	q.Location = location
	q.Parameters = params
	q.MaxBytesBilled = SyncMaxBytesBilled
	q.Labels = map[string]string{"bqt": "1", "bqt_source": "sync"}
	it, err := q.Read(ctx)
	if err != nil {
		return err
	}
	for {
		var row []bqapi.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(row); err != nil {
			return err
		}
	}
}

// ListTableNames implements Metadata with REST tables.list.
func (c *Client) ListTableNames(ctx context.Context, project, dataset string) ([]api.TableMeta, error) {
	var out []api.TableMeta
	err := c.svc.Tables.List(project, dataset).MaxResults(1000).Context(ctx).Pages(ctx, func(r *bqv2.TableList) error {
		for _, t := range r.Tables {
			if t.TableReference == nil {
				continue
			}
			tm := api.TableMeta{
				Dataset:   dataset,
				Name:      t.TableReference.TableId,
				Type:      api.ParseTableType(t.Type),
				Labels:    t.Labels,
				NamesOnly: true,
			}
			if t.CreationTime > 0 {
				tm.CreatedAt = time.UnixMilli(t.CreationTime).UTC()
			}
			out = append(out, tm)
		}
		return nil
	})
	if err != nil {
		return nil, Wrap(fmt.Sprintf("list tables of %s.%s", project, dataset), err)
	}
	return out, nil
}

func str(v bqapi.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func num(v bqapi.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}
