package bqtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

func sampleFake() *Fake {
	f := New()
	p := f.AddProject("proj")
	view := Table("v")
	view.Type = api.TypeView
	view.ViewSQL = "SELECT 1"
	nested := Table("orders", "id", "addr", "addr.city")
	p.Datasets = []Dataset{
		DS("sales", "EU", nested, Table("items", "sku"), view, Table("events_20240101", "e"), Table("events_20240102", "e"), Table("events_20240103", "e")),
		DS("logs", "EU", Table("l1", "msg")),
		DS("us_data", "US", Table("u1", "x")),
	}
	return f
}

func TestFakeSatisfiesContract(t *testing.T) {
	RunMetadataContract(t, sampleFake(), ContractOptions{Project: "proj", Family: &FamilyCase{
		Location: "EU", Dataset: "sales", Patterns: []string{`.+(\d{8})`}, Keep: []string{"events_20240103"},
	}})
}

func TestRegionTablesFiltersToRequestedAndLocation(t *testing.T) {
	f := sampleFake()
	f.Projects["proj"].Leak = []Dataset{DS("ghost", "EU", Table("t", "c"))}
	s, err := f.RegionTables(context.Background(), bq.RegionRequest{Project: "proj", Location: "eu", Datasets: []string{"sales"}})
	if err != nil {
		t.Fatal(err)
	}
	// The fake leaks unrequested datasets the way region views do; callers must filter.
	names := map[string]bool{}
	for _, d := range s.Datasets {
		names[d.Name] = true
	}
	if !names["sales"] || !names["ghost"] || names["logs"] || names["us_data"] {
		t.Fatalf("%v", names)
	}
}

func TestFailureInjection(t *testing.T) {
	f := sampleFake()
	boom := TransientError("boom")
	f.Fail(Failure{Method: "RegionTables", Project: "proj", Location: "EU", Err: boom, Times: 2})
	req := bq.RegionRequest{Project: "proj", Location: "EU", Datasets: []string{"sales"}}
	for i := 0; i < 2; i++ {
		if _, err := f.RegionTables(context.Background(), req); !errors.Is(err, boom) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := f.RegionTables(context.Background(), req); err != nil {
		t.Fatalf("failure should be exhausted: %v", err)
	}
	if n := len(f.CallsTo("RegionTables")); n != 3 {
		t.Fatalf("calls recorded: %d", n)
	}
}

func TestDeniedDatasetLadder(t *testing.T) {
	f := sampleFake()
	f.Projects["proj"].Datasets[0].Denied = true
	ctx := context.Background()
	if _, err := f.RegionTables(ctx, bq.RegionRequest{Project: "proj", Location: "EU", Datasets: []string{"sales", "logs"}}); bq.KindOf(err) != bq.KindPermission {
		t.Fatalf("%v", err)
	}
	if _, err := f.DatasetTables(ctx, bq.DatasetRequest{Project: "proj", Location: "EU", Dataset: "sales"}); bq.KindOf(err) != bq.KindPermission {
		t.Fatalf("%v", err)
	}
	if _, err := f.ListTableNames(ctx, "proj", "sales"); bq.KindOf(err) != bq.KindPermission {
		t.Fatalf("%v", err)
	}
	f.Projects["proj"].Datasets[0].NamesOK = true
	names, err := f.ListTableNames(ctx, "proj", "sales")
	if err != nil || len(names) != 6 || !names[0].NamesOnly {
		t.Fatalf("%v %v", names, err)
	}
}

func TestDelayRespectsCancellation(t *testing.T) {
	f := sampleFake()
	f.Delay = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.ListDatasets(ctx, "proj")
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestFakeJobsContract(t *testing.T) {
	f := New()
	ContractScripts(f)
	RunJobsContract(t, f, JobsContractOptions{})
}
