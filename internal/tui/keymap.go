package tui

import (
	"sort"
	"strings"
)

// Zone is where a key binding applies.
type Zone string

const (
	ZoneGlobal   Zone = "Global"
	ZoneExplorer Zone = "Explorer"
	ZoneEditor   Zone = "Editor"
	ZoneResults  Zone = "Results"
	ZoneSearch   Zone = "Search and jump"
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
	{ZoneGlobal, []string{"f6"}, "zonenext", "next zone: Explorer, Editor, Results", "Next zone", "F6 zone"},
	{ZoneGlobal, []string{"shift+f6"}, "zoneprev", "previous zone", "", ""},
	{ZoneGlobal, []string{"ctrl+w h"}, "zoneexplorer", "go to the Explorer", "Go to the Explorer", ""},
	{ZoneGlobal, []string{"ctrl+w l", "ctrl+w k"}, "zoneeditor", "go to the Editor", "Go to the Editor", ""},
	{ZoneGlobal, []string{"ctrl+w j"}, "zoneresults", "go to the Results", "Go to the Results", ""},
	{ZoneGlobal, []string{"ctrl+p"}, "palette", "command palette", "", "ctrl+p cmds"},
	{ZoneGlobal, []string{"ctrl+t"}, "jump", "jump to table (recently opened when empty)", "Jump to table", "ctrl+t jump"},
	{ZoneGlobal, []string{"f1"}, "help", "this help", "Help", ""},
	{ZoneGlobal, []string{"ctrl+c"}, "cancel", "cancel the export, the focused Run, or the sync", "Cancel the focused Run", ""},
	{ZoneGlobal, []string{}, "cancelall", "cancel every Run", "Cancel all Runs", ""},

	{ZoneExplorer, []string{"j", "down"}, "down", "move down", "", "↑↓←→/hjkl move"},
	{ZoneExplorer, []string{"k", "up"}, "up", "move up", "", ""},
	{ZoneExplorer, []string{"h", "left"}, "left", "parent column / collapse", "", ""},
	{ZoneExplorer, []string{"l", "right"}, "right", "child column / expand", "", ""},
	{ZoneExplorer, []string{"g g", "home"}, "top", "first row", "", ""},
	{ZoneExplorer, []string{"G", "end"}, "bottom", "last row", "", ""},
	{ZoneExplorer, []string{"ctrl+d", "pgdown"}, "halfdown", "half page down", "", ""},
	{ZoneExplorer, []string{"ctrl+u", "pgup"}, "halfup", "half page up", "", ""},
	{ZoneExplorer, []string{"enter"}, "open", "expand a shard family, open a table, zoom the detail when it is collapsed; zoomed preview: show the row as a record", "", ""},
	{ZoneExplorer, []string{"esc"}, "back", "back out one level", "", ""},
	{ZoneExplorer, []string{"/"}, "search", "search the catalog", "Search", "/ search"},
	{ZoneExplorer, []string{":"}, "palette", "command palette", "", ""},
	{ZoneExplorer, []string{"L"}, "layout", "cycle layout: Miller columns, tree, flat list", "Cycle layout", "L layout"},
	{ZoneExplorer, []string{"P"}, "panes", "detail pane: auto, right, bottom, hidden; zoomed: schema and preview side by side or stacked", "Cycle detail pane placement", "P panes"},
	{ZoneExplorer, []string{"tab"}, "tab", "switch between schema and preview (the pane that ctrl+e / ctrl+y move in)", "", ""},
	{ZoneExplorer, []string{"z"}, "zoom", "zoom the detail pane: the movement keys then scroll it, left or esc leaves", "Zoom detail pane", ""},
	{ZoneExplorer, []string{"ctrl+e"}, "detaildown", "scroll the schema / next preview row", "", ""},
	{ZoneExplorer, []string{"ctrl+y"}, "detailup", "scroll the schema / previous preview row", "", ""},
	{ZoneExplorer, []string{"p"}, "preview", "load 20 (more) preview rows of the table", "Preview table rows", ""},
	{ZoneExplorer, []string{"J"}, "record", "show the selected preview row as a full record (after p)", "Show preview row as a record", ""},
	{ZoneExplorer, []string{"S"}, "scope", "flat list: cycle scope all, project, dataset", "", ""},
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
	{ZoneResults, []string{"e"}, "export", "export every row to CSV, JSON or NDJSON", "Export results to a file", "e export"},
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
	{ZoneSearch, []string{"ctrl+e"}, "detaildown", "scroll the detail pane down", "", ""},
	{ZoneSearch, []string{"ctrl+y"}, "detailup", "scroll the detail pane up", "", ""},

	{ZoneOverlay, []string{"j", "down", "ctrl+n"}, "down", "next line", "", ""},
	{ZoneOverlay, []string{"k", "up"}, "up", "previous line", "", ""},
	{ZoneOverlay, []string{"ctrl+d", "pgdown"}, "halfdown", "half page down", "", ""},
	{ZoneOverlay, []string{"ctrl+u", "pgup"}, "halfup", "half page up", "", ""},
	{ZoneOverlay, []string{"enter"}, "open", "run the selected command", "", ""},
	{ZoneOverlay, []string{"n"}, "next", "record popup: next row", "", ""},
	{ZoneOverlay, []string{"N", "p"}, "prev", "record popup: previous row", "", ""},
	{ZoneOverlay, []string{"y"}, "copy", "record popup: copy the record as JSON", "", ""},
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

func helpLines() []string {
	var out []string
	for _, z := range []Zone{ZoneGlobal, ZoneExplorer, ZoneEditor, ZoneResults, ZoneSearch, ZoneOverlay} {
		out = append(out, " "+sBold.Render(string(z)))
		for _, b := range Keymap {
			if b.Zone != z {
				continue
			}
			if len(b.Keys) == 0 {
				continue // palette only
			}
			out = append(out, " "+sKey.Render(pad(strings.Join(b.Keys, "  "), 16))+b.Help)
		}
		if z == ZoneEditor {
			for _, l := range editingHelp {
				out = append(out, " "+sKey.Render(pad(l[0], 16))+l[1])
			}
		}
		out = append(out, "")
	}
	out = append(out,
		" "+sBold.Render("Search syntax"),
		" "+sKey.Render(pad("p: d: type:", 16))+"filters; also col: label: desc:",
		" "+sKey.Render(pad("-d:tmp", 16))+"negate · type:view,mview is OR · d:\"my set\" quoted",
		" "+sKey.Render(pad("col:user.id", 16))+"columns are searched only with col:",
		" "+sKey.Render(pad("proj.ds.table", 16))+"a pasted path is an exact lookup",
	)
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
