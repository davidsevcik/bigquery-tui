package runs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

type rig struct {
	t       *testing.T
	fake    *bqtest.Fake
	cfg     *config.Config
	m       *Manager
	mu      sync.Mutex
	records []api.RunInfo
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, fake: bqtest.New()}
	r.cfg = &config.Config{ExecutionProject: "exec", MaxConcurrentQueries: 2,
		ConfirmBytes:   config.ByteLimit{Mode: config.ByteLimitAuto},
		MaxBytesBilled: config.ByteLimit{Mode: config.ByteLimitValue, Bytes: config.DefaultMaxBytesBilled}}
	r.m = New(Options{
		Jobs:   func(context.Context) (bq.Jobs, error) { return r.fake, nil },
		Config: func() *config.Config { return r.cfg },
		Now:    func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Record: func(i api.RunInfo) { r.mu.Lock(); r.records = append(r.records, i); r.mu.Unlock() },
	})
	t.Cleanup(r.m.Close)
	return r
}

func (r *rig) recorded() []api.RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.RunInfo(nil), r.records...)
}

// until waits for a Run to reach a state.
func until(t *testing.T, run *Run, want api.RunState) api.RunInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if i := run.Info(); i.State == want {
			return i
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %d is %s, want %s", run.ID(), run.Info().State, want)
	return api.RunInfo{}
}

func TestRunSelectEndToEnd(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1234); s.SlotMs = 77; return s }
	run := r.m.Start(context.Background(), Request{SQL: "select 1", Tag: "buf1", Params: []api.QueryParam{{Name: "a", Type: "INT64", Value: "1"}}})
	info := run.Wait()
	if info.State != api.RunDone || info.Error != nil || !info.HasResult || info.TotalRows != 1234 || len(info.Columns) != 2 {
		t.Fatalf("%+v", info)
	}
	if info.Estimate == nil || info.Estimate.Bytes != 123400 || info.Estimate.StatementType != "SELECT" || info.Estimate.Capacity || info.Estimate.ApproxUSD == nil {
		t.Fatalf("estimate %+v", info.Estimate)
	}
	if info.Stats == nil || info.Stats.SlotMs != 77 || info.Job == nil || info.Started == nil || info.Ended == nil || info.Tag != "buf1" {
		t.Fatalf("%+v", info)
	}
	calls := r.fake.QueryCalls()
	if len(calls) != 2 || !calls[0].Dry || calls[1].Dry {
		t.Fatalf("every run does a dry-run first: %+v", calls)
	}
	for _, c := range calls {
		if c.Req.MaxBytesBilled != config.DefaultMaxBytesBilled || c.Req.Labels["bqt"] != "1" || len(c.Req.Params) != 1 {
			t.Fatalf("cap, label and parameters go on every request: %+v", c.Req)
		}
	}
	if rec := r.recorded(); len(rec) != 1 || rec[0].State != api.RunDone || rec[0].Job.ID != info.Job.ID {
		t.Fatalf("one History record per real run: %+v", rec)
	}

	// paging: across the 500-row page boundary, and a jump to the end
	page, err := run.Rows(context.Background(), 498, 4, format.Options{})
	if err != nil || len(page.Rows) != 4 || page.Rows[0][0].Text != "498" || page.Rows[3][0].Text != "501" || page.TotalRows != 1234 {
		t.Fatalf("%v %+v", err, page)
	}
	page, _ = run.Rows(context.Background(), 1230, 50, format.Options{})
	if len(page.Rows) != 4 || page.Rows[3][1].Text != "row 1233" {
		t.Fatalf("%+v", page.Rows)
	}
	if n := len(r.fake.CallsTo("QueryRows")); n != 3 {
		t.Fatalf("pages 0, 500 and 1000 are each fetched once, got %d fetches", n)
	}
}

