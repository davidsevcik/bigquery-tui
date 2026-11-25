package bq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const createReadSessionMethod = "/google.cloud.bigquery.storage.v1.BigQueryRead/CreateReadSession"

const defaultReadSessionBudget = 20 * time.Second

const (
	readStorage      = "storage"
	readREST         = "rest"
	readRESTFallback = "rest_after_read_session_failed"
)

type sessionWatch struct {
	budget time.Duration

	mu        sync.Mutex
	start     time.Time
	attempted bool
	err       error
}

type sessionWatchKey struct{}

func watchSessions(ctx context.Context, w *sessionWatch) context.Context {
	return context.WithValue(ctx, sessionWatchKey{}, w)
}

func (w *sessionWatch) outcome() (attempted bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempted, w.err
}

func (w *sessionWatch) deadline(now time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.attempted {
		w.attempted, w.start = true, now
	}
	return w.start.Add(w.budget)
}

func (w *sessionWatch) end(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
}

var errReadSessionBudget = errors.New("creating the read session took too long")

func budgetSpent(last error) error {
	if last == nil {
		return errReadSessionBudget
	}
	return fmt.Errorf("%w: %s", errReadSessionBudget, last.Error())
}

var sessionRetryCodes = []codes.Code{codes.DeadlineExceeded, codes.Unavailable}

func sessionBackoff() gax.Backoff {
	return gax.Backoff{Initial: 100 * time.Millisecond, Max: 60 * time.Second, Multiplier: 1.30}
}

func sessionInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	w, _ := ctx.Value(sessionWatchKey{}).(*sessionWatch)
	if w == nil || method != createReadSessionMethod {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	err := w.create(ctx, func(actx context.Context) error {
		return invoker(actx, method, req, reply, cc, opts...)
	})
	w.end(err)
	return err
}

func (w *sessionWatch) create(ctx context.Context, attempt func(context.Context) error) error {
	deadline := w.deadline(time.Now())
	retry := gax.OnCodes(sessionRetryCodes, sessionBackoff())
	var last error
	for {
		if !time.Now().Before(deadline) {
			return budgetSpent(last)
		}
		actx, cancel := context.WithDeadline(ctx, deadline)
		err := attempt(actx)
		cancel()
		if err == nil || ctx.Err() != nil {
			return err
		}
		pause, again := retry.Retry(err)
		if !again {
			return err
		}
		last = err
		if time.Until(deadline) <= pause {
			return budgetSpent(last)
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func readSessionUnavailable(err error) (reason string, ok bool) {
	if err == nil {
		return "", false
	}
	if st, isStatus := status.FromError(err); isStatus && st.Code() == codes.PermissionDenied {
		for _, d := range st.Details() {
			if info, isInfo := d.(interface{ GetReason() string }); isInfo && info.GetReason() == "SERVICE_DISABLED" {
				return "the BigQuery Storage API is disabled", true
			}
		}
		if strings.Contains(st.Message(), "has not been used") || strings.Contains(st.Message(), "is disabled") {
			return "the BigQuery Storage API is disabled", true
		}
		return "permission denied (bigquery.readsessions.create)", true
	}
	if ge, isGoogle := errors.AsType[*googleapi.Error](err); isGoogle && ge.Code == 403 {
		for _, e := range ge.Errors {
			if e.Reason == "accessNotConfigured" {
				return "the BigQuery Storage API is disabled", true
			}
		}
		return "permission denied (bigquery.readsessions.create)", true
	}
	return "", false
}

type readPath struct {
	mu      sync.Mutex
	rest    bool
	warning string
}

func (p *readPath) storage() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.rest, p.warning
}

func (p *readPath) sessionFailed(execProject string, err error) (warning string, first bool) {
	reason, ok := readSessionUnavailable(err)
	if !ok {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rest {
		return p.warning, false
	}
	p.rest = true
	p.warning = fmt.Sprintf("the BigQuery Storage Read API cannot be used in Execution project %s (%s), so sync reads results through REST paging, which is much slower on large regions: grant roles/bigquery.readSessionUser there, or enable bigquerystorage.googleapis.com", execProject, reason)
	return p.warning, true
}
