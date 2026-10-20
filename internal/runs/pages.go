package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// ErrNoResult is returned when a Run has no rows to page (it has not
// finished, it failed, or the statement returns none).
var ErrNoResult = errors.New("this run has no result rows")

// page returns the raw page that starts at a multiple of PageRows, from the
// cache or from BigQuery. The cache keeps the PageCache most recently used.
func (r *Run) page(ctx context.Context, start int64) (*bq.ResultPage, error) {
	r.mu.Lock()
	if !r.info.HasResult || r.info.Job == nil {
		r.mu.Unlock()
		return nil, ErrNoResult
	}
	if p := r.pages[start]; p != nil {
		r.touch(start)
		r.mu.Unlock()
		return p, nil
	}
	ref, jobs, total := *r.info.Job, r.jobs, r.info.TotalRows
	r.mu.Unlock()
	p, err := fullPage(ctx, jobs, ref, start, total)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pages[start] = &p
	r.touch(start)
	for len(r.pageUse) > PageCache {
		delete(r.pages, r.pageUse[0])
		r.pageUse = r.pageUse[1:]
	}
	return &p, nil
}

// fullPage reads PageRows rows from start. BigQuery cuts a response short
// when the rows are wide (a size limit, not the end of the result), so it
// keeps asking until the page is full or the result ends. Without this a
// short response would look like missing rows, and an export would stop early.
func fullPage(ctx context.Context, jobs bq.Jobs, ref api.JobRef, start, total int64) (bq.ResultPage, error) {
	return readRows(ctx, jobs, ref, start, PageRows, total)
}

// readRows reads up to n rows from start (total 0: not known yet).
func readRows(ctx context.Context, jobs bq.Jobs, ref api.JobRef, start int64, n int, total int64) (bq.ResultPage, error) {
	var page bq.ResultPage
	for len(page.Rows) < n {
		at := start + int64(len(page.Rows))
		if total > 0 && at >= total {
			break
		}
		p, err := jobs.QueryRows(ctx, ref, at, n-len(page.Rows))
		if err != nil {
			return bq.ResultPage{}, err
		}
		if page.Schema == nil {
			page.Schema, page.TotalRows = p.Schema, p.TotalRows
		}
		if len(p.Rows) == 0 {
			break
		}
		page.Rows = append(page.Rows, p.Rows...)
		if total == 0 {
			total = p.TotalRows
		}
	}
	return page, nil
}

func (r *Run) touch(start int64) {
	for i, s := range r.pageUse {
		if s == start {
			r.pageUse = append(r.pageUse[:i], r.pageUse[i+1:]...)
			break
		}
	}
	r.pageUse = append(r.pageUse, start)
}

// Rows returns up to n result rows from startIndex, formatted with the given
// options. It reads as many server pages as the range spans.
func (r *Run) Rows(ctx context.Context, startIndex int64, n int, o format.Options) (api.PreviewPage, error) {
	info := r.Info()
	if !info.HasResult {
		return api.PreviewPage{}, ErrNoResult
	}
	out := api.PreviewPage{Columns: info.Columns, StartIndex: startIndex, TotalRows: info.TotalRows, Rows: [][]api.Cell{}}
	if startIndex < 0 {
		startIndex = 0
	}
	for i := startIndex; i < startIndex+int64(n) && i < info.TotalRows; {
		base := i - i%PageRows
		p, err := r.page(ctx, base)
		if err != nil {
			return out, err
		}
		off := int(i - base)
		if off >= len(p.Rows) {
			break // BigQuery returned a short page
		}
		for ; off < len(p.Rows) && i < startIndex+int64(n); off, i = off+1, i+1 {
			out.Rows = append(out.Rows, format.Row(r.schema, p.Rows[off], o))
		}
	}
	return out, nil
}

// Schema returns the result's schema (nil without a result).
func (r *Run) Schema() []bq.Field {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.schema
}

