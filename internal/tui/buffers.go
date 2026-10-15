package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// buffer is one query being edited. A scratch buffer autosaves to a draft
// file that this instance holds under an advisory lock, so several bqt
// instances never overwrite each other's drafts. A file buffer saves back to
// its file and has no draft.
type buffer struct {
	id   string
	name string
	ed   *editor
	path string // file buffers; "" for a scratch buffer

	lock         *store.Lock
	draftVersion int // editor version in the draft file
	savedVersion int // editor version in the file (file buffers)

	vim vim // state of the vim layer ([tui] editing = "vim")

	runID  int64 // latest Run started from this buffer
	viewID int64 // Run shown in the results pane
	params map[string]string
}

// orphan is a draft no running instance owns (a crash or a closed terminal).
type orphan struct {
	id      string
	modTime time.Time
	first   string
	lines   int
}

const draftMaxAge = 30 * 24 * time.Hour

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (m *Model) draftPath(id string) string { return filepath.Join(m.be.DraftDir(), id+".sql") }
func (m *Model) lockPath(id string) string  { return filepath.Join(m.be.DraftDir(), id+".lock") }

func (b *buffer) modified() bool { return b.path != "" && b.ed.version != b.savedVersion }

func (b *buffer) title() string {
	if b.path != "" {
		return filepath.Base(b.path)
	}
	return b.name
}

func (m *Model) buf() *buffer { return m.bufs[m.cur] }

// addBuffer appends a buffer and makes it current.
func (m *Model) addBuffer(text, path, id string) *buffer {
	b := &buffer{id: id, ed: newEditor(text), path: path, params: map[string]string{}}
	if b.id == "" {
		b.id = newID()
	}
	m.bufSeq++
	b.name = fmt.Sprintf("query %d", m.bufSeq)
	b.ed.row, b.ed.col, b.ed.want = 0, 0, 0
	if path == "" {
		if err := os.MkdirAll(m.be.DraftDir(), 0o700); err == nil {
			if l, err := store.TryLock(m.lockPath(b.id)); err == nil {
				b.lock = l
			}
		}
		b.draftVersion = b.ed.version
	}
	m.bufs = append(m.bufs, b)
	m.cur = len(m.bufs) - 1
	return b
}

// openFile opens a .sql file as a buffer (bqt path/to/query.sql).
func (m *Model) openFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	abs, aerr := filepath.Abs(path)
	if aerr == nil {
		path = abs
	}
	b := m.addBuffer(string(data), path, "")
	b.savedVersion = b.ed.version
	return nil
}

func (m *Model) saveFile(b *buffer) error {
	text := b.ed.text()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := os.WriteFile(b.path, []byte(text), 0o644); err != nil {
		return err
	}
	b.savedVersion = b.ed.version
	return nil
}

// saveDraft writes a scratch buffer's draft if it changed. An empty buffer
// has no draft.
// removeDraft deletes a buffer's draft and lock files (it now lives in a file).
func (m *Model) removeDraft(b *buffer) {
	os.Remove(m.draftPath(b.id))
	os.Remove(m.lockPath(b.id))
}

func (m *Model) saveDraft(b *buffer) {
	if b.path != "" || b.lock == nil || b.draftVersion == b.ed.version {
		return
	}
	// The version counts as saved only once the file is really there, so a
	// failed write (a full disk) is tried again on the next tick.
	version := b.ed.version
	p := m.draftPath(b.id)
	if b.ed.empty() {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			m.draftErr = err.Error()
			return
		}
		b.draftVersion, m.draftErr = version, ""
		return
	}
	tmp := p + ".tmp"
	err := os.WriteFile(tmp, []byte(b.ed.text()), 0o600)
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		m.draftErr = err.Error() // shown in the status line until a write succeeds
		return
	}
	b.draftVersion, m.draftErr = version, ""
}

