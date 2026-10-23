package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/export"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/handoff"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// ErrUnknownRun is returned for a Run ID this process does not know.
var ErrUnknownRun = runs.ErrUnknownRun

// FollowRuns returns a new Follower of this engine's Runs, for one frontend.
func (e *Engine) FollowRuns() *runs.Follower { return e.runs.Follow() }

// StartRun begins a Run (dry-run, confirmation, job) and returns at once.
func (e *Engine) StartRun(ctx context.Context, req runs.Request) *runs.Run {
	return e.runs.Start(ctx, req)
}

// RunRows returns formatted result rows of a finished Run.
func (e *Engine) RunRows(ctx context.Context, id int64, startIndex int64, n int, o format.Options) (api.PreviewPage, error) {
	r := e.runs.Get(id)
	if r == nil {
		return api.PreviewPage{}, ErrUnknownRun
	}
	return r.Rows(ctx, startIndex, n, o)
}

// LearnPricing pre-learns the pricing model with a free dry-run (spec 10.2).
func (e *Engine) LearnPricing(ctx context.Context) error { return e.runs.LearnPricing(ctx) }

func (e *Engine) recordRun(info api.RunInfo) {
	h, err := e.History()
	if err != nil {
		e.o.Log.Warn("open history to record a run", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.InsertRun(ctx, info); err != nil {
		e.o.Log.Warn("record run in history", "err", err)
	}
}

// Tokenize splits SQL into highlight spans.
func (e *Engine) Tokenize(text string) []api.Token { return sqlx.Tokenize(text) }

// Complete returns completion candidates for a cursor position (byte offset).
func (e *Engine) Complete(text string, cursor int, forced bool) []api.Candidate {
	return sqlx.Complete(text, cursor, completionCatalog{e}, sqlx.Options{Forced: forced})
}

// completionCatalog adapts the in-memory index and the catalog to sqlx.Catalog.
type completionCatalog struct{ e *Engine }

func (c completionCatalog) DefaultProject() string { return c.e.execProject() }

// Projects offers the Selected projects that hold data (completion follows
// the Explorer: deselected projects are not offered).
func (c completionCatalog) Projects() []string {
	sel := c.e.Selection()
	var out []string
	for _, n := range c.e.search.Projects() {
		if sel.Has(n.Name) {
			out = append(out, n.Name)
		}
	}
	return out
}

// offered reports whether completion offers the names inside project: a
// Selected project, or the Execution project (unqualified dataset.table
// names resolve there, selected or not). The columns of a table the query
// names are offered in any cached project: the user wrote that table.
func (c completionCatalog) offered(project string) bool {
	return c.e.Selection().Has(project) || project == c.e.execProject()
}

func (c completionCatalog) Datasets(project string) []string {
	if !c.offered(project) {
		return nil
	}
	var out []string
	for _, n := range c.e.search.Datasets(project) {
		out = append(out, n.Name)
	}
	return out
}

func (c completionCatalog) Tables(project, dataset string) []sqlx.TableName {
	if !c.offered(project) {
		return nil
	}
	var out []sqlx.TableName
	for _, n := range c.e.search.Tables(project, dataset) {
		name := n.Name
		if n.Shard != nil {
			name = n.Shard.Prefix + "*" // a wildcard table over the family
		}
		out = append(out, sqlx.TableName{Name: name, Type: n.Type})
	}
	return out
}

func (c completionCatalog) Columns(project, dataset, table string) []api.ColumnMeta {
	if strings.HasSuffix(table, "*") {
		// a wildcard table has the schema of its newest shard
		prefix := strings.TrimSuffix(table, "*")
		for _, n := range c.e.search.ShardMembers(project, dataset, prefix) {
			table = n.Name
			break
		}
	}
	// Completion runs on the frontend's input path, so a table's columns are
	// read from the catalog once and then served from memory (the cache is
	// dropped whenever the index reloads), and the read itself is bounded.
	key := project + "\x00" + dataset + "\x00" + table
	c.e.colMu.Lock()
	cols, ok := c.e.colCache[key]
	c.e.colMu.Unlock()
	if ok {
		return cols
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	d, err := c.e.TableDetail(ctx, api.ObjectPath{Project: project, Dataset: dataset, Table: table})
	if err != nil {
		return nil
	}
	c.e.colMu.Lock()
	if len(c.e.colCache) > 512 {
		c.e.colCache = map[string][]api.ColumnMeta{}
	}
	if c.e.colCache == nil {
		c.e.colCache = map[string][]api.ColumnMeta{}
	}
	c.e.colCache[key] = d.Table.Columns
	c.e.colMu.Unlock()
	return d.Table.Columns
}

// ExportHandle is a running export.
type ExportHandle struct {
	cancel context.CancelFunc
	events chan api.ExportEvent
	done   chan struct{}
	mu     gosync.Mutex
	last   api.ExportEvent
}

// Events streams progress; when the reader falls behind, older events are
// dropped. The channel closes after the final event (Done set).
func (h *ExportHandle) Events() <-chan api.ExportEvent { return h.events }

// Cancel stops the export; the partial file is removed.
func (h *ExportHandle) Cancel() { h.cancel() }

// Wait blocks until the export has finished and returns the final event.
func (h *ExportHandle) Wait() api.ExportEvent {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

func (h *ExportHandle) emit(ev api.ExportEvent) {
	h.mu.Lock()
	h.last = ev
	h.mu.Unlock()
	for {
		select {
		case h.events <- ev:
			return
		default:
			select {
			case <-h.events:
			default:
			}
		}
	}
}

// DefaultExportPath is the suggested destination: bqt-<timestamp>.<ext> in
// export_dir, or in the working directory when that is unset.
func (e *Engine) DefaultExportPath(f export.Format) string {
	name := fmt.Sprintf("bqt-%s.%s", e.o.Now().Format("20060102-150405"), f)
	return filepath.Join(e.Config().ExportDir, name)
}

// StartExport writes every row of a Run's result to path, streaming through
// the result pages. The file is written under a temporary name and renamed
// when complete, so a cancelled or failed export leaves nothing behind.
func (e *Engine) StartExport(ctx context.Context, runID int64, path string, f export.Format) (*ExportHandle, error) {
	r := e.runs.Get(runID)
	if r == nil {
		return nil, ErrUnknownRun
	}
	info := r.Info()
	if !info.HasResult {
		return nil, runs.ErrNoResult
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return nil, err
	}
	ectx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &ExportHandle{cancel: cancel, events: make(chan api.ExportEvent, 16), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		defer close(h.events)
		defer cancel()
		// the schema the Run kept, so no fetch can fail (or be skipped) here
		schema := r.Schema()
		next := func(c context.Context, start int64) ([][]any, error) {
			if start >= info.TotalRows {
				return nil, nil
			}
			_, rows, err := r.RawRows(c, start)
			return rows, err
		}
		n, werr := export.Write(ectx, tmp, f, schema, next, func(rows int64) {
			h.emit(api.ExportEvent{Rows: rows, Total: info.TotalRows, Path: path})
		})
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), path)
		}
		ev := api.ExportEvent{Rows: n, Total: info.TotalRows, Path: path, Done: true}
		if werr != nil {
			os.Remove(tmp.Name())
			ev.Error = werr.Error()
			if errors.Is(werr, context.Canceled) {
				ev.Error = "cancelled"
			}
		}
		h.emit(ev)
	}()
	return h, nil
}

// RunSample renders the first handoff.SampleRows rows of a Run's result for
// a hand-off. NDJSON is written from the raw values (as an export would), so
// strings keep their line breaks and tabs; markdown and TSV come from the
// formatted cells, which they have to flatten anyway.
func (e *Engine) RunSample(ctx context.Context, id int64, f handoff.SampleFormat) (string, error) {
	r := e.runs.Get(id)
	if r == nil {
		return "", ErrUnknownRun
	}
	info := r.Info()
	if !info.HasResult {
		return "", runs.ErrNoResult
	}
	var raw [][]any
	for start := int64(0); start < info.TotalRows && len(raw) < handoff.SampleRows; {
		_, rows, err := r.RawRows(ctx, start)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			break
		}
		raw = append(raw, rows...)
		start += int64(len(rows))
	}
	if len(raw) > handoff.SampleRows {
		raw = raw[:handoff.SampleRows]
	}
	schema := r.Schema()
	if f == handoff.NDJSON {
		var b strings.Builder
		done := false
		_, err := export.Write(ctx, &b, export.NDJSON, schema, func(context.Context, int64) ([][]any, error) {
			if done {
				return nil, nil
			}
			done = true
			return raw, nil
		}, nil)
		return b.String(), err
	}
	cells := make([][]api.Cell, len(raw))
	for i, row := range raw {
		cells[i] = format.Row(schema, row, format.Options{})
	}
	return handoff.Sample(format.Columns(schema), cells, f), nil
}
