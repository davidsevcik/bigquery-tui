package bq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	bqapi "cloud.google.com/go/bigquery"
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

func (c *Client) snapshot(ctx context.Context, sc scope, location string, params []bqapi.QueryParameter) (*api.Snapshot, error) {
	snap := &api.Snapshot{}
	tables := map[tkey]*api.TableMeta{}
	var order []tkey

	// datasets (region scope only)
	if q, ok := sc.datasetsSQL(); ok {
		meta := map[string]*api.DatasetMeta{}
		err := c.query(ctx, location, q, params, func(r []bqapi.Value) error {
			name, opt, val := str(r[0]), str(r[1]), str(r[2])
			m := meta[name]
			if m == nil {
				m = &api.DatasetMeta{Name: name}
				meta[name] = m
			}
			switch opt {
			case "description":
				m.Description = parseStringOption(val)
			case "friendly_name":
				m.FriendlyName = parseStringOption(val)
			case "labels":
				m.Labels = parseLabelsOption(val)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, m := range meta {
			snap.Datasets = append(snap.Datasets, *m)
		}
	}

	err := c.query(ctx, location, sc.tablesSQL(), params, func(r []bqapi.Value) error {
		k := tkey{str(r[0]), str(r[1])}
		t := &api.TableMeta{Dataset: k.ds, Name: k.table, Type: api.ParseTableType(str(r[2]))}
		if ts, ok := r[3].(time.Time); ok {
			t.CreatedAt = ts.UTC()
		}
		tables[k] = t
		order = append(order, k)
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = c.query(ctx, location, sc.tableOptionsSQL(), params, func(r []bqapi.Value) error {
		t := tables[tkey{str(r[0]), str(r[1])}]
		if t == nil {
			return nil
		}
		val := str(r[3])
		switch str(r[2]) {
		case "description":
			t.Description = parseStringOption(val)
		case "labels":
			t.Labels = parseLabelsOption(val)
		case "expiration_timestamp":
			if ts, ok := parseTimestampOption(val); ok {
				t.ExpiresAt = &ts
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = c.query(ctx, location, sc.viewsSQL(), params, func(r []bqapi.Value) error {
		if t := tables[tkey{str(r[0]), str(r[1])}]; t != nil {
			t.ViewSQL = str(r[2])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	type colInfo struct {
		ordinal      int
		partitioning bool
		clustering   int
	}
	top := map[tkey]map[string]colInfo{}
	err = c.query(ctx, location, sc.columnsSQL(), params, func(r []bqapi.Value) error {
		k := tkey{str(r[0]), str(r[1])}
		m := top[k]
		if m == nil {
			m = map[string]colInfo{}
			top[k] = m
		}
		ci := colInfo{ordinal: int(num(r[3])), partitioning: str(r[4]) == "YES"}
		if r[5] != nil {
			ci.clustering = int(num(r[5]))
		}
		m[str(r[2])] = ci
		return nil
	})
	if err != nil {
		return nil, err
	}

	nested := map[tkey]map[string]int{} // per table: parent path -> next ordinal
	err = c.query(ctx, location, sc.fieldPathsSQL(), params, func(r []bqapi.Value) error {
		k := tkey{str(r[0]), str(r[1])}
		t := tables[k]
		if t == nil {
			return nil
		}
		cm := api.ColumnMeta{FieldPath: str(r[3]), DataType: str(r[4]), Description: str(r[5])}
		if cm.TopLevel() {
			ci := top[k][str(r[2])]
			cm.Ordinal, cm.Partitioning, cm.ClusteringOrdinal = ci.ordinal, ci.partitioning, ci.clustering
		} else {
			n := nested[k]
			if n == nil {
				n = map[string]int{}
				nested[k] = n
			}
			n[cm.ParentPath()]++
			cm.Ordinal = n[cm.ParentPath()]
		}
		t.Columns = append(t.Columns, cm)
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, k := range order {
		snap.Tables = append(snap.Tables, *tables[k])
	}
	return snap, nil
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
