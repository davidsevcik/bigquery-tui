package store

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	Dir     string
	Catalog string
	History string
	Lock    string
	LogDir  string
	Drafts  string
	State   string
}

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

func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	return nil
}
