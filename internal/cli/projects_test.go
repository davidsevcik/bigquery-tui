package cli

import (
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

func projectsOf(t *testing.T, out string) (map[string]any, map[string]map[string]any) {
	t.Helper()
	data := decode(t, out).Data.(map[string]any)
	by := map[string]map[string]any{}
	for _, p := range data["projects"].([]any) {
		m := p.(map[string]any)
		by[m["id"].(string)] = m
	}
	return data, by
}

func TestProjectsListSelectDeselectAndExec(t *testing.T) {
	s := newSandbox(t)
	s.fake.AddProject("proj-b").FriendlyName = "Project B"
	s.writeConfig("")

	// nothing selected yet: everything listed, no Execution project
	code, out, errOut := s.run("projects")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	data, by := projectsOf(t, out)
	if len(by) != 2 || by["proj-b"]["friendly_name"] != "Project B" || by["proj-a"]["selected"] != false || data["execution_project"] != nil {
		t.Fatalf("%v", data)
	}

	// select: saved, not synced, and the first selected becomes the default
	code, out, _ = s.run("projects", "select", "proj-b", "--format", "text")
	if code != 0 || !strings.Contains(out, "bqt sync") {
		t.Fatalf("%d %s", code, out)
	}
	if n := len(s.fake.CallsTo("ListDatasets")); n != 0 {
		t.Fatalf("select synced (%d calls)", n)
	}
	s.run("projects", "select", "proj-a")
	_, out, _ = s.run("projects")
	data, by = projectsOf(t, out)
	if by["proj-a"]["selected"] != true || data["execution_project"] != "proj-b" || data["execution_source"] != "first_selected" {
		t.Fatalf("%v", data)
	}

	// sync, then deselect: the cache stays
	if code, _, errOut := s.run("sync"); code != 0 {
		t.Fatalf("sync %d %s", code, errOut)
	}
	if code, _, errOut := s.run("projects", "deselect", "proj-a"); code != 0 {
		t.Fatalf("deselect %d %s", code, errOut)
	}
	_, out, _ = s.run("projects")
	_, by = projectsOf(t, out)
	if a := by["proj-a"]; a["selected"] != false || a["datasets"] != float64(2) || a["sync"] != "synced" || a["synced_at"] == nil {
		t.Fatalf("deselected proj-a %v", a)
	}

	// the last Selected project stays
	code, _, errOut = s.run("projects", "deselect", "proj-b")
	if code == 0 || errBody(t, errOut).Code != "last_selected" {
		t.Fatalf("%d %s", code, errOut)
	}

	// exec: an unlisted ID is accepted with a warning
	code, out, errOut = s.run("projects", "exec", "billing-proj", "--format", "text")
	if code != 0 || !strings.Contains(errOut, "billing-proj") || !strings.Contains(errOut, "not in BigQuery's list") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	_, out, _ = s.run("projects")
	if data, _ := projectsOf(t, out); data["execution_project"] != "billing-proj" || data["execution_source"] != "chosen" {
		t.Fatalf("%v", data)
	}
	if code, _, _ := s.run("projects", "exec", "--default"); code != api.ExitUsage {
		t.Fatalf("there is no way back to the default: %d", code)
	}
}

func TestProjectsListSurvivesAListingFailure(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.fake.Fail(bqtest.Failure{Method: "ListProjects", Err: bqtest.PermissionError("projects.list")})
	code, out, _ := s.run("projects")
	data, by := projectsOf(t, out)
	if code != 0 || data["list_error"] == nil || by["proj-a"]["selected"] != true {
		t.Fatalf("%d %v", code, data)
	}
}

func TestSyncOfADeselectedProject(t *testing.T) {
	s := newSandbox(t)
	s.fake.AddProject("proj-b").Datasets = []bqtest.Dataset{bqtest.DS("core", "EU", bqtest.Table("orders", "id"))}
	s.writeConfig("projects = [\"proj-a\", \"proj-b\"]\n")
	s.run("sync")
	s.run("projects", "deselect", "proj-a")
	// plain sync covers the Selected projects only
	s.fake.Calls = nil
	s.run("sync")
	for _, c := range s.fake.CallsTo("ListDatasets") {
		if c.Project == "proj-a" {
			t.Fatal("a full sync touched a deselected project")
		}
	}
	// --project refreshes a deselected cache
	if code, _, errOut := s.run("sync", "--project", "proj-a"); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	// but never starts tracking an unknown project
	code, _, errOut := s.run("sync", "--project", "proj-z")
	if e := errBody(t, errOut); code == 0 || e.Code != "not_selected" || !strings.Contains(e.Hint, "bqt projects select proj-z") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestSearchAndSchemaOfADeselectedProject(t *testing.T) {
	s := newSandbox(t)
	s.fake.AddProject("proj-b").Datasets = []bqtest.Dataset{bqtest.DS("core", "EU", bqtest.Table("orders", "id"))}
	s.writeConfig("projects = [\"proj-a\", \"proj-b\"]\n")
	s.run("sync")
	s.run("projects", "deselect", "proj-a")
	_, out, _ := s.run("search", "orders")
	data := decode(t, out).Data.(map[string]any)
	res := data["results"].([]any)
	first, second := res[0].(map[string]any), res[1].(map[string]any)
	if first["path"].(map[string]any)["project"] != "proj-b" || first["selected"] != true || second["selected"] != false {
		t.Fatalf("%v", res)
	}
	code, out, errOut := s.run("schema", "proj-a.sales.orders")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if sc := decode(t, out).Data.(map[string]any); sc["selected"] != false {
		t.Fatalf("%v", sc)
	}
}
