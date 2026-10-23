package tui

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// Codex review 6, finding 4: a pasted path in a project the catalog does
// not hold selects the project, syncs it and opens the path.
func TestPastingAPathInAnUncachedProjectOpensItAfterItsSync(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+t")
	r.typeText("acme-new.raw.hits")
	r.press("enter")
	if !r.eng.Selection().Has("acme-new") {
		t.Fatalf("not selected: %v", r.eng.Selection().IDs())
	}
	if s := r.m.sel(); s.path.String() != "acme-new.raw.hits" {
		t.Fatalf("the path is not open: %+v", s.path)
	}
}

// Codex review 6, finding 5: projects selected while a sync runs sync
// right after it.
func TestClosingTheDialogDuringASyncQueuesTheNewProjects(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j", "space") // select acme-new
	r.m.syncing = true                             // another sync is running
	r.press("esc")
	if strings.Contains(fmt.Sprint(r.fake.CallsTo("ListDatasets")), "acme-new") {
		t.Fatal("started a second sync")
	}
	r.send(syncDoneMsg{})
	calls := r.fake.CallsTo("ListDatasets")
	if calls[len(calls)-1].Project != "acme-new" {
		t.Fatalf("the queued project was not synced: %+v", calls)
	}
}

// Codex review 6, finding 9: a change from another process keeps the
// dialog's cursor on its project.
func TestAnOutsideChangeKeepsTheDialogCursor(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j") // acme-new
	r.eng.SelectProjects("acme-another")  // bqt projects select, elsewhere: a row above
	r.run(r.m.selectionChanged())
	r.press("space")
	if !r.eng.Selection().Has("acme-new") {
		t.Fatalf("space hit another project: %v", r.eng.Selection().IDs())
	}
}

// Codex review 6, finding 10: the open dialog's rows follow a sync.
func TestTheDialogRowsFollowASync(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j", "space")
	if l := r.row("acme-new"); !strings.Contains(l, "not synced yet") {
		t.Fatalf("%q", l)
	}
	r.run(r.m.startSync(api.SyncScope{Projects: []string{"acme-new"}}, false))
	if l := r.row("acme-new"); !strings.Contains(l, "1 dataset") || strings.Contains(l, "not synced yet") {
		t.Fatalf("after the sync: %q", l)
	}
}

// A change made in the dialog is not applied a second time by the tick.
func TestADialogChangeIsNotSeenAsAnOutsideOne(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "e") // a new Execution project
	if cmd := r.m.selectionChanged(); cmd != nil {
		t.Fatal("the tick re-applies the dialog's own change")
	}
}

// A deselected project's sync problems are not counted: nothing refreshes it.
func TestADeselectedProjectsProblemsAreNotShown(t *testing.T) {
	r := projectsRig(t)
	r.fake.Projects["acme-staging"].ListErr = bqtest.PermissionError("datasets.list")
	r.run(r.m.startSync(api.SyncScope{Projects: []string{"acme-staging"}}, false))
	r.run(r.m.statusCmd())
	r.contains("sync problem")
	r.press("ctrl+x", "p", "j", "space", "esc")
	r.run(r.m.statusCmd())
	r.lacks("sync problem")
}

// A list longer than the dialog scrolls with the cursor at every size; the
// header, the filter row included, stays.
func TestALongProjectsListScrolls(t *testing.T) {
	for _, sz := range [][2]int{{120, 36}, {80, 24}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			r := newRig(t, sz[0], sz[1])
			for i := range 40 {
				r.fake.Listed = append(r.fake.Listed, api.ListedProject{ID: fmt.Sprintf("acme-x%02d", i)})
			}
			r.press("ctrl+x", "p")
			r.lacks("acme-x39")
			for range 43 {
				r.press("j")
			}
			if cur := r.m.pd.current(); cur != "acme-x39" {
				t.Fatalf("cursor on %q", cur)
			}
			r.contains("] acme-x39", "Execution project:", "space select")
			r.press("space")
			if !r.eng.Selection().Has("acme-x39") {
				t.Fatalf("space hit another project: %v", r.eng.Selection().IDs())
			}
			r.press("ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u")
			r.contains("] acme-prod")

			// filtered: the filter row stays above the scrolled rows
			r.press("/")
			r.typeText("acme-x")
			r.press("enter")
			for range 39 {
				r.press("j")
			}
			if cur := r.m.pd.current(); cur != "acme-x39" {
				t.Fatalf("filtered: cursor on %q", cur)
			}
			r.contains("] acme-x39", "Execution project:", " / acme-x")
			r.lacks("] acme-x00")
		})
	}
}

// s syncs the project under the cursor now, even when its cache is fresh
// (a deselected one too, when cached); S syncs every Selected project.
func TestTheDialogSyncsOnDemand(t *testing.T) {
	r := projectsRig(t)
	before := len(r.fake.CallsTo("ListDatasets"))
	r.press("ctrl+x", "p", "s") // acme-prod, synced 3h ago
	calls := r.fake.CallsTo("ListDatasets")
	if len(calls) != before+1 || calls[len(calls)-1].Project != "acme-prod" {
		t.Fatalf("calls %+v", calls[before:])
	}
	r.contains("Projects") // the dialog stays open
	r.press("S")
	if got := syncedSince(r, before+1); got != "acme-prod,acme-staging" {
		t.Fatalf("S synced %s", got)
	}
	r.press("j", "space") // deselect acme-staging: cached
	n := len(r.fake.CallsTo("ListDatasets"))
	r.press("s")
	if got := syncedSince(r, n); got != "acme-staging" {
		t.Fatalf("s on a cached deselected project synced %q", got)
	}
	r.press("j", "s") // acme-billing: not selected, no cache
	r.contains("space selects it")
}

// syncedSince lists the projects synced after the first n ListDatasets calls.
func syncedSince(r *rig, n int) string {
	got := map[string]bool{}
	for _, c := range r.fake.CallsTo("ListDatasets")[n:] {
		got[c.Project] = true
	}
	ids := slices.Sorted(maps.Keys(got))
	return strings.Join(ids, ",")
}

// s and S while a sync runs are not dropped: they run right after it, however
// fresh the caches are by then.
func TestSyncKeysWaitForARunningSync(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p")
	before := len(r.fake.CallsTo("ListDatasets"))
	r.m.syncing = true // another sync is running
	r.press("s")
	if !strings.Contains(r.m.toast, "acme-prod syncs right after it") {
		t.Fatalf("toast %q", r.m.toast)
	}
	r.send(syncDoneMsg{})
	if got := syncedSince(r, before); got != "acme-prod" {
		t.Fatalf("queued s synced %q", got)
	}

	before = len(r.fake.CallsTo("ListDatasets"))
	r.m.syncing = true
	r.press("S")
	if !strings.Contains(r.m.toast, "every selected project syncs right after it") {
		t.Fatalf("toast %q", r.m.toast)
	}
	r.send(syncDoneMsg{})
	if got := syncedSince(r, before); got != "acme-prod,acme-staging" {
		t.Fatalf("queued S synced %q", got)
	}
}

// Closing the dialog while s syncs a newly selected project does not
// promise it a second sync.
func TestClosingDuringAnOnDemandSyncPromisesNothing(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j", "space")          // select acme-new
	r.m.syncing, r.m.syncScope = true, []string{"acme-new"} // s is syncing it
	r.m.toast = ""
	r.press("esc")
	if len(r.m.syncLater) != 0 || strings.Contains(r.m.toast, "right after it") {
		t.Fatalf("queued %v, toast %q", r.m.syncLater, r.m.toast)
	}
}
