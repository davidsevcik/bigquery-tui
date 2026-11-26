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

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq"
)

type Dataset struct {
	Meta    api.DatasetMeta
	Tables  []api.TableMeta
	Denied  bool
	NamesOK bool
}

type Project struct {
	FriendlyName string
	Unlisted     bool
	Datasets     []Dataset
	ListErr      error
	Unreachable  []string
	Leak         []Dataset
}

type Failure struct {
	Method   string
	Project  string
	Location string
	Dataset  string
	Err      error
	Times    int
	used     int
}

type Call struct {
	Method   string
	Project  string
	Location string
	Dataset  string
	Datasets []string
	Table    string
	Filter   bq.ColumnFilter
}

type Fake struct {
	mu            sync.Mutex
	Projects      map[string]*Project
	Listed        []api.ListedProject
	Failures      []*Failure
	Calls         []Call
	Delay         time.Duration
	OnCall        func(Call)
	TableData     map[string]TableData
	OnQuery       func(sql string) QueryScript
	PageLimit     int
	ListRowsLimit int
	MissColumns   map[string]bool

	columnReads []string

	queries []QueryCall
	jobs    map[jobKey]*fakeJob
	jobSeq  int
}

type TableData struct {
	Info bq.TableInfo
	Rows [][]any
}

func New() *Fake { return &Fake{Projects: map[string]*Project{}} }

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

var methods = map[string]bool{
	"ListProjects": true, "ListDatasets": true, "GetDataset": true, "ListTableNames": true,
	"RegionTables": true, "RegionColumns": true, "DatasetTables": true, "DatasetColumns": true,
	"GetTableMeta": true, "GetTable": true, "ListRows": true,
	"DryRun": true, "StartQuery": true, "WaitJob": true, "QueryRows": true, "CancelJob": true,
}

func (f *Fake) Fail(fl Failure) {
	if fl.Method != "" && !methods[fl.Method] {
		panic("bqtest: Fail on unknown method " + fl.Method)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Failures = append(f.Failures, &fl)
}

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

func PermissionError(what string) error {
	return &bq.Error{Kind: bq.KindPermission, Err: fmt.Errorf("access denied: %s", what)}
}

func TransientError(what string) error {
	return &bq.Error{Kind: bq.KindTransient, Err: fmt.Errorf("backend error: %s", what)}
}

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
		// ADR-0005
		m.Description = ""
		out.Datasets = append(out.Datasets, m)
	}
	sort.Slice(out.Datasets, func(i, j int) bool { return out.Datasets[i].Name < out.Datasets[j].Name })
	out.Unreachable = append(out.Unreachable, p.Unreachable...)
	return out, nil
}

func (f *Fake) GetDataset(ctx context.Context, project, dataset string) (api.DatasetMeta, error) {
	if err := f.begin(ctx, Call{Method: "GetDataset", Project: project, Dataset: dataset}); err != nil {
		return api.DatasetMeta{}, err
	}
	p, err := f.project(project)
	if err != nil {
		return api.DatasetMeta{}, err
	}
	for _, d := range p.Datasets {
		if d.Meta.Name != dataset {
			continue
		}
		if d.Denied && !d.NamesOK {
			return api.DatasetMeta{}, PermissionError(fmt.Sprintf("datasets.get on %s.%s", project, dataset))
		}
		m := d.Meta
		m.Project = project
		return m, nil
	}
	return api.DatasetMeta{}, &bq.Error{Kind: bq.KindNotFound, Op: "fake", Err: fmt.Errorf("dataset %s.%s not found", project, dataset)}
}

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

func Table(name string, cols ...string) api.TableMeta {
	t := api.TableMeta{Name: name, Type: api.TypeTable, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	for i, c := range cols {
		t.Columns = append(t.Columns, api.ColumnMeta{FieldPath: c, Ordinal: i + 1, DataType: "STRING"})
	}
	return t
}

func DS(name, location string, tables ...api.TableMeta) Dataset {
	return Dataset{Meta: api.DatasetMeta{Name: name, Location: location, Description: "dataset " + name}, Tables: tables}
}

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

func (f *Fake) ListRows(ctx context.Context, ref bq.TableRef, startIndex int64, maxRows int) (bq.RawPage, error) {
	if err := f.begin(ctx, Call{Method: "ListRows", Project: ref.Project, Dataset: ref.Dataset}); err != nil {
		return bq.RawPage{}, err
	}
	info, err := f.GetTable(ctx, ref)
	if err != nil {
		return bq.RawPage{}, err
	}
	if info.Type == api.TypeView || info.Type == api.TypeMView || info.Type == api.TypeExternal {
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
