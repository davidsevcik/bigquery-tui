package store

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// applyReq turns a synthetic snapshot into one ApplyUnit request.
func applyReq(snap *api.Snapshot) ApplyRequest {
	byDS := map[string][]api.TableMeta{}
	for _, t := range snap.Tables {
		byDS[t.Dataset] = append(byDS[t.Dataset], t)
	}
	req := ApplyRequest{Unit: api.UnitRef{Project: "p", Location: "EU"}}
	now := time.Now()
	req.State = api.UnitState{Status: api.UnitOK, LastAttempt: &now, LastSuccess: &now}
	for _, d := range snap.Datasets {
		req.Datasets = append(req.Datasets, DatasetApply{Meta: d, Tables: byDS[d.Name]})
	}
	return req
}

// TestScaleApply measures the transactional unit swap at the spec's target
// size (50k tables, 1.25M field paths). It is slow, so it runs only with
// BQT_SCALE=1. Run: BQT_SCALE=1 go test ./internal/store -run Scale -v
func TestScaleApply(t *testing.T) {
	if os.Getenv("BQT_SCALE") == "" {
		t.Skip("set BQT_SCALE=1 to run the scale measurement")
	}
	datasets := 500
	if v, err := strconv.Atoi(os.Getenv("BQT_SCALE")); err == nil && v > 1 {
		datasets = v // BQT_SCALE=50 runs a tenth of the size
	}
	snap := bqtest.Synthetic(bqtest.SyntheticSpec{Project: "p", Location: "EU", Datasets: datasets, TablesPerDataset: 100, ColumnsPerTable: 20, NestedPerTable: 5})
	c, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := applyReq(snap)

	start := time.Now()
	st, err := c.ApplyUnit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	n, _ := c.Counts(ctx)
	t.Logf("first apply: %s for %d tables, %d columns (%+v)", first.Round(time.Millisecond), n.Tables, n.Columns, st)

	start = time.Now()
	st, err = c.ApplyUnit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("re-apply with no changes: %s (%+v)", time.Since(start).Round(time.Millisecond), st)
	if st.TablesUnchanged != datasets*100 {
		t.Fatalf("unchanged tables = %d", st.TablesUnchanged)
	}

	// change 1% of tables
	for i := 0; i < len(snap.Tables); i += 100 {
		snap.Tables[i].Description = "changed"
	}
	req = applyReq(snap)
	start = time.Now()
	st, err = c.ApplyUnit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("re-apply with 1%% changed: %s (%+v)", time.Since(start).Round(time.Millisecond), st)
}

func BenchmarkApplyUnitSmall(b *testing.B) {
	snap := bqtest.Synthetic(bqtest.SyntheticSpec{Project: "p", Location: "EU", Datasets: 10, TablesPerDataset: 50, ColumnsPerTable: 20, NestedPerTable: 5})
	req := applyReq(snap)
	c, err := OpenCatalog(filepath.Join(b.TempDir(), "catalog.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.ApplyUnit(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
