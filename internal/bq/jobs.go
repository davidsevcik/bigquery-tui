package bq

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	bqapi "cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// QueryRequest is one query to dry-run or run.
type QueryRequest struct {
	SQL    string
	Params []api.QueryParam
	// Location is the job location; empty lets BigQuery infer it.
	Location string
	// MaxBytesBilled is the hard cap; 0 sets none.
	MaxBytesBilled int64
	Labels         map[string]string
}

// DryRunResult is what a dry-run reports.
type DryRunResult struct {
	BytesProcessed int64
	Accuracy       string
	StatementType  string
	ReservationID  string
	Edition        string
	Schema         []Field
	Location       string
}

// JobResult is the outcome of a finished job. Err is the job's own failure
// (a query error); the error WaitJob returns is a failure to learn the outcome.
type JobResult struct {
	Err            error
	BytesProcessed int64
	BytesBilled    int64
	SlotMs         int64
	CacheHit       bool
	AffectedRows   int64
	StatementType  string
	ReservationID  string
	Edition        string
	Destination    string
	Created        time.Time
	Started        time.Time
	Ended          time.Time
}

// ResultPage is a page of a query job's rows in the REST wire shape.
type ResultPage struct {
	Schema    []Field
	Rows      [][]any
	TotalRows int64
}

// Jobs runs queries as jobs in the Execution project.
type Jobs interface {
	// DryRun validates the query and reports its estimate without running it.
	DryRun(ctx context.Context, req QueryRequest) (DryRunResult, error)
	// StartQuery submits the job and returns as soon as BigQuery accepted it.
	StartQuery(ctx context.Context, req QueryRequest) (api.JobRef, error)
	// WaitJob blocks until the job has finished.
	WaitJob(ctx context.Context, ref api.JobRef) (JobResult, error)
	// CancelJob asks BigQuery to cancel; the job may still finish.
	CancelJob(ctx context.Context, ref api.JobRef) error
	// QueryRows reads a page of a finished query job's result (for a script,
	// the last statement's result).
	QueryRows(ctx context.Context, ref api.JobRef, startIndex int64, maxRows int) (ResultPage, error)
}

func (c *Client) userQuery(req QueryRequest, dry bool) (*bqapi.Query, error) {
	q := c.bq.Query(req.SQL)
	q.Location = req.Location
	q.DryRun = dry
	q.MaxBytesBilled = req.MaxBytesBilled
	q.Labels = req.Labels
	for _, p := range req.Params {
		qp, err := queryParam(p)
		if err != nil {
			return nil, err
		}
		q.Parameters = append(q.Parameters, qp)
	}
	return q, nil
}

func queryParam(p api.QueryParam) (bqapi.QueryParameter, error) {
	typ := strings.ToUpper(p.Type)
	if typ == "" {
		typ = "STRING"
	}
	switch typ {
	case "STRING", "INT64", "FLOAT64", "BOOL", "DATE", "DATETIME", "TIMESTAMP", "NUMERIC":
	default:
		return bqapi.QueryParameter{}, &Error{Kind: KindOther, Err: errors.New("parameter @" + p.Name + ": unsupported type " + p.Type)}
	}
	v := &bqapi.QueryParameterValue{Type: bqapi.StandardSQLDataType{TypeKind: typ}}
	if !p.Null {
		v.Value = p.Value
	} else {
		v.Value = bqapi.NullString{}
	}
	return bqapi.QueryParameter{Name: p.Name, Value: v}, nil
}

