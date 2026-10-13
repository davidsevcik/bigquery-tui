package sqlx

import (
	"sort"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// TableName is a table as completion sees it.
type TableName struct {
	Name string
	Type api.TableType
}

// Catalog is the slice of the metadata index completion needs. Every lookup
// is scoped (one project, one dataset, one table), so it stays fast.
type Catalog interface {
	// DefaultProject is where an unqualified `dataset.table` resolves (the
	// Execution project).
	DefaultProject() string
	Projects() []string
	Datasets(project string) []string
	Tables(project, dataset string) []TableName
	Columns(project, dataset, table string) []api.ColumnMeta
}

// Options tunes Complete.
type Options struct {
	// Forced lifts the "two typed characters" rule (ctrl+space).
	Forced bool
	// Limit caps the candidates (default 50).
	Limit int
}

// Complete returns the candidates for the cursor position (a byte offset). It
// works on the token stream, so it never fails on a half-typed query. No
// candidates means the popup should stay closed.
func Complete(text string, cursor int, cat Catalog, o Options) []api.Candidate {
	if cursor < 0 || cursor > len(text) || cat == nil {
		return nil
	}
	if o.Limit <= 0 {
		o.Limit = 50
	}
	all := Tokenize(text)
	// the token the cursor is in or at the end of
	cur := -1
	for i, t := range all {
		if t.Start < cursor && cursor <= t.End {
			cur = i
			break
		}
	}
	if cur >= 0 {
		t := all[cur]
		switch t.Kind {
		case api.TokComment:
			if cursor < t.End || !strings.HasSuffix(text[t.Start:t.End], "*/") {
				return nil
			}
		case api.TokString:
			if cursor < t.End || unterminatedString(text[t.Start:t.End]) {
				return nil
			}
		case api.TokNumber, api.TokParam:
			return nil
		}
	}
	lo, hi := statementBounds(text, all, cursor)
	toks := all[lo:hi]
	cur -= lo
	sc := analyze(text, toks, cat, cursor)

	c := &completer{text: text, cat: cat, sc: sc, o: o}
	if cur >= 0 && cur < len(toks) && toks[cur].Kind == api.TokQuoted {
		t := toks[cur]
		raw := text[t.Start:t.End]
		if cursor < t.End || len(raw) < 2 || raw[len(raw)-1] != '`' {
			inner := text[t.Start+1 : cursor]
			segs := strings.Split(inner, ".")
			partial := segs[len(segs)-1]
			end := cursor
			for end < t.End && text[end] != '.' && text[end] != '`' {
				end++
			}
			c.pStart, c.pEnd, c.partial, c.quoted, c.chainStart = cursor-len(partial), end, partial, true, t.Start
			return c.finish(c.path(segs[:len(segs)-1], len(segs) == 1))
		}
	}

	// the dotted chain that ends at the cursor
	k := cur
	c.pStart, c.pEnd = cursor, cursor
	if cur >= 0 && cur < len(toks) && isWord(toks[cur]) {
		t := toks[cur]
		c.partial, c.pStart, c.pEnd = text[t.Start:cursor], t.Start, t.End
		k = cur - 1
	} else {
		k = -1
		for i, t := range toks {
			if t.End <= cursor {
				k = i
			}
		}
	}
	var quals []string
	start := c.pStart
	for k >= 1 && isPunct(text, toks[k], ".") && toks[k].End == start && toks[k-1].End == toks[k].Start && (isWord(toks[k-1]) || toks[k-1].Kind == api.TokQuoted) {
		seg, s, nk := wordWithHyphens(text, toks, k-1)
		quals = append(seg, quals...)
		start, k = s, nk
	}
	c.chainStart = start
	tableCtx := k >= 0 && inTableContext(text, toks, k)

	if len(quals) == 0 {
		if len(c.partial) < 2 && !o.Forced {
			return nil
		}
		if tableCtx {
			return c.finish(c.path(nil, true))
		}
		return c.finish(c.unqualified())
	}
	if tableCtx {
		return c.finish(c.path(quals, false))
	}
	if cands, ok := c.fields(quals); ok {
		return c.finish(cands)
	}
	return c.finish(c.path(quals, false))
}

func unterminatedString(s string) bool {
	i := 0
	for i < len(s) && s[i] != '\'' && s[i] != '"' {
		i++
	}
	if i >= len(s) {
		return true
	}
	q := s[i]
	body := s[i:]
	if strings.HasPrefix(body, string([]byte{q, q, q})) {
		return len(body) < 6 || !strings.HasSuffix(body, string([]byte{q, q, q}))
	}
	return len(body) < 2 || body[len(body)-1] != q
}

func isWord(t api.Token) bool {
	return t.Kind == api.TokIdent || t.Kind == api.TokKeyword || t.Kind == api.TokFunction
}

func isPunct(text string, t api.Token, p string) bool {
	return t.Kind == api.TokPunct && text[t.Start:t.End] == p
}

func isKeyword(text string, t api.Token, words ...string) bool {
	if t.Kind != api.TokKeyword {
		return false
	}
	w := text[t.Start:t.End]
	for _, x := range words {
		if strings.EqualFold(w, x) {
			return true
		}
	}
	return false
}

// statementBounds returns the token range of the statement around the cursor.
func statementBounds(text string, toks []api.Token, cursor int) (int, int) {
	lo, hi := 0, len(toks)
	for i, t := range toks {
		if !isPunct(text, t, ";") {
			continue
		}
		if t.End <= cursor {
			lo = i + 1
		} else {
			hi = i
			break
		}
	}
	return lo, hi
}

// wordWithHyphens reads the path segment ending at toks[i], joining an
// unquoted hyphenated project id (my-project). It returns the segments (a
// quoted token can hold several), the start offset and the index of the token
// before the segment.
func wordWithHyphens(text string, toks []api.Token, i int) ([]string, int, int) {
	t := toks[i]
	if t.Kind == api.TokQuoted {
		return strings.Split(strings.Trim(text[t.Start:t.End], "`"), "."), t.Start, i - 1
	}
	start := t.Start
	k := i - 1
	for k >= 1 && isPunct(text, toks[k], "-") && toks[k].End == start && toks[k-1].End == toks[k].Start &&
		(isWord(toks[k-1]) || toks[k-1].Kind == api.TokNumber) {
		start = toks[k-1].Start
		k -= 2
	}
	return []string{text[start:t.End]}, start, k
}

var clauseKeywords = []string{"SELECT", "WHERE", "ON", "GROUP", "ORDER", "HAVING", "QUALIFY", "LIMIT", "SET", "USING", "WINDOW", "VALUES", "WHEN", "THEN", "BY"}

// inTableContext reports whether a name after toks[k] names a table.
func inTableContext(text string, toks []api.Token, k int) bool {
	if isKeyword(text, toks[k], "FROM", "JOIN", "INTO", "UPDATE", "TABLE") {
		return !fromOfFunction(text, toks, k)
	}
	if !isPunct(text, toks[k], ",") {
		return false
	}
	// a comma join: walk back to the clause keyword at the same nesting depth
	depth := 0
	for i := k - 1; i >= 0; i-- {
		switch {
		case isPunct(text, toks[i], ")"):
			depth++
		case isPunct(text, toks[i], "("):
			if depth == 0 {
				return false
			}
			depth--
		case depth > 0:
		case isKeyword(text, toks[i], "FROM"):
			return !fromOfFunction(text, toks, i)
		case isKeyword(text, toks[i], clauseKeywords...), isKeyword(text, toks[i], "JOIN"):
			return false
		}
	}
	return false
}

// fromOfFunction reports whether the FROM at toks[i] belongs to
// EXTRACT(x FROM y) or a similar call rather than to a query.
func fromOfFunction(text string, toks []api.Token, i int) bool {
	depth := 0
	for j := i - 1; j >= 0; j-- {
		switch {
		case isPunct(text, toks[j], ")"):
			depth++
		case isPunct(text, toks[j], "("):
			if depth > 0 {
				depth--
				continue
			}
			if j == 0 {
				return false
			}
			w := strings.ToUpper(text[toks[j-1].Start:toks[j-1].End])
			return w == "EXTRACT" || w == "TRIM" || w == "SUBSTRING"
		case depth == 0 && isKeyword(text, toks[j], "SELECT"):
			return false
		}
	}
	return false
}

// --- scope

type relation struct {
	name   string   // alias, or the table's own name
	path   []string // table path segments (empty for a CTE, subquery or UNNEST)
	cte    *cte
	unnest []string // UNNEST(expr): the expression's path segments
	group  int
}

type cte struct {
	name     string
	from, to int // body token range, exclusive of the parentheses
	busy     bool
}

type scope struct {
	text   string
	toks   []api.Token
	cat    Catalog
	ctes   []*cte
	rels   []relation
	group  []int // per token: the query block it belongs to
	up     map[int]int
	at     int // the cursor's query block
	cursor int
}

func analyze(text string, toks []api.Token, cat Catalog, cursor int) *scope {
	s := &scope{text: text, toks: toks, cat: cat, group: make([]int, len(toks)), up: map[int]int{}, at: -1, cursor: cursor}
	// query blocks: a parenthesis that opens a subquery starts a block
	type frame struct{ group int }
	var stack []frame
	g := -1
	for i, t := range toks {
		if isPunct(text, t, "(") {
			stack = append(stack, frame{g})
			if i+1 < len(toks) && isKeyword(text, toks[i+1], "SELECT", "WITH") {
				s.up[i] = g
				g = i
			}
		} else if isPunct(text, t, ")") && len(stack) > 0 {
			g = stack[len(stack)-1].group
			stack = stack[:len(stack)-1]
		}
		s.group[i] = g
		if t.Start < cursor {
			s.at = g
		}
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		// name AS ( ... ) is a CTE
		if (t.Kind == api.TokIdent || t.Kind == api.TokQuoted) && i+2 < len(toks) && isKeyword(text, toks[i+1], "AS") && isPunct(text, toks[i+2], "(") {
			end := matchParen(text, toks, i+2)
			s.ctes = append(s.ctes, &cte{name: strings.Trim(text[t.Start:t.End], "`"), from: i + 3, to: end})
			continue
		}
		if !isKeyword(text, t, "FROM", "JOIN", "INTO", "UPDATE", "MERGE") || (isKeyword(text, t, "FROM") && fromOfFunction(text, toks, i)) {
			continue
		}
		j := i + 1
		for j < len(toks) {
			rel, next, ok := s.parseRef(j)
			if !ok {
				break
			}
			rel.group = s.group[i]
			s.rels = append(s.rels, rel)
			j = next
			if !isKeyword(text, t, "FROM") || j >= len(toks) || !isPunct(text, toks[j], ",") {
				break
			}
			j++
		}
	}
	return s
}

func matchParen(text string, toks []api.Token, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch {
		case isPunct(text, toks[i], "("):
			depth++
		case isPunct(text, toks[i], ")"):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(toks)
}

// parseRef reads one table reference with its alias starting at toks[i].
func (s *scope) parseRef(i int) (relation, int, bool) {
	text, toks := s.text, s.toks
	var rel relation
	t := toks[i]
	switch {
	case isPunct(text, t, "("):
		i = matchParen(text, toks, i) + 1
	case isWord(t) && strings.EqualFold(text[t.Start:t.End], "UNNEST") && i+1 < len(toks) && isPunct(text, toks[i+1], "("):
		end := matchParen(text, toks, i+1)
		for j := i + 2; j < end && j < len(toks); j++ {
			if isWord(toks[j]) || toks[j].Kind == api.TokQuoted {
				rel.unnest = append(rel.unnest, strings.Split(strings.Trim(text[toks[j].Start:toks[j].End], "`"), ".")...)
			} else if !isPunct(text, toks[j], ".") {
				rel.unnest = nil
				break
			}
		}
		if rel.unnest == nil {
			rel.unnest = []string{}
		}
		i = end + 1
	case t.Kind == api.TokQuoted || t.Kind == api.TokIdent || t.Kind == api.TokNumber:
		// a dotted path, possibly with a hyphenated project id
		for {
			seg := text[toks[i].Start:toks[i].End]
			if toks[i].Kind == api.TokQuoted {
				rel.path = append(rel.path, strings.Split(strings.Trim(seg, "`"), ".")...)
			} else {
				for i+2 < len(toks) && isPunct(text, toks[i+1], "-") && toks[i+1].Start == toks[i].End && toks[i+2].Start == toks[i+1].End &&
					(isWord(toks[i+2]) || toks[i+2].Kind == api.TokNumber) {
					seg += "-" + text[toks[i+2].Start:toks[i+2].End]
					i += 2
				}
				rel.path = append(rel.path, seg)
			}
			if i+2 < len(toks) && isPunct(text, toks[i+1], ".") && toks[i+1].Start == toks[i].End && toks[i+2].Start == toks[i+1].End &&
				(isWord(toks[i+2]) || toks[i+2].Kind == api.TokQuoted) {
				i += 2
				continue
			}
			break
		}
		i++
		rel.name = rel.path[len(rel.path)-1]
		if len(rel.path) == 1 {
			for _, c := range s.ctes {
				if strings.EqualFold(c.name, rel.name) {
					rel.cte = c
				}
			}
		}
	default:
		return rel, i, false
	}
	if i < len(toks) && isKeyword(text, toks[i], "AS") {
		i++
	}
	// the word being typed is not an alias yet
	typing := i < len(toks) && toks[i].Start < s.cursor && s.cursor <= toks[i].End
	if i < len(toks) && !typing && (toks[i].Kind == api.TokIdent || toks[i].Kind == api.TokQuoted) {
		rel.name = strings.Trim(text[toks[i].Start:toks[i].End], "`")
		i++
	}
	return rel, i, true
}

// visible returns the relations the cursor's query block can see: its own and
// those of the enclosing blocks. With none, every relation of the statement.
func (s *scope) visible() []relation {
	ok := map[int]bool{}
	for g := s.at; ; g = s.up[g] {
		ok[g] = true
		if g == -1 {
			break
		}
	}
	var out []relation
	for _, r := range s.rels {
		if ok[r.group] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return s.rels
	}
	return out
}

func (s *scope) inGroup(g int) []relation {
	var out []relation
	for _, r := range s.rels {
		if r.group == g {
			out = append(out, r)
		}
	}
	return out
}

// columns resolves a relation's columns.
func (s *scope) columns(r relation, among []relation) []api.ColumnMeta {
	switch {
	case r.cte != nil:
		return s.cteColumns(r.cte)
	case r.unnest != nil:
		return s.unnestColumns(r, among)
	case len(r.path) == 0:
		return nil
	}
	p, d, t, ok := s.resolve(r.path)
	if !ok {
		return nil
	}
	return s.cat.Columns(p, d, t)
}

func (s *scope) resolve(path []string) (string, string, string, bool) {
	switch len(path) {
	case 3:
		return path[0], path[1], path[2], true
	case 2:
		if p, ok := s.projectOf(path[0]); ok {
			return p, path[0], path[1], true
		}
	}
	return "", "", "", false
}

func (s *scope) projectOf(dataset string) (string, bool) {
	def := s.cat.DefaultProject()
	projects := append([]string{def}, s.cat.Projects()...)
	for _, p := range projects {
		if p == "" {
			continue
		}
		for _, d := range s.cat.Datasets(p) {
			if d == dataset {
				return p, true
			}
		}
	}
	return "", false
}

func (s *scope) unnestColumns(r relation, among []relation) []api.ColumnMeta {
	if len(r.unnest) == 0 {
		return nil
	}
	try := func(cols []api.ColumnMeta, path []string) []api.ColumnMeta {
		prefix := strings.ToLower(strings.Join(path, ".")) + "."
		var out []api.ColumnMeta
		for _, c := range cols {
			if strings.HasPrefix(strings.ToLower(c.FieldPath), prefix) {
				c.FieldPath = c.FieldPath[len(prefix):]
				out = append(out, c)
			}
		}
		return out
	}
	for _, o := range among {
		if o.unnest != nil {
			continue
		}
		if len(r.unnest) > 1 && strings.EqualFold(o.name, r.unnest[0]) {
			if out := try(s.columns(o, nil), r.unnest[1:]); len(out) > 0 {
				return out
			}
		}
	}
	for _, o := range among {
		if o.unnest != nil {
			continue
		}
		if out := try(s.columns(o, nil), r.unnest); len(out) > 0 {
			return out
		}
	}
	return nil
}

// cteColumns reads a CTE's columns from its select list: explicit aliases and
// plain column names, or the source tables' columns for `*`.
func (s *scope) cteColumns(c *cte) []api.ColumnMeta {
	if c.busy {
		return nil
	}
	c.busy = true
	defer func() { c.busy = false }()
	text, toks := s.text, s.toks
	// the first SELECT of the body at depth 0
	i := c.from
	depth := 0
	for ; i < c.to; i++ {
		if isPunct(text, toks[i], "(") {
			depth++
		} else if isPunct(text, toks[i], ")") {
			depth--
		} else if depth == 0 && isKeyword(text, toks[i], "SELECT") {
			break
		}
	}
	if i >= c.to {
		return nil
	}
	body := s.inGroup(s.group[i])
	i++
	for i < c.to && (isKeyword(text, toks[i], "DISTINCT", "ALL", "AS", "STRUCT") || (isWord(toks[i]) && strings.EqualFold(text[toks[i].Start:toks[i].End], "VALUE"))) {
		i++
	}
	var out []api.ColumnMeta
	add := func(name string) {
		out = append(out, api.ColumnMeta{FieldPath: name, Ordinal: len(out) + 1})
	}
	item := []int{}
	flush := func() {
		defer func() { item = item[:0] }()
		if len(item) == 0 {
			return
		}
		// star, with an optional qualifier and EXCEPT list
		for n, ti := range item {
			if !isPunct(text, toks[ti], "*") || (n > 0 && !isPunct(text, toks[item[n-1]], ".")) {
				continue
			}
			except := map[string]bool{}
			for _, ei := range item[n+1:] {
				if toks[ei].Kind == api.TokIdent || toks[ei].Kind == api.TokQuoted {
					except[strings.ToLower(strings.Trim(text[toks[ei].Start:toks[ei].End], "`"))] = true
				}
			}
			src := body
			if n >= 2 {
				q := strings.Trim(text[toks[item[n-2]].Start:toks[item[n-2]].End], "`")
				src = nil
				for _, r := range body {
					if strings.EqualFold(r.name, q) {
						src = append(src, r)
					}
				}
			}
			for _, r := range src {
				for _, col := range s.columns(r, body) {
					if col.TopLevel() && !except[strings.ToLower(col.FieldPath)] {
						col.Ordinal = len(out) + 1
						out = append(out, col)
					}
				}
			}
			return
		}
		last := toks[item[len(item)-1]]
		if last.Kind == api.TokIdent || last.Kind == api.TokQuoted || (last.Kind == api.TokKeyword && len(item) > 1 && isPunct(text, toks[item[len(item)-2]], ".")) {
			add(strings.Trim(text[last.Start:last.End], "`"))
		}
	}
	depth = 0
	for ; i < c.to; i++ {
		t := toks[i]
		switch {
		case isPunct(text, t, "("):
			depth++
		case isPunct(text, t, ")"):
			depth--
		case depth == 0 && isPunct(text, t, ","):
			flush()
			continue
		case depth == 0 && isKeyword(text, t, "FROM"):
			flush()
			return out
		}
		item = append(item, i)
	}
	flush()
	return out
}

// --- candidates

type completer struct {
	text       string
	cat        Catalog
	sc         *scope
	o          Options
	partial    string
	pStart     int
	pEnd       int
	chainStart int
	quoted     bool
}

type ranked struct {
	c     api.Candidate
	rank  int
	order int
}

// match ranks a label against the typed text: a prefix first, then (for
// catalog objects only) a substring.
func (c *completer) match(label string, kind api.CandidateKind) (int, bool) {
	if c.partial == "" {
		return 0, true
	}
	l, p := strings.ToLower(label), strings.ToLower(c.partial)
	switch {
	case strings.HasPrefix(l, p):
		return 0, true
	case kind == api.CandKeyword || kind == api.CandFunction:
	case len(p) >= 2 && strings.Contains(l, p):
		return 1, true
	}
	return 0, false
}

func (c *completer) add(out *[]ranked, order int, cand api.Candidate) {
	rank, ok := c.match(cand.Label, cand.Kind)
	if !ok {
		return
	}
	if cand.Insert == "" {
		cand.Insert = cand.Label
	}
	if cand.Start == 0 && cand.End == 0 {
		cand.Start, cand.End = c.pStart, c.pEnd
	}
	*out = append(*out, ranked{cand, rank, order})
}

func (c *completer) finish(rs []ranked) []api.Candidate {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return strings.ToLower(a.c.Label) < strings.ToLower(b.c.Label)
	})
	seen := map[string]bool{}
	var out []api.Candidate
	for _, r := range rs {
		key := string(r.c.Kind) + "\x00" + r.c.Label + "\x00" + r.c.Insert
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r.c)
		if len(out) >= c.o.Limit {
			break
		}
	}
	// typing a whole candidate is not a reason to keep the popup open
	if len(out) == 1 && out[0].Insert == c.partial && out[0].End == c.pEnd {
		return nil
	}
	return out
}

