package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// projectsRig is a rig whose credentials also list acme-billing (no data)
// and acme-new (data, never selected).
func projectsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, 120, 30)
	r.fake.Listed = []api.ListedProject{{ID: "acme-billing", FriendlyName: "Billing"}}
	n := r.fake.AddProject("acme-new")
	n.FriendlyName = "New things"
	n.Datasets = []bqtest.Dataset{bqtest.DS("raw", "EU", bqtest.Table("hits", "id"))}
	return r
}

// row is the dialog line of a project.
func (r *rig) row(id string) string {
	r.t.Helper()
	for _, l := range strings.Split(r.screen(), "\n") {
		if strings.Contains(l, "] "+id+" ") || strings.HasSuffix(strings.TrimRight(l, " │"), "] "+id) {
			return l
		}
	}
	r.t.Fatalf("no row for %s:\n%s", id, r.screen())
	return ""
}

func TestProjectsDialogListsYourProjectsAndTheExecutionProject(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p")
	r.contains("Projects", "Execution project: acme-exec (chosen)", "2 selected", "space select")
	if l := r.row("acme-prod"); !strings.Contains(l, "[x]") || !strings.Contains(l, "4 datasets · 3h ago") {
		t.Fatalf("selected, synced row: %q", l)
	}
	if l := r.row("acme-billing"); !strings.Contains(l, "[ ]") || !strings.Contains(l, "Billing") {
		t.Fatalf("listed row: %q", l)
	}
	// Selected first, then the rest, each A–Z
	sc := r.screen()
	order := []string{"] acme-prod", "] acme-staging", "] acme-billing", "] acme-new"}
	for i := 1; i < len(order); i++ {
		if strings.Index(sc, order[i-1]) > strings.Index(sc, order[i]) {
			t.Fatalf("%s comes after %s:\n%s", order[i-1], order[i], sc)
		}
	}
	if strings.Contains(r.row("acme-prod"), "exec") {
		t.Fatal("the Execution project is named in the header only")
	}
}

func TestProjectsDialogSelectsWhatIsSyncedAndBrowsed(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "space") // deselect acme-staging
	if l := r.row("acme-staging"); !strings.Contains(l, "[ ]") || !strings.Contains(l, "1 dataset") {
		t.Fatalf("deselected, cache kept: %q", l)
	}
	sc := r.screen()
	if strings.Index(sc, "] acme-staging") > strings.Index(sc, "] acme-billing") {
		t.Fatal("a row moved while the dialog is open")
	}
	r.press("j", "j", "space") // select acme-new
	if l := r.row("acme-new"); !strings.Contains(l, "[x]") || !strings.Contains(l, "not synced yet") {
		t.Fatalf("selected, no cache: %q", l)
	}
	if n := len(r.fake.CallsTo("ListDatasets")); n != 2 {
		t.Fatalf("nothing syncs while the dialog is open (%d calls)", n)
	}
	r.press("esc")
	// one sync of the newly selected project only
	calls := r.fake.CallsTo("ListDatasets")
	if len(calls) != 3 || calls[2].Project != "acme-new" {
		t.Fatalf("calls %+v", calls)
	}
	r.contains("2 projects", "acme-new")
	r.lacks("acme-staging")
	if got := strings.Join(r.eng.Selection().IDs(), ","); got != "acme-new,acme-prod" {
		t.Fatalf("saved %s", got)
	}
	// search leaves the deselected project out
	r.press("ctrl+t", "orders")
	r.lacks("acme-staging")

	// reopened: the groups follow the new selection
	r.press("esc", "ctrl+x", "p")
	sc = r.screen()
	if strings.Index(sc, "] acme-new") > strings.Index(sc, "] acme-billing") || strings.Index(sc, "] acme-staging") < strings.Index(sc, "] acme-prod") {
		t.Fatalf("regrouped on reopen:\n%s", sc)
	}
}

func TestTheLastSelectedProjectStays(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "space", "j", "space")
	if !strings.Contains(r.m.toast, "the last selected project stays selected") || !r.eng.Selection().Has("acme-staging") {
		t.Fatalf("toast %q selection %v", r.m.toast, r.eng.Selection().IDs())
	}
	// o selects only this one: the Miller project column goes
	r.press("k", "o", "esc")
	r.contains("project acme-prod", "Datasets ")
	r.lacks("Projects 1/")
}

func TestChoosingTheExecutionProject(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "e") // acme-billing: listed, not selected
	r.contains("Execution project: acme-billing (chosen)", "exec acme-billing")
	if r.eng.Selection().Has("acme-billing") {
		t.Fatal("choosing the Execution project selected it")
	}
	r.press("e") // on the current one: nothing happens
	if p, _ := r.eng.ExecutionProject(); p != "acme-billing" {
		t.Fatalf("e on the current project changed it to %q", p)
	}
}

