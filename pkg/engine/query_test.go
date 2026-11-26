package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/pkg/export"
	"github.com/davidsevcik/bigquery-tui/pkg/format"
	"github.com/davidsevcik/bigquery-tui/pkg/runs"
)

func TestRunIsRecordedInHistoryAndPaged(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(700) }
	run := eng.StartRun(ctx, runs.Request{SQL: "select n, label from t", Params: []api.QueryParam{{Name: "p", Type: "DATE", Value: "2024-01-01"}}})
	info := run.Wait()
	if info.State != api.RunDone || info.TotalRows != 700 || info.Source != "tui" {
		t.Fatalf("%+v", info)
	}
	page, err := eng.RunRows(ctx, info.ID, 699, 10, format.Options{})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Text != "row 699" {
		t.Fatalf("%v %+v", err, page)
	}
	if _, err := eng.RunRows(ctx, 999, 0, 1, format.Options{}); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("%v", err)
	}
	h, _ := eng.History()
	if n, _ := h.RunCount(ctx); n != 1 {
		t.Fatalf("history rows = %d", n)
	}
	var sql, state, params, source string
	var bytes, dur int64
	if err := h.DB().QueryRow("SELECT sql,state,params,source,total_bytes_processed,duration_ms FROM history").Scan(&sql, &state, &params, &source, &bytes, &dur); err != nil {
		t.Fatal(err)
	}
	if sql != "select n, label from t" || state != "done" || !strings.Contains(params, `"2024-01-01"`) || source != "tui" || bytes != 70000 {
		t.Fatalf("%q %q %q %q %d", sql, state, params, source, bytes)
	}
	eng.StartRun(ctx, runs.Request{SQL: "select 1", DryRunOnly: true}).Wait()
	if n, _ := h.RunCount(ctx); n != 1 {
		t.Fatalf("history rows = %d", n)
	}
}

func TestExportStreamsToAFileAndCleansUpOnCancel(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1203) }
	info := eng.StartRun(ctx, runs.Request{SQL: "select"}).Wait()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "out.csv")
	h, err := eng.StartExport(ctx, info.ID, path, export.FormatOf(path))
	if err != nil {
		t.Fatal(err)
	}
	ev := h.Wait()
	if !ev.Done || ev.Error != "" || ev.Rows != 1203 {
		t.Fatalf("%+v", ev)
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1204 || lines[0] != "n,label" || lines[1203] != "1202,row 1202" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}

	path2 := filepath.Join(dir, "cancelled.ndjson")
	h, err = eng.StartExport(ctx, info.ID, path2, export.NDJSON)
	if err != nil {
		t.Fatal(err)
	}
	h.Cancel()
	ev = h.Wait()
	if ev.Error == "" {
		return
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*cancelled*"))
	if len(left) != 0 {
		t.Fatalf("partial files left: %v", left)
	}

	if _, err := eng.StartExport(ctx, 12345, path, export.CSV); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("%v", err)
	}
}

func TestCompleteUsesTheCatalog(t *testing.T) {
	_, eng := synced(t, "proj-a", "proj-b")
	labels := func(marked string) string {
		at := strings.Index(marked, "|")
		var out []string
		for _, c := range eng.Complete(marked[:at]+marked[at+1:], at, false) {
			out = append(out, c.Label)
		}
		return strings.Join(out, ",")
	}
	if got := labels("select * from `proj-a.sales.|"); got != "events_*,events_20240102,orders,v_orders" {
		t.Fatalf("tables, with the shard family as its wildcard and newest shard: %s", got)
	}
	if got := labels("select * from `proj-a.sales.events|"); got != "events_*,events_20240102" {
		t.Fatalf("before the varying part: %s", got)
	}
	if got := labels("select * from `proj-a.sales.events_2|"); got != "events_20240101,events_20240102" {
		t.Fatalf("into the varying part: %s", got)
	}
	if got := labels("select o.| from `proj-a.sales.orders` o"); !strings.Contains(got, "amount") {
		t.Fatalf("columns come from the catalog: %s", got)
	}
	if got := labels("select e.| from `proj-a.sales.events_*` e"); got != "u" {
		t.Fatalf("a wildcard table has its newest shard's columns: %s", got)
	}
}

func TestCompleteWithLimitsProjectsAndTakesAnExecutionProject(t *testing.T) {
	_, eng := synced(t, "proj-a", "proj-b")
	labels := func(sql string, o api.CompleteOptions) string {
		var out []string
		for _, c := range eng.CompleteWith(sql, len(sql), o) {
			out = append(out, c.Label)
		}
		return strings.Join(out, ",")
	}
	onlyA := api.CompleteOptions{Projects: []string{"proj-a", "not-cached"}}
	if got := labels("select * from `proj", onlyA); got != "proj-a" {
		t.Fatalf("projects: %s", got)
	}
	if got := labels("select * from `proj", api.CompleteOptions{}); got != "proj-a,proj-b" {
		t.Fatalf("no limit: %s", got)
	}
	if got := labels("select * from `proj-b.", onlyA); got != "" {
		t.Fatalf("datasets outside the projects: %s", got)
	}
	if got := labels("select * from `proj-b.core.", onlyA); got != "" {
		t.Fatalf("tables outside the projects: %s", got)
	}
	if got := labels("select * from core.", onlyA); got != "" {
		t.Fatalf("an unqualified dataset resolves only in the listed and the Execution projects: %s", got)
	}
	if got := labels("select x.c from `proj-b.core.t` x where x.", onlyA); got != "c" {
		t.Fatalf("columns of a table named in full: %s", got)
	}
	inB := api.CompleteOptions{Projects: onlyA.Projects, Execution: "proj-b"}
	if got := labels("select * from core.", inB); got != "t" {
		t.Fatalf("unqualified names resolve in the given Execution project: %s", got)
	}
	if got := labels("select * from ", api.CompleteOptions{Projects: onlyA.Projects, Execution: "proj-b", Forced: true}); !strings.Contains(got, "core") || strings.Contains(got, "proj-b") {
		t.Fatalf("the Execution project's datasets, not the project itself: %s", got)
	}
	if got := labels("select * from `proj-b.", api.CompleteOptions{}); got != "core" {
		t.Fatalf("%s", got)
	}
}

