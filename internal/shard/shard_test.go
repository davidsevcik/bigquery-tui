package shard

import (
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

const (
	datePattern = `.+(\d{8})`
	hexPattern  = `.+_([0-9a-f]{24})(?:_history)?`
)

func mustCompile(t *testing.T, src ...string) *Patterns {
	t.Helper()
	p, errs := Compile(src)
	if len(errs) > 0 {
		t.Fatalf("Compile(%q): %v", src, errs)
	}
	return p
}

func mk(names ...string) []api.TableMeta {
	var ts []api.TableMeta
	for _, n := range names {
		ts = append(ts, api.TableMeta{Dataset: "ds", Name: n, Type: api.TypeTable})
	}
	return ts
}

func byName(ts []api.TableMeta) map[string]*api.ShardFamily {
	out := map[string]*api.ShardFamily{}
	for _, t := range ts {
		out[t.Name] = t.Shard
	}
	return out
}

func TestCompile(t *testing.T) {
	cases := []struct {
		src  string
		want string // "" valid, else part of the reason
	}{
		{datePattern, ""},
		{hexPattern, ""},
		{`(?i)orders_([a-f0-9]+)`, ""},
		{`orders_\d+`, "found 0"},
		{`(a)_(b)`, "found 2"},
		{`orders_(\d+`, "not a valid RE2"},
		{`a(?=b)(c)`, "not a valid RE2"}, // lookahead is not RE2
	}
	for _, c := range cases {
		_, errs := Compile([]string{c.src})
		switch {
		case c.want == "" && len(errs) > 0:
			t.Errorf("%q: unexpected error %v", c.src, errs)
		case c.want != "" && (len(errs) != 1 || !strings.Contains(errs[0].Reason, c.want)):
			t.Errorf("%q: want an error containing %q, got %v", c.src, c.want, errs)
		}
	}
}

func TestPatternsMatchTheWholeName(t *testing.T) {
	p := mustCompile(t, `events_(\d{8})`)
	if p.MatchesAny("my_events_20240101") || p.MatchesAny("events_20240101_x") {
		t.Fatal("a partial match must not count")
	}
	if !p.MatchesAny("events_20240101") {
		t.Fatal("the whole name matches")
	}
	if got := p.Combined(); got != `^(?:events_(\d{8}))$` {
		t.Fatalf("Combined = %q", got)
	}
	if (&Patterns{}).Combined() != "" || (*Patterns)(nil).Combined() != "" {
		t.Fatal("no patterns combine to nothing")
	}
	a, b := mustCompile(t, "a(b)"), mustCompile(t, "a(b)", "c(d)")
	if a.Hash() == b.Hash() || a.Hash() != mustCompile(t, "a(b)").Hash() {
		t.Fatal("the hash follows the effective patterns")
	}
}

func TestShardAssign(t *testing.T) {
	t.Run("no patterns collapse nothing", func(t *testing.T) {
		ts := mk("events_20240101", "events_20240102")
		Assign(ts, nil)
		Assign(ts, mustCompile(t))
		for _, tb := range ts {
			if tb.Shard != nil {
				t.Fatalf("%s: %+v", tb.Name, tb.Shard)
			}
		}
	})

	t.Run("date families", func(t *testing.T) {
		ts := mk("a_20240101", "a_20240102", "a_20241301" /* bad month */, "b20240101", "b20240105", "20240101", "20240102", "c_2024010", "d_20240101_x")
		Assign(ts, mustCompile(t, datePattern))
		got := byName(ts)
		for _, n := range []string{"a_20240101", "a_20240102"} {
			// a_20241301 joins a_ too: the family is then not a date family
			if f := got[n]; f == nil || f.Prefix != "a_" || f.Count != 3 || f.Date || f.Name != "a_*" {
				t.Errorf("%s: %+v", n, f)
			}
		}
		if f := got["b20240101"]; f == nil || !f.Date || f.Name != "bYYYYMMDD" || f.First != "20240101" || f.Last != "20240105" || f.Newest != "b20240105" || f.Count != 2 {
			t.Errorf("b: %+v", f)
		}
		// .+ needs one character before the date: 20240101 and 20240102 do
		// not match, and nothing matches the 7-digit or the suffixed names
		for _, n := range []string{"20240101", "20240102", "c_2024010", "d_20240101_x"} {
			if got[n] != nil {
				t.Errorf("%s must stay a plain table: %+v", n, got[n])
			}
		}
	})

	t.Run("an 8-digit suffix that is not a date", func(t *testing.T) {
		ts := mk("x_20240101", "x_99999999")
		Assign(ts, mustCompile(t, datePattern))
		if f := ts[0].Shard; f == nil || f.Date || f.Name != "x_*" || f.First != "" {
			t.Fatalf("%+v", f)
		}
	})

	t.Run("hex ids, newest by creation time", func(t *testing.T) {
		at := func(d int) time.Time { return time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC) }
		ts := []api.TableMeta{
			{Dataset: "ds", Name: "orders_aaaaaaaaaaaaaaaaaaaaaaaa", Type: api.TypeTable, CreatedAt: at(1)},
			{Dataset: "ds", Name: "orders_bbbbbbbbbbbbbbbbbbbbbbbb", Type: api.TypeTable, CreatedAt: at(3)},
			{Dataset: "ds", Name: "orders_cccccccccccccccccccccccc", Type: api.TypeTable, CreatedAt: at(2)},
			{Dataset: "ds", Name: "orders_aaaaaaaaaaaaaaaaaaaaaaaa_history", Type: api.TypeTable, CreatedAt: at(1)},
			{Dataset: "ds", Name: "orders_dddddddddddddddddddddddd_history", Type: api.TypeTable, CreatedAt: at(1)},
			{Dataset: "ds", Name: "orders_eeeeeeeeeeeeeeeeeeeeeeee_history", Type: api.TypeView, CreatedAt: at(9)},
			{Dataset: "other", Name: "orders_ffffffffffffffffffffffff", Type: api.TypeTable, CreatedAt: at(9)},
		}
		Assign(ts, mustCompile(t, hexPattern))
		got := byName(ts)
		f := got["orders_aaaaaaaaaaaaaaaaaaaaaaaa"]
		if f == nil || f.Name != "orders_*" || f.Count != 3 || f.Date || f.Newest != "orders_bbbbbbbbbbbbbbbbbbbbbbbb" || !f.NewestCreated.Equal(at(3)) {
			t.Fatalf("orders_*: %+v", f)
		}
		// the text after the group names another family; ties on creation
		// time are broken by name
		h := got["orders_aaaaaaaaaaaaaaaaaaaaaaaa_history"]
		if h == nil || h.Name != "orders_*_history" || h.Suffix != "_history" || h.Count != 2 || h.Newest != "orders_dddddddddddddddddddddddd_history" {
			t.Fatalf("orders_*_history: %+v", h)
		}
		// another type, or another dataset, is not in the family
		if got["orders_eeeeeeeeeeeeeeeeeeeeeeee_history"] != nil || got["orders_ffffffffffffffffffffffff"] != nil {
			t.Fatal("a single match of its type or dataset stays a plain table")
		}
	})

	t.Run("a table with no sibling falls through to the next pattern", func(t *testing.T) {
		// the hex id ends in 8 digits, so the date pattern matches it too,
		// but under it the table has no sibling
		ts := mk("orders_0123456789abcdef01234567", "orders_aaaaaaaaaaaaaaaaaaaaaaaa", "events_20240101", "events_20240102")
		Assign(ts, mustCompile(t, datePattern, hexPattern))
		got := byName(ts)
		if f := got["orders_0123456789abcdef01234567"]; f == nil || f.Name != "orders_*" || f.Count != 2 {
			t.Fatalf("orders: %+v", f)
		}
		if f := got["events_20240101"]; f == nil || !f.Date || f.Name != "events_YYYYMMDD" {
			t.Fatalf("events: %+v", f)
		}
	})

	t.Run("pattern order decides", func(t *testing.T) {
		ts := mk("t_20240101", "t_20240102")
		Assign(ts, mustCompile(t, `(t)_\d+`, datePattern))
		// (t)_\d+ groups by the text after "t", which differs: no family,
		// so the date pattern gets them
		if f := ts[0].Shard; f == nil || !f.Date {
			t.Fatalf("%+v", f)
		}
		ts = mk("t_20240101", "t_20240102")
		Assign(ts, mustCompile(t, `t_(\d+)`, datePattern))
		if f := ts[0].Shard; f == nil || f.Name != "t_YYYYMMDD" {
			t.Fatalf("the first pattern that forms a family wins: %+v", f)
		}
	})
}

func TestSortNewestFirst(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC) }
	type m struct {
		name string
		at   time.Time
	}
	items := []m{{"a_x", at(2)}, {"a_y", at(1)}, {"a_z", at(2)}}
	SortNewestFirst(api.ShardFamily{}, items, func(x m) string { return x.name }, func(x m) time.Time { return x.at })
	if items[0].name != "a_z" || items[1].name != "a_x" || items[2].name != "a_y" {
		t.Fatalf("%+v", items)
	}
	SortNewestFirst(api.ShardFamily{Date: true}, items, func(x m) string { return x.name }, func(x m) time.Time { return x.at })
	if items[0].name != "a_z" || items[2].name != "a_x" {
		t.Fatalf("%+v", items)
	}
}
