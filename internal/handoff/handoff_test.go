package handoff

import (
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func TestSchemaMarkdown(t *testing.T) {
	s := api.TableSchema{Table: "p.d.events_20240102", Type: api.TypeTable, Location: "EU", Description: "Raw\nevents",
		Shard: &api.ShardFamily{Prefix: "events_", First: "20240101", Last: "20240102", Count: 2},
		Columns: []api.SchemaColumn{
			{Name: "user", Type: "STRUCT<id INT64>", Description: "who | what"},
			{Name: "user.id", Type: "INT64", Required: true},
			{Name: "d", Type: "DATE", Partition: true, Clustering: 1},
		}}
	got := SchemaMarkdown(s)
	for _, want := range []string{"## `p.d.events_20240102` (table, EU)", "`events_*` has 2 tables", "Raw events",
		"| user | STRUCT<id INT64> | who \\| what |", "| user.id | INT64 | (REQUIRED) |", "| d | DATE | (partitioning, clustering 1) |"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if j := SchemaJSON(s); !strings.Contains(j, `"name": "user.id"`) || strings.Contains(j, "\x1b") {
		t.Fatal(j)
	}
}

func TestQueryErrorShowsTheLine(t *testing.T) {
	got := QueryError("select\n  nope from t", api.QueryError{Message: "Unrecognized name: nope at [2:3]", Line: 2, Col: 3}, "exec", "EU")
	for _, want := range []string{"Execution project exec, location EU", "at line 2, column 3", "Unrecognized name", "```sql\nselect\n  nope from t\n```", "Line 2: `nope from t`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestSampleFormats(t *testing.T) {
	cols := []api.PreviewColumn{{Name: "n", Type: "INTEGER"}, {Name: "s", Type: "STRING"}}
	var rows [][]api.Cell
	for i := 0; i < 30; i++ {
		rows = append(rows, []api.Cell{{Text: "1"}, {Text: "a|b"}})
	}
	rows[0][1] = api.Cell{Null: true}
	md := Sample(cols, rows, Markdown)
	if n := strings.Count(md, "\n"); n != SampleRows+2 || !strings.Contains(md, "| 1 | NULL |") || !strings.Contains(md, `a\|b`) {
		t.Fatalf("%d lines\n%s", n, md)
	}
	nd := Sample(cols, rows, NDJSON)
	if !strings.HasPrefix(nd, `{"n":1,"s":null}`+"\n") || strings.Count(nd, "\n") != SampleRows {
		t.Fatal(nd)
	}
	if tsv := Sample(cols, rows[:2], TSV); !strings.HasPrefix(tsv, "n\ts\n") {
		t.Fatal(tsv)
	}
}

func TestRunSummary(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(1500 * time.Millisecond)
	usd := 0.01
	got := Run(api.RunInfo{SQL: "select 1", State: api.RunDone, ExecutionProject: "exec", Location: "EU",
		Estimate: &api.Estimate{StatementType: "SELECT", Bytes: 2048, Accuracy: "PRECISE", ApproxUSD: &usd},
		Job:      &api.JobRef{Project: "exec", Location: "EU", ID: "j1"}, Stats: &api.RunStats{BytesProcessed: 2048},
		HasResult: true, TotalRows: 1, Columns: []api.PreviewColumn{{Name: "f0_", Type: "INTEGER"}}, Started: &start, Ended: &end,
		Params: []api.QueryParam{{Name: "x", Type: "INT64", Null: true}}})
	for _, want := range []string{"Run: done (Execution project exec, location EU)", "SELECT 2.0 KiB", "Job: exec:EU.j1", "1 rows, columns f0_ INTEGER", "Took 1.5s", "@x INT64 = NULL", "```sql\nselect 1\n```"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}
