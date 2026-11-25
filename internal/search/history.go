package search

import (
	"strconv"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

var HistoryKeys = map[string]bool{"status": true, "project": true, "p": true, "source": true, "since": true}

var (
	HistoryStates  = []string{string(api.RunDone), string(api.RunFailed), string(api.RunCancelled)}
	HistorySources = []string{"tui", "cli", "mcp"}
)

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

func ExpandPrefix(names []string, v string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, v) {
			out = append(out, n)
		}
	}
	return out
}

const maxAge = 200 * 365 * 24 * time.Hour

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
