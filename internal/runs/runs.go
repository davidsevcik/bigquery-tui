// Package runs executes queries. A Run snapshots its SQL and parameters, does
// a dry-run first, asks for confirmation through its state (never through a
// prompt), waits for a free slot, runs the job with the byte cap set, and then
// serves the result page by page. Every frontend goes through here, so the
// cap, the labels and the History entry come from one code path.
package runs

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

const (
	// OnDemandUSDPerTiB is the on-demand list price used for the estimate.
	OnDemandUSDPerTiB = 6.25
	// AutoConfirmBytes is the confirm_bytes default for on-demand Runs.
	AutoConfirmBytes int64 = 10 << 30
	// PageRows is the size of a result page; PageCache is how many are kept.
	PageRows  = 500
	PageCache = 10
	// PagedRuns is how many Runs keep their pages in memory.
	PagedRuns = 12
)

// Options configures a Manager.
type Options struct {
	// Jobs builds the BigQuery jobs client lazily.
	Jobs   func(ctx context.Context) (bq.Jobs, error)
	Config func() *config.Config
	Now    func() time.Time
	// Source is recorded on History entries and as a job label: tui, cli, mcp.
	Source string
	// Record stores a finished real Run (History). It may be nil.
	Record func(api.RunInfo)
	Log    *slog.Logger
}

// Request starts a Run.
type Request struct {
	SQL    string
	Params []api.QueryParam
	// Tag is opaque: a frontend uses it to tie the Run to a buffer.
	Tag string
	// DryRunOnly stops after the estimate.
	DryRunOnly bool
}

// Manager owns the Runs of one process.
type Manager struct {
	o Options

	mu      sync.Mutex
	seq     int64
	runs    []*Run
	running int
	waiting []chan struct{}
	pricing api.PricingModel
	// forceLearn makes learn accept a zero-byte dry-run (LearnPricing's probe,
	// which is built so that it needs slots).
	forceLearn bool
	events     chan api.RunEvent
	closed  bool
}

// New returns a Manager.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Source == "" {
		o.Source = "tui"
	}
	return &Manager{o: o, events: make(chan api.RunEvent, 256)}
}

// Events streams every Run's state changes. When the reader falls behind,
// the oldest events are dropped, so the newest state always arrives.
func (m *Manager) Events() <-chan api.RunEvent { return m.events }

func (m *Manager) publish(info api.RunInfo) {
	ev := api.RunEvent{Run: info}
	for {
		select {
		case m.events <- ev:
			return
		default:
			select {
			case <-m.events:
			default:
			}
		}
	}
}

// Pricing reports what the dry-runs so far revealed about billing.
func (m *Manager) Pricing() api.PricingModel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pricing
}

// learn records the pricing model a dry-run revealed. A query that scans
// nothing and names no reservation (SELECT 1) says nothing either way:
// BigQuery reports no reservation for it even on a reservation-backed project.
func (m *Manager) learn(dr bq.DryRunResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dr.ReservationID == "" && dr.BytesProcessed == 0 && !m.forceLearn {
		return
	}
	m.pricing = api.PricingModel{Known: true, Capacity: dr.ReservationID != "", ReservationID: dr.ReservationID, Edition: dr.Edition}
}

// ForgetPricing drops the learned model (the Execution project changed).
func (m *Manager) ForgetPricing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pricing = api.PricingModel{}
}

// pricingProbe is a free query that still needs slots, so its dry-run names
// the reservation when there is one (a bare SELECT 1 does not).
const pricingProbe = "SELECT x FROM UNNEST(GENERATE_ARRAY(1, 2)) AS x ORDER BY x"

// LearnPricing runs a free dry-run in the configured location so the first
// Run is presented correctly. Without a location it does nothing:
// the model depends on where the job runs.
func (m *Manager) LearnPricing(ctx context.Context) error {
	cfg := m.o.Config()
	if cfg.Location == "" {
		return nil
	}
	jobs, err := m.o.Jobs(ctx)
	if err != nil {
		return err
	}
	dr, err := jobs.DryRun(ctx, bq.QueryRequest{SQL: pricingProbe, Location: cfg.Location, Labels: m.labels()})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.forceLearn = true
	m.mu.Unlock()
	m.learn(dr)
	m.mu.Lock()
	m.forceLearn = false
	m.mu.Unlock()
	return nil
}

