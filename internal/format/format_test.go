package format

import (
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

func v(x any) map[string]any { return map[string]any{"v": x} }

func TestScalars(t *testing.T) {
	cases := []struct {
		typ  string
		in   any
		want string
	}{
		{"STRING", "hello", "hello"},
		{"INTEGER", "9007199254740993", "9007199254740993"},
		{"NUMERIC", "123456789.123456789", "123456789.123456789"},
		{"BOOLEAN", "true", "true"},
		{"TIMESTAMP", "1767225600123456", "2026-01-01 00:00:00.123456 UTC"},
		{"DATE", "2026-01-01", "2026-01-01"},
		{"BYTES", "aGVsbG8=", "aGVsbG8="},
	}
	for _, c := range cases {
		got := Cell(bq.Field{Name: "c", Type: c.typ}, c.in, Options{})
		if got.Text != c.want || got.Null || got.Truncated {
			t.Errorf("%s %v: %+v", c.typ, c.in, got)
		}
	}
	if got := Cell(bq.Field{Type: "STRING"}, nil, Options{}); !got.Null || got.Text != "" {
		t.Errorf("null: %+v", got)
	}
}

func TestNestedAsCompactJSON(t *testing.T) {
	schema := bq.Field{Name: "order", Type: "RECORD", Fields: []bq.Field{
		{Name: "id", Type: "INTEGER"},
		{Name: "note", Type: "STRING"},
		{Name: "tags", Type: "STRING", Mode: "REPEATED"},
		{Name: "buyer", Type: "RECORD", Fields: []bq.Field{{Name: "vip", Type: "BOOLEAN"}, {Name: "score", Type: "FLOAT"}}},
		{Name: "missing", Type: "STRING"},
	}}
	raw := map[string]any{"f": []any{
		v("9007199254740993"), v(`say "hi"`), v([]any{v("a"), v("b")}),
		v(map[string]any{"f": []any{v("true"), v("1.5")}}), v(nil),
	}}
	got := Cell(schema, raw, Options{})
	want := `{"id":9007199254740993,"note":"say \"hi\"","tags":["a","b"],"buyer":{"vip":true,"score":1.5},"missing":null}`
	if got.Text != want {
		t.Fatalf("\n got %s\nwant %s", got.Text, want)
	}
}

func TestRepeatedAndJSONColumns(t *testing.T) {
	rep := Cell(bq.Field{Name: "n", Type: "INTEGER", Mode: "REPEATED"}, []any{v("1"), v("2"), v("3")}, Options{})
	if rep.Text != "[1,2,3]" {
		t.Fatal(rep.Text)
	}
	recs := Cell(bq.Field{Name: "r", Type: "RECORD", Mode: "REPEATED", Fields: []bq.Field{{Name: "k", Type: "STRING"}}},
		[]any{v(map[string]any{"f": []any{v("x")}}), v(map[string]any{"f": []any{v("y")}})}, Options{})
	if recs.Text != `[{"k":"x"},{"k":"y"}]` {
		t.Fatal(recs.Text)
	}
	j := Cell(bq.Field{Name: "j", Type: "JSON"}, "{ \"a\" : [1, 2] }", Options{})
	if j.Text != `{"a":[1,2]}` {
		t.Fatal(j.Text)
	}
	bad := Cell(bq.Field{Name: "j", Type: "JSON"}, "not json", Options{})
	if bad.Text != `"not json"` {
		t.Fatal(bad.Text)
	}
	if empty := Cell(bq.Field{Type: "STRING", Mode: "REPEATED"}, []any{}, Options{}); empty.Text != "[]" {
		t.Fatal(empty.Text)
	}
}

func TestSanitizeAndTruncate(t *testing.T) {
	c := Cell(bq.Field{Type: "STRING"}, "line1\nline2\tx\x1b[31m", Options{})
	if strings.ContainsAny(c.Text, "\n\t\x1b") || !strings.Contains(c.Text, "line1⏎line2 x") {
		t.Fatalf("%q", c.Text)
	}
	c = Cell(bq.Field{Type: "STRING"}, "héllo wörld", Options{MaxChars: 5})
	if c.Text != "héllo" || !c.Truncated {
		t.Fatalf("%+v", c)
	}
	c = Cell(bq.Field{Type: "STRING"}, "short", Options{MaxChars: 5})
	if c.Text != "short" || c.Truncated {
		t.Fatalf("%+v", c)
	}
}

func TestRowPadsMissingCells(t *testing.T) {
	schema := []bq.Field{{Name: "a", Type: "STRING"}, {Name: "b", Type: "STRING"}}
	row := Row(schema, []any{"x"}, Options{})
	if len(row) != 2 || row[0].Text != "x" || !row[1].Null {
		t.Fatalf("%+v", row)
	}
	if cols := Columns(schema); len(cols) != 2 || cols[1].Name != "b" {
		t.Fatalf("%+v", cols)
	}
}

func TestHumanNumbers(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 5 << 30: "5.0 GiB"} {
		if got := Bytes(in); got != want {
			t.Errorf("%d: %s", in, got)
		}
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := Count(in); got != want {
			t.Errorf("%d: %s", in, got)
		}
	}
}
