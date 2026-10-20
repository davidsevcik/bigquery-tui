package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares a command's JSON output with testdata/<name>.json.
func golden(t *testing.T, name, got string) {
	t.Helper()
	// job IDs are random (bqt_<hex>): pin them
	got = regexp.MustCompile(`bqt_[0-9a-f]{32}`).ReplaceAllString(got, "bqt_JOBID")
	var v any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got)
	}
	pretty, _ := json.MarshalIndent(v, "", "  ")
	path := filepath.Join("testdata", name+".json")
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(pretty)) {
		t.Fatalf("%s differs from the golden file (run with -update if intended):\n%s", name, pretty)
	}
}

func syncedSandbox(t *testing.T, extra string) *sandbox {
	t.Helper()
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n" + extra)
	if code, _, errOut := s.run("sync"); code != 0 {
		t.Fatalf("sync: %d %s", code, errOut)
	}
	return s
}

func TestQueryJSONContractAndPaging(t *testing.T) {
	s := syncedSandbox(t, "")
	s.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(60) }
	// flags after the SQL, a parameter with a forced type
	code, out, errOut := s.run("query", "select n, label from t where d > @d", "--limit", "3", "--param", "d=DATE:2024-01-31")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	golden(t, "query", out)
	env := decode(t, out)
	if !env.Truncated || env.NextCursor != "3" {
		t.Fatalf("the envelope carries the cursor: %+v", env)
	}
	calls := s.fake.QueryCalls()
	last := calls[len(calls)-1]
	if last.Req.Labels["bqt_source"] != "cli" || len(last.Req.Params) != 1 || last.Req.Params[0].Type != "DATE" || last.Req.MaxBytesBilled != 1<<40 {
		t.Fatalf("%+v", last.Req)
	}
	rid := env.Data.(map[string]any)["result_id"].(string)
	code, out, errOut = s.run("results", rid, "--cursor", "58")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	page := decode(t, out)
	if rows := page.Data.(map[string]any)["rows"].([]any); len(rows) != 2 || page.Truncated {
		t.Fatalf("%+v", page)
	}
	// text on a terminal
	s.tty = true
	code, out, _ = s.run("query", "--limit", "2", "select n, label from t")
	if code != 0 || !strings.Contains(out, "label") || !strings.Contains(out, "row 1") || !strings.Contains(out, "bqt results ") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestQueryFromStdinAndFile(t *testing.T) {
	s := syncedSandbox(t, "")
	s.stdin = "-- a comment first\nselect 1"
	if code, _, errOut := s.run("query"); code != 0 {
		t.Fatalf("stdin: %d %s", code, errOut)
	}
	f := filepath.Join(s.dir, "q.sql")
	os.WriteFile(f, []byte("select 2"), 0o644)
	if code, _, errOut := s.run("query", "--file", f, "--dry-run"); code != 0 {
		t.Fatalf("file: %d %s", code, errOut)
	}
	if code, _, _ := s.run("query", "--file", f, "select 3"); code != api.ExitUsage {
		t.Fatalf("both: %d", code)
	}
	if code, _, errOut := s.run("query", "-- leading comment\nselect 4"); code != 0 {
		t.Fatalf("SQL that starts with -- is not a flag: %d %s", code, errOut)
	}
}

func TestQueryRefusalsAndExitCodes(t *testing.T) {
	s := syncedSandbox(t, "")
	s.fake.OnQuery = func(sql string) bqtest.QueryScript {
		switch {
		case strings.HasPrefix(sql, "insert"):
			return bqtest.QueryScript{StatementType: "INSERT", AffectedRows: 3}
		case strings.HasPrefix(sql, "bad"):
			return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected identifier at [1:1]")}
		}
		sc := bqtest.Numbers(1)
		sc.Bytes = 3 << 30
		return sc
	}
	code, _, errOut := s.run("query", "insert into t values (1)")
	e := errBody(t, errOut)
	if code != api.ExitOverLimit || e.Code != api.RefuseNotReadOnly || !strings.Contains(e.Message, "cli_allow_writes") {
		t.Fatalf("%d %+v", code, e)
	}
	code, _, errOut = s.run("query", "select big", "--max-bytes", "1TiB")
	if e := errBody(t, errOut); code != api.ExitOverLimit || e.Code != api.RefuseOverLimit || !strings.Contains(e.Message, "1GiB") {
		t.Fatalf("a flag cannot raise the limit: %d %+v", code, e)
	}
	code, out, _ := s.run("query", "--dry-run", "select big")
	if code != 0 || decode(t, out).Data.(map[string]any)["allowed"] != false {
		t.Fatalf("a dry-run reports the verdict and succeeds: %d %s", code, out)
	}
	code, _, errOut = s.run("query", "bad sql")
	if e := errBody(t, errOut); code != api.ExitBigQuery || e.Code != "query" || !strings.Contains(e.Message, "[1:1]") {
		t.Fatalf("%d %+v", code, e)
	}
	code, _, errOut = s.run("query", "select @x")
	if e := errBody(t, errOut); code != api.ExitUsage || !strings.Contains(e.Message, "@x") {
		t.Fatalf("%d %+v", code, e)
	}
	// the config opens writes for the CLI, and only there
	s.writeConfig("projects = [\"proj-a\"]\ncli_allow_writes = true\n")
	code, out, errOut = s.run("query", "insert into t values (1)")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if st := decode(t, out).Data.(map[string]any)["stats"].(map[string]any); st["affected_rows"] != float64(3) {
		t.Fatalf("%v", st)
	}
	for _, c := range s.fake.QueryCalls() {
		if !c.Dry && !strings.HasPrefix(c.Req.SQL, "insert") {
			t.Fatalf("a refused query started a job: %+v", c.Req.SQL)
		}
	}
}

