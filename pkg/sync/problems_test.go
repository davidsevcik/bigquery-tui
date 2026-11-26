package sync

import (
	"reflect"
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func unit(loc string, status api.UnitStatus, errText string) api.UnitState {
	return api.UnitState{UnitRef: api.UnitRef{Location: loc}, Status: status, Error: errText}
}

func TestUnitProblem(t *testing.T) {
	for _, c := range []struct {
		u    api.UnitState
		want bool
	}{
		{api.UnitState{Status: api.UnitOK}, false},
		{api.UnitState{Status: api.UnitDegraded}, true},
		{api.UnitState{Status: api.UnitInaccessible}, true},
		{api.UnitState{Status: api.UnitOK, NamesOnly: 1}, true},
		{api.UnitState{Status: api.UnitOK, Inaccessible: 2}, true},
	} {
		if got := unitProblem(c.u); got != c.want {
			t.Errorf("%+v: %v", c.u, got)
		}
	}
}

func TestFindProblems(t *testing.T) {
	st := &api.CatalogStatus{Projects: []api.ProjectStatus{
		{Project: "acme", Selected: true, Units: []api.UnitState{
			unit("EU", api.UnitOK, ""),
			unit("US", api.UnitDegraded, "quota"),
			unit("", api.UnitOK, ""),
		}},
		{Project: "old", Selected: false, Units: []api.UnitState{unit("EU", api.UnitInaccessible, "denied")}},
	}}
	datasets := map[string][]api.BrowseNode{
		"acme": {
			{Name: "us_a", Location: "US", Status: api.DatasetPending},
			{Name: "us_b", Location: "US", Status: api.DatasetPending},
			{Name: "eu_live", Location: "EU", Status: api.DatasetPending, Syncing: true},
			{Name: "eu_done", Location: "EU"},
			{Name: "tok", Location: "asia-northeast1", Status: api.DatasetPending},
			{Name: "fra", Location: "europe-west3", Status: api.DatasetPending},
		},
	}
	got := FindProblems(st, func(p string) []api.BrowseNode { return datasets[p] }, []string{"old: denied", "acme: listing was incomplete"})
	if got.Count() != 4 || len(got.List) != 3 {
		t.Fatalf("%+v", got)
	}
	us := got.List[0]
	if us.Project != "acme" || us.Location != "US" || us.Unit == nil || us.Unit.Error != "quota" || !reflect.DeepEqual(us.NotSynced, []string{"us_a", "us_b"}) {
		t.Fatalf("a unit problem carries its not-synced datasets: %+v", us)
	}
	if got.List[1].Location != "asia-northeast1" || got.List[1].Unit != nil || !reflect.DeepEqual(got.List[1].NotSynced, []string{"tok"}) ||
		got.List[2].Location != "europe-west3" {
		t.Fatalf("%+v", got.List[1:])
	}
	if !reflect.DeepEqual(got.Warnings, []string{"acme: listing was incomplete"}) {
		t.Fatalf("a warning a unit shows already is not repeated: %q", got.Warnings)
	}

	if none := FindProblems(nil, nil, []string{"w"}); none.Count() != 1 || len(none.List) != 0 {
		t.Fatalf("without a status only the warnings count: %+v", none)
	}
}
