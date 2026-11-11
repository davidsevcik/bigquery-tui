package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// orderID is the 24-hex-digit object id of the i-th orders shard.
func orderID(i int) string { return fmt.Sprintf("5f%02x00000000000000000abc", i) }

// shopRig is a rig whose acme-prod has a shop dataset with the non-date
// family orders_* (25 shards; orders_<id 25> is the newest, orders_<id 3>
// has a column the others lack), orders_*_history (2 shards) and a plain
// table.
func shopRig(t *testing.T, w, h int, tui string) *rig {
	t.Helper()
	rigTUI = tui
	rigFake = func(f *bqtest.Fake) {
		var tables []api.TableMeta
		for i := 1; i <= 25; i++ {
			cols := []string{"order_id", "total"}
			if i == 3 {
				cols = append(cols, "legacy_flag")
			}
			tb := bqtest.Table("orders_"+orderID(i), cols...)
			tb.CreatedAt = time.Date(2025, 1, 1, i, 0, 0, 0, time.UTC)
			tables = append(tables, tb)
		}
		for i := 1; i <= 2; i++ {
			tables = append(tables, bqtest.Table("orders_"+orderID(i)+"_history", "order_id", "changed_at"))
		}
		tables = append(tables, bqtest.Table("customers", "id", "email"))
		p := f.Projects["acme-prod"]
		p.Datasets = append(p.Datasets, bqtest.DS("shop", "EU", tables...))
	}
	defer func() { rigTUI, rigFake = "", nil }()
	return newRig(t, w, h)
}

// toShopFamily puts the columns style's cursor on orders_*.
func (r *rig) toShopFamily() {
	r.t.Helper()
	r.press("l", "j", "j", "j", "l", "j")
	if s := r.m.sel(); s.kind != api.KindShards || s.path.Table != "orders_*" {
		r.t.Fatalf("not on the family: %+v", s)
	}
}

func TestShardGoldenScreens(t *testing.T) {
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {60, 20}} {
		name := func(s string) string { return s + "_" + itoa(sz[0]) + "x" + itoa(sz[1]) }
		r := shopRig(t, sz[0], sz[1], "")
		r.toShopFamily()
		r.golden(name("shard_columns_family"))
		r.press("enter", "j") // drill in, then an older member
		r.golden(name("shard_columns_member"))
		r.press("esc", "L") // back to the family row, tree style
		r.press("l")        // open the family
		r.golden(name("shard_tree_open"))
		r.press("h", "g", "g", "j", "l", "j", "j", "l") // close it; open the date family in events
		r.golden(name("shard_tree_date_open"))

		r = shopRig(t, sz[0], sz[1], "family_rows = 0\n")
		r.toShopFamily()
		r.press("L", "l")
		r.golden(name("shard_tree_rows0"))
	}
}

func TestDrillingIntoAFamily(t *testing.T) {
	r := shopRig(t, 200, 70, "")
	r.toShopFamily()
	r.contains("orders_*", "×25", "orders_*_history", "×2", "shard family ×25")
	r.press("enter")
	if s := r.m.sel(); s.kind != api.KindTable || s.path.Table != "orders_"+orderID(25) {
		t.Fatalf("newest first: %+v", s.path)
	}
	r.contains("orders_* 1/25")
	r.lacks("customers") // only the family's members
	r.press("j")
	r.contains("schema of orders_"+orderID(25)+" (family of 25)", "gs syncs")

	// / filters the members by an id fragment
	r.press("/", "5f17", "enter")
	if s := r.m.sel(); s.path.Table != "orders_"+orderID(23) {
		t.Fatalf("%+v", s.path)
	}
	r.contains("orders_* 1/1 of 25")
	r.press("esc") // clears the filter
	r.contains("Tables")
	r.press("esc") // back to the family row
	if s := r.m.sel(); s.kind != api.KindShards || s.path.Table != "orders_*" {
		t.Fatalf("esc returns to the family row: %+v", s)
	}
}

func TestFamilyActions(t *testing.T) {
	r := shopRig(t, 200, 70, "")
	r.toShopFamily()
	// p samples the newest shard
	r.press("p")
	r.contains("Sample · free read, no query job · acme-prod.shop.orders_" + orderID(25))
	// s reads one shard through the wildcard
	r.press("s")
	want := "SELECT *\nFROM `acme-prod.shop.orders_*`\nWHERE _TABLE_SUFFIX = '" + orderID(25) + "'\nLIMIT 100"
	if r.text() != want {
		t.Fatalf("s on a family:\n%s", r.text())
	}
	r.contains("orders_*") // the buffer's name
	// i inserts the wildcard
	r.press("ctrl+w", "h", "i")
	if !strings.Contains(r.text(), "WHERE `acme-prod.shop.orders_*`_TABLE_SUFFIX") {
		t.Fatalf("i on a family inserts its wildcard at the cursor: %q", r.text())
	}
	// on orders_*_history the wildcard is still prefix*, and s pins the suffix
	r.press("ctrl+w", "h", "j", "s")
	if !strings.Contains(r.text(), "FROM `acme-prod.shop.orders_*`\nWHERE _TABLE_SUFFIX = '"+orderID(2)+"_history'") {
		t.Fatalf("s on a suffixed family:\n%s", r.text())
	}
}

