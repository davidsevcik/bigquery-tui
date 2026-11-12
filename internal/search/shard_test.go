package search

import (
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

const (
	idA = "0123456789abcdef0000000a"
	idB = "0123456789abcdef0000000b"
	idC = "0123456789abcdef0000000c"
)

// withOrders adds a non-date family orders_* (newest by creation time:
// orders_<idB>), a suffixed family orders_*_history, and a plain table.
func withOrders() *fixture {
	f := standard()
	f.dataset("acme-prod", "shop", "EU", "")
	fam := &api.ShardFamily{Name: "orders_*", Prefix: "orders_", Count: 3, Newest: "orders_" + idB}
	for i, id := range []string{idA, idB, idC} {
		day := []int{1, 3, 2}[i]
		var cols []string
		if id == idB {
			cols = []string{"order_id", "total"}
		}
		t := f.table("acme-prod", "shop", "orders_"+id, api.TypeTable, "", cols...)
		t.Shard, t.CreatedAt = fam, time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC)
		t.SharedSchema = id != idB
	}
	hist := &api.ShardFamily{Name: "orders_*_history", Prefix: "orders_", Suffix: "_history", Count: 2, Newest: "orders_" + idA + "_history"}
	for _, id := range []string{idA, idB} {
		var cols []string
		if id == idA {
			cols = []string{"order_id", "changed_at"}
		}
		f.table("acme-prod", "shop", "orders_"+id+"_history", api.TypeTable, "", cols...).Shard = hist
	}
	f.table("acme-prod", "shop", "customers", api.TypeTable, "", "customer_id")
	return f
}

func TestPatternFamiliesBrowse(t *testing.T) {
	s := withOrders().searcher()
	ts := s.Tables("acme-prod", "shop")
	var got []string
	for _, n := range ts {
		got = append(got, string(n.Kind)+":"+n.Name)
	}
	eq(t, got, "table:customers", "shard_family:orders_*", "shard_family:orders_*_history")
	if ts[1].Path.Table != "orders_*" || ts[1].Children != 3 {
		t.Fatalf("%+v", ts[1])
	}
	var members []string
	for _, n := range s.ShardMembers("acme-prod", "shop", "orders_*", "") {
		members = append(members, n.Name)
	}
	eq(t, members, "orders_"+idB, "orders_"+idC, "orders_"+idA) // newest created first
	for _, p := range []string{"orders_*", "orders_*_history"} {
		ref, ok := s.Lookup(api.ObjectPath{Project: "acme-prod", Dataset: "shop", Table: p})
		if !ok || ref.Family == nil || ref.Family.Name != p {
			t.Fatalf("%s: %+v", p, ref)
		}
	}
	ref, _ := s.Lookup(api.ObjectPath{Project: "acme-prod", Dataset: "shop", Table: "orders_*"})
	if ref.Path.Table != "orders_"+idB {
		t.Fatalf("the family resolves to its newest shard: %+v", ref.Path)
	}
	// a member is itself, with its family attached
	ref, ok := s.Lookup(api.ObjectPath{Project: "acme-prod", Dataset: "shop", Table: "orders_" + idA})
	if !ok || ref.Family != nil || ref.Meta.Shard == nil || ref.Meta.Shard.Name != "orders_*" {
		t.Fatalf("%+v", ref)
	}
	// the date family answers its wildcard too (the CLI and MCP name)
	if ref, ok := s.Lookup(api.ObjectPath{Project: "acme-prod", Dataset: "events", Table: "events_*"}); !ok || ref.Path.Table != "events_20240103" {
		t.Fatalf("%+v", ref)
	}
}

