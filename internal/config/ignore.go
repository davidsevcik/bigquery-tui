package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Matcher evaluates the ordered gitignore-style `ignore` patterns over the
// dotted path project.dataset.table.
//
// Semantics (ticket 05):
//   - `*` stays inside one segment, `**` spans segments, `?` is one character
//     inside a segment.
//   - A pattern covers everything beneath the path it matches, so `proj`
//     ignores the project and `proj.tmp_*` ignores matching datasets.
//   - A leading `!` negates. The last matching line wins.
//   - There is no bare-name-matches-anywhere rule.
//   - Elements starting with `#` are comments; blank elements are skipped.
type Matcher struct {
	rules []rule
}

type rule struct {
	raw    string
	negate bool
	re     *regexp.Regexp
}

// PatternError reports an invalid ignore pattern.
type PatternError struct {
	Index   int
	Pattern string
	Reason  string
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("ignore[%d] %q: %s", e.Index, e.Pattern, e.Reason)
}

// NewMatcher compiles patterns. All invalid patterns are reported.
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

// Ignored reports whether the object is ignored. Pass an empty dataset to ask
// about a project, an empty table to ask about a dataset.
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

// Tightening reports whether newPatterns only adds ignore rules to
// oldPatterns, with no negations among the additions. A tightening change can
// be applied by pruning the stored catalog; anything else may need a sync.
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
