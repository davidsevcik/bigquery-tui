package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

const hexA, hexB = "0123456789abcdef0000000a", "0123456789abcdef0000000b"

// withOrderShards adds the family orders_* (newest: orders_<hexB>) to
// proj-a.sales, with the hex pattern next to the date one.
func withOrderShards(t *testing.T) (*env, *Engine) {
	t.Helper()
	e := newEnv(t, "proj-a")
	e.cfg = e.config("shard_patterns = ['.+(\\d{8})', '.+_([0-9a-f]{24})']\n")
	at := func(d int, tb api.TableMeta) api.TableMeta {
		tb.CreatedAt = time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC)
		return tb
	}
	ds := &e.fake.Projects["proj-a"].Datasets[0]
	ds.Tables = append(ds.Tables, at(1, bqtest.Table("orders_"+hexA, "id", "legacy")), at(2, bqtest.Table("orders_"+hexB, "id", "total")))
	eng := e.open(false)
	fullSync(t, eng)
	return e, eng
}

func TestAFamilyAndItsMembersShowTheFamilySchema(t *testing.T) {
	_, eng := withOrderShards(t)
	fam, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders_*"})
	if err != nil || fam.SchemaFrom != "orders_"+hexB || len(fam.Table.Columns) != 2 || fam.Shard == nil || fam.Shard.Name != "orders_*" {
		t.Fatalf("family: %+v %v", fam, err)
	}
	member, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders_" + hexA})
	if err != nil || member.SchemaFrom != "orders_"+hexB || member.Table.Columns[1].FieldPath != "total" {
		t.Fatalf("a member without columns shows the family's: %+v %v", member, err)
	}
	newest, _ := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders_" + hexB})
	if newest.SchemaFrom != "" {
		t.Fatalf("the newest shard's columns are its own: %+v", newest)
	}
}

func TestSyncTableScope(t *testing.T) {
	e, eng := withOrderShards(t)
	member := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders_" + hexA}
	h, err := eng.StartSync(ctx, api.SyncScope{Table: &member})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := h.Wait(); err != nil || res.UnitsOK != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	d, _ := eng.TableDetail(ctx, member)
	if d.SchemaFrom != "" || len(d.Table.Columns) != 2 || d.Table.Columns[1].FieldPath != "legacy" || !d.Table.OwnSchema {
		t.Fatalf("gs syncs the member's own schema: %+v", d)
	}
	// a family's path syncs its newest shard
	fam := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders_*"}
	h, err = eng.StartSync(ctx, api.SyncScope{Table: &fam})
	if err != nil {
		t.Fatal(err)
	}
	h.Wait()
	calls := e.fake.CallsTo("GetTableMeta")
	if len(calls) != 2 || calls[1].Table != "orders_"+hexB {
		t.Fatalf("%+v", calls)
	}
	nope := api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "nope"}
	if _, err := eng.StartSync(ctx, api.SyncScope{Table: &nope}); !errors.Is(err, ErrUnknownTable) {
		t.Fatalf("%v", err)
	}
}

func TestAgentsSeeTheCollapsedCatalog(t *testing.T) {
	_, eng := withOrderShards(t)
	hits, err := eng.CatalogSearch(ctx, "orders_", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fam *api.CatalogHit
	for i, h := range hits.Hits {
		if h.Kind == api.KindShards {
			fam = &hits.Hits[i]
		}
	}
	if fam == nil || fam.Path != "proj-a.sales.orders_*" || fam.Shard.Count != 2 || fam.Shard.Newest != "orders_"+hexB || fam.Shard.Wildcard != "orders_*" {
		t.Fatalf("%+v", hits.Hits)
	}
	for _, path := range []string{"proj-a.sales.orders_*", "`proj-a.sales.orders_*`", "proj-a.sales.orders_" + hexA} {
		s, err := eng.Schema(ctx, path)
		if err != nil || len(s.Columns) != 2 || s.Shard == nil {
			t.Fatalf("%s: %+v %v", path, s, err)
		}
		if strings.HasSuffix(path, hexA) && s.SchemaFrom != "orders_"+hexB {
			t.Fatalf("a member's schema says where it comes from: %+v", s)
		}
	}
}
