package tui

import (
	"fmt"
	"strings"
)

// The tutorial is a panel docked under the screen that teaches the
// navigation one step at a time. The UI works as usual while it runs; each
// step watches the model and opens the next step once its task is done.
// ctrl+g starts it; while it runs, ctrl+g is a chord prefix for the
// tutorial's own keys (n next, p back, q quit).

// tutorialRows is the panel's fixed height, so the screen does not jump
// between steps.
const tutorialRows = 7

type tutStep struct {
	title string
	// text is the instruction; `backticks` mark keys. It may depend on the
	// editing mode.
	text func(m *Model) string
	// done reports that the task is complete; nil steps advance only with
	// ctrl+g n.
	done func(m *Model, t *tutorial) bool
}

type tutorial struct {
	step    int
	pending bool // ctrl+g was pressed: the next key is the tutorial's
	praise  string

	// observed since the step started
	start    tutSnap
	keys     map[string]bool // keys pressed in the Pane the step starts in
	seenOv   map[overlayKind]bool
	schema   bool // the Schema pane had focus
	inserts  bool // vim Insert mode was entered
	styles   bool
	filtered bool // an Explorer filter was set
}

type tutSnap struct {
	zone  Zone
	sel   string
	style explorerStyle
	runs  int
	text  string
}

func (m *Model) tutSnap() tutSnap {
	return tutSnap{zone: m.zone, sel: m.sel().path.String() + "\x00" + string(m.sel().kind), style: m.style, runs: len(m.runs.List()), text: m.buf().ed.text()}
}

func plain(s string) func(*Model) string { return func(*Model) string { return s } }

func (t *tutorial) pressed(names ...string) bool {
	for _, n := range names {
		if t.keys[n] {
			return true
		}
	}
	return false
}

// closedAfter is true once overlay kind was opened during the step and no
// overlay is open now.
func closedAfter(kind overlayKind) func(m *Model, t *tutorial) bool {
	return func(m *Model, t *tutorial) bool { return t.seenOv[kind] && m.ov == ovNone }
}

