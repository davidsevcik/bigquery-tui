package cli

import (
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

func TestSyncTableSyncsOneTablesSchema(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\nshard_patterns = ['.+_(\\d+)']\n")
	ds := &s.fake.Projects["proj-a"].Datasets[0]
	ds.Tables = append(ds.Tables, bqtest.Table("orders_1", "id", "legacy"), bqtest.Table("orders_2", "id", "total"))
	if code, out, errOut := s.run("sync"); code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	// orders_2 is the newest (equal creation times: by name)
	code, out, errOut := s.run("schema", "proj-a.sales.orders_1")
	if code != 0 || !strings.Contains(out, `"schema_from":"orders_2"`) || !strings.Contains(out, "total") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	code, out, errOut = s.run("sync", "--table", "proj-a.sales.orders_1")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	res := decode(t, out).Data.(map[string]any)
	if res["units_ok"] != float64(1) || res["units_total"] != float64(1) {
		t.Fatalf("%v", res)
	}
	code, out, _ = s.run("schema", "proj-a.sales.orders_1")
	if code != 0 || strings.Contains(out, "schema_from") || !strings.Contains(out, "legacy") {
		t.Fatalf("the member's own schema after --table: %s", out)
	}
	// the family is reported, and accepted, by its name
	code, out, _ = s.run("search", "orders_")
	if code != 0 || !strings.Contains(out, `"table":"orders_*"`) || !strings.Contains(out, `"kind":"shard_family"`) {
		t.Fatalf("%s", out)
	}
	if code, out, _ = s.run("schema", "proj-a.sales.orders_*"); code != 0 || !strings.Contains(out, "total") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ = s.run("sync", "--table", "proj-a.sales.nope"); code == 0 {
		t.Fatal("an unknown table fails")
	}
	if code, _, _ = s.run("sync", "--table", "proj-a.sales.orders_1", "--project", "proj-a"); code == 0 {
		t.Fatal("--table and --project do not combine")
	}
}