func TestSchemaHistoryAndFavorites(t *testing.T) {
	s := syncedSandbox(t, "")
	code, out, errOut := s.run("schema", "proj-a.sales.orders")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	golden(t, "schema", out)
	if code, _, errOut := s.run("schema", "proj-a.sales.nope"); code != api.ExitFailure || errBody(t, errOut).Code != "not_found" {
		t.Fatalf("%d %s", code, errOut)
	}

	dir := filepath.Join(s.dir, "data", "favorites")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "dau.sql"), []byte("-- description: Daily users\n-- param: n INT64 = 7\nSELECT @n AS n\n"), 0o644)
	code, out, _ = s.run("favorites", "list")
	if code != 0 {
		t.Fatal(code)
	}
	golden(t, "favorites_list", out)
	code, out, _ = s.run("favorites", "show", "dau")
	if code != 0 || !strings.Contains(out, "SELECT @n") {
		t.Fatalf("%d %s", code, out)
	}
	s.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
	code, _, errOut = s.run("favorites", "run", "dau")
	if code != 0 {
		t.Fatalf("defaults fill the parameters: %d %s", code, errOut)
	}
	calls := s.fake.QueryCalls()
	if p := calls[len(calls)-1].Req.Params; len(p) != 1 || p[0].Type != "INT64" || p[0].Value != "7" {
		t.Fatalf("%+v", p)
	}
	if code, _, errOut := s.run("favorites", "run", "nope"); code != api.ExitFailure || errBody(t, errOut).Code != "not_found" {
		t.Fatalf("%d %s", code, errOut)
	}

	code, out, _ = s.run("history", "-status:failed", "--limit", "5")
	if code != 0 {
		t.Fatal(code)
	}
	items := decode(t, out).Data.(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["favorite"] != "dau.sql" || items[0].(map[string]any)["source"] != "cli" {
		t.Fatalf("%v", items)
	}
	for _, args := range [][]string{{"history", "--cursor", "99999999999999"}, {"favorites", "list", "--cursor", "99999999999999"}} {
		if code, _, errOut := s.run(args...); code != api.ExitUsage || errBody(t, errOut).Code != "usage" {
			t.Fatalf("%v: a cursor past int range is a usage error: %d %s", args, code, errOut)
		}
	}
}

func TestMCPPrintConfig(t *testing.T) {
	s := newSandbox(t)
	code, out, _ := s.run("mcp", "--print-config")
	if code != 0 || !strings.Contains(out, "claude mcp add") || !strings.Contains(out, "[mcp_servers.bqt]") || !strings.Contains(out, `"--config"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestParseAnywhere(t *testing.T) {
	fs := commandFlags("x", &globals{})
	n := fs.Int("limit", 1, "")
	b := fs.Bool("dry-run", false, "")
	pos, err := parseAnywhere(fs, []string{"a", "--limit", "5", "-status:failed", "--dry-run", "b c", "-5", "--", "--limit"})
	if err != nil || *n != 5 || !*b || strings.Join(pos, "|") != "a|-status:failed|b c|-5|--limit" {
		t.Fatalf("%v %d %v %q", err, *n, *b, pos)
	}
}

func TestFormatAfterTheCommand(t *testing.T) {
	s := newSandbox(t)
	if code, out, _ := s.run("version", "--format", "text"); code != 0 || !strings.HasPrefix(out, "bqt ") {
		t.Fatalf("%d %q", code, out)
	}
	s.tty = true
	if code, out, _ := s.run("version", "--format=json"); code != 0 || !strings.HasPrefix(out, "{") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestFormatFlagReadsLikeTheParser(t *testing.T) {
	cases := map[string][]string{
		"":     {"--format=text\nSELECT 1"},
		"json": {"--format", "text", "--format=json"},
		"text": {"select 1", "--format", "text"},
	}
	for want, args := range cases {
		if got := formatFlag(args); got != want {
			t.Errorf("%q: got %q, want %q", args, got, want)
		}
	}
	s := syncedSandbox(t, "")
	if code, _, errOut := s.run("query", "--format=text\nSELECT 1"); code != 0 {
		t.Fatalf("SQL starting with a comment that looks like a flag: %d %s", code, errOut)
	}
	if code, _, errOut := s.run("query", "select @a", "--param", "a=1", "--param", "A=2"); code != api.ExitUsage || !strings.Contains(errOut, "twice") {
		t.Fatalf("%d %s", code, errOut)
	}
}
