package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// CatalogSchemaVersion is the catalog.db schema version. The catalog has no
// migrations: a mismatch deletes and recreates the file (ticket 06).
const CatalogSchemaVersion = 1

const catalogSchema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE projects (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE datasets (
  id            INTEGER PRIMARY KEY,
  project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  name_lc       TEXT NOT NULL,
  location      TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  friendly_name TEXT NOT NULL DEFAULT '',
  labels        TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'ok',
  UNIQUE (project_id, name)
);
CREATE INDEX datasets_location ON datasets(project_id, location);
CREATE INDEX datasets_name_lc ON datasets(name_lc);
CREATE TABLE tables (
  id           INTEGER PRIMARY KEY,
  dataset_id   INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  name_lc      TEXT NOT NULL,
  type         TEXT NOT NULL,
  created_at   INTEGER NOT NULL DEFAULT 0,
  description  TEXT NOT NULL DEFAULT '',
  labels       TEXT NOT NULL DEFAULT '',
  expires_at   INTEGER NOT NULL DEFAULT 0,
  view_sql     TEXT NOT NULL DEFAULT '',
  names_only   INTEGER NOT NULL DEFAULT 0,
  content_hash TEXT NOT NULL,
  shard_prefix TEXT NOT NULL DEFAULT '',
  shard_first  TEXT NOT NULL DEFAULT '',
  shard_last   TEXT NOT NULL DEFAULT '',
  shard_count  INTEGER NOT NULL DEFAULT 0,
  UNIQUE (dataset_id, name)
);
CREATE INDEX tables_name_lc ON tables(name_lc);
CREATE TABLE columns (
  id                 INTEGER PRIMARY KEY,
  table_id           INTEGER NOT NULL REFERENCES tables(id) ON DELETE CASCADE,
  parent_id          INTEGER REFERENCES columns(id) ON DELETE CASCADE,
  field_path         TEXT NOT NULL,
  field_path_lc      TEXT NOT NULL,
  top_level          INTEGER NOT NULL,
  ordinal            INTEGER NOT NULL,
  data_type          TEXT NOT NULL DEFAULT '',
  description        TEXT NOT NULL DEFAULT '',
  partitioning       INTEGER NOT NULL DEFAULT 0,
  clustering_ordinal INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX columns_table ON columns(table_id, parent_id, ordinal);
CREATE TABLE sync_units (
  project       TEXT NOT NULL,
  location      TEXT NOT NULL,
  last_attempt  INTEGER NOT NULL DEFAULT 0,
  last_success  INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL,
  error         TEXT NOT NULL DEFAULT '',
  datasets      INTEGER NOT NULL DEFAULT 0,
  tables        INTEGER NOT NULL DEFAULT 0,
  names_only    INTEGER NOT NULL DEFAULT 0,
  inaccessible  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project, location)
);
`

// ErrNoCatalog is returned when a read-only open finds no catalog.
var ErrNoCatalog = errors.New("no catalog yet (run `bqt sync`)")

// Catalog is the metadata index.
type Catalog struct {
	db        *sql.DB
	path      string
	readOnly  bool
	recreated string
}

// OpenCatalog opens (creating if needed) the catalog for writing. A schema
// version mismatch or a corrupt file recreates it; Recreated reports why.
func OpenCatalog(path string) (*Catalog, error) {
	reason := ""
	if fileExists(path) {
		db, err := openDB(path, false)
		if err == nil {
			v, verr := userVersion(db)
			switch {
			case verr != nil && isCorrupt(verr):
				reason = "catalog was corrupt and has been recreated"
			case verr != nil:
				db.Close()
				return nil, verr
			case v == CatalogSchemaVersion:
				return &Catalog{db: db, path: path}, nil
			case v == 0:
				// empty file: fall through to create
			default:
				reason = fmt.Sprintf("catalog schema changed (v%d to v%d) and has been recreated", v, CatalogSchemaVersion)
			}
			db.Close()
		} else if isCorrupt(err) {
			reason = "catalog was corrupt and has been recreated"
		} else {
			return nil, err
		}
		if err := removeDBFiles(path); err != nil {
			return nil, err
		}
	}
	db, err := openDB(path, false)
	if err != nil {
		return nil, err
	}
	c := &Catalog{db: db, path: path, recreated: reason}
	if err := c.create(); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

// OpenCatalogReadOnly opens an existing catalog without ever modifying it.
func OpenCatalogReadOnly(path string) (*Catalog, error) {
	if !fileExists(path) {
		return nil, ErrNoCatalog
	}
	db, err := openDB(path, true)
	if err != nil {
		return nil, err
	}
	v, err := userVersion(db)
	if err != nil || v != CatalogSchemaVersion {
		db.Close()
		if err == nil || isCorrupt(err) {
			return nil, ErrNoCatalog
		}
		return nil, err
	}
	return &Catalog{db: db, path: path, readOnly: true}, nil
}

func (c *Catalog) create() error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(catalogSchema); err != nil {
		return fmt.Errorf("create catalog schema: %w", err)
	}
	if err := setUserVersion(tx, CatalogSchemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// Recreated is non-empty when the file was recreated on open.
func (c *Catalog) Recreated() string { return c.recreated }

// Path returns the file path.
func (c *Catalog) Path() string { return c.path }

// DB exposes the handle for search and completion (later phases).
func (c *Catalog) DB() *sql.DB { return c.db }

// Close closes the database.
func (c *Catalog) Close() error { return c.db.Close() }

// Meta reads a meta value.
func (c *Catalog) Meta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := c.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetMeta writes a meta value.
func (c *Catalog) SetMeta(ctx context.Context, key, value string) error {
	_, err := c.db.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// DatasetMode says how ApplyUnit treats a dataset's tables.
type DatasetMode int

const (
	// ModeReplace stores the supplied tables and removes the rest.
	ModeReplace DatasetMode = iota
	// ModeNamesOnly stores name-only tables (no columns) and removes the rest.
	ModeNamesOnly
	// ModeKeepOld leaves the dataset's existing tables untouched and only
	// records that it could not be refreshed.
	ModeKeepOld
)

// DatasetApply is one dataset's part of an ApplyUnit call.
type DatasetApply struct {
	Meta   api.DatasetMeta
	Mode   DatasetMode
	Status api.DatasetStatus
	Tables []api.TableMeta
}

// ApplyRequest replaces one (project, location) unit atomically.
type ApplyRequest struct {
	Unit     api.UnitRef
	Datasets []DatasetApply
	State    api.UnitState // Status, Error and the time fields are taken from here
}

// ApplyStats reports what changed.
type ApplyStats struct {
	DatasetsAdded, DatasetsRemoved                             int
	TablesAdded, TablesUpdated, TablesUnchanged, TablesRemoved int
}

// ApplyUnit swaps one unit's rows in a single transaction: new and changed
// rows are written, rows missing from the snapshot are deleted. Readers see
// the old or the new snapshot, never a half-written one.
func (c *Catalog) ApplyUnit(ctx context.Context, req ApplyRequest) (ApplyStats, error) {
	var st ApplyStats
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	projectID, err := ensureProject(ctx, tx, req.Unit.Project)
	if err != nil {
		return st, err
	}

	existing := map[string]int64{}
	rows, err := tx.QueryContext(ctx, "SELECT name, id FROM datasets WHERE project_id = ? AND location = ?", projectID, req.Unit.Location)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n string
		var id int64
		if err := rows.Scan(&n, &id); err != nil {
			rows.Close()
			return st, err
		}
		existing[n] = id
	}
	rows.Close()

	seen := map[string]bool{}
	var nameOnly, inaccessible int
	var tableCount int
	for _, d := range req.Datasets {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		seen[d.Meta.Name] = true
		status := d.Status
		if status == "" {
			status = api.DatasetOK
		}
		switch status {
		case api.DatasetNamesOnly:
			nameOnly++
		case api.DatasetInaccessible:
			inaccessible++
		}
		dsID, isNew, err := upsertDataset(ctx, tx, projectID, d.Meta, status, existing)
		if err != nil {
			return st, err
		}
		if isNew {
			st.DatasetsAdded++
		}
		if d.Mode != ModeKeepOld {
			ts, err := applyTables(ctx, tx, dsID, d.Tables, d.Mode == ModeNamesOnly)
			if err != nil {
				return st, fmt.Errorf("dataset %s: %w", d.Meta.Name, err)
			}
			st.TablesAdded += ts.TablesAdded
			st.TablesUpdated += ts.TablesUpdated
			st.TablesUnchanged += ts.TablesUnchanged
			st.TablesRemoved += ts.TablesRemoved
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tables WHERE dataset_id = ?", dsID).Scan(&n); err != nil {
			return st, err
		}
		tableCount += n
	}
	for name, id := range existing {
		if seen[name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM datasets WHERE id = ?", id); err != nil {
			return st, err
		}
		st.DatasetsRemoved++
	}

	state := req.State
	state.UnitRef = req.Unit
	state.Datasets = len(req.Datasets)
	state.Tables = tableCount
	state.NamesOnly = nameOnly
	state.Inaccessible = inaccessible
	if err := upsertUnit(ctx, tx, state); err != nil {
		return st, err
	}
	return st, tx.Commit()
}

func ensureProject(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO projects(name) VALUES(?) ON CONFLICT(name) DO NOTHING", name); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE name = ?", name).Scan(&id)
	return id, err
}

func upsertDataset(ctx context.Context, tx *sql.Tx, projectID int64, m api.DatasetMeta, status api.DatasetStatus, existing map[string]int64) (id int64, isNew bool, err error) {
	labels := encodeLabels(m.Labels)
	if id, ok := existing[m.Name]; ok {
		_, err = tx.ExecContext(ctx, `UPDATE datasets SET description=?, friendly_name=?, labels=?, status=? WHERE id=?`,
			m.Description, m.FriendlyName, labels, string(status), id)
		return id, false, err
	}
	// The dataset may exist under another location only if it was recreated
	// elsewhere; handle by replacing it.
	if _, err = tx.ExecContext(ctx, "DELETE FROM datasets WHERE project_id=? AND name=?", projectID, m.Name); err != nil {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO datasets(project_id,name,name_lc,location,description,friendly_name,labels,status) VALUES(?,?,?,?,?,?,?,?)`,
		projectID, m.Name, strings.ToLower(m.Name), m.Location, m.Description, m.FriendlyName, labels, string(status))
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}

