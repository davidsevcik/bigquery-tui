package sync

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

const (
	cfgDate    = "shard_patterns = ['.+(\\d{8})']\n"
	cfgHex     = "shard_patterns = ['.+_([0-9a-f]{24})(?:_history)?']\n"
	cfgDateHex = "shard_patterns = ['.+(\\d{8})', '.+_([0-9a-f]{24})(?:_history)?']\n"
)

// hexID makes a 24-hex-digit object id from a short seed.
func hexID(seed string) string { return strings.Repeat("0", 24-len(seed)) + seed }

func createdOn(day int, t api.TableMeta) api.TableMeta {
	t.CreatedAt = time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC)
	return t
}

func (h *harness) tables(dataset string) map[string]api.TableMeta {
	h.t.Helper()
	ts, err := h.cat.Tables(ctx, "p", dataset)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]api.TableMeta{}
	for _, t := range ts {
		out[t.Name] = t
	}
	return out
}

func (h *harness) columns(dataset, table string) []string {
	h.t.Helper()
	cols, err := h.cat.Columns(ctx, "p", dataset, table)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, c := range cols {
		out = append(out, c.FieldPath)
	}
	return out
}

func TestSyncWithoutPatternsCollapsesNothing(t *testing.T) {
	h := newHarness(t, `projects = ["p"]`)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("ev", "EU",
		bqtest.Table("events_20240101", "c"), bqtest.Table("events_20240102", "c"))}
	h.run(api.SyncScope{})
	for name, tb := range h.tables("ev") {
		if tb.Shard != nil || tb.SharedSchema {
			t.Fatalf("%s: no pattern, no family: %+v", name, tb.Shard)
		}
		if got := h.columns("ev", name); len(got) != 1 {
			t.Fatalf("%s keeps its columns: %v", name, got)
		}
	}
	for _, c := range h.fake.CallsTo("RegionColumns") {
		if c.Filter.Pattern != "" || len(c.Filter.Keep)+len(c.Filter.Only) > 0 {
			t.Fatalf("no patterns: the column queries are today's, got %+v", c.Filter)
		}
	}
}

func TestSyncDatePatternAndANonDateSuffix(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgDate)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("ev", "EU",
		bqtest.Table("events_20240101", "c"), bqtest.Table("events_20240102", "c", "d"),
		bqtest.Table("codes_12345678", "x"), bqtest.Table("codes_20240101", "x"))}
	h.run(api.SyncScope{})
	ts := h.tables("ev")
	ev := ts["events_20240101"].Shard
	if ev == nil || !ev.Date || ev.Name != "events_YYYYMMDD" || ev.First != "20240101" || ev.Last != "20240102" || ev.Count != 2 || ev.Newest != "events_20240102" {
		t.Fatalf("date family: %+v", ev)
	}
	if got := h.columns("ev", "events_20240102"); len(got) != 2 {
		t.Fatalf("the newest shard has its columns: %v", got)
	}
	if got := h.columns("ev", "events_20240101"); len(got) != 0 || !ts["events_20240101"].SharedSchema {
		t.Fatalf("an older shard uses the family's schema: %v %+v", got, ts["events_20240101"])
	}
	// 12345678 is not a date: the family is a non-date one, newest by
	// creation time (equal here, so by name)
	codes := ts["codes_12345678"].Shard
	if codes == nil || codes.Date || codes.Name != "codes_*" || codes.First != "" || codes.Newest != "codes_20240101" {
		t.Fatalf("non-date family: %+v", codes)
	}
}

