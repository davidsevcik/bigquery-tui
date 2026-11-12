//go:build live

package bq

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	bqapi "cloud.google.com/go/bigquery"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// The Storage Read API checks of spec section 17. They read no tables:
//
//	BQT_LIVE_EXECUTION_PROJECT=... [BQT_LIVE_PROJECT=...] [BQT_LIVE_LOCATION=US] \
//	  go test -tags live ./internal/bq -run TestLiveStorageRead -v
//
// With BQT_LIVE_PROJECT set, the region of that project with the most
// datasets is also snapshotted both ways and the timings are logged.
// TestLiveStorageReadFallback needs BQT_LIVE_NO_READ_SESSION_PROJECT: an
// Execution project where the credentials lack bigquery.readsessions.create
// (or where the BigQuery Storage API is disabled).

// manyRows is a query whose result spans several REST pages, so the Go client
// reads it through a read session. It scans no table.
const manyRows = "SELECT n, CONCAT('row-', CAST(n AS STRING)) AS s FROM UNNEST(GENERATE_ARRAY(1, 500000)) AS n"

func liveLocation() string {
	if l := os.Getenv("BQT_LIVE_LOCATION"); l != "" {
		return l
	}
	return "US"
}

func liveClient(t *testing.T, exec string, o Options) (*Client, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	o.Log = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := NewClient(context.Background(), exec, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, &log
}

func liveRows(t *testing.T, ctx context.Context, c *Client) ([]string, string) {
	t.Helper()
	var rows []string
	read, err := c.query(ctx, liveLocation(), manyRows, nil, func(r []bqapi.Value) error {
		rows = append(rows, fmt.Sprint(r))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows, read
}

// TestLiveStorageReadMatchesREST reads the same result both ways.
func TestLiveStorageReadMatchesREST(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	if exec == "" {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT")
	}
	ctx := context.Background()
	storage, _ := liveClient(t, exec, Options{})
	rest, _ := liveClient(t, exec, Options{NoStorageRead: true})

	start := time.Now()
	viaStorage, read := liveRows(t, ctx, storage)
	tookStorage := time.Since(start)
	if read != readStorage {
		t.Fatalf("read %q: the Storage Read API was not used (is it available in %s?)", read, exec)
	}
	start = time.Now()
	viaREST, read := liveRows(t, ctx, rest)
	tookREST := time.Since(start)
	if read != readREST {
		t.Fatalf("read %q with NoStorageRead", read)
	}
	slices.Sort(viaStorage)
	slices.Sort(viaREST)
	if len(viaStorage) != 500000 || !slices.Equal(viaStorage, viaREST) {
		t.Fatalf("%d rows through the Storage Read API, %d through REST, not the same", len(viaStorage), len(viaREST))
	}
	t.Logf("500,000 rows: Storage Read API %s, REST paging %s", tookStorage.Round(time.Millisecond), tookREST.Round(time.Millisecond))

	proj := os.Getenv("BQT_LIVE_PROJECT")
	if proj == "" {
		return
	}
	list, err := rest.ListDatasets(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	byLoc := map[string][]string{}
	for _, d := range list.Datasets {
		byLoc[d.Location] = append(byLoc[d.Location], d.Name)
	}
	loc := ""
	for l, ds := range byLoc {
		if len(ds) > len(byLoc[loc]) {
			loc = l
		}
	}
	if loc == "" {
		t.Skipf("%s has no datasets", proj)
	}
	req := RegionRequest{Project: proj, Location: loc, Datasets: byLoc[loc]}
	type unit struct {
		tables *api.Snapshot
		cols   []TableColumns
	}
	// Both phases of a region unit; with no column filter, phase 2 reads
	// every table's columns. Where the region views are refused, one
	// dataset's views (BQT_LIVE_STORAGE_DATASET, or the location's first),
	// as sync falls back to. BQT_LIVE_SHARD_PATTERN, when set, filters phase
	// 2 as a shard pattern would, so its WHERE precedes readInOrder.
	filter := ColumnFilter{}
	if pat := os.Getenv("BQT_LIVE_SHARD_PATTERN"); pat != "" {
		filter.Pattern, filter.Keep = "^(?:"+pat+")$", []string{}
	}
	dataset := os.Getenv("BQT_LIVE_STORAGE_DATASET")
	if dataset == "" {
		dataset = slices.Min(byLoc[loc])
	}
	dreq := DatasetRequest{Project: proj, Location: loc, Dataset: dataset}
	regionOK := true
	snapshot := func(c *Client) (unit, time.Duration) {
		start := time.Now()
		var tabs *api.Snapshot
		var cols []TableColumns
		var err error
		if regionOK {
			if tabs, err = c.RegionTables(ctx, req); err == nil {
				cols, err = c.RegionColumns(ctx, req, filter)
			}
			if KindOf(err) == KindPermission {
				t.Logf("no region-scope access, comparing dataset %s.%s: %v", proj, dataset, err)
				regionOK, req.Datasets = false, []string{dataset}
			}
		}
		if !regionOK {
			if tabs, err = c.DatasetTables(ctx, dreq); err == nil {
				cols, err = c.DatasetColumns(ctx, dreq, filter)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		return unit{tabs, cols}, time.Since(start)
	}
	a, tookA := snapshot(storage)
	b, tookB := snapshot(rest)
	// Tables come in no fixed order; each table's columns must match exactly,
	// in order (nested ordinals depend on it).
	for _, u := range []unit{a, b} {
		slices.SortFunc(u.tables.Tables, func(x, y api.TableMeta) int {
			return strings.Compare(x.Dataset+"."+x.Name, y.Dataset+"."+y.Name)
		})
		slices.SortFunc(u.tables.Datasets, func(x, y api.DatasetMeta) int { return strings.Compare(x.Name, y.Name) })
		slices.SortFunc(u.cols, func(x, y TableColumns) int {
			return strings.Compare(x.Dataset+"."+x.Table, y.Dataset+"."+y.Table)
		})
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the region unit of %s/%s differs between the read paths", proj, loc)
	}
	cols := 0
	for _, tc := range a.cols {
		cols += len(tc.Columns)
	}
	t.Logf("%s/%s: %d datasets, %d tables, %d field paths: Storage Read API %s, REST paging %s",
		proj, loc, len(req.Datasets), len(a.tables.Tables), cols, tookA.Round(time.Millisecond), tookB.Round(time.Millisecond))
}

// TestLiveStorageReadFallback checks the fallback on an Execution project
// where read sessions are refused.
func TestLiveStorageReadFallback(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_NO_READ_SESSION_PROJECT")
	if exec == "" {
		t.Skip("set BQT_LIVE_NO_READ_SESSION_PROJECT")
	}
	c, log := liveClient(t, exec, Options{})
	var warns []string
	ctx := WithWarnings(context.Background(), func(msg string) { warns = append(warns, msg) })

	rows, read := liveRows(t, ctx, c)
	if read != readRESTFallback || len(rows) != 500000 {
		t.Fatalf("read %q, %d rows: want all rows through REST after the failed read session", read, len(rows))
	}
	rows, read = liveRows(t, ctx, c)
	if read != readREST || len(rows) != 500000 {
		t.Fatalf("second query: read %q, %d rows", read, len(rows))
	}
	if n := strings.Count(log.String(), "Storage Read API not available"); n != 1 {
		t.Fatalf("%d fallback warnings logged, want 1:\n%s", n, log)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], exec) {
		t.Fatalf("sync warnings %q", warns)
	}
	t.Logf("sync warning: %s", warns[0])

	if proj := os.Getenv("BQT_LIVE_PROJECT"); proj != "" {
		list, err := c.ListDatasets(ctx, proj)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Datasets) > 0 {
			d := list.Datasets[0]
			if _, err := c.RegionColumns(ctx, RegionRequest{Project: proj, Location: d.Location, Datasets: []string{d.Name}}, ColumnFilter{}); err != nil {
				t.Fatalf("region columns through the fallback: %v", err)
			}
		}
	}
}
