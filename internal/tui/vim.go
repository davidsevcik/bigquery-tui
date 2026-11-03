package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Vim-style editing for the query editor: Normal, Insert, Visual and
// Visual-line modes over the same editor widget. It is a deliberate subset
// (spec 9.1): counts, the usual motions, the d c y > < operators with motions
// and text objects, one unnamed register, search, and dot-repeat.

type vimMode int

const (
	vimNormal vimMode = iota
	vimInsert
	vimVisual
	vimVisualLine
)

func (m vimMode) String() string {
	return [...]string{"NORMAL", "INSERT", "VISUAL", "V-LINE"}[m]
}

// vim is the per-buffer state of the vim layer.
type vim struct {
	mode vimMode

	count    int
	hasCount bool
	op       string // pending operator: d c y > <
	opCount  int
	wait     string // a key that needs one more: g f F t T r i a (text object) and the space leader

	reg     string // the unnamed register
	regLine bool

	findKind string // last f F t T, for ; and ,
	findCh   rune
	search   string

	anchorR, anchorC int // where visual mode started

	// dot-repeat: the keys of the command in progress, and of the last change
	rec     []tea.KeyPressMsg
	last    []tea.KeyPressMsg
	idleVer int
	tainted bool // the command did something replay cannot reproduce
	replay  bool
}

// vimOut is what a key asks the model to do beyond editing the text.
type vimOut struct {
	action string // an editor action from the registry (run, dryrun, palette, ...)
	clip   string // text for the system clipboard
	toast  string
	prompt bool // open the search prompt
}

func (v *vim) idle() bool {
	return v.mode == vimNormal && v.op == "" && v.wait == "" && !v.hasCount
}

func (v *vim) reset() {
	v.count, v.hasCount, v.op, v.opCount, v.wait = 0, false, "", 0, ""
}

// n is the effective count (operator count times motion count).
func (v *vim) n() int {
	c := 1
	if v.hasCount {
		c = v.count
	}
	if v.opCount > 0 {
		c *= v.opCount
	}
	return max(1, c)
}

// --- helpers over the editor

func (e *editor) lineLen(r int) int { return len(e.lines[r]) }

// clampNormal keeps the cursor on a character (Normal mode never sits past
// the end of a line).
func (e *editor) clampNormal() {
	e.row = clamp(e.row, 0, len(e.lines)-1)
	e.col = clamp(e.col, 0, max(0, e.lineLen(e.row)-1))
}

func (e *editor) firstNonBlank(r int) int {
	c := 0
	for c < len(e.lines[r]) && unicode.IsSpace(e.lines[r][c]) {
		c++
	}
	return c
}

// textRange returns the text between two ordered positions (end exclusive).
func (e *editor) textRange(r1, c1, r2, c2 int) string {
	if r1 == r2 {
		return string(e.lines[r1][c1:c2])
	}
	var b strings.Builder
	b.WriteString(string(e.lines[r1][c1:]))
	for r := r1 + 1; r < r2; r++ {
		b.WriteByte('\n')
		b.WriteString(string(e.lines[r]))
	}
	b.WriteByte('\n')
	b.WriteString(string(e.lines[r2][:c2]))
	return b.String()
}

func (e *editor) linesText(r1, r2 int) string {
	var parts []string
	for r := r1; r <= r2; r++ {
		parts = append(parts, string(e.lines[r]))
	}
	return strings.Join(parts, "\n")
}

// deleteLines removes whole lines, leaving one empty line in an emptied buffer.
func (e *editor) deleteLines(r1, r2 int) {
	e.lines = append(e.lines[:r1], e.lines[r2+1:]...)
	if len(e.lines) == 0 {
		e.lines = [][]rune{{}}
	}
	e.row = min(r1, len(e.lines)-1)
	e.col = e.firstNonBlank(e.row)
}

type charClass int

func classOf(r rune, big bool) charClass {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big || isWordRune(r):
		return 1
	}
	return 2
}

func (e *editor) charAt(r, c int) (rune, bool) {
	if r < 0 || r >= len(e.lines) || c < 0 || c >= len(e.lines[r]) {
		return 0, false
	}
	return e.lines[r][c], true
}

// wordForward is w / W: the start of the next word.
func (e *editor) wordForward(r, c int, big bool) (int, int) {
	ch, ok := e.charAt(r, c)
	start := charClass(0)
	if ok {
		start = classOf(ch, big)
	}
	// leave the current word
	for ok && classOf(ch, big) == start && start != 0 {
		if c+1 >= e.lineLen(r) {
			if r == len(e.lines)-1 {
				return r, e.lineLen(r)
			}
			r, c = r+1, 0
			if e.lineLen(r) == 0 {
				return r, 0 // an empty line is a word
			}
			break
		}
		c++
		ch, ok = e.charAt(r, c)
	}
	// skip blanks
	for {
		ch, ok = e.charAt(r, c)
		if ok && classOf(ch, big) != 0 {
			return r, c
		}
		if c+1 < e.lineLen(r) {
			c++
			continue
		}
		if r == len(e.lines)-1 {
			return r, e.lineLen(r)
		}
		r, c = r+1, 0
		if e.lineLen(r) == 0 {
			return r, 0
		}
	}
}

// wordBack is b / B: the start of the previous word.
func (e *editor) wordBack(r, c int, big bool) (int, int) {
	step := func() bool {
		if c > 0 {
			c--
			return true
		}
		if r > 0 {
			r--
			c = max(0, e.lineLen(r)-1)
			return true
		}
		return false
	}
	if !step() {
		return r, c
	}
	for {
		ch, ok := e.charAt(r, c)
		if ok && classOf(ch, big) != 0 {
			break
		}
		if !ok && e.lineLen(r) == 0 {
			return r, 0
		}
		if !step() {
			return r, c
		}
	}
	ch, _ := e.charAt(r, c)
	cls := classOf(ch, big)
	for c > 0 {
		p, _ := e.charAt(r, c-1)
		if classOf(p, big) != cls {
			break
		}
		c--
	}
	return r, c
}

