// Package selection keeps the user's Selected projects and their chosen
// Execution project: the working set that used to live in the config file.
// It is one small JSON file in the data directory's state folder, shared by
// every frontend (TUI, CLI, MCP) and every process.
package selection

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrLastSelected is returned when a change would deselect the last Selected
// project.
var ErrLastSelected = errors.New("the last selected project stays selected (select only another one instead)")

// Project is one Selected project.
type Project struct {
	ID string `json:"id"`
	// SelectedAt decides the default Execution project: the earliest one.
	SelectedAt time.Time `json:"selected_at"`
}

// State is the saved selection.
type State struct {
	Selected []Project `json:"selected"`
	// Execution is the chosen Execution project; "" until the user first
	// chooses one.
	Execution string `json:"execution_project,omitempty"`
}

// Has reports whether id is selected.
func (s State) Has(id string) bool {
	for _, p := range s.Selected {
		if p.ID == id {
			return true
		}
	}
	return false
}

// IDs lists the Selected projects A–Z.
func (s State) IDs() []string {
	out := make([]string, 0, len(s.Selected))
	for _, p := range s.Selected {
		out = append(out, p.ID)
	}
	sort.Strings(out)
	return out
}

// Earliest is the project selected first (ties by ID), or "".
func (s State) Earliest() string {
	best := Project{}
	for _, p := range s.Selected {
		if best.ID == "" || p.SelectedAt.Before(best.SelectedAt) || (p.SelectedAt.Equal(best.SelectedAt) && p.ID < best.ID) {
			best = p
		}
	}
	return best.ID
}

// Select adds projects. One already selected keeps its selection time.
func (s *State) Select(now time.Time, ids ...string) {
	for _, id := range ids {
		if !s.Has(id) {
			s.Selected = append(s.Selected, Project{ID: id, SelectedAt: now})
		}
	}
}

// Deselect removes projects. It refuses (changing nothing) when the selection
// would become empty.
func (s *State) Deselect(ids ...string) error {
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	var keep []Project
	for _, p := range s.Selected {
		if !drop[p.ID] {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 && len(s.Selected) > 0 {
		return ErrLastSelected
	}
	s.Selected = keep
	return nil
}

// Only leaves id as the one Selected project (keeping its selection time when
// it was selected already).
func (s *State) Only(id string, now time.Time) {
	for _, p := range s.Selected {
		if p.ID == id {
			s.Selected = []Project{p}
			return
		}
	}
	s.Selected = []Project{{ID: id, SelectedAt: now}}
}

// Store reads and writes the state file. Reads are cached until the file
// changes, so a change from another process shows up on the next read.
type Store struct {
	path string

	mu    sync.Mutex
	state State
	stamp stamp
	ok    bool
}

// stamp identifies one version of the file. Every save renames a new file
// into place, so the file's identity changes even within one mtime tick.
type stamp struct {
	fi os.FileInfo
}

func (a stamp) same(b stamp) bool {
	return a.fi != nil && b.fi != nil && os.SameFile(a.fi, b.fi) &&
		a.fi.ModTime().Equal(b.fi.ModTime()) && a.fi.Size() == b.fi.Size()
}

// Open returns the store for path (the file need not exist).
func Open(path string) *Store { return &Store{path: path} }

// Path is the state file.
func (st *Store) Path() string { return st.path }

// Load returns the current state; a missing file is the empty state.
func (st *Store) Load() (State, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.loadLocked()
}

func (st *Store) loadLocked() (State, error) {
	fi, err := os.Stat(st.path)
	if errors.Is(err, os.ErrNotExist) {
		st.state, st.stamp, st.ok = State{}, stamp{}, true
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	now := stamp{fi: fi}
	if st.ok && now.same(st.stamp) {
		return clone(st.state), nil
	}
	b, err := os.ReadFile(st.path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("read %s: %w", st.path, err)
	}
	st.state, st.stamp, st.ok = s, now, true
	return clone(s), nil
}

// Update reads the file afresh, applies fn and writes the result atomically
// (last write wins between processes). Nothing is written when fn fails.
func (st *Store) Update(fn func(*State) error) (State, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.ok = false // never apply a change to a cached copy
	s, err := st.loadLocked()
	if err != nil {
		return State{}, err
	}
	if err := fn(&s); err != nil {
		return State{}, err
	}
	if err := Save(st.path, s); err != nil {
		return State{}, err
	}
	st.ok = false
	return s, nil
}

// Save writes s to path atomically.
func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sort.Slice(s.Selected, func(i, j int) bool { return s.Selected[i].ID < s.Selected[j].ID })
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".projects-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

func clone(s State) State {
	s.Selected = append([]Project(nil), s.Selected...)
	return s
}