func TestPageCacheIsBounded(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(PageRows * (PageCache + 5)) }
	run := r.m.Start(context.Background(), Request{SQL: "select"})
	run.Wait()
	for i := 0; i < PageCache+5; i++ {
		if _, err := run.Rows(context.Background(), int64(i*PageRows), 1, format.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	run.mu.Lock()
	n := len(run.pages)
	run.mu.Unlock()
	if n != PageCache {
		t.Fatalf("cache holds %d pages, want %d", n, PageCache)
	}
}

func TestDryRunOnlyIsNotRecorded(t *testing.T) {
	r := newRig(t)
	info := r.m.Start(context.Background(), Request{SQL: "select 1", DryRunOnly: true}).Wait()
	if info.State != api.RunEstimated || info.Estimate == nil || info.Job != nil {
		t.Fatalf("%+v", info)
	}
	if len(r.recorded()) != 0 || len(r.fake.CallsTo("StartQuery")) != 0 {
		t.Fatal("a dry-run starts no job and writes no History entry")
	}
}

func TestQueryErrorCarriesThePosition(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected keyword FROM at [2:8]")}
	}
	info := r.m.Start(context.Background(), Request{SQL: "select\n from x"}).Wait()
	if info.State != api.RunFailed || info.Error == nil || info.Error.Line != 2 || info.Error.Col != 8 || info.Error.Kind != "query" {
		t.Fatalf("%+v %+v", info, info.Error)
	}
	if !strings.HasPrefix(info.Error.Message, "Syntax error") {
		t.Fatalf("message keeps BigQuery's words: %q", info.Error.Message)
	}
	if len(r.recorded()) != 0 {
		t.Fatal("a failed dry-run is not a real Run")
	}

	// a position is optional
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: &bq.Error{Kind: bq.KindNotFound, Err: errNotFound}}
	}
	info = r.m.Start(context.Background(), Request{SQL: "select * from nope"}).Wait()
	if info.Error == nil || info.Error.Line != 0 || info.Error.Kind != "not_found" {
		t.Fatalf("%+v", info.Error)
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }

const errNotFound = strErr("Not found: Table exec:ds.nope was not found in location EU")

func TestJobFailureIsRecorded(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{JobErr: bqtest.QueryError("Division by zero at [1:8]")}
	}
	info := r.m.Start(context.Background(), Request{SQL: "select 1/0"}).Wait()
	if info.State != api.RunFailed || info.Error.Line != 1 || info.Job == nil {
		t.Fatalf("%+v", info)
	}
	if rec := r.recorded(); len(rec) != 1 || rec[0].State != api.RunFailed {
		t.Fatalf("failed runs are History too: %+v", rec)
	}
}

func TestOnDemandOverThresholdNeedsConfirmation(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{Bytes: 11 << 30, Schema: bqtest.Numbers(0).Schema}
	}
	run := r.m.Start(context.Background(), Request{SQL: "select big"})
	info := until(t, run, api.RunNeedsOK)
	c := info.Confirmation
	if c == nil || len(c.Reasons) != 1 || c.Reasons[0] != "bytes" || c.Threshold != AutoConfirmBytes || c.Bytes != 11<<30 {
		t.Fatalf("%+v", c)
	}
	if usd := *info.Estimate.ApproxUSD; usd < 0.06 || usd > 0.07 {
		t.Fatalf("11 GiB on demand is about $0.067, got %v", usd)
	}
	if len(r.fake.CallsTo("StartQuery")) != 0 {
		t.Fatal("nothing runs before the confirmation")
	}
	run.Confirm()
	if info := run.Wait(); info.State != api.RunDone || info.Confirmation != nil {
		t.Fatalf("%+v", info)
	}

	// declining is a cancel, and nothing ran
	run = r.m.Start(context.Background(), Request{SQL: "select big"})
	until(t, run, api.RunNeedsOK)
	run.Cancel()
	if info := run.Wait(); info.State != api.RunCancelled {
		t.Fatalf("%+v", info)
	}
	if n := len(r.fake.CallsTo("StartQuery")); n != 1 {
		t.Fatalf("%d jobs started", n)
	}
}

