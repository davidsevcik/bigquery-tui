//go:build live

package bq_test

import (
	"context"
	"os"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
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
