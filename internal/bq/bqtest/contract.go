package bqtest

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

type ContractOptions struct {
	Project         string
	MaxDatasets     int
	MaxLocations    int
	FallbackSamples int
	Family          *FamilyCase
}

type FamilyCase struct {
	Project  string
	Location string
	Dataset  string
	Patterns []string
	Keep     []string
}

func runColumnFilter(t *testing.T, md bq.Metadata, project string, f FamilyCase) {
	t.Helper()
	ctx := context.Background()
	if f.Project != "" {
		project = f.Project
	}
	res := make([]*regexp.Regexp, len(f.Patterns))
	parts := make([]string, len(f.Patterns))
	for i, p := range f.Patterns {
		parts[i] = "^(?:" + p + ")$"
		res[i] = regexp.MustCompile(parts[i])
	}
	req := bq.RegionRequest{Project: project, Location: f.Location, Datasets: []string{f.Dataset}}
	columns := func(cf bq.ColumnFilter) ([]bq.TableColumns, error) { return md.RegionColumns(ctx, req, cf) }
	all, err := columns(bq.ColumnFilter{})
	if err != nil && bq.KindOf(err) == bq.KindPermission {
		t.Logf("no region-scope access (%v): checking the dataset views", err)
		dreq := bq.DatasetRequest{Project: project, Location: f.Location, Dataset: f.Dataset}
		columns = func(cf bq.ColumnFilter) ([]bq.TableColumns, error) { return md.DatasetColumns(ctx, dreq, cf) }
		all, err = columns(bq.ColumnFilter{})
	}
	if err != nil {
		if bq.KindOf(err) == bq.KindPermission {
			t.Skipf("no access to the views of %s.%s: %v", project, f.Dataset, err)
		}
		t.Fatalf("columns: %v", err)
	}
	keep := map[string]bool{}
	var keepNames []string
	for _, n := range f.Keep {
		keep[n] = true
		keepNames = append(keepNames, f.Dataset+"."+n)
	}
	want := map[string]bool{}
	byName := map[string][]api.ColumnMeta{}
	left := 0
	for _, tc := range all {
		if tc.Dataset != f.Dataset {
			continue
		}
		byName[tc.Table] = tc.Columns
		matches := false
		for _, re := range res {
			matches = matches || re.MatchString(tc.Table)
		}
		if !matches || keep[tc.Table] {
			want[tc.Table] = true
		} else {
			left++
		}
	}
	if left == 0 {
		t.Fatalf("the family case leaves no table out: patterns %q match nothing in %s beyond the keep list", f.Patterns, f.Dataset)
	}
	got, err := columns(bq.ColumnFilter{Pattern: strings.Join(parts, "|"), Keep: keepNames})
	if err != nil {
		t.Fatalf("filtered columns: %v", err)
	}
	have := map[string]bool{}
	for _, tc := range got {
		if tc.Dataset != f.Dataset {
			continue
		}
		if !want[tc.Table] {
			t.Errorf("%s.%s matches a pattern and is not kept, yet its columns were read", tc.Dataset, tc.Table)
		}
		if g, w := fieldPaths(tc.Columns), fieldPaths(byName[tc.Table]); g != w {
			t.Errorf("%s.%s: the filtered read has other field paths than the unfiltered one", tc.Dataset, tc.Table)
		}
		have[tc.Table] = true
	}
	for n := range want {
		if !have[n] {
			t.Errorf("%s.%s should have columns (kept, or matching no pattern), got none", f.Dataset, n)
		}
	}
	t.Logf("%s.%s: %d tables, %d left out by the filter, %d read", project, f.Dataset, len(want)+left, left, len(have))
	if len(f.Keep) == 0 {
		return
	}
	only, err := columns(bq.ColumnFilter{Pattern: "^(?:.*)$", Only: keepNames[:1]})
	if err != nil {
		t.Fatalf("columns by name: %v", err)
	}
	if len(only) != 1 || only[0].Table != f.Keep[0] {
		t.Fatalf("columns by name returned %d tables, want only %s", len(only), f.Keep[0])
	}
	tm, err := md.GetTableMeta(ctx, project, f.Dataset, f.Keep[0])
	if err != nil {
		t.Fatalf("GetTableMeta(%s.%s): %v", f.Dataset, f.Keep[0], err)
	}
	if g, w := fieldPaths(tm.Columns), fieldPaths(byName[f.Keep[0]]); g != w {
		t.Fatalf("GetTableMeta(%s.%s) field paths differ from the views\n got %s\nwant %s", f.Dataset, f.Keep[0], g, w)
	}
	t.Logf("GetTableMeta(%s.%s): %d field paths, as the views", f.Dataset, f.Keep[0], len(tm.Columns))
}

func withColumns(t *testing.T, s *api.Snapshot, cols []bq.TableColumns) *api.Snapshot {
	t.Helper()
	at := map[string]int{}
	for i, tb := range s.Tables {
		at[tb.Dataset+"."+tb.Name] = i
	}
	for _, tc := range cols {
		i, ok := at[tc.Dataset+"."+tc.Table]
		// ADR-0004
		if !ok {
			continue
		}
		s.Tables[i].Columns = append(s.Tables[i].Columns, tc.Columns...)
	}
	return s
}

