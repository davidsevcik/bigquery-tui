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
	own := make([]ownHit, len(x.tables))
	eachChunk(len(x.tables), func(_, lo, hi int) {
		for i := lo; i < hi; i++ {
			if i%2048 == 0 && ctx.Err() != nil {
				return
			}
			pass[i] = sc.table(x, i) && (pasted < 0 || i == pasted) && x.passTable(i, c, true)
			if pass[i] {
				own[i].max, own[i].sum, own[i].ok = ownScore(x.tables[i].lname, c.terms, true)
			}
		}
	})
	if err := ctx.Err(); err != nil {
		return resp, err
	}

	var out []hit
	var paths []api.ObjectPath // paths[h.i] for every hit
	add := func(k api.ResultKind, p api.ObjectPath, lname, name string) {
		if maxS, sum, ok := ownScore(lname, c.terms, true); ok {
			out = append(out, hit{kind: k, i: len(paths), max: maxS, sum: sum, name: name, path: p.String()})
			paths = append(paths, p)
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
			holds := !tableFilters || pasted < 0 && passDatasetOwn(d, c.filters)
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
				add(api.KindDataset, api.ObjectPath{Project: p.name, Dataset: d.name}, d.lname, d.name)
			}
		}
		if want(api.KindProject) && (projHolds || !dsFilters) && passDataset(p, &dsEntry{}, onlyP(c.filters)) {
			add(api.KindProject, api.ObjectPath{Project: p.name}, p.lname, p.name)
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
			if !own[i].ok {
				continue
			}
			t := &x.tables[i]
			maxS, sum := own[i].max, own[i].sum
			p := x.tablePath(i)
			if want(api.KindTable) {
				out = append(out, hit{kind: api.KindTable, i: len(paths), max: maxS, sum: sum, name: t.name, path: p.String()})
				paths = append(paths, p)
			}
			if t.shard == nil || !want(api.KindShards) {
				continue
			}
			k := famKey{t.ds, t.shard.Prefix, t.typ}
			name := t.shard.Prefix + "YYYYMMDD"
			fp := api.ObjectPath{Project: p.Project, Dataset: p.Dataset, Table: name}
			if j, seen := fams[k]; !seen {
				fams[k] = len(out)
				out = append(out, hit{kind: api.KindShards, i: len(paths), max: maxS, sum: sum, name: name, path: fp.String()})
				paths = append(paths, fp)
			} else if f := &out[j]; maxS < f.max || maxS == f.max && sum < f.sum {
				f.max, f.sum = maxS, sum
			}
		}
	}

	sortHits(out, len(c.terms) > 0)
	for _, h := range out {
		resp.Matches = append(resp.Matches, api.FilterMatch{Kind: h.kind, Path: paths[h.i]})
	}
	resp.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
}

// ownHit is a table's own-name score.
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
