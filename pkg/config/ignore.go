package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type Matcher struct {
	rules []rule
}

type rule struct {
	raw    string
	negate bool
	re     *regexp.Regexp
}

type PatternError struct {
	Index   int
	Pattern string
	Reason  string
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("ignore[%d] %q: %s", e.Index, e.Pattern, e.Reason)
}

func NewMatcher(patterns []string) (*Matcher, []*PatternError) {
	m := &Matcher{}
	var errs []*PatternError
	for i, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		neg := false
		if strings.HasPrefix(p, "!") {
			neg = true
			p = strings.TrimSpace(p[1:])
		}
		if p == "" {
			errs = append(errs, &PatternError{i, raw, "empty pattern after '!'"})
			continue
		}
		re, reason := compilePattern(p)
		if reason != "" {
			errs = append(errs, &PatternError{i, raw, reason})
			continue
		}
		m.rules = append(m.rules, rule{raw: raw, negate: neg, re: re})
	}
	return m, errs
}

func compilePattern(p string) (*regexp.Regexp, string) {
	if strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") || strings.Contains(p, "..") {
		return nil, "empty path segment (leading, trailing or doubled '.')"
	}
	var b strings.Builder
	b.WriteString("^")
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case unicode.IsControl(r):
			return nil, "control character"
		case r == '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				b.WriteString(".*")
				i++
				for i+1 < len(rs) && rs[i+1] == '*' {
					i++
				}
			} else {
				b.WriteString("[^.]*")
			}
		case r == '?':
			b.WriteString("[^.]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err.Error()
	}
	return re, ""
}

func (m *Matcher) Ignored(project, dataset, table string) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	cands := []string{project}
	if dataset != "" {
		cands = append(cands, project+"."+dataset)
		if table != "" {
			cands = append(cands, project+"."+dataset+"."+table)
		}
	}
	ignored := false
	for _, r := range m.rules {
		for _, c := range cands {
			if r.re.MatchString(c) {
				ignored = !r.negate
				break
			}
		}
	}
	return ignored
}

func Tightening(oldPatterns, newPatterns []string) bool {
	o := effective(oldPatterns)
	n := effective(newPatterns)
	if len(n) < len(o) {
		return false
	}
	for i := range o {
		if o[i] != n[i] {
			return false
		}
	}
	for _, p := range n[len(o):] {
		if strings.HasPrefix(p, "!") {
			return false
		}
	}
	return true
}

func effective(ps []string) []string {
	var out []string
	for _, p := range ps {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		out = append(out, p)
	}
	return out
}
