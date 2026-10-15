package search

import (
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func TestParseHistory(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	q := ParseHistory(`orders status:fail,canc -source:mcp p:acme since:7d since:2026-10-01 status:nope source: type:view`)
	if len(q.Terms) != 2 || q.Terms[0].Text != "orders" || q.Terms[1].Text != "type:view" {
		t.Fatalf("terms %+v (type: is not a History filter)", q.Terms)
	}
	if q.Path != nil {
		t.Fatal("no path lookup in History")
	}
	st := map[string]api.FilterStatus{}
	for _, f := range q.Filters {
		st[q.Raw[f.Span.Start:f.Span.End]] = f.Status
	}
	want := map[string]api.FilterStatus{"status:fail,canc": api.FilterOK, "-source:mcp": api.FilterOK, "p:acme": api.FilterOK,
		"since:7d": api.FilterOK, "since:2026-10-01": api.FilterOK, "status:nope": api.FilterInvalid, "source:": api.FilterPending}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("%s: %s, want %s", k, st[k], v)
		}
	}
	hq := HistoryQuery(q, now)
	if len(hq.Conds) != 5 {
		t.Fatalf("%+v", hq.Conds)
	}
	c := hq.Conds[0]
	if c.Field != "state" || len(c.Values) != 2 || c.Values[0] != "failed" || c.Values[1] != "cancelled" {
		t.Fatalf("%+v", c)
	}
	if c := hq.Conds[1]; c.Field != "source" || !c.Negated || c.Values[0] != "mcp" {
		t.Fatalf("%+v", c)
	}
	if c := hq.Conds[2]; c.Field != "project" || c.Values[0] != "acme" {
		t.Fatalf("p: is project: %+v", c)
	}
	if c := hq.Conds[3]; !c.Since.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("%v", c.Since)
	}
	if c := hq.Conds[4]; !c.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v", c.Since)
	}
}

func TestSinceTime(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{"30m": 30 * time.Minute, "12h": 12 * time.Hour, "2w": 14 * 24 * time.Hour, "0d": 0} {
		if got, ok := SinceTime(v, now); !ok || now.Sub(got) != want {
			t.Errorf("%s: %v %v", v, got, ok)
		}
	}
	if got, ok := SinceTime("2026-10-01t08:30", now); !ok || got.Hour() != 8 {
		t.Errorf("date and time: %v %v", got, ok)
	}
	for _, v := range []string{"", "7", "7y", "-1d", "yesterday", "2026-13-01"} {
		if _, ok := SinceTime(v, now); ok {
			t.Errorf("%q accepted", v)
		}
	}
}

// Codex review 4, findings 2 and 10.
func TestSinceBoundsAndOr(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, v := range []string{"200000d", "9999999w", "99999999999h"} {
		if _, ok := SinceTime(v, now); ok {
			t.Errorf("%s overflows and must be refused", v)
		}
	}
	if _, ok := SinceTime("73000d", now); !ok {
		t.Error("200 years is accepted")
	}
	for _, raw := range []string{"since:1d,7d", "since:7d,1d"} {
		hq := HistoryQuery(ParseHistory(raw), now)
		if len(hq.Conds) != 1 || !hq.Conds[0].Since.Equal(now.Add(-7*24*time.Hour)) {
			t.Errorf("%s: %+v", raw, hq.Conds)
		}
	}
}
