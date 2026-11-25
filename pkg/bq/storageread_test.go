package bq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/bigquery/storage/apiv1/storagepb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	bqapi "cloud.google.com/go/bigquery"
)

// serviceDisabled is how BigQuery refuses a read session when the BigQuery
// Storage API is disabled on the project.
func serviceDisabled(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.PermissionDenied, "BigQuery Storage API has not been used in project 123 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/bigquerystorage.googleapis.com/overview?project=123 then retry.").
		WithDetails(&errdetails.ErrorInfo{Reason: "SERVICE_DISABLED", Domain: "googleapis.com", Metadata: map[string]string{"service": "bigquerystorage.googleapis.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func noReadSessionPermission() error {
	return status.Error(codes.PermissionDenied, "request failed: the user does not have 'bigquery.readsessions.create' permission for 'projects/exec'")
}

func TestReadSessionUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		want   bool
		reason string
	}{
		{"no readsessions.create", noReadSessionPermission(), true, "permission denied"},
		{"service disabled", serviceDisabled(t), true, "disabled"},
		{"wrapped", fmt.Errorf("create session: %w", noReadSessionPermission()), true, "permission denied"},
		{"REST accessNotConfigured", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "accessNotConfigured"}}}, true, "disabled"},
		{"unavailable", status.Error(codes.Unavailable, "connection refused"), false, ""},
		{"internal", status.Error(codes.Internal, "boom"), false, ""},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad table"), false, ""},
		{"budget spent", errReadSessionBudget, false, ""},
		{"plain", errors.New("boom"), false, ""},
		{"nil", nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := readSessionUnavailable(tc.err)
			if ok != tc.want || !strings.Contains(reason, tc.reason) {
				t.Fatalf("got (%q, %v), want ok=%v and a reason with %q", reason, ok, tc.want, tc.reason)
			}
		})
	}
}

// The fallback decision: a permission or disabled-service error switches the
// client to REST for good, once; any other error leaves it on the Storage
// Read API.
func TestReadPathRemembersOnlyUnavailable(t *testing.T) {
	var p readPath
	for _, err := range []error{status.Error(codes.Unavailable, "x"), status.Error(codes.Internal, "x"), errReadSessionBudget} {
		if w, first := p.sessionFailed("exec", err); w != "" || first {
			t.Fatalf("%v switched to REST", err)
		}
		if on, _ := p.storage(); !on {
			t.Fatalf("%v is remembered", err)
		}
	}
	w, first := p.sessionFailed("exec", serviceDisabled(t))
	if !first || !strings.Contains(w, "exec") || !strings.Contains(w, "REST paging") {
		t.Fatalf("got (%q, %v)", w, first)
	}
	on, again := p.storage()
	if on || again != w {
		t.Fatalf("storage %v, warning %q: the switch is not remembered", on, again)
	}
	if w2, first := p.sessionFailed("exec", noReadSessionPermission()); first || w2 != w {
		t.Fatalf("a second failure gives (%q, %v): one switch, one warning", w2, first)
	}
}

// fakeRead is a Storage Read server that refuses every read session.
type fakeRead struct {
	storagepb.UnimplementedBigQueryReadServer
	err   error
	calls atomic.Int32
}

func (f *fakeRead) CreateReadSession(context.Context, *storagepb.CreateReadSessionRequest) (*storagepb.ReadSession, error) {
	f.calls.Add(1)
	return nil, f.err
}

