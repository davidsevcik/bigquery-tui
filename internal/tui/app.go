package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
)

type layoutKind int

const (
	layoutMiller layoutKind = iota
	layoutTree
	layoutFlat
)

var layoutNames = []string{"miller", "tree", "flat"}

type placement int

const (
	placeAuto placement = iota
	placeRight
	placeBottom
	placeHidden
)

var placementNames = []string{"auto", "right", "bottom", "hidden"}

type overlayKind int

const (
	ovNone overlayKind = iota
	ovSearch
	ovJump
	ovPalette
	ovHelp
	ovProblems
	ovSync
)

const (
	previewPage  = 20
	previewMax   = 1000
	previewChars = 200
	searchPage   = 300
)

// Options configures the model.
type Options struct {
	// NoBackground disables watchers, the startup sync and timers (tests).
	NoBackground bool
	// StableOutput hides measured timings so screens are reproducible (tests).
	StableOutput bool
	// Getenv resolves $VISUAL/$EDITOR (default os.Getenv).
	Getenv func(string) string
	Now    func() time.Time
}

// Model is the root Bubble Tea model.
type Model struct {
	be     Backend
	o      Options
	ctx    context.Context
	cancel context.CancelFunc

	w, h int

	layout layoutKind
	place  placement

	mil  miller
	tree tree
	flat *searchState

	ov      overlayKind
	search  *searchState
	jump    *searchState
	pal     input
	palSel  int
	palCmds []command
	ovOff   int

	details map[string]*detailData
	lastSel string
	detOff  int // schema pane scroll
	prevOff int // preview pane scroll
	tab     int
	zoom    bool
	statSeq int

	pendingKey string
	toast      string

	syncRun    SyncRun
	syncing    bool
	syncDone   int
	syncTotal  int
	syncLog    []string
	syncNeeded bool
	syncReason string
	status     *api.CatalogStatus
	cfgErr     string
	kick       chan struct{}
}

// New builds the model.
func New(be Backend, o Options) *Model {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{be: be, o: o, ctx: ctx, cancel: cancel, details: map[string]*detailData{}, kick: make(chan struct{}, 1), w: 100, h: 30}
	m.search = &searchState{typing: true}
	m.jump = &searchState{typing: true, tablesOnly: true}
	m.flat = &searchState{all: true}
	m.tree.open = map[string]bool{}
	m.loadState()
	m.reloadBrowse()
	return m
}

// Close stops background work.
func (m *Model) Close() { m.cancel() }

type uiState struct {
	Layout string `json:"layout"`
	Panes  string `json:"panes"`
}

func (m *Model) loadState() {
	b, err := os.ReadFile(m.be.StatePath())
	if err != nil {
		return
	}
	var st uiState
	if json.Unmarshal(b, &st) != nil {
		return
	}
	for i, n := range layoutNames {
		if n == st.Layout {
			m.layout = layoutKind(i)
		}
	}
	for i, n := range placementNames {
		if n == st.Panes {
			m.place = placement(i)
		}
	}
}

func (m *Model) saveState() {
	p := m.be.StatePath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.Marshal(uiState{Layout: layoutNames[m.layout], Panes: placementNames[m.place]})
	_ = os.WriteFile(p, b, 0o600)
}

// --- messages

type (
	detailMsg struct {
		key string
		det api.TableDetail
		err error
	}
	statsTickMsg struct {
		key string
		seq int
	}
	statsMsg struct {
		key   string
		stats api.TableStats
		err   error
	}
	previewMsg struct {
		key  string
		page api.PreviewPage
		err  error
	}
	searchMsg struct {
		st     *searchState
		seq    int
		resp   api.SearchResponse
		err    error
		append bool
	}
	syncStartedMsg struct {
		run  SyncRun
		err  error
		auto bool
	}
	syncEventMsg struct {
		ev api.SyncEvent
		ok bool
	}
	syncDoneMsg struct {
		res api.SyncResult
		err error
	}
	reconcileMsg struct {
		needed    bool
		reason    string
		err       error
		thenStart bool
	}
	statusMsg struct {
		st  api.CatalogStatus
		err error
	}
	editorDoneMsg struct{ err error }
)

// Init starts the background work.
func (m *Model) Init() tea.Cmd {
	if m.o.NoBackground {
		return tea.Batch(m.statusCmd(), m.runSearchIfFlat())
	}
	return tea.Batch(m.runSearchIfFlat(), m.reconcileCmd(true), m.statusCmd(), m.waitCatalog(m.be.WatchCatalog(m.ctx)),
		m.waitConfig(m.be.WatchConfig(m.ctx, m.kick)), func() tea.Msg { _ = m.be.WarmSearch(m.ctx); return nil })
}

