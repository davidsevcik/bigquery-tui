package tui

// Rendering helpers: styles, fixed-width rows, windows, popups and the
// single-line input. Everything here is terminal-only.

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func fg(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

var (
	sBase   = lipgloss.NewStyle()
	sDim    = fg("244")
	sFaint  = fg("240")
	sAcc    = fg("75")
	sWarn   = fg("214")
	sErr    = fg("203")
	sOk     = fg("78")
	sBold   = lipgloss.NewStyle().Bold(true)
	sMatch  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")).Underline(true)
	sKey    = fg("81").Bold(true)
	sNeg    = fg("203").Bold(true)
	sVal    = fg("186")
	sTerm   = fg("255")
	sBadV   = fg("203").Underline(true)
	sTitleF = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255")).Bold(true)
	sTitleN = lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("248"))
	selBG   = lipgloss.Color("238")
	selBG2  = lipgloss.Color("236")
)

func typeLetter(t api.TableType) string {
	switch t {
	case api.TypeTable:
		return "T"
	case api.TypeView:
		return "V"
	case api.TypeMView:
		return "M"
	case api.TypeExternal:
		return "E"
	case api.TypeSnapshot:
		return "S"
	case api.TypeClone:
		return "C"
	}
	return "?"
}

func typeStyle(t api.TableType) lipgloss.Style {
	switch t {
	case api.TypeView:
		return fg("171")
	case api.TypeMView:
		return fg("141")
	case api.TypeExternal:
		return fg("208")
	case api.TypeSnapshot, api.TypeClone:
		return fg("109")
	}
	return fg("75")
}

func dataTypeStyle(t string) lipgloss.Style {
	switch {
	case strings.HasPrefix(t, "STRUCT"), strings.HasPrefix(t, "ARRAY"), strings.HasPrefix(t, "RECORD"):
		return fg("141")
	case t == "STRING":
		return fg("114")
	case t == "TIMESTAMP", t == "DATE", t == "DATETIME", t == "TIME":
		return fg("180")
	}
	return fg("109")
}

func wid(s string) int { return ansi.StringWidth(s) }

