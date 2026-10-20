package search

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// synthetic builds the spec's target catalog: 50k tables, 1.3M field paths.
func synthetic(datasets int) (*Searcher, int) {
	snap := bqtest.Synthetic(bqtest.SyntheticSpec{Project: "acme-prod", Location: "EU", Datasets: datasets, TablesPerDataset: 100, ColumnsPerTable: 20, NestedPerTable: 5})
	idx := &store.IndexSnapshot{Projects: []string{"acme-prod"}}
	dsID := map[string]int64{}
	for i, d := range snap.Datasets {
		dsID[d.Name] = int64(i + 1)
		idx.Datasets = append(idx.Datasets, store.IndexDataset{ID: int64(i + 1), DatasetMeta: d, Status: api.DatasetOK})
	}
	cols := &fakeCols{cols: map[int64][]api.ColumnMeta{}}
	n := 0
	for i, t := range snap.Tables {
		id := int64(1_000_000 + i)
		cols.cols[id] = t.Columns
		n += len(t.Columns)
		t.Columns = nil
		idx.Tables = append(idx.Tables, store.IndexTable{ID: id, DatasetID: dsID[t.Dataset], TableMeta: t})
	}
	s := New(BuildIndex(idx))
	s.Columns = cols
	return s, n
}

var benchQueries = []string{
	"t", "ta", "table", "table_0042", "tab 0042 017", "0042_017", "tbl", "t42", "zzz", "synthetic", "number 42",
	"dataset_0100", "d:dataset_00 type:table", "table p:acme -d:0499", "acme-prod.dataset_0042.table_0042_017",
}

var colQueries = []string{"col:column_07", "col:field", "col:payload.field_03 d:dataset_01", "col:id"}

func percentile(d []time.Duration, p float64) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func measure(t *testing.T, s *Searcher, queries []string, rounds int) (p50, p95, worst time.Duration, slowest string) {
	t.Helper()
	return measureFn(t, queries, rounds, func(q string) error {
		_, err := s.Search(ctx, q, api.SearchOptions{Limit: 50})
		return err
	})
}

// filterScopes are the Explorer filter's two shapes: the whole tree, and one
// Miller column (a dataset's tables).
var filterScopes = []api.FilterOptions{
	{},
	{Scope: &api.ObjectPath{Project: "acme-prod", Dataset: "dataset_0042"}, Kinds: []api.ResultKind{api.KindTable, api.KindShards}},
}

func measureFilter(t *testing.T, s *Searcher, queries []string, rounds int) (p50, p95, worst time.Duration, slowest string) {
	t.Helper()
	return measureFn(t, queries, rounds, func(q string) error {
		for _, o := range filterScopes {
			if _, err := s.Filter(ctx, q, o); err != nil {
				return err
			}
		}
		return nil
	})
}

func measureFn(t *testing.T, queries []string, rounds int, run func(string) error) (p50, p95, worst time.Duration, slowest string) {
	t.Helper()
	var all []time.Duration
	for r := 0; r < rounds; r++ {
		for _, q := range queries {
			st := time.Now()
			if err := run(q); err != nil {
				t.Fatal(err)
			}
			d := time.Since(st)
			all = append(all, d)
			if d > worst {
				worst, slowest = d, q
			}
		}
	}
	return percentile(all, 0.5), percentile(all, 0.95), worst, slowest
}

// TestSearchLatencySmoke is the CI guard: at the target catalog size a
// keystroke must stay well under 5x the 30 ms budget. It catches
// order-of-magnitude regressions without being flaky on slow runners.
// The race detector slows the search path several times over, so CI runs
// this in its own step without -race.
func TestSearchLatencySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 50k-table catalog")
	}
	if raceEnabled {
		t.Skip("wall-clock guard is meaningless under the race detector")
	}
	s, _ := synthetic(500)
	_, p95, worst, slowest := measure(t, s, benchQueries, 3)
	if p95 > 150*time.Millisecond {
		t.Fatalf("p95 %s exceeds the 150 ms guard (worst %s on %q)", p95, worst, slowest)
	}
	_, p95, worst, slowest = measureFilter(t, s, benchQueries, 3)
	if p95 > 2*150*time.Millisecond { // two filter queries per round: tree and column
		t.Fatalf("filter p95 %s exceeds the 300 ms guard (worst %s on %q)", p95, worst, slowest)
	}
}

// TestSearchLatencyReport prints the numbers behind the 30 ms budget.
// Run: BQT_SCALE=1 go test ./internal/search -run LatencyReport -v
func TestSearchLatencyReport(t *testing.T) {
	if os.Getenv("BQT_SCALE") == "" {
		t.Skip("set BQT_SCALE=1")
	}
	st := time.Now()
	s, ncols := synthetic(500)
	t.Logf("index of 50,000 tables built in %s", time.Since(st).Round(time.Millisecond))
	p50, p95, worst, slowest := measure(t, s, benchQueries, 5)
	t.Logf("name/filter queries: p50 %s, p95 %s, worst %s on %q", p50.Round(100*time.Microsecond), p95.Round(100*time.Microsecond), worst.Round(100*time.Microsecond), slowest)
	for _, o := range filterScopes {
		p50, p95, worst, slowest = measureFn(t, benchQueries, 5, func(q string) error { _, err := s.Filter(ctx, q, o); return err })
		t.Logf("explorer filter (scope %v): p50 %s, p95 %s, worst %s on %q", o.Scope, p50.Round(100*time.Microsecond), p95.Round(100*time.Microsecond), worst.Round(100*time.Microsecond), slowest)
	}

	st = time.Now()
	if err := s.WarmColumns(ctx); err != nil {
		t.Fatal(err)
	}
	_, cb, _ := s.columns(ctx)
	t.Logf("column data for %d field paths built in %s (%d MB)", ncols, time.Since(st).Round(time.Millisecond), len(cb.text)>>20)
	p50, p95, worst, slowest = measure(t, s, colQueries, 5)
	t.Logf("col: queries: p50 %s, p95 %s, worst %s on %q", p50.Round(100*time.Microsecond), p95.Round(100*time.Microsecond), worst.Round(100*time.Microsecond), slowest)
	p50, p95, worst, slowest = measureFn(t, colQueries, 5, func(q string) error { _, err := s.Filter(ctx, q, api.FilterOptions{}); return err })
	t.Logf("col: explorer filter (tree): p50 %s, p95 %s, worst %s on %q", p50.Round(100*time.Microsecond), p95.Round(100*time.Microsecond), worst.Round(100*time.Microsecond), slowest)
	for _, q := range append(append([]string{}, benchQueries...), colQueries...) {
		st := time.Now()
		r, _ := s.Search(ctx, q, api.SearchOptions{Limit: 50})
		t.Logf("  %-45s %8s  total=%d", strings.TrimSpace(q), time.Since(st).Round(100*time.Microsecond), r.Total)
	}
}

func BenchmarkSearchPrefixAll(b *testing.B) {
	s, _ := synthetic(500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Search(ctx, "table", api.SearchOptions{Limit: 50})
	}
}
