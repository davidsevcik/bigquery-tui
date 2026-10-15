package bqtest

import (
	"context"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// The contract queries read no tables, so they cost nothing to run for real.
const (
	ContractSelect = "SELECT n, CAST(n AS STRING) AS label FROM UNNEST(GENERATE_ARRAY(0, 1199)) AS n ORDER BY n"
	ContractBroken = "SELECT\n  FROM nothing_here WHERE"
	ContractParams = "SELECT @d AS d, @s AS s, @n AS n, @missing AS missing"
	ContractSlow   = "SELECT COUNT(*) FROM UNNEST(GENERATE_ARRAY(1, 1000000)) a, UNNEST(GENERATE_ARRAY(1, 3000)) b"
	ContractScript = "DECLARE x INT64 DEFAULT 1; SELECT x"
)

// JobsContractOptions tunes RunJobsContract.
type JobsContractOptions struct {
	Location string
	// SkipCancel leaves out the cancellation case.
	SkipCancel bool
}

// ContractScripts makes a Fake answer the contract queries the way BigQuery does.
func ContractScripts(f *Fake) {
	block := make(chan struct{})
	f.OnQuery = func(sql string) QueryScript {
		switch sql {
		case ContractSelect:
			s := Numbers(1200)
			for _, r := range s.Rows {
				r[1] = r[0]
			}
			return s
		case ContractBroken:
			return QueryScript{DryRunErr: QueryError("Syntax error: Unexpected keyword FROM at [2:3]")}
		case ContractParams:
			return QueryScript{Schema: []bq.Field{{Name: "d", Type: "DATE"}, {Name: "s", Type: "STRING"}, {Name: "n", Type: "INTEGER"}, {Name: "missing", Type: "STRING"}},
				Rows: [][]any{{"2024-02-29", "it's", "42", nil}}}
		case ContractSlow:
			return QueryScript{Block: block}
		case ContractScript:
			return QueryScript{StatementType: "SCRIPT"}
		}
		return Numbers(1)
	}
}

// RunJobsContract checks a bq.Jobs implementation: the fake in every build,
// the real client under the live tag.
func RunJobsContract(t *testing.T, jobs bq.Jobs, o JobsContractOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req := func(sql string) bq.QueryRequest {
		return bq.QueryRequest{SQL: sql, Location: o.Location, MaxBytesBilled: 1 << 30, Labels: map[string]string{"bqt": "1", "bqt_source": "contract"}}
	}

	t.Run("dry-run reports the statement type and schema", func(t *testing.T) {
		dr, err := jobs.DryRun(ctx, req(ContractSelect))
		if err != nil {
			t.Fatal(err)
		}
		if dr.StatementType != "SELECT" || len(dr.Schema) != 2 || dr.Schema[0].Name != "n" || dr.Accuracy == "" {
			t.Fatalf("%+v", dr)
		}
		t.Logf("dry-run: %d bytes, accuracy %s, reservation %q, edition %q, location %q", dr.BytesProcessed, dr.Accuracy, dr.ReservationID, dr.Edition, dr.Location)
		script, err := jobs.DryRun(ctx, req(ContractScript))
		if err != nil || script.StatementType != "SCRIPT" {
			t.Fatalf("a script must not classify as SELECT: %+v %v", script, err)
		}
	})

	t.Run("a syntax error carries its position", func(t *testing.T) {
		_, err := jobs.DryRun(ctx, req(ContractBroken))
		if err == nil {
			t.Fatal("expected an error")
		}
		qe := bq.QueryErrorOf(err)
		if qe.Line != 2 || qe.Col == 0 || qe.Kind != "query" || qe.Message == "" {
			t.Fatalf("%+v (raw: %v)", qe, err)
		}
	})

	t.Run("run, wait and page", func(t *testing.T) {
		ref, err := jobs.StartQuery(ctx, req(ContractSelect))
		if err != nil {
			t.Fatal(err)
		}
		if ref.ID == "" || ref.Location == "" || ref.Project == "" {
			t.Fatalf("%+v", ref)
		}
		res, err := jobs.WaitJob(ctx, ref)
		if err != nil || res.Err != nil {
			t.Fatalf("%v %v", err, res.Err)
		}
		if res.StatementType != "SELECT" || res.Ended.IsZero() {
			t.Fatalf("%+v", res)
		}
		p, err := jobs.QueryRows(ctx, ref, 0, 500)
		if err != nil || len(p.Rows) != 500 || p.TotalRows != 1200 || len(p.Schema) != 2 || p.Rows[0][0] != "0" || p.Rows[499][1] != "499" {
			t.Fatalf("%v rows=%d total=%d", err, len(p.Rows), p.TotalRows)
		}
		p, err = jobs.QueryRows(ctx, ref, 1000, 500)
		if err != nil || len(p.Rows) != 200 || p.Rows[0][0] != "1000" {
			t.Fatalf("a page can start anywhere: %v rows=%d", err, len(p.Rows))
		}
		p, err = jobs.QueryRows(ctx, ref, 1200, 500)
		if err != nil || len(p.Rows) != 0 {
			t.Fatalf("past the end is an empty page: %v rows=%d", err, len(p.Rows))
		}
	})

	t.Run("typed parameters", func(t *testing.T) {
		r := req(ContractParams)
		r.Params = []api.QueryParam{{Name: "d", Type: "DATE", Value: "2024-02-29"}, {Name: "s", Type: "STRING", Value: "it's"},
			{Name: "n", Type: "INT64", Value: "42"}, {Name: "missing", Type: "STRING", Null: true}}
		ref, err := jobs.StartQuery(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := jobs.WaitJob(ctx, ref); err != nil || res.Err != nil {
			t.Fatalf("%v %v", err, res.Err)
		}
		p, err := jobs.QueryRows(ctx, ref, 0, 10)
		if err != nil || len(p.Rows) != 1 {
			t.Fatalf("%v %+v", err, p)
		}
		row := p.Rows[0]
		if row[0] != "2024-02-29" || row[1] != "it's" || row[2] != "42" || row[3] != nil || p.Schema[0].Type != "DATE" {
			t.Fatalf("%+v %+v", row, p.Schema)
		}
	})

	if o.SkipCancel {
		return
	}
	t.Run("cancel", func(t *testing.T) {
		ref, err := jobs.StartQuery(ctx, req(ContractSlow))
		if err != nil {
			t.Fatal(err)
		}
		if err := jobs.CancelJob(ctx, ref); err != nil {
			t.Fatal(err)
		}
		res, err := jobs.WaitJob(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if res.Err == nil {
			t.Skip("the job finished before the cancel took effect")
		}
		if qe := bq.QueryErrorOf(res.Err); qe.Kind != "cancelled" {
			t.Fatalf("a cancelled job must classify as cancelled: %+v (raw %v)", qe, res.Err)
		}
	})
}
