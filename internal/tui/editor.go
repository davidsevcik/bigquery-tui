package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// editor is a modeless multi-line text editor: cursor, selection, snapshot
// undo, horizontal scrolling and a per-token highlight hook. It knows nothing
// about SQL; the tokens come from the core.
type editor struct {
	lines    [][]rune
	row, col int
	want     int // column the cursor wants on vertical moves

	selOn          bool
	selRow, selCol int // selection anchor
	// vis, when set, is the selection (r1, c1, r2, c2; end exclusive) of the
	// vim layer's Visual mode and overrides the anchor.
	vis *[4]int
	// insertCursor draws the cursor as the vim layer's Insert-mode cursor.
	insertCursor bool

	top, left int // scroll: first visible line, first visible cell

	undo, redo []edSnap
	typing     bool // the last change was plain typing (coalesced into one undo step)
	// inGroup makes every change until endGroup one undo step (a vim command,
	// including the whole Insert session it starts).
	inGroup, grouped bool
	version          int

	// the error BigQuery pointed at (1-based line and column; 0 = none)
	errLine, errCol int

	hlVersion int
	hl        [][]api.TokenKind
}

type edSnap struct {
	lines    [][]rune
	row, col int
}

const (
	edTabWidth  = 2
	edUndoLimit = 300
)

func newEditor(text string) *editor {
	e := &editor{hlVersion: -1}
	e.setText(text)
	e.undo, e.redo, e.version = nil, nil, 0
	return e
}

func splitLines(text string) [][]rune {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n")
	out := make([][]rune, len(parts))
	for i, p := range parts {
		out[i] = []rune(p)
	}
	return out
}

func (e *editor) text() string {
	var b strings.Builder
	for i, l := range e.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(l))
	}
	return b.String()
}

// setText replaces the whole text as one undoable change.
func (e *editor) setText(text string) {
	e.push(false)
	e.lines = splitLines(text)
	e.row, e.col, e.want = len(e.lines)-1, len(e.lines[len(e.lines)-1]), 0
	e.selOn = false
	e.changed()
}

func (e *editor) changed() {
	e.version++
	e.errLine, e.errCol = 0, 0
}

func (e *editor) empty() bool { return len(e.lines) == 1 && len(e.lines[0]) == 0 }

// offset returns the cursor as a UTF-8 byte offset into text().
func (e *editor) offset() int { return e.offsetOf(e.row, e.col) }

func (e *editor) offsetOf(row, col int) int {
	n := 0
	for i := 0; i < row && i < len(e.lines); i++ {
		n += len(string(e.lines[i])) + 1
	}
	if row < len(e.lines) {
		n += len(string(e.lines[row][:min(col, len(e.lines[row]))]))
	}
	return n
}

// position converts a byte offset to a line and rune column.
func (e *editor) position(off int) (int, int) {
	for i, l := range e.lines {
		n := len(string(l))
		if off <= n {
			col := 0
			for b := 0; col < len(l) && b < off; col++ {
				b += len(string(l[col]))
			}
			return i, col
		}
		off -= n + 1
	}
	last := len(e.lines) - 1
	return last, len(e.lines[last])
}

// moveTo places the cursor at a 1-based line and column (an error position).
func (e *editor) moveTo(line, col int) {
	e.row = clamp(line-1, 0, len(e.lines)-1)
	e.col = clamp(col-1, 0, len(e.lines[e.row]))
	e.want, e.selOn, e.typing = e.col, false, false
}

// --- undo

func (e *editor) snap() edSnap {
	cp := make([][]rune, len(e.lines))
	for i, l := range e.lines {
		cp[i] = append([]rune(nil), l...)
	}
	return edSnap{cp, e.row, e.col}
}

// push records the state before a change. Consecutive typing is one step.
func (e *editor) push(typing bool) {
	if e.lines == nil {
		return
	}
	if e.inGroup {
		if e.grouped {
			return
		}
		e.grouped, typing = true, false
	}
	if typing && e.typing {
		return
	}
	e.typing = typing
	e.undo = append(e.undo, e.snap())
	if len(e.undo) > edUndoLimit {
		e.undo = e.undo[1:]
	}
	e.redo = nil
}

func (e *editor) beginGroup() { e.inGroup, e.grouped = true, false }
func (e *editor) endGroup()   { e.inGroup, e.grouped = false, false }

func (e *editor) restore(s edSnap) {
	e.lines, e.row, e.col = s.lines, s.row, s.col
	e.want, e.selOn, e.typing = e.col, false, false
	e.changed()
}

