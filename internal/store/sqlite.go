package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SQLite result codes we react to.
const (
	sqliteCorrupt = 11
	sqliteNotADB  = 26
)

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	if readOnly {
		q.Set("mode", "ro")
	}
	// The path must be escaped: SQLite parses the DSN as a URI, so a raw '#',
	// '?' or '%' in data_dir would silently change which file is opened.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

func openDB(path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, err
	}
	// One writer at a time per handle keeps transaction semantics simple;
	// WAL still allows other processes (and read-only handles) to read.
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// isCorrupt reports whether err means the file is not a usable database.
func isCorrupt(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqliteCorrupt, sqliteNotADB:
			return true
		}
	}
	return false
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func setUserVersion(tx *sql.Tx, v int) error {
	_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

func removeDBFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