// RawRows returns unformatted rows with their schema (export).
func (r *Run) RawRows(ctx context.Context, startIndex int64) ([]bq.Field, [][]any, error) {
	base := startIndex - startIndex%PageRows
	p, err := r.page(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	off := int(startIndex - base)
	if off >= len(p.Rows) {
		return r.schema, nil, nil
	}
	return r.schema, p.Rows[off:], nil
}

// Reopen shows the result of an earlier job (a History entry) as a finished
// Run. BigQuery keeps a query's result for about 24 hours; after that, or
// when the statement returned no rows, Reopen fails and nothing is added.
// The Run is not recorded in History again.
func (m *Manager) Reopen(ctx context.Context, e api.HistoryEntry, tag string) (*Run, error) {
	jobs, err := m.o.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	page, err := fullPage(ctx, jobs, e.Job, 0, 0)
	if err != nil {
		qe := bq.QueryErrorOf(err)
		if qe.Kind == "not_found" {
			return nil, fmt.Errorf("the result of this run is no longer available (BigQuery keeps it for about 24 hours): %s", qe.Message)
		}
		return nil, errors.New(qe.Message)
	}
	if len(page.Schema) == 0 {
		return nil, ErrNoResult
	}
	ref := e.Job
	stats := &api.RunStats{CacheHit: e.CacheHit, StatementType: e.StatementType, ReservationID: e.ReservationID, Edition: e.Edition, Destination: e.Destination}
	if e.BytesProcessed != nil {
		stats.BytesProcessed = *e.BytesProcessed
	}
	if e.BytesBilled != nil {
		stats.BytesBilled = *e.BytesBilled
	}
	if e.SlotMs != nil {
		stats.SlotMs = *e.SlotMs
	}
	rctx, cancel := context.WithCancel(context.Background())
	cancel() // nothing runs: the Run is finished from the start
	done := make(chan struct{})
	close(done)
	m.mu.Lock()
	m.seq++
	r := &Run{m: m, id: m.seq, ctx: rctx, stop: cancel, confirm: make(chan struct{}), done: done, changed: make(chan struct{}),
		jobs: jobs, schema: page.Schema, pages: map[int64]*bq.ResultPage{0: &page}, pageUse: []int64{0}, final: true, version: 1}
	r.info = api.RunInfo{ID: r.id, Tag: tag, SQL: e.SQL, Params: e.Params, State: api.RunDone, Source: e.Source, Favorite: e.FavoritePath,
		FromHistory: e.ID, ExecutionProject: e.ExecutionProject, Location: ref.Location, Job: &ref, Stats: stats,
		HasResult: true, TotalRows: page.TotalRows, Columns: format.Columns(page.Schema), Created: e.Created, Started: e.Started, Ended: e.Ended}
	m.runs = append(m.runs, r)
	m.mu.Unlock()
	m.trimPages()
	m.publish(r.Info())
	return r, nil
}

// ReadResult reads up to n rows from start of a finished job's result
// without a Run: the agent surface pages a result by its job, possibly in
// another process than the one that ran it. A job BigQuery no longer has (it
// keeps results about 24 hours) is reported as such.
func (m *Manager) ReadResult(ctx context.Context, ref api.JobRef, start int64, n int) (bq.ResultPage, error) {
	jobs, err := m.o.Jobs(ctx)
	if err != nil {
		return bq.ResultPage{}, err
	}
	if start < 0 {
		start = 0
	}
	page, err := readRows(ctx, jobs, ref, start, n, 0)
	if err != nil {
		if qe := bq.QueryErrorOf(err); qe.Kind == "not_found" {
			return page, fmt.Errorf("%w: %s", ErrGone, qe.Message)
		}
		return page, err
	}
	if len(page.Schema) == 0 {
		return page, ErrNoResult
	}
	return page, nil
}

// ErrGone is returned for a result BigQuery no longer keeps.
var ErrGone = errors.New("the result is no longer available (BigQuery keeps it for about 24 hours)")