func plainIdent(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

func eqFold(list []string, s string) (string, bool) {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return x, true
		}
	}
	return "", false
}

// path completes a project.dataset.table path segment by segment.
func (c *completer) path(segs []string, withCTEs bool) []ranked {
	var out []ranked
	tables := func(project, dataset string) {
		for _, t := range c.cat.Tables(project, dataset) {
			cand := api.Candidate{Label: t.Name, Kind: api.CandTable, Detail: string(t.Type)}
			if !c.quoted && !plainIdent(t.Name) {
				// the name needs backticks: replace the whole path
				cand.Insert = "`" + project + "." + dataset + "." + t.Name + "`"
				cand.Start, cand.End = c.chainStart, c.pEnd
			}
			c.add(&out, 2, cand)
		}
	}
	switch len(segs) {
	case 0:
		if withCTEs {
			for _, ct := range c.sc.ctes {
				c.add(&out, 0, api.Candidate{Label: ct.name, Kind: api.CandCTE, Detail: "CTE"})
			}
		}
		def := c.cat.DefaultProject()
		for _, d := range c.cat.Datasets(def) {
			c.add(&out, 1, api.Candidate{Label: d, Kind: api.CandDataset, Detail: def})
		}
		for _, p := range c.cat.Projects() {
			c.add(&out, 2, api.Candidate{Label: p, Kind: api.CandProject, Detail: "project"})
			if p == def {
				continue
			}
			for _, d := range c.cat.Datasets(p) {
				c.add(&out, 3, api.Candidate{Label: d, Insert: p + "." + d, Kind: api.CandDataset, Detail: p})
			}
		}
	case 1:
		if p, ok := eqFold(c.cat.Projects(), segs[0]); ok {
			for _, d := range c.cat.Datasets(p) {
				c.add(&out, 1, api.Candidate{Label: d, Kind: api.CandDataset, Detail: p})
			}
		}
		if p, ok := c.sc.projectOf(segs[0]); ok {
			tables(p, segs[0])
		}
	case 2:
		tables(segs[0], segs[1])
	}
	return out
}