type tableRow struct {
	id        int64
	hash      string
	namesOnly bool
}

func applyTables(ctx context.Context, tx *sql.Tx, datasetID int64, tables []api.TableMeta, namesOnly bool) (ApplyStats, error) {
	var st ApplyStats
	existing := map[string]tableRow{}
	rows, err := tx.QueryContext(ctx, "SELECT name, id, content_hash FROM tables WHERE dataset_id = ?", datasetID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n string
		var r tableRow
		if err := rows.Scan(&n, &r.id, &r.hash); err != nil {
			rows.Close()
			return st, err
		}
		existing[n] = r
	}
	rows.Close()

	insTable, err := tx.PrepareContext(ctx, `INSERT INTO tables(dataset_id,name,name_lc,type,created_at,description,labels,expires_at,view_sql,names_only,content_hash,shard_prefix,shard_first,shard_last,shard_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return st, err
	}
	defer insTable.Close()
	updTable, err := tx.PrepareContext(ctx, `UPDATE tables SET type=?,created_at=?,description=?,labels=?,expires_at=?,view_sql=?,names_only=?,content_hash=?,shard_prefix=?,shard_first=?,shard_last=?,shard_count=? WHERE id=?`)
	if err != nil {
		return st, err
	}
	defer updTable.Close()
	updShard, err := tx.PrepareContext(ctx, `UPDATE tables SET shard_prefix=?,shard_first=?,shard_last=?,shard_count=? WHERE id=?`)
	if err != nil {
		return st, err
	}
	defer updShard.Close()
	insCol, err := tx.PrepareContext(ctx, `INSERT INTO columns(table_id,parent_id,field_path,field_path_lc,top_level,ordinal,data_type,description,partitioning,clustering_ordinal) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return st, err
	}
	defer insCol.Close()

	seen := map[string]bool{}
	for i, t := range tables {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return st, err
			}
		}
		if seen[t.Name] {
			continue // defensive: duplicate names in a snapshot
		}
		seen[t.Name] = true
		if namesOnly {
			t.NamesOnly = true
			t.Columns = nil
		}
		hash := t.ContentHash()
		sp, sf, sl, sc := shardCols(t.Shard)
		old, found := existing[t.Name]
		switch {
		case found && old.hash == hash:
			if _, err := updShard.ExecContext(ctx, sp, sf, sl, sc, old.id); err != nil {
				return st, err
			}
			st.TablesUnchanged++
		case found:
			if _, err := updTable.ExecContext(ctx, string(t.Type), unix(t.CreatedAt), t.Description, encodeLabels(t.Labels), unixPtr(t.ExpiresAt), t.ViewSQL, b2i(t.NamesOnly), hash, sp, sf, sl, sc, old.id); err != nil {
				return st, err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM columns WHERE table_id = ?", old.id); err != nil {
				return st, err
			}
			if err := insertColumns(ctx, insCol, old.id, t.Columns); err != nil {
				return st, err
			}
			st.TablesUpdated++
		default:
			res, err := insTable.ExecContext(ctx, datasetID, t.Name, strings.ToLower(t.Name), string(t.Type), unix(t.CreatedAt), t.Description, encodeLabels(t.Labels), unixPtr(t.ExpiresAt), t.ViewSQL, b2i(t.NamesOnly), hash, sp, sf, sl, sc)
			if err != nil {
				return st, err
			}
			id, _ := res.LastInsertId()
			if err := insertColumns(ctx, insCol, id, t.Columns); err != nil {
				return st, err
			}
			st.TablesAdded++
		}
	}
	for name, r := range existing {
		if seen[name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tables WHERE id = ?", r.id); err != nil {
			return st, err
		}
		st.TablesRemoved++
	}
	return st, nil
}