// wordEnd is e / E: the end of the current or next word.
func (e *editor) wordEnd(r, c int, big bool) (int, int) {
	adv := func() bool {
		if c+1 < e.lineLen(r) {
			c++
			return true
		}
		if r < len(e.lines)-1 {
			r, c = r+1, 0
			return true
		}
		return false
	}
	if !adv() {
		return r, c
	}
	for {
		ch, ok := e.charAt(r, c)
		if ok && classOf(ch, big) != 0 {
			break
		}
		if !adv() {
			return r, c
		}
	}
	ch, _ := e.charAt(r, c)
	cls := classOf(ch, big)
	for c+1 < e.lineLen(r) {
		nx, _ := e.charAt(r, c+1)
		if classOf(nx, big) != cls {
			break
		}
		c++
	}
	return r, c
}

var bracketPairs = map[rune]rune{'(': ')', '[': ']', '{': '}', ')': '(', ']': '[', '}': '{'}

// matchBracket is %: the bracket matching the first one at or after the cursor.
func (e *editor) matchBracket() (int, int, bool) {
	r, c := e.row, e.col
	for c < e.lineLen(r) {
		if _, ok := bracketPairs[e.lines[r][c]]; ok {
			break
		}
		c++
	}
	open, ok := e.charAt(r, c)
	if !ok {
		return 0, 0, false
	}
	closeCh := bracketPairs[open]
	dir := 1
	if open == ')' || open == ']' || open == '}' {
		dir = -1
	}
	depth := 0
	for {
		ch, ok := e.charAt(r, c)
		if ok {
			switch ch {
			case open:
				depth++
			case closeCh:
				depth--
				if depth == 0 {
					return r, c, true
				}
			}
		}
		if dir > 0 {
			if c+1 < e.lineLen(r) {
				c++
			} else if r < len(e.lines)-1 {
				r, c = r+1, 0
			} else {
				return 0, 0, false
			}
		} else {
			if c > 0 {
				c--
			} else if r > 0 {
				r--
				c = max(0, e.lineLen(r)-1)
			} else {
				return 0, 0, false
			}
		}
	}
}

// findChar is f F t T on the cursor line, count times.
func (e *editor) findChar(kind string, ch rune, count int) (int, bool) {
	line := e.lines[e.row]
	c := e.col
	for i := 0; i < count; i++ {
		found := -1
		switch kind {
		case "f", "t":
			from := c + 1
			if kind == "t" && i > 0 {
				from = c + 2
			}
			for j := from; j < len(line); j++ {
				if line[j] == ch {
					found = j
					break
				}
			}
		default:
			from := c - 1
			if kind == "T" && i > 0 {
				from = c - 2
			}
			for j := from; j >= 0; j-- {
				if line[j] == ch {
					found = j
					break
				}
			}
		}
		if found < 0 {
			return 0, false
		}
		c = found
		switch kind {
		case "t":
			c--
		case "T":
			c++
		}
	}
	return c, true
}

// paragraph is { and }: the previous or next blank line.
func (e *editor) paragraph(dir, count int) int {
	r := e.row
	blank := func(i int) bool { return strings.TrimSpace(string(e.lines[i])) == "" }
	for ; count > 0; count-- {
		for r+dir >= 0 && r+dir < len(e.lines) && blank(r) {
			r += dir
		}
		for r+dir >= 0 && r+dir < len(e.lines) && !blank(r) {
			r += dir
		}
	}
	return r
}

// searchFrom finds the next (dir 1) or previous match of pat after the
// cursor, wrapping. The match is case-insensitive unless pat has a capital.
func (e *editor) searchFrom(pat string, dir int) (int, int, bool) {
	if pat == "" {
		return 0, 0, false
	}
	fold := strings.ToLower(pat) == pat
	p := []rune(pat)
	if fold {
		p = []rune(strings.ToLower(pat))
	}
	at := func(r, c int) bool {
		line := e.lines[r]
		if c+len(p) > len(line) {
			return false
		}
		for i, pr := range p {
			lr := line[c+i]
			if fold {
				lr = unicode.ToLower(lr)
			}
			if lr != pr {
				return false
			}
		}
		return true
	}
	n := len(e.lines)
	r, c := e.row, e.col
	for steps := 0; steps <= n; steps++ {
		line := e.lines[r]
		if dir > 0 {
			start := 0
			if steps == 0 {
				start = c + 1
			}
			for j := start; j < len(line); j++ {
				if at(r, j) {
					return r, j, true
				}
			}
			r = (r + 1) % n
		} else {
			start := len(line) - 1
			if steps == 0 {
				start = c - 1
			}
			for j := start; j >= 0; j-- {
				if at(r, j) {
					return r, j, true
				}
			}
			r = (r - 1 + n) % n
		}
	}
	return 0, 0, false
}

// objectBrackets are the bracket text objects' keys and their pairs.
var objectBrackets = map[string][2]rune{
	"(": {'(', ')'}, ")": {'(', ')'}, "b": {'(', ')'},
	"[": {'[', ']'}, "]": {'[', ']'},
	"{": {'{', '}'}, "}": {'{', '}'}, "B": {'{', '}'},
	"<": {'<', '>'}, ">": {'<', '>'},
}

