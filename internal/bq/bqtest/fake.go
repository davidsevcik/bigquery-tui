// Package bqtest provides an in-memory bq.Metadata with failure injection and
// the contract suite shared by the fake and the real implementation.
package bqtest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// Dataset is a fake dataset and its contents.
type Dataset struct {
	Meta   api.DatasetMeta
	Tables []api.TableMeta
	// Denied makes region and dataset-scope queries fail with a permission
	// error, as when the user holds no metadata access to the dataset.
	Denied bool
	// NamesOK lets ListTableNames succeed even when Denied (tables.list needs
	// weaker permissions than INFORMATION_SCHEMA).
	NamesOK bool
}

// Project is a fake project.
type Project struct {
	// FriendlyName is what ListProjects reports.
	FriendlyName string
	// Unlisted leaves the project out of ListProjects (data shared at
	// dataset level: readable, but the projects list does not return it).
	Unlisted bool
	Datasets []Dataset
	// ListErr fails ListDatasets.
	ListErr error
	// Unreachable is returned by ListDatasets.
	Unreachable []string
	// Leak datasets appear in region snapshots although datasets.list does not
	// return them (deleted-in-time-travel and temporary datasets).
	Leak []Dataset
}

// Failure injects an error into matching calls. Empty fields are wildcards.
type Failure struct {
	Method   string // "ListProjects", "ListDatasets", "RegionSnapshot", "DatasetSnapshot", "ListTableNames"
	Project  string
	Location string
	Dataset  string
	Err      error
	// Times limits how many calls fail; 0 means every matching call.
	Times int
	used  int
}

// Call records one port call.
type Call struct {
	Method   string
	Project  string
	Location string
	Dataset  string
	Datasets []string
}

// Fake implements bq.Metadata, bq.Tables and bq.Jobs.
type Fake struct {
	mu       sync.Mutex
	Projects map[string]*Project
	// Listed are extra projects ListProjects returns that hold no data.
	Listed   []api.ListedProject
	Failures []*Failure
	Calls    []Call
	// Delay is slept (respecting ctx) at the start of every call.
	Delay time.Duration
	// OnCall runs at the start of every call, outside the lock.
	OnCall func(Call)
	// TableData holds live table info and rows by "project.dataset.table" for
	// the bq.Tables port. Tables without an entry report a schema derived
	// from their top-level columns and no rows.
	TableData map[string]TableData
	// OnQuery scripts the bq.Jobs port per SQL text; nil answers every query
	// with a three-row SELECT.
	OnQuery func(sql string) QueryScript
	// PageLimit caps the rows one QueryRows call returns, the way BigQuery
	// cuts a response of wide rows short. 0 means no cap.
	PageLimit int
	// ListRowsLimit caps the rows one ListRows call returns, the same way.
	// 0 means no cap.
	ListRowsLimit int

	queries []QueryCall
	jobs    map[string]*fakeJob
	jobSeq  int
}

// TableData is the live side of a fake table.
type TableData struct {
	Info bq.TableInfo
	Rows [][]any
}

// New returns an empty fake.
func New() *Fake { return &Fake{Projects: map[string]*Project{}} }

// AddProject adds (or returns) a project.
func (f *Fake) AddProject(name string) *Project {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.Projects[name]
	if p == nil {
		p = &Project{}
		f.Projects[name] = p
	}
	return p
}

// Fail registers a failure.
func (f *Fake) Fail(fl Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Failures = append(f.Failures, &fl)
}

// CallsTo returns recorded calls for a method.
func (f *Fake) CallsTo(method string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.Calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *Fake) begin(ctx context.Context, c Call) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, c)
	hook := f.OnCall
	delay := f.Delay
	var injected error
	for _, fl := range f.Failures {
		if fl.Method != "" && fl.Method != c.Method {
			continue
		}
		if fl.Project != "" && fl.Project != c.Project {
			continue
		}
		if fl.Location != "" && !strings.EqualFold(fl.Location, c.Location) {
			continue
		}
		if fl.Dataset != "" && fl.Dataset != c.Dataset {
			continue
		}
		if fl.Times > 0 && fl.used >= fl.Times {
			continue
		}
		fl.used++
		injected = fl.Err
		break
	}
	f.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return bq.Wrap("fake", ctx.Err())
		}
	}
	if err := ctx.Err(); err != nil {
		return bq.Wrap("fake", err)
	}
	return injected
}

