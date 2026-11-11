package engine

import (
	"context"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// datasetNodes are proj-a's datasets by name.
func datasetNodes(eng *Engine) map[string]api.BrowseNode {
	out := map[string]api.BrowseNode{}
	for _, n := range eng.Datasets("proj-a") {
		out[n.Name] = n
	}
	return out
}

// Issue 82: a pending dataset is "syncing" only while this process's sync is
// reading its unit, and "not synced" once that unit failed or anywhere else.
func TestPendingDatasetsSyncOnlyWhileTheirUnitIsInFlight(t *testing.T) {
	e := newEnv(t, "proj-a")
	reached, release := make(chan struct{}), make(chan struct{})
	e.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionSnapshot" && c.Location == "US" {
			close(reached)
			<-release
		}
	}
	e.fake.Fail(bqtest.Failure{Method: "RegionSnapshot", Location: "EU", Err: bqtest.TransientError("down")})
	rw := e.open(false)
	h, err := rw.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	for ev := range h.Events() {
		if ev.Kind == api.EventUnitFailed && ev.Location == "EU" {
			break
		}
	}
	if err := rw.ReloadIndex(ctx); err != nil {
		t.Fatal(err)
	}
	ds := datasetNodes(rw)
	if ds["us_data"].Status != api.DatasetPending || !ds["us_data"].Syncing {
		t.Fatalf("the US unit is in flight: %+v", ds["us_data"])
	}
	if ds["sales"].Status != api.DatasetPending || ds["sales"].Syncing || ds["sales"].Error == "" {
		t.Fatalf("the EU unit failed: %+v", ds["sales"])
	}
	r, _ := rw.Search(ctx, "us_data", api.SearchOptions{})
	if len(r.Results) != 1 || !r.Results[0].Pending || !r.Results[0].Syncing {
		t.Fatalf("%+v", r.Results)
	}

	// Another process never sees "syncing": sync events reach only the writer.
	ro := e.open(true)
	if n := datasetNodes(ro)["us_data"]; n.Status != api.DatasetPending || n.Syncing {
		t.Fatalf("%+v", n)
	}
	hits, err := ro.CatalogSearch(ctx, "sales", 10, 0)
	if err != nil || len(hits.Hits) != 1 || !hits.Hits[0].Pending {
		t.Fatalf("%+v %v", hits, err)
	}
	st, _ := ro.Status(ctx)
	if p := st.Projects[0]; p.Pending != 2 || !p.Stale || p.SyncedAt != nil {
		t.Fatalf("%+v", p)
	}

	close(release)
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	ds = datasetNodes(rw)
	if ds["us_data"].Status != api.DatasetOK || ds["us_data"].Children != 1 {
		t.Fatalf("%+v", ds["us_data"])
	}
	if ds["sales"].Status != api.DatasetPending || ds["sales"].Syncing {
		t.Fatalf("%+v", ds["sales"])
	}
}

// Issue 82: a cancelled sync records no unit state for the locations it did
// not reach. Their pending datasets still make the project unsynced, so the
// next start refreshes it.
func TestAProjectWithPendingDatasetsInAnUnsyncedLocationNeedsASync(t *testing.T) {
	e := newEnv(t, "proj-a")
	rw := e.open(false)
	h, _ := rw.StartSync(ctx, api.SyncScope{})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	p := e.fake.Projects["proj-a"]
	p.Datasets = append(p.Datasets, bqtest.DS("big", "asia-east1", bqtest.Table("b", "x")))
	e.now = e.now.Add(time.Hour)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionSnapshot" && c.Location == "asia-east1" {
			cancel()
		}
	}
	h2, _ := rw.StartSync(cctx, api.SyncScope{})
	if res, err := h2.Wait(); err != nil || !res.Cancelled {
		t.Fatalf("%+v %v", res, err)
	}
	st, _ := rw.Status(ctx)
	if ps := st.Projects[0]; ps.Pending != 1 || !ps.Stale || ps.SyncedAt != nil {
		t.Fatalf("%+v", ps)
	}
	if need, _ := rw.NeedsSync(ctx, []string{"proj-a"}); len(need) != 1 {
		t.Fatalf("%v", need)
	}
	if n := datasetNodes(rw)["big"]; n.Status != api.DatasetPending || n.Syncing || n.Location != "asia-east1" {
		t.Fatalf("%+v", n)
	}
}

// A committed unit's datasets are pending in the search index until it
// reloads (the catalog watcher does that within a second). They must read
// "syncing" in that gap, not "not synced" as if their unit had failed.
func TestACommittedUnitIsNotShownNotSyncedBeforeTheIndexReloads(t *testing.T) {
	e := newEnv(t, "proj-a")
	eu, us := make(chan struct{}), make(chan struct{})
	e.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionSnapshot" && c.Location == "EU" {
			<-eu
		}
		if c.Method == "RegionSnapshot" && c.Location == "US" {
			<-us
		}
	}
	rw := e.open(false)
	h, err := rw.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range h.Events() {
		if ev.Kind == api.EventDiscovered {
			break
		}
	}
	// The watcher picks up the listing: the datasets show pending.
	if err := rw.ReloadIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if n := datasetNodes(rw)["sales"]; n.Status != api.DatasetPending || !n.Syncing {
		t.Fatalf("listed, before its unit: %+v", n)
	}
	close(eu)
	for ev := range h.Events() {
		if ev.Kind == api.EventUnitDone && ev.Location == "EU" {
			break
		}
	}
	if n := datasetNodes(rw)["sales"]; n.Status == api.DatasetPending && !n.Syncing {
		t.Fatalf("the EU unit committed, but its dataset reads not synced: %+v", n)
	}
	close(us)
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if n := datasetNodes(rw)["sales"]; n.Status != api.DatasetOK || n.Syncing {
		t.Fatalf("after the run: %+v", n)
	}
}