// fakeREST answers the REST calls of one query read through jobs.query: the
// first page with a page token, the job and its anonymous destination table
// (looked up for the read session), and the second page.
func fakeREST(t *testing.T) *httptest.Server {
	t.Helper()
	var seq atomic.Int32
	schema := map[string]any{"fields": []any{map[string]any{"name": "v", "type": "STRING"}}}
	row := func(v string) any { return map[string]any{"f": []any{map[string]any{"v": v}}} }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/exec/")
		var out any
		switch {
		case r.Method == http.MethodPost && path == "queries":
			id := fmt.Sprintf("job-%d", seq.Add(1))
			out = map[string]any{
				"jobReference": map[string]any{"projectId": "exec", "jobId": id, "location": "EU"},
				"jobComplete":  true, "schema": schema, "rows": []any{row("r1")}, "pageToken": "p2", "totalRows": "2",
			}
		case strings.HasPrefix(path, "jobs/"):
			out = map[string]any{
				"jobReference": map[string]any{"projectId": "exec", "jobId": strings.TrimPrefix(path, "jobs/"), "location": "EU"},
				"configuration": map[string]any{"query": map[string]any{
					"query":            "SELECT 1",
					"destinationTable": map[string]any{"projectId": "exec", "datasetId": "_anon", "tableId": "anon"},
				}},
				"status": map[string]any{"state": "DONE"},
			}
		case path == "datasets/_anon/tables/anon":
			out = map[string]any{"tableReference": map[string]any{"projectId": "exec", "datasetId": "_anon", "tableId": "anon"}, "schema": schema}
		case r.Method == http.MethodGet && strings.HasPrefix(path, "queries/") && r.URL.Query().Get("pageToken") == "p2":
			out = map[string]any{"jobComplete": true, "rows": []any{row("r2")}, "totalRows": "2"}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type syncQueryRig struct {
	c     *Client
	read  *fakeRead
	log   *bytes.Buffer
	mu    gosync.Mutex
	warns []string
}

func newSyncQueryRig(t *testing.T, sessionErr error) *syncQueryRig {
	t.Helper()
	t.Setenv(EndpointEnv, "")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	fr := &fakeRead{err: sessionErr}
	storagepb.RegisterBigQueryReadServer(gs, fr)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	srv := fakeREST(t)
	r := &syncQueryRig{read: fr, log: &bytes.Buffer{}}
	c, err := NewClient(context.Background(), "exec", Options{
		ExtraClientOptions: []option.ClientOption{option.WithEndpoint(srv.URL + "/bigquery/v2/"), option.WithoutAuthentication()},
		StorageReadOptions: []option.ClientOption{
			option.WithEndpoint(lis.Addr().String()), option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		},
		Log: slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.syncBQ == nil {
		t.Fatal("the Storage Read client is not enabled")
	}
	t.Cleanup(func() { _ = c.Close() })
	r.c = c
	return r
}

// query runs one sync query and returns the rows and the read path.
func (r *syncQueryRig) query(t *testing.T) ([]string, string) {
	t.Helper()
	ctx := WithWarnings(context.Background(), func(msg string) {
		r.mu.Lock()
		r.warns = append(r.warns, msg)
		r.mu.Unlock()
	})
	var rows []string
	read, err := r.c.query(ctx, "EU", "SELECT 1", nil, func(row []bqapi.Value) error {
		rows = append(rows, fmt.Sprint(row[0]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows, read
}

func (r *syncQueryRig) logWarnings() int {
	return strings.Count(r.log.String(), "level=WARN")
}

func TestSyncQueryFallsBackToRESTWithoutTheReadSessionPermission(t *testing.T) {
	r := newSyncQueryRig(t, noReadSessionPermission())
	rows, read := r.query(t)
	if strings.Join(rows, ",") != "r1,r2" || read != readRESTFallback {
		t.Fatalf("rows %v read %q: the failed read session must be read again through REST", rows, read)
	}
	if r.logWarnings() != 1 || len(r.warns) != 1 || !strings.Contains(r.warns[0], "REST paging") {
		t.Fatalf("log %q, warnings %q", r.log, r.warns)
	}
	// Remembered: the next query does not try the Storage Read API, and the
	// sync run hears of it again (it adds the warning once).
	rows, read = r.query(t)
	if strings.Join(rows, ",") != "r1,r2" || read != readREST {
		t.Fatalf("rows %v read %q", rows, read)
	}
	if n := r.read.calls.Load(); n != 1 {
		t.Fatalf("%d read sessions: the fallback is not remembered", n)
	}
	if r.logWarnings() != 1 || len(r.warns) != 2 || r.warns[1] != r.warns[0] {
		t.Fatalf("log %q, warnings %q: one log warning per client", r.log, r.warns)
	}
}

func TestSyncQueryFallsBackToRESTWhenTheAPIIsDisabled(t *testing.T) {
	r := newSyncQueryRig(t, serviceDisabled(t))
	if rows, read := r.query(t); strings.Join(rows, ",") != "r1,r2" || read != readRESTFallback {
		t.Fatalf("rows %v read %q", rows, read)
	}
	if len(r.warns) != 1 || !strings.Contains(r.warns[0], "disabled") {
		t.Fatalf("warnings %q", r.warns)
	}
	if on, _ := r.c.reads.storage(); on {
		t.Fatal("the fallback is not remembered")
	}
}

// Any other read-session error reads that result through REST and leaves
// the Storage Read API on for the next query.
func TestSyncQueryKeepsTheStorageReadAPIAfterOtherErrors(t *testing.T) {
	r := newSyncQueryRig(t, status.Error(codes.InvalidArgument, "bad session"))
	for i := 1; i <= 2; i++ {
		if rows, read := r.query(t); strings.Join(rows, ",") != "r1,r2" || read != readRESTFallback {
			t.Fatalf("query %d: rows %v read %q", i, rows, read)
		}
		if n := r.read.calls.Load(); int(n) != i {
			t.Fatalf("query %d: %d read sessions", i, n)
		}
	}
	if r.logWarnings() != 0 || len(r.warns) != 0 {
		t.Fatalf("log %q, warnings %q", r.log, r.warns)
	}
}

// An unreachable endpoint is retried by the client; the budget ends that and
// the result is read through REST.
func TestSyncQueryReadSessionBudget(t *testing.T) {
	r := newSyncQueryRig(t, status.Error(codes.Unavailable, "unreachable"))
	r.c.sessionBudget = 300 * time.Millisecond
	start := time.Now()
	if rows, read := r.query(t); strings.Join(rows, ",") != "r1,r2" || read != readRESTFallback {
		t.Fatalf("rows %v read %q", rows, read)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	if n := r.read.calls.Load(); n < 2 {
		t.Fatalf("%d attempts: Unavailable is retried until the budget is spent", n)
	}
	if on, _ := r.c.reads.storage(); !on {
		t.Fatal("an unreachable endpoint is remembered")
	}
}

// The client retries CreateReadSession outside the interceptor and sleeps
// between attempts, so the budget holds only if the interceptor never
// returns an error the client retries: it retries within the budget itself,
// waits included, whether the endpoint refuses at once or hangs.
func TestSessionInterceptorKeepsRetriesInTheBudget(t *testing.T) {
	const budget = 300 * time.Millisecond
	for _, tc := range []struct {
		name    string
		attempt func(ctx context.Context) error
	}{
		{"unavailable", func(context.Context) error { return status.Error(codes.Unavailable, "unreachable") }},
		{"hangs", func(ctx context.Context) error {
			<-ctx.Done()
			return status.FromContextError(ctx.Err()).Err()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &sessionWatch{budget: budget}
			ctx := watchSessions(context.Background(), w)
			var calls atomic.Int32
			invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
				calls.Add(1)
				return tc.attempt(ctx)
			}
			start := time.Now()
			err := sessionInterceptor(ctx, createReadSessionMethod, nil, nil, nil, invoker)
			took := time.Since(start)
			if !errors.Is(err, errReadSessionBudget) {
				t.Fatalf("got %v, want the budget error", err)
			}
			if _, again := gax.OnCodes(sessionRetryCodes, sessionBackoff()).Retry(err); again {
				t.Fatalf("the client would retry %v and sleep past the budget", err)
			}
			if took > budget+100*time.Millisecond {
				t.Fatalf("took %s with a %s budget", took, budget)
			}
			if tc.name == "unavailable" && calls.Load() < 2 {
				t.Fatalf("%d attempts: Unavailable is retried within the budget", calls.Load())
			}
			if _, last := w.outcome(); !errors.Is(last, errReadSessionBudget) {
				t.Fatalf("outcome %v", last)
			}
		})
	}
}

// Errors the client does not retry, and a created session, come back at
// once, as they are.
func TestSessionInterceptorReturnsFinalResultsAtOnce(t *testing.T) {
	for _, want := range []error{nil, noReadSessionPermission(), status.Error(codes.InvalidArgument, "bad table")} {
		w := &sessionWatch{budget: time.Minute}
		var calls int
		invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			calls++
			return want
		}
		err := sessionInterceptor(watchSessions(context.Background(), w), createReadSessionMethod, nil, nil, nil, invoker)
		if !errors.Is(err, want) || calls != 1 {
			t.Fatalf("got %v after %d calls, want %v after 1", err, calls, want)
		}
		if attempted, last := w.outcome(); !attempted || !errors.Is(last, want) {
			t.Fatalf("outcome (%v, %v)", attempted, last)
		}
	}
}

func TestNoStorageReadWithoutAnExecutionProjectOrWithAnEndpoint(t *testing.T) {
	ctx := context.Background()
	t.Setenv(EndpointEnv, "")
	c, err := NewClient(ctx, "", Options{ExtraClientOptions: []option.ClientOption{option.WithoutAuthentication()}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.syncBQ != nil {
		t.Fatal("Storage Read client without an Execution project")
	}
	t.Setenv(EndpointEnv, "http://localhost:9050")
	c2, err := NewClient(ctx, "exec", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.syncBQ != nil {
		t.Fatal("Storage Read client with an endpoint override")
	}
	c3, err := NewClient(ctx, "exec", Options{NoStorageRead: true, ExtraClientOptions: []option.ClientOption{option.WithoutAuthentication()}})
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if c3.syncBQ != nil {
		t.Fatal("Storage Read client with NoStorageRead")
	}
}
