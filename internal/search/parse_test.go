package search

import (
	"reflect"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func TestParseTermsAndFilters(t *testing.T) {
	q := Parse(`  orders p:prod -d:tmp type:view,mview col:"user id" desc:revenue label:team:data`)
	if len(q.Terms) != 1 || q.Terms[0].Text != "orders" || q.Raw[q.Terms[0].Span.Start:q.Terms[0].Span.End] != "orders" {
		t.Fatalf("%+v", q.Terms)
	}
	want := []struct {
		key  string
		vals []string
		neg  bool
	}{
		{"p", []string{"prod"}, false},
		{"d", []string{"tmp"}, true},
		{"type", []string{"view", "mview"}, false},
		{"col", []string{"user id"}, false},
		{"desc", []string{"revenue"}, false},
		{"label", []string{"team:data"}, false},
	}
	if len(q.Filters) != len(want) {
		t.Fatalf("%+v", q.Filters)
	}
	for i, w := range want {
		f := q.Filters[i]
		if f.Key != w.key || !reflect.DeepEqual(f.Values, w.vals) || f.Negated != w.neg || f.Status != api.FilterOK {
			t.Errorf("filter %d: %+v", i, f)
		}
	}
	f := q.Filters[2]
	if q.Raw[f.Span.Start:f.Span.End] != "type:view,mview" || q.Raw[f.ValueSpan.Start:f.ValueSpan.End] != "view,mview" {
		t.Errorf("spans: %q %q", q.Raw[f.Span.Start:f.Span.End], q.Raw[f.ValueSpan.Start:f.ValueSpan.End])
	}
	if nf := q.Filters[1]; q.Raw[nf.Span.Start:nf.Span.End] != "-d:tmp" || q.Raw[nf.ValueSpan.Start:nf.ValueSpan.End] != "tmp" {
		t.Errorf("negated spans")
	}
}

func TestParseStatuses(t *testing.T) {
	q := Parse("type:foo d: type:v col:")
	st := []api.FilterStatus{api.FilterInvalid, api.FilterPending, api.FilterOK, api.FilterPending}
	for i, f := range q.Filters {
		if f.Status != st[i] {
			t.Errorf("%d: %+v", i, f)
		}
	}
}

func TestParseUnknownKeyIsATerm(t *testing.T) {
	q := Parse("foo:bar -x http://a")
	if len(q.Filters) != 0 || len(q.Terms) != 3 || q.Terms[0].Text != "foo:bar" || q.Terms[1].Text != "-x" {
		t.Fatalf("%+v", q)
	}
}

func TestParseCaseAndQuotes(t *testing.T) {
	q := Parse(`"Daily Active" P:Prod`)
	if q.Terms[0].Text != "daily active" || q.Filters[0].Key != "p" || q.Filters[0].Values[0] != "prod" {
		t.Fatalf("%+v", q)
	}
}

func TestParsePath(t *testing.T) {
	for in, want := range map[string]api.ObjectPath{
		"proj.ds.tbl":          {Project: "proj", Dataset: "ds", Table: "tbl"},
		"`my-proj.ds.tbl`":     {Project: "my-proj", Dataset: "ds", Table: "tbl"},
		"`my-proj`.`ds`.`tbl`": {Project: "my-proj", Dataset: "ds", Table: "tbl"},
		"example.com:p.ds.tbl": {Project: "example.com:p", Dataset: "ds", Table: "tbl"},
	} {
		q := Parse(in)
		if q.Path == nil || *q.Path != want {
			t.Errorf("%q: %+v", in, q.Path)
		}
	}
	for _, in := range []string{"ds.tbl", "a..b", "p.d.t extra", "p.d.t d:x", ""} {
		if q := Parse(in); q.Path != nil {
			t.Errorf("%q must not be a path: %+v", in, q.Path)
		}
	}
	if !Parse("").Empty() || Parse("x").Empty() {
		t.Error("Empty")
	}
}

func TestMatchNameTiers(t *testing.T) {
	cases := []struct {
		name, term string
		fuzzy      bool
		tier       int
	}{
		{"orders", "orders", true, tExact},
		{"order_items", "order", true, tPrefix},
		{"order_items", "oi", true, tWord},
		{"order_items", "items", true, tWord},
		{"order_items", "ordit", true, tWord},
		{"daily_order_items", "oi", true, tWord},
		{"reorders", "order", true, tSub},
		{"order_items", "oris", true, tFuzzy},
		{"order_items", "oris", false, -1},
		{"orders", "zz", true, -1},
		{"ab", "abc", true, -1},
		{"orders", "", true, -1},
	}
	for _, c := range cases {
		tier, sp := matchName(c.name, c.term, c.fuzzy)
		if tier != c.tier {
			t.Errorf("%s ~ %s: tier %d, want %d", c.name, c.term, tier, c.tier)
		}
		if tier >= 0 {
			n := 0
			for _, s := range sp {
				if s.Start < 0 || s.End > len(c.name) || s.Start >= s.End {
					t.Errorf("%s ~ %s: bad span %+v", c.name, c.term, s)
				}
				n += s.End - s.Start
			}
			if n != len(c.term) && tier != tExact {
				t.Errorf("%s ~ %s: spans cover %d bytes, term has %d", c.name, c.term, n, len(c.term))
			}
		}
	}
	if _, sp := matchName("order_items", "oi", true); !reflect.DeepEqual(sp, []api.Span{{Start: 0, End: 1}, {Start: 6, End: 7}}) {
		t.Errorf("word spans: %+v", sp)
	}
}
