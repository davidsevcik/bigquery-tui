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

type buffer struct {
	id   string
	name string
	ed   *editor
	path string

	draft        *drafts.Draft
	draftVersion int
	savedVersion int
	draftErr     string

	vim vim

	runID  int64
	viewID int64
	params map[string]string
}

type orphan struct {
	id      string
	modTime time.Time
	first   string
	lines   int
}

func (m *Model) draftStore() *drafts.Store { return drafts.In(m.be.DraftDir()) }

func (m *Model) draftPath(id string) string { return m.draftStore().Path(id) }

func (b *buffer) modified() bool { return b.path != "" && b.ed.version != b.savedVersion }

func (b *buffer) state() drafts.Buffer {
	drafted := b.draft != nil && b.draftVersion == b.ed.version
	return drafts.Buffer{Path: b.path, Empty: b.ed.empty(), Modified: b.modified(), Ran: b.runID != 0, Drafted: drafted}
}

func (b *buffer) title() string {
	if b.path != "" {
		return filepath.Base(b.path)
	}
	return b.name
}

func (m *Model) buf() *buffer { return m.bufs[m.cur] }

func (m *Model) addBuffer(text, path string) *buffer {
	return m.addBufferWith(text, path, "", nil)
}

func (m *Model) addBufferWith(text, path, id string, d *drafts.Draft) *buffer {
	b := &buffer{id: id, ed: newEditor(text), path: path, params: map[string]string{}, draft: d}
	if b.id == "" {
		b.id = drafts.NewID()
	}
	m.bufSeq++
	b.name = fmt.Sprintf("query %d", m.bufSeq)
	b.ed.row, b.ed.col, b.ed.want = 0, 0, 0
	if path == "" && b.draft == nil {
		d, err := m.draftStore().Hold(b.id)
		if errors.Is(err, drafts.ErrHeld) {
			b.id = drafts.NewID()
			d, err = m.draftStore().Hold(b.id)
		}
		m.heldDraft(b, d, err)
		b.draftVersion = b.ed.version
	}
	m.bufs = append(m.bufs, b)
	m.cur = len(m.bufs) - 1
	return b
}

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

func (m *Model) removeDraft(b *buffer) {
	if b.draft != nil {
		b.draft.Discard()
		b.draft = nil
	}
	b.draftErr = ""
}

func (m *Model) heldDraft(b *buffer, d *drafts.Draft, err error) {
	if err != nil {
		m.draftFailed(b, "hold the buffer's draft", err)
		return
	}
	b.draft, b.draftErr = d, ""
}

func (m *Model) draftFailed(b *buffer, what string, err error) {
	if err.Error() != b.draftErr {
		m.o.Log.Warn(what, "buffer", b.id, "err", err)
	}
	b.draftErr = err.Error()
}

func (m *Model) draftProblem() string {
	for _, b := range m.bufs {
		if b.draftErr != "" {
			return b.draftErr
		}
	}
	return ""
}

func (m *Model) saveDraft(b *buffer) {
	if b.path == "" && b.draft == nil {
		d, err := m.draftStore().Hold(b.id)
		m.heldDraft(b, d, err)
		b.draftVersion = -1
	}
	m.writeDraft(b)
}

func (m *Model) writeDraft(b *buffer) {
	if b.path != "" || b.draft == nil || b.draftVersion == b.ed.version {
		return
	}
	version := b.ed.version
	if err := b.draft.Save(b.ed.text()); err != nil {
		m.draftFailed(b, "save the buffer's draft", err)
		return
	}
	b.draftVersion, b.draftErr = version, ""
}

func (m *Model) saveDrafts() {
	for _, b := range m.bufs {
		m.saveDraft(b)
	}
}

func (m *Model) releaseBuffers() {
	// ADR-0011
	for _, b := range m.bufs {
		m.writeDraft(b)
		if b.draft != nil {
			b.draft.Release()
			b.draft = nil
		}
	}
}

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
				old.draft.Release()
				old.draft = nil
			}
			m.bufs = append(m.bufs[:i], m.bufs[i+1:]...)
			break
		}
		m.cur = len(m.bufs) - 1
	}
	return nil
}
