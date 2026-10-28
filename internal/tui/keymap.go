package tui

import (
	"sort"
	"strings"
)

// Zone is where a key binding applies. The four Panes (Explorer, Schema,
// Editor, Results) are Zones too; user-visible text calls them Panes.
type Zone string

const (
	ZoneGlobal   Zone = "Global"
	ZoneExplorer Zone = "Explorer"
	ZoneSchema   Zone = "Schema"
	ZoneEditor   Zone = "Editor"
	ZoneResults  Zone = "Results"
	ZoneSearch   Zone = "Filter and jump"
	ZonePicker   Zone = "Favorites and History"
	ZoneOverlay  Zone = "Popups"
)

// Binding is one entry of the keymap registry. The registry is the single
// source for key dispatch, the help overlay, the command palette and the
// footer hints, so they cannot drift apart. A key made of two tokens
// ("g g") is a chord.
type Binding struct {
	Zone   Zone
	Keys   []string
	Action string
	Help   string
	// Command is the palette name; empty keeps the binding out of the palette.
	Command string
	// Hint is the short footer text; empty keeps it out of the footer.
	Hint string
}

// Keymap is the registry (spec section 15).
var Keymap = []Binding{
	{ZoneGlobal, []string{"ctrl+q"}, "quit", "quit (asks if Runs are in flight)", "Quit", ""},
	{ZoneGlobal, []string{"ctrl+o", "ctrl+w w"}, "panenext", "next pane: Explorer, Schema, Editor, Results", "Next pane", "ctrl+o pane"},
	{ZoneGlobal, []string{"ctrl+w W"}, "paneprev", "previous pane", "Previous pane", ""},
	{ZoneGlobal, []string{"ctrl+w h"}, "paneleft", "go to the pane on the left", "Go to the pane on the left", ""},
	{ZoneGlobal, []string{"ctrl+w j"}, "panedown", "go to the pane below", "Go to the pane below", ""},
	{ZoneGlobal, []string{"ctrl+w k"}, "paneup", "go to the pane above", "Go to the pane above", ""},
	{ZoneGlobal, []string{"ctrl+w l"}, "paneright", "go to the pane on the right", "Go to the pane on the right", ""},
	{ZoneGlobal, []string{}, "goexplorer", "go to the Explorer", "Go to the Explorer", ""},
	{ZoneGlobal, []string{}, "goschema", "go to the Schema pane (shown over the whole screen when the terminal has no room for it)", "Go to the Schema", ""},
	{ZoneGlobal, []string{}, "goeditor", "go to the Editor", "Go to the Editor", ""},
	{ZoneGlobal, []string{}, "goresults", "go to the Results", "Go to the Results", ""},
	{ZoneGlobal, []string{}, "layoutclassic", "layout classic: Explorer over Schema on the left, Editor over Results on the right", "Layout: classic", ""},
	{ZoneGlobal, []string{}, "layoutfocus", "layout focus: one pane at a time, under a pane bar", "Layout: focus", ""},
	{ZoneGlobal, []string{"ctrl+p"}, "palette", "command palette", "", "ctrl+p cmds"},
	{ZoneGlobal, []string{"ctrl+t"}, "jump", "jump to table (recently opened when empty)", "Jump to table", "ctrl+t jump"},
	{ZoneGlobal, []string{"f1"}, "help", "this help", "Help", ""},
	{ZoneGlobal, []string{"ctrl+x p"}, "projects", "projects: choose which are synced and browsed, and where jobs run (one selected hides the project column)", "Projects", ""},
	{ZoneGlobal, []string{"ctrl+x f"}, "favorites", "favorites: saved queries (tab switches to History)", "Favorites", ""},
	{ZoneGlobal, []string{"ctrl+x r"}, "history", "history: every query run, searchable (tab switches to Favorites)", "History", ""},
	{ZoneGlobal, []string{"ctrl+g"}, "tutorial", "tutorial of the navigation (while it runs: ctrl+g then n, p, r or q)", "Tutorial", "ctrl+g tutorial"},
	{ZoneGlobal, []string{"ctrl+c"}, "cancel", "cancel the export, the focused Run, or the sync", "Cancel the focused Run", ""},
	{ZoneGlobal, []string{}, "cancelall", "cancel every Run", "Cancel all Runs", ""},
	{ZoneGlobal, []string{"ctrl+x y"}, "handoff", "hand-off to an agent: the selected table's schema, the focused Run's error, result sample or summary, to the clipboard or a file", "Hand-off to an agent", ""},
	{ZoneGlobal, []string{}, "hoschemamd", "copy the selected table's schema as markdown", "Hand-off: copy schema (markdown)", ""},
	{ZoneGlobal, []string{}, "hoschemajson", "copy the selected table's schema as JSON (as bqt schema)", "Hand-off: copy schema (JSON)", ""},
	{ZoneGlobal, []string{}, "hoerror", "copy the focused Run's SQL and error", "Hand-off: copy query and error", ""},
	{ZoneGlobal, []string{}, "hosamplemd", "copy the first 20 result rows as markdown", "Hand-off: copy result sample (markdown)", ""},
	{ZoneGlobal, []string{}, "hosampletsv", "copy the first 20 result rows as TSV", "Hand-off: copy result sample (TSV)", ""},
	{ZoneGlobal, []string{}, "hosamplendjson", "copy the first 20 result rows as NDJSON", "Hand-off: copy result sample (NDJSON)", ""},
	{ZoneGlobal, []string{}, "horun", "copy the focused Run: SQL, estimate, state, outcome", "Hand-off: copy the run", ""},

	{ZoneExplorer, []string{"j", "down"}, "down", "move down", "", "↑↓←→/hjkl move"},
	{ZoneExplorer, []string{"k", "up"}, "up", "move up", "", ""},
	{ZoneExplorer, []string{"h", "left"}, "left", "parent column / collapse", "", ""},
	{ZoneExplorer, []string{"l", "right"}, "right", "child column / expand", "", ""},
	{ZoneExplorer, []string{"g g", "home"}, "top", "first row", "", ""},
	{ZoneExplorer, []string{"G", "end"}, "bottom", "last row", "", ""},
	{ZoneExplorer, []string{"ctrl+d", "pgdown"}, "halfdown", "half page down", "", ""},
	{ZoneExplorer, []string{"ctrl+u", "pgup"}, "halfup", "half page up", "", ""},
	{ZoneExplorer, []string{"enter"}, "open", "expand a shard family; on a table, go to the Schema pane", "", ""},
	{ZoneExplorer, []string{"esc"}, "back", "back out one level", "", ""},
	{ZoneExplorer, []string{"/"}, "filter", "filter the column (columns style), the tree, or the list", "Filter", "/ filter"},
	{ZoneExplorer, []string{":"}, "palette", "command palette", "", ""},
	{ZoneExplorer, []string{"L"}, "style", "cycle the explorer style: columns, tree, list", "Cycle explorer style", "L style"},
	{ZoneExplorer, []string{"z"}, "zoom", "show the Schema pane over the whole screen (z or esc restores)", "Maximise the Schema pane", ""},
	{ZoneExplorer, []string{"ctrl+e"}, "detaildown", "scroll the Schema pane down", "", ""},
	{ZoneExplorer, []string{"ctrl+y"}, "detailup", "scroll the Schema pane up", "", ""},
	{ZoneExplorer, []string{"p"}, "sample", "show a free Sample of the table's rows in the Results (no query job; [ / ] there switch back to the Run)", "Sample table rows (free)", "p sample"},
	{ZoneExplorer, []string{"s"}, "select", "open SELECT * … LIMIT 100 for the table in a new buffer (not run; billed when run)", "SELECT the table in a new buffer", ""},
	{ZoneExplorer, []string{"S"}, "scope", "list style: cycle scope all, project, dataset", "", ""},
	{ZoneExplorer, []string{"i"}, "insert", "insert the table's name into the editor", "Insert table name into the editor", ""},
	{ZoneExplorer, []string{"g r"}, "runs", "run list", "", ""},
	{ZoneExplorer, []string{"y"}, "copy", "copy the qualified name", "Copy qualified name", ""},
	{ZoneExplorer, []string{"Y"}, "copyquoted", "copy the name in backticks", "Copy name in backticks", ""},
	{ZoneExplorer, []string{"r"}, "refresh", "refresh the selected project", "Refresh selected project", ""},
	{ZoneExplorer, []string{"R"}, "refreshall", "refresh every project", "Refresh all projects", ""},
	{ZoneExplorer, []string{"g p"}, "problems", "sync problems", "Show sync problems", ""},
	{ZoneExplorer, []string{"g s"}, "syncdetails", "sync progress details", "Show sync progress", ""},
	{ZoneExplorer, []string{"g c"}, "config", "open the config file in your editor", "Open config", ""},
	{ZoneExplorer, []string{"?"}, "help", "this help", "", "? help"},
	{ZoneExplorer, []string{"q"}, "quit", "quit", "", ""},

	{ZoneSchema, []string{"j", "down"}, "down", "scroll down a line", "", "↑↓/jk scroll"},
	{ZoneSchema, []string{"k", "up"}, "up", "scroll up a line", "", ""},
	{ZoneSchema, []string{"ctrl+d", "pgdown"}, "halfdown", "half the pane down", "", ""},
	{ZoneSchema, []string{"ctrl+u", "pgup"}, "halfup", "half the pane up", "", ""},
	{ZoneSchema, []string{"g g", "home"}, "top", "top", "", ""},
	{ZoneSchema, []string{"G", "end"}, "bottom", "bottom", "", ""},
	{ZoneSchema, []string{"p"}, "sample", "show a free Sample of the table's rows in the Results", "", "p sample"},
	{ZoneSchema, []string{"s"}, "select", "open a SELECT for the table in a new buffer (not run)", "", ""},
	{ZoneSchema, []string{"z"}, "zoom", "maximise the Schema pane, or restore the layout", "", "z maximise"},
	{ZoneSchema, []string{"esc", "h", "left"}, "back", "restore the layout if maximised, else go back to the Explorer", "", "esc back"},
	{ZoneSchema, []string{"?"}, "help", "this help", "", ""},
	{ZoneSchema, []string{"q"}, "quit", "quit", "", ""},
	{ZoneSchema, []string{":"}, "palette", "command palette", "", ""},

	{ZoneEditor, []string{"ctrl+r"}, "run", "run the selection, or the whole buffer", "Run query", "ctrl+r run"},
	{ZoneEditor, []string{"ctrl+d"}, "dryrun", "dry-run: validate and estimate without running", "Dry-run query", "ctrl+d dry-run"},
	{ZoneEditor, []string{"ctrl+space", "ctrl+@"}, "complete", "force completion (tab accepts, esc dismisses)", "", "tab complete"},
	{ZoneEditor, []string{"ctrl+s"}, "save", "save the buffer to its file (asks for a name first)", "Save buffer to a file", ""},
	{ZoneEditor, []string{"ctrl+x e"}, "exteditor", "edit the buffer in $EDITOR", "Edit buffer in $EDITOR", ""},
	{ZoneEditor, []string{"ctrl+x l"}, "runs", "run list", "Show the run list", ""},
	{ZoneEditor, []string{"ctrl+x n"}, "newbuf", "new buffer", "New buffer", ""},
	{ZoneEditor, []string{"ctrl+x b"}, "buflist", "buffer list, with drafts to restore", "Show buffers and drafts", "ctrl+x b buffers"},
	{ZoneEditor, []string{"ctrl+x w"}, "closebuf", "close the buffer", "Close buffer", ""},
	{ZoneEditor, []string{"ctrl+x ["}, "prevbuf", "previous buffer", "", ""},
	{ZoneEditor, []string{"ctrl+x ]"}, "nextbuf", "next buffer", "", ""},
	{ZoneEditor, []string{"ctrl+x h"}, "selectall", "select everything", "", ""},
	{ZoneEditor, []string{"ctrl+x s"}, "savefav", "save the buffer as a favorite (name, description, tags)", "Save buffer as favorite", ""},
	{ZoneEditor, []string{"alt+w"}, "copysel", "copy the selection", "", ""},
	{ZoneEditor, []string{"esc"}, "edesc", "dismiss the popup, drop the selection", "", ""},

	{ZoneResults, []string{"j", "down"}, "down", "next row", "", "hjkl move"},
	{ZoneResults, []string{"k", "up"}, "up", "previous row", "", ""},
	{ZoneResults, []string{"h", "left"}, "left", "previous column", "", ""},
	{ZoneResults, []string{"l", "right"}, "right", "next column", "", ""},
	{ZoneResults, []string{"g g", "home"}, "top", "first row", "", ""},
	{ZoneResults, []string{"G", "end"}, "bottom", "last row", "", ""},
	{ZoneResults, []string{"ctrl+d"}, "halfdown", "half page down", "", ""},
	{ZoneResults, []string{"ctrl+u"}, "halfup", "half page up", "", ""},
	{ZoneResults, []string{"pgdown"}, "pagedown", "page down", "", ""},
	{ZoneResults, []string{"pgup"}, "pageup", "page up", "", ""},
	{ZoneResults, []string{"0"}, "firstcol", "first column", "", ""},
	{ZoneResults, []string{"$"}, "lastcol", "last column", "", ""},
	{ZoneResults, []string{"enter", "v"}, "record", "show the row as a full record (n / p move between rows)", "", "enter record"},
	{ZoneResults, []string{"f"}, "freeze", "freeze the first column", "", ""},
	{ZoneResults, []string{"H"}, "hide", "hide the cursor's column", "", ""},
	{ZoneResults, []string{"U"}, "unhide", "show the hidden columns again", "", ""},
	{ZoneResults, []string{"/"}, "find", "find text in the loaded pages", "", "/ find"},
	{ZoneResults, []string{"n"}, "findnext", "next match", "", ""},
	{ZoneResults, []string{"N"}, "findprev", "previous match", "", ""},
	{ZoneResults, []string{"y"}, "copy", "copy the cell", "", "y copy"},
	{ZoneResults, []string{"Y"}, "copyrow", "copy the row as JSON", "Copy result row as JSON", ""},
	{ZoneResults, []string{"g y"}, "copycol", "copy the column's values (loaded page)", "Copy result column values", ""},
	{ZoneResults, []string{"g f"}, "copypath", "copy the column name", "", ""},
	{ZoneResults, []string{"g t"}, "copytsv", "copy the visible rows as TSV", "Copy visible result rows as TSV", ""},
	{ZoneResults, []string{"e"}, "export", "export every row to CSV, JSON or NDJSON (not a Sample)", "Export results to a file", "e export"},
	{ZoneResults, []string{"[", "]"}, "switchsource", "switch between the Sample and the buffer's Run result", "Switch between Sample and Run result", ""},
	{ZoneResults, []string{"g r"}, "runs", "run list", "", ""},
	{ZoneResults, []string{"esc"}, "back", "clear the find text, then back to the editor", "", ""},
	{ZoneResults, []string{"?"}, "help", "this help", "", "? help"},
	{ZoneResults, []string{"q"}, "quit", "quit", "", ""},

	{ZoneSearch, []string{"down", "ctrl+n"}, "down", "next result", "", ""},
	{ZoneSearch, []string{"up", "ctrl+p"}, "up", "previous result", "", ""},
	{ZoneSearch, []string{"pgdown"}, "halfdown", "half page down", "", ""},
	{ZoneSearch, []string{"pgup"}, "halfup", "half page up", "", ""},
	{ZoneSearch, []string{"enter"}, "open", "go to the result in the explorer", "", "enter open"},
	{ZoneSearch, []string{"esc"}, "back", "close", "", "esc close"},
	{ZoneSearch, []string{"ctrl+e"}, "detaildown", "scroll the Schema pane down", "", ""},
	{ZoneSearch, []string{"ctrl+y"}, "detailup", "scroll the Schema pane up", "", ""},

	{ZonePicker, []string{"down", "ctrl+n"}, "down", "next entry", "", ""},
	{ZonePicker, []string{"up", "ctrl+p"}, "up", "previous entry", "", ""},
	{ZonePicker, []string{"pgdown"}, "pagedown", "page down", "", ""},
	{ZonePicker, []string{"pgup"}, "pageup", "page up", "", ""},
	{ZonePicker, []string{"tab", "shift+tab"}, "scope", "switch between Favorites and History", "", "tab favorites/history"},
	{ZonePicker, []string{"enter"}, "open", "load it into the editor (asks before replacing unsaved text)", "", "enter open"},
	{ZonePicker, []string{"ctrl+e"}, "detaildown", "scroll the preview down", "", ""},
	{ZonePicker, []string{"ctrl+y"}, "detailup", "scroll the preview up", "", ""},
	{ZonePicker, []string{"ctrl+r"}, "pickr", "Favorites: rename · History: show the result again (BigQuery keeps it about 24 hours)", "", ""},
	{ZonePicker, []string{"ctrl+d"}, "delete", "Favorites: delete the file (asks first)", "", ""},
	{ZonePicker, []string{"ctrl+s"}, "promote", "History: save as a favorite", "", ""},
	{ZonePicker, []string{"esc"}, "back", "close", "", "esc close"},

	{ZoneOverlay, []string{"j", "down", "ctrl+n"}, "down", "next line", "", ""},
	{ZoneOverlay, []string{"k", "up"}, "up", "previous line", "", ""},
	{ZoneOverlay, []string{"ctrl+d", "pgdown"}, "halfdown", "half page down", "", ""},
	{ZoneOverlay, []string{"ctrl+u", "pgup"}, "halfup", "half page up", "", ""},
	{ZoneOverlay, []string{"enter"}, "open", "run the selected command", "", ""},
	{ZoneOverlay, []string{"n"}, "next", "record popup: next row", "", ""},
	{ZoneOverlay, []string{"N", "p"}, "prev", "record popup: previous row", "", ""},
	{ZoneOverlay, []string{"y"}, "copy", "record popup: copy the record as JSON", "", ""},
	{ZoneOverlay, []string{"space", "x"}, "toggle", "projects popup: select or deselect the project (deselected: not synced, cache kept)", "", ""},
	{ZoneOverlay, []string{"o"}, "only", "projects popup: select only this project", "", ""},
	{ZoneOverlay, []string{"e"}, "exec", "projects popup: run query and sync jobs in this project (the Execution project)", "", ""},
	{ZoneOverlay, []string{"/"}, "filter", "projects popup: filter by project ID or name", "", ""},
	{ZoneOverlay, []string{"r"}, "reloadlist", "projects popup: fetch your projects list again", "", ""},
	{ZoneOverlay, []string{"s"}, "sync", "projects popup: sync the project now, even when its cache is fresh", "", ""},
	{ZoneOverlay, []string{"S"}, "syncall", "projects popup: sync every selected project now", "", ""},
	{ZoneOverlay, []string{"w"}, "write", "hand-off popup: write to a file", "", ""},
	{ZoneOverlay, []string{"esc", "q"}, "back", "close", "", "esc close"},
}

