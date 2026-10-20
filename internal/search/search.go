package search

import (
	"context"
	"runtime"
	"sort"
	"strings"
	gosync "sync"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// ColumnSource provides column data for the col: filter and column hits.
type ColumnSource interface {
	EachColumn(ctx context.Context, fn func(tableID int64, pathLC, descLC string)) error
	ColumnsByTableID(ctx context.Context, tableID int64) ([]api.ColumnMeta, error)
}

// Searcher answers searches and browse requests against the current Index.
// It is safe for concurrent use; SetIndex swaps the catalog atomically.
type Searcher struct {
	// Columns backs the col: filter. Nil disables it.
	Columns ColumnSource
	// Recent returns recently opened object paths, newest first.
	Recent func(ctx context.Context, n int) ([]string, error)
	Now    func() time.Time
	// TTL is the freshness window used for the Stale flag.
	TTL time.Duration

	mu    gosync.RWMutex
	idx   *Index
	cols  *colBlob
	colMu gosync.Mutex
}

// New returns a Searcher over idx (which may be nil until the first load).
func New(idx *Index) *Searcher {
	if idx == nil {
		idx = BuildIndex(&emptySnapshot)
	}
	return &Searcher{idx: idx, Now: time.Now, TTL: 24 * time.Hour}
}

// SetIndex replaces the catalog and drops the derived column data.
func (s *Searcher) SetIndex(idx *Index) {
	s.mu.Lock()
	s.idx, s.cols = idx, nil
	s.mu.Unlock()
}

// Swap replaces the catalog together with its column source.
func (s *Searcher) Swap(idx *Index, cols ColumnSource) {
	s.mu.Lock()
	s.idx, s.cols, s.Columns = idx, nil, cols
	s.mu.Unlock()
}

// SetTTL changes the freshness window.
func (s *Searcher) SetTTL(ttl time.Duration) {
	s.mu.Lock()
	s.TTL = ttl
	s.mu.Unlock()
}

func (s *Searcher) source() ColumnSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Columns
}

func (s *Searcher) ttl() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TTL
}

// Index returns the current index.
func (s *Searcher) Index() *Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idx
}

// WarmColumns builds the column data ahead of the first col: query.
func (s *Searcher) WarmColumns(ctx context.Context) error {
	_, _, err := s.columns(ctx)
	return err
}

// columns returns the column blob for the current index, building it once.
func (s *Searcher) columns(ctx context.Context) (*Index, *colBlob, error) {
	s.mu.RLock()
	idx, cb, src := s.idx, s.cols, s.Columns
	s.mu.RUnlock()
	if cb != nil || src == nil {
		return idx, cb, nil
	}
	s.colMu.Lock()
	defer s.colMu.Unlock()
	s.mu.RLock()
	idx, cb = s.idx, s.cols
	s.mu.RUnlock()
	if cb != nil {
		return idx, cb, nil
	}
	cb, err := buildColBlob(ctx, idx, src)
	if err != nil {
		return idx, nil, err
	}
	s.mu.Lock()
	if s.idx == idx {
		s.cols = cb
	}
	s.mu.Unlock()
	return idx, cb, nil
}

type hit struct {
	kind     api.ResultKind
	i        int // table, dataset or project index (table: representative member)
	max, sum int
	name     string
	path     string
	members  int // shard family: matching members
}

type compiled struct {
	q       api.ParsedQuery
	terms   []string
	filters []api.QueryFilter // usable filters only
	colVals []string          // positive col: values
	colSets []colSet
}

type colSet struct {
	neg bool
	m   map[int]int
}

func usable(f api.QueryFilter) bool { return f.Status == api.FilterOK && len(f.Values) > 0 }

