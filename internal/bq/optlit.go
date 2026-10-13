package bq

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// INFORMATION_SCHEMA *_OPTIONS views return option values as GoogleSQL
// literal text, for example:
//
//	description      "Raw \"events\" table"
//	labels           [STRUCT("team", "data"), STRUCT("env", "prod")]
//	expiration_timestamp  TIMESTAMP "2026-11-01 10:00:00+00"
//
// These helpers decode them.

// parseStringLiteral decodes one quoted string literal at the start of s
// (single, double or triple quoted, with backslash escapes) and returns the
// value and the unconsumed rest.
func parseStringLiteral(s string) (val, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	raw := false
	if len(s) > 1 && (s[0] == 'r' || s[0] == 'R') && (s[1] == '"' || s[1] == '\'') {
		raw = true
		s = s[1:]
	}
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return "", s, false
	}
	q := s[:1]
	delim := q
	if strings.HasPrefix(s, q+q+q) {
		delim = q + q + q
	}
	i := len(delim)
	var b strings.Builder
	for i < len(s) {
		if strings.HasPrefix(s[i:], delim) {
			return b.String(), s[i+len(delim):], true
		}
		c := s[i]
		if c == '\\' && !raw && i+1 < len(s) {
			n, adv := unescape(s[i+1:])
			b.WriteString(n)
			i += 1 + adv
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return "", s, false
}

func unescape(s string) (string, int) {
	switch s[0] {
	case 'n':
		return "\n", 1
	case 't':
		return "\t", 1
	case 'r':
		return "\r", 1
	case 'a':
		return "\a", 1
	case 'b':
		return "\b", 1
	case 'f':
		return "\f", 1
	case 'v':
		return "\v", 1
	case '0', '1', '2', '3', '4', '5', '6', '7':
		if len(s) >= 3 {
			if n, err := strconv.ParseUint(s[:3], 8, 8); err == nil {
				return string([]byte{byte(n)}), 3
			}
		}
	case 'x', 'X':
		if len(s) >= 3 {
			if n, err := strconv.ParseUint(s[1:3], 16, 8); err == nil {
				return string([]byte{byte(n)}), 3
			}
		}
	case 'u':
		if len(s) >= 5 {
			if n, err := strconv.ParseUint(s[1:5], 16, 32); err == nil {
				return string(rune(n)), 5
			}
		}
	case 'U':
		if len(s) >= 9 {
			if n, err := strconv.ParseUint(s[1:9], 16, 32); err == nil {
				return string(rune(n)), 9
			}
		}
	}
	// \\ \" \' \` and unknown escapes: the character itself
	_, size := utf8.DecodeRuneInString(s)
	return s[:size], size
}

// parseStringOption decodes a string-valued option such as description.
func parseStringOption(v string) string {
	if s, _, ok := parseStringLiteral(v); ok {
		return s
	}
	return strings.TrimSpace(v)
}

// parseLabelsOption decodes `[STRUCT("k", "v"), ...]`.
func parseLabelsOption(v string) map[string]string {
	out := map[string]string{}
	rest := v
	for {
		i := strings.Index(rest, "STRUCT")
		if i < 0 {
			break
		}
		rest = rest[i+len("STRUCT"):]
		j := strings.Index(rest, "(")
		if j < 0 {
			break
		}
		rest = rest[j+1:]
		k, r1, ok := parseStringLiteral(rest)
		if !ok {
			break
		}
		r1 = strings.TrimLeft(r1, " \t\r\n")
		r1 = strings.TrimPrefix(r1, ",")
		val, r2, ok := parseStringLiteral(r1)
		if !ok {
			break
		}
		out[k] = val
		rest = r2
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05-07",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05",
}

// parseTimestampOption decodes `TIMESTAMP "2026-11-01 10:00:00+00"`.
func parseTimestampOption(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToUpper(v), "TIMESTAMP") {
		v = v[len("TIMESTAMP"):]
	}
	s, _, ok := parseStringLiteral(v)
	if !ok {
		return time.Time{}, false
	}
	for _, l := range timestampLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
