package search

import (
	"sort"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Match tiers, best first.
const (
	tExact = iota
	tPrefix
	tWord
	tSub
	tFuzzy
	tDesc
)

var tierNames = []string{"exact", "prefix", "word", "substring", "fuzzy", "description"}

// A score is fieldBase + tier*10, lower is better. A hit on the table's own
// name always beats a hit that only comes from its labels, dataset or project
// (otherwise an exact dataset name would lift every table in it above real
// name matches), and any of those beats a description-only hit.
const (
	penName    = 0
	penLabel   = 100
	penDataset = 200
	penProject = 300
	descScore  = 1000
	noMatch    = 1 << 30
)

func tierName(score int) string {
	if score >= descScore {
		return tierNames[tDesc]
	}
	return tierNames[(score%100)/10]
}

func wordSpans(s string) []api.Span {
	var out []api.Span
	st := -1
	for i := 0; i <= len(s); i++ {
		sep := i == len(s) || s[i] == '_' || s[i] == '.' || s[i] == '-' || s[i] == ' ' || s[i] == '/' || s[i] == ':'
		if sep {
			if st >= 0 {
				out = append(out, api.Span{Start: st, End: i})
				st = -1
			}
		} else if st < 0 {
			st = i
		}
	}
	return out
}

func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// wordFrom matches term as a concatenation of prefixes of consecutive words
// starting at word j (so `oi` matches `order_items`).
func wordFrom(name, term string, ws []api.Span, j int, acc []api.Span) ([]api.Span, bool) {
	if term == "" {
		return acc, true
	}
	if j >= len(ws) {
		return nil, false
	}
	w := name[ws[j].Start:ws[j].End]
	for l := commonPrefix(w, term); l >= 1; l-- {
		next := append(acc[:len(acc):len(acc)], api.Span{Start: ws[j].Start, End: ws[j].Start + l})
		if r, ok := wordFrom(name, term[l:], ws, j+1, next); ok {
			return r, true
		}
	}
	return nil, false
}

// matchName returns the tier at which the lower-case term matches the
// lower-case name, or -1, with the matched spans.
func matchName(name, term string, fuzzy bool) (int, []api.Span) {
	if term == "" || len(name) < len(term) {
		return -1, nil
	}
	if name == term {
		return tExact, []api.Span{{Start: 0, End: len(name)}}
	}
	if strings.HasPrefix(name, term) {
		return tPrefix, []api.Span{{Start: 0, End: len(term)}}
	}
	// cheap reject before the more expensive tiers: every byte of the term
	// must occur in the name
	if !fuzzy && !strings.Contains(name, term[:1]) {
		return -1, nil
	}
	ws := wordSpans(name)
	if len(ws) > 1 {
		for j := range ws {
			if name[ws[j].Start] != term[0] {
				continue
			}
			if sp, ok := wordFrom(name, term, ws, j, nil); ok {
				return tWord, sp
			}
		}
	}
	if i := strings.Index(name, term); i >= 0 {
		return tSub, []api.Span{{Start: i, End: i + len(term)}}
	}
	if fuzzy && len(term) >= 2 {
		var sp []api.Span
		ti := 0
		for i := 0; i < len(name) && ti < len(term); i++ {
			if name[i] == term[ti] {
				if n := len(sp); n > 0 && sp[n-1].End == i {
					sp[n-1].End++
				} else {
					sp = append(sp, api.Span{Start: i, End: i + 1})
				}
				ti++
			}
		}
		if ti == len(term) {
			return tFuzzy, sp
		}
	}
	return -1, nil
}

func mergeSpans(sp []api.Span) []api.Span {
	if len(sp) < 2 {
		return sp
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].Start < sp[j].Start })
	out := []api.Span{sp[0]}
	for _, s := range sp[1:] {
		if l := &out[len(out)-1]; s.Start <= l.End {
			l.End = max(l.End, s.End)
		} else {
			out = append(out, s)
		}
	}
	return out
}

// subSpans returns the first occurrence of each value in l.
func subSpans(l string, vals []string) []api.Span {
	var sp []api.Span
	for _, v := range vals {
		if v == "" {
			continue
		}
		if i := strings.Index(l, v); i >= 0 {
			sp = append(sp, api.Span{Start: i, End: i + len(v)})
		}
	}
	return mergeSpans(sp)
}

func anySub(l string, vals []string) bool {
	for _, v := range vals {
		if strings.Contains(l, v) {
			return true
		}
	}
	return false
}
