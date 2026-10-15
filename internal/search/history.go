package search

import (
	"strconv"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// HistoryKeys are the History search filters (spec 11): status:, project:
// (or p:), source: and since:.
var HistoryKeys = map[string]bool{"status": true, "project": true, "p": true, "source": true, "since": true}

// HistoryStates and HistorySources are the values of status: and source:
// (matched by prefix).
var (
	HistoryStates  = []string{string(api.RunDone), string(api.RunFailed), string(api.RunCancelled)}
	HistorySources = []string{"tui", "cli", "mcp"}
)

// ParseHistory parses a History search query. Free terms match the SQL text.
func ParseHistory(raw string) api.ParsedQuery {
	q := ParseWith(raw, HistoryKeys, validateHistoryFilter)
	for i := range q.Filters {
		if q.Filters[i].Key == "p" {
			q.Filters[i].Key = "project"
		}
	}
	return q
}

func validateHistoryFilter(f *api.QueryFilter) {
	for _, v := range f.Values {
		ok := true
		switch strings.ToLower(f.Key) {
		case "status":
			ok = anyPrefix(HistoryStates, v)
		case "source":
			ok = anyPrefix(HistorySources, v)
		case "since":
			_, ok = SinceTime(v, time.Now())
		}
		if !ok {
			f.Status = api.FilterInvalid
		}
	}
}

// HistoryQuery resolves a parsed History query for the store. Incomplete and
// invalid filters are left out.
func HistoryQuery(q api.ParsedQuery, now time.Time) store.HistoryQuery {
	hq := store.HistoryQuery{Terms: q.Terms}
	for _, f := range q.Filters {
		if f.Status != api.FilterOK {
			continue
		}
		c := store.HistoryCond{Field: f.Key, Negated: f.Negated}
		switch f.Key {
		case "status", "source":
			names := HistoryStates
			if f.Key == "status" {
				c.Field = "state"
			} else {
				names = HistorySources
			}
			for _, v := range f.Values {
				c.Values = append(c.Values, ExpandPrefix(names, v)...)
			}
		case "project":
			c.Values = f.Values
		case "since":
			// comma means OR: the earliest cutoff covers every value
			for _, v := range f.Values {
				if t, ok := SinceTime(v, now); ok && (c.Since.IsZero() || t.Before(c.Since)) {
					c.Since = t
				}
			}
			if c.Since.IsZero() {
				continue
			}
		default:
			continue
		}
		hq.Conds = append(hq.Conds, c)
	}
	return hq
}

// ExpandPrefix returns the names v is a prefix of.
func ExpandPrefix(names []string, v string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, v) {
			out = append(out, n)
		}
	}
	return out
}

// maxAge bounds an age so the duration cannot overflow (into the future).
const maxAge = 200 * 365 * 24 * time.Hour

// SinceTime reads a since: value: an age such as 30m, 12h, 7d or 2w (up to
// 200 years), or a date (2026-10-01) or date and time (2026-10-01T08:00) in
// local time.
func SinceTime(v string, now time.Time) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		unit := v[len(v)-1]
		if n, err := strconv.Atoi(v[:len(v)-1]); err == nil && n >= 0 {
			var d time.Duration
			switch unit {
			case 'm':
				d = time.Minute
			case 'h':
				d = time.Hour
			case 'd':
				d = 24 * time.Hour
			case 'w':
				d = 7 * 24 * time.Hour
			default:
				return time.Time{}, false
			}
			if int64(n) > int64(maxAge/d) {
				return time.Time{}, false
			}
			return now.Add(-time.Duration(n) * d), true
		}
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02t15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, v, now.Location()); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
