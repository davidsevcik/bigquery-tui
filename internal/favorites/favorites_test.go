package favorites

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

const dau = `-- description: Daily active users by country
-- tags: events, dau
-- param: start DATE = 2024-01-01
-- param: country STRING
-- this comment is part of the body
SELECT d, COUNT(*) FROM t WHERE d >= @start AND c = @country AND x = @undeclared
`

func TestParseHeader(t *testing.T) {
	f := Parse(dau)
	if f.Description != "Daily active users by country" || !reflect.DeepEqual(f.Tags, []string{"events", "dau"}) {
		t.Fatalf("%+v", f)
	}
	if len(f.Params) != 2 || f.Params[0].Name != "start" || f.Params[0].Type != "DATE" || *f.Params[0].Default != "2024-01-01" ||
		f.Params[1].Default != nil {
		t.Fatalf("%+v", f.Params)
	}
	if !strings.HasPrefix(f.SQL, "-- this comment is part of the body\nSELECT") {
		t.Fatalf("body %q", f.SQL)
	}
	if len(f.Problems) != 0 {
		t.Fatal(f.Problems)
	}
	// round trip
	if got := Render(f); got != dau {
		t.Fatalf("render:\n%s", got)
	}
}

func TestParseProblemsAndEdgeCases(t *testing.T) {
	f := Parse("-- param: n INTEGER\n-- param: 1x INT64\n-- param: d DATE = yesterday\n-- param: s STRING = 'a b'\n-- param: @s STRING\n--   TAGS:  a, ,b,a\n-- description: one\n-- Description: two\nSELECT 1")
	if len(f.Problems) != 4 {
		t.Fatalf("%q", f.Problems)
	}
	for _, want := range []string{"line 1: @n: unknown type INTEGER", "line 2:", "line 3: @d: default:", "line 5: parameter @s is declared twice"} {
		found := false
		for _, p := range f.Problems {
			found = found || strings.HasPrefix(p, want)
		}
		if !found {
			t.Errorf("no problem %q in %q", want, f.Problems)
		}
	}
	if len(f.Params) != 1 || *f.Params[0].Default != "a b" || !reflect.DeepEqual(f.Tags, []string{"a", "b"}) || f.Description != "one two" {
		t.Fatalf("%+v", f)
	}
	if f.SQL != "SELECT 1" {
		t.Fatalf("%q", f.SQL)
	}
	// no header at all, and a header with no body
	if f := Parse("SELECT 1\n-- tags: x\n"); len(f.Tags) != 0 || f.SQL != "SELECT 1\n-- tags: x\n" {
		t.Fatalf("a header must lead: %+v", f)
	}
	if f := Parse("-- tags: x"); f.SQL != "" || len(f.Tags) != 1 {
		t.Fatalf("%+v", f)
	}
	// a default that needs quotes keeps its spaces through a round trip
	sp := " padded "
	g := api.Favorite{Params: []api.ParamSpec{{Name: "s", Type: "STRING", Default: &sp}, {Name: "x"}}, SQL: "SELECT @s"}
	if back := Parse(Render(g)); *back.Params[0].Default != sp || len(back.Params) != 1 {
		t.Fatalf("%q -> %+v", Render(g), back.Params)
	}
}

