package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// edKey builds a key press from a name such as "a", "enter", "shift+left", "ctrl+z" or "alt+b".
func edKey(name string) tea.KeyPressMsg {
	var mod tea.KeyMod
	for {
		switch {
		case strings.HasPrefix(name, "shift+"):
			mod |= tea.ModShift
			name = name[6:]
			continue
		case strings.HasPrefix(name, "ctrl+"):
			mod |= tea.ModCtrl
			name = name[5:]
			continue
		case strings.HasPrefix(name, "alt+"):
			mod |= tea.ModAlt
			name = name[4:]
			continue
		}
		break
	}
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
		"left": tea.KeyLeft, "right": tea.KeyRight, "backspace": tea.KeyBackspace, "delete": tea.KeyDelete, "home": tea.KeyHome, "end": tea.KeyEnd,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "space": tea.KeySpace, "f1": tea.KeyF1, "f6": tea.KeyF6}
	if c, ok := codes[name]; ok {
		k := tea.KeyPressMsg{Code: c, Mod: mod}
		if name == "space" && mod == 0 {
			k.Text = " "
		}
		return k
	}
	r := []rune(name)[0]
	k := tea.KeyPressMsg{Code: r, Mod: mod}
	if mod&^tea.ModShift == 0 {
		k.Text = name
	}
	return k
}

func edType(e *editor, keys ...string) {
	for _, k := range keys {
		if len([]rune(k)) > 1 && !strings.Contains(k, "+") && !isNamedKey(k) {
			for _, ch := range k {
				e.handle(edKey(string(ch)), 10)
			}
			continue
		}
		e.handle(edKey(k), 10)
	}
}

func isNamedKey(k string) bool {
	switch k {
	case "enter", "esc", "tab", "up", "down", "left", "right", "backspace", "delete", "home", "end", "pgup", "pgdown", "space":
		return true
	}
	return false
}

func TestEditorTypingAndUndo(t *testing.T) {
	e := newEditor("")
	edType(e, "select", "space", "1", "enter", "from", "space", "t")
	if e.text() != "select 1\nfrom t" || e.row != 1 || e.col != 6 {
		t.Fatalf("%q %d:%d", e.text(), e.row, e.col)
	}
	if e.offset() != len("select 1\nfrom t") {
		t.Fatalf("offset %d", e.offset())
	}
	// typing is one undo step per run, the newline its own
	edType(e, "ctrl+z")
	if e.text() != "select 1\n" {
		t.Fatalf("%q", e.text())
	}
	edType(e, "ctrl+z")
	if e.text() != "select 1" {
		t.Fatalf("%q", e.text())
	}
	edType(e, "ctrl+z")
	if e.text() != "" {
		t.Fatalf("%q", e.text())
	}
	edType(e, "ctrl+y", "ctrl+y", "ctrl+y")
	if e.text() != "select 1\nfrom t" {
		t.Fatalf("redo: %q", e.text())
	}
	edType(e, "x", "ctrl+z", "ctrl+y")
	if e.text() != "select 1\nfrom tx" {
		t.Fatalf("%q", e.text())
	}
}

