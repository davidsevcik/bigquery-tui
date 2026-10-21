package runs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
)

// follow loops Wait and Apply the way a frontend does, until the Run has
// finished, and returns the states that were news.
func follow(t *testing.T, f *Follower, id int64) []api.RunState {
	t.Helper()
	var news []api.RunState
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		u, ok := f.Wait(ctx, id, nil)
		if !ok || ctx.Err() != nil {
			t.Fatalf("lost run %d (known %v)", id, ok)
		}
		info, isNews := f.Apply(u)
		if isNews {
			news = append(news, info.State)
		}
		if info.State.Finished() {
			return news
		}
	}
}

func TestFollowerReportsEachChangeOnce(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(3) }
	f := r.m.Follow()
	id := f.Start(context.Background(), Request{SQL: "select 1", Tag: "buf1"}, Origin{})
	if info, ok := f.Info(id); !ok || info.State != api.RunEstimating || info.Tag != "buf1" {
		t.Fatalf("a started Run is followed from its first state: %+v", info)
	}
	news := follow(t, f, id)
	if len(news) == 0 || news[len(news)-1] != api.RunDone {
		t.Fatalf("the end is news: %v", news)
	}
	for i := 1; i < len(news); i++ {
		if news[i] == news[i-1] {
			t.Fatalf("a state is news once: %v", news)
		}
	}
	// a finished Run answers at once, and nothing about it is news any more
	u, _ := f.Wait(context.Background(), id, nil)
	if info, isNews := f.Apply(u); isNews || info.State != api.RunDone || info.TotalRows != 3 {
		t.Fatalf("%+v news %v", info, isNews)
	}
}

func TestFollowerIgnoresAnOlderState(t *testing.T) {
	r := newRig(t)
	f := r.m.Follow()
	block := make(chan struct{})
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	id := f.Start(context.Background(), Request{SQL: "select 1"}, Origin{})
	until(t, r.m.Get(id), api.RunRunning)
	early, _ := f.Wait(context.Background(), id, nil)
	close(block)
	r.m.Get(id).Wait()
	late, _ := f.Wait(context.Background(), id, nil)
	f.Apply(late)
	if info, isNews := f.Apply(early); isNews || info.State != api.RunDone {
		t.Fatalf("a state older than the applied one is ignored: %+v news %v", info, isNews)
	}
}

func TestFollowerAdoptsARunItDidNotStart(t *testing.T) {
	r := newRig(t)
	run := r.m.Start(context.Background(), Request{SQL: "select 1"})
	run.Wait()
	f := r.m.Follow()
	if _, ok := f.Info(run.ID()); ok {
		t.Fatal("nothing is known before it is applied")
	}
	u, ok := f.Wait(context.Background(), run.ID(), nil)
	if info, isNews := f.Apply(u); !ok || !isNews || info.State != api.RunDone {
		t.Fatalf("the first state applied is news: %+v", info)
	}
}

func TestFollowerMapsTheErrorIntoTheTextItRanFrom(t *testing.T) {
	r := newRig(t)
	f := r.m.Follow()
	for _, c := range []struct {
		bqPos           string
		line, col, from int
	}{
		{"[1:8]", 3, 12, 0}, // the first line of a selection starts at its column
		{"[2:3]", 4, 3, 0},  // later lines start at column 1
	} {
		r.fake.OnQuery = func(string) bqtest.QueryScript {
			return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected end at " + c.bqPos)}
		}
		id := f.Start(context.Background(), Request{SQL: "select\nx"}, Origin{Line: 2, Col: 4, TextVersion: 7})
		follow(t, f, id)
		mk, ok := f.ErrorMark(id)
		if !ok || mk.Line != c.line || mk.Col != c.col || mk.TextVersion != 7 {
			t.Fatalf("%s: %+v", c.bqPos, mk)
		}
	}
	// no position, no mark
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Access denied")}
	}
	id := f.Start(context.Background(), Request{SQL: "select 1"}, Origin{Line: 2})
	follow(t, f, id)
	if _, ok := f.ErrorMark(id); ok {
		t.Fatal("an error without a position marks nothing")
	}
}

