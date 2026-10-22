package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

func syncedProjects(e *env) map[string]bool {
	out := map[string]bool{}
	for _, c := range e.fake.Calls {
		if c.Method == "ListDatasets" {
			out[c.Project] = true
		}
	}
	return out
}

func fullSync(t *testing.T, eng *Engine) {
	t.Helper()
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedProjectsDecideWhatAFullSyncCovers(t *testing.T) {
	e := newEnv(t)
	eng := e.open(false)
	if err := eng.SelectProjects("proj-b"); err != nil {
		t.Fatal(err)
	}
	fullSync(t, eng)
	if got := syncedProjects(e); !got["proj-b"] || got["proj-a"] {
		t.Fatalf("synced %v", got)
	}
	if ps := eng.Projects(); len(ps) != 1 || ps[0].Name != "proj-b" {
		t.Fatalf("explorer projects %+v", ps)
	}

	// another process sees the selection
	other := e.open(true)
	if got := other.Selection().IDs(); len(got) != 1 || got[0] != "proj-b" {
		t.Fatalf("second engine sees %v", got)
	}
}

func TestDeselectKeepsTheCacheButStopsSyncing(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	fullSync(t, eng)
	if err := eng.DeselectProjects("proj-a"); err != nil {
		t.Fatal(err)
	}
	if ps := eng.Projects(); len(ps) != 1 || ps[0].Name != "proj-b" {
		t.Fatalf("explorer projects %+v", ps)
	}
	// still cached: an exact lookup works
	if _, err := eng.TableDetail(ctx, api.ObjectPath{Project: "proj-a", Dataset: "sales", Table: "orders"}); err != nil {
		t.Fatalf("cache dropped: %v", err)
	}
	e.fake.Calls = nil
	fullSync(t, eng)
	if got := syncedProjects(e); got["proj-a"] || !got["proj-b"] {
		t.Fatalf("synced %v", got)
	}
	// a day later, background refresh leaves it alone too
	e.now = e.now.Add(25 * time.Hour)
	e.fake.Calls = nil
	h, err := eng.AutoRefresh(ctx)
	if err != nil || h == nil {
		t.Fatalf("auto refresh: %v %v", h, err)
	}
	h.Wait()
	if got := syncedProjects(e); got["proj-a"] || !got["proj-b"] {
		t.Fatalf("auto refresh synced %v", got)
	}
}

func TestTheLastSelectedProjectStaysSelected(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	if err := eng.DeselectProjects("proj-a", "proj-b"); !errors.Is(err, ErrLastSelected) {
		t.Fatalf("deselecting everything: %v", err)
	}
	if got := eng.Selection().IDs(); len(got) != 2 {
		t.Fatalf("a refused change was half applied: %v", got)
	}
	if err := eng.SelectOnly("proj-b"); err != nil {
		t.Fatal(err)
	}
	if got := eng.Selection().IDs(); len(got) != 1 || got[0] != "proj-b" {
		t.Fatalf("select only: %v", got)
	}
}

func TestExecutionProjectDefaultsToTheEarliestSelected(t *testing.T) {
	e := newEnv(t)
	eng := e.open(false)
	if _, src := eng.ExecutionProject(); src != api.ExecutionNone {
		t.Fatalf("nothing selected, source %q", src)
	}
	eng.SelectProjects("proj-b")
	e.now = e.now.Add(time.Minute)
	eng.SelectProjects("proj-a")
	if p, src := eng.ExecutionProject(); p != "proj-b" || src != api.ExecutionFirstSelected {
		t.Fatalf("default %q %q", p, src)
	}
	// selecting it again keeps its place
	e.now = e.now.Add(time.Minute)
	eng.SelectProjects("proj-b")
	if p, _ := eng.ExecutionProject(); p != "proj-b" {
		t.Fatalf("reselect moved the default to %q", p)
	}
	// deselect it: the next earliest takes over
	eng.DeselectProjects("proj-b")
	if p, _ := eng.ExecutionProject(); p != "proj-a" {
		t.Fatalf("after deselect %q", p)
	}
	// a chosen project, selected or not, survives selection changes
	if err := eng.ChooseExecution("billing"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	eng.SelectProjects("proj-b")
	eng.DeselectProjects("proj-a")
	if p, src := eng.ExecutionProject(); p != "billing" || src != api.ExecutionChosen {
		t.Fatalf("chosen %q %q", p, src)
	}
}

func TestExecutionOverridesWinWithoutBeingSaved(t *testing.T) {
	e := newEnv(t, "proj-a")
	getenv := func(k string) string {
		if k == "BQT_EXECUTION_PROJECT" {
			return "env-proj"
		}
		return ""
	}
	cfg, err := config.Parse([]byte("data_dir = \""+e.dir+"\"\n"), "t.toml", config.LoadOptions{Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	e.cfg = cfg
	eng := e.open(false)
	if p, src := eng.ExecutionProject(); p != "env-proj" || src != api.ExecutionEnv {
		t.Fatalf("env override %q %q", p, src)
	}
	cfg, _ = config.Parse([]byte("data_dir = \""+e.dir+"\"\n"), "t.toml", config.LoadOptions{
		Getenv: getenv, Flags: map[string]string{"execution_project": "flag-proj"},
	})
	e.cfg = cfg
	if p, src := e.open(false).ExecutionProject(); p != "flag-proj" || src != api.ExecutionFlag {
		t.Fatalf("flag override %q %q", p, src)
	}
	if got := eng.Selection().Execution; got != "exec-proj" {
		t.Fatalf("an override was saved: %q", got)
	}
}

func TestNeedsSyncIsMissingOrStaleCaches(t *testing.T) {
	e := newEnv(t, "proj-a")
	eng := e.open(false)
	fullSync(t, eng)
	got, err := eng.NeedsSync(ctx, []string{"proj-a", "proj-b"})
	if err != nil || len(got) != 1 || got[0] != "proj-b" {
		t.Fatalf("fresh cache: %v %v", got, err)
	}
	e.now = e.now.Add(25 * time.Hour)
	if got, _ := eng.NeedsSync(ctx, []string{"proj-a"}); len(got) != 1 {
		t.Fatalf("a cache older than sync_ttl needs a sync: %v", got)
	}
}

func TestAnExplicitSyncRefreshesADeselectedCacheWithoutSelectingIt(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	fullSync(t, eng)
	eng.DeselectProjects("proj-b")
	e.fake.Calls = nil
	h, err := eng.StartSync(ctx, api.SyncScope{Projects: []string{"proj-b"}})
	if err != nil {
		t.Fatal(err)
	}
	h.Wait()
	if got := syncedProjects(e); !got["proj-b"] {
		t.Fatalf("synced %v", got)
	}
	if eng.Selection().Has("proj-b") {
		t.Fatal("refreshing a cache selected the project")
	}
	if _, err := eng.StartSync(ctx, api.SyncScope{Projects: []string{"proj-c"}}); !errors.Is(err, ErrNotSelected) {
		t.Fatalf("an unknown project: %v", err)
	}
}

func TestStatusListsSelectedThenCachedProjects(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	eng := e.open(false)
	fullSync(t, eng)
	eng.DeselectProjects("proj-a")
	e.now = e.now.Add(25 * time.Hour) // both stale
	st, err := eng.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projects) != 2 || st.Projects[0].Project != "proj-b" || !st.Projects[0].Selected ||
		st.Projects[1].Project != "proj-a" || st.Projects[1].Selected || !st.Projects[1].Stale {
		t.Fatalf("%+v", st.Projects)
	}
	if st.ExecutionProject != "exec-proj" || st.ExecutionSource != api.ExecutionChosen {
		t.Fatalf("execution %q %q", st.ExecutionProject, st.ExecutionSource)
	}
}

func TestProjectInfosMergeListedSelectedAndCached(t *testing.T) {
	e := newEnv(t, "proj-a", "proj-b")
	e.fake.Projects["proj-a"].FriendlyName = "Project A"
	e.fake.Projects["proj-b"].Unlisted = true // shared at dataset level
	e.fake.Listed = []api.ListedProject{{ID: "proj-c", FriendlyName: "proj-c"}} // just the ID again
	e.fake.AddProject("proj-d").ListErr = bqtest.PermissionError("datasets.list")
	e.fake.AddProject("proj-e").ListErr = bqtest.TransientError("boom")
	eng := e.open(false)
	eng.SelectProjects("proj-d", "proj-e")
	fullSync(t, eng)
	eng.DeselectProjects("proj-b")

	listed, err := eng.AccessibleProjects(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := eng.ProjectInfos(ctx, listed)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]api.ProjectInfo{}
	var ids []string
	for _, p := range infos {
		by[p.ID] = p
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "proj-a,proj-b,proj-c,proj-d,proj-e" {
		t.Fatalf("ids %v", ids)
	}
	a := by["proj-a"]
	if !a.Selected || !a.Listed || a.FriendlyName != "Project A" || a.Datasets != 2 || a.Sync != api.ProjectSynced || a.SyncedAt == nil {
		t.Fatalf("proj-a %+v", a)
	}
	if b := by["proj-b"]; b.Selected || b.Listed || b.Sync != api.ProjectSynced || b.Datasets != 1 {
		t.Fatalf("deselected, unlisted, cached proj-b %+v", b)
	}
	if c := by["proj-c"]; c.Selected || !c.Listed || c.Sync != api.ProjectNotSynced || c.Datasets != 0 || c.FriendlyName != "" {
		t.Fatalf("listed, empty proj-c %+v", c)
	}
	if d := by["proj-d"]; d.Accessible || d.Sync == api.ProjectFailed {
		t.Fatalf("permission failure proj-d %+v", d)
	}
	if x := by["proj-e"]; !x.Accessible || x.Sync != api.ProjectFailed || x.Error == "" {
		t.Fatalf("other failure proj-e %+v", x)
	}

	// no list: every project counts as listed
	infos, _ = eng.ProjectInfos(ctx, nil)
	for _, p := range infos {
		if !p.Listed {
			t.Fatalf("without a list %s is unlisted", p.ID)
		}
	}
}

func TestAccessibleProjectsAreFetchedOnceUnlessRefreshed(t *testing.T) {
	e := newEnv(t)
	eng := e.open(false)
	eng.AccessibleProjects(ctx, false)
	eng.AccessibleProjects(ctx, false)
	if n := len(e.fake.CallsTo("ListProjects")); n != 1 {
		t.Fatalf("listed %d times", n)
	}
	e.fake.AddProject("proj-new")
	got, _ := eng.AccessibleProjects(ctx, true)
	if len(got) != 3 || got[2].ID != "proj-new" {
		t.Fatalf("refresh: %+v", got)
	}
	if _, src := eng.ExecutionProject(); src != api.ExecutionNone {
		t.Fatal("listing needs no Execution project")
	}
}

func TestTheTUISearchesOnlySelectedProjects(t *testing.T) {
	e, eng := synced(t, "proj-a", "proj-b")
	_ = e
	eng.DeselectProjects("proj-b")
	r, err := eng.Search(ctx, "t", api.SearchOptions{SelectedOnly: true, AllWhenEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range r.Results {
		if x.Path.Project == "proj-b" {
			t.Fatalf("a deselected project is searched: %+v", x.Path)
		}
	}
	f, _ := eng.Filter(ctx, "core", api.FilterOptions{SelectedOnly: true})
	if len(f.Matches) != 0 {
		t.Fatalf("a deselected project is filtered: %+v", f.Matches)
	}
	// a pasted path still resolves
	if r, _ := eng.Search(ctx, "proj-b.core.t", api.SearchOptions{SelectedOnly: true}); len(r.Results) != 1 {
		t.Fatalf("pasted path: %+v", r.Results)
	}
}

func TestAgentsSeeDeselectedCachesRankedLastWithWarnings(t *testing.T) {
	e, _ := synced(t, "proj-a", "proj-b")
	e.fake.Projects["proj-b"].Datasets[0].Tables = append(e.fake.Projects["proj-b"].Datasets[0].Tables, bqtest.Table("orders", "id"))
	rw := e.openWithTables(false)
	fullSync(t, rw)
	rw.DeselectProjects("proj-a")
	ag := e.agent("mcp")

	hits, err := ag.CatalogSearch(ctx, "orders", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// proj-a also has v_orders; its exact "orders" would rank first without
	// the preference for Selected projects
	if len(hits.Hits) != 3 || hits.Hits[0].Path != "proj-b.core.orders" || !hits.Hits[0].Selected ||
		hits.Hits[1].Path != "proj-a.sales.orders" || hits.Hits[1].Selected || hits.Hits[2].Selected {
		t.Fatalf("selected projects rank first: %+v", hits.Hits)
	}
	if len(hits.Warnings) != 0 {
		t.Fatalf("fresh caches warn: %v", hits.Warnings)
	}

	e.now = e.now.Add(25 * time.Hour)
	hits, _ = ag.CatalogSearch(ctx, "orders", 10, 0)
	if len(hits.Warnings) != 2 || !strings.Contains(hits.Warnings[0], "proj-a") || !strings.Contains(hits.Warnings[0], "not selected") ||
		!strings.Contains(hits.Warnings[0], "bqt sync --project proj-a") || strings.Contains(hits.Warnings[1], "not selected") {
		t.Fatalf("stale warnings: %q", hits.Warnings)
	}

	s, err := ag.Schema(ctx, "proj-a.sales.orders")
	if err != nil {
		t.Fatalf("a deselected project's schema: %v", err)
	}
	if s.Selected || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "not selected") {
		t.Fatalf("schema %+v", s)
	}
}
