// Package store owns bqt's local files: the disposable catalog.db, the durable
// history.db and the advisory lockfile (ticket 06, spec section 5 and 6).
package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths are the files inside data_dir.
type Paths struct {
	Dir     string
	Catalog string
	History string
	Lock    string
	LogDir  string
	Drafts  string
	State   string
}

// PathsFor derives every path from data_dir.
func PathsFor(dataDir string) Paths {
	return Paths{
		Dir:     dataDir,
		Catalog: filepath.Join(dataDir, "catalog.db"),
		History: filepath.Join(dataDir, "history.db"),
		Lock:    filepath.Join(dataDir, "bqt.lock"),
		LogDir:  filepath.Join(dataDir, "logs"),
		Drafts:  filepath.Join(dataDir, "drafts"),
		State:   filepath.Join(dataDir, "state"),
	}
}

// Ensure creates the data directory (private to the user).
func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	return nil
}
