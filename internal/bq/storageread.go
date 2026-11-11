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

// Sync reads its INFORMATION_SCHEMA results through the BigQuery Storage Read
// API (#83): on a region with millions of column rows, REST getQueryResults
// paging takes minutes where the Storage Read API takes seconds. The Go client
// creates the read session itself and, when that fails, quietly reads the same
// job's result through REST paging instead (the job is not run again). The
// interceptor below is how bqt learns what happened to the session, so it can
// tell a missing permission or a disabled API (remembered for the client's
// lifetime: one Execution project) from any other failure (this query only).

const createReadSessionMethod = "/google.cloud.bigquery.storage.v1.BigQueryRead/CreateReadSession"

// defaultReadSessionBudget bounds the time spent creating one read session,
// retries and the waits between them included. The client would retry an
// unreachable endpoint for up to ten minutes; sessionInterceptor retries
// within the budget instead, and past it the result is read through REST.
const defaultReadSessionBudget = 20 * time.Second

// Read paths named by the "information_schema query" debug log.
const (
	readStorage = "storage"
	readREST    = "rest"
	// readRESTFallback is REST paging after the read session failed.
	readRESTFallback = "rest_after_read_session_failed"
)

// sessionWatch records what happened to the read session of one query. It
// travels in the query's context to the interceptor.
type sessionWatch struct {
	budget time.Duration

	mu        sync.Mutex
	start     time.Time
	attempted bool
	err       error // of the last attempt
}

type sessionWatchKey struct{}

func watchSessions(ctx context.Context, w *sessionWatch) context.Context {
	return context.WithValue(ctx, sessionWatchKey{}, w)
}

// outcome reports whether a read session was attempted and the error of the
// attempts (nil when the session was created).
func (w *sessionWatch) outcome() (attempted bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempted, w.err
}

// deadline starts the budget on the first attempt and returns when it ends.
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

// budgetSpent is the error for a read session given up on. It is not a gRPC
// status, so the client does not retry it; the last error is kept as text
// only, so its status does not leak through errors.As either.
func budgetSpent(last error) error {
	if last == nil {
		return errReadSessionBudget
	}
	return fmt.Errorf("%w: %s", errReadSessionBudget, last.Error())
}

// sessionRetryCodes and sessionBackoff are the Go client's own retry
// settings for CreateReadSession.
var sessionRetryCodes = []codes.Code{codes.DeadlineExceeded, codes.Unavailable}

func sessionBackoff() gax.Backoff {
	return gax.Backoff{Initial: 100 * time.Millisecond, Max: 60 * time.Second, Multiplier: 1.30}
}

// sessionInterceptor watches CreateReadSession calls made under a context
// that carries a sessionWatch, and leaves every other call alone. The client
// retries the call outside any interceptor, sleeping between attempts on the
// query's context, so the interceptor does the retrying itself: every
// attempt and wait fits in the budget, and what it returns is either the
// session or an error the client does not retry.
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

// create runs attempt until it succeeds, fails for good or the budget is
// spent.
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
		// An attempt cut off by the budget ends here too.
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

// readSessionUnavailable reports whether a read-session error means the
// Storage Read API cannot be used from this Execution project at all: the
// caller lacks bigquery.readsessions.create, or the API is disabled. BigQuery
// reports both as gRPC PERMISSION_DENIED (the disabled API with the
// SERVICE_DISABLED reason); accessNotConfigured is its REST form. The reason
// is a short text for the warning.
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

// readPath is one client's choice between the Storage Read API and REST
// paging for sync results. It starts on the Storage Read API and switches to
// REST for good on the first read session that fails for want of permission
// or a disabled API.
type readPath struct {
	mu      sync.Mutex
	rest    bool
	warning string
}

// storage reports whether sync should still try the Storage Read API, and
// the warning to repeat to each sync run when it does not.
func (p *readPath) storage() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.rest, p.warning
}

// sessionFailed records a failed read session. It returns the warning to
// give the sync run when err switches the client to REST paging (or already
// had), and first is true only for the call that made the switch.
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
