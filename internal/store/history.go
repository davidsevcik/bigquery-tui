package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// historyMigrations are the embedded, forward-only migrations. Index i takes
// the database from version i to i+1. Never edit a released migration; append.
var historyMigrations = []string{
	// 1: initial schema
	`
CREATE TABLE history (
  id                    INTEGER PRIMARY KEY,
  project               TEXT NOT NULL,
  location              TEXT NOT NULL,
  job_id                TEXT NOT NULL,
  execution_project     TEXT NOT NULL,
  sql                   TEXT NOT NULL,
  statement_type        TEXT NOT NULL DEFAULT '',
  destination_table     TEXT NOT NULL DEFAULT '',
  state                 TEXT NOT NULL,
  error_message         TEXT NOT NULL DEFAULT '',
  total_bytes_processed INTEGER,
  total_bytes_billed    INTEGER,
  cache_hit             INTEGER NOT NULL DEFAULT 0,
  created_at            INTEGER NOT NULL,
  started_at            INTEGER,
  ended_at              INTEGER,
  duration_ms           INTEGER,
  total_slot_ms         INTEGER,
  reservation_id        TEXT NOT NULL DEFAULT '',
  edition               TEXT NOT NULL DEFAULT '',
  source                TEXT NOT NULL,
  favorite_path         TEXT NOT NULL DEFAULT '',
  params                TEXT NOT NULL DEFAULT '',
  UNIQUE (project, location, job_id)
);
CREATE INDEX history_created ON history(created_at);
CREATE VIRTUAL TABLE history_fts USING fts5(sql, content='history', content_rowid='id');
CREATE TRIGGER history_ai AFTER INSERT ON history BEGIN
  INSERT INTO history_fts(rowid, sql) VALUES (new.id, new.sql);
END;
CREATE TRIGGER history_ad AFTER DELETE ON history BEGIN
  INSERT INTO history_fts(history_fts, rowid, sql) VALUES ('delete', old.id, old.sql);
END;
CREATE TRIGGER history_au AFTER UPDATE OF sql ON history BEGIN
  INSERT INTO history_fts(history_fts, rowid, sql) VALUES ('delete', old.id, old.sql);
  INSERT INTO history_fts(rowid, sql) VALUES (new.id, new.sql);
END;
CREATE TABLE recent_objects (
  path        TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  last_opened INTEGER NOT NULL,
  open_count  INTEGER NOT NULL DEFAULT 1
);
`,
}

// HistorySchemaVersion is the newest history.db schema this binary knows.
var HistorySchemaVersion = len(historyMigrations)

// ErrHistoryTooNew is returned for a history.db written by a newer bqt.
var ErrHistoryTooNew = errors.New("history.db was written by a newer bqt; refusing to open it")

// History is the durable store of Runs and recently opened objects.
type History struct {
	db   *sql.DB
	path string
	// Notice carries a message the frontend should show once (a migration
	// backup, or a recovered corrupt file).
	Notice string
}

// OpenHistory opens history.db, migrating it forward after taking a backup.
func OpenHistory(path string) (*History, error) {
	return openHistory(path, historyMigrations, time.Now)
}

func openHistory(path string, migrations []string, now func() time.Time) (*History, error) {
	notice := ""
	if fileExists(path) {
		h, err := tryOpenHistory(path, migrations)
		switch {
		case err == nil:
			return h, nil
		case errors.Is(err, ErrHistoryTooNew):
			return nil, err
		case isCorrupt(err):
			dst := fmt.Sprintf("%s.corrupt-%s", path, now().UTC().Format("20060102T150405Z"))
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if rerr := os.Rename(path+suffix, dst+suffix); rerr != nil && suffix == "" {
					return nil, rerr
				}
			}
			notice = fmt.Sprintf("history.db was corrupt; it was moved to %s and a fresh history started. Entries in the old file are lost unless it can be repaired.", filepath.Base(dst))
		default:
			return nil, err
		}
	}
	db, err := openDB(path, false)
	if err != nil {
		return nil, err
	}
	h := &History{db: db, path: path, Notice: notice}
	if err := h.migrate(0, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return h, nil
}

func tryOpenHistory(path string, migrations []string) (*History, error) {
	db, err := openDB(path, false)
	if err != nil {
		return nil, err
	}
	v, err := userVersion(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	h := &History{db: db, path: path}
	switch {
	case v > len(migrations):
		db.Close()
		return nil, fmt.Errorf("%w (file is v%d, this binary knows v%d)", ErrHistoryTooNew, v, len(migrations))
	case v == len(migrations):
		return h, nil
	}
	// v < latest: back up, then migrate in one transaction.
	if v > 0 {
		if err := backupHistory(db, path, v); err != nil {
			db.Close()
			return nil, fmt.Errorf("back up history before migration: %w", err)
		}
		h.Notice = fmt.Sprintf("history.db upgraded from v%d to v%d (backup: %s)", v, len(migrations), filepath.Base(backupName(path, v)))
	}
	if err := h.migrate(v, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return h, nil
}

func backupName(path string, v int) string { return fmt.Sprintf("%s.bak-v%d", path, v) }

func backupHistory(db *sql.DB, path string, v int) error {
	// VACUUM INTO writes a transactionally consistent copy even while other
	// connections hold the WAL open (a plain file copy after a checkpoint can
	// miss committed pages).
	dst := backupName(path, v)
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := db.Exec("VACUUM INTO '" + strings.ReplaceAll(dst, "'", "''") + "'"); err != nil {
		return err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return err
	}
	// Keep only the newest backup.
	matches, _ := filepath.Glob(path + ".bak-v*")
	sort.Slice(matches, func(i, j int) bool { return bakVersion(matches[i]) < bakVersion(matches[j]) })
	for _, m := range matches {
		if m != dst && bakVersion(m) <= bakVersion(dst) {
			os.Remove(m)
		}
	}
	return nil
}

func bakVersion(p string) int {
	i := strings.LastIndex(p, ".bak-v")
	n, _ := strconv.Atoi(p[i+len(".bak-v"):])
	return n
}

func (h *History) migrate(from int, migrations []string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := from; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("history migration to v%d: %w", i+1, err)
		}
	}
	if err := setUserVersion(tx, len(migrations)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (h *History) Close() error { return h.db.Close() }

// DB exposes the handle for the History and Favorites features (phase 4).
func (h *History) DB() *sql.DB { return h.db }

// Path returns the file path.
func (h *History) Path() string { return h.path }

// Version returns the schema version in the file.
func (h *History) Version() (int, error) { return userVersion(h.db) }

// TouchRecent records that an object was opened.
func (h *History) TouchRecent(ctx context.Context, path, kind string, at time.Time) error {
	_, err := h.db.ExecContext(ctx, `INSERT INTO recent_objects(path,kind,last_opened,open_count) VALUES(?,?,?,1)
ON CONFLICT(path) DO UPDATE SET kind=excluded.kind,last_opened=excluded.last_opened,open_count=recent_objects.open_count+1`,
		path, kind, at.Unix())
	return err
}

// Recent returns the most recently opened objects, newest first.
func (h *History) Recent(ctx context.Context, limit int) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, "SELECT path FROM recent_objects ORDER BY last_opened DESC, path LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