// textObject resolves i/a + key to a range (end exclusive).
func (e *editor) textObject(around bool, key string) (r1, c1, r2, c2 int, ok bool) {
	switch key {
	case "w", "W":
		return e.wordObject(around, key == "W")
	case `"`, "'", "`":
		return e.quoteObject(around, []rune(key)[0])
	}
	if pair, isBracket := objectBrackets[key]; isBracket {
		return e.bracketObject(around, pair[0], pair[1])
	}
	return 0, 0, 0, 0, false
}

// wordObject is iw aw iW aW on the cursor line. aw takes the blanks after the
// word, or those before it when none follow.
func (e *editor) wordObject(around, big bool) (r1, c1, r2, c2 int, ok bool) {
	r := e.row
	line := e.lines[r]
	if len(line) == 0 {
		return r, 0, r, 0, true
	}
	c := min(e.col, len(line)-1)
	cls := classOf(line[c], big)
	a, b := c, c+1
	for a > 0 && classOf(line[a-1], big) == cls {
		a--
	}
	for b < len(line) && classOf(line[b], big) == cls {
		b++
	}
	if around && cls != 0 {
		end := b
		for end < len(line) && unicode.IsSpace(line[end]) {
			end++
		}
		if end == b {
			for a > 0 && unicode.IsSpace(line[a-1]) {
				a--
			}
		}
		b = end
	}
	return r, a, r, b, true
}

// quoteObject is i" a" and the other quotes: the pair the cursor is in, or
// the first one after it on the line. Quotes pair up from the line start.
func (e *editor) quoteObject(around bool, q rune) (r1, c1, r2, c2 int, ok bool) {
	r, c := e.row, e.col
	var qs []int
	for i, ch := range e.lines[r] {
		if ch == q {
			qs = append(qs, i)
		}
	}
	for i := 0; i+1 < len(qs); i += 2 {
		if c <= qs[i+1] { // inside this pair, or before it on the line
			if c < qs[i] && i > 0 {
				continue
			}
			if around {
				return r, qs[i], r, qs[i+1] + 1, true
			}
			return r, qs[i] + 1, r, qs[i+1], true
		}
	}
	return 0, 0, 0, 0, false
}

// bracketObject is i( a( and the other brackets: the pair around the cursor,
// across lines.
func (e *editor) bracketObject(around bool, open, closeCh rune) (r1, c1, r2, c2 int, ok bool) {
	or, oc, found := e.enclosingOpener(open, closeCh)
	if !found {
		return 0, 0, 0, 0, false
	}
	cr, cc, found := e.matchingCloser(or, oc, open, closeCh)
	if !found {
		return 0, 0, 0, 0, false
	}
	if around {
		return or, oc, cr, cc + 1, true
	}
	// inside: step past the opener (to the next line when it ends its line)
	return or, oc + 1, cr, cc, true
}

// enclosingOpener finds the opener of the pair around the cursor, searching
// backwards and skipping the pairs closed on the way.
func (e *editor) enclosingOpener(open, closeCh rune) (int, int, bool) {
	r, c, depth := e.row, e.col, 0
	for {
		if ch, has := e.charAt(r, c); has {
			switch {
			case ch == closeCh && (r != e.row || c != e.col):
				depth++
			case ch == open && depth == 0:
				return r, c, true
			case ch == open:
				depth--
			}
		}
		switch {
		case c > 0:
			c--
		case r > 0:
			r--
			c = max(0, e.lineLen(r)-1)
		default:
			return 0, 0, false
		}
	}
}

// matchingCloser finds the closer of the opener at r, c.
func (e *editor) matchingCloser(r, c int, open, closeCh rune) (int, int, bool) {
	depth := 0
	for {
		if ch, has := e.charAt(r, c); has {
			switch ch {
			case open:
				depth++
			case closeCh:
				depth--
			}
			if depth == 0 {
				return r, c, true
			}
		}
		switch {
		case c+1 < e.lineLen(r):
			c++
		case r < len(e.lines)-1:
			r, c = r+1, 0
		default:
			return 0, 0, false
		}
	}
}

// --- the layer

type motion struct {
	r, c      int
	inclusive bool
	linewise  bool
	keepWant  bool
	ok        bool
}

// motionFor resolves a motion key. page is the viewport height.
func (v *vim) motionFor(e *editor, key string, page int) motion {
	n := v.n()
	r, c := e.row, e.col
	switch key {
	case "h", "left", "backspace":
		return motion{r: r, c: max(0, c-n), ok: true}
	case "l", "right", "space":
		return motion{r: r, c: min(e.lineLen(r), c+n), ok: true}
	case "j", "down", "ctrl+n":
		return motion{r: min(len(e.lines)-1, r+n), c: e.want, linewise: true, keepWant: true, ok: true}
	case "k", "up":
		return motion{r: max(0, r-n), c: e.want, linewise: true, keepWant: true, ok: true}
	case "0", "home":
		return motion{r: r, c: 0, ok: true}
	case "^":
		return motion{r: r, c: e.firstNonBlank(r), ok: true}
	case "$", "end":
		r = min(len(e.lines)-1, r+n-1)
		return motion{r: r, c: e.lineLen(r), ok: true}
	case "w", "W", "b", "B", "e", "E":
		return e.wordMotion(key, n)
	case "G", "gg":
		return v.lineJump(e, key)
	case "{":
		return motion{r: e.paragraph(-1, n), c: 0, ok: true}
	case "}":
		return motion{r: e.paragraph(1, n), c: 0, ok: true}
	case "%":
		if mr, mc, ok := e.matchBracket(); ok {
			return motion{r: mr, c: mc, inclusive: true, ok: true}
		}
	case ";", ",":
		return v.repeatFind(e, key == ",", n)
	case "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "pgdown", "pgup":
		return e.pageMotion(key, page)
	case "n", "N":
		return v.searchMotion(e, key == "N")
	}
	return motion{}
}