func TestSyncHexFamiliesReadColumnsOnlyForTheNewestShard(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\nignore = [\"p.shop.orders_"+hexID("dead")+"\"]\n"+cfgHex)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("shop", "EU",
		createdOn(1, bqtest.Table("orders_"+hexID("a1"), "id")),
		createdOn(3, bqtest.Table("orders_"+hexID("a2"), "id", "total")),
		createdOn(2, bqtest.Table("orders_"+hexID("a3"), "id")),
		createdOn(9, bqtest.Table("orders_"+hexID("dead"), "id")), // ignored: not a member
		createdOn(1, bqtest.Table("orders_"+hexID("a1")+"_history", "id", "at")),
		createdOn(2, bqtest.Table("orders_"+hexID("a2")+"_history", "id", "at")),
		createdOn(1, bqtest.Table("reviews_"+hexID("b1"), "r")), // a single match
		bqtest.Table("customers", "id"),
	)}
	h.run(api.SyncScope{})
	ts := h.tables("shop")
	if _, ok := ts["orders_"+hexID("dead")]; ok {
		t.Fatal("ignored tables never enter the catalog")
	}
	fam := ts["orders_"+hexID("a1")].Shard
	if fam == nil || fam.Name != "orders_*" || fam.Count != 3 || fam.Date || fam.Newest != "orders_"+hexID("a2") || fam.NewestCreated.Day() != 3 {
		t.Fatalf("orders_*: %+v", fam)
	}
	hist := ts["orders_"+hexID("a1")+"_history"].Shard
	if hist == nil || hist.Name != "orders_*_history" || hist.Count != 2 || hist.Newest != "orders_"+hexID("a2")+"_history" {
		t.Fatalf("orders_*_history is its own family: %+v", hist)
	}
	if ts["reviews_"+hexID("b1")].Shard != nil || ts["customers"].Shard != nil {
		t.Fatal("a single match and a plain table are not families")
	}
	want := map[string]int{
		"orders_" + hexID("a2"): 2, "orders_" + hexID("a2") + "_history": 2,
		"reviews_" + hexID("b1"): 1, "customers": 1,
		"orders_" + hexID("a1"): 0, "orders_" + hexID("a3"): 0, "orders_" + hexID("a1") + "_history": 0,
	}
	for name, n := range want {
		if got := h.columns("shop", name); len(got) != n {
			t.Errorf("%s: %d columns, want %d", name, len(got), n)
		}
		if shared := ts[name].SharedSchema; shared != (n == 0) {
			t.Errorf("%s: shared schema = %v", name, shared)
		}
	}
	// the column queries left the other members out on BigQuery's side
	reads := h.fake.ColumnReads()
	for _, name := range []string{"orders_" + hexID("a1"), "orders_" + hexID("a3"), "orders_" + hexID("a1") + "_history"} {
		if slices.Contains(reads, "p.shop."+name) {
			t.Errorf("columns of the collapsed member %s were requested", name)
		}
	}
	if len(h.fake.CallsTo("RegionColumns")) != 1 {
		t.Fatalf("one column call per unit when nothing is missing: %d", len(h.fake.CallsTo("RegionColumns")))
	}
}

// A hex id that ends in 8 digits matches the date pattern too; with no
// sibling under it, it falls through to the hex pattern.
func TestSyncFallThroughToTheNextPattern(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgDateHex)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("shop", "EU",
		createdOn(2, bqtest.Table("orders_0123456789abcdef01234567", "id")),
		createdOn(1, bqtest.Table("orders_"+hexID("ff"), "id")),
	)}
	h.run(api.SyncScope{})
	f := h.tables("shop")["orders_0123456789abcdef01234567"].Shard
	if f == nil || f.Name != "orders_*" || f.Date || f.Count != 2 || f.Newest != "orders_0123456789abcdef01234567" {
		t.Fatalf("%+v", f)
	}
}

// When BigQuery leaves out a table bqt kept (its regex dialect read a
// pattern differently), the table is read again by name and a warning says
// why.
func TestSyncSafetyNetRereadsMissingColumns(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgHex)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("shop", "EU",
		createdOn(1, bqtest.Table("orders_"+hexID("a1"), "id")),
		createdOn(2, bqtest.Table("orders_"+hexID("a2"), "id", "total")),
		bqtest.Table("customers", "id"),
	)}
	h.fake.MissColumns = map[string]bool{"p.shop.orders_" + hexID("a2"): true, "p.shop.customers": true}
	res := h.run(api.SyncScope{})
	if got := h.columns("shop", "orders_"+hexID("a2")); len(got) != 2 {
		t.Fatalf("the newest shard's columns were read again: %v", got)
	}
	if got := h.columns("shop", "customers"); len(got) != 1 {
		t.Fatalf("a plain table's columns were read again: %v", got)
	}
	calls := h.fake.CallsTo("RegionColumns")
	if len(calls) != 2 || len(calls[1].Filter.Only) != 2 {
		t.Fatalf("one re-read by name: %+v", calls)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "regex dialect") || !strings.Contains(res.Warnings[0], "2 table(s)") {
		t.Fatalf("warnings: %q", res.Warnings)
	}
	if u := h.unit("p", "EU"); u.Status != api.UnitOK {
		t.Fatalf("%+v", u)
	}
}

