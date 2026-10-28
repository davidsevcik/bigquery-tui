//go:build live

package bq_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// Run with:
//
//	BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_PROJECT=... go test -tags live ./internal/bq -run Live -v
//
// It only reads metadata (INFORMATION_SCHEMA queries, each billing the 10 MiB
// minimum) and never writes to the project.
func TestLiveMetadataContract(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	proj := os.Getenv("BQT_LIVE_PROJECT")
	if exec == "" || proj == "" {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT and BQT_LIVE_PROJECT")
	}
	c, err := bq.NewClient(context.Background(), exec, bq.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bqtest.RunMetadataContract(t, c, bqtest.ContractOptions{Project: proj, MaxDatasets: 40, MaxLocations: 2, FallbackSamples: 3})
}

// TestLiveJobsContract runs a handful of queries that read no tables.
func TestLiveJobsContract(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	if exec == "" {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT")
	}
	c, err := bq.NewClient(context.Background(), exec, bq.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bqtest.RunJobsContract(t, c, bqtest.JobsContractOptions{Location: os.Getenv("BQT_LIVE_LOCATION")})
}

// TestLiveListProjectsNeedsNoExecutionProject lists the credentials'
// projects with a client built without an Execution project, as the
// Projects dialog does on a fresh install.
func TestLiveListProjectsNeedsNoExecutionProject(t *testing.T) {
	proj := os.Getenv("BQT_LIVE_PROJECT")
	if proj == "" {
		t.Skip("set BQT_LIVE_PROJECT")
	}
	c, err := bq.NewClient(context.Background(), "", bq.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ps, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		found = found || p.ID == proj
	}
	if !found {
		t.Fatalf("%s is not listed among %d projects", proj, len(ps))
	}
	t.Logf("%d projects listed", len(ps))
}

// TestLivePartitionFilterMetadata checks what sync reads for tables that
// require a partition filter, and that the s shortcut's SELECT passes a
// dry-run on them (dry-runs are free; nothing is run or written):
//
//	BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_FILTER_TABLE=p.d.t BQT_LIVE_INGESTION_TABLE=p.d.t \
//	  go test -tags live ./internal/bq -run TestLivePartitionFilterMetadata -v
//
// BQT_LIVE_FILTER_TABLE is column-partitioned and requires a filter;
// BQT_LIVE_INGESTION_TABLE is partitioned by ingestion time and requires one.
func TestLivePartitionFilterMetadata(t *testing.T) {
	exec := os.Getenv("BQT_LIVE_EXECUTION_PROJECT")
	filterT, ingestT := os.Getenv("BQT_LIVE_FILTER_TABLE"), os.Getenv("BQT_LIVE_INGESTION_TABLE")
	if exec == "" || (filterT == "" && ingestT == "") {
		t.Skip("set BQT_LIVE_EXECUTION_PROJECT and BQT_LIVE_FILTER_TABLE and/or BQT_LIVE_INGESTION_TABLE")
	}
	ctx := context.Background()
	c, err := bq.NewClient(ctx, exec, bq.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct {
		name, path string
		ingestion  bool
	}{{"column partitioned", filterT, false}, {"ingestion time", ingestT, true}} {
		if tc.path == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			parts := strings.Split(tc.path, ".")
			if len(parts) != 3 {
				t.Fatalf("%q is not project.dataset.table", tc.path)
			}
			p := api.ObjectPath{Project: parts[0], Dataset: parts[1], Table: parts[2]}
			list, err := c.ListDatasets(ctx, p.Project)
			if err != nil {
				t.Fatal(err)
			}
			loc := ""
			for _, d := range list.Datasets {
				if d.Name == p.Dataset {
					loc = d.Location
				}
			}
			if loc == "" {
				t.Fatalf("dataset %s.%s not listed", p.Project, p.Dataset)
			}
			snap, err := c.DatasetSnapshot(ctx, bq.DatasetRequest{Project: p.Project, Location: loc, Dataset: p.Dataset})
			if err != nil {
				t.Fatal(err)
			}
			var tm *api.TableMeta
			for i := range snap.Tables {
				if snap.Tables[i].Name == p.Table {
					tm = &snap.Tables[i]
				}
			}
			if tm == nil {
				t.Fatalf("%s not in the snapshot", tc.path)
			}
			if !tm.RequirePartitionFilter {
				t.Errorf("RequirePartitionFilter is not set for %s", tc.path)
			}
			var partitioning []string
			for _, col := range tm.Columns {
				if col.FieldPath == "_PARTITIONTIME" || col.FieldPath == "_PARTITIONDATE" {
					t.Logf("pseudo-column %s is listed (partitioning=%v)", col.FieldPath, col.Partitioning)
				}
				if col.Partitioning {
					partitioning = append(partitioning, col.FieldPath)
				}
			}
			t.Logf("partitioning columns: %v", partitioning)
			ordinary := slices.DeleteFunc(slices.Clone(partitioning), func(n string) bool { return n == "_PARTITIONTIME" || n == "_PARTITIONDATE" })
			switch {
			case tc.ingestion && len(ordinary) > 0:
				t.Errorf("an ingestion-time table has an ordinary partitioning column %v: the _PARTITIONTIME fallback would not apply", ordinary)
			case !tc.ingestion && len(ordinary) == 0:
				t.Errorf("no partitioning column for a column-partitioned table")
			}
			text, _ := sqlx.SelectStar(api.TableDetail{Path: p, Table: *tm})
			t.Logf("SELECT:\n%s", text)
			if _, err := c.DryRun(ctx, bq.QueryRequest{SQL: text, Location: loc}); err != nil {
				if strings.Contains(err.Error(), "partition elimination") {
					t.Fatalf("the SELECT lacks a usable partition filter: %v", err)
				}
				t.Fatalf("dry-run: %v", err)
			}
		})
	}
}