// wordMotion is w b e and W B E, n times.
func (e *editor) wordMotion(key string, n int) motion {
	step := e.wordForward
	switch key {
	case "b", "B":
		step = e.wordBack
	case "e", "E":
		step = e.wordEnd
	}
	big := key == "W" || key == "B" || key == "E"
	r, c := e.row, e.col
	for range n {
		r, c = step(r, c, big)
	}
	return motion{r: r, c: c, inclusive: key == "e" || key == "E", ok: true}
}

// lineJump is G (the last line) and gg (the first); with a count both go to
// that line.
func (v *vim) lineJump(e *editor, key string) motion {
	r := 0
	if key == "G" {
		r = len(e.lines) - 1
	}
	if v.hasCount || v.opCount > 0 {
		r = clamp(v.n()-1, 0, len(e.lines)-1)
	}
	return motion{r: r, c: e.firstNonBlank(r), linewise: true, ok: true}
}

// findMotion is f F t T: to (or next to) the n-th ch on the cursor line.
func (e *editor) findMotion(kind string, ch rune, n int) motion {
	fc, ok := e.findChar(kind, ch, n)
	if !ok {
		return motion{}
	}
	return motion{r: e.row, c: fc, inclusive: kind == "f" || kind == "t", ok: true}
}

// repeatFind is ; (the last f F t T again) and , (the same in reverse).
func (v *vim) repeatFind(e *editor, reverse bool, n int) motion {
	if v.findKind == "" {
		return motion{}
	}
	kind := v.findKind
	if reverse {
		kind = map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}[kind]
	}
	return e.findMotion(kind, v.findCh, n)
}

// pageMotion is ctrl+d and ctrl+u (half a page) and ctrl+f, ctrl+b, pgdown
// and pgup (a page less one line), down and up.
func (e *editor) pageMotion(key string, page int) motion {
	d := max(1, page/2)
	switch key {
	case "ctrl+f", "ctrl+b", "pgdown", "pgup":
		d = max(1, page-1)
	}
	switch key {
	case "ctrl+u", "ctrl+b", "pgup":
		d = -d
	}
	r := clamp(e.row+d, 0, len(e.lines)-1)
	return motion{r: r, c: e.want, linewise: true, keepWant: true, ok: true}
}

// searchMotion is n (the next match of the last search) and N (the previous).
func (v *vim) searchMotion(e *editor, back bool) motion {
	dir := 1
	if back {
		dir = -1
	}
	if sr, sc, ok := e.searchFrom(v.search, dir); ok {
		return motion{r: sr, c: sc, ok: true}
	}
	return motion{}
}

func isMotionKey(key string) bool {
	switch key {
	case "h", "l", "j", "k", "left", "right", "up", "down", "backspace", "space", "0", "^", "$", "home", "end",
		"w", "W", "b", "B", "e", "E", "G", "{", "}", "%", ";", ",", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "pgdown", "pgup", "n", "N", "ctrl+n":
		return true
	}
	return false
}

func (v *vim) setReg(text string, line bool, out *vimOut) {
	v.reg, v.regLine = text, line
	out.clip = text
}

// operate applies the pending operator to a range. For a charwise range the
// end is exclusive.
func (v *vim) operate(e *editor, op string, r1, c1, r2, c2 int, linewise bool, out *vimOut) {
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		r1, c1, r2, c2 = r2, c2, r1, c1
	}
	switch op {
	case "y":
		if linewise {
			v.setReg(e.linesText(r1, r2), true, out)
			e.row, e.col = r1, min(e.col, max(0, e.lineLen(r1)-1))
		} else {
			v.setReg(e.textRange(r1, c1, r2, c2), false, out)
			e.row, e.col = r1, c1
		}
		n := r2 - r1 + 1
		if linewise && n > 2 {
			out.toast = itoa(n) + " lines yanked"
		}
	case "d", "c":
		e.push(false)
		if linewise {
			v.setReg(e.linesText(r1, r2), true, out)
			if op == "c" {
				indent := e.lines[r1][:e.firstNonBlank(r1)]
				e.lines = append(e.lines[:r1], append([][]rune{append([]rune(nil), indent...)}, e.lines[r2+1:]...)...)
				e.row, e.col = r1, len(indent)
			} else {
				e.deleteLines(r1, r2)
			}
		} else {
			v.setReg(e.textRange(r1, c1, r2, c2), false, out)
			e.deleteRange(r1, c1, r2, c2)
		}
		e.changed()
		if op == "c" {
			v.mode = vimInsert
		}
	case ">", "<":
		e.push(false)
		for r := r1; r <= r2; r++ {
			line := e.lines[r]
			if op == ">" {
				if len(line) > 0 {
					e.lines[r] = append([]rune(strings.Repeat(" ", edTabWidth)), line...)
				}
				continue
			}
			cut := 0
			for cut < edTabWidth && cut < len(line) && line[cut] == ' ' {
				cut++
			}
			if cut == 0 && len(line) > 0 && line[0] == '\t' {
				cut = 1
			}
			e.lines[r] = append([]rune(nil), line[cut:]...)
		}
		e.row, e.col = r1, e.firstNonBlank(r1)
		e.changed()
	}
	e.want = e.col
}

// paste puts the register after (p) or before (P) the cursor, count times.
func (v *vim) paste(e *editor, before bool, count int) {
	if v.reg == "" {
		return
	}
	e.push(false)
	text := strings.Repeat(v.reg+"\n", count)
	if v.regLine {
		ins := splitLines(strings.TrimSuffix(text, "\n"))
		at := e.row + 1
		if before {
			at = e.row
		}
		nl := make([][]rune, 0, len(e.lines)+len(ins))
		nl = append(nl, e.lines[:at]...)
		nl = append(nl, ins...)
		nl = append(nl, e.lines[at:]...)
		e.lines = nl
		e.row, e.col = at, 0
		e.col = e.firstNonBlank(at)
	} else {
		text = strings.Repeat(v.reg, count)
		if !before && e.lineLen(e.row) > 0 {
			e.col = min(e.col+1, e.lineLen(e.row))
		}
		e.insertRaw(text)
		if !strings.Contains(text, "\n") {
			e.col = max(0, e.col-1)
		}
	}
	e.want = e.col
	e.changed()
}

