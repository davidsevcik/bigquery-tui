package search

import (
	"context"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Filter returns every object that passes the Explorer's in-place filter,
// best match first (by path when the query has no free term).
//
// Unlike Search, free terms match only an object's own name, so a term that
// names a dataset does not keep the tables inside it. Table filters (type:,
// col:, label:, desc:) keep a table that passes them and a project or dataset
// that holds one; p: and d: narrow by path.
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
	f.containerHits()
	if f.want(api.KindTable) || f.want(api.KindShards) {
		f.tableHits()
	}
	resp.Matches = f.matches()
	resp.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
}

// filterRun holds one Filter call's state between its stages.
type filterRun struct {
	x  *Index
	c  *compiled
	o  api.FilterOptions
	sc scoper
	// tableFilters: the query has filters beyond p: and d:, so a container
	// is kept only when it holds a passing table (or passes them itself).
	// dsFilters: the query has filters beyond p:, so a project is kept only
	// when it holds a kept dataset.
	tableFilters, dsFilters bool
	pasted                  int // the table a pasted path resolved to, or -1

	pass []bool   // pass[i]: table i is in scope and passes every filter
	own  []ownHit // own[i]: table i's own-name score; ok false when it fails

	out   []hit
	paths []api.ObjectPath         // paths[h.i] for every hit
	famOf map[int]*api.ShardFamily // family of a shard_family path, by paths index
}

func newFilterRun(x *Index, c *compiled, o api.FilterOptions, path *api.ObjectPath) *filterRun {
	f := &filterRun{
		x: x, c: c, o: o,
		sc:           newScoper(api.SearchOptions{Scope: o.Scope, Hidden: o.Hidden}),
		tableFilters: !onlyScopeFilters(c.filters),
		dsFilters:    len(c.filters) > 0 && !onlyKey(c.filters, "p"),
		pasted:       -1,
		famOf:        map[int]*api.ShardFamily{},
	}
	if path != nil {
		if i, ok := x.resolve(path.String()); ok {
			// a pasted path keeps that table and the containers that hold it
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

// passTables fills pass and own, in parallel chunks.
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

// add appends a hit for a container whose own name matches the terms.
func (f *filterRun) add(k api.ResultKind, p api.ObjectPath, lname, name string) {
	if maxS, sum, ok := ownScore(lname, f.c.terms, true); ok {
		f.addHit(hit{kind: k, max: maxS, sum: sum, name: name}, p)
	}
}

// addHit appends h with p as its path.
func (f *filterRun) addHit(h hit, p api.ObjectPath) {
	h.i, h.path = len(f.paths), p.String()
	f.out = append(f.out, h)
	f.paths = append(f.paths, p)
}

// containerHits adds the projects and datasets that pass.
func (f *filterRun) containerHits() {
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
				f.add(api.KindDataset, api.ObjectPath{Project: p.name, Dataset: d.name}, d.lname, d.name)
			}
		}
		if f.want(api.KindProject) && (projHolds || !f.dsFilters) && passDataset(p, &dsEntry{}, onlyP(f.c.filters)) {
			f.add(api.KindProject, api.ObjectPath{Project: p.name}, p.lname, p.name)
		}
	}
}

// keepDataset reports whether d is in scope, passes the path filters and,
// under table filters, passes them itself or holds a table that does.
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

// famKey names a shard family: its dataset, base name and table type.
type famKey struct {
	ds   int
	name string
	typ  api.TableType
}

// tableHits adds the tables that pass and the shard families they belong
// to. A family passes when one of its members does, ranked by its best
// member.
func (f *filterRun) tableHits() {
	x := f.x
	cols := len(f.c.colVals) > 0
	fams := map[famKey]int{} // index into out
	for i := range x.tables {
		if !f.own[i].ok {
			continue
		}
		t := &x.tables[i]
		maxS, sum := f.own[i].max, f.own[i].sum
		p := x.tablePath(i)
		// a member is a match of its own only when the filter reaches into
		// its varying part (or its own schema, under col:)
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

// matches sorts the hits and turns them into the response's matches.
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

// ownScore scores a name against every term; each term must match it.
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

// passDatasetOwn reports whether a dataset satisfies the table filters by
// itself: only desc: and label: can describe a dataset, through its own
// description and labels.
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

// ProjectMatches is the Projects dialog's filter: every space-separated term
// must occur, ignoring case, in the project ID or its friendly name.
func ProjectMatches(id, friendlyName, query string) bool {
	hay := strings.ToLower(id + " " + friendlyName)
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}
