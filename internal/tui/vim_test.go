package tui

import (
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

// vimDo feeds keys to the vim layer over a bare editor, the way the model
// does: Normal and Visual keys to the layer, Insert keys to the editor. The
// text may hold one "|" for the cursor.
func vimDo(t *testing.T, text string, keys ...string) (*editor, *vim) {
	t.Helper()
	at := strings.Index(text, "|")
	e := newEditor(strings.Replace(text, "|", "", 1))
	e.row, e.col = 0, 0
	if at >= 0 {
		e.row, e.col = e.position(at)
	}
	e.want = e.col
	v := &vim{}
	feed := func(k string) {
		if v.idle() {
			e.beginGroup()
		}
		if v.mode == vimInsert {
			if k == "esc" {
				v.mode = vimNormal
				if e.col > 0 {
					e.col--
				}
				e.clampNormal()
			} else {
				e.handle(edKey(k), 10)
			}
		} else {
			v.key(e, k, 10)
		}
		if v.idle() {
			e.endGroup()
		}
	}
	for _, k := range keys {
		if len([]rune(k)) > 1 && !isNamedKey(k) && !strings.HasPrefix(k, "ctrl+") {
			for _, ch := range k {
				name := string(ch)
				if ch == ' ' {
					name = "space"
				}
				feed(name)
			}
			continue
		}
		feed(k)
	}
	return e, v
}

// marked renders the text with the cursor as "|".
func marked(e *editor) string {
	off := e.offset()
	s := e.text()
	return s[:off] + "|" + s[off:]
}

func TestVimMotionsAndOperators(t *testing.T) {
	cases := []struct {
		name, in, keys, want string
	}{
		{"w", "|select a, b from t", "ww", "select a|, b from t"},
		{"W", "|select a, b from t", "WW", "select a, |b from t"},
		{"b", "select a, b |from t", "bb", "select a|, b from t"},
		{"e", "|select a, b", "ee", "select |a, b"},
		{"0 ^ $", "  select |a", "0", "|  select a"},
		{"^", "  select |a", "^", "  |select a"},
		{"$ stays on the last char", "|select a", "$", "select |a"},
		{"count motion", "|a b c d e", "3w", "a b c |d e"},
		{"gg and G", "one\ntw|o\nthree", "gg", "|one\ntwo\nthree"},
		{"G", "|one\ntwo\n  three", "G", "one\ntwo\n  |three"},
		{"count G", "|one\ntwo\nthree", "2G", "one\n|two\nthree"},
		{"f and ;", "|a(b, c, d)", "f,;", "a(b, c|, d)"},
		{"t", "|a(b, c)", "t,", "a(|b, c)"},
		{"F and ,", "a, b, c|", "F,,", "a, b|, c"},
		{"%", "f|(a, (b))", "%", "f(a, (b)|)"},
		{"}", "|a\nb\n\nc", "}", "a\nb\n|\nc"},
		{"j keeps the column", "abc|def\nab\nabcdef", "jj", "abcdef\nab\nabc|def"},

		{"x", "a|bc", "x", "a|c"},
		{"count x", "|abcdef", "3x", "|def"},
		{"X", "ab|c", "X", "a|c"},
		{"dw", "|select a from t", "dw", "|a from t"},
		{"dw on the last word keeps the line break", "select |a\nfrom t", "dw", "select| \nfrom t"},
		{"d2w", "|a b c d", "d2w", "|c d"},
		{"2dw", "|a b c d", "2dw", "|c d"},
		{"de", "|select a", "de", "| a"},
		{"d$ and D", "select |a from t", "D", "select| "},
		{"dd", "one\nt|wo\nthree", "dd", "one\n|three"},
		{"dd on the last line", "one\nt|wo", "dd", "|one"},
		{"2dd", "|one\ntwo\nthree", "2dd", "|three"},
		{"dj", "|one\ntwo\nthree", "dj", "|three"},
		{"dG", "one\n|two\nthree", "dG", "|one"},
		{"df", "|a(b, c)", "df,", "| c)"},
		{"dt", "|a(b, c)", "dt,", "|, c)"},
		{"d%", "f|(a, (b)) x", "d%", "f| x"},
		{"dd leaves one empty line", "|only", "dd", "|"},

		{"cw then type", "|select a", "cwSELECT", "esc"},
		{"cc keeps the indent", "  |old line\nnext", "ccnew", "esc"},
		{"C", "select |a from t", "C*", "esc"},
		{"s", "a|bc", "sX", "esc"},
		{"S", "one\n|two", "Sx", "esc"},

		{"diw", "select cust|omer_id from t", "diw", "select | from t"},
		{"daw", "select cust|omer_id from t", "daw", "select |from t"},
		{"ciw", "select |a from t", "ciwb", "esc"},
		{`di"`, `where x = "ab|c" and`, `di"`, `where x = "|" and`},
		{`da'`, `where x = 'ab|c' and`, `da'`, `where x = | and`},
		{"di`", "from `p.d|.t` x", "di`", "from `|` x"},
		{"di(", "count(dist|inct x) y", "di(", "count(|) y"},
		{"da(", "count(dist|inct x) y", "da(", "count| y"},
		{"dib nested", "f(a, g(b|, c), d)", "dib", "f(a, g(|), d)"},
		{"di( across lines", "in (\n  1,\n  |2\n)", "di(", "in (|)"},
		{"ci[", "a[i|dx]", "ci[0", "esc"},

		{"yy p", "|one\ntwo", "yyp", "one\n|one\ntwo"},
		{"yy P", "one\n|two", "yyP", "one\n|two\ntwo"},
		{"yw P", "|ab cd", "ywP", "ab| ab cd"},
		{"x p swaps", "|ab", "xp", "b|a"},
		{"dd p moves a line", "|one\ntwo", "ddp", "two\n|one"},
		{"3p", "|a", "yl3p", "aaa|a"},

		{"r", "a|bc", "rX", "a|Xc"},
		{"3r", "|abcd", "3r-", "--|-d"},
		{"J", "|select\n   a", "J", "select| a"},
		{"~", "|abC", "3~", "AB|c"},
		{">>", "|a\nb", ">>", "  |a\nb"},
		{"<<", "    |a", "<<", "  |a"},
		{">j", "|a\nb\nc", ">j", "  |a\n  b\nc"},

		{"i", "a|b", "iX", "esc"},
		{"a", "a|b", "aX", "esc"},
		{"I", "  a|b", "IX", "esc"},
		{"A", "|ab", "AX", "esc"},
		{"o keeps the indent", "  |a\nb", "ox", "esc"},
		{"O", "a\n|b", "Ox", "esc"},

		{"v d is inclusive of the cursor", "|select a from t", "vwwd", "|rom t"},
		{"v e d is inclusive", "|select a", "ved", "| a"},
		{"V d", "one\n|two\nthree", "Vjd", "|one"},
		{"v y p", "|ab cd", "vey$p", "ab cda|b"},
		{"v c", "|abc def", "vecX", "esc"},
		{"viw", "select cu|st from", "viwd", "select | from"},
		{"vi( then d", "f(a|, b) x", "vi(d", "f(|) x"},
		{"v > indents the lines", "|a\nb\nc", "Vj>", "  |a\n  b\nc"},
		{"v ~", "|abc", "ve~", "|ABC"},
		{"v U", "|select a", "veU", "|SELECT a"},
		{"v o swaps the ends", "a|bcd", "vlohd", "|d"},
		{"v p replaces", "|aa bb", "yiwwviwp", "aa a|a"},

		{"esc clears a pending operator", "|a b", "d", "|a b"},
		{"an unknown key after an operator cancels it", "|a b", "dzx", "|a b"},
	}
	for _, c := range cases {
		keys := []string{c.keys}
		want := c.want
		if c.want == "esc" { // an insert command: finish with esc and compare below
			keys = append(keys, "esc")
		}
		e, v := vimDo(t, c.in, keys...)
		got := marked(e)
		if c.want == "esc" {
			want = map[string]string{
				"cw then type": "SELEC|T a", "cc keeps the indent": "  ne|w\nnext", "C": "select |*", "s": "a|Xc", "S": "one\n|x",
				"ciw": "select |b from t", "ci[": "a[|0]", "i": "a|Xb", "a": "ab|X", "I": "  |Xab", "A": "ab|X",
				"o keeps the indent": "  a\n  |x\nb", "O": "a\n|x\nb", "v c": "|X def",
			}[c.name]
		}
		if c.name == "an unknown key after an operator cancels it" {
			want = "| b" // d is cancelled by z, then x deletes one character
		}
		if got != want {
			t.Errorf("%s: %q + %s\n  got  %q\n  want %q", c.name, c.in, c.keys, got, want)
		}
		if v.mode != vimNormal && c.name != "esc clears a pending operator" {
			t.Errorf("%s: ends in %s", c.name, v.mode)
		}
	}
}

func TestVimUndoIsOneStepPerCommand(t *testing.T) {
	e, _ := vimDo(t, "|one two three", "dw", "cwONE", "esc", "A and more\nlines", "esc")
	if e.text() != "ONE three and more\nlines" {
		t.Fatalf("%q", e.text())
	}
	for _, want := range []string{"ONE three", "two three", "one two three"} {
		vimFeed(e, "u")
		if e.text() != want {
			t.Fatalf("undo: got %q want %q", e.text(), want)
		}
	}
	vimFeed(e, "ctrl+r", "ctrl+r")
	if e.text() != "ONE three" {
		t.Fatalf("redo: %q", e.text())
	}
}

func vimFeed(e *editor, keys ...string) {
	v := &vim{}
	for _, k := range keys {
		e.beginGroup()
		v.key(e, k, 10)
		e.endGroup()
	}
}

func TestVimSearch(t *testing.T) {
	e, v := vimDo(t, "|select Amount, amount\nfrom t where amount > 1")
	v.search = "amount"
	v.key(e, "n", 10)
	if marked(e) != "select |Amount, amount\nfrom t where amount > 1" {
		t.Fatalf("lower case ignores case: %q", marked(e))
	}
	v.key(e, "n", 10)
	v.key(e, "n", 10)
	if e.row != 1 {
		t.Fatalf("%q", marked(e))
	}
	v.key(e, "n", 10) // wraps
	if e.row != 0 || e.col != 7 {
		t.Fatalf("wrap: %q", marked(e))
	}
	v.key(e, "N", 10)
	if e.row != 1 {
		t.Fatalf("N goes back, wrapping: %q", marked(e))
	}
	v.search = "Amount"
	v.key(e, "n", 10)
	if e.row != 0 || e.col != 7 {
		t.Fatalf("a capital makes it case-sensitive: %q", marked(e))
	}
	e2, v2 := vimDo(t, "x |total y total", "*")
	if marked(e2) != "x total y |total" || v2.search != "total" {
		t.Fatalf("*: %q", marked(e2))
	}
}

func vimRig(t *testing.T, w, h int) *rig {
	t.Helper()
	rigEditing = "vim"
	defer func() { rigEditing = "plain" }()
	r := newRig(t, w, h)
	r.press("ctrl+o")
	return r
}

func TestVimModesInTheUI(t *testing.T) {
	r := vimRig(t, 120, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(7) }
	r.contains("NORMAL", "i inserts")
	// printable keys are commands in Normal mode, text in Insert mode
	r.press("i")
	r.contains("INSERT")
	r.typeText("select one")
	r.press("esc")
	if r.text() != "select one" || r.m.buf().vim.mode != vimNormal {
		t.Fatalf("%q %s", r.text(), r.m.buf().vim.mode)
	}
	// ctrl+r is redo in Normal mode, never a run
	r.press("u")
	if r.text() != "" {
		t.Fatalf("u undoes the whole insert: %q", r.text())
	}
	r.press("ctrl+r")
	if r.text() != "select one" || len(r.fake.QueryCalls()) != 0 {
		t.Fatalf("ctrl+r must redo, not run: %q, %d queries", r.text(), len(r.fake.QueryCalls()))
	}
	// enter runs the buffer
	r.press("enter")
	r.contains("#1 done", "7 rows")
	// space d is the dry-run; ctrl+d scrolls
	r.press("ctrl+d")
	if n := len(r.fake.QueryCalls()); n != 2 {
		t.Fatalf("ctrl+d must not dry-run in Normal mode (%d queries)", n)
	}
	r.press("space", "d")
	r.contains("dry-run:")
	// in Insert mode ctrl+r runs and ctrl+d dry-runs, as in plain editing
	r.press("A")
	r.typeText(" -- x")
	r.press("ctrl+r")
	r.contains("#3 done")
	if !strings.HasSuffix(r.fake.QueryCalls()[len(r.fake.QueryCalls())-1].Req.SQL, "-- x") {
		t.Fatal("the run uses the text as typed")
	}
	r.press("esc")
	// : opens the palette, ? the help, and the registry's ctrl keys still work
	r.press(":")
	if r.m.ov != ovPalette {
		t.Fatal(": opens the palette")
	}
	r.press("esc", "?")
	if r.m.ov != ovHelp || !strings.Contains(strings.Join(helpLines(80), "\n"), "vim editing") {
		t.Fatal("? opens the help, which documents the vim keys")
	}
	r.press("esc", "ctrl+x", "n")
	if len(r.m.bufs) != 2 || r.m.buf().vim.mode != vimNormal {
		t.Fatal("ctrl+x n works in Normal mode; a new buffer starts in Normal mode")
	}
	r.press("space", "[")
	if r.m.cur != 0 {
		t.Fatal("space [ switches buffers")
	}
}

func TestVimVisualRunsTheSelectionAndDotRepeats(t *testing.T) {
	r := vimRig(t, 120, 30)
	r.press("i")
	r.typeText("select 1;\nselect 2")
	r.press("esc", "V", "enter")
	calls := r.fake.QueryCalls()
	if got := calls[len(calls)-1].Req.SQL; got != "select 2" {
		t.Fatalf("visual + enter runs the selection: %q", got)
	}
	if r.m.buf().vim.mode != vimNormal {
		t.Fatal("back to Normal mode after the run")
	}
	// dot repeats the last change, including what was typed
	r.press("g", "g", "c", "w")
	r.typeText("SELECT")
	r.press("esc", "j", "0", ".")
	if r.text() != "SELECT 1;\nSELECT 2" {
		t.Fatalf("%q", r.text())
	}
	r.press("x", ".", ".")
	if r.text() != "SELECT 1;\nSELEC" {
		t.Fatalf("x then two repeats: %q", r.text())
	}
	r.press("u", "u", "u")
	if r.text() != "SELECT 1;\nSELECT 2" {
		t.Fatalf("each repeat is its own undo step: %q", r.text())
	}
	// y also goes to the system clipboard; / finds
	r.press("/")
	r.contains("Find in the buffer")
	r.typeText("2")
	r.press("enter")
	if ed := r.m.buf().ed; ed.row != 1 || ed.col != 7 {
		t.Fatalf("/ jumped to %d:%d", ed.row, ed.col)
	}
	// the draft holds the text
	r.m.saveDrafts()
	if b := r.m.buf(); b.draftVersion != b.ed.version {
		t.Fatal("changes made in Normal mode reach the draft")
	}
}

func TestVimCompletionInInsertMode(t *testing.T) {
	r := vimRig(t, 120, 30)
	r.press("i")
	r.typeText("select * from `acme-p")
	if !r.m.comp.open {
		t.Fatal("the popup works in Insert mode")
	}
	r.press("tab")
	if r.text() != "select * from `acme-prod." {
		t.Fatalf("%q", r.text())
	}
	// esc closes the popup and leaves Insert mode in one press
	r.press("esc")
	if r.m.comp.open || r.m.buf().vim.mode != vimNormal {
		t.Fatal("esc leaves Insert mode")
	}
	r.press("a", "ctrl+n")
	if !r.m.comp.open {
		t.Fatal("ctrl+n forces completion in Insert mode")
	}
}

func TestPlainEditingIsStillAvailable(t *testing.T) {
	r := editorRig(t, 120, 30) // [tui] editing = "plain"
	r.typeText("dd")
	if r.text() != "dd" {
		t.Fatalf("plain editing types every key: %q", r.text())
	}
	r.lacks("NORMAL", "INSERT")
}
