// Package runs executes queries. A Run snapshots its SQL and parameters, does
// a dry-run first, asks for confirmation through its state (never through a
// prompt), waits for a free slot, runs the job with the byte cap set, and then
// serves the result page by page. Every frontend goes through here, so the
// cap, the labels and the History entry come from one code path.
package runs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq"
	"github.com/davidsevcik/bigquery-tui/pkg/config"
	"github.com/davidsevcik/bigquery-tui/pkg/format"
	"github.com/davidsevcik/bigquery-tui/pkg/policy"
)

const (
	OnDemandUSDPerTiB = 6.25
	AutoConfirmBytes  = policy.AutoConfirmBytes
	PageRows          = 500
	PageCache         = 10
	PagedRuns         = 12
)

type Options struct {
	Jobs          func(ctx context.Context, exec string) (bq.Jobs, error)
	Config        func() *config.Config
	Execution     func() string
	Now           func() time.Time
	Source        api.Source
	Record        func(api.RunInfo)
	Log           *slog.Logger
	RetryDelay    time.Duration
	SubmitTimeout time.Duration
}

type Request struct {
	SQL        string
	Params     []api.QueryParam
	Tag        string
	DryRunOnly bool
	Favorite   string
	Policy     *policy.Profile
	Execution  string
}

type Manager struct {
	o Options

	mu      sync.Mutex
	seq     int64
	runs    []*Run
	running int
	waiting []chan struct{}
	pricing map[string]api.PricingModel
	events  chan api.RunEvent
	busy    int
	closed  bool
}

func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Source == "" {
		o.Source = api.SourceTUI
	}
	if o.Execution == nil {
		o.Execution = func() string { return "" }
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = time.Second
	}
	if o.SubmitTimeout <= 0 {
		o.SubmitTimeout = 2 * time.Minute
	}
	return &Manager{o: o, events: make(chan api.RunEvent, 256)}
}

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

// ADR-0009
func (m *Manager) orShared(exec string) string {
	if exec == "" {
		return m.o.Execution()
	}
	return exec
}

func validExecution(exec string) error {
	if exec != "" && !config.ValidProjectID(exec) {
		return fmt.Errorf("%q is not a valid project ID", exec)
	}
	return nil
}

func (m *Manager) Pricing() api.PricingModel { return m.pricingIn("") }

func (m *Manager) pricingIn(exec string) api.PricingModel {
	exec = m.orShared(exec)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pricing[exec]
}

func (m *Manager) learn(exec string, dr bq.DryRunResult, force bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dr.ReservationID == "" && dr.BytesProcessed == 0 && !force {
		return
	}
	if m.pricing == nil {
		m.pricing = map[string]api.PricingModel{}
	}
	m.pricing[exec] = api.PricingModel{Known: true, Capacity: dr.ReservationID != "", ReservationID: dr.ReservationID, Edition: dr.Edition}
}

func (m *Manager) ForgetPricing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pricing = nil
}

// ADR-0009
const pricingProbe = "SELECT x FROM UNNEST(GENERATE_ARRAY(1, 2)) AS x ORDER BY x"

func (m *Manager) LearnPricing(ctx context.Context) error { return m.LearnPricingIn(ctx, "") }

func (m *Manager) LearnPricingIn(ctx context.Context, exec string) error {
	if err := validExecution(exec); err != nil {
		return err
	}
	cfg := m.o.Config()
	if cfg.Location == "" {
		return nil
	}
	exec = m.orShared(exec)
	jobs, err := m.o.Jobs(ctx, exec)
	if err != nil {
		return err
	}
	dr, err := jobs.DryRun(ctx, bq.QueryRequest{SQL: pricingProbe, Location: cfg.Location, Labels: m.labels()})
	if err != nil {
		return err
	}
	m.learn(exec, dr, true)
	return nil
}

func (m *Manager) labels() map[string]string {
	l := map[string]string{"bqt": "1"}
	if m.o.Source != api.SourceTUI {
		l["bqt_source"] = string(m.o.Source)
	}
	return l
}

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

