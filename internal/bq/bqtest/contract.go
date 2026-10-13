package bqtest

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// ContractOptions bounds the contract run (the real implementation is read-only
// and billed, so it samples).
type ContractOptions struct {
	Project string
	// MaxDatasets limits how many datasets a location's region request covers.
	MaxDatasets int
	// MaxLocations limits how many locations are exercised.
	MaxLocations int
	// FallbackSamples is how many datasets to check through the dataset-scope
	// and tables.list fallbacks.
	FallbackSamples int
}

// RunMetadataContract checks the behaviour every bq.Metadata must share. It
// asserts invariants rather than exact data so it can run against a live
// project as well as the fake.
func RunMetadataContract(t *testing.T, md bq.Metadata, o ContractOptions) {
	t.Helper()
	ctx := context.Background()
	if o.MaxDatasets == 0 {
		o.MaxDatasets = 1 << 30
	}
	if o.MaxLocations == 0 {
		o.MaxLocations = 1 << 30
	}
	if o.FallbackSamples == 0 {
		o.FallbackSamples = 2
	}

	list, err := md.ListDatasets(ctx, o.Project)
	if err != nil {
		t.Fatalf("ListDatasets: %v", err)
	}
	if len(list.Datasets) == 0 {
		t.Skip("project has no datasets")
	}
	byLoc := map[string][]string{}
	seen := map[string]bool{}
	for _, d := range list.Datasets {
		if d.Name == "" || d.Location == "" {
			t.Fatalf("dataset needs a name and a location: %+v", d)
		}
		if d.Project != o.Project {
			t.Fatalf("dataset project = %q, want %q", d.Project, o.Project)
		}
		if seen[d.Name] {
			t.Fatalf("duplicate dataset %q", d.Name)
		}
		seen[d.Name] = true
		byLoc[d.Location] = append(byLoc[d.Location], d.Name)
	}
	locs := make([]string, 0, len(byLoc))
	for l := range byLoc {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	if len(locs) > o.MaxLocations {
		locs = locs[:o.MaxLocations]
	}

	for _, loc := range locs {
		names := byLoc[loc]
		sort.Strings(names)
		if len(names) > o.MaxDatasets {
			names = names[:o.MaxDatasets]
		}
		t.Run("region/"+loc, func(t *testing.T) {
			snap, err := md.RegionSnapshot(ctx, bq.RegionRequest{Project: o.Project, Location: loc, Datasets: names})
			if err != nil {
				if bq.KindOf(err) == bq.KindPermission {
					t.Skipf("no region-scope access: %v", err)
				}
				t.Fatalf("RegionSnapshot: %v", err)
			}
			checkSnapshot(t, snap, names, o.Project)

			if len(snap.Tables) == 0 {
				return
			}
			// dataset-scope and tables.list must agree with the region view.
			sampled := 0
			for _, d := range names {
				if sampled >= o.FallbackSamples {
					break
				}
				region := tablesOf(snap, d)
				if len(region) == 0 {
					continue
				}
				sampled++
				ds, err := md.DatasetSnapshot(ctx, bq.DatasetRequest{Project: o.Project, Location: loc, Dataset: d})
				if err != nil {
					t.Fatalf("DatasetSnapshot(%s): %v", d, err)
				}
				checkSnapshot(t, ds, []string{d}, o.Project)
				if got, want := tableNames(tablesOf(ds, d)), tableNames(region); strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("dataset %s: dataset-scope tables differ from region tables\n got %v\nwant %v", d, got, want)
				}
				if got, want := columnCount(tablesOf(ds, d)), columnCount(region); got != want {
					t.Fatalf("dataset %s: dataset-scope has %d columns, region has %d", d, got, want)
				}
				names2, err := md.ListTableNames(ctx, o.Project, d)
				if err != nil {
					t.Fatalf("ListTableNames(%s): %v", d, err)
				}
				for _, tb := range names2 {
					if !tb.NamesOnly || len(tb.Columns) != 0 {
						t.Fatalf("ListTableNames must return names only: %+v", tb)
					}
				}
				if got, want := tableNames(names2), tableNames(region); strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("dataset %s: tables.list names differ from region tables\n got %v\nwant %v", d, got, want)
				}
			}
		})
	}

	t.Run("unknown project", func(t *testing.T) {
		_, err := md.ListDatasets(ctx, o.Project+"-does-not-exist-zz")
		if err == nil {
			t.Fatal("expected an error for an unknown project")
		}
		if k := bq.KindOf(err); k != bq.KindNotFound && k != bq.KindPermission {
			t.Fatalf("unknown project should classify as not-found or permission, got %v (%v)", k, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := md.ListDatasets(cctx, o.Project)
		if err == nil {
			t.Fatal("cancelled context must fail")
		}
		if bq.KindOf(err) != bq.KindCancelled {
			t.Fatalf("want KindCancelled, got %v (%v)", bq.KindOf(err), err)
		}
	})
}

func checkSnapshot(t *testing.T, s *api.Snapshot, requested []string, project string) {
	t.Helper()
	want := map[string]bool{}
	for _, d := range requested {
		want[d] = true
	}
	have := map[string]bool{}
	for _, d := range s.Datasets {
		if !want[d.Name] {
			t.Errorf("snapshot contains unrequested dataset %q", d.Name)
		}
		if d.Project != project || d.Location == "" {
			t.Errorf("dataset needs project and location: %+v", d)
		}
		have[d.Name] = true
	}
	type key struct{ ds, tb string }
	tables := map[key]bool{}
	for _, tb := range s.Tables {
		if !have[tb.Dataset] {
			t.Errorf("table %s.%s has no dataset entry in the snapshot", tb.Dataset, tb.Name)
		}
		k := key{tb.Dataset, tb.Name}
		if tables[k] {
			t.Errorf("duplicate table %s.%s", tb.Dataset, tb.Name)
		}
		tables[k] = true
		switch tb.Type {
		case api.TypeTable, api.TypeView, api.TypeMView, api.TypeExternal, api.TypeSnapshot, api.TypeClone, api.TypeOther:
		default:
			t.Errorf("table %s.%s has unnormalized type %q", tb.Dataset, tb.Name, tb.Type)
		}
		if tb.NamesOnly {
			t.Errorf("table %s.%s is names-only in a full snapshot", tb.Dataset, tb.Name)
		}
		paths := map[string]bool{}
		for _, c := range tb.Columns {
			if c.FieldPath == "" {
				t.Errorf("table %s.%s has an empty field path", tb.Dataset, tb.Name)
			}
			if paths[c.FieldPath] {
				t.Errorf("table %s.%s has duplicate field path %q", tb.Dataset, tb.Name, c.FieldPath)
			}
			paths[c.FieldPath] = true
		}
		for _, c := range tb.Columns {
			if p := c.ParentPath(); p != "" && !paths[p] {
				t.Errorf("table %s.%s: field %q has no parent %q", tb.Dataset, tb.Name, c.FieldPath, p)
			}
		}
		if tb.Type == api.TypeView && tb.ViewSQL == "" {
			t.Errorf("view %s.%s has no SQL", tb.Dataset, tb.Name)
		}
	}
}

func tablesOf(s *api.Snapshot, dataset string) []api.TableMeta {
	var out []api.TableMeta
	for _, tb := range s.Tables {
		if tb.Dataset == dataset {
			out = append(out, tb)
		}
	}
	return out
}

func tableNames(ts []api.TableMeta) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

func columnCount(ts []api.TableMeta) int {
	n := 0
	for _, t := range ts {
		n += len(t.Columns)
	}
	return n
}
