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
	"google.golang.org/grpc"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

const SyncMaxBytesBilled = 10 << 30

type Client struct {
	bq            *bqapi.Client
	syncBQ        *bqapi.Client
	reads         readPath
	sessionBudget time.Duration
	svc           *bqv2.Service
	execProject   string
	log           *slog.Logger
	jobs          jobTable
}

type Options struct {
	ExtraClientOptions []option.ClientOption
	Log                *slog.Logger
	NoStorageRead      bool
	StorageReadOptions []option.ClientOption
}

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
	client := &Client{bq: c, svc: svc, execProject: executionProject, log: log, sessionBudget: defaultReadSessionBudget}
	if executionProject != "" && !o.NoStorageRead && EndpointOptions() == nil {
		client.syncBQ, err = newStorageReadClient(ctx, executionProject, opts, o.StorageReadOptions)
		if err != nil {
			log.Warn("Storage Read client not created, sync reads results through REST paging", "execution_project", executionProject, "err", err)
		}
	}
	return client, nil
}

func newStorageReadClient(ctx context.Context, executionProject string, opts, storageOpts []option.ClientOption) (*bqapi.Client, error) {
	c, err := bqapi.NewClient(ctx, executionProject, opts...)
	if err != nil {
		return nil, err
	}
	ropts := []option.ClientOption{
		option.WithQuotaProject(executionProject),
		option.WithGRPCDialOption(grpc.WithChainUnaryInterceptor(sessionInterceptor)),
	}
	ropts = append(ropts, storageOpts...)
	if err := c.EnableStorageReadClient(ctx, ropts...); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (c *Client) Close() error {
	err := c.bq.Close()
	if c.syncBQ != nil {
		err = errors.Join(err, c.syncBQ.Close())
	}
	return err
}

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

func (c *Client) ListDatasets(ctx context.Context, project string) (DatasetList, error) {
	var out DatasetList
	seenUnreachable := map[string]bool{}
	// ADR-0005
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

func (c *Client) query(ctx context.Context, location, sql string, params []bqapi.QueryParameter, fn func(row []bqapi.Value) error) (string, error) {
	client, watch := c.bq, (*sessionWatch)(nil)
	if c.syncBQ != nil {
		if storage, warning := c.reads.storage(); storage {
			client, watch = c.syncBQ, &sessionWatch{budget: c.sessionBudget}
			ctx = watchSessions(ctx, watch)
		} else {
			Warn(ctx, warning)
		}
	}
	q := client.Query(sql)
	q.Location = location
	q.Parameters = params
	q.MaxBytesBilled = SyncMaxBytesBilled
	q.Labels = map[string]string{"bqt": "1", "bqt_source": "sync"}
	it, err := q.Read(ctx)
	if err != nil {
		return readREST, err
	}
	read := c.readOutcome(ctx, watch)
	for {
		var row []bqapi.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return read, nil
		}
		if err != nil {
			return read, err
		}
		if err := fn(row); err != nil {
			return read, err
		}
	}
}

func (c *Client) readOutcome(ctx context.Context, watch *sessionWatch) string {
	if watch == nil {
		return readREST
	}
	attempted, err := watch.outcome()
	switch {
	case !attempted:
		return readREST
	case err == nil:
		return readStorage
	}
	warning, first := c.reads.sessionFailed(c.execProject, err)
	switch {
	case first:
		c.log.Warn("Storage Read API not available, sync reads results through REST paging", "execution_project", c.execProject, "err", err)
		Warn(ctx, warning)
	case warning != "":
		Warn(ctx, warning)
	default:
		c.log.Info("read session failed, this result is read through REST paging", "execution_project", c.execProject, "err", err)
	}
	return readRESTFallback
}

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

const EndpointEnv = "BQT_BIGQUERY_ENDPOINT"

func EndpointOptions() []option.ClientOption {
	ep := strings.TrimSpace(os.Getenv(EndpointEnv))
	if ep == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(ep), option.WithoutAuthentication()}
}
