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
// migrations: a mismatch deletes and recreates the file (ticket 06). A column
// with a default is added in place instead (addedColumns), without a version
// change, so that older bqt binaries sharing the file keep working.
const CatalogSchemaVersion = 3

// addedColumns are the columns added to the schema after its version: the
// writer open adds any an existing file lacks, and the read-only open reads
// a missing one as its default. Older binaries name their columns, so they
// never see these.
var addedColumns = []struct{ table, column, decl, def string }{
	{"tables", "require_partition_filter", "INTEGER NOT NULL DEFAULT 0", "0"},
	// shard families from shard_patterns (spec section 8.3)
	{"tables", "shard_name", "TEXT NOT NULL DEFAULT ''", "''"},
	{"tables", "shard_suffix", "TEXT NOT NULL DEFAULT ''", "''"},
	{"tables", "shard_date", "INTEGER NOT NULL DEFAULT 0", "0"},
	{"tables", "shard_newest", "TEXT NOT NULL DEFAULT ''", "''"},
	{"tables", "shard_newest_created", "INTEGER NOT NULL DEFAULT 0", "0"},
	{"tables", "shared_schema", "INTEGER NOT NULL DEFAULT 0", "0"},
	{"tables", "own_schema", "INTEGER NOT NULL DEFAULT 0", "0"},
}

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
  require_partition_filter INTEGER NOT NULL DEFAULT 0,
  shard_prefix TEXT NOT NULL DEFAULT '',
  shard_first  TEXT NOT NULL DEFAULT '',
  shard_last   TEXT NOT NULL DEFAULT '',
  shard_count  INTEGER NOT NULL DEFAULT 0,
  shard_name   TEXT NOT NULL DEFAULT '',
  shard_suffix TEXT NOT NULL DEFAULT '',
  shard_date   INTEGER NOT NULL DEFAULT 0,
  shard_newest TEXT NOT NULL DEFAULT '',
  shard_newest_created INTEGER NOT NULL DEFAULT 0,
  shared_schema INTEGER NOT NULL DEFAULT 0,
  own_schema   INTEGER NOT NULL DEFAULT 0,
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
  clustering_ordinal INTEGER NOT NULL DEFAULT 0,
  required           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX columns_table ON columns(table_id, parent_id, ordinal);
-- Required: the self-referencing parent_id foreign key makes SQLite look up
-- children by parent_id on every column delete. Without this index a changed
-- table costs a full scan of columns per deleted row (quadratic at scale).
CREATE INDEX columns_parent ON columns(parent_id);
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
	// missing lists the added tables columns a read-only file predates:
	// the readers select their defaults instead (see tcol).
	missing map[string]bool
	// trim retries a WAL truncation that a reader blocked (trimWAL).
	trim walTrimmer
}

// OpenCatalog opens (creating if needed) the catalog for writing. A schema
// version mismatch or a corrupt file recreates it; Recreated reports why.
func OpenCatalog(path string) (*Catalog, error) {
	// Creating or recreating the file must not interleave with another
	// process doing the same (two first launches at once), so the whole
	// check-and-create sequence runs under a blocking init lock.
	unlock, err := lockBlocking(path + ".init.lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
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
				if err := addMissingColumns(db); err != nil {
					db.Close()
					return nil, err
				}
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
	c := &Catalog{db: db, path: path, readOnly: true}
	for _, a := range addedColumns {
		has, err := hasColumn(db, a.table, a.column)
		if err != nil {
			db.Close()
			return nil, err
		}
		if !has {
			if c.missing == nil {
				c.missing = map[string]bool{}
			}
			c.missing[a.column] = true // written before the column: read its default
		}
	}
	return c, nil
}

// addMissingColumns adds the columns an existing file lacks, keeping its data.
// The caller holds the init lock.
func addMissingColumns(db *sql.DB) error {
	for _, a := range addedColumns {
		has, err := hasColumn(db, a.table, a.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + a.table + " ADD COLUMN " + a.column + " " + a.decl); err != nil {
			return fmt.Errorf("add catalog column %s.%s: %w", a.table, a.column, err)
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == column {
			return true, nil
		}
	}
	return false, rows.Err()
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
func (c *Catalog) Close() error {
	c.trim.stop()
	return c.db.Close()
}

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
	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var n string
		var id int64
		if err := rows.Scan(&n, &id); err != nil {
			return err
		}
		existing[n] = id
		return nil
	}, "SELECT name, id FROM datasets WHERE project_id = ? AND location = ?", projectID, req.Unit.Location)
	if err != nil {
		return st, err
	}

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
	if err := tx.Commit(); err != nil {
		return st, err
	}
	c.trimWAL(ctx)
	return st, nil
}

// trimWAL checkpoints the WAL and truncates it, so a large unit does not
// leave a WAL file of its size behind while the process keeps running (#91).
// It never waits: a reader still in the WAL (another pooled connection, or
// the TUI, CLI or MCP process) makes it give up at once instead of holding
// the write lock for busy_timeout, and it then retries in the background
// until the WAL is truncated or the catalog closes.
func (c *Catalog) trimWAL(ctx context.Context) {
	if done, _ := truncateWAL(ctx, c.db); !done {
		c.trim.retry(ctx, c.db)
	}
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
	id   int64
	hash string
}