// Search runs a query. Results are ranked by tier when the query has free
// terms and ordered by path otherwise; spans and column hits are computed only
// for the returned page.
func (s *Searcher) Search(ctx context.Context, raw string, o api.SearchOptions) (api.SearchResponse, error) {
	start := time.Now()
	q := Parse(raw)
	resp := api.SearchResponse{Query: q, Results: []api.SearchResult{}}
	x := s.Index()

	c := &compiled{q: q}
	for _, t := range q.Terms {
		c.terms = append(c.terms, t.Text)
	}
	needCols := false
	for _, f := range q.Filters {
		if !usable(f) {
			continue
		}
		c.filters = append(c.filters, f)
		if f.Key == "col" {
			needCols = true
		}
	}
	if needCols {
		ix, cb, err := s.columns(ctx)
		if err != nil {
			return resp, err
		}
		x = ix
		for _, f := range c.filters {
			if f.Key != "col" {
				continue
			}
			set := colSet{neg: f.Negated, m: map[int]int{}}
			if cb != nil {
				set.m = cb.match(ctx, f.Values)
			}
			c.colSets = append(c.colSets, set)
			if !f.Negated {
				c.colVals = append(c.colVals, f.Values...)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}

	var hits []hit
	switch {
	case q.Path != nil:
		if i, ok := x.resolve(q.Path.String()); ok {
			hits = []hit{x.tableHit(i, tExact*10, 0)}
		} else {
			// not a known path: treat the text as an ordinary term
			c.q.Path = nil
			hits = s.rank(ctx, x, c, o)
		}
	case q.Empty() && !o.AllWhenEmpty:
		resp.Recent = true
		if s.Recent != nil {
			paths, err := s.Recent(ctx, 50)
			if err != nil {
				return resp, err
			}
			for _, p := range paths {
				if i, ok := x.resolve(p); ok && newScoper(o).table(x, i) {
					hits = append(hits, x.tableHit(i, 0, 0))
				}
			}
		}
	default:
		hits = s.rank(ctx, x, c, o)
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}

	resp.Total = len(hits)
	lo := min(max(o.Offset, 0), len(hits))
	hi := len(hits)
	if o.Limit > 0 {
		hi = min(lo+o.Limit, len(hits))
	}
	for _, h := range hits[lo:hi] {
		r, err := s.materialize(ctx, x, c, h, resp.Recent)
		if err != nil {
			return resp, err
		}
		resp.Results = append(resp.Results, r)
	}
	resp.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
}

func (x *Index) tableHit(i, maxScore, sum int) hit {
	return hit{kind: api.KindTable, i: i, max: maxScore, sum: sum, name: x.tables[i].name, path: x.tablePath(i).String()}
}

// scoper decides which tables and projects a search may return.
type scoper struct {
	scope  *api.ObjectPath
	hidden map[string]bool
}

func newScoper(o api.SearchOptions) scoper {
	sc := scoper{scope: o.Scope}
	for _, p := range o.Hidden {
		if sc.hidden == nil {
			sc.hidden = map[string]bool{}
		}
		sc.hidden[p] = true
	}
	return sc
}

func (sc scoper) project(name string) bool {
	if sc.hidden[name] {
		return false
	}
	return sc.scope == nil || sc.scope.Project == "" || sc.scope.Project == name
}

func (sc scoper) table(x *Index, ti int) bool {
	d := &x.datasets[x.tables[ti].ds]
	if !sc.project(x.projects[d.proj].name) {
		return false
	}
	return sc.scope == nil || sc.scope.Project == "" || sc.scope.Dataset == "" || d.name == sc.scope.Dataset
}

func (s *Searcher) rank(ctx context.Context, x *Index, c *compiled, o api.SearchOptions) []hit {
	sc := newScoper(o)
	terms := c.terms
	// Dataset- and project-level scores are the same for every table of a
	// dataset, so compute them once per (term, dataset).
	dsScore := make([][]int, len(terms))
	for ti, term := range terms {
		ps := make([]int, len(x.projects))
		for pi := range x.projects {
			ps[pi] = noMatch
			if tier, _ := matchName(x.projects[pi].lname, term, false); tier >= 0 {
				ps[pi] = tier*10 + penProject
			}
		}
		row := make([]int, len(x.datasets))
		for di := range x.datasets {
			d := &x.datasets[di]
			best := ps[d.proj]
			if tier, _ := matchName(d.lname, term, false); tier >= 0 && tier*10+penDataset < best {
				best = tier*10 + penDataset
			}
			if best >= noMatch && d.ldesc != "" && strings.Contains(d.ldesc, term) {
				best = descScore
			}
			row[di] = best
		}
		dsScore[ti] = row
	}

	// Score tables in parallel chunks; chunk order keeps the result deterministic.
	workers := min(runtime.GOMAXPROCS(0), 8)
	n := len(x.tables)
	if n < 4096 {
		workers = 1
	}
	parts := make([][]hit, workers)
	var wg gosync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := n*w/workers, n*(w+1)/workers
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var out []hit
			for i := lo; i < hi; i++ {
				if i%2048 == 0 && ctx.Err() != nil {
					return
				}
				if !sc.table(x, i) || !x.passTable(i, c) {
					continue
				}
				maxS, sum := 0, 0
				for ti, term := range terms {
					sc := x.scoreTerm(i, term, dsScore[ti])
					if sc >= noMatch {
						maxS = -1
						break
					}
					if sc > maxS {
						maxS = sc
					}
					sum += sc
				}
				if maxS < 0 {
					continue
				}
				out = append(out, x.tableHit(i, maxS, sum))
			}
			parts[w] = out
		}(w, lo, hi)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	var out []hit
	for _, p := range parts {
		out = append(out, p...)
	}
	if !o.ExpandShards {
		out = x.collapseShards(out)
	}

	if !o.TablesOnly && len(terms) > 0 && onlyScopeFilters(c.filters) {
		for pi := range x.projects {
			p := &x.projects[pi]
			if !sc.project(p.name) {
				continue
			}
			if len(c.filters) == 0 && (o.Scope == nil || o.Scope.Project == "") {
				if m, sm, ok := scoreNames(p.lname, "", terms); ok {
					out = append(out, hit{kind: api.KindProject, i: pi, max: m, sum: sm, name: p.name, path: p.name})
				}
			}
			for _, di := range p.datasets {
				d := &x.datasets[di]
				if o.Scope != nil && o.Scope.Dataset != "" && o.Scope.Dataset != d.name {
					continue
				}
				if !passDataset(p, d, c.filters) {
					continue
				}
				if m, sm, ok := scoreNames(d.lname, d.ldesc, terms); ok {
					out = append(out, hit{kind: api.KindDataset, i: di, max: m, sum: sm, name: d.name, path: p.name + "." + d.name})
				}
			}
		}
	}

	if len(terms) == 0 {
		sort.SliceStable(out, func(i, j int) bool { return out[i].path < out[j].path })
		return out
	}
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
		return a.path < b.path
	})
	return out
}