func TestGSSyncsAMembersOwnSchema(t *testing.T) {
	r := shopRig(t, 200, 70, "")
	r.toShopFamily()
	r.press("enter", "G", "k", "k") // orders_<id 3>: newest first, so third from the end
	if s := r.m.sel(); s.path.Table != "orders_"+orderID(3) {
		t.Fatalf("%+v", s.path)
	}
	r.contains("schema of orders_" + orderID(25))
	r.lacks("legacy_flag")
	r.press("g", "s")
	r.contains("synced the schema of acme-prod.shop.orders_"+orderID(3), "legacy_flag")
	r.lacks("schema of orders_" + orderID(25))
	if n := len(r.fake.CallsTo("GetTableMeta")); n != 1 {
		t.Fatalf("one tables.get: %d", n)
	}
	// from the Schema pane too, and on a plain table
	r.press("esc", "k") // the family row, then customers
	r.press("ctrl+w", "j", "g", "s")
	r.contains("synced the schema of acme-prod.shop.customers")
}

func TestTreeFamilyRowsAndFilter(t *testing.T) {
	r := shopRig(t, 200, 70, "")
	r.toShopFamily()
	r.press("L", "l")
	r.contains("orders_"+orderID(25), "orders_"+orderID(6), "… 5 more · / to filter")
	r.lacks("orders_" + orderID(5) + " ")
	// the tree filter reveals only the members that match
	r.press("h", "/", "5f17", "enter")
	var shown []string
	for _, row := range r.m.tree.rows {
		if row.depth == 3 {
			shown = append(shown, row.node.Name)
		}
	}
	if len(shown) != 1 || shown[0] != "orders_"+orderID(23) {
		t.Fatalf("the filter reveals only the matching member: %v", shown)
	}
	r.lacks("more · / to filter")
	r.press("esc")

	r = shopRig(t, 200, 70, "family_rows = 0\n")
	r.toShopFamily()
	r.press("L", "l")
	r.contains("… 25 more · / to filter")
	for _, row := range r.m.tree.rows {
		if row.depth == 3 && row.more == 0 {
			t.Fatalf("family_rows = 0 shows no member: %+v", row.node)
		}
	}
}

func TestSearchReturnsTheFamilyUnlessAnIDIsTyped(t *testing.T) {
	r := shopRig(t, 200, 70, "")
	r.press("ctrl+t")
	r.typeText("orders_")
	var got []string
	for _, x := range r.m.jump.results {
		if x.Path.Dataset != "shop" {
			continue
		}
		got = append(got, string(x.Kind)+":"+x.Path.Table)
	}
	if strings.Join(got, ",") != "shard_family:orders_*,shard_family:orders_*_history" {
		t.Fatalf("a prefix finds the families: %v", got)
	}
	r.press("esc", "ctrl+t")
	r.typeText(orderID(23))
	if len(r.m.jump.results) == 0 || r.m.jump.results[0].Path.Table != "orders_"+orderID(23) || r.m.jump.results[0].Kind != api.KindTable {
		t.Fatalf("an id finds the member: %+v", r.m.jump.results)
	}
}

// A filter match carries its family, so a member is found under it even when
// the family's fixed parts hold YYYYMMDD and its name reads two ways.
func TestMatchedFamilyUsesTheFamilysOwnParts(t *testing.T) {
	cases := []struct {
		fam    api.ShardFamily
		member string
	}{
		{api.ShardFamily{Name: "r_YYYYMMDD_YYYYMMDD", Prefix: "r_YYYYMMDD_", Date: true}, "r_YYYYMMDD_20240101"},
		{api.ShardFamily{Name: "events_YYYYMMDD_YYYYMMDD", Prefix: "events_", Suffix: "_YYYYMMDD", Date: true}, "events_20240101_YYYYMMDD"},
	}
	for _, c := range cases {
		fam := c.fam
		fams := []api.FilterMatch{{Kind: api.KindShards, Path: api.ObjectPath{Project: "p", Dataset: "d", Table: fam.Name}, Shard: &fam}}
		got, ok := matchedFamily(fams, api.ObjectPath{Project: "p", Dataset: "d", Table: c.member})
		if !ok || got.Table != fam.Name {
			t.Fatalf("%s under %s: %v %v", c.member, fam.Name, got, ok)
		}
	}
}
