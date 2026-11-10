package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func TestPatternFamilyAndSchemaMarksRoundTrip(t *testing.T) {
	c := openTemp(t)
	fam := &api.ShardFamily{Name: "orders_*_history", Prefix: "orders_", Suffix: "_history", Count: 2, Newest: "orders_b_history", NewestCreated: t0}
	newest := table("d1", "orders_b_history", "id")
	newest.Shard = fam
	member := table("d1", "orders_a_history")
	member.Shard, member.SharedSchema = fam, true
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", newest, member))
	tabs, err := c.Tables(ctx, "p", "d1")
	if err != nil || len(tabs) != 2 {
		t.Fatalf("%v %v", tabs, err)
	}
	got := tabs[0] // orders_a_history sorts first
	if got.Shard == nil || *got.Shard != *fam || !got.SharedSchema || got.OwnSchema {
		t.Fatalf("%+v %+v", got, got.Shard)
	}
	snap, err := c.LoadIndex(ctx)
	if err != nil || snap.Tables[1].Shard == nil || snap.Tables[1].Shard.Newest != "orders_b_history" || snap.Tables[1].SharedSchema {
		t.Fatalf("LoadIndex: %+v %v", snap.Tables, err)
	}
	if byID, _ := c.TableByID(ctx, tableID(t, c, "orders_a_history")); byID.Shard == nil || byID.Shard.Suffix != "_history" || !byID.SharedSchema {
		t.Fatalf("TableByID: %+v", byID)
	}

	// gs on the member: its own row and columns, marked
	own := table("d1", "orders_a_history", "id", "extra")
	if err := c.ApplyTable(ctx, "p", own, true); err != nil {
		t.Fatal(err)
	}
	byID, _ := c.TableByID(ctx, tableID(t, c, "orders_a_history"))
	if !byID.OwnSchema || byID.SharedSchema || len(byID.Columns) != 2 || byID.Shard == nil || byID.Shard.Name != "orders_*_history" {
		t.Fatalf("after ApplyTable: %+v", byID)
	}
	marks, err := c.OwnSchemaTables(ctx, unit("p", "EU"))
	if err != nil || !marks["d1.orders_a_history"] || len(marks) != 1 {
		t.Fatalf("%v %v", marks, err)
	}
	if m, _ := c.OwnSchemaTables(ctx, unit("p", "US")); len(m) != 0 {
		t.Fatalf("another unit: %v", m)
	}

	// an unchanged table still takes the new marks
	own.Shard, own.OwnSchema = fam, true
	st := apply(t, c, unit("p", "EU"), t0.Add(time.Hour), dsApply("d1", "EU", newest, own))
	if st.TablesUnchanged != 2 {
		t.Fatalf("%+v", st)
	}
	if err := c.ClearOwnSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.OwnSchemaTables(ctx, unit("p", "EU")); len(m) != 0 {
		t.Fatalf("cleared: %v", m)
	}

	if err := c.ApplyTable(ctx, "p", table("d1", "nope"), false); !errors.Is(err, ErrTableNotStored) {
		t.Fatalf("a table the catalog lacks: %v", err)
	}
	loc, err := c.StoredTable(ctx, api.ObjectPath{Project: "p", Dataset: "d1", Table: "orders_b_history"})
	if err != nil || loc.Location != "EU" || loc.Table.Shard == nil || loc.Table.Shard.Newest != "orders_b_history" {
		t.Fatalf("StoredTable: %+v %v", loc, err)
	}
	if _, err := c.StoredTable(ctx, api.ObjectPath{Project: "p", Dataset: "d1", Table: "nope"}); !errors.Is(err, ErrTableNotStored) {
		t.Fatalf("%v", err)
	}
}

// legacyCatalog is a catalog an older bqt wrote: the built-in date rule put
// events_* in a family, with only prefix, first, last and count.
func legacyCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "events_20240101", "x"), table("d1", "events_20240102", "x")))
	for _, a := range addedColumns[1:] {
		if _, err := c.db.Exec("ALTER TABLE tables DROP COLUMN " + a.column); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.db.Exec(`UPDATE tables SET shard_prefix='events_', shard_first='20240101', shard_last='20240102', shard_count=2`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	return path
}

func TestALegacyFamilyReadsAsADateFamily(t *testing.T) {
	path := legacyCatalog(t)
	ro, err := OpenCatalogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	check := func(c *Catalog) {
		t.Helper()
		tabs, err := c.Tables(ctx, "p", "d1")
		if err != nil || len(tabs) != 2 {
			t.Fatalf("%v %v", tabs, err)
		}
		f := tabs[0].Shard
		if f == nil || !f.Date || f.Name != "events_YYYYMMDD" || f.Newest != "events_20240102" || f.Count != 2 {
			t.Fatalf("%+v", f)
		}
		if legacy, err := c.HasLegacyDateFamilies(ctx); err != nil || !legacy {
			t.Fatalf("legacy families: %v %v", legacy, err)
		}
		if m, err := c.OwnSchemaTables(ctx, unit("p", "EU")); err != nil || len(m) != 0 {
			t.Fatalf("%v %v", m, err)
		}
	}
	check(ro)
	w, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Recreated() != "" {
		t.Fatal("the added columns are added in place")
	}
	check(w)
	// once a sync stores the families with names, nothing is legacy
	fam := &api.ShardFamily{Name: "events_YYYYMMDD", Prefix: "events_", Date: true, First: "20240101", Last: "20240102", Count: 2, Newest: "events_20240102"}
	a, b := table("d1", "events_20240101", "x"), table("d1", "events_20240102", "x")
	a.Shard, b.Shard = fam, fam
	apply(t, w, unit("p", "EU"), t0.Add(time.Hour), dsApply("d1", "EU", a, b))
	if legacy, _ := w.HasLegacyDateFamilies(ctx); legacy {
		t.Fatal("no legacy families after a sync")
	}
}