func (m *Model) statusCmd() tea.Cmd {
	return func() tea.Msg {
		st, err := m.be.Status(m.ctx)
		return statusMsg{st, err}
	}
}

func (m *Model) reconcileCmd(thenStart bool) tea.Cmd {
	return func() tea.Msg {
		needed, reason, err := m.be.Reconcile(m.ctx)
		return reconcileMsg{needed, reason, err, thenStart}
	}
}

func (m *Model) waitCatalog(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-ch
		if !ok {
			return nil
		}
		return catalogWait{ch}
	}
}

type catalogWait struct{ ch <-chan struct{} }

func (m *Model) waitConfig(ch <-chan config.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return configWait{ev, ch}
	}
}

type configWait struct {
	ev config.Event
	ch <-chan config.Event
}

func (m *Model) startSync(scope api.SyncScope, auto bool) tea.Cmd {
	if m.syncing {
		m.toast = "a sync is already running (gs shows progress)"
		return nil
	}
	return func() tea.Msg {
		var run SyncRun
		var err error
		if auto {
			run, err = m.be.AutoRefresh(m.ctx)
		} else {
			run, err = m.be.StartSync(m.ctx, scope)
		}
		return syncStartedMsg{run, err, auto}
	}
}

func waitSync(run SyncRun) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-run.Events()
		if !ok {
			res, err := run.Wait()
			return syncDoneMsg{res, err}
		}
		return syncEventMsg{ev, true}
	}
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.FocusMsg:
		select {
		case m.kick <- struct{}{}:
		default:
		}
	case tea.PasteMsg:
		if st := m.typingState(); st != nil {
			st.in.insert(strings.ReplaceAll(msg.Content, "\n", " "))
			return m, m.runSearch(st)
		}
	case tea.KeyPressMsg:
		cmd := m.key(msg)
		return m, tea.Batch(cmd, m.afterSelect())
	case searchMsg:
		m.onSearch(msg)
		return m, m.afterSelect()
	case detailMsg:
		d := m.detail(msg.key)
		if msg.err != nil {
			d.detErr = msg.err.Error()
		} else {
			det := msg.det
			d.det = &det
		}
	case statsTickMsg:
		if msg.seq == m.statSeq && msg.key == m.lastSel {
			return m, m.statsCmd(msg.key)
		}
	case statsMsg:
		d := m.detail(msg.key)
		if msg.err != nil {
			d.statsErr = true
		} else {
			st := msg.stats
			d.stats = &st
		}
	case previewMsg:
		m.onPreview(msg)
	case syncStartedMsg:
		if msg.err != nil {
			m.toast = "sync: " + msg.err.Error()
			return m, nil
		}
		if msg.run == nil {
			return m, nil
		}
		m.syncRun, m.syncing, m.syncDone, m.syncTotal, m.syncLog = msg.run, true, 0, 0, nil
		return m, waitSync(msg.run)
	case syncEventMsg:
		m.onSyncEvent(msg.ev)
		return m, waitSync(m.syncRun)
	case syncDoneMsg:
		m.syncing, m.syncRun = false, nil
		switch {
		case msg.err != nil:
			m.toast = "sync failed: " + msg.err.Error()
		case msg.res.Cancelled:
			m.toast = "sync cancelled (finished units were kept)"
		default:
			m.toast = fmt.Sprintf("sync finished: %d ok, %d degraded, %d failed", msg.res.UnitsOK, msg.res.UnitsDegraded, msg.res.UnitsFailed)
		}
		m.reloadBrowse()
		// The config may have changed while the sync held the lock (the
		// reconcile was skipped then), so apply it now. It is a no-op otherwise.
		return m, tea.Batch(m.reconcileCmd(false), m.rerunSearches(), m.afterSelect())
	case catalogWait:
		m.reloadBrowse()
		m.details = map[string]*detailData{}
		m.lastSel = ""
		return m, tea.Batch(m.waitCatalog(msg.ch), m.statusCmd(), m.rerunSearches(), m.afterSelect())
	case configWait:
		cmd := m.onConfig(msg.ev)
		return m, tea.Batch(cmd, m.waitConfig(msg.ch))
	case reconcileMsg:
		if msg.err == nil {
			m.syncNeeded, m.syncReason = msg.needed, msg.reason
			m.reloadBrowse()
		}
		if msg.thenStart && !m.o.NoBackground {
			return m, tea.Batch(m.startSync(api.SyncScope{}, true), m.statusCmd())
		}
		return m, m.statusCmd()
	case statusMsg:
		if msg.err == nil {
			st := msg.st
			m.status = &st
			if !m.syncing {
				m.syncNeeded, m.syncReason = st.SyncNeeded, st.SyncReason
			}
		}
	case editorDoneMsg:
		if msg.err != nil {
			m.toast = "editor: " + msg.err.Error()
		}
		select {
		case m.kick <- struct{}{}:
		default:
		}
	}
	return m, nil
}