func TestAPrefixTermFindsTheFamilyAndAnIDFragmentTheMember(t *testing.T) {
	s := withOrders().searcher()
	scope := api.SearchOptions{TablesOnly: true, Scope: &api.ObjectPath{Project: "acme-prod", Dataset: "shop"}}
	eq(t, names(t, s, "orders", scope), "acme-prod.shop.orders_*", "acme-prod.shop.orders_*_history")
	eq(t, names(t, s, "history", scope), "acme-prod.shop.orders_*_history")
	eq(t, names(t, s, "0000000c", scope), "acme-prod.shop.orders_"+idC)
	// a term that reaches past the prefix into the id finds the members
	eq(t, names(t, s, "orders_"+idA, scope), "acme-prod.shop.orders_"+idA, "acme-prod.shop.orders_"+idA+"_history")

	r, _ := s.Search(ctx, "history", scope)
	fam := r.Results[0]
	if fam.Kind != api.KindShards || fam.Shard == nil || fam.Shard.Newest != "orders_"+idA+"_history" {
		t.Fatalf("%+v", fam)
	}
	// the span on the suffix moves onto the family name
	if sp := fam.Spans["name"]; len(sp) != 1 || fam.Name[sp[0].Start:sp[0].End] != "history" {
		t.Fatalf("%+v on %q", sp, fam.Name)
	}
	// a pasted family path is an exact hit on the family
	r, _ = s.Search(ctx, "acme-prod.shop.orders_*", api.SearchOptions{})
	if len(r.Results) != 1 || r.Results[0].Kind != api.KindShards || r.Results[0].Path.Table != "orders_*" {
		t.Fatalf("%+v", r.Results)
	}
}

func TestColSearchFindsAFamilyOnce(t *testing.T) {
	f := withOrders()
	// a member synced on its own (gs) has columns of its own
	for i := range f.snap.Tables {
		if f.snap.Tables[i].Name == "orders_"+idC {
			f.snap.Tables[i].OwnSchema, f.snap.Tables[i].SharedSchema = true, false
			f.cols.cols[f.snap.Tables[i].ID] = []api.ColumnMeta{{FieldPath: "order_id", DataType: "STRING"}}
		}
	}
	s := f.searcher()
	scope := api.SearchOptions{TablesOnly: true, Scope: &api.ObjectPath{Project: "acme-prod", Dataset: "shop"}}
	eq(t, names(t, s, "col:order_id", scope), "acme-prod.shop.orders_*", "acme-prod.shop.orders_*_history", "acme-prod.shop.orders_"+idC)
	r, _ := s.Search(ctx, "col:total", scope)
	if len(r.Results) != 1 || r.Results[0].Kind != api.KindShards || len(r.Results[0].ColumnHits) != 1 {
		t.Fatalf("the family's column hits come from its newest shard: %+v", r.Results)
	}
}

func TestFilterRevealsOnlyTheMembersThatMatch(t *testing.T) {
	s := withOrders().searcher()
	shop := &api.ObjectPath{Project: "acme-prod", Dataset: "shop"}
	opts := api.FilterOptions{Scope: shop, Kinds: kinds(api.KindTable, api.KindShards)}
	eq(t, filtered(t, s, "orders", opts), "shard_family:acme-prod.shop.orders_*", "shard_family:acme-prod.shop.orders_*_history")
	eq(t, filtered(t, s, "0000000c", opts), "shard_family:acme-prod.shop.orders_*", "table:acme-prod.shop.orders_"+idC)
	// a list of the family's members asks for every passing member
	opts.ExpandShards = true
	got := filtered(t, s, "orders", opts)
	if len(got) != 7 {
		t.Fatalf("%v", got)
	}
}

// A term in the varying part finds the member even when lower-casing the name
// changes its byte length (the Kelvin sign U+212A lowers to an ASCII k).
func TestMemberHitWithACaseChangingPrefix(t *testing.T) {
	f := standard()
	f.dataset("acme-prod", "k", "EU", "")
	fam := &api.ShardFamily{Name: "\u212a_*", Prefix: "\u212a_", Count: 2, Newest: "\u212a_2"}
	for _, n := range []string{"\u212a_1", "\u212a_2"} {
		f.table("acme-prod", "k", n, api.TypeTable, "", "c").Shard = fam
	}
	r, _ := f.searcher().Search(ctx, "1", api.SearchOptions{Scope: &api.ObjectPath{Project: "acme-prod", Dataset: "k"}})
	if len(r.Results) == 0 || r.Results[0].Kind != api.KindTable || r.Results[0].Path.Table != "\u212a_1" {
		t.Fatalf("%+v", r.Results)
	}
}

// A family's filter match carries the family, whose parts say which tables
// are its members.
func TestFilterFamilyMatchesCarryTheirFamily(t *testing.T) {
	s := withOrders().searcher()
	r, err := s.Filter(ctx, "orders", api.FilterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range r.Matches {
		if m.Kind != api.KindShards {
			continue
		}
		n++
		if m.Shard == nil || m.Shard.Name != m.Path.Table || m.Shard.Prefix != "orders_" {
			t.Fatalf("%+v", m)
		}
	}
	if n != 2 {
		t.Fatalf("%d family matches: %+v", n, r.Matches)
	}
}
