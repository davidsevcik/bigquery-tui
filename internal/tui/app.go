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
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/format"
	"github.com/davidsevcik/bigquery-tui/internal/runs"
)

type explorerStyle int

const (
	styleColumns explorerStyle = iota
	styleTree
	styleList
)

var styleNames = []string{"columns", "tree", "list"}

type overlayKind int

const (
	ovNone overlayKind = iota
	ovJump
	ovPalette
	ovHelp
	ovProblems
	ovSync
	ovRecord
	ovRuns
	ovBuffers
	ovProjects
	ovPrompt
	ovConfirm
	ovPicker
	ovHandoff
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
	// JobHeld reports that a test holds a job running on purpose (tests, with
	// NoBackground): the TUI stops waiting for that Run instead of timing out.
	JobHeld func(jobID string) bool
	// Getenv resolves $VISUAL/$EDITOR (default os.Getenv).
	Getenv func(string) string
	Now    func() time.Time
	// Location is where times are shown (default the local zone).
	Location *time.Location
	// File is a .sql file to open as the first buffer (bqt path/to/query.sql).
	File string
}

// Model is the root Bubble Tea model.
type Model struct {
	be     Backend
	o      Options
	ctx    context.Context
	cancel context.CancelFunc

	w, h  int
	termH int // the terminal's rows; h leaves out the tutorial panel

	style     explorerStyle       // how the Explorer draws the catalog
	preset    string              // the chosen Layout preset (classic, focus)
	maxed     Zone                // the maximised Pane ("" for none); session only
	mru       []Zone              // the Panes, most recently focused first
	pd        *projDialog         // the Projects dialog while open
	listed    []api.ListedProject // BigQuery's projects list, once fetched
	selSeen   string              // the selection last applied (see tick)
	autoProjs bool                // nothing selected at start: open the dialog

	hoItems []handoffItem // the hand-off popup's entries
	hoTop   int           // first entry line in the popup

	mil  miller
	tree tree
	flat *searchState

	ov      overlayKind
	jump    *searchState
	pal     input
	palSel  int
	palCmds []command
	ovOff   int

	details map[string]*detailData
	lastSel string
	detOff  int // schema pane scroll
	prevOff int // preview pane scroll
	prevRow int // preview row cursor (the row J shows)
	rec     *recordData
	tab     int // the Schema pane's half: 0 schema, 1 preview
	statSeq int

	tut      *tutorial
	firstRun bool // no UI state file yet

	pk          *picker  // Favorites and History; kept while closed, so the search survives
	pendingLoad *loadReq // a pick waiting for the "replace the buffer?" answer

	pendingKey string
	toast      string

	// query side: buffers, Runs and their result grids
	zone       Zone
	bufs       []*buffer
	cur        int
	bufSeq     int
	orphans    []orphan
	comp       completion
	runs       *runs.Follower // the Runs as this model last applied them
	grids      map[int64]*grid
	prompt     promptState
	confirm    confirmState
	confirmQ   []confirmState // questions waiting for the open popup to close
	draftErr   string         // the last draft write failed with this (retried every tick)
	ovSel      int
	exp        Export
	expEv      api.ExportEvent
	jumpInsert bool // jump-to-table was opened from the editor: enter inserts the name
	edX, edY   int  // where the editor drew its cursor (for the completion popup)

	syncRun    SyncRun
	syncing    bool
	syncDone   int
	syncTotal  int
	syncLog    []string
	syncNeeded bool
	syncReason string
	syncScope  []string        // the projects the running sync covers (nil: the Selected ones)
	syncLater  []string        // projects to sync once the running sync ends
	forceLater []string        // projects to sync once it ends, however fresh (s in the Projects dialog)
	forceAll   bool            // every Selected project too, however fresh (S)
	openAfter  *api.ObjectPath // a pasted path to open once its project's sync ends
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
	if o.Location == nil {
		o.Location = time.Local
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{be: be, o: o, ctx: ctx, cancel: cancel, details: map[string]*detailData{}, kick: make(chan struct{}, 1), w: 100, h: 30, termH: 30, preset: presetClassic}
	m.jump = &searchState{typing: true, tablesOnly: true}
	m.flat = &searchState{all: true}
	m.tree.open = map[string]bool{}
	m.zone, m.mru = ZoneExplorer, slices.Clone(paneOrder)
	m.runs, m.grids = be.FollowRuns(), map[int64]*grid{}
	m.loadState()
	m.reloadBrowse()
	m.selSeen = selectionKey(be)
	m.autoProjs = len(be.Selection().Selected) == 0
	if o.File != "" {
		if err := m.openFile(o.File); err != nil {
			m.toast = "open " + o.File + ": " + err.Error()
		} else {
			m.setZone(ZoneEditor)
		}
	}
	if len(m.bufs) == 0 {
		m.addBuffer("", "", "")
	}
	m.scanOrphans()
	if n := len(m.orphans); n > 0 && m.toast == "" {
		m.toast = fmt.Sprintf("%d draft(s) from earlier sessions can be restored (ctrl+x b in the editor)", n)
	}
	if m.firstRun && m.toast == "" && !o.NoBackground && !m.autoProjs {
		m.toast = "new to bqt? ctrl+g starts a short tutorial of the navigation"
		m.saveState() // the hint shows once
	}
	return m
}

// Close stops background work and releases the drafts.
func (m *Model) Close() {
	m.releaseBuffers()
	m.cancel()
}

// uiState is state/tui.json. Older bqt instances share the file and read
// "layout" as the Explorer style (miller, tree, flat), so it is read but never
// written, and the new keys are separate.
type uiState struct {
	ExplorerStyle string `json:"explorer_style"`
	// PaneLayout is an object so that a custom tree can join the preset later;
	// it is decoded on its own, so a bad value does not lose the style.
	PaneLayout json.RawMessage `json:"pane_layout,omitempty"`
	Layout     string          `json:"layout,omitempty"` // legacy: the Explorer style
}

type paneLayoutState struct {
	Preset string `json:"preset"`
}

// legacyStyles maps the old "layout" values to Explorer styles.
var legacyStyles = map[string]explorerStyle{"miller": styleColumns, "tree": styleTree, "flat": styleList}

func parseStyle(s string) (explorerStyle, bool) {
	for i, n := range styleNames {
		if n == s {
			return explorerStyle(i), true
		}
	}
	st, ok := legacyStyles[s]
	return st, ok
}

func (m *Model) loadState() {
	b, err := os.ReadFile(m.be.StatePath())
	if err != nil {
		m.firstRun = errors.Is(err, os.ErrNotExist)
		return
	}
	var st uiState
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if s, ok := parseStyle(st.ExplorerStyle); ok {
		m.style = s
	} else if s, ok := parseStyle(st.Layout); ok {
		m.style = s
	}
	var pl paneLayoutState
	if json.Unmarshal(st.PaneLayout, &pl) == nil && knownPreset(pl.Preset) {
		m.preset = pl.Preset
	}
}

// saveState writes the chosen Explorer style and Layout preset; the
// step-down and the maximised Pane are never saved.
func (m *Model) saveState() {
	p := m.be.StatePath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	pl, _ := json.Marshal(paneLayoutState{Preset: m.preset})
	b, _ := json.Marshal(uiState{ExplorerStyle: styleNames[m.style], PaneLayout: pl})
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
	var open tea.Cmd
	if m.autoProjs {
		// nothing selected yet: the Projects dialog comes first
		m.autoProjs = false
		open = m.openProjects()
		m.pd.auto = m.firstRun
	}
	if m.o.NoBackground {
		return tea.Batch(open, m.statusCmd(), m.runSearchIfFlat())
	}
	return tea.Batch(open, m.runSearchIfFlat(), m.reconcileCmd(true), m.statusCmd(), m.waitCatalog(m.be.WatchCatalog(m.ctx)),
		m.waitConfig(m.be.WatchConfig(m.ctx, m.kick)), func() tea.Msg { _ = m.be.WarmSearch(m.ctx); return nil },
		m.learnPricing(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// learnPricing asks BigQuery, with a free dry-run, how jobs are billed.
func (m *Model) learnPricing() tea.Cmd {
	return func() tea.Msg {
		_ = m.be.LearnPricing(m.ctx)
		return pricingMsg{}
	}
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
	m.syncScope = scope.Projects // nil: every Selected project (or the stale ones)
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
	mod, cmd := m.update(msg)
	m.mil.col = max(m.mil.col, m.milFirst())
	m.dropDeeperFilters()
	m.tutorialObserve()
	return mod, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.termH = msg.Width, msg.Height
		m.fitHeight()
		return m, m.gridEnsure() // a Results pane that grew shows more rows
	case tea.FocusMsg:
		select {
		case m.kick <- struct{}{}:
		default:
		}
	case tea.PasteMsg:
		if m.ov == ovPrompt {
			m.prompt.fields[m.prompt.idx].insert(strings.ReplaceAll(msg.Content, "\n", " "))
			return m, nil
		}
		if st := m.typingState(); st != nil {
			st.in.insert(strings.ReplaceAll(msg.Content, "\n", " "))
			return m, m.runSearch(st)
		}
		if f := m.typingFilter(); f != nil {
			f.in.insert(strings.ReplaceAll(msg.Content, "\n", " "))
			f.best = true
			return m, m.refilter(f)
		}
		if m.zone == ZoneEditor && m.ov == ovNone {
			m.buf().ed.insert(msg.Content) // verbatim: a tab inside a string literal matters
			return m, m.afterEdit(false)
		}
	case runMsg:
		return m, m.onRun(msg)
	case pageMsg:
		m.onPage(msg)
	case histMsg:
		m.onHistory(msg)
	case reopenMsg:
		return m, m.onReopen(msg)
	case exportMsg:
		return m, m.onExport(msg)
	case pricingMsg:
		// the status line reads the pricing model live: redraw
	case tickMsg:
		m.saveDrafts()
		return m, tea.Batch(tick(), m.selectionChanged())
	case projListMsg:
		m.onProjectList(msg)
	case handoffMsg:
		return m, m.onHandoff(msg)
	case clipMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			return m, nil
		}
		m.toast = msg.toast
		return m, tea.SetClipboard(msg.text)
	case extEditMsg:
		m.onExtEdit(msg)
	case tea.KeyPressMsg:
		cmd := m.key(msg)
		m.nextConfirm()
		return m, tea.Batch(cmd, m.afterSelect())
	case searchMsg:
		m.onSearch(msg)
		return m, m.afterSelect()
	case filterMsg:
		m.onFilter(msg)
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
	case recordMsg:
		m.onRecord(msg)
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
		if msg.ev.Kind == api.EventUnitDone || msg.ev.Kind == api.EventUnitFailed {
			m.refreshProjects() // the dialog's rows follow the sync
		}
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
		m.refreshProjects()
		// The config may have changed while the sync held the lock (the
		// reconcile was skipped then), so apply it now. It is a no-op otherwise.
		return m, tea.Batch(m.reconcileCmd(false), m.rerunSearches(), m.afterSelect(), m.afterSync())
	case catalogWait:
		m.reloadBrowse()
		m.refreshProjects()
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
	relearn := m.be.Config().Location != ev.Config.Location
	m.be.SetConfig(ev.Config) // forgets the pricing model when it may differ
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
	if relearn && !m.o.NoBackground {
		return tea.Batch(m.statusCmd(), m.learnPricing())
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
	case m.ov == ovJump:
		return m.jump
	case m.ov == ovNone && m.zone == ZoneExplorer && m.style == styleList && m.flat.typing:
		return m.flat
	}
	return nil
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	name := keyName(msg)
	m.toast = ""
	if m.tutorialKey(name) {
		return m.gridEnsure() // the panel takes or gives back rows
	}

	switch m.ov {
	case ovPrompt:
		return m.promptKey(msg, name)
	case ovConfirm:
		return m.confirmKey(name)
	case ovPicker:
		return m.pickerKey(msg, name)
	}
	if st := m.typingState(); st != nil {
		return m.searchKey(st, msg, name)
	}
	if f := m.typingFilter(); f != nil {
		return m.filterKey(f, msg, name)
	}
	if m.ov == ovPalette {
		return m.paletteKey(msg, name)
	}
	if m.ov == ovProjects && m.pd != nil && m.pd.typing {
		return m.projectsKey(msg, name)
	}
	zone := m.zone
	if m.ov != ovNone {
		zone = ZoneOverlay
	}
	if zone == ZoneEditor {
		return m.editorKey(msg, name)
	}
	if m.pendingKey != "" {
		name = m.pendingKey + " " + name
		m.pendingKey = ""
	} else if keys.chordPrefix(zone, name) {
		m.pendingKey = name
		return nil
	}
	act := keys.action(zone, name)
	switch {
	case m.ov == ovRuns:
		return m.runsOverlayAction(act, name)
	case m.ov == ovBuffers:
		return m.buffersOverlayAction(act)
	case m.ov == ovProjects:
		return m.projectsOverlayAction(act)
	case m.ov == ovHandoff:
		return m.handoffOverlayAction(act)
	case m.ov != ovNone:
		return m.overlayAction(act)
	case zone == ZoneResults:
		return m.resultsAction(act)
	case zone == ZoneSchema:
		return m.schemaAction(act)
	}
	return m.explorerAction(act)
}

func (m *Model) globalAction(act string) (tea.Cmd, bool) {
	if cmd, ok := m.paneAction(act); ok {
		m.ov = ovNone
		return cmd, true
	}
	switch act {
	case "quit":
		var why []string
		if n := len(m.activeRuns()); n > 0 {
			why = append(why, fmt.Sprintf("%d run(s) are still in flight and will be cancelled.", n))
		}
		var unsaved []string
		for _, b := range m.bufs {
			if b.modified() {
				unsaved = append(unsaved, b.title())
			}
		}
		if len(unsaved) > 0 {
			why = append(why, "Unsaved changes will be lost in: "+strings.Join(unsaved, ", ")+" (ctrl+s in the editor saves).")
		}
		if len(why) > 0 {
			m.ov = ovNone
			m.askConfirm(confirmState{kind: "quit", lines: append(why, "Quit anyway?")})
			return nil, true
		}
		return m.quit(), true
	case "tutorial":
		m.startTutorial()
		return m.gridEnsure(), true
	case "projects":
		return m.openProjects(), true
	case "favorites":
		return m.openPicker(scopeFavorites), true
	case "history":
		return m.openPicker(scopeHistory), true
	case "handoff":
		m.openHandoff()
		return nil, true
	case "hoschemamd", "hoschemajson", "hoerror", "hosamplemd", "hosampletsv", "hosamplendjson", "horun":
		m.ov = ovNone
		return m.handoffDirect(act), true
	case "cancelall":
		m.runs.CancelAll()
		m.toast = "cancel requested for every run"
		return nil, true
	case "palette":
		m.ov, m.palSel = ovPalette, 0
		m.pal.set("")
		m.filterPalette()
		return nil, true
	case "jump":
		m.jumpInsert = m.zone == ZoneEditor && m.ov == ovNone
		m.ov = ovJump
		m.jump.in.set("")
		m.jump.cur, m.jump.off = 0, 0
		return m.runSearch(m.jump), true
	case "help":
		m.ov, m.ovOff = ovHelp, 0
		return nil, true
	case "cancel":
		if m.cancelFocused() {
			return nil, true
		}
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
	case "next", "prev":
		if m.ov == ovRecord {
			if act == "prev" {
				return m.recordStep(-1)
			}
			return m.recordStep(1)
		}
	case "copy":
		if m.ov == ovRecord && m.rec != nil && m.rec.cells != nil {
			m.toast = "copied the record as JSON"
			return tea.SetClipboard(format.RowJSON(m.rec.cols, m.rec.cells))
		}
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
	case "filter":
		m.setZone(ZoneExplorer) // the palette runs it from any zone
		if m.style == styleList {
			m.flat.typing = true
			return nil
		}
		if f := m.styleFilter(); f != nil {
			f.typing = true
		}
		return nil
	case "style":
		m.setStyle((m.style + 1) % 3)
		m.saveState()
		return m.runSearchIfFlat()
	case "tab":
		m.tab = 1 - m.tab
	case "zoom":
		m.focusSchema(true)
	case "detaildown":
		return m.scrollDetail(1)
	case "detailup":
		return m.scrollDetail(-1)
	case "preview":
		return m.previewMore()
	case "record":
		return m.openRecord()
	case "scope":
		if m.style == styleList {
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
	case "insert":
		s := m.sel()
		if s.detailKey() == "" {
			m.toast = "select a table to insert its name"
			return nil
		}
		m.insertPath(s.path)
	case "runs":
		m.ov, m.ovSel, m.ovOff = ovRuns, 0, 0
	}
	return nil
}

func (m *Model) openConfig() tea.Cmd {
	path := m.be.ConfigPath()
	argv := append(config.ResolveEditor(m.o.Getenv, runtime.GOOS), path)
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return editorDoneMsg{err} })
}

// scrollDetail moves within the pane that tab last selected: it scrolls the
// schema, or moves the row cursor of the preview once rows are loaded (p
// switches to it). Moving down from the last loaded row loads the next page.
func (m *Model) scrollDetail(d int) tea.Cmd {
	if _, p := m.previewRows(); p != nil && m.tab == 1 {
		last := len(p.Rows) - 1
		if d > 0 && m.prevRow >= last {
			return m.previewMore()
		}
		m.prevRow = clamp(m.prevRow+d, 0, last)
		return nil
	}
	m.detOff = clamp(m.detOff+d, 0, 1<<30)
	return nil
}

func (m *Model) moreIfFlat() tea.Cmd {
	if m.style == styleList {
		return m.maybeMore(m.flat)
	}
	return nil
}

// back implements "esc backs out exactly one level".
func (m *Model) back() tea.Cmd {
	switch {
	case m.style == styleList && m.flat.in.value() != "":
		m.flat.in.set("")
		m.flat.cur, m.flat.off = 0, 0
		m.flat.expandShards = false
		return m.runSearch(m.flat)
	case m.style == styleColumns && m.mil.family != nil:
		m.milCollapseFamily()
	case m.styleFilter() != nil && m.styleFilter().active():
		f := m.styleFilter()
		f.clear()
		return m.refilter(f)
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
			c := m.palCmds[m.palSel]
			switch c.zone {
			case ZoneEditor:
				m.setZone(ZoneEditor)
				return m.editorAction(c.action)
			case ZoneResults:
				m.setZone(ZoneResults)
				return m.resultsAction(c.action)
			case ZoneGlobal:
				cmd, _ := m.globalAction(c.action)
				return cmd
			}
			return m.explorerAction(c.action)
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

// afterSelect loads what the Schema pane needs when the selection changed.
func (m *Model) afterSelect() tea.Cmd {
	s := m.sel()
	key := s.detailKey()
	if key == m.lastSel {
		return nil
	}
	m.lastSel, m.detOff, m.prevOff, m.prevRow = key, 0, 0, 0
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
		page, err := m.be.Preview(m.ctx, p, start, previewPage, format.Options{MaxChars: previewChars})
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
