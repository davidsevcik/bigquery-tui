package search

import (
	"context"
	"sort"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

type projEntry struct {
	name, lname string
	datasets    []int
}

type dsEntry struct {
	id          int64
	proj        int
	name, lname string
	location    string
	desc, ldesc string
	labels      map[string]string
	status      api.DatasetStatus
	tables      []int
}

type tableEntry struct {
	id          int64
	ds          int
	name, lname string
	typ         api.TableType
	desc, ldesc string
	labels      []string // lower-case "key:value"
	namesOnly   bool
	shard       *api.ShardFamily
}

// Index is the immutable in-memory catalog (everything but columns).
type Index struct {
	projects []projEntry
	datasets []dsEntry
	tables   []tableEntry
	units    map[api.UnitRef]api.UnitState
	byPath   map[string]int // lower-case project.dataset.table -> table index
	byID     map[int64]int
}

// Empty reports whether the index holds no tables.
func (x *Index) Empty() bool { return x == nil || len(x.tables) == 0 }

// Size returns the number of datasets and tables.
func (x *Index) Size() (datasets, tables int) { return len(x.datasets), len(x.tables) }

// BuildIndex turns a catalog snapshot into an Index.
func BuildIndex(snap *store.IndexSnapshot) *Index {
	x := &Index{units: map[api.UnitRef]api.UnitState{}, byPath: map[string]int{}, byID: map[int64]int{}}
	pi := map[string]int{}
	for _, p := range snap.Projects {
		pi[p] = len(x.projects)
		x.projects = append(x.projects, projEntry{name: p, lname: strings.ToLower(p)})
	}
	di := map[int64]int{}
	for _, d := range snap.Datasets {
		p, ok := pi[d.Project]
		if !ok {
			continue
		}
		di[d.ID] = len(x.datasets)
		x.projects[p].datasets = append(x.projects[p].datasets, len(x.datasets))
		x.datasets = append(x.datasets, dsEntry{id: d.ID, proj: p, name: d.Name, lname: strings.ToLower(d.Name), location: d.Location,
			desc: d.Description, ldesc: strings.ToLower(d.Description), labels: d.Labels, status: d.Status})
	}
	for _, t := range snap.Tables {
		d, ok := di[t.DatasetID]
		if !ok {
			continue
		}
		e := tableEntry{id: t.ID, ds: d, name: t.Name, lname: strings.ToLower(t.Name), typ: t.Type,
			desc: t.Description, ldesc: strings.ToLower(t.Description), namesOnly: t.NamesOnly, shard: t.Shard}
		for k, v := range t.Labels {
			e.labels = append(e.labels, strings.ToLower(k+":"+v))
		}
		sort.Strings(e.labels)
		idx := len(x.tables)
		x.datasets[d].tables = append(x.datasets[d].tables, idx)
		x.tables = append(x.tables, e)
		x.byID[t.ID] = idx
		de := &x.datasets[d]
		x.byPath[x.projects[de.proj].lname+"."+de.lname+"."+e.lname] = idx
	}
	for _, u := range snap.Units {
		x.units[u.UnitRef] = u
	}
	return x
}

func (x *Index) tablePath(i int) api.ObjectPath {
	t := &x.tables[i]
	d := &x.datasets[t.ds]
	return api.ObjectPath{Project: x.projects[d.proj].name, Dataset: d.name, Table: t.name}
}

// colBlob is every column's lower-case "path\x00description\n" in one string,
// so a col: value is found with a handful of fast substring scans.
type colBlob struct {
	text   string
	starts []int32 // record start offsets
	table  []int32 // table index per record
}

func buildColBlob(ctx context.Context, x *Index, src ColumnSource) (*colBlob, error) {
	var b strings.Builder
	cb := &colBlob{}
	err := src.EachColumn(ctx, func(tableID int64, pathLC, descLC string) {
		ti, ok := x.byID[tableID]
		if !ok {
			return
		}
		cb.starts = append(cb.starts, int32(b.Len()))
		cb.table = append(cb.table, int32(ti))
		b.WriteString(pathLC)
		b.WriteByte(0)
		b.WriteString(strings.ReplaceAll(descLC, "\n", " "))
		b.WriteByte('\n')
	})
	if err != nil {
		return nil, err
	}
	cb.text = b.String()
	return cb, nil
}

// match returns table index -> number of columns containing any value.
func (cb *colBlob) match(ctx context.Context, values []string) map[int]int {
	out := map[int]int{}
	hit := map[int32]bool{} // records already counted (several values, one column)
	for _, v := range values {
		if v == "" || strings.ContainsAny(v, "\x00\n") {
			continue
		}
		pos := 0
		for n := 0; pos < len(cb.text); n++ {
			if n%4096 == 0 && ctx.Err() != nil {
				return out
			}
			i := strings.Index(cb.text[pos:], v)
			if i < 0 {
				break
			}
			off := int32(pos + i)
			r := sort.Search(len(cb.starts), func(k int) bool { return cb.starts[k] > off }) - 1
			if !hit[int32(r)] {
				hit[int32(r)] = true
				out[int(cb.table[r])]++
			}
			if r+1 >= len(cb.starts) {
				break
			}
			pos = int(cb.starts[r+1])
		}
	}
	return out
}