func fieldCandidates(cols []api.ColumnMeta, parent string) []api.Candidate {
	var out []api.Candidate
	for _, col := range cols {
		if !strings.EqualFold(col.ParentPath(), parent) {
			continue
		}
		leaf := col.FieldPath[strings.LastIndex(col.FieldPath, ".")+1:]
		out = append(out, api.Candidate{Label: leaf, Kind: api.CandColumn, Detail: col.DataType})
	}
	return out
}

// fields completes after "alias." or "struct_column.": the columns of the
// relation, or the fields under a struct path.
func (c *completer) fields(quals []string) ([]ranked, bool) {
	var out []ranked
	vis := c.sc.visible()
	found := false
	for _, r := range vis {
		if !strings.EqualFold(r.name, quals[0]) {
			continue
		}
		found = true
		for _, cand := range fieldCandidates(c.sc.columns(r, vis), strings.Join(quals[1:], ".")) {
			c.add(&out, 0, cand)
		}
	}
	if found {
		return out, true
	}
	// a struct column of any visible relation
	for _, r := range vis {
		cands := fieldCandidates(c.sc.columns(r, vis), strings.Join(quals, "."))
		if len(cands) > 0 {
			found = true
		}
		for _, cand := range cands {
			c.add(&out, 0, cand)
		}
	}
	return out, found
}