func (m *Manager) labels() map[string]string {
	l := map[string]string{"bqt": "1"}
	if m.o.Source != "tui" {
		l["bqt_source"] = m.o.Source
	}
	return l
}

// List returns every Run of this process, newest first.
func (m *Manager) List() []api.RunInfo {
	m.mu.Lock()
	runs := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	out := make([]api.RunInfo, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Info())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Get returns a Run by ID.
func (m *Manager) Get(id int64) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.id == id {
			return r
		}
	}
	return nil
}

// Active counts the Runs that have not finished.
func (m *Manager) Active() int {
	n := 0
	for _, r := range m.List() {
		if !r.State.Finished() {
			n++
		}
	}
	return n
}

// CancelAll cancels every unfinished Run.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	runs := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	for _, r := range runs {
		r.Cancel()
	}
}

// Close cancels everything and stops accepting Runs.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.CancelAll()
}

// trimPages drops the cached result pages of all but the newest
// PagedRuns Runs. Their rows stay readable: a page is fetched again on demand.
func (m *Manager) trimPages() {
	m.mu.Lock()
	runs := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	kept := 0
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		r.mu.Lock()
		if len(r.pages) > 0 {
			if kept++; kept > PagedRuns {
				r.pages, r.pageUse = map[int64]*bq.ResultPage{}, nil
			}
		}
		r.mu.Unlock()
	}
}

// acquire waits for a free slot (max_concurrent_queries, read live).
func (m *Manager) acquire(ctx context.Context) bool {
	for {
		m.mu.Lock()
		limit := m.o.Config().MaxConcurrentQueries
		if limit <= 0 {
			limit = config.DefaultMaxConcurrent
		}
		if m.running < limit {
			m.running++
			m.mu.Unlock()
			return true
		}
		ch := make(chan struct{})
		m.waiting = append(m.waiting, ch)
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			m.mu.Lock()
			for i, w := range m.waiting {
				if w == ch {
					m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
					break
				}
			}
			m.mu.Unlock()
			return false
		}
	}
}

func (m *Manager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	if len(m.waiting) > 0 {
		close(m.waiting[0])
		m.waiting = m.waiting[1:]
	}
}

// Start begins a Run and returns at once; follow it through Events or Wait.
func (m *Manager) Start(ctx context.Context, req Request) *Run {
	cfg := m.o.Config()
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.mu.Lock()
	m.seq++
	r := &Run{m: m, id: m.seq, ctx: rctx, stop: cancel, confirm: make(chan struct{}), done: make(chan struct{}),
		changed: make(chan struct{}), pages: map[int64]*bq.ResultPage{}}
	r.info = api.RunInfo{ID: r.id, Tag: req.Tag, SQL: req.SQL, Params: append([]api.QueryParam(nil), req.Params...),
		State: api.RunEstimating, DryRunOnly: req.DryRunOnly, Source: m.o.Source,
		ExecutionProject: cfg.ExecutionProject, Location: cfg.Location, Created: m.o.Now()}
	r.version = 1
	closed := m.closed
	m.runs = append(m.runs, r)
	m.mu.Unlock()
	if closed {
		r.finish(func(i *api.RunInfo) { i.State = api.RunCancelled })
		return r
	}
	m.publish(r.Info())
	go r.run(cfg)
	return r
}

// Run is one execution of a SQL snapshot.
type Run struct {
	m       *Manager
	id      int64
	ctx     context.Context
	stop    context.CancelFunc
	confirm chan struct{}
	done    chan struct{}

	mu        sync.Mutex
	info      api.RunInfo
	version   int64
	changed   chan struct{} // closed and replaced on every change
	final     bool          // the end state is published (and recorded)
	confirmed bool
	jobs      bq.Jobs
	schema    []bq.Field
	pages     map[int64]*bq.ResultPage
	pageUse   []int64
}

// ID returns the Run's ID.
func (r *Run) ID() int64 { return r.id }