func applyTables(ctx context.Context, tx *sql.Tx, datasetID int64, tables []api.TableMeta, namesOnly bool) (ApplyStats, error) {
	var st ApplyStats
	existing := map[string]tableRow{}
	err := eachRow(ctx, tx, func(rows *sql.Rows) error {
		var n string
		var r tableRow
		if err := rows.Scan(&n, &r.id, &r.hash); err != nil {
			return err
		}
		existing[n] = r
		return nil
	}, "SELECT name, id, content_hash FROM tables WHERE dataset_id = ?", datasetID)
	if err != nil {
		return st, err
	}

	insTable, err := tx.PrepareContext(ctx, `INSERT INTO tables(dataset_id,name,name_lc,type,created_at,description,labels,expires_at,view_sql,names_only,content_hash,require_partition_filter,shard_prefix,shard_first,shard_last,shard_count,shard_name,shard_suffix,shard_date,shard_newest,shard_newest_created,shared_schema,own_schema) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return st, err
	}
	defer insTable.Close()
	updTable, err := tx.PrepareContext(ctx, `UPDATE tables SET type=?,created_at=?,description=?,labels=?,expires_at=?,view_sql=?,names_only=?,content_hash=?,require_partition_filter=?,`+shardAssign+` WHERE id=?`)
	if err != nil {
		return st, err
	}
	defer updTable.Close()
	updShard, err := tx.PrepareContext(ctx, `UPDATE tables SET `+shardAssign+` WHERE id=?`)
	if err != nil {
		return st, err
	}
	defer updShard.Close()
	insCol, err := tx.PrepareContext(ctx, insertColumnSQL)
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
		sv := shardValues(t)
		old, found := existing[t.Name]
		switch {
		case found && old.hash == hash:
			if _, err := updShard.ExecContext(ctx, append(sv, old.id)...); err != nil {
				return st, err
			}
			st.TablesUnchanged++
		case found:
			args := append([]any{string(t.Type), unix(t.CreatedAt), t.Description, encodeLabels(t.Labels), unixPtr(t.ExpiresAt), t.ViewSQL, b2i(t.NamesOnly), hash, b2i(t.RequirePartitionFilter)}, sv...)
			if _, err := updTable.ExecContext(ctx, append(args, old.id)...); err != nil {
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
			args := append([]any{datasetID, t.Name, strings.ToLower(t.Name), string(t.Type), unix(t.CreatedAt), t.Description, encodeLabels(t.Labels), unixPtr(t.ExpiresAt), t.ViewSQL, b2i(t.NamesOnly), hash, b2i(t.RequirePartitionFilter)}, sv...)
			res, err := insTable.ExecContext(ctx, args...)
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
		res, err := ins.ExecContext(ctx, tableID, parent, c.FieldPath, strings.ToLower(c.FieldPath), b2i(c.TopLevel()), c.Ordinal, c.DataType, c.Description, b2i(c.Partitioning), c.ClusteringOrdinal, b2i(c.Required))
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

// RecordEmptyProject records a complete dataset listing that left nothing to
// sync: the project has no (project, location) unit, so its discovery unit
// (location "") carries the successful sync, with 0 datasets.
func (c *Catalog) RecordEmptyProject(ctx context.Context, project string, at time.Time) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := ensureProject(ctx, tx, project); err != nil {
		return err
	}
	if err := upsertUnit(ctx, tx, api.UnitState{UnitRef: api.UnitRef{Project: project}, Status: api.UnitOK, LastAttempt: &at, LastSuccess: &at}); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordListing stores a project's dataset listing before its units sync.
// Every listed dataset the catalog does not hold yet is inserted as pending,
// with the listing's location, friendly name and labels and no tables; rows
// the catalog already holds in the listed location are never touched. A
// dataset listed in another location than its row was recreated there, so
// its row is replaced by a pending one in the new location: otherwise the
// old location's unit or its vanished-location removal would delete it, and
// it would be missing until the new location's unit commits. When the
// listing is complete (no unreachable locations), pending rows it no longer
// returns are deleted. It reports how many rows it added and removed.
func (c *Catalog) RecordListing(ctx context.Context, project string, listed []api.DatasetMeta, complete bool) (added, removed int, err error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var projectID int64
	if len(listed) > 0 {
		if projectID, err = ensureProject(ctx, tx, project); err != nil {
			return 0, 0, err
		}
	} else {
		err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE name = ?", project).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil // nothing listed, nothing held
		}
		if err != nil {
			return 0, 0, err
		}
	}
	type heldRow struct {
		status   api.DatasetStatus
		location string
	}
	held := map[string]heldRow{}
	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var n, s, l string
		if err := rows.Scan(&n, &s, &l); err != nil {
			return err
		}
		held[n] = heldRow{api.DatasetStatus(s), l}
		return nil
	}, "SELECT name, status, location FROM datasets WHERE project_id = ?", projectID)
	if err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, d := range listed {
		seen[d.Name] = true
		if h, ok := held[d.Name]; ok {
			if h.location == d.Location {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM datasets WHERE project_id = ? AND name = ?", projectID, d.Name); err != nil {
				return 0, 0, err
			}
			removed++
		}
		held[d.Name] = heldRow{api.DatasetPending, d.Location}
		if _, err := tx.ExecContext(ctx, `INSERT INTO datasets(project_id,name,name_lc,location,friendly_name,labels,status) VALUES(?,?,?,?,?,?,?)`,
			projectID, d.Name, strings.ToLower(d.Name), d.Location, d.FriendlyName, encodeLabels(d.Labels), string(api.DatasetPending)); err != nil {
			return 0, 0, err
		}
		added++
	}
	if complete {
		for name, h := range held {
			if h.status != api.DatasetPending || seen[name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM datasets WHERE project_id = ? AND name = ?", projectID, name); err != nil {
				return 0, 0, err
			}
			removed++
		}
	}
	return added, removed, tx.Commit()
}

// PendingUnits counts the pending datasets per (project, location).
func (c *Catalog) PendingUnits(ctx context.Context) (map[api.UnitRef]int, error) {
	out := map[api.UnitRef]int{}
	err := eachRow(ctx, c.db, func(rows *sql.Rows) error {
		var u api.UnitRef
		var n int
		if err := rows.Scan(&u.Project, &u.Location, &n); err != nil {
			return err
		}
		out[u] = n
		return nil
	}, `SELECT p.name, d.location, COUNT(*) FROM datasets d JOIN projects p ON p.id = d.project_id
WHERE d.status = ? GROUP BY p.name, d.location`, string(api.DatasetPending))
	return out, err
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
	//nolint:gosec // tcol and shardSelect are fixed column expressions, never input
	rows, err := c.db.QueryContext(ctx, `SELECT t.name,t.type,t.created_at,t.description,t.labels,t.expires_at,t.view_sql,t.names_only,`+c.tcol("require_partition_filter")+`,`+c.shardSelect()+`
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
		var names, rpf int
		var sr shardRow
		if err := rows.Scan(append([]any{&t.Name, &typ, &created, &t.Description, &labels, &expires, &t.ViewSQL, &names, &rpf}, sr.dest()...)...); err != nil {
			return nil, err
		}
		t.RequirePartitionFilter = rpf != 0
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
		sr.apply(&t)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Columns lists a table's field paths in order.
func (c *Catalog) Columns(ctx context.Context, project, dataset, table string) ([]api.ColumnMeta, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT c.field_path,c.ordinal,c.data_type,c.description,c.partitioning,c.clustering_ordinal,c.required
FROM columns c JOIN tables t ON t.id=c.table_id JOIN datasets d ON d.id=t.dataset_id JOIN projects p ON p.id=d.project_id
WHERE p.name=? AND d.name=? AND t.name=? ORDER BY c.top_level DESC, c.ordinal, c.id`, project, dataset, table)
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

	type pr struct {
		id   int64
		name string
	}
	var projects []pr
	err = eachRow(ctx, tx, func(rows *sql.Rows) error {
		var p pr
		if err := rows.Scan(&p.id, &p.name); err != nil {
			return err
		}
		projects = append(projects, p)
		return nil
	}, "SELECT id, name FROM projects")
	if err != nil {
		return st, err
	}
	for _, p := range projects {
		// Only the allowlist removes a whole project (nil keeps every
		// project). An ignored project can still hold re-included datasets
		// ("p", "!p.keep"), so ignore rules are applied per dataset and per
		// table, exactly as sync applies them.
		if allowed != nil && !allowed[p.name] {
			if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", p.id); err != nil {
				return st, err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM sync_units WHERE project = ?", p.name); err != nil {
				return st, err
			}
			st.Projects++
			continue
		}
		type dr struct {
			id   int64
			name string
		}
		var datasets []dr
		err := eachRow(ctx, tx, func(rows *sql.Rows) error {
			var d dr
			if err := rows.Scan(&d.id, &d.name); err != nil {
				return err
			}
			datasets = append(datasets, d)
			return nil
		}, "SELECT id, name FROM datasets WHERE project_id = ?", p.id)
		if err != nil {
			return st, err
		}
		for _, d := range datasets {
			if ignored(p.name, d.name, "") {
				if _, err := tx.ExecContext(ctx, "DELETE FROM datasets WHERE id = ?", d.id); err != nil {
					return st, err
				}
				st.Datasets++
				continue
			}
			var drop []int64
			err := eachRow(ctx, tx, func(rows *sql.Rows) error {
				var id int64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					return err
				}
				if ignored(p.name, d.name, name) {
					drop = append(drop, id)
				}
				return nil
			}, "SELECT id, name FROM tables WHERE dataset_id = ?", d.id)
			if err != nil {
				return st, err
			}
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

const insertColumnSQL = `INSERT INTO columns(table_id,parent_id,field_path,field_path_lc,top_level,ordinal,data_type,description,partitioning,clustering_ordinal,required) VALUES(?,?,?,?,?,?,?,?,?,?,?)`

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
