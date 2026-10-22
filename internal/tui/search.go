package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// searchState backs the search overlay, the jump popup and the flat layout.
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
	all        bool // list everything for an empty query (flat layout)
	scope      *api.ObjectPath
	expand     bool // next result set replaces a shard family: keep the cursor at the top
	// expandShards lists shard members individually. It is set when a family
	// is opened in the flat layout and cleared as soon as the filter is edited.
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
	n := api.BrowseNode{Kind: r.Kind, Path: r.Path, Name: r.Name, Type: r.Type, Description: r.Description, Shard: r.Shard, NamesOnly: r.NamesOnly, Stale: r.Stale, SyncedAt: r.SyncedAt}
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
		if msg.err == context.Canceled {
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

// searchKey handles a key while a search input has the focus.
func (m *Model) searchKey(st *searchState, msg tea.KeyPressMsg, name string) tea.Cmd {
	half := max(1, m.listHeight()/2)
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
				m.insertPath(s.path)
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