// visualRange returns the selection of Visual mode (end exclusive).
func (v *vim) visualRange(e *editor) (r1, c1, r2, c2 int) {
	r1, c1, r2, c2 = v.anchorR, v.anchorC, e.row, e.col
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		r1, c1, r2, c2 = r2, c2, r1, c1
	}
	if v.mode == vimVisualLine {
		return r1, 0, r2, e.lineLen(r2)
	}
	return r1, c1, r2, min(c2+1, e.lineLen(r2))
}

// syncSelection shows Visual mode's range as the editor's selection.
func (v *vim) syncSelection(e *editor) {
	if !v.visual() {
		e.vis = nil
		return
	}
	r1, c1, r2, c2 := v.visualRange(e)
	e.vis = &[4]int{r1, c1, r2, c2}
}

func (v *vim) enterInsert(e *editor) {
	v.mode = vimInsert
	e.selOn = false
}

func (v *vim) visual() bool { return v.mode == vimVisual || v.mode == vimVisualLine }

// key handles one key in Normal or a Visual mode. key is the printable text
// or the key's name; page is the viewport height.
//
// A key finishes what is pending first: the second key of a two-key command,
// a digit of the count, then Visual mode's keys or what follows an operator.
// Otherwise it starts a Normal-mode command.
func (v *vim) key(e *editor, key string, page int) vimOut {
	defer func() {
		if v.mode != vimInsert {
			e.clampNormal()
		}
		v.syncSelection(e)
	}()
	if w := v.wait; w != "" {
		v.wait = ""
		return v.secondKey(e, w, key, page)
	}
	if v.countDigit(key) {
		return vimOut{}
	}
	if v.visual() {
		return v.visualKey(e, key, page)
	}
	if v.op != "" {
		return v.operatorKey(e, key, page)
	}
	return v.normalKey(e, key, page)
}

// countDigit adds key to the count when it is a digit. 0 is a digit only
// after another one; on its own it is the motion.
func (v *vim) countDigit(key string) bool {
	if len(key) != 1 || key[0] < '0' || key[0] > '9' || (key == "0" && !v.hasCount) {
		return false
	}
	v.count = min(v.count*10+int(key[0]-'0'), 100000)
	v.hasCount = true
	return true
}

// keyRune is the character a key types, for the second key of f and r.
func keyRune(key string) (rune, bool) {
	if key == "space" {
		return ' ', true
	}
	rs := []rune(key)
	if len(rs) != 1 {
		return 0, false
	}
	return rs[0], true
}

// secondKey completes a two-key command whose first key, w, is waiting.
func (v *vim) secondKey(e *editor, w, key string, page int) vimOut {
	var out vimOut
	switch w {
	case "f", "F", "t", "T":
		v.find(e, w, key, &out)
	case "r":
		v.replaceChars(e, key)
	case "g":
		v.gKey(e, key, page, &out)
	case "i", "a":
		v.textObjectKey(e, w == "a", key, &out)
	case "leader":
		v.reset()
		out = leaderKey(key)
	}
	return out
}

// find is f F t T: a motion to the character key types.
func (v *vim) find(e *editor, kind, key string, out *vimOut) {
	ch, ok := keyRune(key)
	if !ok {
		v.reset()
		return
	}
	v.findKind, v.findCh = kind, ch
	v.applyMotion(e, e.findMotion(kind, ch, v.n()), out)
}

// replaceChars is r: the count characters from the cursor become the one key
// types, if the line has that many.
func (v *vim) replaceChars(e *editor, key string) {
	n := v.n()
	v.reset()
	ch, ok := keyRune(key)
	if !ok || e.col+n > e.lineLen(e.row) {
		return
	}
	e.push(false)
	for i := range n {
		e.lines[e.row][e.col+i] = ch
	}
	e.col += n - 1
	e.changed()
}

// gKey is the key after g: gg is a motion, gr opens the run list.
func (v *vim) gKey(e *editor, key string, page int, out *vimOut) {
	switch key {
	case "g":
		v.applyMotion(e, v.motionFor(e, "gg", page), out)
		return
	case "r":
		out.action = "runs"
	}
	v.reset()
}

// textObjectKey is the key after i or a: Visual mode selects the object, an
// operator applies to it.
func (v *vim) textObjectKey(e *editor, around bool, key string, out *vimOut) {
	r1, c1, r2, c2, ok := e.textObject(around, key)
	switch {
	case !ok:
		v.reset()
	case v.visual():
		v.mode = vimVisual
		v.anchorR, v.anchorC = r1, c1
		e.row, e.col = r2, max(c1, c2-1)
		if r2 > r1 && c2 == 0 {
			e.row, e.col = r2-1, max(0, e.lineLen(r2-1)-1)
		}
	default:
		op := v.op
		v.reset()
		v.operate(e, op, r1, c1, r2, c2, false, out)
	}
}

// leaderActions are the editor actions of the space leader's second key.
var leaderActions = map[string]string{
	"r": "run", "d": "dryrun", "e": "exteditor", "l": "runs", "n": "newbuf", "b": "buflist",
	"w": "closebuf", "[": "prevbuf", "]": "nextbuf", "s": "save", "f": "favorites", "h": "history", "F": "savefav", "p": "projects", "y": "handoff",
}

