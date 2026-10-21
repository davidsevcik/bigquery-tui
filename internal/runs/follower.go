package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
)

// ErrUnknownRun is returned for a Run ID this process does not know.
var ErrUnknownRun = errors.New("unknown run")

// QueryFailure is a failed dry-run or job, with BigQuery's message and the
// error position when there is one.
type QueryFailure struct{ Err api.QueryError }

func (f *QueryFailure) Error() string {
	msg := f.Err.Message
	if f.Err.Line > 0 && !strings.Contains(msg, fmt.Sprintf("[%d:%d]", f.Err.Line, f.Err.Col)) {
		msg += fmt.Sprintf(" (at line %d, column %d)", f.Err.Line, f.Err.Col)
	}
	return msg
}

// pollEvery is how often Wait re-checks its condition between changes: the
// condition may depend on more than the Run (a test holding a job open).
const pollEvery = 10 * time.Millisecond

// Follower follows Runs for one frontend. It keeps the state of each Run as
// the frontend last applied it, so the frontend reacts to every change once
// and reads a state that only moves when it says so. It also remembers where
// each Run's SQL sits in the text it came from, so an error position maps
// back into that text.
//
// An interactive frontend loops Wait (in the background) and Apply (where it
// owns its state). An agent frontend calls Start and then Outcome.
type Follower struct {
	m *Manager

	mu   sync.Mutex
	seen map[int64]*followed
}

type followed struct {
	info    api.RunInfo
	version int64
	from    Origin
}

// Origin is where a Run's SQL came from: where it starts in its text (the
// zero-based line and column of a selection, zero for the whole text) and the
// version of the text it was taken from.
type Origin struct {
	Line, Col   int
	TextVersion int
}

// Update is a Run's state as Wait found it. Apply records it.
type Update struct {
	Info    api.RunInfo
	version int64
}

// Mark is where a failed Run's error points in the text it ran from: a
// one-based line and the column as BigQuery counts it, shifted by the Origin.
// TextVersion is the version of the text the position belongs to.
type Mark struct {
	Line, Col   int
	TextVersion int
}

// Follow returns a Follower of this Manager's Runs.
func (m *Manager) Follow() *Follower {
	return &Follower{m: m, seen: map[int64]*followed{}}
}

// Start begins a Run and follows it. Its state starts as the Run started
// (estimating), so the first change Wait reports is news to Apply.
func (f *Follower) Start(ctx context.Context, req Request, from Origin) int64 {
	r := f.m.Start(ctx, req)
	f.mu.Lock()
	f.seen[r.id] = &followed{info: r.first, version: 1, from: from}
	f.mu.Unlock()
	return r.id
}

// Wait blocks until the Run has changed since the state last applied, and
// returns that change without applying it. With until, it also waits until
// until(state) holds, re-checking it as the Run changes and every few
// milliseconds. It returns early when the Run has finished or ctx ends
// (with the newest state). false means the process does not know the Run.
func (f *Follower) Wait(ctx context.Context, id int64, until func(api.RunInfo) bool) (Update, bool) {
	r := f.m.Get(id)
	if r == nil {
		return Update{}, false
	}
	f.mu.Lock()
	var after int64
	if s := f.seen[id]; s != nil {
		after = s.version
	}
	f.mu.Unlock()
	if until == nil {
		info, v := r.Await(ctx, after)
		return Update{Info: info, version: v}, true
	}
	for {
		step, cancel := context.WithTimeout(ctx, pollEvery)
		info, v := r.Await(step, after)
		cancel()
		if info.State.Finished() || until(info) || ctx.Err() != nil {
			return Update{Info: info, version: v}, true
		}
		after = v
	}
}

// Apply records a state Wait returned, and reports whether it is news: the
// first state seen of the Run, or a new state or cancel request. A state
// older than the one applied is ignored.
func (f *Follower) Apply(u Update) (api.RunInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := u.Info.ID
	s, known := f.seen[id]
	if known && u.version < s.version {
		return s.info, false
	}
	if !known {
		s = &followed{}
		f.seen[id] = s
	}
	prev := s.info
	s.info, s.version = u.Info, u.version
	return u.Info, !known || prev.State != u.Info.State || prev.CancelRequested != u.Info.CancelRequested
}

// Info returns the state of a Run as it was last applied.
func (f *Follower) Info(id int64) (api.RunInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.seen[id]; s != nil {
		return s.info, true
	}
	return api.RunInfo{}, false
}

// ErrorMark is where the applied error of a failed Run points in the text
// it ran from. false when there is no position.
func (f *Follower) ErrorMark(id int64) (Mark, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.seen[id]
	if s == nil || s.info.Error == nil || s.info.Error.Line <= 0 {
		return Mark{}, false
	}
	e := s.info.Error
	mk := Mark{Line: e.Line + s.from.Line, Col: e.Col, TextVersion: s.from.TextVersion}
	if e.Line == 1 {
		mk.Col += s.from.Col
	}
	return mk, true
}

// List returns every Run of the process as it is now, newest first.
func (f *Follower) List() []api.RunInfo { return f.m.List() }

// Pricing reports what the dry-runs so far revealed about billing.
func (f *Follower) Pricing() api.PricingModel { return f.m.Pricing() }

// Confirm lets a Run waiting for confirmation proceed. Its applied state
// moves on to queued at once, so the question is not asked again.
func (f *Follower) Confirm(id int64) {
	if r := f.m.Get(id); r != nil {
		r.Confirm()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.seen[id]; s != nil && s.info.State == api.RunNeedsOK {
		s.info.State, s.info.Confirmation = api.RunQueued, nil
	}
}

// Cancel stops a Run (declining its confirmation is a cancel too).
func (f *Follower) Cancel(id int64) {
	if r := f.m.Get(id); r != nil {
		r.Cancel()
	}
}

// CancelAll cancels every unfinished Run of the process.
func (f *Follower) CancelAll() { f.m.CancelAll() }

// Outcome follows a Run to its end and stops following it. When ctx ends
// first, the Run (and its job) is cancelled before Outcome returns. The error
// is the end state's: a refusal is a *policy.Error, a failure a
// *QueryFailure, a cancel context.Canceled.
func (f *Follower) Outcome(ctx context.Context, id int64) (api.RunInfo, error) {
	r := f.m.Get(id)
	if r == nil {
		return api.RunInfo{}, ErrUnknownRun
	}
	select {
	case <-r.Done():
	case <-ctx.Done():
		r.Cancel()
		<-r.Done()
	}
	f.mu.Lock()
	delete(f.seen, id)
	f.mu.Unlock()
	info := r.Info()
	return info, endError(info)
}

// endError turns a Run's end state into the error a frontend reports.
func endError(info api.RunInfo) error {
	switch info.State {
	case api.RunRefused:
		if info.Refusal != nil {
			return &policy.Error{Refusal: *info.Refusal}
		}
	case api.RunCancelled:
		return context.Canceled
	case api.RunFailed:
		if info.Error != nil {
			return &QueryFailure{Err: *info.Error}
		}
		return errors.New("the query failed")
	}
	return nil
}
