package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func kinds(items []Item) []Kind {
	out := make([]Kind, len(items))
	for i, it := range items {
		out[i] = it.Kind
	}
	return out
}

func TestItemsFollowTheSituation(t *testing.T) {
	if got := Items(Situation{}); len(got) != 0 {
		t.Fatalf("nothing selected, no Run: %v", kinds(got))
	}
	if got := Items(Situation{Table: &api.ObjectPath{Project: "p", Dataset: "d"}}); len(got) != 0 {
		t.Fatalf("a dataset has no schema to hand off: %v", kinds(got))
	}

	table := &api.ObjectPath{Project: "my-proj", Dataset: "sales", Table: "orders_*"}
	failed := &api.RunInfo{ID: 3, State: api.RunFailed, Error: &api.QueryError{Message: "bad"}}
	got := Items(Situation{Table: table, Run: failed})
	if strings.Join(stringsOf(kinds(got)), " ") != "schema-markdown schema-json query-error run" {
		t.Fatalf("%v", kinds(got))
	}
	if got[0].Label != "Schema of my-proj.sales.orders_* (markdown)" || got[0].File != "my-proj.sales.orders__.md" || got[0].Table != *table {
		t.Fatalf("%+v", got[0])
	}
	if got[1].File != "my-proj.sales.orders__.json" || got[2].File != "query-error.md" || got[2].Label != "Query and error of run #3" {
		t.Fatalf("%+v %+v", got[1], got[2])
	}
	if got[3].Label != "Run #3: SQL, estimate, state, outcome" || got[3].File != "run-3.md" || got[3].Run.ID != 3 {
		t.Fatalf("%+v", got[3])
	}

	done := &api.RunInfo{ID: 7, State: api.RunDone, HasResult: true}
	got = Items(Situation{Run: done})
	if strings.Join(stringsOf(kinds(got)), " ") != "rows-markdown rows-tsv rows-ndjson run" {
		t.Fatalf("%v", kinds(got))
	}
	for i, want := range []struct {
		label, file string
		f           SampleFormat
	}{{"First 20 rows of run #7 (md)", "sample-7.md", Markdown}, {"First 20 rows of run #7 (tsv)", "sample-7.tsv", TSV}, {"First 20 rows of run #7 (ndjson)", "sample-7.ndjson", NDJSON}} {
		if got[i].Label != want.label || got[i].File != want.file || got[i].RowsFormat() != want.f {
			t.Fatalf("%+v", got[i])
		}
	}
	if got[3].RowsFormat() != "" || got[3].DefaultPath() != filepath.Join(".bqt-handoff", "run-7.md") {
		t.Fatalf("%+v %s", got[3], got[3].DefaultPath())
	}
}

func stringsOf(ks []Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

func TestMissingSaysWhatIsNeeded(t *testing.T) {
	for k, want := range map[Kind]Need{
		KindSchemaMarkdown: NeedTable, KindSchemaJSON: NeedTable, KindQueryError: NeedRunError,
		KindRowsMarkdown: NeedRunRows, KindRowsTSV: NeedRunRows, KindRowsNDJSON: NeedRunRows, KindRun: NeedRun,
	} {
		if got := Missing(k); got != want {
			t.Errorf("%s: %v", k, got)
		}
	}
}

func TestWriteCreatesExclusively(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "run-1.md")
	if err := Write(path, "one", false); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, "two", false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a file that appeared meanwhile is not replaced: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "one" {
		t.Fatalf("%q", b)
	}
	if err := Write(path, "three", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "three" {
		t.Fatalf("%q", b)
	}
}