// leaderKey is the key after space.
func leaderKey(key string) vimOut {
	if action := leaderActions[key]; action != "" {
		return vimOut{action: action}
	}
	return vimOut{toast: "space " + key + " is not bound (space r runs, space d dry-runs)"}
}

// operatorKey handles the key after an operator: the operator again for whole
// lines (dd, cc, yy, >>, <<), a text object, or a motion. Any other key
// cancels the operator.
func (v *vim) operatorKey(e *editor, key string, page int) vimOut {
	var out vimOut
	switch {
	case key == v.op:
		n, op := v.n(), v.op
		v.reset()
		v.operate(e, op, e.row, 0, min(len(e.lines)-1, e.row+n-1), 0, true, &out)
	case key == "i" || key == "a" || key == "f" || key == "F" || key == "t" || key == "T" || key == "g":
		v.wait = key
	case isMotionKey(key):
		if key = v.changeWord(e, key, &out); key != "" {
			v.applyMotion(e, v.motionFor(e, key, page), &out)
		}
	default:
		v.reset()
	}
	return out
}

// changeWord makes cw and cW change to the end of the word, like ce and cE,
// so the blanks after it stay. It returns the motion to apply, or "" when it
// changed the word's last letter itself (ce from there would run into the
// next word).
func (v *vim) changeWord(e *editor, key string, out *vimOut) string {
	ch, ok := e.charAt(e.row, e.col)
	if v.op != "c" || (key != "w" && key != "W") || !ok || unicode.IsSpace(ch) {
		return key
	}
	big := key == "W"
	key = "e"
	if big {
		key = "E"
	}
	if nx, has := e.charAt(e.row, e.col+1); (!has || classOf(nx, big) != classOf(ch, big)) && v.n() == 1 {
		v.reset()
		v.operate(e, "c", e.row, e.col, e.row, e.col+1, false, out)
		return ""
	}
	return key
}

// startPending begins a command that needs more keys: an operator (taking
// the count typed so far), a two-key command, or the space leader. With a
// count, space is the motion instead.
func (v *vim) startPending(key string) bool {
	switch key {
	case "d", "c", "y", ">", "<":
		v.op = key
		if v.hasCount {
			v.opCount, v.count, v.hasCount = v.count, 0, false
		}
	case "f", "F", "t", "T", "r", "g":
		v.wait = key
	case "space":
		if v.hasCount {
			return false
		}
		v.wait = "leader"
	default:
		return false
	}
	return true
}

// normalActions are the Normal-mode keys the model handles.
var normalActions = map[string]string{":": "palette", "?": "help", "enter": "run", ".": "vimrepeat"}

// normalKey handles a key in Normal mode with nothing pending but a count.
func (v *vim) normalKey(e *editor, key string, page int) vimOut {
	var out vimOut
	if v.startPending(key) {
		return out
	}
	n := v.n()
	switch key {
	case "esc":
	case "i", "a", "I", "A", "o", "O":
		v.insert(e, key)
	case "x", "delete", "X", "s", "S", "D", "C", "Y":
		v.shorthand(e, key, n, &out)
	case "p", "P":
		v.paste(e, key == "P", n)
	case "u":
		out.toast = e.undoTimes(n, false)
	case "ctrl+r":
		out.toast = e.undoTimes(n, true)
	case "J":
		e.joinLines(max(1, n-1))
	case "~":
		e.flipCaseAtCursor(n)
	case "v":
		v.mode, v.anchorR, v.anchorC = vimVisual, e.row, e.col
	case "V":
		v.mode, v.anchorR, v.anchorC = vimVisualLine, e.row, e.col
	case ":", "?", "enter", ".":
		out.action = normalActions[key]
	case "/":
		out.prompt = true
	case "*":
		v.searchWord(e, page, &out)
	default:
		if isMotionKey(key) {
			v.applyMotion(e, v.motionFor(e, key, page), &out)
		}
	}
	v.reset()
	return out
}

// insert is i a I A o O: each starts Insert mode in its own place.
func (v *vim) insert(e *editor, key string) {
	switch key {
	case "a":
		e.col = min(e.col+1, e.lineLen(e.row))
	case "I":
		e.col = e.firstNonBlank(e.row)
	case "A":
		e.col = e.lineLen(e.row)
	case "o", "O":
		e.openLine(key == "O")
	}
	v.enterInsert(e)
}

// openLine is o and O: a new line below or above the cursor, with its indent.
func (e *editor) openLine(above bool) {
	e.push(false)
	indent := append([]rune(nil), e.lines[e.row][:e.firstNonBlank(e.row)]...)
	at := e.row + 1
	if above {
		at = e.row
	}
	nl := make([][]rune, 0, len(e.lines)+1)
	nl = append(nl, e.lines[:at]...)
	nl = append(nl, indent)
	nl = append(nl, e.lines[at:]...)
	e.lines = nl
	e.row, e.col = at, len(indent)
	e.changed()
}

// shorthand is x X s S D C Y, each an operator over a range: x is dl, X is
// dh, s is cl, S is cc, D is d$, C is c$ and Y is yy.
func (v *vim) shorthand(e *editor, key string, n int, out *vimOut) {
	row, col, end := e.row, e.col, e.lineLen(e.row)
	last := min(len(e.lines)-1, row+n-1)
	switch key {
	case "x", "delete":
		if end > 0 {
			v.operate(e, "d", row, col, row, min(col+n, end), false, out)
		}
	case "X":
		if col > 0 {
			v.operate(e, "d", row, max(0, col-n), row, col, false, out)
		}
	case "s":
		v.operate(e, "c", row, col, row, min(col+n, end), false, out)
	case "S":
		v.operate(e, "c", row, 0, last, 0, true, out)
	case "D":
		v.operate(e, "d", row, col, row, end, false, out)
	case "C":
		v.operate(e, "c", row, col, row, end, false, out)
	case "Y":
		v.operate(e, "y", row, 0, last, 0, true, out)
	}
}

