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

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/pkg/config"
	"github.com/davidsevcik/bigquery-tui/pkg/engine"
)

func projectsRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, 120, 30)
	r.fake.Listed = []api.ListedProject{{ID: "acme-billing", FriendlyName: "Billing"}}
	n := r.fake.AddProject("acme-new")
	n.FriendlyName = "New things"
	n.Datasets = []bqtest.Dataset{bqtest.DS("raw", "EU", bqtest.Table("hits", "id"))}
	return r
}

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
	r.press("ctrl+x", "p", "j", "space")
	if l := r.row("acme-staging"); !strings.Contains(l, "[ ]") || !strings.Contains(l, "1 dataset") {
		t.Fatalf("deselected, cache kept: %q", l)
	}
	sc := r.screen()
	if strings.Index(sc, "] acme-staging") > strings.Index(sc, "] acme-billing") {
		t.Fatal("a row moved while the dialog is open")
	}
	r.press("j", "j", "space")
	if l := r.row("acme-new"); !strings.Contains(l, "[x]") || !strings.Contains(l, "not synced yet") {
		t.Fatalf("selected, no cache: %q", l)
	}
	if n := len(r.fake.CallsTo("ListDatasets")); n != 2 {
		t.Fatalf("nothing syncs while the dialog is open (%d calls)", n)
	}
	r.press("esc")
	calls := r.fake.CallsTo("ListDatasets")
	if len(calls) != 3 || calls[2].Project != "acme-new" {
		t.Fatalf("calls %+v", calls)
	}
	r.press("h")
	r.contains("2 projects", "acme-new")
	r.lacks("acme-staging")
	if got := strings.Join(r.eng.Selection().IDs(), ","); got != "acme-new,acme-prod" {
		t.Fatalf("saved %s", got)
	}
	r.press("ctrl+t", "orders")
	r.lacks("acme-staging")

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
	r.press("k", "o", "esc")
	r.contains("project acme-prod", "Datasets ")
	r.lacks("Projects 1/")
}

func TestChoosingTheExecutionProject(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "e")
	r.contains("Execution project: acme-billing (chosen)", "exec acme-billing")
	if r.eng.Selection().Has("acme-billing") {
		t.Fatal("choosing the Execution project selected it")
	}
	r.press("e")
	if p, _ := r.eng.ExecutionProject(); p != "acme-billing" {
		t.Fatalf("e on the current project changed it to %q", p)
	}
}

func TestProjectsDialogFilterAndAddByID(t *testing.T) {
	r := projectsRig(t)
	shared := r.fake.AddProject("shared-proj")
	shared.Unlisted = true
	shared.Datasets = []bqtest.Dataset{bqtest.DS("pub", "EU", bqtest.Table("t", "c"))}
	r.press("ctrl+x", "p", "/")
	r.typeText("bill")
	r.contains("acme-billing")
	r.lacks("] acme-prod")
	r.press("enter", "esc")
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
	if err := r.eng.DeselectProjects("acme-staging"); err != nil {
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
		words := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), " ")
		if !strings.Contains(words, "result sample or summary, to the clipboard or a file") {
			t.Fatalf("width %d: description cut:\n%s", w, strings.Join(lines, "\n"))
		}
	}
}