func (e *editor) doUndo() bool {
	if len(e.undo) == 0 {
		return false
	}
	e.redo = append(e.redo, e.snap())
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.restore(s)
	return true
}

func (e *editor) doRedo() bool {
	if len(e.redo) == 0 {
		return false
	}
	e.undo = append(e.undo, e.snap())
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.restore(s)
	return true
}

// --- selection

// selRange returns the selection as ordered positions; ok is false without one.
func (e *editor) selRange() (r1, c1, r2, c2 int, ok bool) {
	if v := e.vis; v != nil {
		return v[0], v[1], v[2], v[3], !(v[0] == v[2] && v[1] == v[3])
	}
	if !e.selOn || (e.selRow == e.row && e.selCol == e.col) {
		return 0, 0, 0, 0, false
	}
	r1, c1, r2, c2 = e.selRow, e.selCol, e.row, e.col
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		r1, c1, r2, c2 = r2, c2, r1, c1
	}
	return r1, c1, r2, c2, true
}

func (e *editor) selection() string {
	r1, c1, r2, c2, ok := e.selRange()
	if !ok {
		return ""
	}
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

func (e *editor) selectAll() {
	e.selOn, e.selRow, e.selCol = true, 0, 0
	e.row = len(e.lines) - 1
	e.col = len(e.lines[e.row])
	e.typing = false
}

// deleteRange removes the text between two positions (no undo push).
func (e *editor) deleteRange(r1, c1, r2, c2 int) {
	head := append([]rune(nil), e.lines[r1][:c1]...)
	head = append(head, e.lines[r2][c2:]...)
	e.lines = append(e.lines[:r1], append([][]rune{head}, e.lines[r2+1:]...)...)
	e.row, e.col = r1, c1
	e.selOn = false
}

func (e *editor) deleteSelection() bool {
	r1, c1, r2, c2, ok := e.selRange()
	if !ok {
		e.selOn = false
		return false
	}
	e.deleteRange(r1, c1, r2, c2)
	return true
}

// --- edits

// insert types text at the cursor, replacing the selection.
func (e *editor) insert(s string) {
	if s == "" {
		return
	}
	single := len([]rune(s)) == 1 && s != "\n" && !e.selOn
	e.push(single)
	e.deleteSelection()
	e.insertRaw(s)
	if !single {
		e.typing = false
	}
	e.changed()
}

func (e *editor) insertRaw(s string) {
	parts := splitLines(s)
	line := e.lines[e.row]
	tail := append([]rune(nil), line[e.col:]...)
	if len(parts) == 1 {
		nl := append(append(append([]rune(nil), line[:e.col]...), parts[0]...), tail...)
		e.lines[e.row] = nl
		e.col += len(parts[0])
	} else {
		first := append(append([]rune(nil), line[:e.col]...), parts[0]...)
		last := append(append([]rune(nil), parts[len(parts)-1]...), tail...)
		mid := parts[1 : len(parts)-1]
		nl := make([][]rune, 0, len(e.lines)+len(parts))
		nl = append(nl, e.lines[:e.row]...)
		nl = append(nl, first)
		nl = append(nl, mid...)
		nl = append(nl, last)
		nl = append(nl, e.lines[e.row+1:]...)
		e.lines = nl
		e.row += len(parts) - 1
		e.col = len(parts[len(parts)-1])
	}
	e.want = e.col
}

// replaceBytes replaces the byte range [start, end) of the text (a completion).
func (e *editor) replaceBytes(start, end int, s string) {
	e.push(false)
	r1, c1 := e.position(start)
	r2, c2 := e.position(end)
	e.deleteRange(r1, c1, r2, c2)
	e.insertRaw(s)
	e.changed()
}

func (e *editor) newline() {
	e.push(false)
	e.deleteSelection()
	indent := 0
	for indent < len(e.lines[e.row]) && indent < e.col && (e.lines[e.row][indent] == ' ' || e.lines[e.row][indent] == '\t') {
		indent++
	}
	e.insertRaw("\n" + string(e.lines[e.row][:indent]))
	e.changed()
}

func (e *editor) backspace() bool {
	if e.selOn {
		e.push(false)
		if e.deleteSelection() {
			e.changed()
			return true
		}
	}
	if e.row == 0 && e.col == 0 {
		return false
	}
	e.push(true)
	if e.col == 0 {
		e.typing = false
		e.deleteRange(e.row-1, len(e.lines[e.row-1]), e.row, 0)
	} else {
		n := 1
		// a soft tab goes in one stroke
		if e.col%edTabWidth == 0 && e.col >= edTabWidth && strings.TrimSpace(string(e.lines[e.row][:e.col])) == "" {
			n = edTabWidth
		}
		e.deleteRange(e.row, e.col-n, e.row, e.col)
	}
	e.want = e.col
	e.changed()
	return true
}

func (e *editor) deleteForward() bool {
	if e.selOn {
		e.push(false)
		if e.deleteSelection() {
			e.changed()
			return true
		}
	}
	switch {
	case e.col < len(e.lines[e.row]):
		e.push(false)
		e.deleteRange(e.row, e.col, e.row, e.col+1)
	case e.row < len(e.lines)-1:
		e.push(false)
		e.deleteRange(e.row, e.col, e.row+1, 0)
	default:
		return false
	}
	e.changed()
	return true
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (e *editor) wordLeft() (int, int) {
	r, c := e.row, e.col
	if c == 0 {
		if r == 0 {
			return 0, 0
		}
		return r - 1, len(e.lines[r-1])
	}
	l := e.lines[r]
	for c > 0 && !isWordRune(l[c-1]) {
		c--
	}
	for c > 0 && isWordRune(l[c-1]) {
		c--
	}
	return r, c
}

func (e *editor) wordRight() (int, int) {
	r, c := e.row, e.col
	l := e.lines[r]
	if c >= len(l) {
		if r == len(e.lines)-1 {
			return r, c
		}
		return r + 1, 0
	}
	for c < len(l) && !isWordRune(l[c]) {
		c++
	}
	for c < len(l) && isWordRune(l[c]) {
		c++
	}
	return r, c
}

func (e *editor) deleteTo(r, c int) bool {
	if r == e.row && c == e.col {
		return false
	}
	e.push(false)
	r1, c1, r2, c2 := r, c, e.row, e.col
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		r1, c1, r2, c2 = r2, c2, r1, c1
	}
	e.deleteRange(r1, c1, r2, c2)
	e.want = e.col
	e.changed()
	return true
}

// --- keys

// handle applies an editing or movement key. changed reports a text change,
// handled whether the key belonged to the editor at all. page is the number
// of lines a page key moves.
func (e *editor) handle(msg tea.KeyPressMsg, page int) (changed, handled bool) {
	name := msg.String()
	shift := strings.HasPrefix(name, "shift+")
	base := strings.TrimPrefix(name, "shift+")

	move := func(r, c int, keepWant bool) (bool, bool) {
		if shift && !e.selOn {
			e.selOn, e.selRow, e.selCol = true, e.row, e.col
		} else if !shift {
			e.selOn = false
		}
		e.row = clamp(r, 0, len(e.lines)-1)
		e.col = clamp(c, 0, len(e.lines[e.row]))
		if !keepWant {
			e.want = e.col
		}
		e.typing = false
		return false, true
	}
	switch base {
	case "left", "ctrl+b":
		if !shift {
			if r1, c1, _, _, ok := e.selRange(); ok {
				return move(r1, c1, false)
			}
		}
		if e.col > 0 {
			return move(e.row, e.col-1, false)
		}
		if e.row > 0 {
			return move(e.row-1, len(e.lines[e.row-1]), false)
		}
		return move(e.row, e.col, false)
	case "right", "ctrl+f":
		if !shift {
			if _, _, r2, c2, ok := e.selRange(); ok {
				return move(r2, c2, false)
			}
		}
		if e.col < len(e.lines[e.row]) {
			return move(e.row, e.col+1, false)
		}
		if e.row < len(e.lines)-1 {
			return move(e.row+1, 0, false)
		}
		return move(e.row, e.col, false)
	case "up":
		if e.row == 0 {
			return move(0, 0, false)
		}
		return move(e.row-1, e.want, true)
	case "down":
		if e.row == len(e.lines)-1 {
			return move(e.row, len(e.lines[e.row]), false)
		}
		return move(e.row+1, e.want, true)
	case "pgup":
		return move(e.row-page, e.want, true)
	case "pgdown":
		return move(e.row+page, e.want, true)
	case "home", "ctrl+a":
		// first press: the first non-blank; second: column 0
		ind := 0
		for ind < len(e.lines[e.row]) && unicode.IsSpace(e.lines[e.row][ind]) {
			ind++
		}
		if e.col == ind {
			ind = 0
		}
		return move(e.row, ind, false)
	case "end", "ctrl+e":
		return move(e.row, len(e.lines[e.row]), false)
	case "ctrl+home", "alt+<":
		return move(0, 0, false)
	case "ctrl+end", "alt+>":
		return move(len(e.lines)-1, len(e.lines[len(e.lines)-1]), false)
	case "ctrl+left", "alt+left", "alt+b":
		r, c := e.wordLeft()
		return move(r, c, false)
	case "ctrl+right", "alt+right", "alt+f":
		r, c := e.wordRight()
		return move(r, c, false)
	}
	switch name {
	case "enter":
		e.newline()
		return true, true
	case "tab":
		e.insert(strings.Repeat(" ", edTabWidth-e.col%edTabWidth))
		return true, true
	case "backspace":
		return e.backspace(), true
	case "delete":
		return e.deleteForward(), true
	case "alt+backspace", "ctrl+backspace":
		r, c := e.wordLeft()
		return e.deleteTo(r, c), true
	case "alt+d", "alt+delete", "ctrl+delete":
		r, c := e.wordRight()
		return e.deleteTo(r, c), true
	case "ctrl+k":
		if e.col == len(e.lines[e.row]) && e.row < len(e.lines)-1 {
			return e.deleteTo(e.row+1, 0), true
		}
		return e.deleteTo(e.row, len(e.lines[e.row])), true
	case "ctrl+u":
		return e.deleteTo(e.row, 0), true
	case "ctrl+z":
		return e.doUndo(), true
	case "ctrl+y":
		return e.doRedo(), true
	}
	k := msg.Key()
	if k.Text != "" && k.Mod&^tea.ModShift == 0 {
		e.insert(k.Text)
		return true, true
	}
	return false, false
}

// --- rendering

func runeCells(r rune) int {
	if r == '\t' {
		return edTabWidth
	}
	if r < 0x1100 {
		return 1
	}
	return max(1, wid(string(r)))
}

// cursorCell returns the display column of the cursor within its line.
func (e *editor) cursorCell() int {
	c := 0
	for _, r := range e.lines[e.row][:min(e.col, len(e.lines[e.row]))] {
		c += runeCells(r)
	}
	return c
}

func (e *editor) gutter() int {
	n, d := len(e.lines), 1
	for n >= 10 {
		n /= 10
		d++
	}
	return max(d, 2) + 2
}

// scroll keeps the cursor inside a w x h viewport.
func (e *editor) scroll(w, h int) {
	tw := max(1, w-e.gutter())
	if e.row < e.top {
		e.top = e.row
	}
	if e.row >= e.top+h {
		e.top = e.row - h + 1
	}
	e.top = clamp(e.top, 0, max(0, len(e.lines)-1))
	cc := e.cursorCell()
	if cc < e.left {
		e.left = cc
	}
	if cc >= e.left+tw {
		e.left = cc - tw + 1
	}
	if cc < tw {
		e.left = 0
	}
}

// highlight caches the token kind of every rune.
func (e *editor) highlight(tokenize func(string) []api.Token) {
	if e.hlVersion == e.version && e.hl != nil {
		return
	}
	e.hlVersion = e.version
	e.hl = make([][]api.TokenKind, len(e.lines))
	for i, l := range e.lines {
		e.hl[i] = make([]api.TokenKind, len(l))
	}
	if tokenize == nil {
		return
	}
	text := e.text()
	row, col, off := 0, 0, 0
	advance := func(to int) {
		for off < to && row < len(e.lines) {
			if col >= len(e.lines[row]) {
				row, col, off = row+1, 0, off+1
				continue
			}
			off += len(string(e.lines[row][col]))
			col++
		}
	}
	for _, t := range tokenize(text) {
		advance(t.Start)
		for off < t.End && row < len(e.lines) {
			if col >= len(e.lines[row]) {
				row, col, off = row+1, 0, off+1
				continue
			}
			e.hl[row][col] = t.Kind
			off += len(string(e.lines[row][col]))
			col++
		}
	}
}

var tokenStyles = map[api.TokenKind]lipgloss.Style{
	api.TokKeyword:  fg("75").Bold(true),
	api.TokString:   fg("114"),
	api.TokNumber:   fg("209"),
	api.TokComment:  fg("242").Italic(true),
	api.TokQuoted:   fg("186"),
	api.TokFunction: fg("141"),
	api.TokParam:    fg("213"),
	api.TokIdent:    fg("252"),
	api.TokPunct:    fg("246"),
}

// view draws the editor into w x h cells. cursorXY reports where the cursor
// landed (relative to the view), for the completion popup.
func (e *editor) view(w, h int, focused bool, tokenize func(string) []api.Token, placeholder string) (lines []string, curX, curY int) {
	e.scroll(w, h)
	e.highlight(tokenize)
	gw := e.gutter()
	tw := max(1, w-gw)
	r1, c1, r2, c2, sel := e.selRange()
	inSel := func(r, c int) bool {
		if !sel || r < r1 || r > r2 {
			return false
		}
		if r == r1 && c < c1 {
			return false
		}
		if r == r2 && c >= c2 {
			return false
		}
		return true
	}
	out := make([]string, 0, h)
	for y := 0; y < h; y++ {
		r := e.top + y
		if r >= len(e.lines) {
			out = append(out, sFaint.Render(pad("~", w)))
			continue
		}
		num := pad(strings.Repeat(" ", gw-2-len(itoa(r+1)))+itoa(r+1), gw-1)
		gs := sFaint
		mark := " "
		switch {
		case r+1 == e.errLine:
			gs, mark = sErr, "✗"
		case r == e.row && focused:
			gs = sDim
		}
		var sb strings.Builder
		sb.WriteString(gs.Render(num + mark))

		line := e.lines[r]
		var run strings.Builder
		var runStyle lipgloss.Style
		have := false
		flush := func() {
			if have {
				sb.WriteString(runStyle.Render(run.String()))
				run.Reset()
				have = false
			}
		}
		put := func(s string, st lipgloss.Style) {
			if have && !sameStyle(runStyle, st) {
				flush()
			}
			runStyle, have = st, true
			run.WriteString(s)
		}
		cell := 0
		used := 0
		for c := 0; c <= len(line); c++ {
			var ch rune = ' '
			cw := 1
			if c < len(line) {
				ch = line[c]
				cw = runeCells(ch)
			}
			isCursor := focused && r == e.row && c == e.col
			if c == len(line) && !isCursor && !(inSel(r, c) && r < r2) {
				break
			}
			if cell+cw <= e.left {
				cell += cw
				continue
			}
			if used+cw > tw {
				break
			}
			st := sBase
			if c < len(line) {
				st = tokenStyles[e.hl[r][c]]
				if e.hl[r][c] == "" {
					st = sBase
				}
				if r+1 == e.errLine && e.errCol > 0 && c >= e.errCol-1 && sameTokenRun(e.hl[r], e.errCol-1, c) {
					st = sErr.Underline(true)
				}
			}
			if inSel(r, c) {
				st = st.Background(lipgloss.Color("60")).Foreground(lipgloss.Color("255"))
			}
			if isCursor && e.insertCursor {
				st = lipgloss.NewStyle().Background(lipgloss.Color("114")).Foreground(lipgloss.Color("16"))
				curX, curY = gw+used, y
			} else if isCursor {
				st = st.Reverse(true)
				curX, curY = gw+used, y
			}
			text := string(ch)
			if ch == '\t' {
				text = strings.Repeat(" ", edTabWidth)
			}
			put(text, st)
			used += cw
			cell += cw
		}
		flush()
		if used < tw {
			sb.WriteString(strings.Repeat(" ", tw-used))
		}
		out = append(out, sb.String())
	}
	if e.empty() && placeholder != "" && len(out) > 0 {
		cur := " "
		if focused {
			cur = lipgloss.NewStyle().Reverse(true).Render(" ")
		}
		out[0] = fit(sFaint.Render(pad(" 1", gw-1)+" ")+cur+sFaint.Render(placeholder), w)
	}
	return out, curX, curY
}

// sameTokenRun reports whether columns a..b lie in one token (the error mark
// covers the token BigQuery pointed at).
func sameTokenRun(kinds []api.TokenKind, a, b int) bool {
	if a < 0 || b >= len(kinds) || a >= len(kinds) {
		return false
	}
	if kinds[a] == "" {
		return a == b
	}
	for i := a; i <= b; i++ {
		if kinds[i] != kinds[a] {
			return false
		}
	}
	return true
}

func sameStyle(a, b lipgloss.Style) bool {
	return a.GetForeground() == b.GetForeground() && a.GetBackground() == b.GetBackground() &&
		a.GetBold() == b.GetBold() && a.GetReverse() == b.GetReverse() && a.GetUnderline() == b.GetUnderline() && a.GetItalic() == b.GetItalic()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
