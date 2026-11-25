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

type sample struct {
	key  string
	path api.ObjectPath
	name string
	g    *grid
	err  string
	seq  int

	prev     *sample
	prevShow bool
}

type sampleMsg struct {
	seq  int
	page api.PreviewPage
	err  error
}

func (m *Model) sampleShown() bool { return m.showSample && m.sample != nil }

func (m *Model) showRun() { m.showSample = false }

func (m *Model) revealPane(z Zone) {
	m.uncover(z)
	if a := m.arr(); z != m.zone && a.bar != nil && slices.Contains(a.barPanes, z) && !a.single && a.zoom == "" && !a.offLayout {
		m.mru = append([]Zone{m.mru[0], z}, slices.DeleteFunc(slices.Clone(m.mru[1:]), func(o Zone) bool { return o == z })...)
	}
	if _, ok := m.arr().rects[z]; !ok {
		m.setZone(z)
	}
}

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
	mviewNoSample    = "materialized views have no free sample; press s for a SELECT (billed)"
)

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
	case api.TypeMView:
		m.toast = mviewNoSample
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
		sm.prev.prev = nil
	}
	if s.kind == api.KindShards && s.node.Shard != nil {
		sm.path.Table = s.node.Shard.Newest
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
		return nil
	}
	switch {
	case errors.Is(msg.err, bq.ErrNeedsQuery):
		m.toast = viewNoSample
		prev := sm.prev
		if prev != nil && prev.g == nil && prev.err == "" {
			prev = nil
		}
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

func (m *Model) sampleTitle() string {
	t := " Sample · free read, no query job · " + m.sample.name
	if g := m.sample.g; g != nil {
		t += " · " + format.Count(g.total) + " rows"
	}
	return t
}

func (m *Model) switchSource() tea.Cmd {
	if m.sample == nil {
		m.toast = "no Sample: p on a table loads one"
		return nil
	}
	m.showSample = !m.showSample
	return m.gridEnsure()
}

type selectDetailMsg struct {
	path api.ObjectPath
	key  string
	det  api.TableDetail
	err  error
}

func (m *Model) syncTableAction() tea.Cmd {
	s := m.sel()
	if s.detailKey() == "" {
		m.toast = "select a table to sync its schema"
		return nil
	}
	p := s.path
	return m.startSync(api.SyncScope{Table: &p}, false)
}

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
	m.openSelect(msg.path, msg.det)
}

func (m *Model) openSelect(path api.ObjectPath, det api.TableDetail) {
	det.Path = path
	if det.Shard == nil {
		det.Shard = det.Table.Shard
	}
	text, at := sqlx.SelectStar(det)
	b := m.addBuffer(text, "", "")
	b.draftVersion = -1
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

func selectName(p api.ObjectPath) string {
	return strings.Replace(p.Table, "YYYYMMDD", "*", 1)
}

func (m *Model) bufferName(name string) string {
	taken := map[string]bool{}
	for _, b := range m.bufs[:len(m.bufs)-1] {
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

func lineCol(text string, at int) (int, int) {
	before := text[:at]
	return strings.Count(before, "\n"), utf8.RuneCountInString(before[strings.LastIndex(before, "\n")+1:])
}