func TestARunRunsInTheExecutionProjectItIsGiven(t *testing.T) {
	e := newEnv(t, "proj-a")
	var mu sync.Mutex
	var asked []string
	eng, err := Open(ctx, Options{
		Config: e.cfg, Now: func() time.Time { return e.now },
		Client: func(_ context.Context, exec string) (Client, error) {
			mu.Lock()
			asked = append(asked, exec)
			mu.Unlock()
			return e.fake.In(exec), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	for _, exec := range []string{"window-proj", "", "window-proj"} {
		info := eng.StartRun(ctx, runs.Request{SQL: "select 1", Execution: exec}).Wait()
		want := exec
		if want == "" {
			want = "exec-proj"
		}
		if info.State != api.RunDone || info.ExecutionProject != want || info.Job.Project != want {
			t.Fatalf("%q: %+v", exec, info)
		}
	}
	h, _ := eng.History()
	rows, err := h.DB().Query("SELECT execution_project, project FROM history ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var exec, project string
		if err := rows.Scan(&exec, &project); err != nil {
			t.Fatal(err)
		}
		got = append(got, exec+"/"+project)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "window-proj/window-proj,exec-proj/exec-proj,window-proj/window-proj" {
		t.Fatalf("History: %q", got)
	}
	e.cfg.Location = "EU"
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{ReservationID: "res"} }
	if err := eng.LearnPricingIn(ctx, "window-proj"); err != nil {
		t.Fatal(err)
	}
	if f := eng.FollowRuns(); !f.PricingIn("window-proj").Capacity || f.Pricing().Capacity {
		t.Fatalf("%+v %+v", f.PricingIn("window-proj"), f.Pricing())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "window-proj,exec-proj" {
		t.Fatalf("clients built for %q", asked)
	}
}

func setExportDir(eng *Engine, dir string) {
	cfg := *eng.Config()
	cfg.ExportDir = dir
	eng.SetConfig(&cfg)
}

func TestDefaultExportPathFollowsExportDir(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(5) }
	for _, f := range export.Formats {
		p := eng.DefaultExportPath(f)
		if filepath.Dir(p) != "." || !strings.HasPrefix(p, "bqt-") || !strings.HasSuffix(p, "."+string(f)) {
			t.Fatalf("unset keeps the working directory: %q", p)
		}
	}

	dir := filepath.Join(t.TempDir(), "not", "yet")
	setExportDir(eng, dir)
	for _, f := range export.Formats {
		p := eng.DefaultExportPath(f)
		if filepath.Dir(p) != dir || !strings.HasPrefix(filepath.Base(p), "bqt-") || !strings.HasSuffix(p, "."+string(f)) {
			t.Fatalf("set, %s: %q", f, p)
		}
	}

	info := eng.StartRun(ctx, runs.Request{SQL: "select"}).Wait()
	path := eng.DefaultExportPath(export.JSON)
	h, err := eng.StartExport(ctx, info.ID, path, export.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if ev := h.Wait(); ev.Error != "" || ev.Rows != 5 {
		t.Fatalf("%+v", ev)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != filepath.Base(path) {
		t.Fatalf("only the finished export is in export_dir: %v", ents)
	}

	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1203) }
	big := eng.StartRun(ctx, runs.Request{SQL: "select many"}).Wait()
	h, err = eng.StartExport(ctx, big.ID, filepath.Join(dir, "cancelled.ndjson"), export.NDJSON)
	if err != nil {
		t.Fatal(err)
	}
	h.Cancel()
	if ev := h.Wait(); ev.Error != "" {
		if left, _ := filepath.Glob(filepath.Join(dir, "*cancelled*")); len(left) != 0 {
			t.Fatalf("partial files left: %v", left)
		}
	}
}

func TestExportDirThatIsAFileFailsTheExport(t *testing.T) {
	e, eng := synced(t, "proj-a")
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(5) }
	blocker := filepath.Join(t.TempDir(), "taken")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setExportDir(eng, blocker)
	info := eng.StartRun(ctx, runs.Request{SQL: "select"}).Wait()
	if _, err := eng.StartExport(ctx, info.ID, eng.DefaultExportPath(export.CSV), export.CSV); err == nil {
		t.Fatal("exporting into a path under a regular file must fail")
	}
	if b, _ := os.ReadFile(blocker); string(b) != "x" {
		t.Fatalf("the file was touched: %q", b)
	}
}