func (m *Model) onSyncEvent(ev api.SyncEvent) {
	m.syncDone, m.syncTotal = ev.UnitsDone, ev.UnitsTotal
	where := ev.Project
	if ev.Location != "" {
		where += "/" + ev.Location
	}
	var line string
	switch ev.Kind {
	case api.EventRunStarted:
		line = "started: " + ev.Message
	case api.EventDiscovered:
		line = "found " + ev.Message
	case api.EventUnitStarted:
		line = fmt.Sprintf("%s: reading %d datasets", where, ev.Datasets)
	case api.EventUnitDone:
		line = fmt.Sprintf("%s: %d datasets, %d tables (%s)", where, ev.Datasets, ev.Tables, ev.Status)
		m.reloadBrowse()
	case api.EventUnitFailed:
		line = fmt.Sprintf("%s: FAILED: %s", where, ev.Error)
	case api.EventWarning:
		line = "warning: " + ev.Project + " " + ev.Message
	case api.EventRunFinished:
		line = "finished: " + ev.Message
	}
	if line != "" {
		m.syncLog = append(m.syncLog, ev.Time.Format("15:04:05")+"  "+line)
	}
}

func (m *Model) onConfig(ev config.Event) tea.Cmd {
	if ev.Err != nil {
		m.cfgErr = ev.Err.Error()
		return nil
	}
	m.cfgErr = ""
	m.be.SetConfig(ev.Config)
	if len(ev.RestartRequired) > 0 {
		m.toast = "config: " + strings.Join(ev.RestartRequired, ", ") + " changed: restart bqt to apply"
	} else if len(ev.Changed) > 0 {
		m.toast = "config reloaded: " + strings.Join(ev.Changed, ", ")
	}
	m.reloadBrowse()
	for _, k := range ev.Changed {
		if k == "projects" || k == "ignore" {
			return m.reconcileCmd(false)
		}
	}
	return m.statusCmd()
}

// --- keys

func keyName(msg tea.KeyPressMsg) string {
	k := msg.Key()
	if k.Text != "" && k.Mod&^tea.ModShift == 0 && k.Text != " " {
		return k.Text
	}
	return msg.String()
}

func (m *Model) typingState() *searchState {
	switch {
	case m.ov == ovSearch:
		return m.search
	case m.ov == ovJump:
		return m.jump
	case m.ov == ovNone && m.layout == layoutFlat && m.flat.typing:
		return m.flat
	}
	return nil
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	name := keyName(msg)
	m.toast = ""

	if st := m.typingState(); st != nil {
		return m.searchKey(st, msg, name)
	}
	if m.ov == ovPalette {
		return m.paletteKey(msg, name)
	}
	zone := ZoneExplorer
	if m.ov != ovNone {
		zone = ZoneOverlay
	}
	if m.pendingKey != "" {
		name = m.pendingKey + " " + name
		m.pendingKey = ""
	} else if keys.chordPrefix(zone, name) {
		m.pendingKey = name
		return nil
	}
	act := keys.action(zone, name)
	if m.ov != ovNone {
		return m.overlayAction(act)
	}
	return m.explorerAction(act)
}

