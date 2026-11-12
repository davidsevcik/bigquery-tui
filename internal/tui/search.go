package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// searchState backs the search overlay, the jump popup and the list style.
type searchState struct {
	in      input
	parsed  api.ParsedQuery
	results []api.SearchResult
	total   int
	recent  bool
	tookMS  float64
	err     string

	cur, off int
	typing   bool

	tablesOnly bool
	all        bool // list everything for an empty query (list style)
	scope      *api.ObjectPath
	expand     bool // next result set replaces a shard family: keep the cursor at the top
	// expandShards lists shard members individually. It is set when a family
	// is opened in the list style and cleared as soon as the filter is edited.
	expandShards bool
	wantEnd      bool
	pending      *api.ObjectPath // select this path once results arrive

	seq    int
	cancel context.CancelFunc
}

func (st *searchState) selection() selection {
	if st.cur < 0 || st.cur >= len(st.results) {
		return selection{}
	}
	r := st.results[st.cur]
	n := api.BrowseNode{Kind: r.Kind, Path: r.Path, Name: r.Name, Type: r.Type, Description: r.Description, Shard: r.Shard, NamesOnly: r.NamesOnly, Stale: r.Stale, SyncedAt: r.SyncedAt, Syncing: r.Syncing}
	if r.Pending {
		n.Status = api.DatasetPending
	}
	return selection{kind: r.Kind, path: r.Path, node: n}
}

func (st *searchState) move(d int) {
	st.cur = clamp(st.cur+d, 0, max(0, len(st.results)-1))
}

func (st *searchState) options(limit, offset int) api.SearchOptions {
	return api.SearchOptions{Limit: limit, Offset: offset, TablesOnly: st.tablesOnly, AllWhenEmpty: st.all, Scope: st.scope, ExpandShards: st.expandShards}
}

// runSearch starts an asynchronous search for the state's current input,
// cancelling the previous one. Results that arrive out of date are dropped.
func (m *Model) runSearch(st *searchState) tea.Cmd {
	return m.searchCmd(st, searchPage, 0, false)
}

func (m *Model) fetchAll(st *searchState) tea.Cmd {
	return m.searchCmd(st, 0, len(st.results), true)
}

func (m *Model) searchCmd(st *searchState, limit, offset int, appendTo bool) tea.Cmd {
	if st.cancel != nil {
		st.cancel()
	}
	query := st.in.value()
	st.parsed = m.be.ParseQuery(query)
	st.seq++
	seq := st.seq
	ctx, cancel := context.WithCancel(m.ctx)
	st.cancel = cancel
	opts := st.options(limit, offset)
	opts.SelectedOnly = true
	return func() tea.Msg {
		resp, err := m.be.Search(ctx, query, opts)
		return searchMsg{st: st, seq: seq, resp: resp, err: err, append: appendTo}
	}
}

func (m *Model) onSearch(msg searchMsg) {
	st := msg.st
	if msg.seq != st.seq {
		return // superseded by a newer keystroke
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return
		}
		st.err = msg.err.Error()
		return
	}
	st.err = ""
	st.total, st.recent, st.tookMS = msg.resp.Total, msg.resp.Recent, msg.resp.TookMS
	if m.o.StableOutput {
		st.tookMS = 0
	}
	if msg.append {
		st.results = append(st.results, msg.resp.Results...)
	} else {
		st.results = msg.resp.Results
		if !st.expand && st.pending == nil {
			st.cur = clamp(st.cur, 0, max(0, len(st.results)-1))
		}
	}
	st.expand = false
	if st.wantEnd {
		st.wantEnd = false
		st.cur = max(0, len(st.results)-1)
	}
	if st.pending != nil {
		for i, r := range st.results {
			if r.Path == *st.pending {
				st.cur = i
				break
			}
		}
		st.pending = nil
	}
	st.cur = clamp(st.cur, 0, max(0, len(st.results)-1))
}

// searchKey handles a key while a search input has the focus; half is half
// the rows its list shows.
func (m *Model) searchKey(st *searchState, msg tea.KeyPressMsg, name string, half int) tea.Cmd {
	// Printable keys type; only non-text keys are looked up in the registry.
	isText := msg.Key().Text != "" && msg.Key().Mod&^tea.ModShift == 0
	if !isText {
		switch keys[ZoneSearch][name] {
		case "down":
			st.move(1)
			return m.maybeMore(st)
		case "up":
			st.move(-1)
			return nil
		case "halfdown":
			st.move(half)
			return m.maybeMore(st)
		case "halfup":
			st.move(-half)
			return nil
		case "detaildown":
			return m.scrollDetail(1)
		case "detailup":
			return m.scrollDetail(-1)
		case "back":
			if st == m.flat {
				st.typing = false
				return nil
			}
			m.ov = ovNone
			return nil
		case "open":
			if st == m.flat {
				st.typing = false
				return nil
			}
			s := st.selection()
			if s.kind == "" {
				if st == m.jump && !m.jumpInsert && st.parsed.Path != nil {
					// a pasted path in a project the catalog does not hold
					return m.openUncached(*st.parsed.Path)
				}
				return nil
			}
			if st == m.jump && m.jumpInsert {
				m.ov = ovNone
				m.insertPath(s)
				return nil
			}
			return m.reveal(s.path, s.kind)
		}
		if a := keys[ZoneGlobal][name]; a != "" && a != "palette" {
			cmd, _ := m.globalAction(a)
			return cmd
		}
	}
	if st.in.handle(msg) {
		st.cur, st.off = 0, 0
		st.expandShards = false
		return m.runSearch(st)
	}
	return nil
}

// maybeMore fetches the rest of the results when the cursor nears the end of
// what is loaded.
func (m *Model) maybeMore(st *searchState) tea.Cmd {
	if st.total > len(st.results) && st.cur >= len(st.results)-20 {
		return m.fetchAll(st)
	}
	return nil
}

// jumpOverlay is Jump to table (ctrl+t): a search of the catalog's tables.
type jumpOverlay struct{}

func (jumpOverlay) frame(m *Model, w, h int) ovFrame {
	bw := clamp(w-8, 30, 96)
	bh := clamp(h-4, 8, 24)
	return ovFrame{x: (w - bw) / 2, y: max(0, (h-bh)/3), lines: m.jumpBox(bw, bh), rows: bh - 4}
}

func (jumpOverlay) key(m *Model, k tea.KeyPressMsg, name string, f ovFrame) tea.Cmd {
	return m.searchKey(m.jump, k, name, max(1, f.rows/2))
}

func (jumpOverlay) hints(*Model) string {
	return joinHints("type to search", selectHint(ZoneSearch), footerHints(ZoneSearch))
}

func (m *Model) jumpBox(bw, bh int) []string {
	inner := bw - 2
	st := m.jump
	b := newRB(false)
	b.add(" ⌕ ", sAcc)
	b.l.WriteString(st.in.render(st.parsed, true, "table name, p:prod, d:events …"))
	b.addR(m.searchInfo(st)+" ", sDim)
	lines := []string{b.line(inner), " " + chips(st.parsed, inner-1, "")}
	lines = append(lines, m.resultsList(st, inner, bh-4, false)...)
	return popup("Jump to table", lines, bw)
}
