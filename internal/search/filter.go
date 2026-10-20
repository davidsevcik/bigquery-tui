package search

import (
	"context"
	"runtime"
	"sort"
	gosync "sync"
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
	sc := newScoper(api.SearchOptions{Scope: o.Scope, Hidden: o.Hidden})
	want := func(k api.ResultKind) bool {
		if len(o.Kinds) == 0 {
			return true
		}
		for _, w := range o.Kinds {
			if w == k {
				return true
			}
		}
		return false
	}
	tableFilters := !onlyScopeFilters(c.filters)
	dsFilters := len(c.filters) > 0 && !onlyKey(c.filters, "p")

	// pass[i]: table i is in scope and passes every filter
	pass := make([]bool, len(x.tables))
	pasted := -1
	if q.Path != nil {
		if i, ok := x.resolve(q.Path.String()); ok {
			// a pasted path keeps that table and the containers that hold it
			pasted, c.terms, tableFilters, dsFilters = i, nil, true, true
		}
	}
	// own[i]: the table's own-name score (max, sum), or max -1 when it fails
	own := make([][2]int, len(x.tables))
	workers := min(runtime.GOMAXPROCS(0), 8)
	if len(x.tables) < 4096 {
		workers = 1
	}
	var wg gosync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := len(x.tables)*w/workers, len(x.tables)*(w+1)/workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				if i%2048 == 0 && ctx.Err() != nil {
					return
				}
				own[i][0] = -1
				pass[i] = sc.table(x, i) && (pasted < 0 || i == pasted) && x.passTable(i, c)
				if pass[i] {
					if maxS, sum, ok := ownScore(x.tables[i].lname, c.terms, true); ok {
						own[i] = [2]int{maxS, sum}
					}
				}
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return resp, err
	}

	type scored struct {
		m        api.FilterMatch
		max, sum int
		name     string
		key      string // the path, for ordering
	}
	var out []scored
	add := func(k api.ResultKind, p api.ObjectPath, lname, name string, fuzzy bool) {
		if maxS, sum, ok := ownScore(lname, c.terms, fuzzy); ok {
			out = append(out, scored{api.FilterMatch{Kind: k, Path: p}, maxS, sum, name, p.String()})
		}
	}
	for pi := range x.projects {
		p := &x.projects[pi]
		if !sc.project(p.name) {
			continue
		}
		projHolds := false
		for _, di := range p.datasets {
			d := &x.datasets[di]
			if o.Scope != nil && o.Scope.Dataset != "" && o.Scope.Dataset != d.name {
				continue
			}
			if !passDataset(p, d, c.filters) {
				continue
			}
			holds := !tableFilters
			for _, ti := range d.tables {
				if holds {
					break
				}
				holds = pass[ti]
			}
			if !holds {
				continue
			}
			projHolds = true
			if want(api.KindDataset) {
				add(api.KindDataset, api.ObjectPath{Project: p.name, Dataset: d.name}, d.lname, d.name, false)
			}
		}
		if want(api.KindProject) && (projHolds || !dsFilters) && passDataset(p, &dsEntry{}, onlyP(c.filters)) {
			add(api.KindProject, api.ObjectPath{Project: p.name}, p.lname, p.name, false)
		}
	}
	if want(api.KindTable) || want(api.KindShards) {
		// a shard family passes when one of its members does, ranked by its
		// best member
		type famKey struct {
			ds     int
			prefix string
			typ    api.TableType
		}
		fams := map[famKey]int{} // index into out
		for i := range x.tables {
			if own[i][0] < 0 {
				continue
			}
			t := &x.tables[i]
			maxS, sum := own[i][0], own[i][1]
			p := x.tablePath(i)
			if want(api.KindTable) {
				out = append(out, scored{api.FilterMatch{Kind: api.KindTable, Path: p}, maxS, sum, t.name, p.String()})
			}
			if t.shard == nil || !want(api.KindShards) {
				continue
			}
			k := famKey{t.ds, t.shard.Prefix, t.typ}
			name := t.shard.Prefix + "YYYYMMDD"
			fp := api.ObjectPath{Project: p.Project, Dataset: p.Dataset, Table: name}
			if j, seen := fams[k]; !seen {
				fams[k] = len(out)
				out = append(out, scored{api.FilterMatch{Kind: api.KindShards, Path: fp}, maxS, sum, name, fp.String()})
			} else if f := &out[j]; maxS < f.max || maxS == f.max && sum < f.sum {
				f.max, f.sum = maxS, sum
			}
		}
	}

	if len(c.terms) == 0 {
		sort.SliceStable(out, func(i, j int) bool { return out[i].key < out[j].key })
	} else {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.max != b.max {
				return a.max < b.max
			}
			if a.sum != b.sum {
				return a.sum < b.sum
			}
			if len(a.name) != len(b.name) {
				return len(a.name) < len(b.name)
			}
			return a.key < b.key
		})
	}
	for _, h := range out {
		resp.Matches = append(resp.Matches, h.m)
	}
	resp.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
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