func onlyScopeFilters(fs []api.QueryFilter) bool {
	for _, f := range fs {
		if f.Key != "p" && f.Key != "d" {
			return false
		}
	}
	return true
}

// scoreNames scores a project or dataset: every term must match its name, or
// (datasets) its description.
func scoreNames(lname, ldesc string, terms []string) (maxS, sum int, ok bool) {
	for _, t := range terms {
		sc := noMatch
		if tier, _ := matchName(lname, t, false); tier >= 0 {
			sc = tier * 10
		} else if ldesc != "" && strings.Contains(ldesc, t) {
			sc = descScore
		}
		if sc >= noMatch {
			return 0, 0, false
		}
		maxS = max(maxS, sc)
		sum += sc
	}
	return maxS, sum, true
}

// scoreTerm returns the best (lowest) score of one term against a table.
// dsScore holds the precomputed dataset/project-level score per dataset.
// Columns are never consulted here.
func (x *Index) scoreTerm(i int, term string, dsScore []int) int {
	t := &x.tables[i]
	best := noMatch
	if tier, _ := matchName(t.lname, term, true); tier >= 0 {
		best = tier*10 + penName
		return best // a name hit beats every other field
	}
	for _, l := range t.labels {
		if tier, _ := matchName(l, term, false); tier >= 0 && tier*10+penLabel < best {
			best = tier*10 + penLabel
		}
	}
	if ds := dsScore[t.ds]; ds < best {
		best = ds
	}
	if best >= noMatch && t.ldesc != "" && strings.Contains(t.ldesc, term) {
		best = descScore
	}
	return best
}

func passDataset(p *projEntry, d *dsEntry, fs []api.QueryFilter) bool {
	for _, f := range fs {
		var m bool
		switch f.Key {
		case "p":
			m = anySub(p.lname, f.Values)
		case "d":
			m = anySub(d.lname, f.Values)
		default:
			continue
		}
		if m == f.Negated {
			return false
		}
	}
	return true
}