func TestSchemaAndBind(t *testing.T) {
	s := Schema(Parse(dau))
	want := []string{"start:DATE", "country:STRING", "undeclared:"}
	var got []string
	for _, p := range s {
		got = append(got, p.Name+":"+p.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	f := Parse(dau)
	ps, err := Bind(f, Prompted(map[string]string{"start": "", "country": "", "undeclared": "42"}))
	if err != nil || len(ps) != 3 {
		t.Fatal(ps, err)
	}
	if ps[0] != (api.QueryParam{Name: "start", Type: "DATE", Value: "2024-01-01"}) {
		t.Fatalf("an empty answer takes the default: %+v", ps[0])
	}
	if ps[1].Value != "" || ps[1].Null || ps[1].Type != "STRING" {
		t.Fatalf("an empty STRING is allowed: %+v", ps[1])
	}
	if ps[2].Type != "INT64" {
		t.Fatalf("an undeclared type is guessed: %+v", ps[2])
	}
	_, err = Bind(f, Prompted(map[string]string{"start": "2024-02-30", "country": "", "undeclared": ""}))
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Name != "start" {
		t.Fatalf("an invalid date is refused, naming the parameter: %v", err)
	}
	if q, _ := bind1("STRING", "NULL"); !q.Null {
		t.Fatal("null")
	}
	for typ, cases := range map[string]map[string]bool{
		"INT64":     {"42": true, "-1": true, "4.2": false, "": false},
		"FLOAT64":   {"4.2": true, "1e3": true, "x": false},
		"BOOL":      {"TRUE": true, "false": true, "yes": false},
		"DATETIME":  {"2024-01-01 10:00:00": true, "2024-01-01T10:00": true, "2024-01-01Z": false},
		"TIMESTAMP": {"2024-01-01T10:00:00Z": true, "2024-01-01 10:00:00+02:00": true, "2024-01-01 10:00:00 UTC": true, "soon": false},
	} {
		for v, ok := range cases {
			_, err := bind1(typ, v)
			if (err == nil) != ok {
				t.Errorf("%s %q: %v", typ, v, err)
			}
		}
	}
	if q, _ := bind1("BOOL", " TRUE "); q.Value != "true" {
		t.Fatalf("%+v", q)
	}
}

// bind1 binds one value, given by name, to "SELECT @p" with p declared typ.
func bind1(typ, v string) (api.QueryParam, error) {
	ps, err := Bind(api.Favorite{SQL: "SELECT @p", Params: []api.ParamSpec{{Name: "p", Type: typ}}}, Given(map[string]string{"p": v}))
	if err != nil {
		return api.QueryParam{}, err
	}
	return ps[0], nil
}

// The agent surface (spec 14): a default fills only a missing value, a
// value given empty stays empty, names ignore case, and a missing or unused
// parameter is an error.
func TestBindGivenValues(t *testing.T) {
	f := Parse("-- param: start DATE = 2024-01-01\n-- param: country STRING\nSELECT 1 WHERE @start > '2000-01-01' AND @country = 'x' AND @n > 0\n")
	if _, err := Bind(f, Given(map[string]string{"country": "CZ"})); err == nil || !strings.Contains(err.Error(), "@n") {
		t.Fatalf("a parameter without a value or default: %v", err)
	}
	ps, err := Bind(f, Given(map[string]string{"COUNTRY": "CZ", "n": "5"}))
	if err != nil || len(ps) != 3 || ps[0].Type != "DATE" || ps[0].Value != "2024-01-01" || ps[1].Value != "CZ" || ps[2].Type != "INT64" {
		t.Fatalf("%v %+v", err, ps)
	}
	if _, err := Bind(f, Given(map[string]string{"country": "CZ", "n": "1", "typo": "x"})); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("an unused parameter is an error: %v", err)
	}
	if _, err := Bind(f, Given(map[string]string{"country": "CZ", "n": "1", "start": "soon"})); err == nil {
		t.Fatal("a bad DATE is an error")
	}
	if ps, err := Bind(Parse("select @x"), Given(map[string]string{"x": "DATE:2024-01-01"})); err != nil || ps[0].Type != "DATE" {
		t.Fatalf("a prefix forces the type: %v %+v", err, ps)
	}
	if _, err := Bind(Parse("select @Country"), Given(map[string]string{"country": "CZ", "COUNTRY": "US"})); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("names that differ only in case: %v", err)
	}
	def := Parse("-- param: country STRING = CZ\nselect @country")
	if ps, err := Bind(def, Given(map[string]string{"country": ""})); err != nil || ps[0].Value != "" {
		t.Fatalf("a value given empty stays empty: %v %+v", err, ps)
	}
	if ps, err := Bind(def, Given(map[string]string{})); err != nil || ps[0].Value != "CZ" {
		t.Fatalf("a missing value takes the default: %v %+v", err, ps)
	}
	if _, err := Bind(Parse("-- param: n INT64 = 7\nselect @n"), Given(map[string]string{"n": " "})); err == nil {
		t.Fatal("an INT64 given blank is an error, not the default")
	}
}

