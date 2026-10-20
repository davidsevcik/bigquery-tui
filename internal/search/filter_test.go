package search

import (
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func filtered(t *testing.T, s *Searcher, q string, o api.FilterOptions) []string {
	t.Helper()
	r, err := s.Filter(ctx, q, o)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(r.Matches))
	for i, m := range r.Matches {
		out[i] = string(m.Kind) + ":" + m.Path.String()
	}
	return out
}

func inSales() *api.ObjectPath { return &api.ObjectPath{Project: "acme-prod", Dataset: "sales"} }

func kinds(k ...api.ResultKind) []api.ResultKind { return k }

func TestFilterMatchesOwnNamesInRankOrder(t *testing.T) {
	s := standard().searcher()
	got := filtered(t, s, "orders", api.FilterOptions{Scope: inSales(), Kinds: kinds(api.KindTable)})
	// exact, word boundary, substring, fuzzy; customers mentions orders only in its description
	eq(t, got, "table:acme-prod.sales.orders", "table:acme-prod.sales.v_orders", "table:acme-prod.sales.reorders", "table:acme-prod.sales.order_items")
}

func TestFilterTermsMatchContainersByTheirOwnName(t *testing.T) {
	s := standard().searcher()
	eq(t, filtered(t, s, "sal", api.FilterOptions{Kinds: kinds(api.KindDataset)}), "dataset:acme-prod.sales", "dataset:acme-staging.sales")
	eq(t, filtered(t, s, "staging", api.FilterOptions{Kinds: kinds(api.KindProject)}), "project:acme-staging")
	// a dataset name does not keep the tables inside it (search would)
	eq(t, filtered(t, s, "sales", api.FilterOptions{Kinds: kinds(api.KindTable)}))
	// scope narrows containers too
	eq(t, filtered(t, s, "sal", api.FilterOptions{Kinds: kinds(api.KindDataset), Scope: &api.ObjectPath{Project: "acme-staging"}}), "dataset:acme-staging.sales")
}

func TestFilterTableFiltersKeepContainersThatHoldAPassingTable(t *testing.T) {
	s := standard().searcher()
	ds := api.FilterOptions{Kinds: kinds(api.KindDataset)}
	eq(t, filtered(t, s, "type:view", api.FilterOptions{Scope: inSales(), Kinds: kinds(api.KindTable)}), "table:acme-prod.sales.v_orders")
	eq(t, filtered(t, s, "type:view", ds), "dataset:acme-prod.sales")
	eq(t, filtered(t, s, "type:view", api.FilterOptions{Kinds: kinds(api.KindProject)}), "project:acme-prod")
	eq(t, filtered(t, s, "col:user_id", ds), "dataset:acme-prod.events")
	eq(t, filtered(t, s, "label:team:finance", ds), "dataset:acme-prod.sales")
	eq(t, filtered(t, s, "desc:marts", ds), "dataset:acme-prod.sales")
	// free terms still describe the container itself
	eq(t, filtered(t, s, "sal type:view", ds), "dataset:acme-prod.sales")
	eq(t, filtered(t, s, "sal -type:view", ds), "dataset:acme-prod.sales", "dataset:acme-staging.sales")
}

func TestFilterPathFiltersNarrowByPath(t *testing.T) {
	s := standard().searcher()
	eq(t, filtered(t, s, "d:tmp", api.FilterOptions{Kinds: kinds(api.KindTable)}), "table:acme-prod.tmp_scratch.orders_tmp")
	eq(t, filtered(t, s, "-d:tmp", api.FilterOptions{Kinds: kinds(api.KindDataset)}), "dataset:acme-prod.events", "dataset:acme-prod.sales", "dataset:acme-staging.sales")
	eq(t, filtered(t, s, "d:tmp", api.FilterOptions{Kinds: kinds(api.KindProject)}), "project:acme-prod")
	eq(t, filtered(t, s, "p:staging", api.FilterOptions{Kinds: kinds(api.KindProject, api.KindDataset)}), "project:acme-staging", "dataset:acme-staging.sales")
}

func TestFilterShardFamilyPassesWhenAMemberPasses(t *testing.T) {
	s := standard().searcher()
	events := &api.ObjectPath{Project: "acme-prod", Dataset: "events"}
	fams := api.FilterOptions{Scope: events, Kinds: kinds(api.KindShards)}
	eq(t, filtered(t, s, "events_20240102", fams), "shard_family:acme-prod.events.events_YYYYMMDD")
	eq(t, filtered(t, s, "clicks", fams))
	// members come back as tables, so a family can be narrowed to them
	eq(t, filtered(t, s, "20240102", api.FilterOptions{Scope: events, Kinds: kinds(api.KindTable)}), "table:acme-prod.events.events_20240102")
}

func TestFilterPastedPathAndHiddenProjects(t *testing.T) {
	s := standard().searcher()
	eq(t, filtered(t, s, "acme-prod.sales.orders", api.FilterOptions{}), "project:acme-prod", "dataset:acme-prod.sales", "table:acme-prod.sales.orders")
	eq(t, filtered(t, s, "`acme-staging.sales.orders`", api.FilterOptions{Kinds: kinds(api.KindTable)}), "table:acme-staging.sales.orders")
	// an unknown path is an ordinary term
	eq(t, filtered(t, s, "nope.sales.orders", api.FilterOptions{}))
	eq(t, filtered(t, s, "orders", api.FilterOptions{Kinds: kinds(api.KindTable), Hidden: []string{"acme-prod"}}), "table:acme-staging.sales.orders")
	eq(t, filtered(t, s, "", api.FilterOptions{Kinds: kinds(api.KindProject), Hidden: []string{"acme-prod"}}), "project:acme-staging")
}

func TestFilterDescAndLabelDescribeTheObjectItself(t *testing.T) {
	f := standard()
	f.dataset("acme-prod", "empty_marts", "EU", "Marts to come")
	s := f.searcher()
	// a table does not pass desc: through its dataset's description
	eq(t, filtered(t, s, "desc:marts", api.FilterOptions{Scope: inSales(), Kinds: kinds(api.KindTable)}))
	// a dataset passes on its own description, even with no tables
	eq(t, filtered(t, s, "desc:marts", api.FilterOptions{Kinds: kinds(api.KindDataset)}), "dataset:acme-prod.empty_marts", "dataset:acme-prod.sales")
	eq(t, filtered(t, s, "desc:marts", api.FilterOptions{Kinds: kinds(api.KindProject)}), "project:acme-prod")
}

func TestFilterContainersMatchFuzzily(t *testing.T) {
	s := standard().searcher()
	eq(t, filtered(t, s, "tmsrh", api.FilterOptions{Kinds: kinds(api.KindDataset)}), "dataset:acme-prod.tmp_scratch")
}
