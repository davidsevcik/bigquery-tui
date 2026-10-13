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

func TestQuoteIdentEscapesBackticks(t *testing.T) {
	if got := quoteIdent("a`b"); got != "`a\\`b`" {
		t.Fatal(got)
	}
}