func TestSyncDatasetScopeFallbackFiltersColumnsToo(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgHex)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{
		bqtest.DS("shop", "EU",
			createdOn(1, bqtest.Table("orders_"+hexID("a1"), "id")),
			createdOn(2, bqtest.Table("orders_"+hexID("a2"), "id", "total"))),
	}
	h.fake.Fail(bqtest.Failure{Method: "RegionTables", Err: bqtest.PermissionError("region-eu")})
	h.run(api.SyncScope{})
	if got := h.columns("shop", "orders_"+hexID("a2")); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if got := h.columns("shop", "orders_"+hexID("a1")); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if slices.Contains(h.fake.ColumnReads(), "p.shop.orders_"+hexID("a1")) {
		t.Fatal("the dataset-scope column query leaves members out too")
	}
	if len(h.fake.CallsTo("DatasetColumns")) != 1 {
		t.Fatal("the fallback runs both phases in the dataset")
	}
}

func TestSyncNamesOnlyFallbackAssignsFamilies(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgDate)
	p := h.fake.AddProject("p")
	p.Datasets = []bqtest.Dataset{bqtest.DS("ev", "EU", bqtest.Table("events_20240101", "c"), bqtest.Table("events_20240102", "c"))}
	p.Datasets[0].Denied, p.Datasets[0].NamesOK = true, true
	h.run(api.SyncScope{})
	ts := h.tables("ev")
	if f := ts["events_20240101"].Shard; f == nil || f.Name != "events_YYYYMMDD" || !ts["events_20240101"].NamesOnly {
		t.Fatalf("%+v", ts["events_20240101"])
	}
}

func TestSyncTableKeepsAMembersOwnSchema(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgHex)
	odd := createdOn(1, bqtest.Table("orders_"+hexID("a1"), "id", "legacy_flag"))
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("shop", "EU",
		odd,
		createdOn(2, bqtest.Table("orders_"+hexID("a2"), "id", "total")),
		bqtest.Table("customers", "id"),
	)}
	h.run(api.SyncScope{})
	member := api.ObjectPath{Project: "p", Dataset: "shop", Table: "orders_" + hexID("a1")}
	if got := h.columns("shop", member.Table); len(got) != 0 {
		t.Fatalf("%v", got)
	}

	// gs on the member: tables.get, no job
	res := h.run(api.SyncScope{Table: &member})
	if res.UnitsOK != 1 || res.UnitsTotal != 1 || len(h.fake.CallsTo("GetTableMeta")) != 1 {
		t.Fatalf("%+v", res)
	}
	if got := h.columns("shop", member.Table); strings.Join(got, ",") != "id,legacy_flag" {
		t.Fatalf("the member's own schema: %v", got)
	}
	tb := h.tables("shop")[member.Table]
	if !tb.OwnSchema || tb.SharedSchema || tb.Shard == nil || tb.Shard.Name != "orders_*" {
		t.Fatalf("%+v", tb)
	}

	// a later unit sync keeps reading it
	h.now = h.now.Add(time.Hour)
	before := len(h.fake.ColumnReads())
	h.run(api.SyncScope{})
	if !slices.Contains(h.fake.ColumnReads()[before:], "p.shop."+member.Table) {
		t.Fatal("the marked member is on the keep list")
	}
	if got := h.columns("shop", member.Table); len(got) != 2 || !h.tables("shop")[member.Table].OwnSchema {
		t.Fatalf("the own schema survives a unit sync: %v", got)
	}

	// gs on the family's newest shard and on a plain table only refresh them
	for _, name := range []string{"orders_" + hexID("a2"), "customers"} {
		p := api.ObjectPath{Project: "p", Dataset: "shop", Table: name}
		h.run(api.SyncScope{Table: &p})
		if h.tables("shop")[name].OwnSchema {
			t.Fatalf("%s is not marked", name)
		}
	}

	// a table the catalog does not hold is an error
	nope := api.ObjectPath{Project: "p", Dataset: "shop", Table: "nope"}
	if _, err := h.syncer().Run(ctx, api.SyncScope{Table: &nope}, nil); err == nil {
		t.Fatal("an unknown table is an error")
	}
	// a table BigQuery no longer has fails the one unit
	h.fake.Projects["p"].Datasets[0].Tables = h.fake.Projects["p"].Datasets[0].Tables[1:]
	if res := h.run(api.SyncScope{Table: &member}); res.UnitsFailed != 1 {
		t.Fatalf("%+v", res)
	}

	// changing shard_patterns: sync needed, and the mark goes
	h.cfg = cfgFrom(t, cfgDateHex)
	r, err := ReconcileConfig(ctx, h.cat, h.cfg)
	if err != nil || !r.SyncNeeded || r.Reason != ReasonShardPatterns {
		t.Fatalf("%+v %v", r, err)
	}
	if pending, reason, _ := PendingConfigChange(ctx, h.cat, h.cfg); !pending || reason != ReasonShardPatterns {
		t.Fatalf("pending: %v %q", pending, reason)
	}
	if marks, _ := h.cat.OwnSchemaTables(ctx, api.UnitRef{Project: "p", Location: "EU"}); len(marks) != 0 {
		t.Fatalf("marks after a pattern change: %v", marks)
	}
}