var tutSteps = []tutStep{
	{"Welcome", plain("This panel teaches the navigation, one step at a time. Each step names a few keys: do what it says and the next step opens by itself. Everything works as usual meanwhile. `ctrl+g n` goes on now."), nil},
	{"Move in the Explorer", plain("The Explorer lists projects, datasets and tables. Press `j` and `k` (or `↓` `↑`) to move the cursor. `g g` and `G` go to the first and last row."),
		func(m *Model, t *tutorial) bool {
			return m.zone == ZoneExplorer && m.tutSnap().sel != t.start.sel
		}},
	{"In and out", plain("`l` or `→` opens the selected project or dataset (in the tree style: expands it); `h` or `←` goes back out. Go in once and back out once."),
		func(m *Model, t *tutorial) bool {
			return t.pressed("l", "right", "enter") && t.pressed("h", "left", "esc")
		}},
	{"Filter the Explorer", plain("`/` filters the list you are in: the focused column in the columns style, the whole tree in the tree style. Type part of a name; `enter` or `esc` goes back to the list with the filter kept, and `esc` there clears it. Filter once, then clear it."),
		func(m *Model, t *tutorial) bool { return t.filtered && !m.filterOn() }},
	{"Jump to a table", plain("`ctrl+t` jumps to a table from any pane: type, then `enter`. Empty, it lists the tables you opened lately. From the Editor it inserts the name instead. Try it, then close it."),
		closedAfter(ovJump)},
	{"Explorer styles", plain("`L` cycles the Explorer style: columns, tree, list. bqt remembers your choice, so press it until you are back at the one you like."),
		func(m *Model, t *tutorial) bool { return t.styles }},
	{"The Schema pane", plain("The Schema pane shows the selected table. `enter` on a table goes there, and the movement keys scroll it. `tab` switches to the preview (`p` loads rows), `z` shows the pane over the whole screen, and `esc` goes back to the Explorer. Go there and back once."),
		func(m *Model, t *tutorial) bool { return t.schema && m.zone == ZoneExplorer }},
	{"Command palette", plain("`ctrl+p` (or `:` in the Explorer) lists every command by name, with its key. Type to filter, `enter` runs one, `esc` closes. Open it and close it."),
		closedAfter(ovPalette)},
	{"Help", plain("`?` lists every key of every pane (`f1` too). `j` `k` scroll it, `esc` closes it. Open it and close it."),
		closedAfter(ovHelp)},
	{"Panes", plain("The screen has four panes: Explorer, Schema, Editor and Results. `ctrl+o` moves to the next one (`ctrl+w w` too, `ctrl+w W` goes back). Go to the Editor now."),
		func(m *Model, t *tutorial) bool { return m.zone == ZoneEditor }},
	{"Write a query", func(m *Model) string {
		if m.vimOn() {
			return "The editor uses vim keys and starts in Normal mode. `i` enters Insert mode: type SELECT 1, then `esc` goes back to Normal mode."
		}
		return "Type SELECT 1. `shift+arrows` select, `ctrl+z` undoes, `tab` accepts a completion."
	},
		func(m *Model, t *tutorial) bool {
			b := m.buf()
			if strings.TrimSpace(b.ed.text()) == "" || b.ed.text() == t.start.text {
				return false
			}
			return !m.vimOn() || (t.inserts && b.vim.mode == vimNormal)
		}},
	{"Run it", func(m *Model) string {
		if m.vimOn() {
			return "In Normal mode `enter` runs the buffer (in Visual mode, the selection); `space d` is a dry-run, which only checks and estimates. SELECT 1 reads no data. Run it."
		}
		return "`ctrl+r` runs the buffer (or the selection); `ctrl+d` is a dry-run, which only checks and estimates. SELECT 1 reads no data. Run it."
	},
		func(m *Model, t *tutorial) bool { return len(m.runs.List()) > t.start.runs }},
	{"Go to the Results", plain("`ctrl+w` then a direction (`h` `j` `k` `l`) moves to the pane on that side: `ctrl+w j` from the Editor reaches the Results below it. Go to the Results now."),
		func(m *Model, t *tutorial) bool { return m.zone == ZoneResults }},
	{"Read the Results", plain("`h` `j` `k` `l` move between cells, `enter` shows the row as a record (`n` `p` step rows), `esc` closes it. `y` copies a cell, `e` exports. Open a record and close it."),
		closedAfter(ovRecord)},
	{"Back to the Explorer", plain("`ctrl+w h` goes left and `ctrl+w k` goes up: from the Results that is the Schema, then the Explorer. There, `i` inserts the selected table's name into the editor. `esc` in the Results goes back to the Editor."),
		func(m *Model, t *tutorial) bool { return m.zone == ZoneExplorer }},
	{"Favorites and History", plain("`ctrl+x r` opens History: every query you ran, searchable (`status:failed`, `since:7d`). `tab` switches to Favorites, your saved queries (`ctrl+x s` saves the buffer as one); `enter` loads one into the editor. Open it and close it."),
		closedAfter(ovPicker)},
	{"Done", plain("That is the navigation. `?` lists every key, `ctrl+p` every command, `ctrl+g` starts this tutorial again. `ctrl+g n` closes it."), nil},
}

func (m *Model) startTutorial() {
	m.ov, m.pendingKey = ovNone, ""
	m.setZone(ZoneExplorer)
	m.tut = &tutorial{}
	m.tutGo(0)
}

func (m *Model) stopTutorial() {
	m.tut = nil
	m.fitHeight()
}

func (m *Model) tutGo(step int) {
	t := m.tut
	t.step = clamp(step, 0, len(tutSteps)-1)
	t.keys, t.seenOv = map[string]bool{}, map[overlayKind]bool{}
	t.schema, t.inserts, t.styles, t.filtered = false, false, false, false
	t.start = m.tutSnap()
	m.fitHeight()
}