func TestEditorMovementSelectionAndDeletes(t *testing.T) {
	e := newEditor("alpha beta\n  gamma_1 delta\nlast")
	e.row, e.col = 0, 0
	edType(e, "ctrl+right", "ctrl+right")
	if e.col != 10 {
		t.Fatalf("word right: %d", e.col)
	}
	edType(e, "right") // wraps to the next line
	if e.row != 1 || e.col != 0 {
		t.Fatalf("%d:%d", e.row, e.col)
	}
	edType(e, "end", "home")
	if e.col != 2 {
		t.Fatalf("home goes to the first non-blank first: %d", e.col)
	}
	edType(e, "home")
	if e.col != 0 {
		t.Fatalf("then to column 0: %d", e.col)
	}
	edType(e, "end", "alt+b", "shift+end")
	if e.selection() != "delta" {
		t.Fatalf("%q", e.selection())
	}
	edType(e, "X")
	if e.lines[1] == nil || string(e.lines[1]) != "  gamma_1 X" {
		t.Fatalf("typing replaces the selection: %q", string(e.lines[1]))
	}
	edType(e, "alt+backspace", "alt+backspace")
	if string(e.lines[1]) != "  " {
		t.Fatalf("delete word back: %q", string(e.lines[1]))
	}
	edType(e, "backspace")
	if string(e.lines[1]) != "" {
		t.Fatalf("a soft tab goes in one backspace: %q", string(e.lines[1]))
	}
	edType(e, "backspace")
	if e.text() != "alpha beta\nlast" || e.row != 0 || e.col != 10 {
		t.Fatalf("joining lines: %q %d:%d", e.text(), e.row, e.col)
	}
	edType(e, "home", "ctrl+k")
	if e.text() != "\nlast" {
		t.Fatalf("kill to end of line: %q", e.text())
	}
	edType(e, "ctrl+k")
	if e.text() != "last" {
		t.Fatalf("kill at the end joins: %q", e.text())
	}
	// multi-line selection, up and down keep the wanted column
	e.setText("abcdef\nab\nabcdef")
	e.row, e.col, e.want = 0, 5, 5
	edType(e, "down")
	if e.col != 2 {
		t.Fatalf("%d", e.col)
	}
	edType(e, "down")
	if e.col != 5 {
		t.Fatalf("the wanted column survives a short line: %d", e.col)
	}
	edType(e, "shift+up", "shift+up")
	if e.selection() != "f\nab\nabcde" {
		t.Fatalf("%q", e.selection())
	}
	edType(e, "delete")
	if e.text() != "abcdef" {
		t.Fatalf("%q", e.text())
	}
	e.selectAll()
	if e.selection() != "abcdef" {
		t.Fatalf("%q", e.selection())
	}
}

func TestEditorAutoIndentPasteAndReplace(t *testing.T) {
	e := newEditor("")
	edType(e, "tab", "a", "enter", "b")
	if e.text() != "  a\n  b" {
		t.Fatalf("auto-indent: %q", e.text())
	}
	e.insert("X\r\nY\nZ")
	if e.text() != "  a\n  bX\nY\nZ" || e.row != 3 || e.col != 1 {
		t.Fatalf("paste: %q %d:%d", e.text(), e.row, e.col)
	}
	e = newEditor("select čau, cu from t")
	start := strings.Index(e.text(), "cu")
	e.replaceBytes(start, start+2, "customer_id")
	if e.text() != "select čau, customer_id from t" || e.col != len([]rune("select čau, customer_id")) {
		t.Fatalf("%q col %d", e.text(), e.col)
	}
	if r, c := e.position(e.offset()); r != e.row || c != e.col {
		t.Fatalf("offset and position disagree: %d:%d", r, c)
	}
	e.doUndo()
	if e.text() != "select čau, cu from t" {
		t.Fatalf("%q", e.text())
	}
}

func TestEditorViewScrollsAndHighlights(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("select " + strings.Repeat("x", 30) + " -- line\n")
	}
	e := newEditor(sb.String())
	e.row, e.col = 30, 39
	lines, cx, cy := e.view(24, 6, true, sqlx.Tokenize, "")
	if len(lines) != 6 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 24 {
			t.Fatalf("line %d is %d cells: %q", i, w, ansi.Strip(l))
		}
	}
	if e.top != 25 || cy != 5 || cx != 23 || e.left == 0 {
		t.Fatalf("top=%d cursor=%d,%d left=%d", e.top, cx, cy, e.left)
	}
	if !strings.Contains(ansi.Strip(lines[5]), "31") {
		t.Fatalf("line numbers: %q", ansi.Strip(lines[5]))
	}
	if e.hl[0][0] != "keyword" || e.hl[0][39] != "comment" {
		t.Fatalf("%v", e.hl[0])
	}
	// an error position marks the line
	e.errLine, e.errCol = 31, 1
	lines, _, _ = e.view(24, 6, true, sqlx.Tokenize, "")
	if !strings.Contains(ansi.Strip(lines[5]), "✗") {
		t.Fatalf("%q", ansi.Strip(lines[5]))
	}
	e.insert("z")
	if e.errLine != 0 {
		t.Fatal("editing clears the error mark")
	}
	empty := newEditor("")
	lines, _, _ = empty.view(30, 3, true, sqlx.Tokenize, "type SQL here")
	if !strings.Contains(ansi.Strip(lines[0]), "type SQL here") || ansi.StringWidth(lines[0]) != 30 {
		t.Fatalf("%q", ansi.Strip(lines[0]))
	}
}
