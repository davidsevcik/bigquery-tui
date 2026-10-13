// Package sqlx is bqt's SQL language intelligence: a BigQuery tokenizer and a
// completion engine built on the token stream. Both are pure functions that
// never fail on broken input, because a half-typed query is the editor's
// normal state. Offsets are UTF-8 byte offsets.
package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Tokenize splits SQL into tokens. Whitespace yields no token; every other
// byte belongs to exactly one token, and tokens are in order.
func Tokenize(text string) []api.Token {
	var out []api.Token
	n := len(text)
	i := 0
	emit := func(start, end int, k api.TokenKind) { out = append(out, api.Token{Start: start, End: end, Kind: k}) }
	for i < n {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < n && text[i+1] == '-', c == '#':
			j := strings.IndexByte(text[i:], '\n')
			if j < 0 {
				j = n - i
			}
			emit(i, i+j, api.TokComment)
			i += j
		case c == '/' && i+1 < n && text[i+1] == '*':
			j := strings.Index(text[i+2:], "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			emit(i, end, api.TokComment)
			i = end
		case c == '\'' || c == '"':
			end := scanString(text, i, false)
			emit(i, end, api.TokString)
			i = end
		case c == '`':
			j := i + 1
			for j < n && text[j] != '`' && text[j] != '\n' {
				if text[j] == '\\' && j+1 < n {
					j++
				}
				j++
			}
			if j < n && text[j] == '`' {
				j++
			}
			emit(i, j, api.TokQuoted)
			i = j
		case c == '@':
			j := i + 1
			if j < n && text[j] == '@' {
				j++
			}
			for j < n && isIdentByte(text[j]) {
				j++
			}
			emit(i, j, api.TokParam)
			i = j
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(text[i+1]) && !prevIsIdent(out, text, i)):
			end := scanNumber(text, i)
			emit(i, end, api.TokNumber)
			i = end
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentByte(text[j]) {
				j++
			}
			word := text[i:j]
			// string prefixes: r'..', b"..", rb'..', br'..'
			if j < n && (text[j] == '\'' || text[j] == '"') && isStringPrefix(word) {
				end := scanString(text, j, strings.ContainsAny(word, "rR"))
				emit(i, end, api.TokString)
				i = end
				continue
			}
			kind := api.TokIdent
			up := strings.ToUpper(word)
			switch {
			case prevIsDot(out, text, i):
				// a field or path segment, whatever it is spelled like
			case keywords[up]:
				kind = api.TokKeyword
			default:
				k := j
				for k < n && (text[k] == ' ' || text[k] == '\t') {
					k++
				}
				if k < n && text[k] == '(' {
					kind = api.TokFunction
				}
			}
			emit(i, j, kind)
			i = j
		default:
			// one punctuation character (a multi-byte rune stays whole)
			j := i + 1
			for j < n && text[j]&0xC0 == 0x80 {
				j++
			}
			emit(i, j, api.TokPunct)
			i = j
		}
	}
	return out
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentByte(c byte) bool  { return isIdentStart(c) || isDigit(c) }

func isStringPrefix(w string) bool {
	switch strings.ToLower(w) {
	case "r", "b", "rb", "br":
		return true
	}
	return false
}

func prevIsDot(toks []api.Token, text string, at int) bool {
	if len(toks) == 0 {
		return false
	}
	p := toks[len(toks)-1]
	return p.End == at && p.Kind == api.TokPunct && text[p.Start:p.End] == "."
}

func prevIsIdent(toks []api.Token, text string, at int) bool {
	if len(toks) == 0 {
		return false
	}
	p := toks[len(toks)-1]
	return p.End == at && (p.Kind == api.TokIdent || p.Kind == api.TokQuoted || p.Kind == api.TokKeyword || p.Kind == api.TokFunction)
}

// scanString returns the end of the string literal starting at the quote at i.
// An unterminated literal ends at the line end (triple-quoted: at the text end).
func scanString(text string, i int, raw bool) int {
	n := len(text)
	q := text[i]
	if i+2 < n && text[i+1] == q && text[i+2] == q {
		j := i + 3
		for j < n {
			if text[j] == '\\' && !raw && j+1 < n {
				j += 2
				continue
			}
			if text[j] == q && j+2 < n && text[j+1] == q && text[j+2] == q {
				return j + 3
			}
			j++
		}
		return n
	}
	j := i + 1
	for j < n && text[j] != '\n' {
		if text[j] == '\\' && j+1 < n && text[j+1] != '\n' {
			// a raw string keeps the backslash but it still guards the quote
			j += 2
			continue
		}
		if text[j] == q {
			return j + 1
		}
		j++
	}
	return j
}