func (m *Manager) Active() int {
	n := 0
	for _, r := range m.List() {
		if !r.State.Finished() {
			n++
		}
	}
	return n
}

func (m *Manager) CancelAll() {
	m.mu.Lock()
	runs := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	for _, r := range runs {
		r.Cancel()
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.CancelAll()
	deadline := time.Now().Add(15 * time.Second)
	m.mu.Lock()
	all := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	for _, r := range all {
		select {
		case <-r.done:
		case <-time.After(time.Until(deadline)):
		}
	}
	for {
		m.mu.Lock()
		busy := m.busy
		m.mu.Unlock()
		if busy == 0 {
			return
		}
		if time.Now().After(deadline) {
			m.o.Log.Warn("gave up waiting for job submissions and cancellations", "in_flight", busy)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (m *Manager) busyAdd(d int) {
	m.mu.Lock()
	m.busy += d
	m.mu.Unlock()
}

//nolint:contextcheck // the cancel must outlive the caller, whose context is usually what was just cancelled
func (m *Manager) cancelJob(jobs bq.Jobs, ref api.JobRef, onFail func(error)) {
	m.busyAdd(1)
	go func() {
		defer m.busyAdd(-1)
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(m.o.RetryDelay * time.Duration(attempt))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = jobs.CancelJob(ctx, ref)
			cancel()
			if err == nil || !bq.Retryable(err) {
				break
			}
		}
		if err != nil {
			m.o.Log.Warn("cancel job", "job", ref.ID, "err", err)
			if onFail != nil {
				onFail(err)
			}
		}
	}()
}

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

func (m *Manager) Start(ctx context.Context, req Request) *Run {
	cfg := m.o.Config()
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var pol *policy.Profile
	if req.Policy != nil {
		p := *req.Policy
		pol = &p
		if p.Timeout > 0 {
			cancel()
			rctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), p.Timeout)
		}
	}
	exec := m.orShared(req.Execution)
	m.mu.Lock()
	m.seq++
	r := &Run{
		m: m, id: m.seq, exec: exec, ctx: rctx, stop: cancel, policy: pol, confirm: make(chan struct{}), done: make(chan struct{}),
		changed: make(chan struct{}), pages: map[int64]*bq.ResultPage{},
	}
	r.info = api.RunInfo{
		ID: r.id, Tag: req.Tag, SQL: req.SQL, Params: append([]api.QueryParam(nil), req.Params...),
		State: api.RunEstimating, DryRunOnly: req.DryRunOnly, Source: m.o.Source, Favorite: req.Favorite,
		ExecutionProject: exec, Location: cfg.Location, Created: m.o.Now(),
	}
	r.version, r.first = 1, r.info
	closed := m.closed
	m.runs = append(m.runs, r)
	m.mu.Unlock()
	if closed {
		r.finish(r.markCancelled)
		return r
	}
	if err := validExecution(req.Execution); err != nil {
		r.fail(err)
		return r
	}
	m.publish(r.Info())
	go r.run(cfg) //nolint:contextcheck // a run outlives the call that starts it; Cancel stops it
	return r
}

type Run struct {
	m       *Manager
	id      int64
	exec    string
	policy  *policy.Profile
	ctx     context.Context
	stop    context.CancelFunc
	confirm chan struct{}
	done    chan struct{}

	mu        sync.Mutex
	info      api.RunInfo
	first     api.RunInfo
	version   int64
	changed   chan struct{}
	final     bool
	confirmed bool
	jobs      bq.Jobs
	schema    []bq.Field
	pages     map[int64]*bq.ResultPage
	pageUse   []int64
}

func (r *Run) ID() int64 { return r.id }

func (r *Run) Info() api.RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

func (r *Run) Wait() api.RunInfo {
	<-r.done
	return r.Info()
}

func (r *Run) Done() <-chan struct{} { return r.done }

func (r *Run) bump() {
	r.version++
	close(r.changed)
	r.changed = make(chan struct{})
}

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

func (r *Run) finish(fn func(*api.RunInfo)) {
	now := r.m.o.Now()
	r.mu.Lock()
	info := r.info
	fn(&info)
	info.Ended = &now
	info.Confirmation = nil
	r.mu.Unlock()
	// ADR-0009
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

func (r *Run) Confirm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.info.State == api.RunNeedsOK && !r.confirmed {
		r.confirmed = true
		close(r.confirm)
	}
}

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
		r.m.cancelJob(jobs, *job, func(error) {
			r.mu.Lock()
			if r.final {
				r.mu.Unlock()
				return
			}
			r.info.CancelRequested = false
			r.info.CancelFailed = true
			r.bump()
			info := r.info
			r.mu.Unlock()
			r.m.publish(info)
		})
		return
	}
	r.mu.Unlock()
	r.stop()
}

