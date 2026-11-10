//go:build live

package bq

import (
	"context"
	"os"
	"testing"

	bqapi "cloud.google.com/go/bigquery"

	"github.com/davidsevcik/bigquery-tui/internal/shard"
)

// TestLiveShardRegexParity checks that BigQuery's REGEXP_CONTAINS agrees with
// Go's regexp on shard patterns, anchored as bqt anchors them. Sync leaves
// collapsed members out of the column queries on BigQuery's side, so a
// construct the two read differently would cost a re-read (the safety net);
// one BigQuery rejects would fail the column queries. Either must fail here,
// and config validation must then refuse the construct.
//
//	BQT_LIVE_EXECUTION_PROJECT=... go test -tags live ./internal/bq -run TestLiveShardRegexParity -v
//
// One query over UNNEST parameters: no INFORMATION_SCHEMA access, and it
// bills the 10 MB minimum. BQT_LIVE_LOCATION sets the job location.
func TestLiveShardRegexParity(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	if exec == "" {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT")
	}
	cases := []struct {
		pattern string
		names   []string
	}{
		{`.+(\d{8})`, []string{"events_20240101", "20240101", "events_2024010", "orders_0123456789abcdef01234567", "events_20240101_x"}},
		{`.+_([0-9a-f]{24})(?:_history)?`, []string{"orders_0123456789abcdef01234567", "orders_0123456789abcdef01234567_history", "orders_0123456789ABCDEF01234567", "orders_0123456789abcdef0123456", "orders_0123456789abcdef01234567_hist"}},
		{`.+_([0-9a-f]{8}(?:[-_][0-9a-f]{4}){3}[-_][0-9a-f]{12})`, []string{"sessions_123e4567-e89b-12d3-a456-426614174000", "sessions_123e4567_e89b_12d3_a456_426614174000", "sessions_123e4567e89b12d3a456426614174000"}},
		{`.+_(\d+)`, []string{"customer_123", "customer_", "customer_12a", "a_b_7"}},
		{`(?:orders|reviews)_(\d+)`, []string{"orders_1", "reviews_22", "returns_3", "xorders_1"}},
		{`(?i)orders_([a-f0-9]+)`, []string{"ORDERS_ABC", "Orders_abc", "orders_xyz"}},
		{`t_(\p{N}+)`, []string{"t_123", "t_١٢٣", "t_x"}},
		{`a\.b_(\d+)`, []string{"a.b_1", "axb_1"}},
		{`a\_b(\d+)`, []string{"a_b1", "axb1"}},
		{`x(\d{2,4})`, []string{"x1", "x12", "x12345"}},
	}
	var names, res []string
	var want []bool
	for _, c := range cases {
		p, errs := shard.Compile([]string{c.pattern})
		if len(errs) > 0 {
			t.Fatalf("%q: %v", c.pattern, errs)
		}
		for _, n := range c.names {
			names = append(names, n)
			res = append(res, p.Combined())
			want = append(want, p.MatchesAny(n))
		}
	}
	cl, err := NewClient(context.Background(), exec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sql := `SELECT off, REGEXP_CONTAINS(n, r)
FROM UNNEST(@names) AS n WITH OFFSET off
JOIN UNNEST(@res) AS r WITH OFFSET off2 ON off = off2`
	got := map[int64]bool{}
	err = cl.query(context.Background(), os.Getenv("BQT_LIVE_LOCATION"), sql,
		[]bqapi.QueryParameter{{Name: "names", Value: names}, {Name: "res", Value: res}},
		func(r []bqapi.Value) error {
			b, _ := r[1].(bool)
			got[num(r[0])] = b
			return nil
		})
	if err != nil {
		t.Fatalf("BigQuery refused a pattern (config validation must then refuse the construct): %v", err)
	}
	for i, n := range names {
		if g, ok := got[int64(i)]; !ok || g != want[i] {
			t.Errorf("%q on %q: BigQuery says %v, Go says %v", res[i], n, g, want[i])
		}
	}
}
