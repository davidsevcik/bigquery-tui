package bq

import (
	"strings"
	"testing"
)

func TestScopeViewRefs(t *testing.T) {
	r := regionScope("my-proj", "EU")
	if got := r.view("TABLES"); got != "`my-proj`.`region-eu`.INFORMATION_SCHEMA.TABLES" {
		t.Fatal(got)
	}
	d := datasetScope("my-proj", "sales")
	if got := d.view("COLUMNS"); got != "`my-proj`.`sales`.INFORMATION_SCHEMA.COLUMNS" {
		t.Fatal(got)
	}
	if got := regionScope("p", "europe-west2").view("VIEWS"); !strings.Contains(got, "`region-europe-west2`") {
		t.Fatal(got)
	}
}

func TestScopeFilters(t *testing.T) {
	r, d := regionScope("p", "US"), datasetScope("p", "ds")
	if !strings.Contains(r.tablesSQL(), "table_schema IN UNNEST(@ds)") {
		t.Fatal("region queries filter by the @ds parameter")
	}
	if strings.Contains(d.tablesSQL(), "@ds") {
		t.Fatal("dataset queries have no parameter")
	}
	if _, ok := d.datasetsSQL(); ok {
		t.Fatal("SCHEMATA_OPTIONS is region-only")
	}
	q, ok := r.datasetsSQL()
	if !ok || !strings.Contains(q, "SCHEMATA_OPTIONS") || !strings.Contains(q, "schema_name IN UNNEST(@ds)") {
		t.Fatal(q)
	}
}

// The Go client reads a result through one Storage Read stream, in order,
// when ORDER BY appears in the SQL outside parentheses. The field paths need
// that; the other views do not and keep parallel streams.
func TestFieldPathsAreReadInOrder(t *testing.T) {
	rootHasOrderBy := func(sql string) bool {
		depth, root := 0, ""
		for _, c := range sql {
			switch {
			case c == '(':
				depth++
			case c == ')':
				depth--
			case depth == 0:
				root += string(c)
			}
		}
		return strings.Contains(strings.ToUpper(root), "ORDER BY")
	}
	for _, s := range []scope{regionScope("p", "US"), datasetScope("p", "ds")} {
		if q := s.fieldPathsSQL(); !rootHasOrderBy(q) || strings.ContainsAny(readInOrder, "()") {
			t.Fatalf("not read in order:\n%s", q)
		}
		for _, q := range []string{s.tablesSQL(), s.columnsSQL(), s.tableOptionsSQL(), s.viewsSQL()} {
			if rootHasOrderBy(q) {
				t.Fatalf("read in order without need:\n%s", q)
			}
		}
	}
}

func TestQuoteIdentEscapesBackticks(t *testing.T) {
	if got := quoteIdent("a`b"); got != "`a\\`b`" {
		t.Fatal(got)
	}
}

func TestTableOptionsAskForTheRequiredPartitionFilter(t *testing.T) {
	for _, s := range []scope{regionScope("p", "US"), datasetScope("p", "ds")} {
		if q := s.tableOptionsSQL(); !strings.Contains(q, "'require_partition_filter'") {
			t.Fatal(q)
		}
	}
}
