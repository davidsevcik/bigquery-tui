package bq

import (
	"reflect"
	"testing"
	"time"
)

func TestParseStringLiteral(t *testing.T) {
	cases := []struct {
		in, val, rest string
		ok            bool
	}{
		{`"plain"`, "plain", "", true},
		{`'single'`, "single", "", true},
		{`"say \"hi\""`, `say "hi"`, "", true},
		{`"a\nb\tc\\d"`, "a\nb\tc\\d", "", true},
		{`"""multi
line "quoted" text"""`, "multi\nline \"quoted\" text", "", true},
		{`"café \x41"`, "café A", "", true},
		{`r"raw\n"`, `raw\n`, "", true},
		{`"héllo wörld"`, "héllo wörld", "", true},
		{`"a", "b"`, "a", `, "b"`, true},
		{`  "lead space"`, "lead space", "", true},
		{`"unterminated`, "", `"unterminated`, false},
		{`not a literal`, "", "not a literal", false},
		{``, "", "", false},
	}
	for _, c := range cases {
		v, rest, ok := parseStringLiteral(c.in)
		if v != c.val || rest != c.rest || ok != c.ok {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q, %v)", c.in, v, rest, ok, c.val, c.rest, c.ok)
		}
	}
}

func TestParseLabels(t *testing.T) {
	got := parseLabelsOption(`[STRUCT("team", "data platform"), STRUCT("env", "prod"), STRUCT("odd \"k\"", "v,)")]`)
	want := map[string]string{"team": "data platform", "env": "prod", `odd "k"`: "v,)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if parseLabelsOption(`[]`) != nil || parseLabelsOption(``) != nil {
		t.Fatal("empty labels are nil")
	}
}

func TestParseStringOptionFallsBack(t *testing.T) {
	if got := parseStringOption(`"hello"`); got != "hello" {
		t.Fatal(got)
	}
	if got := parseStringOption(`  bare text `); got != "bare text" {
		t.Fatal(got)
	}
}

func TestParseTimestampOption(t *testing.T) {
	for _, in := range []string{
		`TIMESTAMP "2026-11-01 10:30:00+00"`,
		`TIMESTAMP "2026-11-01 10:30:00.000000+00:00"`,
		`TIMESTAMP "2026-11-01T10:30:00Z"`,
	} {
		ts, ok := parseTimestampOption(in)
		if !ok || !ts.Equal(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)) {
			t.Errorf("%q: %v %v", in, ts, ok)
		}
	}
	ts, ok := parseTimestampOption(`TIMESTAMP "2026-11-01 10:30:00-05"`)
	if !ok || !ts.Equal(time.Date(2026, 11, 1, 15, 30, 0, 0, time.UTC)) {
		t.Errorf("offset: %v %v", ts, ok)
	}
	if _, ok := parseTimestampOption(`garbage`); ok {
		t.Error("garbage must fail")
	}
}