func (x *Index) passTable(i int, c *compiled) bool {
	t := &x.tables[i]
	d := &x.datasets[t.ds]
	p := &x.projects[d.proj]
	ci := 0
	for _, f := range c.filters {
		var m bool
		switch f.Key {
		case "p":
			m = anySub(p.lname, f.Values)
		case "d":
			m = anySub(d.lname, f.Values)
		case "type":
			for _, v := range f.Values {
				if strings.HasPrefix(string(t.typ), v) {
					m = true
				}
			}
		case "label":
			for _, l := range t.labels {
				if anySub(l, f.Values) {
					m = true
				}
			}
			for k, v := range d.labels {
				if anySub(strings.ToLower(k+":"+v), f.Values) {
					m = true
				}
			}
		case "desc":
			m = anySub(t.ldesc, f.Values) || anySub(d.ldesc, f.Values)
		case "col":
			_, m = c.colSets[ci].m[i]
			ci++
		}
		if m == f.Negated {
			return false
		}
	}
	return true
}

// collapseShards folds two or more equally scored members of one shard family
// into a single family hit.
func (x *Index) collapseShards(hits []hit) []hit {
	type key struct {
		ds       int
		prefix   string
		typ      api.TableType
		max, sum int
	}
	groups := map[key][]int{}
	for hi, h := range hits {
		t := &x.tables[h.i]
		if t.shard == nil {
			continue
		}
		k := key{t.ds, t.shard.Prefix, t.typ, h.max, h.sum}
		groups[k] = append(groups[k], hi)
	}
	drop := map[int]bool{}
	for k, members := range groups {
		if len(members) < 2 {
			continue
		}
		// representative: the newest member
		rep := members[0]
		for _, m := range members[1:] {
			if hits[m].name > hits[rep].name {
				rep = m
			}
		}
		for _, m := range members {
			if m != rep {
				drop[m] = true
			}
		}
		d := &x.datasets[k.ds]
		h := &hits[rep]
		h.kind = api.KindShards
		h.members = len(members)
		h.name = k.prefix + "YYYYMMDD"
		h.path = x.projects[d.proj].name + "." + d.name + "." + h.name
	}
	if len(drop) == 0 {
		return hits
	}
	out := hits[:0]
	for i, h := range hits {
		if !drop[i] {
			out = append(out, h)
		}
	}
	return out
}

func (s *Searcher) freshness(x *Index, project, location string) (*time.Time, bool) {
	u, ok := x.units[api.UnitRef{Project: project, Location: location}]
	if !ok || u.LastSuccess == nil {
		return nil, true
	}
	t := *u.LastSuccess
	return &t, s.Now().Sub(t) > s.ttl()
}

func (c *compiled) filterValues(key string) []string {
	var vals []string
	for _, f := range c.filters {
		if f.Key == key && !f.Negated {
			vals = append(vals, f.Values...)
		}
	}
	return vals
}

func termSpans(lname string, terms []string, fuzzy bool) []api.Span {
	var sp []api.Span
	for _, term := range terms {
		if tier, s := matchName(lname, term, fuzzy); tier >= 0 {
			sp = append(sp, s...)
		}
	}
	return mergeSpans(sp)
}

func setSpans(m map[string][]api.Span, key, original, lower string, sp []api.Span) {
	// Offsets are computed on the lower-case text; they are only valid for the
	// original when lower-casing kept the byte length (true for ASCII).
	if len(sp) > 0 && len(original) == len(lower) {
		m[key] = sp
	}
}