func scanNumber(text string, i int) int {
	n := len(text)
	j := i
	if text[j] == '0' && j+1 < n && (text[j+1] == 'x' || text[j+1] == 'X') {
		j += 2
		for j < n && (isDigit(text[j]) || (text[j]|0x20 >= 'a' && text[j]|0x20 <= 'f')) {
			j++
		}
		return j
	}
	for j < n && isDigit(text[j]) {
		j++
	}
	if j < n && text[j] == '.' {
		j++
		for j < n && isDigit(text[j]) {
			j++
		}
	}
	if j < n && (text[j] == 'e' || text[j] == 'E') {
		k := j + 1
		if k < n && (text[k] == '+' || text[k] == '-') {
			k++
		}
		if k < n && isDigit(text[k]) {
			for k < n && isDigit(text[k]) {
				k++
			}
			j = k
		}
	}
	return j
}

// Params lists the named query parameters (@name) in order of first use.
// System variables (@@name) are not parameters.
func Params(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range Tokenize(text) {
		if t.Kind != api.TokParam {
			continue
		}
		w := text[t.Start:t.End]
		if strings.HasPrefix(w, "@@") || len(w) < 2 {
			continue
		}
		name := w[1:]
		if !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			out = append(out, name)
		}
	}
	return out
}

// InferParam guesses a parameter's type from the text the user typed. A
// "TYPE:" prefix forces the type ("STRING:42"); "NULL" is a null STRING.
func InferParam(name, input string) api.QueryParam {
	p := api.QueryParam{Name: name, Type: "STRING", Value: input}
	if i := strings.IndexByte(input, ':'); i > 0 {
		t := strings.ToUpper(strings.TrimSpace(input[:i]))
		switch t {
		case "STRING", "INT64", "FLOAT64", "BOOL", "DATE", "DATETIME", "TIMESTAMP", "NUMERIC":
			p.Type, p.Value = t, strings.TrimSpace(input[i+1:])
			if strings.EqualFold(p.Value, "null") {
				p.Null, p.Value = true, ""
			}
			return p
		}
	}
	v := strings.TrimSpace(input)
	switch {
	case strings.EqualFold(v, "null"):
		p.Null, p.Value = true, ""
	case strings.EqualFold(v, "true"), strings.EqualFold(v, "false"):
		p.Type, p.Value = "BOOL", strings.ToLower(v)
	case isInt(v):
		p.Type, p.Value = "INT64", v
	case isFloat(v):
		p.Type, p.Value = "FLOAT64", v
	case isDate(v):
		p.Type, p.Value = "DATE", v
	case len(v) >= 19 && isDate(v[:10]) && (v[10] == ' ' || v[10] == 'T'):
		p.Type, p.Value = "TIMESTAMP", v
		last := v[len(v)-1]
		if isDigit(last) && !strings.ContainsAny(v[19:], "+-Zz") {
			p.Type = "DATETIME"
		}
	}
	return p
}

func isInt(v string) bool {
	if v == "" {
		return false
	}
	i := 0
	if v[0] == '-' || v[0] == '+' {
		i++
	}
	if i == len(v) {
		return false
	}
	for ; i < len(v); i++ {
		if !isDigit(v[i]) {
			return false
		}
	}
	return true
}

func isFloat(v string) bool {
	if v == "" {
		return false
	}
	end := scanNumber(v, 0)
	if v[0] == '-' || v[0] == '+' {
		if len(v) == 1 {
			return false
		}
		end = scanNumber(v, 1)
	}
	return end == len(v) && strings.ContainsAny(v, "0123456789")
}

func isDate(v string) bool {
	if len(v) != 10 || v[4] != '-' || v[7] != '-' {
		return false
	}
	for i, c := range []byte(v) {
		if i != 4 && i != 7 && !isDigit(c) {
			return false
		}
	}
	return true
}
