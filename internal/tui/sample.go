package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// The Sample is a table's rows read for free through tabledata.list
// (Engine.Preview) and shown in the Results pane. It is not a Run: it has no
// job, no Run ID, no History entry and no place in the run list. There is one
// Sample slot per session, not per buffer, because it is about a table.

// sample is the Sample slot.
type sample struct {
	key  string         // the selection's detail key
	path api.ObjectPath // the selection's path (a shard family reads its newest shard)
	name string         // project.dataset.table as shown (the newest shard for a family)
	g    *grid          // nil until the first page arrives
	err  string
	seq  int

	// what the Results showed before: restored when the table turns out to
	// need a query (the catalog was out of date)
	prev     *sample
	prevShow bool
}

type sampleMsg struct {
	seq  int
	page api.PreviewPage
	err  error
}

// sampleShown reports whether the Results pane shows the Sample.
func (m *Model) sampleShown() bool { return m.showSample && m.sample != nil }

// showRun switches the Results back to the buffer's Run (a Run was started
// or picked).
func (m *Model) showRun() { m.showSample = false }

// revealPane makes sure a Pane that just got content is on screen. A hidden
// Pane is shown again, and another Pane maximised over it is restored (the
// zoom stays when z itself is maximised). Where it is still not drawn (the
// focus Layout, chosen or the small-terminal step-down, or a Pane the Layout
// has no room for) it takes the focus; elsewhere the focus stays.
func (m *Model) revealPane(z Zone) {
	m.uncover(z)
	if a := m.arr(); z != m.zone && a.bar != nil && slices.Contains(a.barPanes, z) && !a.single && a.zoom == "" && !a.offLayout {
		// browse's shared slot draws the one of its Panes focused last: z
		// becomes that one, and the focus stays
		m.mru = append([]Zone{m.mru[0], z}, slices.DeleteFunc(slices.Clone(m.mru[1:]), func(o Zone) bool { return o == z })...)
	}
	if _, ok := m.arr().rects[z]; !ok {
		m.setZone(z)
	}
}

// uncover shows a hidden Pane and clears a zoom on another Pane, without
// moving the focus.
func (m *Model) uncover(z Zone) {
	if m.panes.hiddenPane(z) {
		m.setPanes(showPane(m.panes, z))
	}
	if m.maxed != "" && m.maxed != z {
		m.maxed = ""
	}
}

const (
	viewNoSample     = "views have no free sample; press s for a SELECT (billed)"
	externalNoSample = "external tables have no free sample; press s for a SELECT (billed)"
)

// sampleAction is p in the Explorer and the Schema pane.
func (m *Model) sampleAction() tea.Cmd {
	s := m.sel()
	key := s.detailKey()
	if key == "" {
		m.toast = "select a table to sample it"
		return nil
	}
	if m.tut != nil {
		m.tut.sampled = true
	}
	switch s.node.Type {
	case api.TypeView:
		m.toast = viewNoSample
		return nil
	case api.TypeExternal:
		m.toast = externalNoSample
		return nil
	}
	if m.sample != nil && m.sample.key == key && m.sample.err == "" {
		m.showSample = true
		m.revealPane(ZoneResults)
		return m.gridEnsure()
	}
	m.sampleSeq++
	sm := &sample{key: key, path: s.path, name: s.path.String(), seq: m.sampleSeq, prev: m.sample, prevShow: m.showSample}
	if sm.prev != nil {
		sm.prev.prev = nil // keep one step back, not a chain
	}
	if s.kind == api.KindShards && s.node.Shard != nil {
		// read the newest shard as it is now, page after page, even when a
		// sync adds a newer one meanwhile
		sm.path.Table = s.node.Shard.Prefix + s.node.Shard.Last
		sm.name = sm.path.String()
	}
	m.sample, m.showSample = sm, true
	m.revealPane(ZoneResults)
	p, seq := sm.path, sm.seq
	return func() tea.Msg {
		page, err := m.be.Preview(m.ctx, p, 0, gridPage, gridFormat)
		return sampleMsg{seq, page, err}
	}
}