func insertColumns(ctx context.Context, ins *sql.Stmt, tableID int64, cols []api.ColumnMeta) error {
	// Parents first so parent_id can be resolved.
	ordered := append([]api.ColumnMeta(nil), cols...)
	sortByDepth(ordered)
	ids := make(map[string]int64, len(ordered))
	for _, c := range ordered {
		var parent any
		if p := c.ParentPath(); p != "" {
			if pid, ok := ids[p]; ok {
				parent = pid
			}
		}
		res, err := ins.ExecContext(ctx, tableID, parent, c.FieldPath, strings.ToLower(c.FieldPath), b2i(c.TopLevel()), c.Ordinal, c.DataType, c.Description, b2i(c.Partitioning), c.ClusteringOrdinal)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		ids[c.FieldPath] = id
	}
	return nil
}

func sortByDepth(cols []api.ColumnMeta) {
	depth := func(c api.ColumnMeta) int { return strings.Count(c.FieldPath, ".") }
	sort.SliceStable(cols, func(i, j int) bool { return depth(cols[i]) < depth(cols[j]) })
}

func upsertUnit(ctx context.Context, tx *sql.Tx, s api.UnitState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO sync_units(project,location,last_attempt,last_success,status,error,datasets,tables,names_only,inaccessible)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project,location) DO UPDATE SET last_attempt=excluded.last_attempt,
  last_success=CASE WHEN excluded.last_success>0 THEN excluded.last_success ELSE sync_units.last_success END,
  status=excluded.status,error=excluded.error,datasets=excluded.datasets,tables=excluded.tables,names_only=excluded.names_only,inaccessible=excluded.inaccessible`,
		s.Project, s.Location, unixPtr(s.LastAttempt), unixPtr(s.LastSuccess), string(s.Status), s.Error, s.Datasets, s.Tables, s.NamesOnly, s.Inaccessible)
	return err
}

// RecordUnitFailure records a failed attempt without touching any data. The
// previous last_success and counts are kept.
func (c *Catalog) RecordUnitFailure(ctx context.Context, s api.UnitState) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO sync_units(project,location,last_attempt,last_success,status,error)
VALUES(?,?,?,0,?,?)
ON CONFLICT(project,location) DO UPDATE SET last_attempt=excluded.last_attempt,status=excluded.status,error=excluded.error`,
		s.Project, s.Location, unixPtr(s.LastAttempt), string(s.Status), s.Error)
	return err
}

