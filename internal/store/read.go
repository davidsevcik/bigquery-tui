package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

type IndexDataset struct {
	ID int64
	api.DatasetMeta
	Status api.DatasetStatus
}

type IndexTable struct {
	ID        int64
	DatasetID int64
	api.TableMeta
}

type IndexSnapshot struct {
	Projects []string
	Datasets []IndexDataset
	Tables   []IndexTable
	Units    []api.UnitState
}

func (c *Catalog) LoadIndex(ctx context.Context) (*IndexSnapshot, error) {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	snap := &IndexSnapshot{}

	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		snap.Projects = append(snap.Projects, n)
		return nil
	}, "SELECT name FROM projects ORDER BY name")
	if err != nil {
		return nil, err
	}

	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var d IndexDataset
		var labels, status string
		if err := rows.Scan(&d.ID, &d.Project, &d.Name, &d.Location, &d.Description, &d.FriendlyName, &labels, &status); err != nil {
			return err
		}
		d.Labels, d.Status = decodeLabels(labels), api.DatasetStatus(status)
		snap.Datasets = append(snap.Datasets, d)
		return nil
	}, `SELECT d.id, p.name, d.name, d.location, d.description, d.friendly_name, d.labels, d.status
FROM datasets d JOIN projects p ON p.id = d.project_id ORDER BY p.name, d.name_lc`)
	if err != nil {
		return nil, err
	}

	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var t IndexTable
		var typ, labels string
		var created, expires int64
		var names int
		var sr shardRow
		if err := rows.Scan(append([]any{&t.ID, &t.DatasetID, &t.Dataset, &t.Name, &typ, &created, &t.Description, &labels, &expires, &names}, sr.dest()...)...); err != nil {
			return err
		}
		t.Type, t.Labels, t.NamesOnly = api.TableType(typ), decodeLabels(labels), names != 0
		if created > 0 {
			t.CreatedAt = time.Unix(created, 0).UTC()
		}
		if expires > 0 {
			e := time.Unix(expires, 0).UTC()
			t.ExpiresAt = &e
		}
		sr.apply(&t.TableMeta)
		snap.Tables = append(snap.Tables, t)
		return nil
	}, `SELECT t.id, t.dataset_id, d.name, t.name, t.type, t.created_at, t.description, t.labels, t.expires_at, t.names_only,
 `+c.shardSelect()+`
FROM tables t JOIN datasets d ON d.id = t.dataset_id ORDER BY t.dataset_id, t.name_lc`)
	if err != nil {
		return nil, err
	}

	urows, err := tx.QueryContext(ctx, `SELECT project,location,last_attempt,last_success,status,error,datasets,tables,names_only,inaccessible FROM sync_units ORDER BY project, location`)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var s api.UnitState
		var la, ls int64
		var status string
		if err := urows.Scan(&s.Project, &s.Location, &la, &ls, &status, &s.Error, &s.Datasets, &s.Tables, &s.NamesOnly, &s.Inaccessible); err != nil {
			return nil, err
		}
		s.Status, s.LastAttempt, s.LastSuccess = api.UnitStatus(status), fromUnix(la), fromUnix(ls)
		snap.Units = append(snap.Units, s)
	}
	return snap, urows.Err()
}

func (c *Catalog) EachColumn(ctx context.Context, fn func(tableID int64, pathLC, descLC string)) error {
	rows, err := c.db.QueryContext(ctx, "SELECT table_id, field_path_lc, lower(description) FROM columns ORDER BY table_id, id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p, d string
		if err := rows.Scan(&id, &p, &d); err != nil {
			return err
		}
		fn(id, p, d)
	}
	return rows.Err()
}

func (c *Catalog) ColumnsByTableID(ctx context.Context, tableID int64) ([]api.ColumnMeta, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT field_path, ordinal, data_type, description, partitioning, clustering_ordinal, required
FROM columns WHERE table_id = ? ORDER BY id`, tableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.ColumnMeta
	for rows.Next() {
		var cm api.ColumnMeta
		var part, req int
		if err := rows.Scan(&cm.FieldPath, &cm.Ordinal, &cm.DataType, &cm.Description, &part, &cm.ClusteringOrdinal, &req); err != nil {
			return nil, err
		}
		cm.Partitioning, cm.Required = part != 0, req != 0
		out = append(out, cm)
	}
	return out, rows.Err()
}

func (c *Catalog) ViewSQL(ctx context.Context, tableID int64) (string, error) {
	var s string
	err := c.db.QueryRowContext(ctx, "SELECT view_sql FROM tables WHERE id = ?", tableID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

type Watcher struct {
	conn *sql.Conn
	last int64
}

func (c *Catalog) NewWatcher(ctx context.Context) (*Watcher, error) {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	w := &Watcher{conn: conn}
	if _, err := w.Changed(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return w, nil
}

func (w *Watcher) Changed(ctx context.Context) (bool, error) {
	var v int64
	if err := w.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
		return false, err
	}
	changed := v != w.last
	w.last = v
	return changed, nil
}

func (w *Watcher) Close() error { return w.conn.Close() }

func (c *Catalog) TableByID(ctx context.Context, id int64) (api.TableMeta, error) {
	var t api.TableMeta
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	var typ, labels string
	var created, expires int64
	var names, rpf int
	var sr shardRow
	//nolint:gosec // tcol and shardSelect are fixed column expressions, never input
	err = tx.QueryRowContext(ctx, `SELECT d.name, t.name, t.type, t.created_at, t.description, t.labels, t.expires_at, t.view_sql, t.names_only, `+c.tcol("require_partition_filter")+`,
 `+c.shardSelect()+`
FROM tables t JOIN datasets d ON d.id = t.dataset_id WHERE t.id = ?`, id).
		Scan(append([]any{&t.Dataset, &t.Name, &typ, &created, &t.Description, &labels, &expires, &t.ViewSQL, &names, &rpf}, sr.dest()...)...)
	if err != nil {
		return t, err
	}
	t.Type, t.Labels, t.NamesOnly, t.RequirePartitionFilter = api.TableType(typ), decodeLabels(labels), names != 0, rpf != 0
	if created > 0 {
		t.CreatedAt = time.Unix(created, 0).UTC()
	}
	if expires > 0 {
		e := time.Unix(expires, 0).UTC()
		t.ExpiresAt = &e
	}
	sr.apply(&t)
	rows, err := tx.QueryContext(ctx, `SELECT field_path, ordinal, data_type, description, partitioning, clustering_ordinal, required
FROM columns WHERE table_id = ? ORDER BY id`, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var cm api.ColumnMeta
		var part, req int
		if err := rows.Scan(&cm.FieldPath, &cm.Ordinal, &cm.DataType, &cm.Description, &part, &cm.ClusteringOrdinal, &req); err != nil {
			return t, err
		}
		cm.Partitioning, cm.Required = part != 0, req != 0
		t.Columns = append(t.Columns, cm)
	}
	return t, rows.Err()
}