func TestProjectsDialogFilterAndAddByID(t *testing.T) {
	r := projectsRig(t)
	shared := r.fake.AddProject("shared-proj") // dataset-level share: not listed
	shared.Unlisted = true
	shared.Datasets = []bqtest.Dataset{bqtest.DS("pub", "EU", bqtest.Table("t", "c"))}
	r.press("ctrl+x", "p", "/")
	r.typeText("bill")
	r.contains("acme-billing")
	r.lacks("] acme-prod")
	r.press("enter", "esc") // esc clears the filter, the dialog stays
	r.contains("] acme-prod", "Execution project")

	r.press("/")
	r.typeText("shared-proj")
	r.contains("add shared-proj")
	r.press("enter", "space")
	if l := r.row("shared-proj"); !strings.Contains(l, "[x]") || !strings.Contains(l, "not listed") {
		t.Fatalf("added by ID: %q", l)
	}
	r.press("esc", "esc")
	if calls := r.fake.CallsTo("ListDatasets"); calls[len(calls)-1].Project != "shared-proj" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestProjectsListFailureKeepsTheSelectionUsable(t *testing.T) {
	r := projectsRig(t)
	r.fake.Fail(bqtest.Failure{Method: "ListProjects", Err: bqtest.PermissionError("projects.list")})
	r.press("ctrl+x", "p")
	r.contains("could not list your projects", "] acme-prod", "] acme-staging")
	r.lacks("acme-billing")
}

func TestNothingSelectedOpensTheDialog(t *testing.T) {
	r := newRig(t, 120, 30)
	os.Remove(filepath.Join(r.dir, "data", "state", "projects.json"))
	m2 := New(r.m.be, Options{NoBackground: true, StableOutput: true})
	defer m2.Close()
	m2.Init()
	if m2.ov != ovProjects {
		t.Fatal("the Projects dialog did not open")
	}
	r.m = m2
	r.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	r.contains("Execution project: none", "0 selected", "] acme-prod")
}

func TestASelectionChangedElsewhereShowsUp(t *testing.T) {
	r := newRig(t, 120, 30)
	if err := r.eng.DeselectProjects("acme-staging"); err != nil { // bqt projects deselect, in another terminal
		t.Fatal(err)
	}
	r.run(r.m.selectionChanged())
	r.contains("project acme-prod")
	r.lacks("acme-staging")
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

func TestJumpToAPathInADeselectedProjectSelectsIt(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc") // only acme-prod
	r.press("ctrl+t")
	r.typeText("acme-staging.sales.orders")
	r.press("enter")
	if s := r.m.sel(); s.path.String() != "acme-staging.sales.orders" {
		t.Fatalf("the pasted path is not selected: %+v", s.path)
	}
	if !r.eng.Selection().Has("acme-staging") || !strings.Contains(r.m.toast, "selected project acme-staging") {
		t.Fatalf("selection %v toast %q", r.eng.Selection().IDs(), r.m.toast)
	}
	if n := len(r.fake.CallsTo("ListDatasets")); n != 2 {
		t.Fatalf("a fresh cache needs no sync (%d calls)", n)
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
	if err := r.eng.DeselectProjects("acme-prod"); err != nil {
		t.Fatal(err)
	}
	r.run(r.m.selectionChanged())
	if d, _ := r.m.mil.at(1); d.Path.String() != "acme-staging.events" {
		t.Fatalf("precondition: staging's events dataset is selected: %+v", d.Path)
	}
	if r.m.mil.family != nil || indexOf(r.m.mil.cols[2], "sessions") < 0 {
		t.Fatalf("another project's family filters the tables: %+v", r.m.mil.cols[2])
	}
}

func TestProjectsDialogGoldenScreens(t *testing.T) {
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {60, 20}} {
		name := fmt.Sprintf("%dx%d", sz[0], sz[1])
		r := newRig(t, sz[0], sz[1])
		r.fake.Listed = []api.ListedProject{{ID: "acme-billing", FriendlyName: "Billing and invoicing"}}
		for i := 0; i < 30; i++ { // a long list
			r.fake.Listed = append(r.fake.Listed, api.ListedProject{ID: fmt.Sprintf("team-%02d-analytics", i), FriendlyName: "Team analytics"})
		}
		r.press("ctrl+x", "p")
		r.golden("projects_dialog_" + name)
		r.press("/")
		r.typeText("bill")
		r.golden("projects_filter_" + name)

		e := newRig(t, sz[0], sz[1])
		e.fake.Fail(bqtest.Failure{Method: "ListProjects", Err: bqtest.PermissionError("projects.list")})
		e.press("ctrl+x", "p")
		e.golden("projects_error_" + name)
	}
}
