// Package drafts keeps the text of a frontend's scratch buffers that is not
// saved to a file: each buffer autosaves to a draft file that one instance
// holds under an advisory lock, so several instances never overwrite each
// other's drafts, and a draft no instance holds (after a crash or a closed
// window) is offered for restore.
//
// Each frontend has its own drafts directory (Dir): the TUI offers only its
// own drafts, and a GUI only its own.
package drafts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/config"
	"github.com/davidsevcik/bigquery-tui/pkg/store"
)

const maxAge = 30 * 24 * time.Hour

var ErrHeld = errors.New("the draft is held by another instance")

var ErrNotAProjectID = errors.New("not a project ID")

func Dir(root string, frontend api.Source) string {
	return filepath.Join(root, string(frontend))
}

func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

type Store struct {
	dir string
}

func In(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Path(id string) string { return filepath.Join(s.dir, id+".sql") }

func (s *Store) lockPath(id string) string { return filepath.Join(s.dir, id+".lock") }

func (s *Store) Hold(id string) (*Draft, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	l, err := store.TryLock(s.lockPath(id))
	if errors.Is(err, store.ErrLocked) {
		return nil, ErrHeld
	}
	if err != nil {
		return nil, err
	}
	empty := true
	if st, err := os.Stat(s.Path(id)); err == nil && st.Size() > 0 {
		empty = false
	}
	return &Draft{s: s, id: id, lock: l, empty: empty, executionProject: heldExecutionProject(l)}, nil
}

const recordEnd = "\n"

func heldExecutionProject(l *store.Lock) string {
	record, err := l.Read()
	if err != nil {
		return ""
	}
	return executionProjectIn(record)
}

func executionProjectIn(record []byte) string {
	project, complete := strings.CutSuffix(string(record), recordEnd)
	if !complete || !config.ValidProjectID(project) {
		return ""
	}
	return project
}

func (s *Store) read(id string) (string, error) {
	data, err := os.ReadFile(s.Path(id))
	return string(data), err
}

func (s *Store) Restore(id string) (*Draft, string, error) {
	d, err := s.Hold(id)
	if err != nil {
		return nil, "", err
	}
	text, err := s.read(id)
	if err != nil {
		d.Release()
		return nil, "", err
	}
	return d, text, nil
}

type Orphan struct {
	ID               string
	ModTime          time.Time
	First            string
	Lines            int
	ExecutionProject string
}

func (s *Store) Orphans(now time.Time, open []string) []Orphan {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	mine := map[string]bool{}
	for _, id := range open {
		mine[id] = true
	}
	var out []Orphan
	for _, e := range entries {
		name := e.Name()
		id, isSQL := strings.CutSuffix(name, ".sql")
		if !isSQL {
			if lid, ok := strings.CutSuffix(name, ".lock"); ok && !mine[lid] {
				s.dropLoneLock(lid)
			}
			continue
		}
		if mine[id] {
			continue
		}
		if o, ok := s.orphan(e, id, now); ok {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

func (s *Store) dropLoneLock(id string) {
	// ADR-0011
	l, err := store.TryLock(s.lockPath(id))
	if err != nil {
		return
	}
	defer l.Unlock()
	if _, err := os.Stat(s.Path(id)); os.IsNotExist(err) {
		os.Remove(s.lockPath(id))
	}
}

func (s *Store) orphan(e os.DirEntry, id string, now time.Time) (Orphan, bool) {
	l, err := store.TryLock(s.lockPath(id))
	if err != nil {
		return Orphan{}, false
	}
	defer l.Unlock()
	info, ierr := e.Info()
	data, rerr := os.ReadFile(s.Path(id))
	if ierr != nil || rerr != nil || strings.TrimSpace(string(data)) == "" || now.Sub(info.ModTime()) > maxAge {
		os.Remove(s.Path(id))
		os.Remove(s.lockPath(id))
		return Orphan{}, false
	}
	first := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			first = strings.TrimSpace(line)
			break
		}
	}
	return Orphan{ID: id, ModTime: info.ModTime(), First: first, Lines: strings.Count(string(data), "\n") + 1, ExecutionProject: heldExecutionProject(l)}, true
}

type Draft struct {
	s                *Store
	id               string
	lock             *store.Lock
	empty            bool
	executionProject string
}

func (d *Draft) ExecutionProject() string { return d.executionProject }

func (d *Draft) SetExecutionProject(project string) error {
	project = strings.TrimSpace(project)
	record := ""
	if project != "" {
		if !config.ValidProjectID(project) {
			return fmt.Errorf("%w: %q", ErrNotAProjectID, project)
		}
		record = project + recordEnd
	}
	if err := d.lock.Rewrite([]byte(record)); err != nil {
		d.executionProject = heldExecutionProject(d.lock)
		return err
	}
	d.executionProject = project
	return nil
}

func (d *Draft) Save(text string) error {
	d.empty = text == ""
	p := d.s.Path(d.id)
	if d.empty {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp := p + ".tmp"
	err := os.WriteFile(tmp, []byte(text), 0o600)
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func (d *Draft) Release() {
	d.lock.Unlock()
	if d.empty {
		os.Remove(d.s.lockPath(d.id))
	}
}

func (d *Draft) Discard() {
	d.lock.Unlock()
	os.Remove(d.s.lockPath(d.id))
	os.Remove(d.s.Path(d.id))
}
