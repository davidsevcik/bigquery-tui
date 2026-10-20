package runs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
)

func agentProfile(r *rig) *policy.Profile {
	r.cfg.AgentMaxBytes, r.cfg.AgentTimeout = 1<<30, time.Minute
	p := policy.For(r.cfg, "mcp")
	return &p
}

func TestPolicyRefusesAfterTheDryRunWithoutAJob(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DELETE", Bytes: 5} }
	info := r.m.Start(context.Background(), Request{SQL: "delete from t where true", Policy: p}).Wait()
	if info.State != api.RunRefused || info.Refusal == nil || info.Refusal.Code != api.RefuseNotReadOnly || info.Job != nil {
		t.Fatalf("%+v", info)
	}
	if calls := r.fake.QueryCalls(); len(calls) != 1 || !calls[0].Dry {
		t.Fatalf("only the dry-run may reach BigQuery: %+v", calls)
	}
	if len(r.recorded()) != 0 {
		t.Fatal("a refused Run started no job and is not History")
	}
}

func TestPolicyDryRunReportsTheVerdict(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Bytes = 2 << 30; return s }
	info := r.m.Start(context.Background(), Request{SQL: "select", Policy: p, DryRunOnly: true}).Wait()
	if info.State != api.RunEstimated || info.Refusal == nil || info.Refusal.Code != api.RefuseOverLimit || info.Estimate.Bytes != 2<<30 {
		t.Fatalf("%+v", info)
	}
}

func TestPolicyNeverWaitsForConfirmation(t *testing.T) {
	r := newRig(t)
	agentProfile(r)
	r.cfg.CLIAllowWrites = true
	cli := policy.For(r.cfg, "cli")
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{StatementType: "INSERT", Bytes: 10, AffectedRows: 2}
	}
	info := r.m.Start(context.Background(), Request{SQL: "insert", Policy: &cli}).Wait()
	if info.State != api.RunDone || info.Confirmation != nil || info.Stats.AffectedRows != 2 {
		t.Fatalf("a write the config allows runs without a question: %+v", info)
	}
	for _, c := range r.fake.QueryCalls() {
		if c.Req.MaxBytesBilled != r.cfg.MaxBytesBilled.Bytes || c.Req.Labels["bqt"] != "1" {
			t.Fatalf("the job cap is max_bytes_billed (BigQuery bills 10 MiB per table at least): %+v", c.Req)
		}
	}
}

func TestPolicyTimeoutCancelsTheJob(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	short := p.Lower(0, 50*time.Millisecond)
	block := make(chan struct{})
	defer close(block)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	info := r.m.Start(context.Background(), Request{SQL: "select slow", Policy: &short}).Wait()
	if info.State != api.RunFailed || info.Error == nil || info.Error.Kind != "timeout" || info.Job == nil {
		t.Fatalf("%+v", info)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(r.fake.CallsTo("CancelJob")) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the timed-out job was not cancelled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec := r.recorded(); len(rec) != 1 || rec[0].Error == nil || rec[0].Error.Kind != "timeout" {
		t.Fatalf("the timed-out Run is History: %+v", rec)
	}
}

func TestNoPolicyStillAsks(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DELETE"} }
	run := r.m.Start(context.Background(), Request{SQL: "delete"})
	until(t, run, api.RunNeedsOK)
	run.Cancel()
	if info := run.Wait(); info.State != api.RunCancelled || info.Refusal != nil {
		t.Fatalf("%+v", info)
	}
}

func TestSubmissionThatOutlivesTheTimeoutIsCancelledByID(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	short := p.Lower(0, 80*time.Millisecond)
	never := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.SubmitBlock = never; return s }
	info := r.m.Start(context.Background(), Request{SQL: "select", Policy: &short}).Wait()
	if info.State != api.RunFailed || info.Error == nil || info.Error.Kind != "timeout" {
		t.Fatalf("%+v", info)
	}
	var id string
	for _, c := range r.fake.QueryCalls() {
		if !c.Dry {
			id = c.Req.JobID
		}
	}
	if !strings.HasPrefix(id, "bqt_") {
		t.Fatalf("the job ID is chosen before submitting: %q", id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(r.fake.CallsTo("CancelJob")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job that may exist was not cancelled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSubmissionIsBoundedOutsideThePolicyToo(t *testing.T) {
	r := newRig(t)
	r.m.o.SubmitTimeout = 50 * time.Millisecond
	never := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.SubmitBlock = never; return s }
	info := r.m.Start(context.Background(), Request{SQL: "select"}).Wait()
	if info.State != api.RunFailed || info.Error == nil || !strings.Contains(info.Error.Message, "took too long") {
		t.Fatalf("%+v", info)
	}
}

func TestTimeoutWhileReadingTheFirstPage(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	short := p.Lower(0, 80*time.Millisecond)
	r.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "QueryRows" {
			time.Sleep(150 * time.Millisecond)
		}
	}
	info := r.m.Start(context.Background(), Request{SQL: "select", Policy: &short}).Wait()
	if info.State != api.RunFailed || info.Error == nil || info.Error.Kind != "timeout" || info.Stats == nil {
		t.Fatalf("a timeout while reading rows is a timeout, with the job's stats: %+v", info)
	}
	if rec := r.recorded(); len(rec) != 1 || rec[0].State != api.RunFailed {
		t.Fatalf("History says the same: %+v", rec)
	}
}