func (m *Model) saveDrafts() {
	for _, b := range m.bufs {
		m.saveDraft(b)
	}
}

// releaseBuffers saves the drafts and gives up their locks (on quit), so the
// next start can offer them for restore.
func (m *Model) releaseBuffers() {
	for _, b := range m.bufs {
		m.saveDraft(b)
		if b.lock != nil {
			b.lock.Unlock()
			b.lock = nil
			if b.ed.empty() {
				os.Remove(m.lockPath(b.id))
			}
		}
	}
}

// closeBuffer discards the current buffer and its draft.
func (m *Model) closeBuffer() {
	b := m.buf()
	if b.lock != nil {
		b.lock.Unlock()
		os.Remove(m.lockPath(b.id))
	}
	if b.path == "" {
		os.Remove(m.draftPath(b.id))
	}
	m.bufs = append(m.bufs[:m.cur], m.bufs[m.cur+1:]...)
	m.trimGrids()
	if len(m.bufs) == 0 {
		m.addBuffer("", "", "")
	}
	m.cur = clamp(m.cur, 0, len(m.bufs)-1)
	m.comp.open = false
}

// scanOrphans lists the drafts no instance holds, deleting empty ones and
// those older than 30 days.
func (m *Model) scanOrphans() {
	m.orphans = nil
	dir := m.be.DraftDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	mine := map[string]bool{}
	for _, b := range m.bufs {
		mine[b.id] = true
	}
	now := m.o.Now()
	for _, e := range entries {
		name := e.Name()
		id, isSQL := strings.CutSuffix(name, ".sql")
		if !isSQL {
			if lid, ok := strings.CutSuffix(name, ".lock"); ok && !mine[lid] {
				// a lock file without a draft is left over from an empty buffer
				if _, err := os.Stat(m.draftPath(lid)); os.IsNotExist(err) {
					if l, err := store.TryLock(filepath.Join(dir, name)); err == nil {
						os.Remove(filepath.Join(dir, name))
						l.Unlock()
					}
				}
			}
			continue
		}
		if mine[id] {
			continue
		}
		l, err := store.TryLock(m.lockPath(id))
		if err != nil {
			continue // another instance is editing it
		}
		info, ierr := e.Info()
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if ierr != nil || rerr != nil || strings.TrimSpace(string(data)) == "" || now.Sub(info.ModTime()) > draftMaxAge {
			os.Remove(filepath.Join(dir, name))
			os.Remove(m.lockPath(id))
			l.Unlock()
			continue
		}
		l.Unlock()
		first := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				first = strings.TrimSpace(line)
				break
			}
		}
		m.orphans = append(m.orphans, orphan{id: id, modTime: info.ModTime(), first: first, lines: strings.Count(string(data), "\n") + 1})
	}
	sort.Slice(m.orphans, func(i, j int) bool { return m.orphans[i].modTime.After(m.orphans[j].modTime) })
}

// restoreOrphan opens an orphaned draft as a buffer and takes over its lock.
// An empty scratch buffer it would leave behind is dropped.
func (m *Model) restoreOrphan(o orphan) error {
	data, err := os.ReadFile(m.draftPath(o.id))
	if err != nil {
		return err
	}
	old := m.buf()
	b := m.addBuffer(string(data), "", o.id)
	if b.lock == nil {
		m.bufs = m.bufs[:len(m.bufs)-1]
		m.cur = clamp(m.cur, 0, len(m.bufs)-1)
		return fmt.Errorf("the draft is open in another bqt")
	}
	if old.path == "" && old.ed.empty() && old.runID == 0 {
		for i, x := range m.bufs {
			if x != old {
				continue
			}
			if old.lock != nil {
				old.lock.Unlock()
				os.Remove(m.lockPath(old.id))
			}
			m.bufs = append(m.bufs[:i], m.bufs[i+1:]...)
			break
		}
		m.cur = len(m.bufs) - 1
	}
	return nil
}