// DryRun implements Jobs.
func (c *Client) DryRun(ctx context.Context, req QueryRequest) (DryRunResult, error) {
	q, err := c.userQuery(req, true)
	if err != nil {
		return DryRunResult{}, err
	}
	job, err := q.Run(ctx)
	if err != nil {
		return DryRunResult{}, Wrap("dry-run", err)
	}
	st := job.LastStatus()
	out := DryRunResult{Location: job.Location()}
	if st == nil || st.Statistics == nil {
		return out, nil
	}
	out.ReservationID, out.Edition = st.Statistics.ReservationID, edition(st.Statistics.Edition)
	out.BytesProcessed = st.Statistics.TotalBytesProcessed
	if qs, ok := st.Statistics.Details.(*bqapi.QueryStatistics); ok {
		out.Accuracy, out.StatementType = qs.TotalBytesProcessedAccuracy, qs.StatementType
		out.Schema = fieldsOf(qs.Schema)
		if qs.TotalBytesProcessed > 0 {
			out.BytesProcessed = qs.TotalBytesProcessed
		}
	}
	return out, nil
}

func edition(e bqapi.ReservationEdition) string {
	if e == bqapi.ReservationEditionUnspecified {
		return ""
	}
	return string(e)
}

func fieldsOf(s bqapi.Schema) []Field {
	out := make([]Field, 0, len(s))
	for _, f := range s {
		mode := "NULLABLE"
		switch {
		case f.Repeated:
			mode = "REPEATED"
		case f.Required:
			mode = "REQUIRED"
		}
		out = append(out, Field{Name: f.Name, Type: string(f.Type), Mode: mode, Fields: fieldsOf(f.Schema)})
	}
	return out
}

// jobs keeps the handles of submitted jobs so waiting needs no extra lookup.
type jobTable struct {
	mu sync.Mutex
	m  map[api.JobRef]*bqapi.Job
}

func (t *jobTable) put(ref api.JobRef, j *bqapi.Job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[api.JobRef]*bqapi.Job{}
	}
	t.m[ref] = j
}

func (t *jobTable) get(ref api.JobRef) *bqapi.Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[ref]
}

func (t *jobTable) drop(ref api.JobRef) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, ref)
}

// StartQuery implements Jobs.
func (c *Client) StartQuery(ctx context.Context, req QueryRequest) (api.JobRef, error) {
	q, err := c.userQuery(req, false)
	if err != nil {
		return api.JobRef{}, err
	}
	job, err := q.Run(ctx)
	if err != nil {
		return api.JobRef{}, Wrap("start query", err)
	}
	ref := api.JobRef{Project: job.ProjectID(), Location: job.Location(), ID: job.ID()}
	c.jobs.put(ref, job)
	return ref, nil
}

func (c *Client) job(ctx context.Context, ref api.JobRef) (*bqapi.Job, error) {
	if j := c.jobs.get(ref); j != nil {
		return j, nil
	}
	j, err := c.bq.JobFromProject(ctx, ref.Project, ref.ID, ref.Location)
	if err != nil {
		return nil, Wrap("look up job "+ref.ID, err)
	}
	return j, nil
}

// WaitJob implements Jobs.
func (c *Client) WaitJob(ctx context.Context, ref api.JobRef) (JobResult, error) {
	job, err := c.job(ctx, ref)
	if err != nil {
		return JobResult{}, err
	}
	st, err := job.Wait(ctx)
	if err != nil {
		// Wait reports a failed or cancelled job as an error of its own. Ask
		// for the status so the caller gets the outcome with its statistics.
		if ctx.Err() != nil {
			return JobResult{}, Wrap("wait for job "+ref.ID, err)
		}
		var serr error
		if st, serr = job.Status(ctx); serr != nil || !st.Done() {
			return JobResult{}, Wrap("wait for job "+ref.ID, err)
		}
	}
	c.jobs.drop(ref)
	var out JobResult
	if jerr := st.Err(); jerr != nil {
		out.Err = Wrap("", jerr)
	}
	if s := st.Statistics; s != nil {
		out.Created, out.Started, out.Ended = s.CreationTime, s.StartTime, s.EndTime
		out.BytesProcessed, out.ReservationID, out.Edition = s.TotalBytesProcessed, s.ReservationID, edition(s.Edition)
		if qs, ok := s.Details.(*bqapi.QueryStatistics); ok {
			out.BytesBilled, out.SlotMs, out.CacheHit = qs.TotalBytesBilled, qs.SlotMillis, qs.CacheHit
			out.AffectedRows, out.StatementType = qs.NumDMLAffectedRows, qs.StatementType
			if qs.TotalBytesProcessed > 0 {
				out.BytesProcessed = qs.TotalBytesProcessed
			}
		}
	}
	if cfg, cerr := job.Config(); cerr == nil {
		if qc, ok := cfg.(*bqapi.QueryConfig); ok && qc.Dst != nil {
			out.Destination = qc.Dst.ProjectID + "." + qc.Dst.DatasetID + "." + qc.Dst.TableID
		}
	}
	return out, nil
}

