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
	e, v, _ := vimDoOut(t, text, keys...)
	return e, v
}

// vimDoOut is vimDo that also returns what the keys asked of the model: the
// last action, toast and clipboard text, and whether any key opened the
// search prompt.
func vimDoOut(t *testing.T, text string, keys ...string) (*editor, *vim, vimOut) {
	t.Helper()
	var all vimOut
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
			out := v.key(e, k, 10)
			all.prompt = all.prompt || out.prompt
			if out.action != "" {
				all.action = out.action
			}
			if out.toast != "" {
				all.toast = out.toast
			}
			if out.clip != "" {
				all.clip = out.clip
			}
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
	return e, v, all
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
	r.press("ctrl+w", "l")
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

// ctrl+p is kept from bqt in every vim mode (spec 9.1): it opens the command
// palette, also in Normal mode.
func TestCtrlPOpensThePaletteInNormalMode(t *testing.T) {
	r := vimRig(t, 120, 30)
	if r.m.buf().vim.mode != vimNormal {
		t.Fatalf("mode %v", r.m.buf().vim.mode)
	}
	r.press("ctrl+p")
	if r.m.ov != ovPalette {
		t.Fatalf("ctrl+p in Normal mode: overlay %v", r.m.ov)
	}
}

// TestVimKeyPaths covers the key paths the motion and operator table above
// leaves out: the second key of a two-key command, counts, the leader, what
// the keys ask of the model, and Visual mode's own keys. pending is what is
// still pending after the keys, as the pane title shows it ("" when idle).
// The clipboard text is compared only where a case names it.
func TestVimKeyPaths(t *testing.T) {
	cases := []struct {
		name, in string
		keys     []string
		want     string
		mode     vimMode
		out      vimOut
		pending  string
	}{
		// the second key of f F t T, r and g
		{name: "f space", in: "|a b", keys: []string{"f "}, want: "a| b"},
		{name: "f then a named key cancels it and the count", in: "|abc", keys: []string{"2f", "esc", "x"}, want: "|bc"},
		{name: "f with no match cancels the count", in: "|abc", keys: []string{"2fzx"}, want: "|bc"},
		{name: "r space", in: "a|bc", keys: []string{"r "}, want: "a| c"},
		{name: "r past the line end does nothing", in: "|ab", keys: []string{"3rx"}, want: "|ab"},
		{name: "r then a named key cancels it", in: "|ab", keys: []string{"r", "esc"}, want: "|ab"},
		{name: "gr opens the run list", in: "|ab", keys: []string{"gr"}, want: "|ab", out: vimOut{action: "runs"}},
		{name: "g then an unknown key cancels it", in: "|ab", keys: []string{"gzx"}, want: "|b"},
		{name: "count gg", in: "one\ntwo\n|three", keys: []string{"2gg"}, want: "one\n|two\nthree"},
		{name: "dgg", in: "one\n|two\nthree", keys: []string{"dgg"}, want: "|three"},

		// the space leader
		{name: "space r runs", in: "|a", keys: []string{"space", "r"}, want: "|a", out: vimOut{action: "run"}},
		{name: "space y hands off", in: "|a", keys: []string{"space", "y"}, want: "|a", out: vimOut{action: "handoff"}},
		{name: "space then an unbound key", in: "|a", keys: []string{"space", "z"}, want: "|a", out: vimOut{toast: "space z is not bound (space r runs, space d dry-runs)"}},
		{name: "space waits for the leader key", in: "|a", keys: []string{"space"}, want: "|a", pending: "leader"},
		{name: "a count makes space a motion", in: "|abc", keys: []string{"2", "space"}, want: "ab|c"},

		// counts and pending operators
		{name: "esc clears a count", in: "|abc", keys: []string{"2", "esc", "x"}, want: "|bc"},
		{name: "a count is pending", in: "|abc", keys: []string{"12"}, want: "|abc", pending: "12"},
		{name: "an operator count is pending", in: "|abc", keys: []string{"2d3"}, want: "|abc", pending: "32d"},
		{name: "operator count times motion count", in: "|a b c d e f g h", keys: []string{"2d3w"}, want: "|g h"},
		{name: "0 after a count is a digit", in: "|" + strings.Repeat("x", 12), keys: []string{"10x"}, want: "|xx"},
		{name: "d i then an unknown object cancels it", in: "|a b", keys: []string{"diz"}, want: "|a b"},
		{name: "a text object that is not there cancels the operator", in: "|abc", keys: []string{`di"x`}, want: "|bc"},
		{name: "db deletes backwards", in: "a b |c", keys: []string{"db"}, want: "a |c"},
		{name: "dk", in: "one\n|two\nthree", keys: []string{"dk"}, want: "|three"},
		{name: "k", in: "one\n|two", keys: []string{"k"}, want: "|one\ntwo"},
		{name: "d3w stops before blank lines", in: "|a b\n\nc", keys: []string{"d3w"}, want: "|\n\nc"},
		{name: "d% with no bracket does nothing", in: "|abc", keys: []string{"d%"}, want: "|abc"},
		{name: "; before any f does nothing", in: "|a,b", keys: []string{";"}, want: "|a,b"},
		{name: "{", in: "a\n\nb\n|c", keys: []string{"{"}, want: "a\n|\nb\nc"},

		// cw
		{name: "cw on the last letter of a word", in: "selec|t a", keys: []string{"cwX", "esc"}, want: "selec|X a"},
		{name: "2cw on the last letter of a word is 2ce", in: "selec|t a b", keys: []string{"2cwX", "esc"}, want: "selec|X"},
		{name: "cW", in: "|a.b c", keys: []string{"cWX", "esc"}, want: "|X c"},
		{name: "cw on blanks changes them", in: "a|  b", keys: []string{"cwX", "esc"}, want: "a|Xb"},

		// commands that ask the model
		{name: ": opens the palette", in: "|a", keys: []string{":"}, want: "|a", out: vimOut{action: "palette"}},
		{name: "? opens the help", in: "|a", keys: []string{"?"}, want: "|a", out: vimOut{action: "help"}},
		{name: "/ opens the search prompt", in: "|a", keys: []string{"/"}, want: "|a", out: vimOut{prompt: true}},
		{name: "enter runs", in: "|a", keys: []string{"enter"}, want: "|a", out: vimOut{action: "run"}},
		{name: ". repeats", in: "|a", keys: []string{"."}, want: "|a", out: vimOut{action: "vimrepeat"}},
		{name: "* on an empty line does nothing", in: "a\n|\na", keys: []string{"*"}, want: "a\n|\na"},
		{name: "u at the oldest change", in: "|a", keys: []string{"u"}, want: "|a", out: vimOut{toast: "already at the oldest change"}},
		{name: "ctrl+r at the newest change", in: "|a", keys: []string{"ctrl+r"}, want: "|a", out: vimOut{toast: "already at the newest change"}},
		{name: "Y yanks lines", in: "|one\ntwo", keys: []string{"Yp"}, want: "one\n|one\ntwo", out: vimOut{clip: "one"}},
		{name: "3Y says how many lines", in: "|a\nb\nc", keys: []string{"3Y"}, want: "|a\nb\nc", out: vimOut{clip: "a\nb\nc", toast: "3 lines yanked"}},
		{name: "<< takes a tab", in: "\t|a", keys: []string{"<<"}, want: "|a"},
		{name: "J with a count joins count-1 times", in: "|a\nb\nc\nd", keys: []string{"3J"}, want: "a b| c\nd"},
		{name: "x on an empty line does nothing", in: "|\na", keys: []string{"x"}, want: "|\na"},
		{name: "~ on an empty line does nothing", in: "|\na", keys: []string{"~"}, want: "|\na"},

		// text objects
		{name: "diw on an empty line", in: "a\n|\nb", keys: []string{"diw"}, want: "a\n|\nb"},
		{name: "daw takes the leading blanks at the line end", in: "a |b", keys: []string{"daw"}, want: "|a"},
		{name: `di" between two pairs`, in: `"a" | "b"`, keys: []string{`di"`}, want: `"a" | "b"`},
		{name: `di" before the first pair`, in: `x| "a"`, keys: []string{`di"`}, want: `x "|"`},
		{name: "di{", in: "f{a|b}", keys: []string{"di{"}, want: "f{|}"},
		{name: "diB", in: "f{a|b}", keys: []string{"diB"}, want: "f{|}"},
		{name: "di<", in: "a<b|c>", keys: []string{"di<"}, want: "a<|>"},
		{name: "di( skips a closed pair before the cursor", in: "(a (b) |c)", keys: []string{"di("}, want: "(|)"},
		{name: "di( with no opener", in: "a|b)", keys: []string{"di("}, want: "a|b)"},
		{name: "di( with no closer", in: "(a|b", keys: []string{"di("}, want: "(a|b"},

		// Visual mode
		{name: "v esc", in: "|abc", keys: []string{"vl", "esc"}, want: "a|bc"},
		{name: "v v leaves", in: "|abc", keys: []string{"vlv"}, want: "a|bc"},
		{name: "V v is charwise", in: "|abc", keys: []string{"Vlv"}, want: "a|bc", mode: vimVisual},
		{name: "V V leaves", in: "|abc", keys: []string{"VV"}, want: "|abc"},
		{name: "v V is linewise", in: "|abc", keys: []string{"vV"}, want: "|abc", mode: vimVisualLine},
		{name: "v x", in: "|abc", keys: []string{"vlx"}, want: "|c"},
		{name: "v delete", in: "|abc", keys: []string{"vl", "delete"}, want: "|c"},
		{name: "v s", in: "|abc", keys: []string{"vlsX", "esc"}, want: "|Xc"},
		{name: "v p with a line register", in: "|one\ntwo", keys: []string{"yyjvp"}, want: "one\n|one\nwo"},
		{name: "V p with a charwise register", in: "|ab\ncd\nef", keys: []string{"yljVp"}, want: "ab\n|a\nef"},
		{name: "v p with an empty register", in: "|ab", keys: []string{"vp"}, want: "|b"},
		{name: "V J", in: "|a\nb\nc", keys: []string{"VjJ"}, want: "a| b\nc"},
		{name: "v u", in: "|ABC", keys: []string{"veu"}, want: "|abc"},
		{name: "v ~ on capitals", in: "|aBc", keys: []string{"ve~"}, want: "|AbC"},
		{name: "V U", in: "|ab\ncd", keys: []string{"VU"}, want: "|AB\ncd"},
		{name: "v f", in: "|a,b", keys: []string{"vf,d"}, want: "|b"},
		{name: "v gg", in: "a\n|b", keys: []string{"vggd"}, want: "|"},
		{name: "v count", in: "|abcd", keys: []string{"v2ld"}, want: "|d"},
		{name: "v i( across lines", in: "in (\n  1,\n  |2\n)", keys: []string{"vi(d"}, want: "in |(\n)"},
		{name: "v i( that is not there stays in Visual mode", in: "|abc", keys: []string{"vi("}, want: "|abc", mode: vimVisual},
		{name: "v :", in: "|a", keys: []string{"v:"}, want: "|a", mode: vimVisual, out: vimOut{action: "palette"}},
		{name: "v /", in: "|a", keys: []string{"v/"}, want: "|a", mode: vimVisual, out: vimOut{prompt: true}},
		{name: "v enter", in: "|a", keys: []string{"v", "enter"}, want: "|a", mode: vimVisual, out: vimOut{action: "run"}},
		{name: "v space r", in: "|a", keys: []string{"v", "space", "r"}, want: "|a", mode: vimVisual, out: vimOut{action: "run"}},
		{name: "v y", in: "|ab cd", keys: []string{"vey"}, want: "|ab cd", out: vimOut{clip: "ab"}},
		{name: "v an unknown key", in: "|ab", keys: []string{"vz"}, want: "|ab", mode: vimVisual},
	}
	for _, c := range cases {
		e, v, out := vimDoOut(t, c.in, c.keys...)
		if got := marked(e); got != c.want {
			t.Errorf("%s: %q + %q\n  got  %q\n  want %q", c.name, c.in, c.keys, got, c.want)
		}
		if v.mode != c.mode {
			t.Errorf("%s: ends in %s, want %s", c.name, v.mode, c.mode)
		}
		if c.out.clip == "" {
			out.clip = ""
		}
		if out != c.out {
			t.Errorf("%s: out %+v, want %+v", c.name, out, c.out)
		}
		pending := ""
		if v.hasCount {
			pending += itoa(v.count)
		}
		if v.opCount > 0 {
			pending += itoa(v.opCount)
		}
		pending += v.op + v.wait
		if pending != c.pending {
			t.Errorf("%s: pending %q, want %q", c.name, pending, c.pending)
		}
	}
}

// TestVimScrollAndSearchMisses covers the page motions, which move by the
// viewport height, and a search with no match.
func TestVimScrollAndSearchMisses(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, "l"+itoa(i))
	}
	text := strings.Join(lines, "\n")
	for _, c := range []struct {
		keys []string
		row  int
	}{
		{[]string{"ctrl+d"}, 5},
		{[]string{"ctrl+f"}, 9},
		{[]string{"pgdown"}, 9},
		{[]string{"G", "ctrl+u"}, 34},
		{[]string{"G", "ctrl+b"}, 30},
		{[]string{"G", "pgup"}, 30},
		{[]string{"ctrl+u"}, 0},
		{[]string{"2", "ctrl+d"}, 5}, // the count does not scale a page
	} {
		e, _ := vimDo(t, text, c.keys...)
		if e.row != c.row {
			t.Errorf("%q: row %d, want %d", c.keys, e.row, c.row)
		}
	}
	e, v := vimDo(t, "a |b c")
	v.search = "zzz"
	v.key(e, "n", 10)
	if marked(e) != "a |b c" {
		t.Fatalf("a search with no match stays: %q", marked(e))
	}
}