func (f *Fake) project(name string) (*Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.Projects[name]
	if p == nil {
		return nil, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("project %q not found", name)}
	}
	return p, nil
}

// PermissionError is a ready-made injected permission failure.
func PermissionError(what string) error {
	return &bq.Error{Kind: bq.KindPermission, Err: fmt.Errorf("access denied: %s", what)}
}

// TransientError is a ready-made injected transient failure.
func TransientError(what string) error {
	return &bq.Error{Kind: bq.KindTransient, Err: fmt.Errorf("backend error: %s", what)}
}

// ListProjects implements bq.Metadata: every project not Unlisted, plus
// Listed, sorted by ID.
func (f *Fake) ListProjects(ctx context.Context) ([]api.ListedProject, error) {
	if err := f.begin(ctx, Call{Method: "ListProjects"}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]api.ListedProject(nil), f.Listed...)
	for id, p := range f.Projects {
		if !p.Unlisted {
			out = append(out, api.ListedProject{ID: id, FriendlyName: p.FriendlyName})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListDatasets implements bq.Metadata.
func (f *Fake) ListDatasets(ctx context.Context, project string) (bq.DatasetList, error) {
	if err := f.begin(ctx, Call{Method: "ListDatasets", Project: project}); err != nil {
		return bq.DatasetList{}, err
	}
	p, err := f.project(project)
	if err != nil {
		return bq.DatasetList{}, err
	}
	if p.ListErr != nil {
		return bq.DatasetList{}, p.ListErr
	}
	var out bq.DatasetList
	for _, d := range p.Datasets {
		m := d.Meta
		m.Project = project
		// datasets.list does not return descriptions
		m.Description = ""
		out.Datasets = append(out.Datasets, m)
	}
	sort.Slice(out.Datasets, func(i, j int) bool { return out.Datasets[i].Name < out.Datasets[j].Name })
	out.Unreachable = append(out.Unreachable, p.Unreachable...)
	return out, nil
}

// RegionSnapshot implements bq.Metadata.
func (f *Fake) RegionSnapshot(ctx context.Context, req bq.RegionRequest) (*api.Snapshot, error) {
	if err := f.begin(ctx, Call{Method: "RegionSnapshot", Project: req.Project, Location: req.Location, Datasets: append([]string(nil), req.Datasets...)}); err != nil {
		return nil, err
	}
	p, err := f.project(req.Project)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, d := range req.Datasets {
		want[d] = true
	}
	snap := &api.Snapshot{}
	add := func(d Dataset, requestedOnly bool) error {
		if !strings.EqualFold(d.Meta.Location, req.Location) {
			return nil
		}
		if requestedOnly && !want[d.Meta.Name] {
			return nil
		}
		if d.Denied {
			return PermissionError(fmt.Sprintf("region view for %s.%s", req.Project, d.Meta.Name))
		}
		m := d.Meta
		m.Project = req.Project
		snap.Datasets = append(snap.Datasets, m)
		for _, t := range d.Tables {
			t.Dataset = d.Meta.Name
			snap.Tables = append(snap.Tables, cloneTable(t))
		}
		return nil
	}
	for _, d := range p.Datasets {
		if err := add(d, true); err != nil {
			return nil, err
		}
	}
	for _, d := range p.Leak {
		if err := add(d, false); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// DatasetSnapshot implements bq.Metadata.
func (f *Fake) DatasetSnapshot(ctx context.Context, req bq.DatasetRequest) (*api.Snapshot, error) {
	if err := f.begin(ctx, Call{Method: "DatasetSnapshot", Project: req.Project, Location: req.Location, Dataset: req.Dataset}); err != nil {
		return nil, err
	}
	p, err := f.project(req.Project)
	if err != nil {
		return nil, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != req.Dataset {
			continue
		}
		if d.Denied {
			return nil, PermissionError(fmt.Sprintf("dataset %s.%s", req.Project, req.Dataset))
		}
		m := d.Meta
		m.Project = req.Project
		snap := &api.Snapshot{Datasets: []api.DatasetMeta{m}}
		for _, t := range d.Tables {
			t.Dataset = d.Meta.Name
			snap.Tables = append(snap.Tables, cloneTable(t))
		}
		return snap, nil
	}
	return nil, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("dataset %s.%s not found", req.Project, req.Dataset)}
}

// ListTableNames implements bq.Metadata.
func (f *Fake) ListTableNames(ctx context.Context, project, dataset string) ([]api.TableMeta, error) {
	if err := f.begin(ctx, Call{Method: "ListTableNames", Project: project, Dataset: dataset}); err != nil {
		return nil, err
	}
	p, err := f.project(project)
	if err != nil {
		return nil, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != dataset {
			continue
		}
		if d.Denied && !d.NamesOK {
			return nil, PermissionError(fmt.Sprintf("tables.list on %s.%s", project, dataset))
		}
		var out []api.TableMeta
		for _, t := range d.Tables {
			out = append(out, api.TableMeta{Dataset: dataset, Name: t.Name, Type: t.Type, CreatedAt: t.CreatedAt, Labels: t.Labels, NamesOnly: true})
		}
		return out, nil
	}
	return nil, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("dataset %s.%s not found", project, dataset)}
}

func cloneTable(t api.TableMeta) api.TableMeta {
	t.Columns = append([]api.ColumnMeta(nil), t.Columns...)
	return t
}

// Table builds a fake table with simple top-level STRING columns.
func Table(name string, cols ...string) api.TableMeta {
	t := api.TableMeta{Name: name, Type: api.TypeTable, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	for i, c := range cols {
		t.Columns = append(t.Columns, api.ColumnMeta{FieldPath: c, Ordinal: i + 1, DataType: "STRING"})
	}
	return t
}

// DS builds a fake dataset.
func DS(name, location string, tables ...api.TableMeta) Dataset {
	return Dataset{Meta: api.DatasetMeta{Name: name, Location: location, Description: "dataset " + name}, Tables: tables}
}

// GetTable implements bq.Tables.
func (f *Fake) GetTable(ctx context.Context, ref bq.TableRef) (bq.TableInfo, error) {
	if err := f.begin(ctx, Call{Method: "GetTable", Project: ref.Project, Dataset: ref.Dataset}); err != nil {
		return bq.TableInfo{}, err
	}
	f.mu.Lock()
	td, ok := f.TableData[ref.String()]
	f.mu.Unlock()
	if ok {
		info := td.Info
		if info.Rows == 0 {
			info.Rows = int64(len(td.Rows))
		}
		return info, nil
	}
	p, err := f.project(ref.Project)
	if err != nil {
		return bq.TableInfo{}, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != ref.Dataset {
			continue
		}
		for _, t := range d.Tables {
			if t.Name != ref.Table {
				continue
			}
			info := bq.TableInfo{Type: t.Type}
			for _, c := range t.Columns {
				if c.TopLevel() {
					info.Schema = append(info.Schema, bq.Field{Name: c.FieldPath, Type: c.DataType})
				}
			}
			return info, nil
		}
	}
	return bq.TableInfo{}, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("table %s not found", ref)}
}

// ListRows implements bq.Tables.
func (f *Fake) ListRows(ctx context.Context, ref bq.TableRef, startIndex int64, maxRows int) (bq.RawPage, error) {
	if err := f.begin(ctx, Call{Method: "ListRows", Project: ref.Project, Dataset: ref.Dataset}); err != nil {
		return bq.RawPage{}, err
	}
	info, err := f.GetTable(ctx, ref)
	if err != nil {
		return bq.RawPage{}, err
	}
	if info.Type == api.TypeView || info.Type == api.TypeExternal {
		return bq.RawPage{}, &bq.Error{Kind: bq.KindOther, Op: "fake", Err: fmt.Errorf("cannot list a table of type %s", info.Type)}
	}
	f.mu.Lock()
	rows := f.TableData[ref.String()].Rows
	limit := f.ListRowsLimit
	f.mu.Unlock()
	if limit > 0 {
		maxRows = min(maxRows, limit)
	}
	page := bq.RawPage{TotalRows: int64(len(rows))}
	lo := min(int(startIndex), len(rows))
	hi := min(lo+maxRows, len(rows))
	page.Rows = append(page.Rows, rows[lo:hi]...)
	return page, nil
}