// undoTimes is u (or ctrl+r, redo) n times. It returns a toast when it runs
// out of changes.
func (e *editor) undoTimes(n int, redo bool) string {
	step, toast := e.doUndo, "already at the oldest change"
	if redo {
		step, toast = e.doRedo, "already at the newest change"
	}
	for range n {
		if !step() {
			return toast
		}
	}
	return ""
}

// joinLines is J: the next line joins the cursor line, joins times, with one
// space between them.
func (e *editor) joinLines(joins int) {
	e.push(false)
	for i := 0; i < joins && e.row < len(e.lines)-1; i++ {
		left := []rune(strings.TrimRight(string(e.lines[e.row]), " \t"))
		right := []rune(strings.TrimLeft(string(e.lines[e.row+1]), " \t"))
		at := len(left)
		if len(left) > 0 && len(right) > 0 {
			left = append(left, ' ')
		}
		e.lines[e.row] = append(left, right...)
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
		e.col = at
	}
	e.changed()
}

func flipCase(ch rune) rune {
	if unicode.IsUpper(ch) {
		return unicode.ToLower(ch)
	}
	return unicode.ToUpper(ch)
}

// flipCaseAtCursor is ~: n characters from the cursor change case, and the
// cursor moves past them.
func (e *editor) flipCaseAtCursor(n int) {
	if e.lineLen(e.row) == 0 {
		return
	}
	e.push(false)
	for i := 0; i < n && e.col < e.lineLen(e.row); i++ {
		e.lines[e.row][e.col] = flipCase(e.lines[e.row][e.col])
		e.col++
	}
	e.changed()
}

// searchWord is *: search for the word under the cursor.
func (v *vim) searchWord(e *editor, page int, out *vimOut) {
	if r1, c1, _, c2, ok := e.textObject(false, "w"); ok && c2 > c1 {
		v.search = string(e.lines[r1][c1:c2])
		v.applyMotion(e, v.motionFor(e, "n", page), out)
	}
}

// applyMotion moves the cursor, or applies the pending operator over the motion.
func (v *vim) applyMotion(e *editor, mo motion, out *vimOut) {
	op, visual := v.op, v.visual()
	v.reset()
	if !mo.ok {
		return
	}
	if op == "" || visual {
		e.row = clamp(mo.r, 0, len(e.lines)-1)
		e.col = clamp(mo.c, 0, e.lineLen(e.row))
		if !mo.keepWant {
			e.want = e.col
		}
		e.typing = false
		return
	}
	r1, c1, r2, c2 := e.row, e.col, mo.r, mo.c
	if mo.linewise {
		v.operate(e, op, min(r1, r2), 0, max(r1, r2), 0, true, out)
		return
	}
	forward := r2 > r1 || (r2 == r1 && c2 >= c1)
	if !forward {
		r1, c1, r2, c2 = r2, c2, r1, c1
	} else if mo.inclusive {
		c2 = min(c2+1, e.lineLen(r2))
	}
	// dw on the last word of a line stops at the line end
	if forward && !mo.inclusive && r2 > r1 && c2 <= e.firstNonBlank(r2) && op != "y" || (forward && !mo.inclusive && r2 > r1 && c2 == 0) {
		r2, c2 = r2-1, e.lineLen(r2-1)
		for r2 > r1 && e.lineLen(r2) == 0 {
			r2--
			c2 = e.lineLen(r2)
		}
	}
	v.operate(e, op, r1, c1, r2, c2, false, out)
}

// visualOperators are the operators Visual mode applies to the selection.
var visualOperators = map[string]string{"d": "d", "x": "d", "delete": "d", "y": "y", "c": "c", "s": "c", ">": ">", "<": "<"}

// caseChanges are Visual mode's ~ (flip), u (lower) and U (upper).
var caseChanges = map[string]func(rune) rune{"~": flipCase, "u": unicode.ToLower, "U": unicode.ToUpper}

// visualKey handles a key in Visual and Visual-line mode.
func (v *vim) visualKey(e *editor, key string, page int) vimOut {
	var out vimOut
	r1, c1, r2, c2 := v.visualRange(e)
	line := v.mode == vimVisualLine
	if op, ok := visualOperators[key]; ok {
		v.leaveVisual()
		v.operate(e, op, r1, c1, r2, c2, line, &out)
		return out
	}
	if change, ok := caseChanges[key]; ok {
		v.leaveVisual()
		e.changeCase(r1, c1, r2, c2, line, change)
		return out
	}
	switch key {
	case "esc":
		v.leaveVisual()
	case "v":
		v.toggleVisual(vimVisual)
	case "V":
		v.toggleVisual(vimVisualLine)
	case "o":
		v.anchorR, e.row = e.row, v.anchorR
		v.anchorC, e.col = e.col, v.anchorC
	case "p", "P":
		v.leaveVisual()
		v.replaceSelection(e, r1, c1, r2, c2, line)
	case "J":
		v.leaveVisual()
		e.row, e.col = r1, 0
		v.count, v.hasCount = r2-r1+1, true
		return v.key(e, "J", page)
	case "i", "a", "f", "F", "t", "T", "g":
		v.wait = key
	case "space":
		v.wait = "leader"
	case "enter":
		out.action = "run" // runs the selection
	case ":":
		out.action = "palette"
	case "/":
		out.prompt = true
	default:
		if isMotionKey(key) {
			v.applyMotion(e, v.motionFor(e, key, page), &out)
		}
	}
	return out
}