type keyIndex map[Zone]map[string]string

func buildKeyIndex(km []Binding) keyIndex {
	idx := keyIndex{}
	for _, b := range km {
		if idx[b.Zone] == nil {
			idx[b.Zone] = map[string]string{}
		}
		for _, k := range b.Keys {
			idx[b.Zone][k] = b.Action
		}
	}
	return idx
}

var keys = buildKeyIndex(Keymap)

// action resolves a key in a zone, falling back to the global zone.
func (k keyIndex) action(z Zone, key string) string {
	if a, ok := k[z][key]; ok {
		return a
	}
	return k[ZoneGlobal][key]
}

// chordPrefix reports whether key starts a chord in the zone (or globally).
func (k keyIndex) chordPrefix(z Zone, key string) bool {
	for _, zone := range []Zone{z, ZoneGlobal} {
		for full := range k[zone] {
			if strings.HasPrefix(full, key+" ") {
				return true
			}
		}
	}
	return false
}

// duplicateBindings lists keys bound twice within one zone.
func duplicateBindings(km []Binding) []string {
	seen := map[string]bool{}
	var dup []string
	for _, b := range km {
		for _, k := range b.Keys {
			id := string(b.Zone) + " / " + k
			if seen[id] {
				dup = append(dup, id)
			}
			seen[id] = true
		}
	}
	sort.Strings(dup)
	return dup
}