// RemoveUnit deletes a unit's datasets and its state row (used when a
// location no longer has any datasets).
func (c *Catalog) RemoveUnit(ctx context.Context, u api.UnitRef) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM datasets WHERE location = ? AND project_id IN (SELECT id FROM projects WHERE name = ?)`, u.Location, u.Project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sync_units WHERE project = ? AND location = ?", u.Project, u.Location); err != nil {
		return err
	}
	return tx.Commit()
}

// Units returns the persisted state of every unit.
func (c *Catalog) Units(ctx context.Context) ([]api.UnitState, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT project,location,last_attempt,last_success,status,error,datasets,tables,names_only,inaccessible FROM sync_units ORDER BY project, location`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.UnitState
	for rows.Next() {
		var s api.UnitState
		var la, ls int64
		var status string
		if err := rows.Scan(&s.Project, &s.Location, &la, &ls, &status, &s.Error, &s.Datasets, &s.Tables, &s.NamesOnly, &s.Inaccessible); err != nil {
			return nil, err
		}
		s.Status = api.UnitStatus(status)
		s.LastAttempt = fromUnix(la)
		s.LastSuccess = fromUnix(ls)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Locations returns the locations the catalog holds datasets or unit state
// for in a project.
func (c *Catalog) Locations(ctx context.Context, project string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT location FROM sync_units WHERE project = ? AND location <> ''
UNION SELECT d.location FROM datasets d JOIN projects p ON p.id = d.project_id WHERE p.name = ?`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Counts are the catalog totals.
type Counts struct{ Projects, Datasets, Tables, Columns int }

// Counts returns row counts.
func (c *Catalog) Counts(ctx context.Context) (Counts, error) {
	var n Counts
	err := c.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM projects),(SELECT COUNT(*) FROM datasets),(SELECT COUNT(*) FROM tables),(SELECT COUNT(*) FROM columns)`).
		Scan(&n.Projects, &n.Datasets, &n.Tables, &n.Columns)
	return n, err
}

