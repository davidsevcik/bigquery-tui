package search

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

var FilterKeys = map[string]bool{"p": true, "d": true, "type": true, "col": true, "label": true, "desc": true}

var TypeNames = []string{"table", "view", "mview", "external", "snapshot", "clone"}

func Parse(raw string) api.ParsedQuery {
	q := ParseWith(raw, FilterKeys, validateCatalogFilter)
	if len(q.Terms) == 1 && len(q.Filters) == 0 {
		if p, ok := SplitPath(strings.Trim(raw[q.Terms[0].Span.Start:q.Terms[0].Span.End], `"`)); ok {
			q.Path = &p
		}
	}
	return q
}

func ParseWith(raw string, keys map[string]bool, validate func(*api.QueryFilter)) api.ParsedQuery {
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
		parseToken(&q, start, i, keys, validate)
	}
	return q
}

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

func parseToken(q *api.ParsedQuery, s, e int, keys map[string]bool, validate func(*api.QueryFilter)) {
	tok := q.Raw[s:e]
	body, neg := tok, false
	if strings.HasPrefix(body, "-") && len(body) > 1 {
		body, neg = body[1:], true
	}
	if ci := strings.IndexByte(body, ':'); ci > 0 && keys[strings.ToLower(body[:ci])] {
		f := api.QueryFilter{
			Key: strings.ToLower(body[:ci]), Negated: neg, Status: api.FilterOK,
			Span: api.Span{Start: s, End: e}, ValueSpan: api.Span{Start: e - (len(body) - ci - 1), End: e},
		}
		for _, v := range splitValues(body[ci+1:]) {
			v = strings.ToLower(strings.Trim(v, `"`))
			if v != "" {
				f.Values = append(f.Values, v)
			}
		}
		if len(f.Values) == 0 {
			f.Status = api.FilterPending
		} else if validate != nil {
			validate(&f)
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

func validateCatalogFilter(f *api.QueryFilter) {
	if f.Key == "type" {
		for _, v := range f.Values {
			if !anyPrefix(TypeNames, v) {
				f.Status = api.FilterInvalid
			}
		}
	}
}

func anyPrefix(names []string, v string) bool {
	for _, k := range names {
		if strings.HasPrefix(k, v) {
			return true
		}
	}
	return false
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