// fitHeight gives the tutorial panel its rows.
func (m *Model) fitHeight() {
	m.h = m.termH
	if m.tut != nil {
		m.h = max(6, m.termH-tutorialRows)
	}
}

// tutorialKey handles ctrl+g and the key after it. It reports whether it
// used the key.
func (m *Model) tutorialKey(name string) bool {
	if m.tut == nil {
		if keys.action(ZoneGlobal, name) != "tutorial" {
			return false
		}
		if m.ov == ovPrompt || m.ov == ovConfirm {
			// closing these would drop the question unanswered
			m.toast = "answer the popup first, then ctrl+g starts the tutorial"
			return true
		}
		m.startTutorial()
		return true
	}
	t := m.tut
	if t.pending {
		t.pending = false
		switch name {
		case "n", "enter", "right", "l":
			if t.step == len(tutSteps)-1 {
				m.stopTutorial()
				m.toast = "tutorial finished (ctrl+g starts it again)"
				return true
			}
			t.praise = ""
			m.tutGo(t.step + 1)
		case "p", "left", "h":
			t.praise = ""
			m.tutGo(t.step - 1)
		case "q", "esc":
			m.stopTutorial()
			m.toast = "tutorial closed (ctrl+g starts it again)"
		case "r":
			m.startTutorial()
		default:
			m.toast = "ctrl+g " + name + " is not a tutorial key (n next · p back · r restart · q quit)"
		}
		return true
	}
	if name == "ctrl+g" {
		t.pending = true
		return true
	}
	if m.zone == t.start.zone {
		t.keys[name] = true
	}
	return false
}

// tutorialObserve runs after every update: it records what happened and
// moves on when the step's task is done.
func (m *Model) tutorialObserve() {
	t := m.tut
	if t == nil {
		return
	}
	if m.ov != ovNone {
		t.seenOv[m.ov] = true
	}
	if m.zone == ZoneSchema {
		t.schema = true
	}
	if m.filterActive() {
		t.filtered = true
	}
	if m.style != t.start.style {
		t.styles = true
	}
	if m.vimOn() && m.buf().vim.mode == vimInsert {
		t.inserts = true
	}
	st := tutSteps[t.step]
	if st.done != nil && st.done(m, t) {
		t.praise = "✓ " + st.title
		m.tutGo(t.step + 1)
	}
}

// tutorialPanel draws the panel, tutorialRows lines high.
func (m *Model) tutorialPanel(w int) []string {
	t := m.tut
	st := tutSteps[t.step]
	inner := w - 2
	head := fmt.Sprintf("Tutorial %d/%d · %s", t.step+1, len(tutSteps), st.title)
	body := keyMarkup(wrap(st.text(m), max(10, inner-2)))
	lines := make([]string, 0, tutorialRows-2)
	for _, l := range body {
		lines = append(lines, " "+l)
	}
	for len(lines) < tutorialRows-3 {
		lines = append(lines, "")
	}
	foot := " "
	if t.praise != "" {
		foot += sOk.Render(t.praise+" done") + "  "
	}
	if t.pending {
		foot += sWarn.Render("tutorial key: n next · p back · r restart · q quit")
	} else {
		foot += sFaint.Render("ctrl+g then: n next · p back · r restart · q quit")
	}
	lines = append(lines[:tutorialRows-3], foot)
	return popup(head, lines, w)
}

// keyMarkup highlights the `backticked` keys of wrapped lines; a key may be
// split across lines.
func keyMarkup(lines []string) []string {
	in := false
	out := make([]string, len(lines))
	for i, l := range lines {
		var sb strings.Builder
		parts := strings.Split(l, "`")
		for j, p := range parts {
			if j > 0 {
				in = !in
			}
			if in {
				sb.WriteString(sKey.Render(p))
			} else {
				sb.WriteString(p)
			}
		}
		out[i] = sb.String()
	}
	return out
}