func (s *Searcher) materialize(ctx context.Context, x *Index, c *compiled, h hit, recent bool) (api.SearchResult, error) {
	r := api.SearchResult{Kind: h.kind, Name: h.name, Spans: map[string][]api.Span{}}
	terms := c.terms
	switch h.kind {
	case api.KindProject:
		p := &x.projects[h.i]
		r.Path = api.ObjectPath{Project: p.name}
		setSpans(r.Spans, "name", p.name, p.lname, termSpans(p.lname, terms, false))
		r.MatchedField, r.Tier = "name", tierName(h.max)
		return r, nil
	case api.KindDataset:
		d := &x.datasets[h.i]
		p := &x.projects[d.proj]
		r.Path = api.ObjectPath{Project: p.name, Dataset: d.name}
		r.Description = d.desc
		setSpans(r.Spans, "name", d.name, d.lname, mergeSpans(append(termSpans(d.lname, terms, false), subSpans(d.lname, c.filterValues("d"))...)))
		setSpans(r.Spans, "project", p.name, p.lname, subSpans(p.lname, c.filterValues("p")))
		setSpans(r.Spans, "description", d.desc, d.ldesc, subSpans(d.ldesc, terms))
		r.MatchedField, r.Tier = "name", tierName(h.max)
		if h.max >= descScore {
			r.MatchedField = "description"
		}
		r.NamesOnly = d.status == api.DatasetNamesOnly
		r.SyncedAt, r.Stale = s.freshness(x, p.name, d.location)
		return r, nil
	}

	t := &x.tables[h.i]
	d := &x.datasets[t.ds]
	p := &x.projects[d.proj]
	r.Path = api.ObjectPath{Project: p.name, Dataset: d.name, Table: t.name}
	r.Type, r.Description, r.NamesOnly = t.typ, t.desc, t.namesOnly || d.status == api.DatasetNamesOnly
	r.SyncedAt, r.Stale = s.freshness(x, p.name, d.location)
	if h.kind == api.KindShards {
		fam := *t.shard
		r.Shard = &fam
		r.Path.Table = h.name
	} else if t.shard != nil {
		fam := *t.shard
		r.Shard = &fam
	}
	if recent {
		r.Tier = "recent"
		return r, nil
	}
	if c.q.Path != nil {
		r.Spans["name"] = []api.Span{{Start: 0, End: len(t.name)}}
		r.MatchedField, r.Tier = "name", tierNames[tExact]
		return r, nil
	}

	nameSp := termSpans(t.lname, terms, true)
	if h.kind == api.KindShards {
		// keep only the spans inside the shared prefix
		var clipped []api.Span
		for _, sp := range nameSp {
			if sp.Start < len(t.shard.Prefix) {
				sp.End = min(sp.End, len(t.shard.Prefix))
				clipped = append(clipped, sp)
			}
		}
		nameSp = clipped
	}
	setSpans(r.Spans, "name", t.name, t.lname, nameSp)
	dsSp := mergeSpans(append(termSpans(d.lname, terms, false), subSpans(d.lname, c.filterValues("d"))...))
	setSpans(r.Spans, "dataset", d.name, d.lname, dsSp)
	prSp := mergeSpans(append(termSpans(p.lname, terms, false), subSpans(p.lname, c.filterValues("p"))...))
	setSpans(r.Spans, "project", p.name, p.lname, prSp)
	descVals := append(c.filterValues("desc"), terms...)
	descSp := subSpans(t.ldesc, descVals)
	setSpans(r.Spans, "description", t.desc, t.ldesc, descSp)
	if len(terms) > 0 {
		r.Tier = tierName(h.max)
	}

	if src := s.source(); len(c.colVals) > 0 && src != nil {
		cols, err := src.ColumnsByTableID(ctx, t.id)
		if err != nil {
			return r, err
		}
		for _, cm := range cols {
			lp, ld := strings.ToLower(cm.FieldPath), strings.ToLower(cm.Description)
			psp, dsp := subSpans(lp, c.colVals), subSpans(ld, c.colVals)
			if len(psp) == 0 && len(dsp) == 0 {
				continue
			}
			if len(r.ColumnHits) >= 3 {
				r.MoreColumns++
				continue
			}
			ch := api.ColumnHit{FieldPath: cm.FieldPath, DataType: cm.DataType, Description: cm.Description}
			if len(lp) == len(cm.FieldPath) {
				ch.Spans = psp
			}
			if len(ld) == len(cm.Description) {
				ch.DescSpans = dsp
			}
			r.ColumnHits = append(r.ColumnHits, ch)
		}
	}

	switch {
	case len(terms) == 0 && len(r.ColumnHits) > 0:
		r.MatchedField = "column"
	case len(terms) == 0:
		r.MatchedField = "filter"
	case h.max >= descScore:
		r.MatchedField = "description"
	case len(nameSp) > 0:
		r.MatchedField = "name"
	case len(dsSp) > 0:
		r.MatchedField = "dataset"
	case len(prSp) > 0:
		r.MatchedField = "project"
	default:
		r.MatchedField = "label"
	}
	if len(r.Spans) == 0 {
		r.Spans = nil
	}
	return r, nil
}
