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
	{ZoneGlobal, []string{"ctrl+q"}, "quit", "quit", "Quit", ""},
	{ZoneGlobal, []string{"ctrl+p"}, "palette", "command palette", "", "ctrl+p cmds"},
	{ZoneGlobal, []string{"ctrl+t"}, "jump", "jump to table (recently opened when empty)", "Jump to table", "ctrl+t jump"},
	{ZoneGlobal, []string{"f1"}, "help", "this help", "Help", ""},
	{ZoneGlobal, []string{"ctrl+c"}, "cancel", "cancel the running sync", "Cancel sync", ""},

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
	{ZoneExplorer, []string{"y"}, "copy", "copy the qualified name", "Copy qualified name", ""},
	{ZoneExplorer, []string{"Y"}, "copyquoted", "copy the name in backticks", "Copy name in backticks", ""},
	{ZoneExplorer, []string{"r"}, "refresh", "refresh the selected project", "Refresh selected project", ""},
	{ZoneExplorer, []string{"R"}, "refreshall", "refresh every project", "Refresh all projects", ""},
	{ZoneExplorer, []string{"g p"}, "problems", "sync problems", "Show sync problems", ""},
	{ZoneExplorer, []string{"g s"}, "syncdetails", "sync progress details", "Show sync progress", ""},
	{ZoneExplorer, []string{"g c"}, "config", "open the config file in your editor", "Open config", ""},
	{ZoneExplorer, []string{"?"}, "help", "this help", "", "? help"},
	{ZoneExplorer, []string{"q"}, "quit", "quit", "", ""},

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
	{ZoneOverlay, []string{"N"}, "prev", "record popup: previous row", "", ""},
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

// chordPrefix reports whether key starts a chord in the zone.
func (k keyIndex) chordPrefix(z Zone, key string) bool {
	for full := range k[z] {
		if strings.HasPrefix(full, key+" ") {
			return true
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
}

func paletteCommands() []command {
	var out []command
	for _, b := range Keymap {
		if b.Command != "" {
			out = append(out, command{name: b.Command, action: b.Action, key: strings.Join(b.Keys, " / ")})
		}
	}
	return out
}

func helpLines() []string {
	var out []string
	for _, z := range []Zone{ZoneGlobal, ZoneExplorer, ZoneSearch, ZoneOverlay} {
		out = append(out, " "+sBold.Render(string(z)))
		for _, b := range Keymap {
			if b.Zone != z {
				continue
			}
			out = append(out, " "+sKey.Render(pad(strings.Join(b.Keys, "  "), 16))+b.Help)
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