func fieldPaths(cols []api.ColumnMeta) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.FieldPath
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

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

	projects, err := md.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	ids := map[string]bool{}
	for _, p := range projects {
		if p.ID == "" || ids[p.ID] {
			t.Fatalf("ListProjects: empty or duplicate ID in %+v", projects)
		}
		ids[p.ID] = true
	}
	if !ids[o.Project] {
		t.Fatalf("ListProjects does not include %s: %+v", o.Project, projects)
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
		t.Run("region/"+loc, func(t *testing.T) { runRegion(t, md, o, loc, names) })
	}

	if f := o.Family; f != nil {
		t.Run("column filter", func(t *testing.T) {
			runColumnFilter(t, md, o.Project, *f)
		})
	}

	t.Run("get dataset", func(t *testing.T) {
		d := list.Datasets[0]
		got, err := md.GetDataset(ctx, o.Project, d.Name)
		if err != nil {
			t.Fatalf("GetDataset(%s): %v", d.Name, err)
		}
		if got.Project != o.Project || got.Name != d.Name || !strings.EqualFold(got.Location, d.Location) {
			t.Fatalf("GetDataset = %+v, want the listed %+v", got, d)
		}
		_, err = md.GetDataset(ctx, o.Project, "bqt_does_not_exist_zz")
		if k := bq.KindOf(err); k != bq.KindNotFound && k != bq.KindPermission {
			t.Fatalf("unknown dataset should classify as not-found or permission, got %v (%v)", k, err)
		}
	})

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

func runRegion(t *testing.T, md bq.Metadata, o ContractOptions, loc string, names []string) {
	t.Helper()
	ctx := context.Background()
	req := bq.RegionRequest{Project: o.Project, Location: loc, Datasets: names}
	snap, err := md.RegionTables(ctx, req)
	if err != nil {
		if bq.KindOf(err) == bq.KindPermission {
			t.Skipf("no region-scope access: %v", err)
		}
		t.Fatalf("RegionTables: %v", err)
	}
	for _, tb := range snap.Tables {
		if len(tb.Columns) > 0 {
			t.Fatalf("phase 1 returns no columns: %s.%s has %d", tb.Dataset, tb.Name, len(tb.Columns))
		}
	}
	cols, err := md.RegionColumns(ctx, req, bq.ColumnFilter{})
	if err != nil {
		t.Fatalf("RegionColumns: %v", err)
	}
	snap = withColumns(t, snap, cols)
	checkSnapshot(t, snap, names, o.Project)

	if len(snap.Tables) == 0 {
		return
	}
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
		checkFallbacks(t, md, o, loc, d, snap, region)
	}
}

func checkFallbacks(t *testing.T, md bq.Metadata, o ContractOptions, loc, d string, snap *api.Snapshot, region []api.TableMeta) {
	t.Helper()
	ctx := context.Background()
	dreq := bq.DatasetRequest{Project: o.Project, Location: loc, Dataset: d}
	ds, err := md.DatasetTables(ctx, dreq)
	if err != nil {
		t.Fatalf("DatasetTables(%s): %v", d, err)
	}
	dcols, err := md.DatasetColumns(ctx, dreq, bq.ColumnFilter{})
	if err != nil {
		t.Fatalf("DatasetColumns(%s): %v", d, err)
	}
	ds = withColumns(t, ds, dcols)
	checkSnapshot(t, ds, []string{d}, o.Project)
	if got, want := tableNames(tablesOf(ds, d)), tableNames(region); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dataset %s: dataset-scope tables differ from region tables\n got %v\nwant %v", d, got, want)
	}
	if got, want := columnCount(tablesOf(ds, d)), columnCount(region); got != want {
		t.Fatalf("dataset %s: dataset-scope has %d columns, region has %d", d, got, want)
	}
	meta, err := md.GetDataset(ctx, o.Project, d)
	if err != nil {
		t.Fatalf("GetDataset(%s): %v", d, err)
	}
	for _, rd := range snap.Datasets {
		if rd.Name == d && (meta.Description != rd.Description || meta.FriendlyName != rd.FriendlyName || len(meta.Labels) != len(rd.Labels)) {
			t.Fatalf("dataset %s: datasets.get differs from the region view\n got %+v\nwant %+v", d, meta, rd)
		}
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
	for _, tb := range region {
		if len(tb.Columns) == 0 {
			continue
		}
		got, err := md.GetTableMeta(ctx, o.Project, d, tb.Name)
		if err != nil {
			t.Fatalf("GetTableMeta(%s.%s): %v", d, tb.Name, err)
		}
		if got.Name != tb.Name || got.Type != tb.Type {
			t.Fatalf("GetTableMeta(%s.%s) = %s %s, want %s %s", d, tb.Name, got.Name, got.Type, tb.Name, tb.Type)
		}
		if g, w := fieldPaths(got.Columns), fieldPaths(tb.Columns); g != w {
			t.Fatalf("GetTableMeta(%s.%s) field paths differ from the views\n got %s\nwant %s", d, tb.Name, g, w)
		}
		break
	}
}