func (m *Model) globalAction(act string) (tea.Cmd, bool) {
	switch act {
	case "quit":
		m.cancel()
		return tea.Quit, true
	case "palette":
		m.ov, m.palSel = ovPalette, 0
		m.pal.set("")
		m.filterPalette()
		return nil, true
	case "jump":
		m.ov = ovJump
		m.jump.in.set("")
		m.jump.cur, m.jump.off = 0, 0
		return m.runSearch(m.jump), true
	case "help":
		m.ov, m.ovOff = ovHelp, 0
		return nil, true
	case "cancel":
		if m.syncing && m.syncRun != nil {
			m.syncRun.Cancel()
			m.toast = "cancelling the sync…"
		} else {
			m.toast = "nothing is running (ctrl+q quits)"
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) overlayAction(act string) tea.Cmd {
	half := max(1, (m.h-6)/2)
	switch act {
	case "back", "open":
		m.ov = ovNone
	case "down":
		m.ovOff++
	case "up":
		m.ovOff = max(0, m.ovOff-1)
	case "halfdown":
		m.ovOff += half
	case "halfup":
		m.ovOff = max(0, m.ovOff-half)
	default:
		if cmd, ok := m.globalAction(act); ok {
			return cmd
		}
	}
	return nil
}

func (m *Model) explorerAction(act string) tea.Cmd {
	if cmd, ok := m.globalAction(act); ok {
		return cmd
	}
	page := max(1, m.listHeight()/2)
	switch act {
	case "down":
		m.move(1)
		return m.moreIfFlat()
	case "up":
		m.move(-1)
	case "halfdown":
		m.move(page)
		return m.moreIfFlat()
	case "halfup":
		m.move(-page)
	case "top":
		m.move(-1 << 30)
	case "bottom":
		return m.bottom()
	case "left":
		m.left()
	case "right":
		return m.right()
	case "open":
		return m.open()
	case "back":
		return m.back()
	case "search":
		if m.layout == layoutFlat {
			m.flat.typing = true
			return nil
		}
		m.ov = ovSearch
		return m.runSearch(m.search)
	case "layout":
		m.setLayout((m.layout + 1) % 3)
		m.saveState()
		return m.runSearchIfFlat()
	case "panes":
		m.place = (m.place + 1) % 4
		m.saveState()
		m.toast = "detail pane: " + placementNames[m.place]
	case "tab":
		m.tab = 1 - m.tab
	case "zoom":
		m.zoom = !m.zoom
	case "detaildown":
		m.scrollDetail(1)
	case "detailup":
		m.scrollDetail(-1)
	case "preview":
		return m.previewMore()
	case "scope":
		if m.layout == layoutFlat {
			m.cycleScope()
			return m.runSearch(m.flat)
		}
	case "copy", "copyquoted":
		s := m.sel()
		if s.kind == "" {
			return nil
		}
		text := s.path.String()
		if act == "copyquoted" {
			text = "`" + text + "`"
		}
		m.toast = "copied " + text
		return tea.SetClipboard(text)
	case "refresh":
		s := m.sel()
		if s.path.Project == "" {
			return nil
		}
		return m.startSync(api.SyncScope{Projects: []string{s.path.Project}}, false)
	case "refreshall":
		return m.startSync(api.SyncScope{}, false)
	case "problems":
		m.ov, m.ovOff = ovProblems, 0
	case "syncdetails":
		m.ov, m.ovOff = ovSync, 1<<30
	case "config":
		return m.openConfig()
	}
	return nil
}

func (m *Model) openConfig() tea.Cmd {
	path := m.be.ConfigPath()
	argv := append(config.ResolveEditor(m.o.Getenv, runtime.GOOS), path)
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return editorDoneMsg{err} })
}

// scrollDetail scrolls the pane that tab last selected: the schema, or the
// preview once rows are loaded (p switches to it).
func (m *Model) scrollDetail(d int) {
	if m.tab == 1 {
		m.prevOff = max(0, m.prevOff+d)
		return
	}
	m.detOff = max(0, m.detOff+d)
}

func (m *Model) moreIfFlat() tea.Cmd {
	if m.layout == layoutFlat {
		return m.maybeMore(m.flat)
	}
	return nil
}

// back implements "esc backs out exactly one level".
func (m *Model) back() tea.Cmd {
	switch {
	case m.zoom:
		m.zoom = false
	case m.layout == layoutFlat && m.flat.in.value() != "":
		m.flat.in.set("")
		m.flat.cur, m.flat.off = 0, 0
		m.flat.expandShards = false
		return m.runSearch(m.flat)
	case m.layout == layoutMiller && m.mil.family != nil:
		m.milCollapseFamily()
	}
	return nil
}

// --- palette

func (m *Model) filterPalette() {
	q := strings.ToLower(m.pal.value())
	m.palCmds = m.palCmds[:0]
	for _, c := range paletteCommands() {
		if q == "" || fuzzyContains(strings.ToLower(c.name), q) {
			m.palCmds = append(m.palCmds, c)
		}
	}
	m.palSel = clamp(m.palSel, 0, max(0, len(m.palCmds)-1))
}