// Info returns a snapshot.
func (r *Run) Info() api.RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// Wait blocks until the Run has finished.
func (r *Run) Wait() api.RunInfo {
	<-r.done
	return r.Info()
}

// Done is closed when the Run has finished.
func (r *Run) Done() <-chan struct{} { return r.done }

// bump marks a change; the caller holds r.mu.
func (r *Run) bump() {
	r.version++
	close(r.changed)
	r.changed = make(chan struct{})
}

// Await blocks until the Run has changed since version after (0 returns the
// current state at once), and returns the snapshot with its version. It also
// returns when ctx ends. Calling it in a loop follows the Run without missing
// its final state.
func (r *Run) Await(ctx context.Context, after int64) (api.RunInfo, int64) {
	for {
		r.mu.Lock()
		info, v, ch, final := r.info, r.version, r.changed, r.final
		r.mu.Unlock()
		if v > after || final {
			return info, v
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return info, v
		}
	}
}

func (r *Run) set(fn func(*api.RunInfo)) {
	r.mu.Lock()
	fn(&r.info)
	r.bump()
	info := r.info
	r.mu.Unlock()
	r.m.publish(info)
}

// finish publishes the end state. The History entry is written first, so
// whoever sees a finished Run can rely on the entry being there.
func (r *Run) finish(fn func(*api.RunInfo)) {
	now := r.m.o.Now()
	r.mu.Lock()
	info := r.info
	fn(&info)
	info.Ended = &now
	info.Confirmation = nil
	r.mu.Unlock()
	if info.Job != nil && r.m.o.Record != nil {
		r.m.o.Record(info)
	}
	r.mu.Lock()
	r.info = info
	r.final = true
	r.bump()
	r.mu.Unlock()
	r.stop()
	r.m.trimPages()
	r.m.publish(info)
	close(r.done)
}

// Confirm lets a Run in the needs_confirmation state proceed.
func (r *Run) Confirm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.info.State == api.RunNeedsOK && !r.confirmed {
		r.confirmed = true
		close(r.confirm)
	}
}

// Cancel stops the Run. A job that is already running is asked to cancel and
// the Run stays "cancel requested" until BigQuery confirms.
func (r *Run) Cancel() {
	r.mu.Lock()
	state, job, jobs := r.info.State, r.info.Job, r.jobs
	if state.Finished() {
		r.mu.Unlock()
		return
	}
	if state == api.RunRunning && job != nil && jobs != nil {
		already := r.info.CancelRequested
		r.info.CancelRequested = true
		if !already {
			r.bump()
		}
		info := r.info
		r.mu.Unlock()
		if already {
			return
		}
		r.m.publish(info)
		ref := *job
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := jobs.CancelJob(ctx, ref); err != nil {
				r.m.o.Log.Warn("cancel job", "job", ref.ID, "err", err)
			}
		}()
		return
	}
	r.mu.Unlock()
	r.stop()
}

func (r *Run) fail(err error) {
	qe := bq.QueryErrorOf(err)
	if r.ctx.Err() != nil || qe.Kind == "cancelled" {
		r.finish(func(i *api.RunInfo) { i.State = api.RunCancelled })
		return
	}
	r.finish(func(i *api.RunInfo) { i.State, i.Error = api.RunFailed, qe })
}

