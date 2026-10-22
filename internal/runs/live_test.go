//go:build live

package runs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// TestLiveReopen runs a query that reads no table, then reopens its result
// from a second Manager, as a later bqt process does from a History entry.
//
//	BQT_LIVE_EXECUTION_PROJECT=... go test -tags live ./internal/runs -run Live -v
func TestLiveReopen(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	if exec == "" {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT")
	}
	ctx := context.Background()
	c, err := bq.NewClient(ctx, exec, bq.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cfg := &config.Config{Location: os.Getenv("BQT_LIVE_LOCATION"), MaxConcurrentQueries: 1,
		ConfirmBytes: config.ByteLimit{Mode: config.ByteLimitAuto}, MaxBytesBilled: config.ByteLimit{Mode: config.ByteLimitValue, Bytes: 10 << 20}}
	opts := Options{Jobs: func(context.Context, string) (bq.Jobs, error) { return c, nil }, Config: func() *config.Config { return cfg }, Execution: func() string { return exec }}
	m := New(opts)
	defer m.Close()
	info := m.Start(ctx, Request{SQL: "SELECT x, CONCAT('row ', CAST(x AS STRING)) AS label FROM UNNEST(GENERATE_ARRAY(1, 1200)) AS x ORDER BY x"}).Wait()
	if info.State != api.RunDone || info.TotalRows != 1200 {
		t.Fatalf("%+v", info)
	}
	m2 := New(opts)
	defer m2.Close()
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r, err := m2.Reopen(rctx, api.HistoryEntry{ID: 1, Job: *info.Job, SQL: info.SQL, State: api.RunDone, Source: "tui"}, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := r.Rows(rctx, 1195, 10, format.Options{})
	if err != nil || len(page.Rows) != 5 || page.Rows[4][1].Text != "row 1200" {
		t.Fatalf("%+v %v", page, err)
	}
	t.Logf("reopened job %s in %s: %d rows", info.Job.ID, info.Job.Location, r.Info().TotalRows)
	// a job id that does not exist reads as gone
	ref := *info.Job
	ref.ID += "-nope"
	if _, err := m2.Reopen(rctx, api.HistoryEntry{Job: ref, State: api.RunDone}, ""); err == nil {
		t.Fatal("an unknown job must fail")
	} else {
		t.Logf("unknown job: %v", err)
	}
}
