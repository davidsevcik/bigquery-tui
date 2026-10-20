package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

func (r *rig) handoffText(id string) string {
	r.t.Helper()
	for _, it := range r.m.handoffItems() {
		if it.id == id {
			s, err := it.make(context.Background())
			if err != nil {
				r.t.Fatal(err)
			}
			return s
		}
	}
	r.t.Fatalf("no hand-off item %s", id)
	return ""
}

func TestHandoffOfARunAndItsResult(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(30) }
	r.typeText("select n, label from t")
	r.press("ctrl+r")
	r.contains("#1 done")

	r.press("ctrl+x", "y")
	r.contains("Hand-off to an agent", "First 20 rows of run #1 (md)", "(ndjson)", "Run #1: SQL")
	r.lacks("Query and error")
	r.press("enter")
	r.contains("copied first 20 rows of run #1 (md)")

	md := r.handoffText("hosamplemd")
	if strings.Count(md, "\n") != 22 || !strings.Contains(md, "| 19 | row 19 |") {
		t.Fatalf("%s", md)
	}
	if nd := r.handoffText("hosamplendjson"); !strings.HasPrefix(nd, `{"n":0,"label":"row 0"}`) {
		t.Fatal(nd)
	}
	if run := r.handoffText("horun"); !strings.Contains(run, "select n, label from t") || !strings.Contains(run, "30 rows") {
		t.Fatal(run)
	}

	// w writes a file, after asking about an existing one
	path := filepath.Join(t.TempDir(), "sub", "run.md")
	r.press("ctrl+x", "y", "j", "j", "j", "w")
	r.contains("Write Run #1", ".bqt-handoff/run-1.md")
	r.m.prompt.fields[0].set(path)
	r.press("enter")
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "BigQuery Run: done") {
		t.Fatalf("%v %q", err, b)
	}
	r.contains("wrote " + path)
	r.press("ctrl+x", "y", "j", "j", "j", "w")
	r.m.prompt.fields[0].set(path)
	r.press("enter")
	r.contains("the file exists")
	r.press("enter")
	r.contains("wrote " + path)
}

func TestHandoffOfAFailedQuery(t *testing.T) {
	r := editorRig(t, 120, 36)
	r.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Unrecognized name: nope at [1:8]")}
	}
	r.typeText("select nope")
	r.press("ctrl+r")
	r.press("ctrl+x", "y")
	r.contains("Query and error of run #1")
	r.lacks("First 20 rows")
	text := r.handoffText("hoerror")
	if !strings.Contains(text, "at line 1, column 8") || !strings.Contains(text, "Execution project acme-exec") || !strings.Contains(text, "select nope") {
		t.Fatal(text)
	}
	r.press("esc")
	// a palette command copies directly; one that does not apply says why
	r.m.handoffDirect("hosamplemd")
	if !strings.Contains(r.m.toast, "no result rows") {
		t.Fatal(r.m.toast)
	}
}

func TestHandoffOfTheSelectedTable(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "orders")
	r.press("enter")
	r.press("ctrl+x", "y")
	r.contains("Schema of", "(markdown)", "(JSON, as bqt schema)")
	md := r.handoffText("hoschemamd")
	if !strings.Contains(md, "| column | type | description |") || !strings.Contains(md, "orders") {
		t.Fatal(md)
	}
	if js := r.handoffText("hoschemajson"); !strings.Contains(js, `"columns"`) {
		t.Fatal(js)
	}
	r.press("esc", "ctrl+p")
	r.typeText("hand-off: copy schema (markdown)")
	r.press("enter")
	r.contains("copied schema of")
}

func TestHandoffFileIsNotReplacedUnasked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.md")
	if err := writeHandoff(path, "one", false); err != nil {
		t.Fatal(err)
	}
	// the file appeared between the prompt's check and the write
	if err := writeHandoff(path, "two", false); err == nil || !strings.Contains(err.Error(), "created meanwhile") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "one" {
		t.Fatalf("%q", b)
	}
	if err := writeHandoff(path, "three", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "three" {
		t.Fatalf("%q", b)
	}
}