// unqualified completes a bare word: columns of the visible relations, their
// aliases, functions and keywords.
func (c *completer) unqualified() []ranked {
	var out []ranked
	vis := c.sc.visible()
	for _, r := range vis {
		for _, cand := range fieldCandidates(c.sc.columns(r, vis), "") {
			if cand.Detail == "" {
				cand.Detail = r.name
			}
			if !plainIdent(cand.Label) {
				cand.Insert = "`" + cand.Label + "`"
			}
			c.add(&out, 0, cand)
		}
		if r.name != "" {
			detail := "alias"
			if len(r.path) > 0 {
				detail = strings.Join(r.path, ".")
			}
			c.add(&out, 1, api.Candidate{Label: r.name, Kind: api.CandAlias, Detail: detail})
		}
	}
	for _, ct := range c.sc.ctes {
		c.add(&out, 1, api.Candidate{Label: ct.name, Kind: api.CandCTE, Detail: "CTE"})
	}
	for _, k := range completionKeywords {
		c.add(&out, 2, api.Candidate{Label: k, Kind: api.CandKeyword})
	}
	for _, f := range functions {
		c.add(&out, 3, api.Candidate{Label: f.Name, Insert: f.Name + "(", Kind: api.CandFunction, Detail: f.Signature})
	}
	return out
}
