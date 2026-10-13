package search

import (
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

var emptySnapshot store.IndexSnapshot

// Projects lists the catalog's projects with their sync health. Configured
// projects that have not been synced yet are added by the engine.
func (s *Searcher) Projects() []api.BrowseNode {
	x := s.Index()
	out := make([]api.BrowseNode, 0, len(x.projects))
	for pi := range x.projects {
		p := &x.projects[pi]
		n := api.BrowseNode{Kind: api.KindProject, Path: api.ObjectPath{Project: p.name}, Name: p.name, Children: len(p.datasets), UnitStatus: api.UnitOK}
		s.projectHealth(x, p.name, &n)
		out = append(out, n)
	}
	return out
}

// ProjectHealth fills the sync-health fields of a project node from the unit
// states (used for projects without any data too).
func (s *Searcher) ProjectHealth(project string, n *api.BrowseNode) {
	s.projectHealth(s.Index(), project, n)
}

func (s *Searcher) projectHealth(x *Index, project string, n *api.BrowseNode) {
	var oldest *time.Time
	known := false
	rank := map[api.UnitStatus]int{api.UnitOK: 0, api.UnitDegraded: 1, api.UnitInaccessible: 2}
	if n.UnitStatus == "" {
		n.UnitStatus = api.UnitOK
	}
	for ref, u := range x.units {
		if ref.Project != project {
			continue
		}
		if rank[u.Status] > rank[n.UnitStatus] {
			n.UnitStatus = u.Status
		}
		if u.Error != "" && (n.Error == "" || ref.Location == "") {
			n.Error = u.Error
		}
		if ref.Location == "" {
			continue
		}
		known = true
		if u.LastSuccess == nil {
			n.Stale = true
			continue
		}
		if oldest == nil || u.LastSuccess.Before(*oldest) {
			t := *u.LastSuccess
			oldest = &t
		}
	}
	if !known {
		n.Stale = true
		return
	}
	n.SyncedAt = oldest
	if oldest != nil && s.Now().Sub(*oldest) > s.ttl() {
		n.Stale = true
	}
}

// Datasets lists a project's datasets.
func (s *Searcher) Datasets(project string) []api.BrowseNode {
	x := s.Index()
	for pi := range x.projects {
		p := &x.projects[pi]
		if p.name != project {
			continue
		}
		out := make([]api.BrowseNode, 0, len(p.datasets))
		for _, di := range p.datasets {
			d := &x.datasets[di]
			n := api.BrowseNode{Kind: api.KindDataset, Path: api.ObjectPath{Project: p.name, Dataset: d.name}, Name: d.name,
				Description: d.desc, Location: d.location, Children: len(d.tables), Status: d.status}
			n.SyncedAt, n.Stale = s.freshness(x, p.name, d.location)
			if u, ok := x.units[api.UnitRef{Project: p.name, Location: d.location}]; ok {
				n.UnitStatus = u.Status
			}
			out = append(out, n)
		}
		return out
	}
	return nil
}

// Tables lists a dataset's tables with each shard family collapsed into one
// node, placed where its prefix sorts.
func (s *Searcher) Tables(project, dataset string) []api.BrowseNode {
	x := s.Index()
	d := x.findDataset(project, dataset)
	if d == nil {
		return nil
	}
	type famKey struct {
		prefix string
		typ    api.TableType
	}
	seen := map[famKey]bool{}
	out := make([]api.BrowseNode, 0, len(d.tables))
	for _, ti := range d.tables {
		t := &x.tables[ti]
		if t.shard != nil {
			k := famKey{t.shard.Prefix, t.typ}
			if seen[k] {
				continue
			}
			seen[k] = true
			fam := *t.shard
			name := fam.Prefix + "YYYYMMDD"
			out = append(out, api.BrowseNode{Kind: api.KindShards, Path: api.ObjectPath{Project: project, Dataset: dataset, Table: name},
				Name: name, Type: t.typ, Children: fam.Count, Shard: &fam, NamesOnly: t.namesOnly})
			continue
		}
		out = append(out, s.tableNode(x, ti))
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Searcher) tableNode(x *Index, ti int) api.BrowseNode {
	t := &x.tables[ti]
	n := api.BrowseNode{Kind: api.KindTable, Path: x.tablePath(ti), Name: t.name, Type: t.typ, Description: t.desc, NamesOnly: t.namesOnly}
	if t.shard != nil {
		fam := *t.shard
		n.Shard = &fam
	}
	return n
}

// ShardMembers lists the tables of one shard family, newest first.
func (s *Searcher) ShardMembers(project, dataset, prefix string) []api.BrowseNode {
	x := s.Index()
	d := x.findDataset(project, dataset)
	if d == nil {
		return nil
	}
	var out []api.BrowseNode
	for _, ti := range d.tables {
		if t := &x.tables[ti]; t.shard != nil && t.shard.Prefix == prefix {
			out = append(out, s.tableNode(x, ti))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func (x *Index) findDataset(project, dataset string) *dsEntry {
	for pi := range x.projects {
		if x.projects[pi].name != project {
			continue
		}
		for _, di := range x.projects[pi].datasets {
			if x.datasets[di].name == dataset {
				return &x.datasets[di]
			}
		}
	}
	return nil
}

// TableRef is what Lookup returns for a table path.
type TableRef struct {
	ID        int64
	Path      api.ObjectPath
	Location  string
	Type      api.TableType
	Meta      api.TableMeta // without columns and view SQL
	NamesOnly bool
}

// Lookup resolves a table path. For a shard-family path (prefix + YYYYMMDD)
// it returns the newest member.
func (s *Searcher) Lookup(p api.ObjectPath) (TableRef, bool) {
	x := s.Index()
	ti, ok := x.byPath[p.String()]
	if !ok && strings.HasSuffix(p.Table, "YYYYMMDD") {
		if m := s.ShardMembers(p.Project, p.Dataset, strings.TrimSuffix(p.Table, "YYYYMMDD")); len(m) > 0 {
			ti, ok = x.byPath[m[0].Path.String()]
		}
	}
	if !ok {
		return TableRef{}, false
	}
	t := &x.tables[ti]
	d := &x.datasets[t.ds]
	ref := TableRef{ID: t.id, Path: x.tablePath(ti), Location: d.location, Type: t.typ, NamesOnly: t.namesOnly || d.status == api.DatasetNamesOnly}
	ref.Meta = api.TableMeta{Dataset: d.name, Name: t.name, Type: t.typ, Description: t.desc, NamesOnly: t.namesOnly}
	if len(t.labels) > 0 {
		ref.Meta.Labels = map[string]string{}
		for _, l := range t.labels {
			if k, v, found := strings.Cut(l, ":"); found {
				ref.Meta.Labels[k] = v
			}
		}
	}
	if t.shard != nil {
		fam := *t.shard
		ref.Meta.Shard = &fam
	}
	return ref, true
}

// Unit returns the sync state of a (project, location) unit.
func (s *Searcher) Unit(project, location string) (api.UnitState, bool) {
	u, ok := s.Index().units[api.UnitRef{Project: project, Location: location}]
	return u, ok
}
