package sync

import (
	"context"
	"fmt"
	"sort"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// ownSchemaMarks reads which tables of a unit were synced on their own (gs):
// sync keeps reading their columns. A failed read is logged and costs only
// those tables' own schemas until the next sync.
func (s *Syncer) ownSchemaMarks(ctx context.Context, u api.UnitRef) map[string]bool {
	marks, err := s.o.Catalog.OwnSchemaTables(ctx, u)
	if err != nil {
		s.o.Log.Warn("read own-schema marks", "project", u.Project, "location", u.Location, "err", err)
		return nil
	}
	return marks
}

// readColumns is phase 2 of a unit: it reads the columns of every table that
// should have them and attaches them. Those are the tables in no family, and
// the keep list: each family's newest shard, every table that matches a
// pattern but is in no family, and every member synced on its own. The other
// members are left out on BigQuery's side and marked as using their family's
// schema.
//
// The safety net: a table that should have columns but got none is read
// again by name, so a difference between BigQuery's regex dialect and Go's
// costs a query but never a schema.
func (s *Syncer) readColumns(ctx context.Context, project string, applies []store.DatasetApply, op string, r *run, fetch func(bq.ColumnFilter) ([]bq.TableColumns, error)) error {
	pats := s.o.Config.Shards()
	f := bq.ColumnFilter{Pattern: pats.Combined()}
	type at struct{ a, t int }
	want := map[string]at{}
	for ai := range applies {
		ts := applies[ai].Tables
		for ti := range ts {
			t := &ts[ti]
			key := t.Dataset + "." + t.Name
			t.SharedSchema = false
			switch {
			case t.Shard == nil:
				want[key] = at{ai, ti}
				if f.Pattern != "" && pats.MatchesAny(t.Name) {
					f.Keep = append(f.Keep, key)
				}
			case t.Shard.Newest == t.Name || t.OwnSchema:
				want[key] = at{ai, ti}
				f.Keep = append(f.Keep, key)
			default:
				t.SharedSchema = true
			}
		}
	}
	if f.Pattern == "" {
		f.Keep = nil // today's query: every table
	}
	sort.Strings(f.Keep)
	attach := func(cols []bq.TableColumns) int {
		n := 0
		for _, tc := range cols {
			w, ok := want[tc.Dataset+"."+tc.Table]
			if !ok {
				continue // a collapsed member BigQuery returned anyway, or a leaked table
			}
			t := &applies[w.a].Tables[w.t]
			if len(t.Columns) == 0 && len(tc.Columns) > 0 {
				n++
			}
			t.Columns = append(t.Columns, tc.Columns...)
		}
		return n
	}
	cols, err := retry(ctx, s, op, func() ([]bq.TableColumns, error) { return fetch(f) })
	if err != nil {
		return err
	}
	attach(cols)
	if f.Pattern == "" {
		return nil
	}
	var missing []string
	for key, w := range want {
		if len(applies[w.a].Tables[w.t].Columns) == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	again, err := retry(ctx, s, op+" by name", func() ([]bq.TableColumns, error) {
		return fetch(bq.ColumnFilter{Only: missing})
	})
	if err != nil {
		return err
	}
	// Tables without any columns stay missing and are not news; a table the
	// re-read found is one the filtered query should have returned.
	if n := attach(again); n > 0 {
		var found []string
		for _, key := range missing {
			w := want[key]
			if len(applies[w.a].Tables[w.t].Columns) > 0 {
				found = append(found, key)
			}
		}
		msg := fmt.Sprintf("%d table(s) got no columns from the filtered column query and were read again by name: BigQuery's regex dialect may read shard_patterns differently from bqt's (%s)", n, sample(found))
		s.o.Log.Warn("shard pattern dialect", "project", project, "tables", n, "op", op)
		r.warn(project, msg)
	}
	return nil
}

// syncTable syncs one table's row and schema with REST tables.get (gs, bqt
// sync --table). A family member read this way is marked as having its own
// schema, so later unit syncs keep reading its columns, the newest shard
// included (it keeps them once a newer shard comes). family is set when the
// user named the family: its newest shard is then only refreshed, and keeps
// any mark it had. A plain table is only refreshed.
func (s *Syncer) syncTable(ctx context.Context, p api.ObjectPath, family bool, r *run) error {
	stored, err := s.o.Catalog.StoredTable(ctx, p)
	if err != nil {
		return err
	}
	if s.o.Config.Matcher().Ignored(p.Project, p.Dataset, p.Table) {
		return fmt.Errorf("%s is excluded by the ignore patterns", p)
	}
	ref := api.UnitRef{Project: p.Project, Location: stored.Location}
	r.mu.Lock()
	r.total = 1
	r.mu.Unlock()
	r.startUnit(ref, 1, p.String())
	now := s.o.Now()
	st := api.UnitState{UnitRef: ref, Status: api.UnitOK, LastAttempt: &now, Datasets: 1, Tables: 1}
	t, err := retry(ctx, s, "get table "+p.String(), func() (api.TableMeta, error) {
		return s.o.Meta.GetTableMeta(ctx, p.Project, p.Dataset, p.Table)
	})
	if err == nil {
		t.Dataset, t.Name = p.Dataset, p.Table
		own := stored.Table.Shard != nil && (!family || stored.Table.OwnSchema)
		err = s.o.Catalog.ApplyTable(ctx, p.Project, t, own)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // a cancelled run reports Cancelled, not an error
		}
		st.Status, st.Error = api.UnitInaccessible, errText(err)
		r.finishUnit(st, api.EventUnitFailed)
		return nil
	}
	st.LastSuccess = &now
	r.finishUnit(st, api.EventUnitDone)
	return nil
}