func fuzzyContains(s, q string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && byte(r) == q[i] {
			i++
		}
	}
	return i == len(q)
}

func (m *Model) paletteKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	switch keys.action(ZoneSearch, name) {
	case "back":
		m.ov = ovNone
		return nil
	case "down":
		m.palSel = min(m.palSel+1, max(0, len(m.palCmds)-1))
		return nil
	case "up":
		m.palSel = max(0, m.palSel-1)
		return nil
	case "open":
		m.ov = ovNone
		if m.palSel < len(m.palCmds) {
			return m.explorerAction(m.palCmds[m.palSel].action)
		}
		return nil
	}
	if m.pal.handle(msg) {
		m.palSel = 0
		m.filterPalette()
	}
	return nil
}

// --- detail data

type detailData struct {
	det      *api.TableDetail
	detErr   string
	stats    *api.TableStats
	statsErr bool
	prev     *api.PreviewPage
	prevErr  string
	prevBusy bool
	prevEnd  bool
}

func (m *Model) detail(key string) *detailData {
	d := m.details[key]
	if d == nil {
		if len(m.details) > 64 {
			m.details = map[string]*detailData{}
		}
		d = &detailData{}
		m.details[key] = d
	}
	return d
}

// afterSelect loads what the detail pane needs when the selection changed.
func (m *Model) afterSelect() tea.Cmd {
	s := m.sel()
	key := s.detailKey()
	if key == m.lastSel {
		return nil
	}
	m.lastSel, m.detOff, m.prevOff = key, 0, 0
	if key == "" {
		return nil
	}
	d := m.detail(key)
	var cmds []tea.Cmd
	if d.det == nil && d.detErr == "" {
		p := s.path
		cmds = append(cmds, func() tea.Msg {
			det, err := m.be.TableDetail(m.ctx, p)
			return detailMsg{key, det, err}
		})
	}
	if d.stats == nil && !d.statsErr {
		m.statSeq++
		seq := m.statSeq
		if m.o.NoBackground {
			cmds = append(cmds, m.statsCmd(key))
		} else {
			cmds = append(cmds, tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return statsTickMsg{key, seq} }))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) statsCmd(key string) tea.Cmd {
	p, ok := pathFromKey(key)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		st, err := m.be.TableStats(m.ctx, p)
		return statsMsg{key, st, err}
	}
}

func (m *Model) previewMore() tea.Cmd {
	s := m.sel()
	key := s.detailKey()
	if key == "" {
		m.toast = "select a table to preview"
		return nil
	}
	d := m.detail(key)
	if d.prevBusy || d.prevEnd {
		return nil
	}
	var start int64
	if d.prev != nil {
		start = int64(len(d.prev.Rows))
	}
	if start >= previewMax {
		m.toast = fmt.Sprintf("preview stops at %d rows: run a query for more", previewMax)
		return nil
	}
	d.prevBusy = true
	m.tab = 1
	p := s.path
	return func() tea.Msg {
		page, err := m.be.Preview(m.ctx, p, start, previewPage, previewChars)
		return previewMsg{key, page, err}
	}
}

func (m *Model) onPreview(msg previewMsg) {
	d := m.detail(msg.key)
	d.prevBusy = false
	if msg.err != nil {
		if errors.Is(msg.err, bq.ErrNeedsQuery) {
			d.prevErr = "needs a query: views and external tables cannot be read directly"
		} else {
			d.prevErr = msg.err.Error()
		}
		return
	}
	d.prevErr = ""
	if d.prev == nil {
		page := msg.page
		d.prev = &page
	} else {
		d.prev.Rows = append(d.prev.Rows, msg.page.Rows...)
		d.prev.TotalRows = msg.page.TotalRows
	}
	// A page can be shorter than requested when rows are wide (response size
	// limit), so only an empty page or the reported total ends the preview.
	if len(msg.page.Rows) == 0 || int64(len(d.prev.Rows)) >= d.prev.TotalRows {
		d.prevEnd = true
	}
}

func pathFromKey(key string) (api.ObjectPath, bool) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 3 {
		return api.ObjectPath{}, false
	}
	return api.ObjectPath{Project: parts[0], Dataset: parts[1], Table: parts[2]}, true
}

// View renders the screen.
func (m *Model) View() tea.View {
	v := tea.NewView(strings.Join(m.render(), "\n"))
	v.AltScreen = true
	v.ReportFocus = true
	return v
}
