package sqlx

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func kinds(text string) string {
	var out []string
	for _, t := range Tokenize(text) {
		out = append(out, string(t.Kind)+":"+text[t.Start:t.End])
	}
	return strings.Join(out, " | ")
}

func TestTokenize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"select a from `p.d.t`", "keyword:select | ident:a | keyword:from | quoted:`p.d.t`"},
		{"SELECT COUNT(*) -- c\nFROM x", "keyword:SELECT | function:COUNT | punct:( | punct:* | punct:) | comment:-- c | keyword:FROM | ident:x"},
		{"a /* b\nc */ 1.5e3 0xFF .5", "ident:a | comment:/* b\nc */ | number:1.5e3 | number:0xFF | number:.5"},
		{`'it''s' "q\"x" r'\d' '''a'b'''`, `string:'it' | string:'s' | string:"q\"x" | string:r'\d' | string:'''a'b'''`},
		{"@start @@session.x #c", "param:@start | param:@@session | punct:. | ident:x | comment:#c"},
		{"t.select t.from", "ident:t | punct:. | ident:select | ident:t | punct:. | ident:from"},
		{"'open\nnext", "string:'open | ident:next"},
		{"`open\nx", "quoted:`open | ident:x"},
		{"a→b", "ident:a | punct:→ | ident:b"},
		{"my_func (1)", "function:my_func | punct:( | number:1 | punct:)"},
		{"from my-proj-412345.sales.orders", "keyword:from | ident:my | punct:- | ident:proj | punct:- | ident:412345 | punct:. | ident:sales | punct:. | ident:orders"},
		{"from a, my-1st-proj.d.t", "keyword:from | ident:a | punct:, | ident:my | punct:- | ident:1st | punct:- | ident:proj | punct:. | ident:d | punct:. | ident:t"},
		{"select 1.5, x-1.0 from t limit 10", "keyword:select | number:1.5 | punct:, | ident:x | punct:- | number:1.0 | keyword:from | ident:t | keyword:limit | number:10"},
		{"from t where n-12", "keyword:from | ident:t | keyword:where | ident:n | punct:- | number:12"},
		{"from a; select x-1", "keyword:from | ident:a | punct:; | keyword:select | ident:x | punct:- | number:1"},
		{"from a; x, y-1", "keyword:from | ident:a | punct:; | ident:x | punct:, | ident:y | punct:- | number:1"},
		{"select trim(a; delete from my-proj-412345.sales.orders", "keyword:select | function:trim | punct:( | ident:a | punct:; | keyword:delete | keyword:from | ident:my | punct:- | ident:proj | punct:- | ident:412345 | punct:. | ident:sales | punct:. | ident:orders"},
		{"select extract(a; update my-proj-412345.sales.orders set x = 1", "keyword:select | keyword:extract | punct:( | ident:a | punct:; | keyword:update | ident:my | punct:- | ident:proj | punct:- | ident:412345 | punct:. | ident:sales | punct:. | ident:orders | keyword:set | ident:x | punct:= | number:1"},
		{"select substring(a; insert into my-proj-412345.sales.orders values (1)", "keyword:select | function:substring | punct:( | ident:a | punct:; | keyword:insert | keyword:into | ident:my | punct:- | ident:proj | punct:- | ident:412345 | punct:. | ident:sales | punct:. | ident:orders | keyword:values | punct:( | number:1 | punct:)"},
		{"insert into t values (1,-1), (2, x=-2)", "keyword:insert | keyword:into | ident:t | keyword:values | punct:( | number:1 | punct:, | punct:- | number:1 | punct:) | punct:, | punct:( | number:2 | punct:, | ident:x | punct:= | punct:- | number:2 | punct:)"},
	}
	for _, c := range cases {
		if got := kinds(c.in); got != c.want {
			t.Errorf("%q\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}

func TestTokenizeTimeGrowsLinearlyWithNegativeLiterals(t *testing.T) {
	values := func(rows int) string {
		var b strings.Builder
		b.WriteString("insert into t (a, b, c) values ")
		for i := range rows {
			n := strconv.Itoa(i)
			b.WriteString("(" + n + ",-" + n + ", x =-" + n + "),")
		}
		return b.String()
	}
	fastest := func(text string) time.Duration {
		best := time.Hour
		for range 5 {
			start := time.Now()
			Tokenize(text)
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := fastest(values(1000)), fastest(values(10000))
	if large > 40*small && large > 250*time.Millisecond {
		t.Fatalf("ten times the rows took %.0f times as long (%v, %v)", float64(large)/float64(small), small, large)
	}
}

func FuzzTokenize(f *testing.F) {
	for _, s := range []string{"select 1", "`a.b", "'x", "/* x", "r'''a", "@", "0x", "1e+", "a.b.c", "é`'\"", "--", "#\n", "from a-b.c-1-", "select x-y-"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		prev := 0
		for _, tk := range Tokenize(s) {
			if tk.Start < prev || tk.End <= tk.Start || tk.End > len(s) {
				t.Fatalf("bad token %+v after %d in %q", tk, prev, s)
			}
			if strings.TrimSpace(s[prev:tk.Start]) != "" {
				t.Fatalf("bytes %q between tokens are not whitespace in %q", s[prev:tk.Start], s)
			}
			prev = tk.End
		}
		if strings.TrimSpace(s[prev:]) != "" {
			t.Fatalf("trailing bytes %q are not covered in %q", s[prev:], s)
		}
		for i := 0; i <= len(s); i++ {
			Complete(s, i, testCat, Options{Forced: true})
		}
	})
}

func TestParams(t *testing.T) {
	got := Params("select @a, @B, @a, '@no', @@sys -- @c\n where x = @b")
	if strings.Join(got, ",") != "a,B" {
		t.Fatalf("%v", got)
	}
}

func TestInferParam(t *testing.T) {
	cases := map[string]string{
		"42": "INT64=42", "-7": "INT64=-7", "1.5": "FLOAT64=1.5", "true": "BOOL=true", "2024-01-31": "DATE=2024-01-31",
		"2024-01-31 10:00:00": "DATETIME=2024-01-31 10:00:00", "2024-01-31T10:00:00Z": "TIMESTAMP=2024-01-31T10:00:00Z",
		"2024-01-31 10:00:00+02:00": "TIMESTAMP=2024-01-31 10:00:00+02:00",
		"hello":                     "STRING=hello", "STRING:42": "STRING=42", "date: 2024-01-01": "DATE=2024-01-01", "a:b": "STRING=a:b", "": "STRING=",
	}
	for in, want := range cases {
		p := InferParam("x", in)
		if got := p.Type + "=" + p.Value; got != want || p.Null {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
	if p := InferParam("x", "null"); !p.Null || p.Type != "STRING" {
		t.Errorf("%+v", p)
	}
	if p := InferParam("x", "INT64:null"); !p.Null || p.Type != "INT64" {
		t.Errorf("%+v", p)
	}
}

type fakeCat struct {
	def  string
	data map[string]map[string]map[string][]string
}

func (f fakeCat) DefaultProject() string { return f.def }
func (f fakeCat) Projects() []string {
	var out []string
	for _, p := range []string{"acme-prod", "acme-staging", "exec", "my-proj-412345"} {
		if _, ok := f.data[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

func (f fakeCat) Datasets(p string) []string {
	var out []string
	for d := range f.data[p] {
		out = append(out, d)
	}
	sortStrings(out)
	return out
}

func (f fakeCat) Tables(p, d string) []TableName {
	var names []string
	for t := range f.data[p][d] {
		names = append(names, t)
	}
	sortStrings(names)
	var out []TableName
	for _, n := range names {
		out = append(out, TableName{Name: n, Type: api.TypeTable})
	}
	return out
}

func (f fakeCat) Columns(p, d, t string) []api.ColumnMeta {
	var out []api.ColumnMeta
	for i, c := range f.data[p][d][t] {
		out = append(out, api.ColumnMeta{FieldPath: c, Ordinal: i + 1, DataType: "STRING"})
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

var testCat = fakeCat{def: "exec", data: map[string]map[string]map[string][]string{
	"acme-prod": {
		"sales": {
			"orders":    {"id", "customer_id", "amount", "address", "address.city", "address.geo", "address.geo.lat", "items", "items.sku", "items.qty", "created_at"},
			"customers": {"id", "email", "country"},
			"events_*":  {"user_id", "event_name"},
		},
		"finance": {"refunds": {"id", "amount"}},
	},
	"acme-staging":   {"sales": {"orders": {"id"}}},
	"exec":           {"scratch": {"tmp": {"x", "y"}}},
	"my-proj-412345": {"sales": {"orders": {"id", "total"}}},
}}

func complete(t *testing.T, marked string, forced bool) ([]string, string) {
	t.Helper()
	at := strings.Index(marked, "|")
	if at < 0 {
		t.Fatalf("no cursor marker in %q", marked)
	}
	text := marked[:at] + marked[at+1:]
	cands := Complete(text, at, testCat, Options{Forced: forced})
	var labels []string
	for _, c := range cands {
		labels = append(labels, c.Label)
	}
	applied := ""
	if len(cands) > 0 {
		applied = text[:cands[0].Start] + cands[0].Insert + text[cands[0].End:]
	}
	return labels, applied
}

func TestComplete(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		forced  bool
		want    string
		applied string
	}{
		{name: "backtick: projects and datasets", in: "select * from `|", want: "scratch,acme-prod,acme-staging,exec,my-proj-412345,finance,sales,sales,sales"},
		{name: "backtick: project prefix", in: "select * from `acme-p|", want: "acme-prod", applied: "select * from `acme-prod"},
		{name: "backtick: datasets of a project", in: "select * from `acme-prod.|", want: "finance,sales"},
		{name: "backtick: tables", in: "select * from `acme-prod.sales.|`", want: "customers,events_*,orders"},
		{name: "backtick: table prefix replaces the segment", in: "select * from `acme-prod.sales.cu|xx`", want: "customers", applied: "select * from `acme-prod.sales.customers`"},
		{name: "backtick: dataset then tables", in: "from `scratch.|", want: "tmp"},
		{name: "backtick: dataset of another project inserts the project", in: "from `fin|", want: "finance", applied: "from `acme-prod.finance"},
		{name: "unquoted: needs two characters", in: "select * from s|", want: ""},
		{name: "unquoted: datasets after two characters", in: "select * from sc|", want: "scratch", applied: "select * from scratch"},
		{name: "unquoted: forced lists everything", in: "select * from |", forced: true, want: "scratch,acme-prod,…"},
		{name: "unquoted: tables after dataset dot", in: "select * from scratch.|", want: "tmp"},
		{name: "unquoted: hyphenated project", in: "select * from acme-prod.sales.o|", want: "orders", applied: "select * from acme-prod.sales.orders"},
		{name: "unquoted: a wildcard table gets backticks", in: "select * from acme-prod.sales.ev|", want: "events_*", applied: "select * from `acme-prod.sales.events_*`"},
		{name: "unquoted: a dashed project is one word", in: "select * from acme-pr|", want: "acme-prod", applied: "select * from acme-prod"},
		{name: "unquoted: datasets of a dashed project", in: "select * from acme-prod.sa|", want: "sales", applied: "select * from acme-prod.sales"},
		{name: "unquoted: a dashed project right after its dash", in: "select * from acme-|", want: "acme-prod,acme-staging", applied: "select * from acme-prod"},
		{name: "unquoted: a dashed project replaces the rest of its name", in: "select * from acme|-pr", want: "acme-prod,acme-staging", applied: "select * from acme-prod"},
		{name: "unquoted: a dashed project after join", in: "select * from scratch.tmp join acme-st|", want: "acme-staging", applied: "select * from scratch.tmp join acme-staging"},
		{name: "unquoted: a dashed project that ends in digits", in: "select * from my-proj-4123|", want: "my-proj-412345", applied: "select * from my-proj-412345"},
		{name: "unquoted: datasets after a project that ends in digits", in: "select * from my-proj-412345.|", want: "sales"},
		{name: "unquoted: dataset prefix after a project that ends in digits", in: "select * from my-proj-412345.sa|", want: "sales", applied: "select * from my-proj-412345.sales"},
		{name: "unquoted: tables of a project that ends in digits", in: "select * from my-proj-412345.sales.o|", want: "orders", applied: "select * from my-proj-412345.sales.orders"},
		{name: "unquoted: columns of a table in a project that ends in digits", in: "select t| from my-proj-412345.sales.orders", forced: true, want: "total,…"},
		{name: "an unclosed call in an earlier statement: delete from", in: "select trim(a; delete from my-proj-412345.sa|", want: "sales", applied: "select trim(a; delete from my-proj-412345.sales"},
		{name: "an unclosed call in an earlier statement: update", in: "select extract(a; update my-proj-412345.sales.o|", want: "orders", applied: "select extract(a; update my-proj-412345.sales.orders"},
		{name: "an unclosed call in an earlier statement: insert into", in: "select substring(a; insert into my-proj-4123|", want: "my-proj-412345", applied: "select substring(a; insert into my-proj-412345"},
		{name: "a dash in the select list is a minus", in: "select a-b|", want: ""},
		{name: "a dash in the select list is a minus when forced", in: "select a-b|", forced: true, want: "BETWEEN,…", applied: "select a-BETWEEN"},
		{name: "a column after a minus", in: "select a-cu| from acme-prod.sales.orders", want: "customer_id,CURRENT ROW,…", applied: "select a-customer_id from acme-prod.sales.orders"},
		{name: "join is a table position", in: "select * from scratch.tmp join sc|", want: "scratch"},
		{name: "comma join is a table position", in: "select * from scratch.tmp, sc|", want: "scratch"},
		{name: "select list comma is not a table position", in: "select xx, sc| from scratch.tmp", want: ""},
		{name: "columns of the from table", in: "select cu| from `acme-prod.sales.orders`", want: "customer_id,CURRENT ROW,…"},
		{name: "columns by substring rank after prefix", in: "select am| from `acme-prod.sales.orders`", want: "amount"},
		{name: "alias dot lists columns", in: "select o.| from `acme-prod.sales.orders` o", want: "address,amount,created_at,customer_id,id,items"},
		{name: "alias with AS", in: "select o.cu| from `acme-prod.sales.orders` AS o", want: "customer_id", applied: "select o.customer_id from `acme-prod.sales.orders` AS o"},
		{name: "table name as qualifier", in: "select orders.a| from acme-prod.sales.orders", want: "address,amount"},
		{name: "struct path", in: "select o.address.| from `acme-prod.sales.orders` o", want: "city,geo"},
		{name: "nested struct path", in: "select o.address.geo.| from `acme-prod.sales.orders` o", want: "lat"},
		{name: "struct column without alias", in: "select address.| from `acme-prod.sales.orders`", want: "city,geo"},
		{name: "two tables", in: "select c.| from `acme-prod.sales.orders` o join `acme-prod.sales.customers` c on o.customer_id = c.id", want: "country,email,id"},
		{name: "where clause", in: "select 1 from scratch.tmp t where t.|", want: "x,y"},
		{name: "unnest alias", in: "select i.| from `acme-prod.sales.orders` o, unnest(o.items) as i", want: "qty,sku"},
		{name: "unnest without table alias", in: "select it.s| from `acme-prod.sales.orders`, unnest(items) it", want: "sku"},
		{name: "cte name in from", in: "with recent as (select 1) select * from re|", want: "recent"},
		{name: "cte columns from aliases", in: "with r as (select id as order_id, amount, count(*) n, o.address.city from `acme-prod.sales.orders` o) select r.| from r", want: "amount,city,n,order_id"},
		{name: "cte star", in: "with r as (select * from scratch.tmp) select r.| from r", want: "x,y"},
		{name: "cte star except", in: "with r as (select * except (x) from scratch.tmp) select r.| from r", want: "y"},
		{name: "cte qualified star", in: "with r as (select c.*, o.amount from `acme-prod.sales.orders` o join `acme-prod.sales.customers` c on true) select r.| from r", want: "amount,country,email,id"},
		{name: "second cte", in: "with a as (select 1 as one), b as (select one as uno from a) select b.| from b", want: "uno"},
		{name: "subquery sees only its own tables", in: "select * from scratch.tmp t where exists (select 1 from `acme-prod.finance.refunds` r where am|)", want: "amount"},
		{name: "subquery can use the outer alias", in: "select * from scratch.tmp t where exists (select 1 from `acme-prod.finance.refunds` r where t.|)", want: "x,y"},
		{name: "outer query does not see the subquery's tables", in: "select am| from scratch.tmp where x in (select id from `acme-prod.finance.refunds`)", want: ""},
		{name: "keywords", in: "sel|", want: "SELECT"},
		{name: "keyword phrase", in: "select 1 from scratch.tmp gro|", want: "GROUP BY", applied: "select 1 from scratch.tmp GROUP BY"},
		{name: "function gets a parenthesis", in: "select regexp_c|", want: "REGEXP_CONTAINS", applied: "select REGEXP_CONTAINS("},
		{name: "one typed character is not enough", in: "select c| from scratch.tmp", want: ""},
		{name: "no popup in a string", in: "select 'se|' from x", want: ""},
		{name: "no popup in a comment", in: "select 1 -- sel|", want: ""},
		{name: "no popup after a number", in: "select 12|", want: ""},
		{name: "extract from is not a table position", in: "select extract(year from cr|) from `acme-prod.sales.orders`", want: "created_at,CREATE OR REPLACE TABLE,…"},
		{name: "second statement has its own scope", in: "select 1 from `acme-prod.sales.orders`; select t.| from scratch.tmp t", want: "x,y"},
		{name: "a fully typed word closes the popup", in: "SELECT| ", want: ""},
		{name: "broken input does not fail", in: "select ((( from `acme-prod.sales.orders` o where o.|", want: "address,amount,created_at,customer_id,id,items"},
		{name: "update target", in: "update scratch.tmp set x = 1 where |", forced: true, want: "x,y,…"},
	}
	for _, c := range cases {
		labels, applied := complete(t, c.in, c.forced)
		got := strings.Join(labels, ",")
		want := c.want
		if strings.HasSuffix(want, ",…") {
			want = strings.TrimSuffix(want, ",…")
			if !strings.HasPrefix(got+",", want+",") {
				t.Errorf("%s\n  %s\n  got  %s\n  want %s…", c.name, c.in, got, want)
			}
		} else if got != want {
			t.Errorf("%s\n  %s\n  got  %s\n  want %s", c.name, c.in, got, want)
		}
		if c.applied != "" && applied != c.applied {
			t.Errorf("%s: accepting gives %q, want %q", c.name, applied, c.applied)
		}
	}
}

func TestACandidateSpellingTheDashedTextAroundTheWordReplacesItToo(t *testing.T) {
	for _, c := range []struct{ name, marked, label, want string }{
		{"the dashed text before the word", "select my-ord| from acme-prod.sales.my-orders", "my-orders", "select my-orders from acme-prod.sales.my-orders"},
		{"the dashed text after the word", "select my|-ord from acme-prod.sales.my-orders", "my-orders", "select my-orders from acme-prod.sales.my-orders"},
		{"the dashed text on both sides", "select my-bi|g-or from acme-prod.sales.my-big-orders", "my-big-orders", "select my-big-orders from acme-prod.sales.my-big-orders"},
		{"a keyword after a minus replaces only the word", "select my-ord| from acme-prod.sales.my-orders", "ORDER BY", "select my-ORDER BY from acme-prod.sales.my-orders"},
		{"a function between minuses replaces only the word", "select my-bi|g-or from acme-prod.sales.my-big-orders", "BIT_COUNT", "select my-BIT_COUNT(-or from acme-prod.sales.my-big-orders"},
	} {
		at := strings.Index(c.marked, "|")
		text := c.marked[:at] + c.marked[at+1:]
		got := "not offered"
		for _, cand := range Complete(text, at, testCat, Options{}) {
			if cand.Label == c.label {
				got = text[:cand.Start] + cand.Insert + text[cand.End:]
			}
		}
		if got != c.want {
			t.Errorf("%s: accepting %s in %q gives %q, want %q", c.name, c.label, c.marked, got, c.want)
		}
	}
}
