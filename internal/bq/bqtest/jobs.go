package bqtest

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// QueryScript says how the fake answers one query.
type QueryScript struct {
	StatementType string // default SELECT
	Bytes         int64
	Accuracy      string // default PRECISE
	ReservationID string
	Edition       string
	// DryRunErr fails the dry-run; JobErr fails the job after it started.
	DryRunErr error
	JobErr    error
	Schema    []bq.Field
	Rows      [][]any
	// Block keeps the job running until it is closed or the job is cancelled.
	Block        chan struct{}
	SlotMs       int64
	CacheHit     bool
	AffectedRows int64
}

// QueryCall records one DryRun or StartQuery request.
type QueryCall struct {
	Req bq.QueryRequest
	Dry bool
}

type fakeJob struct {
	script    QueryScript
	cancelled chan struct{}
	created   time.Time
}

// message is an error worded the way BigQuery words it (capitals and all).
type message string

func (m message) Error() string { return string(m) }

// QueryError builds a query failure the way BigQuery words it, for example
// QueryError("Syntax error: Unexpected keyword FROM at [1:8]").
func QueryError(msg string) error { return &bq.Error{Kind: bq.KindOther, Err: message(msg)} }

// Numbers is a script whose result has n rows with the columns n and label.
func Numbers(n int) QueryScript {
	s := QueryScript{Bytes: int64(n) * 100, Schema: []bq.Field{{Name: "n", Type: "INTEGER"}, {Name: "label", Type: "STRING"}}}
	for i := 0; i < n; i++ {
		s.Rows = append(s.Rows, []any{strconv.Itoa(i), "row " + strconv.Itoa(i)})
	}
	return s
}

func (f *Fake) script(sql string) QueryScript {
	f.mu.Lock()
	on := f.OnQuery
	f.mu.Unlock()
	s := Numbers(3)
	if on != nil {
		s = on(sql)
	}
	if s.StatementType == "" {
		s.StatementType = "SELECT"
	}
	if s.Accuracy == "" {
		s.Accuracy = "PRECISE"
	}
	return s
}

// QueryCalls returns the recorded query requests.
func (f *Fake) QueryCalls() []QueryCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]QueryCall(nil), f.queries...)
}

// DryRun implements bq.Jobs.
func (f *Fake) DryRun(ctx context.Context, req bq.QueryRequest) (bq.DryRunResult, error) {
	if err := f.begin(ctx, Call{Method: "DryRun", Location: req.Location}); err != nil {
		return bq.DryRunResult{}, err
	}
	f.mu.Lock()
	f.queries = append(f.queries, QueryCall{req, true})
	f.mu.Unlock()
	s := f.script(req.SQL)
	if s.DryRunErr != nil {
		return bq.DryRunResult{}, s.DryRunErr
	}
	loc := req.Location
	if loc == "" {
		loc = "EU"
	}
	return bq.DryRunResult{BytesProcessed: s.Bytes, Accuracy: s.Accuracy, StatementType: s.StatementType,
		ReservationID: s.ReservationID, Edition: s.Edition, Schema: s.Schema, Location: loc}, nil
}

// StartQuery implements bq.Jobs.
func (f *Fake) StartQuery(ctx context.Context, req bq.QueryRequest) (api.JobRef, error) {
	if err := f.begin(ctx, Call{Method: "StartQuery", Location: req.Location}); err != nil {
		return api.JobRef{}, err
	}
	s := f.script(req.SQL)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, QueryCall{req, false})
	if req.MaxBytesBilled > 0 && s.Bytes > req.MaxBytesBilled {
		return api.JobRef{}, &bq.Error{Kind: bq.KindOverLimit, Err: message(fmt.Sprintf("Query exceeded limit for bytes billed: %d. %d or higher required.", req.MaxBytesBilled, s.Bytes))}
	}
	if f.jobs == nil {
		f.jobs = map[string]*fakeJob{}
	}
	f.jobSeq++
	loc := req.Location
	if loc == "" {
		loc = "EU"
	}
	ref := api.JobRef{Project: "fake-exec", Location: loc, ID: fmt.Sprintf("job_%d", f.jobSeq)}
	f.jobs[ref.ID] = &fakeJob{script: s, cancelled: make(chan struct{}), created: time.Unix(1700000000+int64(f.jobSeq), 0).UTC()}
	return ref, nil
}

func (f *Fake) fakeJob(ref api.JobRef) (*fakeJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[ref.ID]
	if j == nil {
		return nil, &bq.Error{Kind: bq.KindNotFound, Err: message("Not found: Job " + ref.ID)}
	}
	return j, nil
}

// WaitJob implements bq.Jobs.
func (f *Fake) WaitJob(ctx context.Context, ref api.JobRef) (bq.JobResult, error) {
	j, err := f.fakeJob(ref)
	if err != nil {
		return bq.JobResult{}, err
	}
	if err := f.begin(ctx, Call{Method: "WaitJob", Location: ref.Location}); err != nil {
		return bq.JobResult{}, err
	}
	s := j.script
	res := bq.JobResult{StatementType: s.StatementType, ReservationID: s.ReservationID, Edition: s.Edition,
		Created: j.created, Started: j.created.Add(time.Second), Ended: j.created.Add(3 * time.Second)}
	if s.Block != nil {
		select {
		case <-s.Block:
		case <-j.cancelled:
		case <-ctx.Done():
			return bq.JobResult{}, bq.Wrap("fake", ctx.Err())
		}
	}
	select {
	case <-j.cancelled:
		res.Err = &bq.Error{Kind: bq.KindCancelled, Err: message("Job execution was cancelled: User requested cancellation")}
		return res, nil
	default:
	}
	if s.JobErr != nil {
		res.Err = s.JobErr
		return res, nil
	}
	res.BytesProcessed, res.BytesBilled, res.SlotMs, res.CacheHit, res.AffectedRows = s.Bytes, s.Bytes, s.SlotMs, s.CacheHit, s.AffectedRows
	res.Destination = "fake-exec._anon." + ref.ID
	return res, nil
}

// CancelJob implements bq.Jobs.
func (f *Fake) CancelJob(ctx context.Context, ref api.JobRef) error {
	j, err := f.fakeJob(ref)
	if err != nil {
		return err
	}
	if err := f.begin(ctx, Call{Method: "CancelJob", Location: ref.Location}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-j.cancelled:
	default:
		close(j.cancelled)
	}
	return nil
}

// QueryRows implements bq.Jobs.
func (f *Fake) QueryRows(ctx context.Context, ref api.JobRef, startIndex int64, maxRows int) (bq.ResultPage, error) {
	j, err := f.fakeJob(ref)
	if err != nil {
		return bq.ResultPage{}, err
	}
	if err := f.begin(ctx, Call{Method: "QueryRows", Location: ref.Location}); err != nil {
		return bq.ResultPage{}, err
	}
	rows := j.script.Rows
	page := bq.ResultPage{Schema: j.script.Schema, TotalRows: int64(len(rows))}
	lo := int(min(startIndex, int64(len(rows))))
	hi := min(lo+maxRows, len(rows))
	page.Rows = append(page.Rows, rows[lo:hi]...)
	return page, nil
}
