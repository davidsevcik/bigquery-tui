package tui

import (
	"context"
	gosync "sync"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

// pendingSync refreshes acme-prod after BigQuery started listing two more
// datasets: ml_features in a new region, whose unit it holds in flight, and
// raw_landing in US, whose unit fails. It returns once the other units are
// done; release lets the held unit finish.
func pendingSync(t *testing.T, r *rig) (h *engine.SyncHandle, release func()) {
	t.Helper()
	p := r.fake.Projects["acme-prod"]
	ml := bqtest.DS("ml_features", "asia-northeast1", bqtest.Table("embeddings", "id", "vector"), bqtest.Table("labels", "id"))
	ml.Meta.FriendlyName = "ML feature store"
	ml.Meta.Labels = map[string]string{"team": "ml", "tier": "gold"}
	p.Datasets = append(p.Datasets, ml, bqtest.DS("raw_landing", "US", bqtest.Table("drops", "id")))
	r.fake.Fail(bqtest.Failure{Method: "RegionTables", Project: "acme-prod", Location: "US", Err: bqtest.TransientError("region views unavailable")})
	reached, hold := make(chan struct{}), make(chan struct{})
	var once gosync.Once
	r.fake.OnCall = func(c bqtest.Call) {
		if c.Method == "RegionTables" && c.Location == "asia-northeast1" {
			once.Do(func() { close(reached) })
			<-hold // released: later syncs pass
		}
	}
	h, err := r.eng.StartSync(context.Background(), api.SyncScope{Projects: []string{"acme-prod"}})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	finished := 0
	var last api.SyncEvent
	for ev := range h.Events() {
		last = ev
		if ev.Kind == api.EventUnitDone || ev.Kind == api.EventUnitFailed {
			if finished++; finished == 2 {
				break
			}
		}
	}
	r.m.syncing, r.m.syncDone, r.m.syncTotal = true, last.UnitsDone, last.UnitsTotal
	r.catalogChanged()
	return h, func() { close(hold) }
}

// catalogChanged is what the catalog watcher does after a commit.
func (r *rig) catalogChanged() {
	r.t.Helper()
	if err := r.eng.ReloadIndex(context.Background()); err != nil {
		r.t.Fatal(err)
	}
	ch := make(chan struct{})
	close(ch)
	r.send(catalogWait{ch})
}

// Issue 82: listed datasets show before their unit syncs, flagged syncing
// while it is in flight and not synced once it failed, in every Explorer
// style; the Schema pane says which and why.
func TestGoldenPendingDatasets(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{120, 36}, {80, 24}, {60, 20}} {
		name := func(s string) string { return s + "_" + itoa(sz.w) + "x" + itoa(sz.h) }
		r := newRig(t, sz.w, sz.h)
		h, release := pendingSync(t, r)
		r.press("l", "j", "j") // the datasets of acme-prod: ml_features
		if sz.w == 120 {
			r.contains("⟳ syncing", "● not synced", "ML feature store", "labels team:ml tier:gold")
		}
		r.golden(name("pending_columns"))
		r.press("j") // raw_landing
		r.golden(name("pending_schema_not_synced"))
		r.press("L")
		r.golden(name("pending_tree"))
		r.press("L")
		r.golden(name("pending_list"))
		r.press("L")

		release()
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		r.m.syncing = false
		r.catalogChanged()
		r.press("g", "p")
		r.golden(name("pending_problems"))
		r.press("esc")
	}
}

func TestJumpFindsAPendingDataset(t *testing.T) {
	r := newRig(t, 120, 36)
	h, release := pendingSync(t, r)
	defer func() {
		release()
		_, _ = h.Wait()
	}()
	r.press("ctrl+t", "raw_land")
	r.contains("raw_landing", "● not synced")
	r.press("enter")
	if s := r.m.sel(); s.path.String() != "acme-prod.raw_landing" {
		t.Fatalf("jump reveals the pending dataset: %v", s.path)
	}
	r.contains("● not synced: BigQuery lists this", "the US unit failed")
}

func TestRefreshSyncsNotSyncedDatasets(t *testing.T) {
	r := newRig(t, 120, 36)
	h, release := pendingSync(t, r)
	release()
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	r.m.syncing = false
	r.catalogChanged()
	r.fake.Failures = nil
	r.press("l", "j", "j", "j") // raw_landing
	r.contains("● not synced")
	r.press("r")
	r.contains("raw_landing                             1")
	r.lacks("not synced", "syncing")
}
