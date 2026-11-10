package bq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	jobs        jobTable
}

// Options configures NewClient.
type Options struct {
	// ExtraClientOptions are appended (tests, custom endpoints).
	ExtraClientOptions []option.ClientOption
	Log                *slog.Logger
}

// NewClient creates a client whose jobs run in executionProject using
// Application Default Credentials, with executionProject as the quota project.
// With no Execution project only ListProjects works (the credentials' own
// quota project applies).
func NewClient(ctx context.Context, executionProject string, o Options) (*Client, error) {
	var opts []option.ClientOption
	if executionProject != "" {
		opts = append(opts, option.WithQuotaProject(executionProject))
	}
	opts = append(opts, EndpointOptions()...)
	opts = append(opts, o.ExtraClientOptions...)
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

// ListProjects implements Metadata using REST projects.list.
func (c *Client) ListProjects(ctx context.Context) ([]api.ListedProject, error) {
	var out []api.ListedProject
	err := c.svc.Projects.List().MaxResults(1000).Context(ctx).Pages(ctx, func(r *bqv2.ProjectList) error {
		for _, p := range r.Projects {
			if p.ProjectReference == nil || p.ProjectReference.ProjectId == "" {
				continue
			}
			out = append(out, api.ListedProject{ID: p.ProjectReference.ProjectId, FriendlyName: p.FriendlyName})
		}
		return nil
	})
	if err != nil {
		return nil, Wrap("list projects", err)
	}
	return out, nil
}

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

// GetDataset implements Metadata using REST datasets.get.
func (c *Client) GetDataset(ctx context.Context, project, dataset string) (api.DatasetMeta, error) {
	d, err := c.svc.Datasets.Get(project, dataset).Context(ctx).Do()
	if err != nil {
		return api.DatasetMeta{}, Wrap(fmt.Sprintf("get dataset %s.%s", project, dataset), err)
	}
	return api.DatasetMeta{
		Project:      project,
		Name:         dataset,
		Location:     d.Location,
		Description:  d.Description,
		FriendlyName: d.FriendlyName,
		Labels:       d.Labels,
	}, nil
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

// EndpointEnv names a BigQuery endpoint to use instead of Google's, without
// credentials: a local emulator for manual testing (spec section 16).
const EndpointEnv = "BQT_BIGQUERY_ENDPOINT"

// EndpointOptions are the client options for EndpointEnv, nil when unset.
func EndpointOptions() []option.ClientOption {
	ep := strings.TrimSpace(os.Getenv(EndpointEnv))
	if ep == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(ep), option.WithoutAuthentication()}
}