func footerHints(zones ...Zone) string {
	var parts []string
	for _, z := range zones {
		for _, b := range Keymap {
			if b.Zone == z && b.Hint != "" {
				parts = append(parts, b.Hint)
			}
		}
	}
	return strings.Join(parts, " · ")
}

type command struct {
	name, action, key string
	zone              Zone
}

func paletteCommands() []command {
	var out []command
	for _, b := range Keymap {
		if b.Command != "" {
			out = append(out, command{name: b.Command, action: b.Action, key: strings.Join(b.Keys, " / "), zone: b.Zone})
		}
	}
	return out
}

// helpLines lays the key reference out for an inner width of w. A key column
// is at least 16 cells; descriptions wrap under their own column.
func helpLines(w int) []string {
	var out []string
	row := func(keys, text string) {
		kw := max(16, wid(keys)+2)
		tw := max(20, w-1-kw)
		for i, l := range wrap(text, tw) {
			k := strings.Repeat(" ", kw)
			if i == 0 {
				k = sKey.Render(pad(keys, kw))
			}
			out = append(out, " "+k+l)
		}
	}
	for _, z := range []Zone{ZoneGlobal, ZoneExplorer, ZoneSchema, ZoneEditor, ZoneResults, ZoneSearch, ZonePicker, ZoneOverlay} {
		out = append(out, " "+sBold.Render(string(z)))
		for _, b := range Keymap {
			if b.Zone != z {
				continue
			}
			if len(b.Keys) == 0 {
				continue // palette only
			}
			row(strings.Join(b.Keys, "  "), b.Help)
		}
		if z == ZoneEditor {
			for _, l := range editingHelp {
				row(l[0], l[1])
			}
			out = append(out, "", " "+sBold.Render("Editor, vim editing"))
			for _, l := range wrap(`[tui] editing = "vim" is the default; "plain" turns it off`, max(20, w-2)) {
				out = append(out, " "+sDim.Render(l))
			}
			for _, l := range vimHelp {
				row(l[0], l[1])
			}
		}
		out = append(out, "")
	}
	out = append(out, " "+sBold.Render("Search syntax"))
	row("p: d: type:", "filters; also col: label: desc:")
	row("-d:tmp", "negate · type:view,mview is OR · d:\"my set\" quoted")
	row("col:user.id", "columns are searched only with col:")
	row("proj.ds.table", "a pasted path is an exact lookup")
	return out
}