func pad(s string, w int) string {
	if d := w - wid(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// fit truncates (with an ellipsis) and pads to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if wid(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return pad(s, w)
}

func wrap(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && wid(line)+1+wid(word) > w {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// rb builds one row from a left and a right segment sharing an optional
// selection background.
type rb struct {
	l, r strings.Builder
	bg   color.Color
}

func newRB(selected bool) *rb {
	b := &rb{}
	if selected {
		b.bg = selBG
	}
	return b
}

func (b *rb) style(st lipgloss.Style) lipgloss.Style {
	if b.bg != nil {
		return st.Background(b.bg)
	}
	return st
}

func (b *rb) add(text string, st lipgloss.Style) *rb {
	if text != "" {
		b.l.WriteString(b.style(st).Render(text))
	}
	return b
}

func (b *rb) addR(text string, st lipgloss.Style) *rb {
	if text != "" {
		b.r.WriteString(b.style(st).Render(text))
	}
	return b
}

// spans writes text with the byte spans highlighted.
func (b *rb) spans(text string, sp []api.Span, base lipgloss.Style) *rb {
	pos := 0
	for _, s := range sp {
		if s.Start < pos || s.End > len(text) || s.Start >= s.End {
			continue
		}
		b.add(text[pos:s.Start], base)
		b.add(text[s.Start:s.End], sMatch)
		pos = s.End
	}
	b.add(text[pos:], base)
	return b
}

func (b *rb) line(w int) string {
	ls, rs := b.l.String(), b.r.String()
	wl, wr := wid(ls), wid(rs)
	if wr > 0 && wl+wr+1 > w {
		if wr+2 >= w {
			rs, wr = "", 0
		} else {
			ls = ansi.Truncate(ls, w-wr-1, "…")
			wl = wid(ls)
		}
	}
	if wl > w {
		ls = ansi.Truncate(ls, w, "…")
		wl = wid(ls)
	}
	gap := max(w-wl-wr, 0)
	return ls + b.style(sBase).Render(strings.Repeat(" ", gap)) + rs
}

// window keeps cur visible in a list of n rows drawn in h rows; returns [start,end).
func window(cur int, off *int, n, h int) (int, int) {
	if h <= 0 || n == 0 {
		return 0, 0
	}
	if cur < *off {
		*off = cur
	}
	if cur >= *off+h {
		*off = cur - h + 1
	}
	if *off > n-h {
		*off = n - h
	}
	if *off < 0 {
		*off = 0
	}
	return *off, min(n, *off+h)
}

func fillTo(lines []string, w, h int) []string {
	if h < 0 {
		h = 0
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}

func vsep(h int) []string {
	out := make([]string, h)
	for i := range out {
		out[i] = sFaint.Render("│")
	}
	return out
}

func title(text string, w int, focused bool) string {
	st := sTitleN
	if focused {
		st = sTitleF
	}
	if wid(text) > w {
		text = ansi.Truncate(text, w, "…")
	}
	return st.Render(pad(text, w))
}

// overlay draws box onto base at (x,y).
func overlay(base []string, box []string, x, y, W int) []string {
	out := append([]string(nil), base...)
	for i, bl := range box {
		r := y + i
		if r < 0 || r >= len(out) {
			continue
		}
		bw := wid(bl)
		out[r] = ansi.Cut(out[r], 0, x) + bl + ansi.Cut(out[r], x+bw, W)
	}
	return out
}

// popup wraps content lines in a box with a title.
func popup(ttl string, lines []string, w int) []string {
	inner := w - 2
	bs := fg("75")
	out := []string{bs.Render("╭─ ") + sBold.Render(ttl) + bs.Render(" "+strings.Repeat("─", max(0, inner-wid(ttl)-3))+"╮")}
	for _, l := range lines {
		out = append(out, bs.Render("│")+fit(l, inner)+bs.Render("│"))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

// input is a single-line editor whose text is coloured by the query parser.
type input struct {
	r   []rune
	pos int
}

func (in *input) value() string { return string(in.r) }
func (in *input) set(s string)  { in.r = []rune(s); in.pos = len(in.r) }
func (in *input) insert(s string) {
	rs := []rune(s)
	in.r = append(in.r[:in.pos], append(rs, in.r[in.pos:]...)...)
	in.pos += len(rs)
}

// handle applies an editing key and reports whether the text changed.
func (in *input) handle(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "backspace":
		if in.pos > 0 {
			in.r = append(in.r[:in.pos-1], in.r[in.pos:]...)
			in.pos--
			return true
		}
		return false
	case "delete":
		if in.pos < len(in.r) {
			in.r = append(in.r[:in.pos], in.r[in.pos+1:]...)
			return true
		}
		return false
	case "left", "ctrl+b":
		in.pos = max(0, in.pos-1)
		return false
	case "right", "ctrl+f":
		in.pos = min(len(in.r), in.pos+1)
		return false
	case "home", "ctrl+a":
		in.pos = 0
		return false
	case "end":
		in.pos = len(in.r)
		return false
	case "ctrl+u":
		ch := len(in.r) > 0
		in.r, in.pos = nil, 0
		return ch
	case "ctrl+w":
		i := in.pos
		for i > 0 && in.r[i-1] == ' ' {
			i--
		}
		for i > 0 && in.r[i-1] != ' ' {
			i--
		}
		ch := i != in.pos
		in.r = append(in.r[:i], in.r[in.pos:]...)
		in.pos = i
		return ch
	}
	if t := k.Key().Text; t != "" && k.Key().Mod&^tea.ModShift == 0 {
		in.insert(t)
		return true
	}
	return false
}

// render draws the input, colouring filter keys, values, negation and
// invalid values from the parsed query.
func (in *input) render(q api.ParsedQuery, cursor bool, placeholder string) string {
	raw := in.value()
	if raw == "" {
		cur := ""
		if cursor {
			cur = lipgloss.NewStyle().Reverse(true).Render(" ")
		}
		return cur + sFaint.Render(placeholder)
	}
	cls := make([]lipgloss.Style, len(raw))
	for i := range cls {
		cls[i] = sDim
	}
	paint := func(sp api.Span, st lipgloss.Style) {
		for i := max(sp.Start, 0); i < sp.End && i < len(cls); i++ {
			cls[i] = st
		}
	}
	for _, t := range q.Terms {
		if q.Path != nil {
			paint(t.Span, sKey)
		} else {
			paint(t.Span, sTerm)
		}
	}
	for _, f := range q.Filters {
		ks := sKey
		if f.Negated {
			ks = sNeg
		}
		paint(f.Span, ks)
		vs := sVal
		if f.Status == api.FilterInvalid {
			vs = sBadV
		}
		paint(f.ValueSpan, vs)
	}
	var sb strings.Builder
	bi := 0
	for i, r := range in.r {
		st := sDim
		if bi < len(cls) {
			st = cls[bi]
		}
		if cursor && i == in.pos {
			st = st.Reverse(true)
		}
		sb.WriteString(st.Render(string(r)))
		bi += len(string(r))
	}
	if cursor && in.pos >= len(in.r) {
		sb.WriteString(lipgloss.NewStyle().Reverse(true).Render(" "))
	}
	return sb.String()
}

// chips shows what the parser understood.
func chips(q api.ParsedQuery, w int, scope string) string {
	return chipsWith(q, w, scope, `p: d: type: col: label: desc:  -neg  a,b=OR  "quoted"  proj.ds.table`)
}

// historyHint lists the History search filters.
const historyHint = `status:done|failed|cancelled  project:  source:tui|cli|mcp  since:7d|2026-10-01  -neg  a,b=OR`

// chipsWith shows the parsed query, or hint when it is empty.
func chipsWith(q api.ParsedQuery, w int, scope, hint string) string {
	var parts []string
	if scope != "" {
		parts = append(parts, sDim.Render("⊂ "+scope))
	}
	if q.Path != nil {
		parts = append(parts, sOk.Render("[path "+q.Path.String()+"]"))
	}
	for _, f := range q.Filters {
		st := sKey
		key := f.Key
		if f.Negated {
			st, key = sNeg, "-"+key
		}
		label := key + ":" + strings.Join(f.Values, "|")
		switch f.Status {
		case api.FilterPending:
			parts = append(parts, sFaint.Render("["+key+":…]"))
		case api.FilterInvalid:
			parts = append(parts, sBadV.Render("["+label+"]"))
		default:
			parts = append(parts, st.Render("["+label+"]"))
		}
	}
	if len(q.Terms) > 0 && q.Path == nil {
		var ts []string
		for _, t := range q.Terms {
			ts = append(ts, "“"+t.Text+"”")
		}
		parts = append(parts, sTerm.Render(strings.Join(ts, " ")))
	}
	if len(parts) == 0 || (len(parts) == 1 && scope != "") {
		parts = append(parts, sFaint.Render(hint))
	}
	return ansi.Truncate(strings.Join(parts, " "), w, "…")
}
