package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func (c *Catalog) tcol(name string) string {
	if c.missing[name] {
		for _, a := range addedColumns {
			if a.column == name {
				return a.def
			}
		}
	}
	return "t." + name
}

func (c *Catalog) shardSelect() string {
	return "t.shard_prefix, t.shard_first, t.shard_last, t.shard_count, " + strings.Join([]string{
		c.tcol("shard_name"), c.tcol("shard_suffix"), c.tcol("shard_date"), c.tcol("shard_newest"),
		c.tcol("shard_newest_created"), c.tcol("shared_schema"), c.tcol("own_schema"),
	}, ", ")
}

type shardRow struct {
	prefix, first, last, name, suffix, newest string
	count, date, shared, own                  int
	newestCreated                             int64
}

func (s *shardRow) dest() []any {
	return []any{&s.prefix, &s.first, &s.last, &s.count, &s.name, &s.suffix, &s.date, &s.newest, &s.newestCreated, &s.shared, &s.own}
}

func (s shardRow) apply(t *api.TableMeta) {
	t.SharedSchema, t.OwnSchema = s.shared != 0, s.own != 0
	if s.count <= 0 {
		return
	}
	f := &api.ShardFamily{Name: s.name, Prefix: s.prefix, Suffix: s.suffix, Date: s.date != 0, First: s.first, Last: s.last, Count: s.count, Newest: s.newest}
	if s.newestCreated > 0 {
		f.NewestCreated = time.Unix(s.newestCreated, 0).UTC()
	}
	if f.Name == "" {
		f.Name, f.Date, f.Newest = s.prefix+"YYYYMMDD", true, s.prefix+s.last
	}
	t.Shard = f
}

func shardValues(t api.TableMeta) []any {
	s := t.Shard
	if s == nil {
		return []any{"", "", "", 0, "", "", 0, "", 0, b2i(t.SharedSchema), b2i(t.OwnSchema)}
	}
	return []any{s.Prefix, s.First, s.Last, s.Count, s.Name, s.Suffix, b2i(s.Date), s.Newest, unix(s.NewestCreated), b2i(t.SharedSchema), b2i(t.OwnSchema)}
}

const shardAssign = "shard_prefix=?,shard_first=?,shard_last=?,shard_count=?,shard_name=?,shard_suffix=?,shard_date=?,shard_newest=?,shard_newest_created=?,shared_schema=?,own_schema=?"

func (c *Catalog) StaleFamilies(ctx context.Context) (bool, error) {
	if c.missing["shard_name"] || c.missing["shard_newest"] {
		return false, nil
	}
	var one int
	err := c.db.QueryRowContext(ctx, `WITH modern AS (SELECT DISTINCT dataset_id FROM tables WHERE shard_name <> '')
SELECT 1 FROM tables t
WHERE t.shard_count > 0 AND (
  (t.shard_name <> '' AND NOT EXISTS (SELECT 1 FROM tables n WHERE n.dataset_id = t.dataset_id AND n.name = t.shard_newest))
  OR (t.shard_name = '' AND t.dataset_id IN (SELECT dataset_id FROM modern)))
LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (c *Catalog) OwnSchemaTables(ctx context.Context, u api.UnitRef) (map[string]bool, error) {
	out := map[string]bool{}
	if c.missing["own_schema"] {
		return out, nil
	}
	err := eachRow(ctx, c.db, func(rows *sql.Rows) error {
		var d, t string
		if err := rows.Scan(&d, &t); err != nil {
			return err
		}
		out[d+"."+t] = true
		return nil
	}, `SELECT d.name, t.name FROM tables t JOIN datasets d ON d.id = t.dataset_id JOIN projects p ON p.id = d.project_id
WHERE p.name = ? AND d.location = ? AND t.own_schema = 1`, u.Project, u.Location)
	return out, err
}

func (c *Catalog) ClearOwnSchema(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, "UPDATE tables SET own_schema = 0 WHERE own_schema <> 0")
	return err
}

func (c *Catalog) HasLegacyDateFamilies(ctx context.Context) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM tables t WHERE t.shard_count > 0 AND "+c.tcol("shard_name")+" = '' LIMIT 1)").Scan(&n)
	return n > 0, err
}

type TableLocation struct {
	Location string
	Table    api.TableMeta
}

var ErrTableNotStored = errors.New("table is not in the catalog")

func (c *Catalog) StoredTable(ctx context.Context, p api.ObjectPath) (TableLocation, error) {
	var out TableLocation
	var typ string
	var sr shardRow
	dest := append([]any{&out.Location, &out.Table.Name, &typ}, sr.dest()...)
	//nolint:gosec // shardSelect is fixed column expressions, never input
	err := c.db.QueryRowContext(ctx, `SELECT d.location, t.name, t.type, `+c.shardSelect()+`
FROM tables t JOIN datasets d ON d.id = t.dataset_id JOIN projects p ON p.id = d.project_id
WHERE p.name = ? AND d.name = ? AND t.name = ?`, p.Project, p.Dataset, p.Table).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("%w: %s", ErrTableNotStored, p)
	}
	if err != nil {
		return out, err
	}
	out.Table.Dataset, out.Table.Type = p.Dataset, api.TableType(typ)
	sr.apply(&out.Table)
	return out, nil
}

func (c *Catalog) ApplyTable(ctx context.Context, project string, t api.TableMeta, own bool) error {
	tx, err := beginTx(ctx, c.db, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT t.id FROM tables t JOIN datasets d ON d.id = t.dataset_id JOIN projects p ON p.id = d.project_id
WHERE p.name = ? AND d.name = ? AND t.name = ?`, project, t.Dataset, t.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s.%s.%s", ErrTableNotStored, project, t.Dataset, t.Name)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tables SET type=?,created_at=?,description=?,labels=?,expires_at=?,view_sql=?,names_only=0,content_hash=?,require_partition_filter=?,shared_schema=0,own_schema=? WHERE id=?`,
		string(t.Type), unix(t.CreatedAt), t.Description, encodeLabels(t.Labels), unixPtr(t.ExpiresAt), t.ViewSQL, t.ContentHash(), b2i(t.RequirePartitionFilter), b2i(own), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM columns WHERE table_id = ?", id); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, insertColumnSQL)
	if err != nil {
		return err
	}
	defer ins.Close()
	if err := insertColumns(ctx, ins, id, t.Columns); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