func TestSyncTableRespectsTheLockScopeAndEvents(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n")
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("shop", "EU", bqtest.Table("customers", "id"))}
	h.run(api.SyncScope{})
	p := api.ObjectPath{Project: "p", Dataset: "shop", Table: "customers"}
	h.fake.Projects["p"].Datasets[0].Tables[0] = bqtest.Table("customers", "id", "email")
	h.run(api.SyncScope{Table: &p})
	if got := h.columns("shop", "customers"); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	var kinds []api.SyncEventKind
	for _, e := range h.events {
		kinds = append(kinds, e.Kind)
	}
	want := []api.SyncEventKind{api.EventRunStarted, api.EventUnitStarted, api.EventUnitDone, api.EventRunFinished}
	if !slices.Equal(kinds, want) {
		t.Fatalf("%v", kinds)
	}
	if len(h.fake.CallsTo("ListDatasets")) != 1 {
		t.Fatal("a table sync lists no datasets")
	}
}

// A catalog from a bqt with the built-in date rule holds date families and
// no pattern hash: with no patterns configured, bqt says once that a sync is
// needed and names the pattern that restores the old behaviour.
func TestUpgradeFromTheBuiltInDateRule(t *testing.T) {
	h := newHarness(t, "projects = [\"p\"]\n"+cfgDate)
	h.fake.AddProject("p").Datasets = []bqtest.Dataset{bqtest.DS("ev", "EU", bqtest.Table("events_20240101", "c"), bqtest.Table("events_20240102", "c"))}
	h.run(api.SyncScope{})
	// what an older bqt left behind
	for _, q := range []string{`UPDATE tables SET shard_name = ''`, `DELETE FROM meta WHERE key = 'shard_patterns_hash'`} {
		if _, err := h.cat.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h.cfg = cfgFrom(t, "")
	pending, reason, err := PendingConfigChange(ctx, h.cat, h.cfg)
	if err != nil || !pending || reason != ReasonDateRuleGone || !strings.Contains(reason, `'.+(\d{8})'`) {
		t.Fatalf("%v %q %v", pending, reason, err)
	}
	r, err := ReconcileConfig(ctx, h.cat, h.cfg)
	if err != nil || !r.SyncNeeded || r.Reason != ReasonDateRuleGone {
		t.Fatalf("%+v %v", r, err)
	}
	// once: the hash is recorded, and the flag stays until a sync
	if r, _ = ReconcileConfig(ctx, h.cat, h.cfg); r.Changed || !r.SyncNeeded {
		t.Fatalf("%+v", r)
	}
	h.run(api.SyncScope{})
	if pending, reason, _ := PendingConfigChange(ctx, h.cat, h.cfg); pending {
		t.Fatalf("the full sync clears it: %q", reason)
	}
	if h.tables("ev")["events_20240101"].Shard != nil {
		t.Fatal("without patterns the date tables are plain")
	}

	// with patterns configured an upgrade just needs the sync
	h2 := newHarness(t, "projects = [\"p\"]\n"+cfgDate)
	h2.fake = h.fake
	h2.run(api.SyncScope{})
	if _, err := h2.cat.DB().Exec(`DELETE FROM meta WHERE key = 'shard_patterns_hash'`); err != nil {
		t.Fatal(err)
	}
	if r, _ := ReconcileConfig(ctx, h2.cat, h2.cfg); !r.SyncNeeded || r.Reason != ReasonShardPatterns {
		t.Fatalf("%+v", r)
	}

	// a catalog without families and without patterns needs nothing
	h3 := newHarness(t, `projects = ["p"]`)
	h3.fake = h.fake
	h3.run(api.SyncScope{})
	if _, err := h3.cat.DB().Exec(`DELETE FROM meta WHERE key = 'shard_patterns_hash'`); err != nil {
		t.Fatal(err)
	}
	if r, _ := ReconcileConfig(ctx, h3.cat, h3.cfg); r.SyncNeeded {
		t.Fatalf("%+v", r)
	}
}
