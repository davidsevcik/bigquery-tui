package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/policy"
)

var ErrUnknownRun = errors.New("unknown run")

type QueryFailure struct{ Err api.QueryError }

func (f *QueryFailure) Error() string {
	msg := f.Err.Message
	if f.Err.Line > 0 && !strings.Contains(msg, fmt.Sprintf("[%d:%d]", f.Err.Line, f.Err.Col)) {
		msg += fmt.Sprintf(" (at line %d, column %d)", f.Err.Line, f.Err.Col)
	}
	return msg
}

const pollEvery = 10 * time.Millisecond

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

type Origin struct {
	Line, Col   int
	TextVersion int
}

type Update struct {
	Info    api.RunInfo
	version int64
}

type Mark struct {
	Line, Col   int
	TextVersion int
}

func (m *Manager) Follow() *Follower {
	return &Follower{m: m, seen: map[int64]*followed{}}
}

func (f *Follower) Start(ctx context.Context, req Request, from Origin) int64 {
	r := f.m.Start(ctx, req)
	f.mu.Lock()
	f.seen[r.id] = &followed{info: r.first, version: 1, from: from}
	f.mu.Unlock()
	return r.id
}

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

func (f *Follower) Info(id int64) (api.RunInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.seen[id]; s != nil {
		return s.info, true
	}
	return api.RunInfo{}, false
}

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

func (f *Follower) MarkIn(id, latest int64, version int) (Mark, bool) {
	mk, ok := f.ErrorMark(id)
	if !ok || id != latest || version != mk.TextVersion {
		return Mark{}, false
	}
	return mk, true
}

func (f *Follower) List() []api.RunInfo { return f.m.List() }

func (f *Follower) Pricing() api.PricingModel { return f.m.Pricing() }

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

func (f *Follower) Cancel(id int64) {
	if r := f.m.Get(id); r != nil {
		r.Cancel()
	}
}

func (f *Follower) CancelAll() { f.m.CancelAll() }

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
