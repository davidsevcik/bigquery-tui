package bq

import (
	"strings"
	"testing"

	bqv2 "google.golang.org/api/bigquery/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func TestColumnFilterClause(t *testing.T) {
	if got := (ColumnFilter{}).clause(); got != "TRUE" || len((ColumnFilter{}).params()) != 0 {
		t.Fatalf("no patterns: today's query, got %q", got)
	}
	f := ColumnFilter{Pattern: `^(?:.+(\d{8}))$`}
	if got := f.clause(); !strings.Contains(got, "NOT REGEXP_CONTAINS(table_name, @shard_re)") || !strings.Contains(got, "IN UNNEST(@keep)") {
		t.Fatalf("%q", got)
	}
	ps := f.params()
	if len(ps) != 2 || ps[1].Name != "keep" {
		t.Fatalf("%+v", ps)
	}
	if keep, ok := ps[1].Value.([]string); !ok || keep == nil {
		t.Fatalf("an empty keep list is still a typed array: %#v", ps[1].Value)
	}
	only := ColumnFilter{Pattern: "x", Only: []string{"d.t"}}
	if got := only.clause(); got != "CONCAT(table_schema, '.', table_name) IN UNNEST(@only)" || only.params()[0].Name != "only" {
		t.Fatalf("%q", got)
	}
	sc := regionScope("p", "EU")
	if q := sc.columnsSQL(f.clause()); !strings.Contains(q, "table_schema IN UNNEST(@ds) AND (NOT REGEXP_CONTAINS") {
		t.Fatal(q)
	}
}

func TestTableMetaOfMatchesTheViews(t *testing.T) {
	tb := &bqv2.Table{
		TableReference: &bqv2.TableReference{TableId: "orders_1"},
		Type:           "TABLE", Description: "one shard", CreationTime: 1735689600000,
		TimePartitioning: &bqv2.TimePartitioning{Field: "day", RequirePartitionFilter: true},
		Clustering:       &bqv2.Clustering{Fields: []string{"id"}},
		Schema: &bqv2.TableSchema{Fields: []*bqv2.TableFieldSchema{
			{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
			{Name: "day", Type: "DATE"},
			{Name: "addr", Type: "RECORD", Mode: "REQUIRED", Fields: []*bqv2.TableFieldSchema{
				{Name: "city", Type: "STRING", Mode: "REQUIRED", Description: "town"},
				{Name: "tags", Type: "STRING", Mode: "REPEATED"},
			}},
			{Name: "ok", Type: "BOOLEAN"},
		}},
	}
	m := tableMetaOf("ds", tb)
	if m.Name != "orders_1" || m.Dataset != "ds" || m.Type != api.TypeTable || !m.RequirePartitionFilter || m.CreatedAt.Year() != 2025 {
		t.Fatalf("%+v", m)
	}
	got := map[string]api.ColumnMeta{}
	var order []string
	for _, c := range m.Columns {
		got[c.FieldPath] = c
		order = append(order, c.FieldPath)
	}
	if strings.Join(order, ",") != "id,day,addr,addr.city,addr.tags,ok" {
		t.Fatalf("%v", order)
	}
	if c := got["id"]; c.DataType != "INT64" || !c.Required || c.ClusteringOrdinal != 1 || c.Ordinal != 1 {
		t.Fatalf("id %+v", c)
	}
	if c := got["day"]; !c.Partitioning {
		t.Fatalf("day %+v", c)
	}
	if c := got["addr"]; c.DataType != "STRUCT<city STRING, tags ARRAY<STRING>>" || !c.Required {
		t.Fatalf("addr %+v", c)
	}
	// the views carry no mode for nested fields
	if c := got["addr.city"]; c.Required || c.Description != "town" || c.Ordinal != 1 {
		t.Fatalf("addr.city %+v", c)
	}
	if c := got["addr.tags"]; c.DataType != "ARRAY<STRING>" || c.Ordinal != 2 {
		t.Fatalf("addr.tags %+v", c)
	}
	if got["ok"].DataType != "BOOL" {
		t.Fatalf("%+v", got["ok"])
	}
	v := tableMetaOf("ds", &bqv2.Table{TableReference: &bqv2.TableReference{TableId: "v"}, Type: "VIEW", View: &bqv2.ViewDefinition{Query: "SELECT 1"}})
	if v.Type != api.TypeView || v.ViewSQL != "SELECT 1" {
		t.Fatalf("%+v", v)
	}
}
