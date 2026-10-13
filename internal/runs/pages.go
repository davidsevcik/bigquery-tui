package runs

import (
	"context"
	"errors"

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
	var page bq.ResultPage
	for len(page.Rows) < PageRows {
		at := start + int64(len(page.Rows))
		if total > 0 && at >= total {
			break
		}
		p, err := jobs.QueryRows(ctx, ref, at, PageRows-len(page.Rows))
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
