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
	q, err := Bind(s[0], "")
	if err != nil || q.Value != "2024-01-01" || q.Type != "DATE" {
		t.Fatalf("the default fills an empty value: %+v %v", q, err)
	}
	if _, err := Bind(s[0], "2024-02-30"); err == nil {
		t.Fatal("an invalid date is refused")
	}
	if q, err := Bind(s[1], ""); err != nil || q.Value != "" || q.Null {
		t.Fatalf("an empty STRING is allowed: %+v %v", q, err)
	}
	if q, _ := Bind(s[1], "NULL"); !q.Null {
		t.Fatal("null")
	}
	if q, _ := Bind(s[2], "42"); q.Type != "INT64" {
		t.Fatalf("an undeclared type is guessed: %+v", q)
	}
	for typ, cases := range map[string]map[string]bool{
		"INT64":     {"42": true, "-1": true, "4.2": false, "": false},
		"FLOAT64":   {"4.2": true, "1e3": true, "x": false},
		"BOOL":      {"TRUE": true, "false": true, "yes": false},
		"DATETIME":  {"2024-01-01 10:00:00": true, "2024-01-01T10:00": true, "2024-01-01Z": false},
		"TIMESTAMP": {"2024-01-01T10:00:00Z": true, "2024-01-01 10:00:00+02:00": true, "2024-01-01 10:00:00 UTC": true, "soon": false},
	} {
		for v, ok := range cases {
			_, err := Bind(api.ParamSpec{Name: "p", Type: typ}, v)
			if (err == nil) != ok {
				t.Errorf("%s %q: %v", typ, v, err)
			}
		}
	}
	if q, _ := Bind(api.ParamSpec{Name: "b", Type: "BOOL"}, " TRUE "); q.Value != "true" {
		t.Fatalf("%+v", q)
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
	// rename refuses an existing target, moves across folders and prunes empty ones
	if _, err := d.Rename("events/dau", "top"); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	id, err = d.Rename("events/dau", "metrics/daily/dau")
	if err != nil || id != "metrics/daily/dau.sql" {
		t.Fatal(id, err)
	}
	if _, err := os.Stat(filepath.Join(string(d), "events")); !os.IsNotExist(err) {
		t.Fatal("the empty folder is removed")
	}
	if f, err := d.Read("metrics/daily/dau"); err != nil || f.Description == "" {
		t.Fatalf("%+v %v", f, err)
	}
	if err := d.Delete("metrics/daily/dau.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(string(d), "metrics")); !os.IsNotExist(err) {
		t.Fatal("empty folders go with the last file")
	}
	if err := d.Delete("metrics/daily/dau.sql"); !errors.Is(err, ErrNotFound) {
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
	q, err := Bind(api.ParamSpec{Name: "n", Type: "INT64"}, "INT64:7")
	if err != nil || q.Value != "7" {
		t.Fatalf("%+v %v", q, err)
	}
	if q, _ := Bind(api.ParamSpec{Name: "s", Type: "STRING"}, "string:abc"); q.Value != "abc" {
		t.Fatalf("%+v", q)
	}
	if q, _ := Bind(api.ParamSpec{Name: "s", Type: "STRING"}, "STRING:null"); !q.Null {
		t.Fatal("null")
	}
	if q, _ := Bind(api.ParamSpec{Name: "s", Type: "STRING"}, "DATE:x"); q.Value != "DATE:x" {
		t.Fatal("another type's prefix is part of a STRING")
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
	if err := d.Delete("out/secret"); !errors.Is(err, ErrBadID) {
		t.Errorf("delete: %v", err)
	}
	d.Write("mine", "select 1\n", false)
	if _, err := d.Rename("mine", "out/mine"); !errors.Is(err, ErrBadID) {
		t.Errorf("rename out: %v", err)
	}
	if _, err := d.Rename("out/secret", "stolen"); !errors.Is(err, ErrBadID) {
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