func (r *Run) run(cfg *config.Config) {
	m := r.m
	jobs, err := m.o.Jobs(r.ctx)
	if err != nil {
		r.fail(err)
		return
	}
	r.mu.Lock()
	r.jobs = jobs
	sql, params := r.info.SQL, r.info.Params
	dryOnly := r.info.DryRunOnly
	r.mu.Unlock()

	req := bq.QueryRequest{SQL: sql, Params: params, Location: cfg.Location, Labels: m.labels()}
	if cfg.MaxBytesBilled.Mode == config.ByteLimitValue {
		req.MaxBytesBilled = cfg.MaxBytesBilled.Bytes
	}
	dr, err := jobs.DryRun(r.ctx, req)
	if err != nil {
		r.fail(err)
		return
	}
	m.learn(dr)
	est := &api.Estimate{Bytes: dr.BytesProcessed, Accuracy: dr.Accuracy, StatementType: dr.StatementType,
		ReservationID: dr.ReservationID, Edition: dr.Edition, Capacity: dr.ReservationID != "", Columns: format.Columns(dr.Schema)}
	if !est.Capacity {
		usd := float64(dr.BytesProcessed) / float64(1<<40) * OnDemandUSDPerTiB
		est.ApproxUSD = &usd
	}
	if dryOnly {
		r.finish(func(i *api.RunInfo) { i.State, i.Estimate = api.RunEstimated, est })
		return
	}

	conf := confirmation(cfg, est)
	if conf != nil {
		r.set(func(i *api.RunInfo) { i.State, i.Estimate, i.Confirmation = api.RunNeedsOK, est, conf })
		select {
		case <-r.confirm:
		case <-r.ctx.Done():
			r.finish(func(i *api.RunInfo) { i.State = api.RunCancelled })
			return
		}
	}
	r.set(func(i *api.RunInfo) { i.State, i.Estimate, i.Confirmation = api.RunQueued, est, nil })
	if !m.acquire(r.ctx) {
		r.finish(func(i *api.RunInfo) { i.State = api.RunCancelled })
		return
	}
	defer m.release()

	if dr.Location != "" && req.Location == "" {
		// run where the dry-run resolved the query, so results are read there
		req.Location = dr.Location
	}
	ref, err := jobs.StartQuery(r.ctx, req)
	if err != nil {
		r.fail(err)
		return
	}
	started := m.o.Now()
	r.set(func(i *api.RunInfo) {
		i.State, i.Job, i.Started, i.Location = api.RunRunning, &ref, &started, ref.Location
	})

	// The job outlives a cancelled context: ask BigQuery to stop it.
	res, err := jobs.WaitJob(r.ctx, ref)
	if err != nil {
		if r.ctx.Err() != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = jobs.CancelJob(ctx, ref)
			}()
		}
		r.fail(err)
		return
	}
	stats := &api.RunStats{BytesProcessed: res.BytesProcessed, BytesBilled: res.BytesBilled, SlotMs: res.SlotMs, CacheHit: res.CacheHit,
		AffectedRows: res.AffectedRows, StatementType: res.StatementType, ReservationID: res.ReservationID, Edition: res.Edition, Destination: res.Destination}
	if stats.StatementType == "" {
		stats.StatementType = dr.StatementType
	}
	if res.Err != nil {
		qe := bq.QueryErrorOf(res.Err)
		state := api.RunFailed
		if qe.Kind == "cancelled" {
			state, qe = api.RunCancelled, nil
		}
		r.finish(func(i *api.RunInfo) { i.State, i.Error, i.Stats, i.CancelRequested = state, qe, stats, false })
		return
	}

	// The first page tells whether the statement produced rows at all.
	page, perr := jobs.QueryRows(r.ctx, ref, 0, PageRows)
	r.finish(func(i *api.RunInfo) {
		i.State, i.Stats, i.CancelRequested = api.RunDone, stats, false
		switch {
		case perr != nil:
			i.ResultError = bq.QueryErrorOf(perr).Message
		case len(page.Schema) > 0:
			i.HasResult, i.TotalRows, i.Columns = true, page.TotalRows, format.Columns(page.Schema)
			r.schema = page.Schema
			r.pages[0] = &page
			r.pageUse = []int64{0}
		}
	})
}

// confirmation decides whether the Run must wait for the user.
func confirmation(cfg *config.Config, est *api.Estimate) *api.Confirmation {
	c := &api.Confirmation{Bytes: est.Bytes, StatementType: est.StatementType}
	if est.StatementType != "SELECT" {
		c.Reasons = append(c.Reasons, "statement")
	}
	var threshold int64
	switch cfg.ConfirmBytes.Mode {
	case config.ByteLimitValue:
		threshold = cfg.ConfirmBytes.Bytes
	case config.ByteLimitAuto:
		if !est.Capacity {
			threshold = AutoConfirmBytes
		}
	}
	if threshold > 0 && est.Bytes > threshold {
		c.Reasons = append(c.Reasons, "bytes")
		c.Threshold = threshold
	}
	if len(c.Reasons) == 0 {
		return nil
	}
	return c
}