// CancelJob implements Jobs.
func (c *Client) CancelJob(ctx context.Context, ref api.JobRef) error {
	job, err := c.job(ctx, ref)
	if err != nil {
		return err
	}
	return Wrap("cancel job "+ref.ID, job.Cancel(ctx))
}

// QueryRows implements Jobs with REST jobs.getQueryResults.
func (c *Client) QueryRows(ctx context.Context, ref api.JobRef, startIndex int64, maxRows int) (ResultPage, error) {
	call := c.svc.Jobs.GetQueryResults(ref.Project, ref.ID).Context(ctx).Location(ref.Location).
		StartIndex(uint64(startIndex)).MaxResults(int64(maxRows)).FormatOptionsUseInt64Timestamp(true)
	r, err := call.Do()
	if err != nil {
		return ResultPage{}, Wrap("read results of job "+ref.ID, err)
	}
	page := ResultPage{TotalRows: int64(r.TotalRows)}
	if r.Schema != nil {
		page.Schema = convertFields(r.Schema.Fields)
	}
	for _, row := range r.Rows {
		cells := make([]any, len(row.F))
		for i, cell := range row.F {
			cells[i] = cell.V
		}
		page.Rows = append(page.Rows, cells)
	}
	return page, nil
}

var errPosition = regexp.MustCompile(`\[(\d+):(\d+)\]`)

// authHint is shown for every credentials failure.
const authHint = "run: gcloud auth application-default login"

// QueryErrorOf turns a dry-run or job failure into plain data: the message
// without client wrapping, the position when BigQuery gave one, and a class.
func QueryErrorOf(err error) *api.QueryError {
	if err == nil {
		return nil
	}
	qe := &api.QueryError{Message: err.Error(), Kind: "other"}
	var be *bqapi.Error
	var ge *googleapi.Error
	reason := ""
	switch {
	case errors.As(err, &be):
		qe.Message, reason = be.Message, be.Reason
	case errors.As(err, &ge):
		qe.Message = ge.Message
		if len(ge.Errors) > 0 {
			reason = ge.Errors[0].Reason
			if ge.Errors[0].Message != "" {
				qe.Message = ge.Errors[0].Message
			}
		}
	default:
		var w *Error
		if errors.As(err, &w) && w.Err != nil {
			qe.Message = w.Err.Error()
		}
	}
	switch KindOf(err) {
	case KindOverLimit:
		qe.Kind = "over_limit"
		qe.Hint = "the query is over max_bytes_billed; narrow it or raise the cap in the config"
	case KindAuth:
		qe.Kind, qe.Hint = "auth", authHint
	case KindPermission:
		qe.Kind = "permission"
	case KindNotFound:
		qe.Kind = "not_found"
	case KindTransient:
		qe.Kind = "transient"
	case KindCancelled:
		qe.Kind = "cancelled"
	default:
		if reason == "invalidQuery" || reason == "invalid" || errPosition.MatchString(qe.Message) {
			qe.Kind = "query"
		}
	}
	if reason == "invalidQuery" || reason == "invalid" || reason == "notFound" {
		if qe.Kind == "other" {
			qe.Kind = "query"
		}
	}
	if m := errPosition.FindStringSubmatch(qe.Message); m != nil {
		qe.Line, _ = strconv.Atoi(m[1])
		qe.Col, _ = strconv.Atoi(m[2])
	}
	return qe
}
