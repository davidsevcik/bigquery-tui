package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

var schema = []bq.Field{
	{Name: "id", Type: "INTEGER"}, {Name: "note", Type: "STRING"}, {Name: "big", Type: "NUMERIC"},
	{Name: "addr", Type: "RECORD", Fields: []bq.Field{{Name: "city", Type: "STRING"}}},
	{Name: "tags", Type: "STRING", Mode: "REPEATED"},
}

func rows(n int) [][]any {
	var out [][]any
	for i := 0; i < n; i++ {
		out = append(out, []any{"9007199254740993", "a,\"b\"\nline", "1.10", map[string]any{"f": []any{map[string]any{"v": "Brno"}}},
			[]any{map[string]any{"v": "x"}, map[string]any{"v": "y"}}})
	}
	out[0][1] = nil
	return out
}

// paged serves the rows in pages of 2.
func paged(all [][]any) Next {
	return func(_ context.Context, start int64) ([][]any, error) {
		if start >= int64(len(all)) {
			return nil, nil
		}
		return all[start:min(start+2, int64(len(all)))], nil
	}
}

func TestCSV(t *testing.T) {
	var b bytes.Buffer
	var seen []int64
	n, err := Write(context.Background(), &b, CSV, schema, paged(rows(3)), func(r int64) { seen = append(seen, r) })
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	want := "id,note,big,addr,tags\n" +
		`9007199254740993,,1.10,"{""city"":""Brno""}","[""x"",""y""]"` + "\n" +
		`9007199254740993,"a,""b""` + "\nline" + `",1.10,"{""city"":""Brno""}","[""x"",""y""]"` + "\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Fatalf("got\n%s\nwant prefix\n%s", b.String(), want)
	}
	if len(seen) != 2 || seen[1] != 3 {
		t.Fatalf("progress per batch: %v", seen)
	}
}

func TestJSONAndNDJSONKeepExactNumbers(t *testing.T) {
	var b bytes.Buffer
	if _, err := Write(context.Background(), &b, JSON, schema, paged(rows(3)), nil); err != nil {
		t.Fatal(err)
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(b.Bytes(), &arr); err != nil || len(arr) != 3 {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if string(arr[0]["id"]) != "9007199254740993" || string(arr[0]["note"]) != "null" || string(arr[0]["big"]) != "1.10" || string(arr[1]["tags"]) != `["x","y"]` {
		t.Fatalf("%s", b.String())
	}
	b.Reset()
	Write(context.Background(), &b, NDJSON, schema, paged(rows(2)), nil)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !json.Valid([]byte(lines[1])) || !strings.Contains(lines[1], `"note":"a,\"b\"\nline"`) {
		t.Fatalf("%q", lines)
	}
	b.Reset()
	Write(context.Background(), &b, JSON, schema, paged(nil), nil)
	if strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("an empty result is an empty array: %q", b.String())
	}
}

func TestCancelAndErrorsStopTheExport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	all := rows(6)
	calls := 0
	next := func(c context.Context, start int64) ([][]any, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return paged(all)(c, start)
	}
	n, err := Write(ctx, &bytes.Buffer{}, NDJSON, schema, next, nil)
	if !errors.Is(err, context.Canceled) || n != 4 {
		t.Fatal(n, err)
	}
	boom := errors.New("boom")
	_, err = Write(context.Background(), &bytes.Buffer{}, CSV, schema, func(context.Context, int64) ([][]any, error) { return nil, boom }, nil)
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestFormatOfAndTSV(t *testing.T) {
	for p, want := range map[string]Format{"a.csv": CSV, "b.JSON": JSON, "c.ndjson": NDJSON, "d.jsonl": NDJSON, "e": CSV, "f.txt": CSV} {
		if FormatOf(p) != want {
			t.Errorf("%s: %s", p, FormatOf(p))
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("unknown format must fail")
	}
	got := TSV([]api.PreviewColumn{{Name: "a"}, {Name: "b"}}, [][]api.Cell{{{Text: "1\t2"}, {Null: true}}, {{Text: "x\ny"}, {Text: "z"}}})
	if got != "a\tb\n1 2\t\nx y\tz\n" {
		t.Fatalf("%q", got)
	}
}
