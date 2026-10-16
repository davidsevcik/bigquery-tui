package favorites

import (
	"path"
	"sort"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Search ranks Favorites for a query: every whitespace-separated term must
// match the name (path), a tag, the description or the body. Name hits rank
// above tag hits, tag hits above description hits, description above body.
// Within the name: exact, prefix, word start, substring, then a fuzzy
// subsequence. An empty query returns everything by ID.
func Search(favs []api.Favorite, query string) []api.FavoriteMatch {
	terms := strings.Fields(strings.ToLower(query))
	out := []api.FavoriteMatch{}
	for _, f := range favs {
		total := 0
		var spans []api.Span
		ok := true
		for _, t := range terms {
			s, sp := scoreTerm(f, t)
			if s == 0 {
				ok = false
				break
			}
			total += s
			spans = append(spans, sp...)
		}
		if ok {
			out = append(out, api.FavoriteMatch{Favorite: f, Score: total, NameSpans: mergeSpans(spans)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Favorite.ID < out[j].Favorite.ID
	})
	return out
}

func scoreTerm(f api.Favorite, t string) (int, []api.Span) {
	id := strings.ToLower(f.ID)
	base := strings.ToLower(f.Name)
	baseAt := len(id) - len(path.Base(id)) // where the base name starts in the ID
	switch {
	case base == t:
		return 100, []api.Span{{Start: baseAt, End: baseAt + len(t)}}
	case strings.HasPrefix(base, t):
		return 85, []api.Span{{Start: baseAt, End: baseAt + len(t)}}
	}
	if i := wordStart(id, t); i >= 0 {
		return 75, []api.Span{{Start: i, End: i + len(t)}}
	}
	if i := strings.Index(id, t); i >= 0 {
		return 65, []api.Span{{Start: i, End: i + len(t)}}
	}
	for _, tag := range f.Tags {
		tag = strings.ToLower(tag)
		if tag == t {
			return 60, nil
		}
	}
	for _, tag := range f.Tags {
		if strings.HasPrefix(strings.ToLower(tag), t) {
			return 50, nil
		}
	}
	if sp := subsequence(id, t); sp != nil {
		return 40, sp
	}
	if strings.Contains(strings.ToLower(f.Description), t) {
		return 30, nil
	}
	if strings.Contains(strings.ToLower(f.SQL), t) {
		return 10, nil
	}
	return 0, nil
}

// wordStart finds t at the start of a word of s (after / _ - . or a space).
func wordStart(s, t string) int {
	for i := 0; i+len(t) <= len(s); i++ {
		if (i == 0 || strings.ContainsRune("/_-. ", rune(s[i-1]))) && strings.HasPrefix(s[i:], t) {
			return i
		}
	}
	return -1
}

// subsequence matches t's characters in order in s.
func subsequence(s, t string) []api.Span {
	var out []api.Span
	j := 0
	for i := 0; i < len(s) && j < len(t); i++ {
		if s[i] == t[j] {
			out = append(out, api.Span{Start: i, End: i + 1})
			j++
		}
	}
	if j < len(t) {
		return nil
	}
	return mergeSpans(out)
}

func mergeSpans(sp []api.Span) []api.Span {
	if len(sp) < 2 {
		return sp
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].Start < sp[j].Start })
	out := []api.Span{sp[0]}
	for _, s := range sp[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			last.End = max(last.End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}
