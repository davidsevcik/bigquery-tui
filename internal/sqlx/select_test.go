package sqlx

import (
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func detail(table string, mut func(*api.TableMeta)) api.TableDetail {
	t := api.TableMeta{Dataset: "sales", Name: table, Type: api.TypeTable,
		Columns: []api.ColumnMeta{{FieldPath: "id", Ordinal: 1, DataType: "INT64"}}}
	if mut != nil {
		mut(&t)
	}
	return api.TableDetail{Path: api.ObjectPath{Project: "acme-prod", Dataset: "sales", Table: table}, Table: t}
}

func partitionedOn(col, typ string, required bool) func(*api.TableMeta) {
	return func(t *api.TableMeta) {
		t.RequirePartitionFilter = required
		t.Columns = append(t.Columns, api.ColumnMeta{FieldPath: col, Ordinal: 2, DataType: typ, Partitioning: true})
	}
}

func TestSelectStar(t *testing.T) {
	cases := []struct {
		name string
		in   api.TableDetail
		from string
		cond string
	}{
		{"plain table", detail("customers", nil), "`acme-prod.sales.customers`", "true"},
		{"required DATE", detail("orders", partitionedOn("day", "DATE", true)), "`acme-prod.sales.orders`",
			"day >= DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)"},
		{"required TIMESTAMP", detail("orders", partitionedOn("created_at", "TIMESTAMP", true)), "`acme-prod.sales.orders`",
			"created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"},
		{"required DATETIME", detail("orders", partitionedOn("event_at", "DATETIME", true)), "`acme-prod.sales.orders`",
			"event_at >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 1 DAY)"},
		{"required INT64", detail("orders", partitionedOn("bucket", "INT64", true)), "`acme-prod.sales.orders`",
			"bucket BETWEEN 0 AND 0  -- set the range"},
		{"required INTEGER", detail("orders", partitionedOn("bucket", "INTEGER", true)), "`acme-prod.sales.orders`",
			"bucket BETWEEN 0 AND 0  -- set the range"},
		{"required, other type", detail("orders", partitionedOn("s", "STRING", true)), "`acme-prod.sales.orders`", "true"},
		{"required, no partition column", detail("ingest", func(t *api.TableMeta) { t.RequirePartitionFilter = true }),
			"`acme-prod.sales.ingest`", "_PARTITIONTIME >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"},
		{"required, only the pseudo-column", detail("ingest", partitionedOn("_PARTITIONTIME", "TIMESTAMP", true)),
			"`acme-prod.sales.ingest`", "_PARTITIONTIME >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"},
		{"required, _PARTITIONDATE before the real column", detail("orders", func(t *api.TableMeta) {
			t.RequirePartitionFilter = true
			t.Columns = append(t.Columns, api.ColumnMeta{FieldPath: "_PARTITIONDATE", DataType: "DATE", Partitioning: true},
				api.ColumnMeta{FieldPath: "day", DataType: "DATE", Partitioning: true})
		}), "`acme-prod.sales.orders`", "day >= DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)"},
		{"partitioned, not required", detail("orders", partitionedOn("created_at", "TIMESTAMP", false)), "`acme-prod.sales.orders`", "true"},
		{"view", detail("v_orders", func(t *api.TableMeta) { t.Type = api.TypeView }), "`acme-prod.sales.v_orders`", "true"},
		{"names-only table", detail("old", func(t *api.TableMeta) { t.NamesOnly, t.Columns = true, nil }), "`acme-prod.sales.old`", "true"},
		{"column needing backticks", detail("orders", partitionedOn("select", "DATE", true)), "`acme-prod.sales.orders`",
			"`select` >= DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)"},
		{"column with a dash", detail("orders", partitionedOn("order-day", "DATE", true)), "`acme-prod.sales.orders`",
			"`order-day` >= DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)"},
		{"shard family", func() api.TableDetail {
			d := detail("events_YYYYMMDD", partitionedOn("day", "DATE", true))
			d.Table.Shard = &api.ShardFamily{Prefix: "events_", First: "20240101", Last: "20240104", Count: 4}
			return d
		}(), "`acme-prod.sales.events_*`", "_TABLE_SUFFIX = '20240104'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, cursor := SelectStar(c.in)
			want := "SELECT *\nFROM " + c.from + "\nWHERE " + c.cond + "\nLIMIT 100"
			if text != want {
				t.Fatalf("got\n%s\nwant\n%s", text, want)
			}
			if !strings.HasPrefix(text[cursor:], c.cond+"\n") || !strings.HasSuffix(text[:cursor], "\nWHERE ") {
				t.Fatalf("cursor %d is not at the condition: %q", cursor, text[cursor:])
			}
		})
	}
}

func TestQuoteTable(t *testing.T) {
	cases := []struct {
		in   api.ObjectPath
		want string
	}{
		{api.ObjectPath{Project: "acme-prod", Dataset: "sales", Table: "orders"}, "`acme-prod.sales.orders`"},
		{api.ObjectPath{Project: "p", Dataset: "d", Table: "events_YYYYMMDD"}, "`p.d.events_*`"},
		{api.ObjectPath{Project: "p", Dataset: "d", Table: "events_20240101"}, "`p.d.events_20240101`"},
	}
	for _, c := range cases {
		if got := QuoteTable(c.in); got != c.want {
			t.Errorf("%v: got %s want %s", c.in, got, c.want)
		}
	}
}