func (r *Run) markCancelled(i *api.RunInfo) {
	if r.policy != nil && errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		i.State, i.Error = api.RunFailed, &api.QueryError{
			Kind:    "timeout",
			Message: fmt.Sprintf("the query took longer than %s (agent_timeout) and was cancelled", r.policy.Timeout),
		}
		return
	}
	i.State = api.RunCancelled
}

func (r *Run) fail(err error) {
	qe := bq.QueryErrorOf(err)
	if r.ctx.Err() != nil || qe.Kind == "cancelled" {
		r.finish(r.markCancelled)
		return
	}
	r.finish(func(i *api.RunInfo) { i.State, i.Error = api.RunFailed, qe })
}

func (r *Run) run(cfg *config.Config) {
	m := r.m
	jobs, err := m.o.Jobs(r.ctx, r.exec)
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
	m.learn(r.exec, dr, false)
	est := &api.Estimate{
		Bytes: dr.BytesProcessed, Accuracy: dr.Accuracy, StatementType: dr.StatementType,
		ReservationID: dr.ReservationID, Edition: dr.Edition, Capacity: dr.ReservationID != "", Columns: format.Columns(dr.Schema),
	}
	if !est.Capacity {
		usd := float64(dr.BytesProcessed) / float64(1<<40) * OnDemandUSDPerTiB
		est.ApproxUSD = &usd
	}
	var refusal *api.Refusal
	if r.policy != nil {
		refusal = r.policy.Check(cfg, *est)
	}
	if dryOnly {
		r.finish(func(i *api.RunInfo) { i.State, i.Estimate, i.Refusal = api.RunEstimated, est, refusal })
		return
	}
	if refusal != nil {
		r.finish(func(i *api.RunInfo) { i.State, i.Estimate, i.Refusal = api.RunRefused, est, refusal })
		return
	}

	conf := confirmation(cfg, est)
	if r.policy != nil {
		conf = nil
	}
	if conf != nil {
		r.set(func(i *api.RunInfo) { i.State, i.Estimate, i.Confirmation = api.RunNeedsOK, est, conf })
		select {
		case <-r.confirm:
		case <-r.ctx.Done():
			r.finish(r.markCancelled)
			return
		}
	}
	r.set(func(i *api.RunInfo) { i.State, i.Estimate, i.Confirmation = api.RunQueued, est, nil })
	if !m.acquire(r.ctx) {
		r.finish(r.markCancelled)
		return
	}
	defer m.release()

	if dr.Location != "" && req.Location == "" {
		req.Location = dr.Location
	}
	if r.ctx.Err() != nil {
		r.finish(r.markCancelled)
		return
	}
	req.JobID = newJobID()
	m.busyAdd(1)
	dl, runDeadline := r.ctx.Deadline()
	if !runDeadline {
		dl = time.Now().Add(m.o.SubmitTimeout)
	}
	sctx, scancel := context.WithDeadline(context.WithoutCancel(r.ctx), dl)
	ref, err := jobs.StartQuery(sctx, req)
	expired := errors.Is(sctx.Err(), context.DeadlineExceeded)
	scancel()
	if expired && runDeadline {
		<-r.ctx.Done()
	}
	if err != nil {
		if expired {
			m.cancelJob(jobs, api.JobRef{Project: r.exec, Location: req.Location, ID: req.JobID}, nil)
		}
		m.busyAdd(-1)
		if expired && r.ctx.Err() == nil {
			qe := bq.QueryErrorOf(err)
			qe.Message = "submitting the job took too long (a cancel was requested): " + qe.Message
			r.finish(func(i *api.RunInfo) { i.State, i.Error = api.RunFailed, qe })
			return
		}
		r.fail(err)
		return
	}
	started := m.o.Now()
	if r.ctx.Err() != nil {
		m.cancelJob(jobs, ref, nil)
		m.busyAdd(-1)
		r.finish(func(i *api.RunInfo) {
			r.markCancelled(i)
			i.Job, i.Started, i.Location = &ref, &started, ref.Location
		})
		return
	}
	r.set(func(i *api.RunInfo) {
		i.State, i.Job, i.Started, i.Location = api.RunRunning, &ref, &started, ref.Location
	})
	m.busyAdd(-1)

	var res bq.JobResult
	for attempt := 0; ; attempt++ {
		res, err = jobs.WaitJob(r.ctx, ref)
		if err == nil {
			break
		}
		if r.ctx.Err() == nil && bq.Retryable(err) && attempt < 3 {
			select {
			case <-time.After(m.o.RetryDelay * time.Duration(attempt+1)):
				continue
			case <-r.ctx.Done():
			}
		}
		m.cancelJob(jobs, ref, nil)
		if r.ctx.Err() == nil {
			qe := bq.QueryErrorOf(err)
			qe.Message = "lost track of the job (a cancel was requested): " + qe.Message
			r.finish(func(i *api.RunInfo) { i.State, i.Error = api.RunFailed, qe })
			return
		}
		r.fail(err)
		return
	}
	stats := &api.RunStats{
		BytesProcessed: res.BytesProcessed, BytesBilled: res.BytesBilled, SlotMs: res.SlotMs, CacheHit: res.CacheHit,
		AffectedRows: res.AffectedRows, StatementType: res.StatementType, ReservationID: res.ReservationID, Edition: res.Edition, Destination: res.Destination,
	}
	if stats.StatementType == "" {
		stats.StatementType = dr.StatementType
	}
	if res.Err != nil {
		qe := bq.QueryErrorOf(res.Err)
		state := api.RunFailed
		if qe.Kind == "cancelled" {
			state, qe = api.RunCancelled, nil
		}
		r.finish(func(i *api.RunInfo) {
			i.State, i.Error, i.Stats, i.CancelRequested = state, qe, stats, false
			if state == api.RunCancelled {
				r.markCancelled(i)
			}
		})
		return
	}

	page, perr := fullPage(r.ctx, jobs, ref, 0, 0)
	r.finish(func(i *api.RunInfo) {
		i.State, i.Stats, i.CancelRequested = api.RunDone, stats, false
		switch {
		case perr != nil && r.policy != nil && r.ctx.Err() != nil:
			r.markCancelled(i)
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

func confirmation(cfg *config.Config, est *api.Estimate) *api.Confirmation {
	c := &api.Confirmation{Bytes: est.Bytes, StatementType: est.StatementType}
	if est.StatementType != "SELECT" {
		c.Reasons = append(c.Reasons, "statement")
	}
	threshold := policy.ConfirmThreshold(cfg, est.Capacity)
	if threshold > 0 && est.Bytes > threshold {
		c.Reasons = append(c.Reasons, "bytes")
		c.Threshold = threshold
	}
	if len(c.Reasons) == 0 {
		return nil
	}
	return c
}

func newJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("bqt_%x", time.Now().UnixNano())
	}
	return "bqt_" + hex.EncodeToString(b[:])
}