// The same Favorite binds the same way from the TUI's prompt and from the
// CLI or MCP: the values given bind identically, and only an empty value
// differs, as the spec has it (spec 11: an empty answer takes the default;
// spec 14: a value given empty stays empty).
func TestPromptedAndGivenBindTheSameFavorite(t *testing.T) {
	f := Parse("-- param: d DATE = 2024-01-01\n-- param: s STRING = CZ\n-- param: n INT64\nselect @d, @s, @n, @x")
	vals := map[string]string{"d": "DATE:2025-05-05", "s": "null", "n": " 7 ", "x": "true"}
	tui, err1 := Bind(f, Prompted(vals))
	cli, err2 := Bind(f, Given(vals))
	if err1 != nil || err2 != nil || !reflect.DeepEqual(tui, cli) || len(tui) != 4 {
		t.Fatalf("the same values bind the same way:\n%+v %v\n%+v %v", tui, err1, cli, err2)
	}
	if tui[0].Value != "2025-05-05" || !tui[1].Null || tui[2].Value != "7" || tui[3].Type != "BOOL" {
		t.Fatalf("%+v", tui)
	}
	empty := map[string]string{"d": "", "s": "", "n": "1", "x": "y"}
	if tui, err := Bind(f, Prompted(empty)); err != nil || tui[0].Value != "2024-01-01" || tui[1].Value != "CZ" {
		t.Fatalf("the prompt fills an empty answer with the default: %+v %v", tui, err)
	}
	if _, err := Bind(f, Given(empty)); err == nil || !strings.Contains(err.Error(), "@d needs a DATE value") {
		t.Fatalf("a DATE given empty is an error, not the default: %v", err)
	}
	empty["d"] = "2025-01-01"
	if cli, err := Bind(f, Given(empty)); err != nil || cli[1].Value != "" || cli[1].Null {
		t.Fatalf("a STRING given empty stays empty: %+v %v", cli, err)
	}
	// a Run's parameters, remembered, bind back to the same parameters
	ran := []api.QueryParam{{Name: "d", Type: "DATE", Value: "2025-05-05"}, {Name: "s", Type: "STRING", Null: true}, {Name: "n", Type: "INT64", Value: "7"}, {Name: "x", Type: "BOOL", Value: "true"}}
	again, err := Bind(f, Prompted(Remembered(ran)))
	if err != nil || !reflect.DeepEqual(again, ran) {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestCleanID(t *testing.T) {
	for in, want := range map[string]string{"dau": "dau.sql", "events/dau.SQL": "events/dau.SQL", " a\\b ": "a/b.sql", "a/./b": ""} {
		got, err := CleanID(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/etc/x", "../x", "a/../../x", ".git/x", "a//b", "a/.hidden"} {
		if _, err := CleanID(bad); !errors.Is(err, ErrBadID) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestDirOperations(t *testing.T) {
	root := t.TempDir()
	d := Dir(filepath.Join(root, "favorites")) // does not exist yet
	if fs, err := d.List(); err != nil || len(fs) != 0 {
		t.Fatalf("a missing dir is empty: %v %v", fs, err)
	}
	id, err := d.Write("events/dau", dau, false)
	if err != nil || id != "events/dau.sql" {
		t.Fatal(id, err)
	}
	if _, err := d.Write("events/dau.sql", "x", false); !errors.Is(err, ErrExists) {
		t.Fatalf("no silent overwrite: %v", err)
	}
	if _, err := d.Write("events/dau.sql", dau+"-- more\n", true); err != nil {
		t.Fatal(err)
	}
	d.Write("top", "SELECT 2\n", false)
	os.MkdirAll(filepath.Join(string(d), ".git"), 0o755)
	os.WriteFile(filepath.Join(string(d), ".git", "x.sql"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(string(d), "notes.txt"), []byte("x"), 0o644)
	fs, err := d.List()
	if err != nil || len(fs) != 2 || fs[0].ID != "events/dau.sql" || fs[0].Name != "dau" || fs[1].ID != "top.sql" {
		t.Fatalf("%+v %v", fs, err)
	}
	if !strings.HasSuffix(fs[0].Text, "-- more\n") || fs[0].ModTime.IsZero() {
		t.Fatal("overwrite")
	}
	if fs[0].Path != filepath.Join(string(d), "events", "dau.sql") {
		t.Fatalf("a Favorite knows its file: %q", fs[0].Path)
	}
	// rename refuses an existing target, moves across folders and prunes empty ones
	if _, err := d.Rename(fs[0], "top"); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	moved, err := d.Rename(fs[0], "metrics/daily/dau")
	if err != nil || moved.ID != "metrics/daily/dau.sql" || moved.Path != filepath.Join(string(d), "metrics", "daily", "dau.sql") || moved.Description == "" {
		t.Fatal(moved, err)
	}
	if _, err := os.Stat(filepath.Join(string(d), "events")); !os.IsNotExist(err) {
		t.Fatal("the empty folder is removed")
	}
	if _, err := d.Rename(fs[0], "again"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old name is gone: %v", err)
	}
	if err := d.Delete(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(string(d), "metrics")); !os.IsNotExist(err) {
		t.Fatal("empty folders go with the last file")
	}
	if err := d.Delete(moved); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := d.Read("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(string(d)); err != nil {
		t.Fatal("the root itself stays")
	}
	if left, _ := filepath.Glob(filepath.Join(string(d), ".bqt-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestSearch(t *testing.T) {
	mk := func(id, desc string, tags []string, sql string) api.Favorite {
		f := Parse(sql)
		f.ID, f.Name, f.Description, f.Tags = id, strings.TrimSuffix(filepath.Base(id), ".sql"), desc, tags
		return f
	}
	favs := []api.Favorite{
		mk("events/dau.sql", "Daily active users", []string{"events"}, "SELECT 1 FROM sessions"),
		mk("finance/revenue_daily.sql", "", []string{"money"}, "SELECT revenue"),
		mk("ops/dau_backfill.sql", "", nil, "SELECT 2"),
		mk("misc/x.sql", "about active users", nil, "SELECT dau FROM y"),
	}
	ids := func(ms []api.FavoriteMatch) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Favorite.ID)
		}
		return out
	}
	if got := ids(Search(favs, "dau")); !reflect.DeepEqual(got, []string{"events/dau.sql", "ops/dau_backfill.sql", "misc/x.sql"}) {
		t.Fatalf("exact, prefix, then body: %v", got)
	}
	if got := ids(Search(favs, "daily")); !reflect.DeepEqual(got, []string{"finance/revenue_daily.sql", "events/dau.sql"}) {
		t.Fatalf("word start beats description: %v", got)
	}
	if got := ids(Search(favs, "money")); len(got) != 1 {
		t.Fatalf("tags: %v", got)
	}
	if got := ids(Search(favs, "fnrev")); len(got) != 1 || got[0] != "finance/revenue_daily.sql" {
		t.Fatalf("fuzzy: %v", got)
	}
	if got := ids(Search(favs, "active users")); len(got) != 2 {
		t.Fatalf("terms AND: %v", got)
	}
	if got := ids(Search(favs, "")); len(got) != 4 || got[0] != "events/dau.sql" {
		t.Fatalf("empty: %v", got)
	}
	m := Search(favs, "dau")[0]
	if sp := m.NameSpans; len(sp) != 1 || m.Favorite.ID[sp[0].Start:sp[0].End] != "dau" {
		t.Fatalf("%+v", sp)
	}
}

// Codex review 4, finding 5: a default with a line break must not end the
// comment line; every awkward default round-trips.
func TestDefaultsRoundTrip(t *testing.T) {
	for _, d := range []string{"first\nsecond", "a\r\nb", `say "hi"`, `back\slash`, "'single'", " edge ", "", "plain value", "null"} {
		d := d
		f := api.Favorite{Params: []api.ParamSpec{{Name: "p", Type: "STRING", Default: &d}}, SQL: "SELECT @p\n"}
		text := Render(f)
		if strings.Count(text, "\n") != 2 {
			t.Errorf("%q: the header must stay one line: %q", d, text)
		}
		back := Parse(text)
		if len(back.Params) != 1 || *back.Params[0].Default != d || back.SQL != "SELECT @p\n" {
			t.Errorf("%q: %q -> %+v", d, text, back)
		}
	}
	// hand-written quoted defaults keep working
	if f := Parse("-- param: s STRING = \"a b\"\n-- param: t STRING = 'c d'\nSELECT @s, @t"); *f.Params[0].Default != "a b" || *f.Params[1].Default != "c d" {
		t.Fatalf("%+v", f.Params)
	}
}

// Codex review 4, finding 4: a remembered "TYPE:value" binds under the
// declared type.
func TestBindStripsTheDeclaredTypePrefix(t *testing.T) {
	q, err := bind1("INT64", "INT64:7")
	if err != nil || q.Value != "7" {
		t.Fatalf("%+v %v", q, err)
	}
	if q, _ := bind1("STRING", "string:abc"); q.Value != "abc" {
		t.Fatalf("%+v", q)
	}
	if q, _ := bind1("STRING", "STRING:null"); !q.Null {
		t.Fatal("null")
	}
	if q, _ := bind1("STRING", "DATE:x"); q.Value != "DATE:x" {
		t.Fatal("another type's prefix is part of a STRING")
	}
}

// Codex review 4, finding 3: rename and delete act only on the file that
// was shown, so a favorites_dir reload meanwhile cannot redirect them.
func TestRenameAndDeleteActOnTheShownFile(t *testing.T) {
	root := t.TempDir()
	a, b := Dir(filepath.Join(root, "a")), Dir(filepath.Join(root, "b"))
	a.Write("q", "select 1\n", false)
	b.Write("q", "select 2\n", false)
	shown, err := a.Read("q")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(shown); !errors.Is(err, ErrMoved) {
		t.Fatalf("delete after a reload: %v", err)
	}
	if _, err := b.Rename(shown, "r"); !errors.Is(err, ErrMoved) {
		t.Fatalf("rename after a reload: %v", err)
	}
	if err := b.Delete(api.Favorite{ID: "q.sql"}); !errors.Is(err, ErrMoved) {
		t.Fatalf("a Favorite that was never read: %v", err)
	}
	for _, d := range []Dir{a, b} {
		if _, err := d.Read("q"); err != nil {
			t.Fatalf("%s/q.sql must survive: %v", d, err)
		}
	}
	if err := a.Delete(shown); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRewritesTheHeader(t *testing.T) {
	d := Dir(t.TempDir())
	text := "-- description: old\n-- param: start DATE = 2024-01-01\nselect @start" // no final newline
	s, err := d.Save(SaveRequest{Text: text, Name: "sales/daily", Description: " Daily ", Tags: []string{"sales", "daily"}})
	if err != nil || s.Favorite.ID != "sales/daily.sql" || s.Favorite.Path != filepath.Join(string(d), "sales", "daily.sql") {
		t.Fatal(s, err)
	}
	head := "-- description: Daily\n-- tags: sales, daily\n-- param: start DATE = 2024-01-01\n"
	if data, _ := os.ReadFile(s.Favorite.Path); string(data) != head+"select @start\n" {
		t.Fatalf("%q", data)
	}
	if s.Header.Old != len(text)-len("select @start") || s.Header.New != head {
		t.Fatalf("%+v", s.Header)
	}
	if got := s.Header.New + text[s.Header.Old:]; got != head+"select @start" {
		t.Fatalf("the edit gives the text the file's header: %q", got)
	}
	// saving under its own name again replaces it; another existing name asks
	if _, err := d.Save(SaveRequest{Text: text, Name: "sales/daily.sql ", Own: "sales/daily.sql"}); err != nil {
		t.Fatal(err)
	}
	d.Write("taken", "select 0\n", false)
	if _, err := d.Save(SaveRequest{Text: text, Name: "taken", Own: "sales/daily.sql"}); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	if _, err := d.Save(SaveRequest{Text: text, Name: "taken", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	// a text without a header, and an empty body
	if s, _ := d.Save(SaveRequest{Text: "select 1\n", Name: "plain"}); s.Header != (HeaderEdit{Old: 0, New: ""}) {
		t.Fatalf("%+v", s.Header)
	}
	if s, _ := d.Save(SaveRequest{Text: "", Name: "empty", Description: "x"}); s.Header != (HeaderEdit{Old: 0, New: "-- description: x\n"}) {
		t.Fatalf("%+v", s.Header)
	}
}

func TestFromHistory(t *testing.T) {
	// a Run from a Favorite's buffer carries its header: it is replaced, not
	// stacked; the declared default stays; NULL is written as null
	sql := "-- description: old\n-- tags: a\n-- param: d DATE = 2024-01-01\nselect @d, @p, @n"
	got := FromHistory(sql, []api.QueryParam{{Name: "d", Type: "DATE", Value: "2025-05-05"}, {Name: "p", Type: "STRING", Null: true},
		{Name: "n", Type: "INT64", Value: "7"}, {Name: "unused", Type: "INT64", Value: "1"}, {Name: "bad", Type: "STRUCT"}}, "new", nil)
	want := "-- description: new\n-- tags: a\n-- param: d DATE = 2024-01-01\n-- param: p STRING = null\n-- param: n INT64 = 7\nselect @d, @p, @n\n"
	if got != want {
		t.Fatalf("%q", got)
	}
	if got := FromHistory("select @k", []api.QueryParam{{Name: "k", Type: "INT64", Value: "7"}}, "A kept query", nil); got != "-- description: A kept query\n-- param: k INT64 = 7\nselect @k\n" {
		t.Fatalf("%q", got)
	}
}

func TestIDOf(t *testing.T) {
	root := t.TempDir()
	d := Dir(filepath.Join(root, "favs"))
	d.Write("a/b", "select 1\n", false)
	for file, want := range map[string]string{
		filepath.Join(string(d), "a", "b.sql"): "a/b.sql",
		filepath.Join(root, "x.sql"):           "",
		filepath.Join(string(d), "notes.txt"):  "",
		"":                                     "",
	} {
		if got := d.IDOf(file); got != want {
			t.Errorf("%q: %q, want %q", file, got, want)
		}
	}
	if Dir("").IDOf(filepath.Join(root, "x.sql")) != "" {
		t.Error("no favorites_dir")
	}
}

// Codex review 4, finding 1: links never carry an operation out of the
// favorites directory.
func TestLinksCannotLeaveTheDirectory(t *testing.T) {
	root := t.TempDir()
	d := Dir(filepath.Join(root, "favs"))
	outside := filepath.Join(root, "outside")
	os.MkdirAll(outside, 0o755)
	os.MkdirAll(string(d), 0o755)
	os.WriteFile(filepath.Join(outside, "secret.sql"), []byte("select secret\n"), 0o644)
	if err := os.Symlink(outside, filepath.Join(string(d), "out")); err != nil {
		t.Skip(err)
	}
	os.Symlink(filepath.Join(outside, "secret.sql"), filepath.Join(string(d), "leaf.sql"))
	inside := filepath.Join(string(d), "real")
	os.MkdirAll(inside, 0o755)
	os.Symlink(inside, filepath.Join(string(d), "alias"))

	if _, err := d.Write("out/new", "x", false); !errors.Is(err, ErrBadID) {
		t.Errorf("write through a linked folder: %v", err)
	}
	if _, err := d.Read("out/secret"); !errors.Is(err, ErrBadID) {
		t.Errorf("read: %v", err)
	}
	if _, err := d.Read("leaf"); !errors.Is(err, ErrBadID) {
		t.Errorf("read a linked file: %v", err)
	}
	secret := api.Favorite{ID: "out/secret.sql", Path: filepath.Join(string(d), "out", "secret.sql")}
	if err := d.Delete(secret); !errors.Is(err, ErrBadID) {
		t.Errorf("delete: %v", err)
	}
	d.Write("mine", "select 1\n", false)
	mine, _ := d.Read("mine")
	if _, err := d.Rename(mine, "out/mine"); !errors.Is(err, ErrBadID) {
		t.Errorf("rename out: %v", err)
	}
	if _, err := d.Rename(secret, "stolen"); !errors.Is(err, ErrBadID) {
		t.Errorf("rename in: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.sql")); err != nil {
		t.Fatal("the outside file is untouched")
	}
	if left, _ := os.ReadDir(outside); len(left) != 1 {
		t.Fatalf("nothing was written outside: %v", left)
	}
	// a link that stays inside is fine
	if _, err := d.Write("alias/ok", "select 2\n", false); err != nil {
		t.Fatal(err)
	}
}
