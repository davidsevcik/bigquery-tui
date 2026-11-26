package search

import (
	"context"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func (s *Searcher) Filter(ctx context.Context, raw string, o api.FilterOptions) (api.FilterResponse, error) {
	start := time.Now()
	q := Parse(raw)
	resp := api.FilterResponse{Query: q, Matches: []api.FilterMatch{}}
	x, c, err := s.compile(ctx, q)
	if err != nil {
		return resp, err
	}
	f := newFilterRun(x, c, o, q.Path)
	if err := f.passTables(ctx); err != nil {
		return resp, err
	}
	f.addContainers()
	if f.want(api.KindTable) || f.want(api.KindShards) {
		f.addTables()
	}
	resp.Matches = f.matches()
	resp.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
}

type filterRun struct {
	x                       *Index
	c                       *compiled
	o                       api.FilterOptions
	sc                      scoper
	tableFilters, dsFilters bool
	pasted                  int

	pass []bool
	own  []ownHit

	out   []hit
	paths []api.ObjectPath
	famOf map[int]*api.ShardFamily
}

func newFilterRun(x *Index, c *compiled, o api.FilterOptions, path *api.ObjectPath) *filterRun {
	f := &filterRun{
		x: x, c: c, o: o,
		sc:           newScoper(api.SearchOptions{Scope: o.Scope, Projects: o.Projects, Hidden: o.Hidden}),
		tableFilters: !onlyScopeFilters(c.filters),
		dsFilters:    len(c.filters) > 0 && !onlyKey(c.filters, "p"),
		pasted:       -1,
		famOf:        map[int]*api.ShardFamily{},
	}
	if path != nil {
		if i, ok := x.resolve(path.String()); ok {
			f.pasted, c.terms, f.tableFilters, f.dsFilters = i, nil, true, true
		}
	}
	return f
}

func (f *filterRun) want(k api.ResultKind) bool {
	if len(f.o.Kinds) == 0 {
		return true
	}
	for _, w := range f.o.Kinds {
		if w == k {
			return true
		}
	}
	return false
}

func (f *filterRun) passTables(ctx context.Context) error {
	x, c := f.x, f.c
	f.pass = make([]bool, len(x.tables))
	f.own = make([]ownHit, len(x.tables))
	eachChunk(len(x.tables), func(_, lo, hi int) {
		for i := lo; i < hi; i++ {
			if i%2048 == 0 && ctx.Err() != nil {
				return
			}
			f.pass[i] = f.sc.table(x, i) && (f.pasted < 0 || i == f.pasted) && x.passTable(i, c, true)
			if f.pass[i] {
				f.own[i].max, f.own[i].sum, f.own[i].ok = ownScore(x.tables[i].lname, c.terms, true)
			}
		}
	})
	return ctx.Err()
}

func (f *filterRun) addIfNamed(k api.ResultKind, p api.ObjectPath, lname, name string) {
	if maxS, sum, ok := ownScore(lname, f.c.terms, true); ok {
		f.addHit(hit{kind: k, max: maxS, sum: sum, name: name}, p)
	}
}

func (f *filterRun) addHit(h hit, p api.ObjectPath) {
	h.i, h.path = len(f.paths), p.String()
	f.out = append(f.out, h)
	f.paths = append(f.paths, p)
}

func (f *filterRun) addContainers() {
	x := f.x
	for pi := range x.projects {
		p := &x.projects[pi]
		if !f.sc.project(p.name) {
			continue
		}
		projHolds := false
		for _, di := range p.datasets {
			d := &x.datasets[di]
			if !f.keepDataset(p, d) {
				continue
			}
			projHolds = true
			if f.want(api.KindDataset) {
				f.addIfNamed(api.KindDataset, api.ObjectPath{Project: p.name, Dataset: d.name}, d.lname, d.name)
			}
		}
		if f.want(api.KindProject) && (projHolds || !f.dsFilters) && passDataset(p, &dsEntry{}, onlyP(f.c.filters)) {
			f.addIfNamed(api.KindProject, api.ObjectPath{Project: p.name}, p.lname, p.name)
		}
	}
}

func (f *filterRun) keepDataset(p *projEntry, d *dsEntry) bool {
	if f.o.Scope != nil && f.o.Scope.Dataset != "" && f.o.Scope.Dataset != d.name {
		return false
	}
	if !passDataset(p, d, f.c.filters) {
		return false
	}
	if !f.tableFilters || f.pasted < 0 && passDatasetOwn(d, f.c.filters) {
		return true
	}
	for _, ti := range d.tables {
		if f.pass[ti] {
			return true
		}
	}
	return false
}

type famKey struct {
	ds   int
	name string
	typ  api.TableType
}

func (f *filterRun) addTables() {
	x := f.x
	cols := len(f.c.colVals) > 0
	fams := map[famKey]int{}
	for i := range x.tables {
		if !f.own[i].ok {
			continue
		}
		t := &x.tables[i]
		maxS, sum := f.own[i].max, f.own[i].sum
		p := x.tablePath(i)
		if f.want(api.KindTable) && (f.o.ExpandShards || f.pasted == i || x.memberHit(i, f.c.terms, cols)) {
			f.addHit(hit{kind: api.KindTable, max: maxS, sum: sum, name: t.name}, p)
		}
		if t.shard == nil || !f.want(api.KindShards) {
			continue
		}
		k := famKey{t.ds, t.shard.Name, t.typ}
		if j, seen := fams[k]; !seen {
			fams[k] = len(f.out)
			f.famOf[len(f.paths)] = t.shard
			f.addHit(hit{kind: api.KindShards, max: maxS, sum: sum, name: t.shard.Name}, api.ObjectPath{Project: p.Project, Dataset: p.Dataset, Table: t.shard.Name})
		} else if h := &f.out[j]; maxS < h.max || maxS == h.max && sum < h.sum {
			h.max, h.sum = maxS, sum
		}
	}
}

func (f *filterRun) matches() []api.FilterMatch {
	sortHits(f.out, len(f.c.terms) > 0)
	ms := make([]api.FilterMatch, 0, len(f.out))
	for _, h := range f.out {
		m := api.FilterMatch{Kind: h.kind, Path: f.paths[h.i]}
		if fam := f.famOf[h.i]; fam != nil {
			cp := *fam
			m.Shard = &cp
		}
		ms = append(ms, m)
	}
	return ms
}

type ownHit struct {
	max, sum int
	ok       bool
}

func ownScore(lname string, terms []string, fuzzy bool) (maxS, sum int, ok bool) {
	for _, t := range terms {
		tier, _ := matchName(lname, t, fuzzy)
		if tier < 0 {
			return 0, 0, false
		}
		maxS = max(maxS, tier)
		sum += tier
	}
	return maxS, sum, true
}

func passDatasetOwn(d *dsEntry, fs []api.QueryFilter) bool {
	for _, f := range fs {
		var m bool
		switch f.Key {
		case "p", "d":
			continue
		case "desc":
			m = anySub(d.ldesc, f.Values)
		case "label":
			m = datasetLabel(d, f.Values)
		default:
			return false
		}
		if m == f.Negated {
			return false
		}
	}
	return true
}

func onlyKey(fs []api.QueryFilter, key string) bool {
	for _, f := range fs {
		if f.Key != key {
			return false
		}
	}
	return true
}

func onlyP(fs []api.QueryFilter) []api.QueryFilter {
	var out []api.QueryFilter
	for _, f := range fs {
		if f.Key == "p" {
			out = append(out, f)
		}
	}
	return out
}

func ProjectMatches(id, friendlyName, query string) bool {
	hay := strings.ToLower(id + " " + friendlyName)
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}
