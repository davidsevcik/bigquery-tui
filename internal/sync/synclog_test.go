package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// logBuf collects JSON log records from parallel units.
type logBuf struct {
	mu  gosync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// records returns the records whose msg starts with prefix.
func (b *logBuf) records(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if strings.HasPrefix(rec["msg"].(string), prefix) {
			out = append(out, rec)
		}
	}
	return out
}

// Every unit that starts logs its start and exactly one end with a
// duration, for region units and a single-table sync alike (#92).
func TestSyncLogsEachUnitStartAndEnd(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	h.run(api.SyncScope{})
	starts, ends := h.logs.records(t, "sync unit started"), h.logs.records(t, "sync unit done")
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("%d starts, %d ends, want 3 of each", len(starts), len(ends))
	}
	for _, e := range ends {
		if _, ok := e["took"]; !ok || e["tables"] == nil {
			t.Fatalf("an end record without its duration or counts: %v", e)
		}
	}
	h.logs = &logBuf{}
	table := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"}
	h.run(api.SyncScope{Table: &table})
	starts, ends = h.logs.records(t, "sync unit started"), h.logs.records(t, "sync unit done")
	if len(starts) != 1 || starts[0]["table"] != "proj-a.sales.orders" || len(ends) != 1 {
		t.Fatalf("single-table sync: %v %v", starts, ends)
	}
}

// A project whose discovery failed started no unit: one warn record says
// so, with no duration taken from other projects' work.
func TestSyncLogsDiscoveryFailure(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	h.fake.Fail(bqtest.Failure{Method: "ListDatasets", Project: "proj-b", Err: errors.New("denied")})
	h.run(api.SyncScope{})
	recs := h.logs.records(t, "sync discovery failed")
	if len(recs) != 1 || recs[0]["project"] != "proj-b" || recs[0]["level"] != "WARN" {
		t.Fatalf("%v", recs)
	}
	if _, ok := recs[0]["took"]; ok {
		t.Fatalf("a discovery failure has no unit duration: %v", recs[0])
	}
	if n := len(h.logs.records(t, "sync unit failed")); n != 0 {
		t.Fatalf("%d unit failures logged for a discovery failure", n)
	}
}

// A cancel ends the record of each unit it stopped.
func TestSyncLogsCancelledUnits(t *testing.T) {
	h := newHarness(t, `projects = ["proj-a", "proj-b"]`)
	twoProjectFake(h.fake)
	cctx, cancel := context.WithCancel(ctx)
	var regionCalls atomic.Int32
	h.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionTables" && regionCalls.Add(1) == 2 {
			cancel()
		}
	}
	s := New(Options{Config: h.cfg, Selected: h.selected, Meta: h.fake, Catalog: h.cat, Parallelism: 1, Now: func() time.Time { return h.now }, Backoff: func(int) time.Duration { return 0 }, Log: h.logs.logger()})
	if _, err := s.Run(cctx, api.SyncScope{}, nil); err != nil {
		t.Fatal(err)
	}
	starts := len(h.logs.records(t, "sync unit started"))
	ends := len(h.logs.records(t, "sync unit done")) + len(h.logs.records(t, "sync unit failed")) + len(h.logs.records(t, "sync unit cancelled"))
	if starts == 0 || len(h.logs.records(t, "sync unit cancelled")) == 0 || starts != ends {
		t.Fatalf("%d starts, %d ends, %d cancelled", starts, ends, len(h.logs.records(t, "sync unit cancelled")))
	}
}