func TestJumpToAPathInADeselectedProjectSelectsIt(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc")
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
		for i := 0; i < 30; i++ {
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

func addToFake(r *rig, project string, ds bqtest.Dataset) {
	p := r.fake.Projects[project]
	for i, d := range p.Datasets {
		if d.Meta.Name == ds.Meta.Name {
			p.Datasets[i].Tables = append(p.Datasets[i].Tables, ds.Tables...)
			return
		}
	}
	p.Datasets = append(p.Datasets, ds)
}

func (r *rig) paste(path string) {
	r.t.Helper()
	r.press("ctrl+t")
	r.typeText(path)
	r.press("enter")
}

func TestPastingAPathMissingFromASelectedProjectSyncsItAndOpensThePath(t *testing.T) {
	for _, path := range []string{"acme-prod.sales.fresh_table", "acme-prod.fresh_ds.fresh_table", "`acme-prod.sales.fresh_table`"} {
		t.Run(path, func(t *testing.T) {
			r := newRig(t, 120, 30)
			addToFake(r, "acme-prod", bqtest.DS("sales", "EU", bqtest.Table("fresh_table", "id")))
			addToFake(r, "acme-prod", bqtest.DS("fresh_ds", "EU", bqtest.Table("fresh_table", "id")))
			before := len(r.fake.CallsTo("ListDatasets"))
			r.paste(path)
			if r.m.ov != ovNone {
				t.Fatal("the overlay stayed open")
			}
			if got := syncedSince(r, before); got != "acme-prod" || len(r.fake.CallsTo("ListDatasets")) != before+1 {
				t.Fatalf("want one sync of acme-prod, synced %q", got)
			}
			want := strings.Trim(path, "`")
			if s := r.m.sel(); s.path.String() != want {
				t.Fatalf("the pasted path is not open: %+v (toast %q)", s.path, r.m.toast)
			}
		})
	}
}

func TestPastingAPathThatExistsNowhereSaysSoAfterTheSync(t *testing.T) {
	r := newRig(t, 120, 30)
	before := len(r.fake.CallsTo("ListDatasets"))
	r.paste("acme-prod.sales.nope")
	if got := syncedSince(r, before); got != "acme-prod" {
		t.Fatalf("synced %q", got)
	}
	if !strings.Contains(r.m.toast, "acme-prod.sales.nope is not in the catalog after syncing acme-prod") {
		t.Fatalf("toast %q", r.m.toast)
	}
}

func TestAFailedSyncOfAPastedPathSaysWhy(t *testing.T) {
	cases := []struct {
		name, project, path string
	}{
		{"cached", "acme-prod", "acme-prod.sales.fresh_table"},
		{"uncached", "acme-new", "acme-new.raw.hits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := projectsRig(t)
			r.fake.Projects[c.project].ListErr = bqtest.PermissionError("datasets.list")
			r.paste(c.path)
			if !strings.Contains(r.m.toast, c.path) || !strings.Contains(r.m.toast, "access denied: datasets.list") || strings.Contains(r.m.toast, "not in the catalog") {
				t.Fatalf("toast %q", r.m.toast)
			}
			if r.m.openAfter != nil {
				t.Fatal("the open is left pending")
			}
		})
	}
}

func TestPastingAPathMissingFromAFreshDeselectedProjectSyncsIt(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc")
	addToFake(r, "acme-staging", bqtest.DS("sales", "EU", bqtest.Table("fresh_table", "id")))
	before := len(r.fake.CallsTo("ListDatasets"))
	r.paste("acme-staging.sales.fresh_table")
	if !r.eng.Selection().Has("acme-staging") {
		t.Fatalf("not selected: %v", r.eng.Selection().IDs())
	}
	if got := syncedSince(r, before); got != "acme-staging" {
		t.Fatalf("synced %q", got)
	}
	if s := r.m.sel(); s.path.String() != "acme-staging.sales.fresh_table" {
		t.Fatalf("the pasted path is not open: %+v (toast %q)", s.path, r.m.toast)
	}
}

func TestAPastedPathWaitsForTheRunningSyncOfItsProject(t *testing.T) {
	r := newRig(t, 120, 30)
	addToFake(r, "acme-prod", bqtest.DS("sales", "EU", bqtest.Table("fresh_table", "id")))
	r.m.syncing, r.m.syncScope = true, []string{"acme-prod"}
	before := len(r.fake.CallsTo("ListDatasets"))
	r.paste("acme-prod.sales.fresh_table")
	if n := len(r.fake.CallsTo("ListDatasets")); n != before || len(r.m.forceLater) != 0 {
		t.Fatalf("started or queued a second sync (%d calls, queued %v)", n-before, r.m.forceLater)
	}
	if !strings.Contains(r.m.toast, "acme-prod.sales.fresh_table opens when") {
		t.Fatalf("toast %q", r.m.toast)
	}
	hd, err := r.eng.StartSync(context.Background(), api.SyncScope{Projects: []string{"acme-prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	r.send(syncDoneMsg{})
	if s := r.m.sel(); s.path.String() != "acme-prod.sales.fresh_table" {
		t.Fatalf("the pasted path is not open: %+v (toast %q)", s.path, r.m.toast)
	}
	if n := len(r.fake.CallsTo("ListDatasets")); n != before+1 {
		t.Fatalf("synced again after the running sync (%d calls)", n-before)
	}
}

func TestAPastedPathQueuesASyncBehindOneOfAnotherProject(t *testing.T) {
	for _, running := range []struct {
		name  string
		scope []string
		table *api.ObjectPath
	}{
		{"another project", []string{"acme-staging"}, nil},
		{"a table of the same project", []string{"acme-prod"}, &api.ObjectPath{Project: "acme-prod", Dataset: "sales", Table: "orders"}},
	} {
		t.Run(running.name, func(t *testing.T) {
			r := newRig(t, 120, 30)
			addToFake(r, "acme-prod", bqtest.DS("sales", "EU", bqtest.Table("fresh_table", "id")))
			r.m.syncing, r.m.syncScope, r.m.syncTable = true, running.scope, running.table
			before := len(r.fake.CallsTo("ListDatasets"))
			r.paste("acme-prod.sales.fresh_table")
			if n := len(r.fake.CallsTo("ListDatasets")); n != before {
				t.Fatalf("started a second sync (%d calls)", n-before)
			}
			if !strings.Contains(r.m.toast, "acme-prod syncs right after it") || !strings.Contains(r.m.toast, "acme-prod.sales.fresh_table") {
				t.Fatalf("toast %q", r.m.toast)
			}
			r.send(syncDoneMsg{})
			if got := syncedSince(r, before); got != "acme-prod" {
				t.Fatalf("synced %q", got)
			}
			if s := r.m.sel(); s.path.String() != "acme-prod.sales.fresh_table" {
				t.Fatalf("the pasted path is not open: %+v (toast %q)", s.path, r.m.toast)
			}
		})
	}
}

func TestAnIgnoredPastedPathStartsNoSync(t *testing.T) {
	r := newRig(t, 120, 30)
	src := "data_dir = \"" + r.dir + "/data\"\nignore = [\"acme-prod.scratch\"]\n"
	if err := os.WriteFile(r.cfg, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	r.m.be.SetConfig(cfg)
	before := len(r.fake.CallsTo("ListDatasets"))
	r.paste("acme-prod.scratch.tmp_1")
	if n := len(r.fake.CallsTo("ListDatasets")); n != before {
		t.Fatalf("started a sync (%d calls)", n-before)
	}
	if r.m.ov != ovNone || !strings.Contains(r.m.toast, "acme-prod.scratch.tmp_1 is excluded by the ignore config") {
		t.Fatalf("overlay %v toast %q", r.m.ov, r.m.toast)
	}
}

func TestAPastedPathTheSearchHasNotFoundYetOpensWithoutASync(t *testing.T) {
	r := newRig(t, 120, 30)
	before := len(r.fake.CallsTo("ListDatasets"))
	r.run(r.m.openMissing(api.ObjectPath{Project: "acme-prod", Dataset: "sales", Table: "customers"}))
	if n := len(r.fake.CallsTo("ListDatasets")); n != before {
		t.Fatalf("started a sync (%d calls)", n-before)
	}
	if s := r.m.sel(); s.path.String() != "acme-prod.sales.customers" {
		t.Fatalf("the path is not open: %+v", s.path)
	}
}

type noSyncBackend struct {
	EngineBackend
	err error
}

func (b noSyncBackend) StartSync(context.Context, api.SyncScope) (SyncRun, error) { return nil, b.err }

func TestAPastedPathWhoseSyncCannotStartSaysSo(t *testing.T) {
	r := newRig(t, 120, 30)
	r.m.be = noSyncBackend{EngineBackend: r.m.be.(EngineBackend), err: engine.ErrReadOnly}
	r.paste("acme-prod.sales.fresh_table")
	if !strings.Contains(r.m.toast, "acme-prod.sales.fresh_table") || !strings.Contains(r.m.toast, engine.ErrReadOnly.Error()) {
		t.Fatalf("toast %q", r.m.toast)
	}
	if r.m.openAfter != nil {
		t.Fatal("the open is left pending")
	}
}

func TestClosingTheDialogDuringASyncQueuesTheNewProjects(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j", "space")
	r.m.syncing = true
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

func TestAnOutsideChangeKeepsTheDialogCursor(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j")
	if err := r.eng.SelectProjects("acme-another"); err != nil {
		t.Fatal(err)
	}
	r.run(r.m.selectionChanged())
	r.press("space")
	if !r.eng.Selection().Has("acme-new") {
		t.Fatalf("space hit another project: %v", r.eng.Selection().IDs())
	}
}

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

func TestADialogChangeIsNotSeenAsAnOutsideOne(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "e")
	if cmd := r.m.selectionChanged(); cmd != nil {
		t.Fatal("the tick re-applies the dialog's own change")
	}
}

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
			for range 25 {
				r.press("ctrl+u")
			}
			r.contains("] acme-prod")

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

func TestTheDialogSyncsOnDemand(t *testing.T) {
	r := projectsRig(t)
	before := len(r.fake.CallsTo("ListDatasets"))
	r.press("ctrl+x", "p", "s")
	calls := r.fake.CallsTo("ListDatasets")
	if len(calls) != before+1 || calls[len(calls)-1].Project != "acme-prod" {
		t.Fatalf("calls %+v", calls[before:])
	}
	r.contains("Projects")
	r.press("S")
	if got := syncedSince(r, before+1); got != "acme-prod,acme-staging" {
		t.Fatalf("S synced %s", got)
	}
	r.press("j", "space")
	n := len(r.fake.CallsTo("ListDatasets"))
	r.press("s")
	if got := syncedSince(r, n); got != "acme-staging" {
		t.Fatalf("s on a cached deselected project synced %q", got)
	}
	r.press("j", "s")
	r.contains("space selects it")
}

func syncedSince(r *rig, n int) string {
	got := map[string]bool{}
	for _, c := range r.fake.CallsTo("ListDatasets")[n:] {
		got[c.Project] = true
	}
	ids := slices.Sorted(maps.Keys(got))
	return strings.Join(ids, ",")
}

func TestSyncKeysWaitForARunningSync(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p")
	before := len(r.fake.CallsTo("ListDatasets"))
	r.m.syncing = true
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

func TestClosingDuringAnOnDemandSyncPromisesNothing(t *testing.T) {
	r := projectsRig(t)
	r.press("ctrl+x", "p", "j", "j", "j", "space")
	r.m.syncing, r.m.syncScope = true, []string{"acme-new"}
	r.m.toast = ""
	r.press("esc")
	if len(r.m.syncLater) != 0 || strings.Contains(r.m.toast, "right after it") {
		t.Fatalf("queued %v, toast %q", r.m.syncLater, r.m.toast)
	}
}

func TestAShortDialogKeepsTheFilterAndARow(t *testing.T) {
	r := newRig(t, 60, 12)
	r.fake.Listed = []api.ListedProject{{ID: "acme-billing", FriendlyName: "Billing"}}
	r.press("ctrl+x", "p", "/")
	r.typeText("bill")
	r.contains("2 selected", " / bill", "] acme-billing")
	r.lacks("space select")
	r.press("esc", "esc", "esc")
	if r.m.ov != ovNone {
		t.Fatal("the dialog stayed open")
	}
}