// DatasetRow is a dataset as stored.
type DatasetRow struct {
	api.DatasetMeta
	Status api.DatasetStatus
	Tables int
}

// Datasets lists a project's datasets.
func (c *Catalog) Datasets(ctx context.Context, project string) ([]DatasetRow, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT d.name,d.location,d.description,d.friendly_name,d.labels,d.status,
 (SELECT COUNT(*) FROM tables t WHERE t.dataset_id = d.id)
 FROM datasets d JOIN projects p ON p.id = d.project_id WHERE p.name = ? ORDER BY d.name_lc`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatasetRow
	for rows.Next() {
		var r DatasetRow
		var labels, status string
		if err := rows.Scan(&r.Name, &r.Location, &r.Description, &r.FriendlyName, &labels, &status, &r.Tables); err != nil {
			return nil, err
		}
		r.Project = project
		r.Labels = decodeLabels(labels)
		r.Status = api.DatasetStatus(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tables lists a dataset's tables (without columns).
func (c *Catalog) Tables(ctx context.Context, project, dataset string) ([]api.TableMeta, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT t.name,t.type,t.created_at,t.description,t.labels,t.expires_at,t.view_sql,t.names_only,t.shard_prefix,t.shard_first,t.shard_last,t.shard_count
FROM tables t JOIN datasets d ON d.id=t.dataset_id JOIN projects p ON p.id=d.project_id
WHERE p.name = ? AND d.name = ? ORDER BY t.name_lc`, project, dataset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.TableMeta
	for rows.Next() {
		var t api.TableMeta
		var typ, labels string
		var created, expires int64
		var names int
		var sp, sf, sl string
		var sc int
		if err := rows.Scan(&t.Name, &typ, &created, &t.Description, &labels, &expires, &t.ViewSQL, &names, &sp, &sf, &sl, &sc); err != nil {
			return nil, err
		}
		t.Dataset = dataset
		t.Type = api.TableType(typ)
		if created > 0 {
			t.CreatedAt = time.Unix(created, 0).UTC()
		}
		if expires > 0 {
			e := time.Unix(expires, 0).UTC()
			t.ExpiresAt = &e
		}
		t.Labels = decodeLabels(labels)
		t.NamesOnly = names != 0
		if sc > 0 {
			t.Shard = &api.ShardFamily{Prefix: sp, First: sf, Last: sl, Count: sc}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Columns lists a table's field paths in order.
func (c *Catalog) Columns(ctx context.Context, project, dataset, table string) ([]api.ColumnMeta, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT c.field_path,c.ordinal,c.data_type,c.description,c.partitioning,c.clustering_ordinal
FROM columns c JOIN tables t ON t.id=c.table_id JOIN datasets d ON d.id=t.dataset_id JOIN projects p ON p.id=d.project_id
WHERE p.name=? AND d.name=? AND t.name=? ORDER BY c.top_level DESC, c.ordinal, c.id`, project, dataset, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.ColumnMeta
	for rows.Next() {
		var cm api.ColumnMeta
		var part int
		if err := rows.Scan(&cm.FieldPath, &cm.Ordinal, &cm.DataType, &cm.Description, &part, &cm.ClusteringOrdinal); err != nil {
			return nil, err
		}
		cm.Partitioning = part != 0
		out = append(out, cm)
	}
	return out, rows.Err()
}

// PruneStats reports what Prune removed.
type PruneStats struct{ Projects, Datasets, Tables int }

// Prune removes everything the current configuration no longer allows,
// without any BigQuery call. allowed is the project allowlist; ignored decides
// dataset and table exclusion (an empty table asks about a dataset).
func (c *Catalog) Prune(ctx context.Context, allowed map[string]bool, ignored func(project, dataset, table string) bool) (PruneStats, error) {
	var st PruneStats
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM projects")
	if err != nil {
		return st, err
	}
	type pr struct {
		id   int64
		name string
	}
	var projects []pr
	for rows.Next() {
		var p pr
		if err := rows.Scan(&p.id, &p.name); err != nil {
			rows.Close()
			return st, err
		}
		projects = append(projects, p)
	}
	rows.Close()
	for _, p := range projects {
		if !allowed[p.name] || ignored(p.name, "", "") {
			if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", p.id); err != nil {
				return st, err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM sync_units WHERE project = ?", p.name); err != nil {
				return st, err
			}
			st.Projects++
			continue
		}
		drows, err := tx.QueryContext(ctx, "SELECT id, name FROM datasets WHERE project_id = ?", p.id)
		if err != nil {
			return st, err
		}
		type dr struct {
			id   int64
			name string
		}
		var datasets []dr
		for drows.Next() {
			var d dr
			if err := drows.Scan(&d.id, &d.name); err != nil {
				drows.Close()
				return st, err
			}
			datasets = append(datasets, d)
		}
		drows.Close()
		for _, d := range datasets {
			if ignored(p.name, d.name, "") {
				if _, err := tx.ExecContext(ctx, "DELETE FROM datasets WHERE id = ?", d.id); err != nil {
					return st, err
				}
				st.Datasets++
				continue
			}
			trows, err := tx.QueryContext(ctx, "SELECT id, name FROM tables WHERE dataset_id = ?", d.id)
			if err != nil {
				return st, err
			}
			var drop []int64
			for trows.Next() {
				var id int64
				var name string
				if err := trows.Scan(&id, &name); err != nil {
					trows.Close()
					return st, err
				}
				if ignored(p.name, d.name, name) {
					drop = append(drop, id)
				}
			}
			trows.Close()
			for _, id := range drop {
				if _, err := tx.ExecContext(ctx, "DELETE FROM tables WHERE id = ?", id); err != nil {
					return st, err
				}
				st.Tables++
			}
		}
	}
	return st, tx.Commit()
}

// --- helpers

func shardCols(s *api.ShardFamily) (string, string, string, int) {
	if s == nil {
		return "", "", "", 0
	}
	return s.Prefix, s.First, s.Last, s.Count
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixPtr(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return unix(*t)
}

func fromUnix(n int64) *time.Time {
	if n <= 0 {
		return nil
	}
	t := time.Unix(n, 0).UTC()
	return &t
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func encodeLabels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m) // sorted keys
	return string(b)
}

func decodeLabels(s string) map[string]string {
	if s == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}