func TestCapacityDoesNotConfirmBytesByDefault(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{Bytes: 500 << 30, ReservationID: "admin:EU.res", Edition: "STANDARD", Schema: bqtest.Numbers(0).Schema}
	}
	info := r.m.Start(context.Background(), Request{SQL: "select big"}).Wait()
	if info.State != api.RunDone || !info.Estimate.Capacity || info.Estimate.ApproxUSD != nil || info.Estimate.Edition != "STANDARD" {
		t.Fatalf("%+v %+v", info, info.Estimate)
	}
	if p := r.m.Pricing(); !p.Known || !p.Capacity || p.ReservationID != "admin:EU.res" {
		t.Fatalf("pricing is learned from the dry-run: %+v", p)
	}
	// an explicit confirm_bytes applies on capacity too
	r.cfg.ConfirmBytes = config.ByteLimit{Mode: config.ByteLimitValue, Bytes: 1 << 30}
	run := r.m.Start(context.Background(), Request{SQL: "select big"})
	until(t, run, api.RunNeedsOK)
	run.Cancel()
}

func TestNonSelectNeedsConfirmationNamingTheType(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DROP_TABLE"} }
	run := r.m.Start(context.Background(), Request{SQL: "drop table x"})
	info := until(t, run, api.RunNeedsOK)
	if c := info.Confirmation; c == nil || c.Reasons[0] != "statement" || c.StatementType != "DROP_TABLE" {
		t.Fatalf("%+v", c)
	}
	run.Confirm()
	if info := run.Wait(); info.State != api.RunDone || info.HasResult {
		t.Fatalf("a DDL statement finishes without rows: %+v", info)
	}
}

func TestHardCapRefusalIsAnOrdinaryFailure(t *testing.T) {
	r := newRig(t)
	r.cfg.MaxBytesBilled = config.ByteLimit{Mode: config.ByteLimitValue, Bytes: 1000}
	r.cfg.ConfirmBytes = config.ByteLimit{Mode: config.ByteLimitOff}
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{Bytes: 5000} }
	info := r.m.Start(context.Background(), Request{SQL: "select"}).Wait()
	if info.State != api.RunFailed || info.Error.Kind != "over_limit" || !strings.Contains(info.Error.Message, "5000 or higher required") {
		t.Fatalf("%+v %+v", info, info.Error)
	}
	// "off" sets no cap at all
	r.cfg.MaxBytesBilled = config.ByteLimit{Mode: config.ByteLimitOff}
	if info := r.m.Start(context.Background(), Request{SQL: "select"}).Wait(); info.State != api.RunDone {
		t.Fatalf("%+v", info)
	}
	calls := r.fake.QueryCalls()
	if calls[len(calls)-1].Req.MaxBytesBilled != 0 {
		t.Fatal("max_bytes_billed = off must not set a cap")
	}
}

func TestRunsQueueBeyondTheConcurrencyLimit(t *testing.T) {
	r := newRig(t) // limit 2
	block := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	a := r.m.Start(context.Background(), Request{SQL: "a"})
	b := r.m.Start(context.Background(), Request{SQL: "b"})
	until(t, a, api.RunRunning)
	until(t, b, api.RunRunning)
	c := r.m.Start(context.Background(), Request{SQL: "c"})
	d := r.m.Start(context.Background(), Request{SQL: "d"})
	until(t, c, api.RunQueued)
	until(t, d, api.RunQueued)
	if r.m.Active() != 4 {
		t.Fatalf("active = %d", r.m.Active())
	}
	// a queued run can be cancelled without ever starting a job
	d.Cancel()
	if d.Wait().State != api.RunCancelled {
		t.Fatal("queued run was not cancelled")
	}
	close(block)
	for _, run := range []*Run{a, b, c} {
		if info := run.Wait(); info.State != api.RunDone {
			t.Fatalf("%+v", info)
		}
	}
	if n := len(r.fake.CallsTo("StartQuery")); n != 3 {
		t.Fatalf("%d jobs", n)
	}
	list := r.m.List()
	if len(list) != 4 || list[0].SQL != "d" || list[3].SQL != "a" {
		t.Fatalf("the list is newest first: %+v", list)
	}
}

func TestCancelRunningJobWaitsForBigQuery(t *testing.T) {
	r := newRig(t)
	block := make(chan struct{})
	defer close(block)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	run := r.m.Start(context.Background(), Request{SQL: "slow"})
	until(t, run, api.RunRunning)
	run.Cancel()
	info := run.Wait()
	if info.State != api.RunCancelled || info.Error != nil || info.CancelRequested {
		t.Fatalf("%+v", info)
	}
	if len(r.fake.CallsTo("CancelJob")) != 1 {
		t.Fatal("the job itself must be cancelled")
	}
	if rec := r.recorded(); len(rec) != 1 || rec[0].State != api.RunCancelled {
		t.Fatalf("a cancelled job is a real Run: %+v", rec)
	}
}

