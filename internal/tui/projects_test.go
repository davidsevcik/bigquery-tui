package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

func TestProjectsDialogTurnsProjectsOnAndOff(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p")
	r.contains("Projects", "[x] acme-prod", "[x] acme-staging", "space turns a project on or off")
	r.press("j", "space")
	r.contains("[x] acme-prod", "[ ] acme-staging", "projects acme-prod")
	r.press("esc")

	// one project on: the Miller project column is gone
	r.lacks("Projects 1/", "acme-staging")
	r.contains("Datasets 1/4", "Tables")
	if s := r.m.sel(); s.path.Project != "acme-prod" || s.path.Dataset == "" {
		t.Fatalf("the cursor starts in the datasets column: %+v", s.path)
	}
	r.press("h")
	if s := r.m.sel(); s.path.Dataset == "" {
		t.Fatal("h does not go back to a hidden project column")
	}

	// search and the tree leave the project out
	r.press("/", "orders")
	r.contains("acme-prod")
	if strings.Contains(r.screen(), "acme-staging") {
		t.Fatalf("a turned-off project is searched:\n%s", r.screen())
	}
	r.press("esc", "L")
	r.lacks("acme-staging")
	r.press("L", "L")

	// the choice is remembered
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if !m2.hidden["acme-staging"] || m2.hidden["acme-prod"] {
		t.Fatalf("hidden %v", m2.hidden)
	}

	// the last project on stays on; a turns everything back on
	r.press("ctrl+x", "p", "space")
	if !strings.Contains(r.m.toast, "at least one project stays on") || r.m.hidden["acme-prod"] {
		t.Fatalf("toast %q hidden %v", r.m.toast, r.m.hidden)
	}
	r.press("a", "esc", "h", "h")
	r.contains("Projects 1/2", "acme-staging")
	r.lacks("projects acme-prod")
}

func TestProjectsDialogOnlyThisOne(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "j", "o", "esc")
	if !r.m.hidden["acme-prod"] || r.m.hidden["acme-staging"] {
		t.Fatalf("hidden %v", r.m.hidden)
	}
	r.contains("Datasets 1/1", "projects acme-staging")
	r.press("ctrl+t", "orders")
	r.contains("acme-staging.sales")
	r.lacks("acme-prod")
}

func TestHelpWrapsDescriptions(t *testing.T) {
	for _, w := range []int{40, 60, 94} {
		lines := helpLines(w)
		for _, l := range lines {
			if wid(l) > w {
				t.Fatalf("width %d: line is %d wide: %q", w, wid(l), l)
			}
		}
		// a long description is wrapped, not cut
		words := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), " ")
		if !strings.Contains(words, "zoomed preview: show the row as a record") {
			t.Fatalf("width %d: description cut:\n%s", w, strings.Join(lines, "\n"))
		}
	}
}

func nodes(names ...string) []api.BrowseNode {
	var out []api.BrowseNode
	for _, n := range names {
		out = append(out, api.BrowseNode{Kind: api.KindProject, Name: n, Path: api.ObjectPath{Project: n}})
	}
	return out
}

func TestNextHiddenKeepsAbsentProjectsAndActsOnWhatShows(t *testing.T) {
	// c is off but missing from the config for now: every action keeps it off
	for _, act := range []string{"toggle", "only", "all"} {
		h, ok := nextHidden(nodes("a", "b"), map[string]bool{"c": true}, "a", act)
		if !ok || !h["c"] {
			t.Fatalf("%s forgot an absent project: %v", act, h)
		}
	}
	// a, the one project on, left the config: b and c show as on, so space
	// on b turns b off and leaves c on
	h, ok := nextHidden(nodes("b", "c"), map[string]bool{"b": true, "c": true}, "b", "toggle")
	if !ok || !h["b"] || h["c"] {
		t.Fatalf("toggle in the all-off fallback: %v", h)
	}
	if _, ok := nextHidden(nodes("a", "b"), map[string]bool{"b": true}, "a", "toggle"); ok {
		t.Fatal("the last project on was turned off")
	}
}

func TestJumpToAPathInATurnedOffProjectTurnsItOn(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc") // only acme-prod
	r.press("ctrl+t")
	r.typeText("acme-staging.sales.orders")
	r.press("enter")
	if s := r.m.sel(); s.path.String() != "acme-staging.sales.orders" {
		t.Fatalf("the pasted path is not selected: %+v", s.path)
	}
	if r.m.hidden["acme-staging"] || !strings.Contains(r.m.toast, "turned project acme-staging on") {
		t.Fatalf("hidden %v toast %q", r.m.hidden, r.m.toast)
	}
}

func TestTurningAProjectOffDropsItsOpenShardFamily(t *testing.T) {
	r := newRig(t, 120, 30)
	// staging gets an events dataset with a family of the same prefix
	staging := r.fake.Projects["acme-staging"]
	staging.Datasets = append(staging.Datasets, bqtest.DS("events", "EU",
		bqtest.Table("sessions", "id"), bqtest.Table("events_20240101", "id"), bqtest.Table("events_20240102", "id")))
	hd, err := r.eng.StartSync(context.Background(), api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := r.eng.ReloadIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.m.locate(api.ObjectPath{Project: "acme-prod", Dataset: "events", Table: "events_20240102"}, api.KindTable)
	if r.m.mil.family == nil {
		t.Fatal("precondition: the family is open")
	}
	r.run(r.m.setHidden(map[string]bool{"acme-prod": true}))
	if d, _ := r.m.mil.at(1); d.Path.String() != "acme-staging.events" {
		t.Fatalf("precondition: staging's events dataset is selected: %+v", d.Path)
	}
	if r.m.mil.family != nil || indexOf(r.m.mil.cols[2], "sessions") < 0 {
		t.Fatalf("another project's family filters the tables: %+v", r.m.mil.cols[2])
	}
}

func TestStatusNamesTheOnlyConfiguredProject(t *testing.T) {
	r := newRig(t, 120, 30)
	closed := make(chan config.Event)
	close(closed)
	src := "execution_project = \"acme-exec\"\ndata_dir = \"" + r.dir + "/data\"\nprojects = [\"acme-prod\"]\n"
	os.WriteFile(r.cfg, []byte(src), 0o600)
	cfg, err := config.Load(config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	r.send(configWait{ev: config.Event{Config: cfg, Changed: []string{"projects"}}, ch: closed})
	r.contains("projects acme-prod")
}
