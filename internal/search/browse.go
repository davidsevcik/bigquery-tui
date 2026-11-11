package search

import (
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/shard"
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
	var oldest, discovery *time.Time
	known := false
	rank := map[api.UnitStatus]int{api.UnitOK: 0, api.UnitDegraded: 1, api.UnitInaccessible: 2}
	if n.UnitStatus == "" {
		n.UnitStatus = api.UnitOK
	}
	// A location that holds pending datasets but no unit state is a unit
	// that never succeeded, and while any dataset is pending the discovery
	// unit's success does not count (spec 7.2).
	hasPending := false
	for ref := range x.pending {
		if ref.Project != project {
			continue
		}
		hasPending = true
		if _, ok := x.units[ref]; !ok {
			known, n.Stale = true, true
		}
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
			if !hasPending {
				discovery = u.LastSuccess // carries a sync only for an empty project
			}
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
	if !known && discovery == nil {
		n.Stale = true
		return
	}
	if !known {
		t := *discovery
		oldest = &t
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
			n := api.BrowseNode{
				Kind: api.KindDataset, Path: api.ObjectPath{Project: p.name, Dataset: d.name}, Name: d.name,
				Description: d.desc, Location: d.location, Children: len(d.tables), Status: d.status,
				FriendlyName: d.friendly, Labels: d.labels,
			}
			n.SyncedAt, n.Stale = s.datasetFreshness(x, p.name, d)
			if u, ok := x.units[api.UnitRef{Project: p.name, Location: d.location}]; ok {
				n.UnitStatus, n.Error = u.Status, u.Error
			}
			out = append(out, n)
		}
		return out
	}
	return nil
}

// Tables lists a dataset's tables with each shard family collapsed into one
// node named after the family, placed where its name sorts.
func (s *Searcher) Tables(project, dataset string) []api.BrowseNode {
	x := s.Index()
	d := x.findDataset(project, dataset)
	if d == nil {
		return nil
	}
	type famKey struct {
		name string
		typ  api.TableType
	}
	seen := map[famKey]bool{}
	out := make([]api.BrowseNode, 0, len(d.tables))
	for _, ti := range d.tables {
		t := &x.tables[ti]
		if t.shard != nil {
			k := famKey{t.shard.Name, t.typ}
			if seen[k] {
				continue
			}
			seen[k] = true
			fam := *t.shard
			out = append(out, api.BrowseNode{
				Kind: api.KindShards, Path: api.ObjectPath{Project: project, Dataset: dataset, Table: fam.Name},
				Name: fam.Name, Type: t.typ, Children: fam.Count, Shard: &fam, NamesOnly: t.namesOnly,
			})
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

// ShardMembers lists the tables of one shard family, newest first: by date
// for a date family, by creation time for any other. family is the family's
// name (orders_*, events_YYYYMMDD) and typ its table type: families of two
// types can share a name. An empty typ matches any type.
func (s *Searcher) ShardMembers(project, dataset, family string, typ api.TableType) []api.BrowseNode {
	x := s.Index()
	d := x.findDataset(project, dataset)
	if d == nil {
		return nil
	}
	var idx []int
	var fam api.ShardFamily
	for _, ti := range d.tables {
		if t := &x.tables[ti]; t.shard != nil && t.shard.Name == family && (typ == "" || t.typ == typ) {
			idx = append(idx, ti)
			fam = *t.shard
		}
	}
	shard.SortNewestFirst(fam, idx, func(i int) string { return x.tables[i].name }, func(i int) time.Time { return x.tables[i].created })
	out := make([]api.BrowseNode, len(idx))
	for i, ti := range idx {
		out[i] = s.tableNode(x, ti)
	}
	return out
}

// Dataset returns a dataset (its project, name and location).
func (s *Searcher) Dataset(project, dataset string) (api.DatasetMeta, bool) {
	d := s.Index().findDataset(project, dataset)
	if d == nil {
		return api.DatasetMeta{}, false
	}
	return api.DatasetMeta{Project: project, Name: d.name, Location: d.location}, true
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
	// Family is set when the path named a shard family: Path is then its
	// newest shard.
	Family *api.ShardFamily
}

// Lookup resolves a table path. A shard family's path (its name, or its
// wildcard prefix*) resolves to its newest shard.
func (s *Searcher) Lookup(p api.ObjectPath) (TableRef, bool) {
	x := s.Index()
	ti, ok := x.byPath[p.String()]
	family := false
	if !ok {
		ti, ok = x.families[p.String()]
		family = ok
	}
	if !ok {
		return TableRef{}, false
	}
	t := &x.tables[ti]
	d := &x.datasets[t.ds]
	ref := TableRef{ID: t.id, Path: x.tablePath(ti), Location: d.location, Type: t.typ, NamesOnly: t.namesOnly || d.status == api.DatasetNamesOnly}
	ref.Meta = api.TableMeta{Dataset: d.name, Name: t.name, Type: t.typ, Description: t.desc, NamesOnly: t.namesOnly, CreatedAt: t.created, OwnSchema: t.ownSchema}
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
		if family {
			ref.Family = &fam
		}
	}
	return ref, true
}

// Unit returns the sync state of a (project, location) unit.
func (s *Searcher) Unit(project, location string) (api.UnitState, bool) {
	u, ok := s.Index().units[api.UnitRef{Project: project, Location: location}]
	return u, ok
}