func TestEventsDeliverTheFinalState(t *testing.T) {
	r := newRig(t)
	run := r.m.Start(context.Background(), Request{SQL: "select 1"})
	run.Wait()
	var last api.RunEvent
	var states []api.RunState
	for {
		select {
		case ev := <-r.m.Events():
			last = ev
			states = append(states, ev.Run.State)
			continue
		default:
		}
		break
	}
	if last.Run.State != api.RunDone || states[0] != api.RunEstimating {
		t.Fatalf("%v", states)
	}
	// a reader that never reads must not block runs
	for i := 0; i < 300; i++ {
		r.m.Start(context.Background(), Request{SQL: "select 1", DryRunOnly: true}).Wait()
	}
}

func TestLearnPricingNeedsALocation(t *testing.T) {
	r := newRig(t)
	if err := r.m.LearnPricing(context.Background()); err != nil || r.m.Pricing().Known {
		t.Fatal("without a location nothing is learned")
	}
	r.cfg.Location = "EU"
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{ReservationID: "res"} }
	if err := r.m.LearnPricing(context.Background()); err != nil || !r.m.Pricing().Capacity {
		t.Fatalf("%v %+v", err, r.m.Pricing())
	}
	if c := r.fake.QueryCalls(); len(c) != 1 || !c[0].Dry || c[0].Req.Location != "EU" {
		t.Fatalf("%+v", c)
	}
	// a query that scans nothing and names no reservation teaches nothing
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{Bytes: 0, Schema: bqtest.Numbers(0).Schema} }
	r.m.Start(context.Background(), Request{SQL: "select 1"}).Wait()
	if !r.m.Pricing().Capacity {
		t.Fatal("SELECT 1 must not flip the model to on-demand")
	}
	// one that scans bytes without a reservation is on-demand
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(5) }
	r.m.Start(context.Background(), Request{SQL: "select"}).Wait()
	if p := r.m.Pricing(); !p.Known || p.Capacity {
		t.Fatalf("%+v", p)
	}
}

func TestAwaitFollowsARunToItsEnd(t *testing.T) {
	r := newRig(t)
	block := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	run := r.m.Start(context.Background(), Request{SQL: "slow"})
	var states []api.RunState
	var v int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var info api.RunInfo
			info, v = run.Await(context.Background(), v)
			states = append(states, info.State)
			if info.State.Finished() {
				return
			}
		}
	}()
	until(t, run, api.RunRunning)
	close(block)
	<-done
	if states[len(states)-1] != api.RunDone {
		t.Fatalf("%v", states)
	}
	// after the end it returns at once, and a cancelled context unblocks a waiter
	if info, _ := run.Await(context.Background(), v); info.State != api.RunDone {
		t.Fatal("await after the end must not block")
	}
	block2 := make(chan struct{})
	defer close(block2)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block2; return s }
	run = r.m.Start(context.Background(), Request{SQL: "slow"})
	info := until(t, run, api.RunRunning)
	_ = info
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, cur := run.Await(context.Background(), 0)
	if got, _ := run.Await(ctx, cur); got.State != api.RunRunning {
		t.Fatalf("%+v", got)
	}
}

func TestOldRunsGiveUpTheirPagesButStayReadable(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
	var first *Run
	for i := 0; i < PagedRuns+3; i++ {
		run := r.m.Start(context.Background(), Request{SQL: "select"})
		run.Wait()
		if first == nil {
			first = run
		}
	}
	first.mu.Lock()
	n := len(first.pages)
	first.mu.Unlock()
	if n != 0 {
		t.Fatalf("the oldest run still holds %d pages", n)
	}
	page, err := first.Rows(context.Background(), 0, 10, format.Options{})
	if err != nil || len(page.Rows) != 3 {
		t.Fatalf("an old run is read again on demand: %v %+v", err, page)
	}
}