func (m *Model) onSample(msg sampleMsg) tea.Cmd {
	sm := m.sample
	if sm == nil || sm.seq != msg.seq {
		return nil // a replaced Sample
	}
	switch {
	case errors.Is(msg.err, bq.ErrNeedsQuery):
		// the catalog said table, BigQuery says view or external table: back
		// to what the Results showed (a Sample still loading then never
		// finishes, so it goes too)
		m.toast = viewNoSample
		prev := sm.prev
		if prev != nil && prev.g == nil && prev.err == "" {
			prev = nil
		}
		// switching to a Run meanwhile is not undone
		m.sample, m.showSample = prev, m.showSample && prev != nil && sm.prevShow
		if m.sampleShown() {
			return m.gridEnsure()
		}
		return nil
	case msg.err != nil:
		sm.err = msg.err.Error()
		return nil
	}
	sm.prev = nil
	path := sm.path
	src := func(ctx context.Context, start int64, n int, o format.Options) (api.PreviewPage, error) {
		return m.be.Preview(ctx, path, start, n, o)
	}
	g := newGrid(sm.name[strings.LastIndex(sm.name, ".")+1:], msg.page.Columns, msg.page.TotalRows, src)
	g.store(0, msg.page.Rows)
	sm.g = g
	if m.sampleShown() {
		return m.gridEnsure()
	}
	return nil
}

// sampleTitle is the Results title while the Sample is shown.
func (m *Model) sampleTitle() string {
	t := " Sample · free read, no query job · " + m.sample.name
	if g := m.sample.g; g != nil {
		t += " · " + format.Count(g.total) + " rows"
	}
	return t
}

// switchSource is [ and ] in the Results: the Sample or the buffer's Run.
func (m *Model) switchSource() tea.Cmd {
	if m.sample == nil {
		m.toast = "no Sample: p on a table loads one"
		return nil
	}
	m.showSample = !m.showSample
	return m.gridEnsure()
}

// --- s: a SELECT for the table in a new buffer

type selectDetailMsg struct {
	path api.ObjectPath
	key  string
	det  api.TableDetail
	err  error
}

// selectAction is s in the Explorer and the Schema pane. It never runs the
// query.
func (m *Model) selectAction() tea.Cmd {
	s := m.sel()
	key := s.detailKey()
	if key == "" {
		m.toast = "select a table to write a SELECT for it"
		return nil
	}
	if d := m.details[key]; d != nil && (d.det != nil || d.detErr != "") {
		var det api.TableDetail
		if d.det != nil {
			det = *d.det
		}
		m.openSelect(s.path, det)
		return nil
	}
	p := s.path
	return func() tea.Msg {
		det, err := m.be.TableDetail(m.ctx, p)
		return selectDetailMsg{p, key, det, err}
	}
}

func (m *Model) onSelectDetail(msg selectDetailMsg) {
	if msg.err == nil {
		det := msg.det
		m.detail(msg.key).det = &det
	}
	m.openSelect(msg.path, msg.det) // without the detail: WHERE true
}

// openSelect opens the SELECT for the table at path in a new buffer named
// after it, with the cursor on the WHERE condition. det may be empty (the
// detail could not be read).
func (m *Model) openSelect(path api.ObjectPath, det api.TableDetail) {
	det.Path = path // a shard family keeps its family path
	if det.Shard == nil {
		det.Shard = det.Table.Shard
	}
	text, at := sqlx.SelectStar(det)
	b := m.addBuffer(text, "", "")
	b.draftVersion = -1 // write the draft on the next tick
	b.name = m.bufferName(selectName(path))
	row, col := lineCol(text, at)
	b.ed.moveTo(row+1, col+1)
	m.revealPane(ZoneEditor)
	m.setZone(ZoneEditor)
	how := "ctrl+d estimates"
	if m.vimOn() {
		how = "space d estimates"
	}
	m.toast = fmt.Sprintf("new buffer %s: not run, billed when run (%s)", b.name, how)
}

// selectName is what a SELECT buffer is called: the table, or prefix* for a
// shard family.
func selectName(p api.ObjectPath) string {
	if prefix, ok := strings.CutSuffix(p.Table, "YYYYMMDD"); ok {
		return prefix + "*"
	}
	return p.Table
}

// bufferName is name, or name 2, name 3…: the lowest number no other open
// buffer has.
func (m *Model) bufferName(name string) string {
	taken := map[string]bool{}
	for _, b := range m.bufs[:len(m.bufs)-1] { // the new buffer is last
		taken[b.title()] = true
	}
	if !taken[name] {
		return name
	}
	for n := 2; ; n++ {
		if c := fmt.Sprintf("%s %d", name, n); !taken[c] {
			return c
		}
	}
}

// lineCol turns a byte offset into a row and a column in runes, from 0.
func lineCol(text string, at int) (int, int) {
	before := text[:at]
	return strings.Count(before, "\n"), utf8.RuneCountInString(before[strings.LastIndex(before, "\n")+1:])
}