func TestFollowerConfirmMovesOnAtOnce(t *testing.T) {
	r := newRig(t)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DELETE"} }
	f := r.m.Follow()
	asks := func(i api.RunInfo) bool { return i.State == api.RunNeedsOK }

	id := f.Start(context.Background(), Request{SQL: "delete from t where true"}, Origin{})
	u, _ := f.Wait(context.Background(), id, asks)
	if info, isNews := f.Apply(u); !isNews || info.State != api.RunNeedsOK || info.Confirmation == nil {
		t.Fatalf("%+v", info)
	}
	f.Confirm(id)
	if info, _ := f.Info(id); info.State != api.RunQueued || info.Confirmation != nil {
		t.Fatalf("the answered question is not asked again: %+v", info)
	}
	if news := follow(t, f, id); news[len(news)-1] != api.RunDone {
		t.Fatalf("%v", news)
	}

	// declining is a cancel
	id = f.Start(context.Background(), Request{SQL: "delete from t where true"}, Origin{})
	u, _ = f.Wait(context.Background(), id, asks)
	f.Apply(u)
	f.Cancel(id)
	if news := follow(t, f, id); news[len(news)-1] != api.RunCancelled {
		t.Fatalf("%v", news)
	}
	if n := len(r.fake.CallsTo("StartQuery")); n != 1 {
		t.Fatalf("only the confirmed statement ran: %d jobs", n)
	}
}

func TestFollowerWaitUntil(t *testing.T) {
	r := newRig(t)
	block := make(chan struct{})
	defer close(block)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	f := r.m.Follow()
	id := f.Start(context.Background(), Request{SQL: "select 1"}, Origin{})
	// the condition may depend on more than the Run: it is checked between changes too
	u, _ := f.Wait(context.Background(), id, func(i api.RunInfo) bool { return i.Job != nil && r.fake.Held(i.Job.ID) })
	if u.Info.State != api.RunRunning {
		t.Fatalf("%+v", u.Info)
	}
	// ctx ends the wait with the newest state
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	f.Apply(u)
	if u, _ := f.Wait(ctx, id, nil); u.Info.State != api.RunRunning {
		t.Fatalf("%+v", u.Info)
	}
	// a finished Run ends the wait whatever the condition says
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	id = f.Start(context.Background(), Request{SQL: "select 2"}, Origin{})
	if u, _ := f.Wait(context.Background(), id, func(api.RunInfo) bool { return false }); u.Info.State != api.RunDone {
		t.Fatalf("%+v", u.Info)
	}
}

func TestFollowerUnknownRun(t *testing.T) {
	r := newRig(t)
	f := r.m.Follow()
	if _, ok := f.Wait(context.Background(), 42, nil); ok {
		t.Fatal("an unknown Run is reported, not invented")
	}
	if _, err := f.Outcome(context.Background(), 42); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("%v", err)
	}
	f.Confirm(42) // no-ops
	f.Cancel(42)
}

func TestFollowerOutcome(t *testing.T) {
	r := newRig(t)
	p := agentProfile(r)
	f := r.m.Follow()
	ctx := context.Background()

	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(2) }
	id := f.Start(ctx, Request{SQL: "select 1", Policy: p}, Origin{})
	if info, err := f.Outcome(ctx, id); err != nil || info.State != api.RunDone || info.TotalRows != 2 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, ok := f.Info(id); ok {
		t.Fatal("Outcome stops following the Run")
	}

	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DELETE"} }
	var pe *policy.Error
	if _, err := f.Outcome(ctx, f.Start(ctx, Request{SQL: "delete", Policy: p}, Origin{})); !errors.As(err, &pe) || pe.Refusal.Code != api.RefuseNotReadOnly {
		t.Fatalf("a refusal is a *policy.Error: %v", err)
	}

	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected keyword FROM")}
	}
	var qf *QueryFailure
	if _, err := f.Outcome(ctx, f.Start(ctx, Request{SQL: "select\n from", Policy: p}, Origin{})); !errors.As(err, &qf) || !strings.HasPrefix(qf.Error(), "Syntax error") {
		t.Fatalf("a failure is a *QueryFailure: %v", err)
	}

	// the caller giving up cancels the Run and its job
	block := make(chan struct{})
	defer close(block)
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	id = f.Start(ctx, Request{SQL: "select slow", Policy: p}, Origin{})
	until(t, r.m.Get(id), api.RunRunning)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if info, err := f.Outcome(gone, id); !errors.Is(err, context.Canceled) || info.State != api.RunCancelled {
		t.Fatalf("%+v %v", info, err)
	}
	if len(r.fake.CallsTo("CancelJob")) != 1 {
		t.Fatal("the job was asked to stop")
	}
}

func TestQueryFailureNamesThePosition(t *testing.T) {
	f := &QueryFailure{Err: api.QueryError{Message: "Unrecognized name: x", Line: 2, Col: 5}}
	if got := f.Error(); got != "Unrecognized name: x (at line 2, column 5)" {
		t.Fatal(got)
	}
	f.Err.Message = "Unrecognized name: x at [2:5]"
	if got := f.Error(); got != f.Err.Message {
		t.Fatalf("a position already in the message is not repeated: %s", got)
	}
}
