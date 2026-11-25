package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/drafts"
)

// buffer is one query being edited. A scratch buffer autosaves to a draft
// that this instance holds (package drafts), so several bqt instances never
// overwrite each other's drafts. A file buffer saves back to its file and has
// no draft.
type buffer struct {
	id   string
	name string
	ed   *editor
	path string // file buffers; "" for a scratch buffer

	draft        *drafts.Draft // scratch buffers, while this instance holds the draft
	draftVersion int           // editor version in the draft file
	savedVersion int           // editor version in the file (file buffers)

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

// draftStore is this frontend's drafts directory.
func (m *Model) draftStore() *drafts.Store { return drafts.In(m.be.DraftDir()) }

func (m *Model) draftPath(id string) string { return m.draftStore().Path(id) }

func (b *buffer) modified() bool { return b.path != "" && b.ed.version != b.savedVersion }

// state is what the drafts package knows of b.
func (b *buffer) state() drafts.Buffer {
	return drafts.Buffer{Path: b.path, Empty: b.ed.empty(), Modified: b.modified(), Ran: b.runID != 0}
}

func (b *buffer) title() string {
	if b.path != "" {
		return filepath.Base(b.path)
	}
	return b.name
}

func (m *Model) buf() *buffer { return m.bufs[m.cur] }

// addBuffer appends a buffer and makes it current.
func (m *Model) addBuffer(text, path string) *buffer {
	return m.addBufferWith(text, path, "", nil)
}

// addBufferWith is addBuffer for a scratch buffer whose draft d is held
// already (a restored draft); with d nil it holds a draft of its own.
func (m *Model) addBufferWith(text, path, id string, d *drafts.Draft) *buffer {
	b := &buffer{id: id, ed: newEditor(text), path: path, params: map[string]string{}, draft: d}
	if b.id == "" {
		b.id = drafts.NewID()
	}
	m.bufSeq++
	b.name = fmt.Sprintf("query %d", m.bufSeq)
	b.ed.row, b.ed.col, b.ed.want = 0, 0, 0
	if path == "" && b.draft == nil {
		b.draft, _ = m.draftStore().Hold(b.id) // nil: not autosaved
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
	b := m.addBuffer(string(data), path)
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

// removeDraft deletes a buffer's draft and lock files (it now lives in a file).
func (m *Model) removeDraft(b *buffer) {
	if b.draft != nil {
		b.draft.Discard()
		b.draft = nil
	}
}

// saveDraft writes a scratch buffer's draft if it changed. An empty buffer
// has no draft.
func (m *Model) saveDraft(b *buffer) {
	if b.path != "" || b.draft == nil || b.draftVersion == b.ed.version {
		return
	}
	// The version counts as saved only once the file is really there, so a
	// failed write (a full disk) is tried again on the next tick.
	version := b.ed.version
	if err := b.draft.Save(b.ed.text()); err != nil {
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
		if b.draft != nil {
			b.draft.Release()
			b.draft = nil
		}
	}
}

// closeBuffer discards the current buffer and its draft.
func (m *Model) closeBuffer() {
	b := m.buf()
	m.removeDraft(b)
	m.bufs = append(m.bufs[:m.cur], m.bufs[m.cur+1:]...)
	m.trimGrids()
	if len(m.bufs) == 0 {
		m.addBuffer("", "")
	}
	m.cur = clamp(m.cur, 0, len(m.bufs)-1)
	m.comp.open = false
}

// scanOrphans lists the drafts no instance holds, deleting empty ones and
// those older than 30 days.
func (m *Model) scanOrphans() {
	ids := make([]string, len(m.bufs))
	for i, b := range m.bufs {
		ids[i] = b.id
	}
	m.orphans = nil
	for _, o := range m.draftStore().Orphans(m.o.Now(), ids) {
		m.orphans = append(m.orphans, orphan{id: o.ID, modTime: o.ModTime, first: o.First, lines: o.Lines})
	}
}

// restoreOrphan opens an orphaned draft as a buffer and takes over its lock.
// An empty scratch buffer it would leave behind is dropped.
func (m *Model) restoreOrphan(o orphan) error {
	d, text, err := m.draftStore().Restore(o.id)
	if errors.Is(err, drafts.ErrHeld) {
		return errors.New("the draft is open in another bqt")
	}
	if err != nil {
		return err
	}
	old := m.buf()
	m.addBufferWith(text, "", o.id, d)
	if old.state().Disposable() {
		for i, x := range m.bufs {
			if x != old {
				continue
			}
			if old.draft != nil {
				old.draft.Release() // an empty buffer: its lock file goes
				old.draft = nil
			}
			m.bufs = append(m.bufs[:i], m.bufs[i+1:]...)
			break
		}
		m.cur = len(m.bufs) - 1
	}
	return nil
}