// editingHelp lists the text-editing keys, which belong to the editor widget
// and not to the registry.
var editingHelp = [][2]string{
	{"shift+arrows", "select; typing replaces the selection"},
	{"ctrl+a  ctrl+e", "line start (first press: first non-blank) and end"},
	{"alt+b  alt+f", "word left and right (also ctrl+←/→)"},
	{"alt+backspace", "delete the word before the cursor (alt+d: after)"},
	{"ctrl+k  ctrl+u", "delete to the line end / line start"},
	{"ctrl+z  ctrl+y", "undo and redo"},
}

// vimHelp lists the vim layer's keys (spec 9.1).
var vimHelp = [][2]string{
	{"i a I A o O", "insert before / after the cursor, at line start / end, on a new line below / above"},
	{"esc", "back to Normal mode"},
	{"h j k l", "move; also w b e W B E, 0 ^ $, gg G, { }, %, f F t T ; ,"},
	{"ctrl+d ctrl+u", "half a page down / up (ctrl+f ctrl+b: a page)"},
	{"d c y > <", "delete, change, yank, indent: with a motion or text object (dw, ci(, yap is not built)"},
	{"dd cc yy >> <<", "the same on whole lines; D C Y work to the line end / on the line"},
	{"iw aw i\" i( i[ i{", "text objects: inner / a word, quotes (also ' and `), brackets (also a…)"},
	{"x X s S r J ~", "delete char, substitute, replace, join lines, toggle case"},
	{"p P", "paste after / before; yanks also go to the system clipboard"},
	{"u  ctrl+r", "undo, redo"},
	{".", "repeat the last change"},
	{"v V", "Visual and Visual-line mode: then d c y > < ~ u U, or enter to run the selection"},
	{"/ n N *", "find in the buffer, next, previous, the word under the cursor"},
	{"enter", "run the buffer (in Visual mode: the selection)"},
	{"space r / d", "run / dry-run; space l runs, space b buffers, space n new, space w close, space [ ] switch, space s save, space e $EDITOR"},
	{"space f / h", "favorites / history; space F saves the buffer as a favorite"},
	{"space p", "projects: choose which are synced and where jobs run"},
	{"space y", "hand-off to an agent (schema, error, result sample, run)"},
	{":  ?", "command palette, this help"},
	{"Insert mode", "ctrl+r runs, ctrl+d dry-runs, tab accepts a completion, ctrl+n forces one"},
}