func (v *vim) leaveVisual() {
	v.mode = vimNormal
	v.reset()
}

// toggleVisual is v and V in Visual mode: the same mode again leaves it, the
// other one switches to it.
func (v *vim) toggleVisual(mode vimMode) {
	if v.mode == mode {
		v.leaveVisual()
		return
	}
	v.mode = mode
}

// replaceSelection is p and P in Visual mode: the register replaces the
// selection, which is deleted without changing the register.
func (v *vim) replaceSelection(e *editor, r1, c1, r2, c2 int, line bool) {
	reg, regLine := v.reg, v.regLine
	var discard vimOut
	v.operate(e, "d", r1, c1, r2, c2, line, &discard)
	v.reg, v.regLine = reg, regLine
	if reg == "" {
		return
	}
	e.push(false)
	if regLine || line {
		v.regLine = true
		v.paste(e, true, 1)
		return
	}
	e.insertRaw(reg)
	e.col = max(0, e.col-1)
	e.changed()
}

// changeCase applies change to every character of a range (end exclusive),
// or of whole lines, and puts the cursor at its start.
func (e *editor) changeCase(r1, c1, r2, c2 int, linewise bool, change func(rune) rune) {
	e.push(false)
	for r := r1; r <= r2; r++ {
		from, to := 0, e.lineLen(r)
		if !linewise && r == r1 {
			from = c1
		}
		if !linewise && r == r2 {
			to = c2
		}
		for c := from; c < to; c++ {
			e.lines[r][c] = change(e.lines[r][c])
		}
	}
	e.row, e.col = r1, c1
	e.changed()
}

// --- model side

func (m *Model) vimOn() bool { return m.be.Config().Editing() == "vim" }

// vimClaims reports whether Normal and Visual mode handle the key themselves
// instead of the editor's registry binding (ctrl+r is redo, ctrl+d scrolls).
func vimClaims(name string) bool {
	switch name {
	case "ctrl+r", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "ctrl+n", "esc":
		return true
	}
	return false
}

// vimKey handles a key in Normal or a Visual mode.
func (m *Model) vimKey(msg tea.KeyPressMsg, name string) tea.Cmd {
	b := m.buf()
	v, e := &b.vim, b.ed
	wasIdle := v.idle()
	if wasIdle {
		v.rec, v.tainted, v.idleVer = nil, false, e.version
		e.beginGroup()
	}
	if !v.replay {
		v.rec = append(v.rec, msg)
	}
	before := e.version
	out := v.key(e, name, max(2, m.editorRows()))
	cmd := m.vimApply(b, out, before)
	m.vimSettle(b)
	return cmd
}

// vimSettle closes the command in progress once the layer is idle again: the
// undo group ends, and a command that changed the text becomes the one "."
// repeats.
func (m *Model) vimSettle(b *buffer) {
	v, e := &b.vim, b.ed
	if !v.idle() {
		return
	}
	e.endGroup()
	if !v.replay && !v.tainted && e.version != v.idleVer && len(v.rec) > 0 {
		v.last = v.rec
	}
	v.rec = nil
}

func (m *Model) vimApply(b *buffer, out vimOut, before int) tea.Cmd {
	v, e := &b.vim, b.ed
	var cmds []tea.Cmd
	if out.toast != "" {
		m.toast = out.toast
	}
	if out.clip != "" && !v.replay {
		cmds = append(cmds, tea.SetClipboard(out.clip))
	}
	if e.version != before && m.o.NoBackground {
		m.saveDraft(b)
	}
	if out.prompt {
		v.tainted = true
		m.openPrompt(promptState{
			kind: "edfind", title: "Find in the buffer", labels: []string{"/"},
			note: "enter jumps to the next match · n / N repeat it · lower case ignores case",
		}, "")
	}
	switch out.action {
	case "":
	case "vimrepeat":
		cmds = append(cmds, m.vimRepeat(b))
	case "run", "dryrun":
		v.tainted = true
		cmds = append(cmds, m.requestRun(out.action == "dryrun"))
		if v.mode == vimVisual || v.mode == vimVisualLine {
			v.mode = vimNormal
			v.syncSelection(e)
			e.clampNormal()
		}
	default:
		v.tainted = true
		cmds = append(cmds, m.editorAction(out.action))
	}
	return tea.Batch(cmds...)
}

// vimRepeat replays the keys of the last change.
func (m *Model) vimRepeat(b *buffer) tea.Cmd {
	v := &b.vim
	v.tainted = true // the repeat itself is not a new "last change"
	if len(v.last) == 0 {
		return nil
	}
	keys := v.last
	v.replay = true
	defer func() { v.replay = false }()
	for _, k := range keys {
		if v.mode == vimInsert {
			m.vimInsertKey(b, k, keyName(k))
			continue
		}
		v.key(b.ed, keyName(k), max(2, m.editorRows()))
	}
	if v.mode == vimInsert { // a recording always ends in Normal mode
		m.vimLeaveInsert(b)
	}
	b.ed.clampNormal()
	if m.o.NoBackground {
		m.saveDraft(b)
	}
	return nil
}

// vimLeaveInsert is esc in Insert mode.
func (m *Model) vimLeaveInsert(b *buffer) {
	b.vim.mode = vimNormal
	b.ed.selOn = false
	if b.ed.col > 0 {
		b.ed.col--
	}
	b.ed.want = b.ed.col
	b.ed.clampNormal()
	m.comp.open = false
}

// vimInsertKey applies a key in Insert mode during a replay (no popup).
func (m *Model) vimInsertKey(b *buffer, msg tea.KeyPressMsg, name string) {
	if name == "esc" {
		m.vimLeaveInsert(b)
		return
	}
	b.ed.handle(msg, max(1, m.editorRows()-1))
}
