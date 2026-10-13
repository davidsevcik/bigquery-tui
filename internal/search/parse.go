// Package search implements catalog search: the query grammar, tiered
// ranking, shard-family grouping and the browse lists (spec section 8). It is
// pure core logic over an in-memory index of projects, datasets and tables;
// columns are consulted only for the col: filter.
package search

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// FilterKeys are the recognised filter keys. Anything else stays a free term.
var FilterKeys = map[string]bool{"p": true, "d": true, "type": true, "col": true, "label": true, "desc": true}

// TypeNames are the values of the type: filter (matched by prefix).
var TypeNames = []string{"table", "view", "mview", "external", "snapshot", "clone"}

// Parse turns a raw query into its structured form. It never fails: an
// incomplete or invalid filter is reported through its Status so a frontend
// can show it as a pending or red chip.
func Parse(raw string) api.ParsedQuery {
	q := api.ParsedQuery{Raw: raw}
	i := 0
	for i < len(raw) {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			break
		}
		start, inQ := i, false
		for i < len(raw) && (inQ || (raw[i] != ' ' && raw[i] != '\t')) {
			if raw[i] == '"' {
				inQ = !inQ
			}
			i++
		}
		parseToken(&q, start, i)
	}
	// A single pasted `project.dataset.table` is an exact path lookup.
	if len(q.Terms) == 1 && len(q.Filters) == 0 {
		if p, ok := SplitPath(q.Terms[0].Text); ok {
			q.Path = &p
		}
	}
	return q
}

// SplitPath splits project.dataset.table (optionally backticked). The project
// may itself contain dots (domain-scoped ids), so the last two segments are
// the dataset and the table.
func SplitPath(s string) (api.ObjectPath, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "`", "")
	parts := strings.Split(s, ".")
	if len(parts) < 3 {
		return api.ObjectPath{}, false
	}
	for _, p := range parts {
		if p == "" {
			return api.ObjectPath{}, false
		}
	}
	n := len(parts)
	return api.ObjectPath{Project: strings.Join(parts[:n-2], "."), Dataset: parts[n-2], Table: parts[n-1]}, true
}

func parseToken(q *api.ParsedQuery, s, e int) {
	tok := q.Raw[s:e]
	body, neg := tok, false
	if strings.HasPrefix(body, "-") && len(body) > 1 {
		body, neg = body[1:], true
	}
	if ci := strings.IndexByte(body, ':'); ci > 0 && FilterKeys[strings.ToLower(body[:ci])] {
		f := api.QueryFilter{Key: strings.ToLower(body[:ci]), Negated: neg, Status: api.FilterOK,
			Span: api.Span{Start: s, End: e}, ValueSpan: api.Span{Start: e - (len(body) - ci - 1), End: e}}
		for _, v := range splitValues(body[ci+1:]) {
			v = strings.ToLower(strings.Trim(v, `"`))
			if v != "" {
				f.Values = append(f.Values, v)
			}
		}
		if len(f.Values) == 0 {
			f.Status = api.FilterPending
		}
		if f.Key == "type" {
			for _, v := range f.Values {
				ok := false
				for _, k := range TypeNames {
					if strings.HasPrefix(k, v) {
						ok = true
					}
				}
				if !ok {
					f.Status = api.FilterInvalid
				}
			}
		}
		q.Filters = append(q.Filters, f)
		return
	}
	t := strings.ToLower(strings.Trim(tok, `"`))
	if t == "" {
		return
	}
	q.Terms = append(q.Terms, api.QueryTerm{Text: t, Span: api.Span{Start: s, End: e}})
}

func splitValues(s string) []string {
	var out []string
	inQ, st := false, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case ',':
			if !inQ {
				out = append(out, s[st:i])
				st = i + 1
			}
		}
	}
	return append(out, s[st:])
}
