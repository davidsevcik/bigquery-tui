package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

type sandbox struct {
	t       *testing.T
	dir     string
	cfgPath string
	fake    *bqtest.Fake
	tty     bool
	edit    func(argv []string)
	gcloud  string
	stdin   string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	s := &sandbox{t: t, dir: dir, cfgPath: filepath.Join(dir, "cfg", "config.toml"), fake: bqtest.New()}
	a := s.fake.AddProject("proj-a")
	a.Datasets = []bqtest.Dataset{
		bqtest.DS("sales", "EU", bqtest.Table("orders", "id", "amount")),
		bqtest.DS("us_data", "US", bqtest.Table("u1", "x")),
	}
	return s
}

func (s *sandbox) writeConfig(extra string) {
	s.t.Helper()
	os.MkdirAll(filepath.Dir(s.cfgPath), 0o700)
	src := "execution_project = \"exec-proj\"\ndata_dir = \"" + filepath.Join(s.dir, "data") + "\"\n" + extra
	if err := os.WriteFile(s.cfgPath, []byte(src), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) run(args ...string) (code int, stdout, stderr string) {
	s.t.Helper()
	var so, se bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(s.stdin), Stdout: &so, Stderr: &se,
		Getenv: func(k string) string {
			if k == "HOME" {
				return s.dir
			}
			return os.Getenv(k)
		},
		IsTTY: func(any) bool { return s.tty },
		Now:   func() time.Time { return time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC) },
		Meta:  func(context.Context) (bq.Metadata, error) { return s.fake, nil },
		CheckAuth: func(context.Context) (bq.AuthInfo, error) {
			return bq.AuthInfo{Type: "authorized_user", QuotaProject: "exec-proj"}, nil
		},
		Gcloud:  func() string { return s.gcloud },
		Backoff: func(int) time.Duration { return 0 },
		RunEditor: func(ctx context.Context, argv []string, env Env) error {
			if s.edit != nil {
				s.edit(argv)
			}
			return nil
		},
	}
	full := append([]string{"--config", s.cfgPath}, args...)
	code = Run(context.Background(), full, env)
	return code, so.String(), se.String()
}

func decode(t *testing.T, s string) api.Envelope {
	t.Helper()
	var env api.Envelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, s)
	}
	if env.SchemaVersion != api.SchemaVersion {
		t.Fatalf("schema_version %d", env.SchemaVersion)
	}
	return env
}

func errBody(t *testing.T, s string) api.ErrorInfo {
	t.Helper()
	var b api.ErrorBody
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		t.Fatalf("not an error body: %v\n%s", err, s)
	}
	return b.Error
}

func TestNoConfigIsAHintedConfigError(t *testing.T) {
	s := newSandbox(t)
	code, out, errOut := s.run("status")
	if code != api.ExitConfig || out != "" {
		t.Fatalf("code %d out %q", code, out)
	}
	e := errBody(t, errOut)
	if e.Code != "no_config" || !strings.Contains(e.Hint, "bqt init") {
		t.Fatalf("%+v", e)
	}
}

func TestInvalidConfigNamesKeyAndLine(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("ingore = [\"x\"]\n")
	code, _, errOut := s.run("status")
	e := errBody(t, errOut)
	if code != api.ExitConfig || e.Code != "config" || !strings.Contains(e.Message, "ingore") || !strings.Contains(e.Message, "line 3") {
		t.Fatalf("%d %+v", code, e)
	}
}

func TestVersion(t *testing.T) {
	s := newSandbox(t)
	code, out, _ := s.run("version")
	if code != 0 {
		t.Fatal(code)
	}
	data := decode(t, out).Data.(map[string]any)
	if data["version"] == "" || data["os"] == "" {
		t.Fatalf("%v", data)
	}
	s.tty = true
	_, out, _ = s.run("version")
	if !strings.HasPrefix(out, "bqt ") {
		t.Fatalf("text on a terminal: %q", out)
	}
}

func TestUsageErrors(t *testing.T) {
	s := newSandbox(t)
	for _, args := range [][]string{{"frobnicate"}, {"sync", "--nope"}, {"--format", "xml", "version"}, {"config"}, {"config", "wat"}} {
		code, _, errOut := s.run(args...)
		if code != api.ExitUsage {
			t.Errorf("%v: code %d (%s)", args, code, errOut)
		}
	}
	var so, se bytes.Buffer
	env := DefaultEnv()
	env.Stdout, env.Stderr = &so, &se
	if code := Run(context.Background(), nil, env); code != api.ExitUsage || !strings.Contains(se.String(), "Usage:") {
		t.Fatalf("bare bqt prints usage on stderr: %d %q", code, se.String())
	}
	so.Reset()
	if code := Run(context.Background(), []string{"help"}, env); code != 0 || !strings.Contains(so.String(), "Commands:") {
		t.Fatalf("help: %d", code)
	}
}

func TestInitCreatesTemplateAndNeverOverwrites(t *testing.T) {
	s := newSandbox(t)
	code, out, _ := s.run("init", "--execution-project", "my-exec")
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	b, err := os.ReadFile(s.cfgPath)
	if err != nil || !strings.Contains(string(b), `execution_project = "my-exec"`) {
		t.Fatalf("%v %s", err, b)
	}
	if fi, _ := os.Stat(s.cfgPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config must be private, got %v", fi.Mode().Perm())
	}
	os.WriteFile(s.cfgPath, []byte("# precious"), 0o600)
	code, _, errOut := s.run("init")
	if code == 0 || errBody(t, errOut).Code != "exists" {
		t.Fatalf("%d %s", code, errOut)
	}
	if b, _ := os.ReadFile(s.cfgPath); string(b) != "# precious" {
		t.Fatal("init must never overwrite")
	}
}

func TestInitOffersGcloudProjectOnlyWithConfirmation(t *testing.T) {
	s := newSandbox(t)
	s.tty, s.gcloud, s.stdin = true, "gcloud-proj", "y\n"
	if code, _, _ := s.run("init"); code != 0 {
		t.Fatal(code)
	}
	if b, _ := os.ReadFile(s.cfgPath); !strings.Contains(string(b), `execution_project = "gcloud-proj"`) {
		t.Fatalf("confirmed suggestion is written: %s", b)
	}

	s2 := newSandbox(t)
	s2.tty, s2.gcloud, s2.stdin = true, "gcloud-proj", "n\n"
	s2.run("init")
	if b, _ := os.ReadFile(s2.cfgPath); strings.Contains(string(b), "gcloud-proj") {
		t.Fatal("declined suggestion must not be written")
	}

	s3 := newSandbox(t)
	s3.gcloud = "gcloud-proj" // not a TTY: never asks
	s3.run("init")
	if b, _ := os.ReadFile(s3.cfgPath); strings.Contains(string(b), "gcloud-proj") {
		t.Fatal("no prompt, no suggestion without a terminal")
	}
}

func TestConfigPathAndEdit(t *testing.T) {
	s := newSandbox(t)
	_, out, _ := s.run("config", "path")
	if decode(t, out).Data.(map[string]any)["path"] != s.cfgPath {
		t.Fatal(out)
	}

	var argv []string
	s.edit = func(a []string) {
		argv = a
		os.WriteFile(s.cfgPath, []byte("execution_project = \"edited-proj\"\nprojects = [\"proj-a\"]\ndata_dir = \""+filepath.Join(s.dir, "data")+"\"\n"), 0o600)
	}
	code, out, errOut := s.run("config", "edit", "--execution-project", "seed-proj")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if argv[len(argv)-1] != s.cfgPath {
		t.Fatalf("editor argv %v", argv)
	}
	data := decode(t, out).Data.(map[string]any)
	if data["valid"] != true || data["projects"] != float64(1) {
		t.Fatalf("%v", data)
	}

	// an edit that breaks the file reports a config error
	s.edit = func([]string) { os.WriteFile(s.cfgPath, []byte("nonsense = 1\n"), 0o600) }
	code, _, errOut = s.run("config", "edit")
	if code != api.ExitConfig {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestSyncThenStatusJSON(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")

	code, out, errOut := s.run("sync")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	res := decode(t, out).Data.(map[string]any)
	if res["units_total"] != float64(2) || res["units_ok"] != float64(2) {
		t.Fatalf("%v", res)
	}
	if errOut != "" {
		t.Fatalf("no progress or noise on stderr when not a terminal: %q", errOut)
	}

	code, out, _ = s.run("status")
	if code != 0 {
		t.Fatal(code)
	}
	st := decode(t, out).Data.(map[string]any)
	if st["tables"] != float64(2) || st["sync_needed"] != false {
		t.Fatalf("%v", st)
	}
	projects := st["projects"].([]any)
	if len(projects) != 1 || projects[0].(map[string]any)["project"] != "proj-a" {
		t.Fatalf("%v", projects)
	}
}

func TestSyncProgressFlagStreamsNDJSON(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	code, _, errOut := s.run("sync", "--progress")
	if code != 0 {
		t.Fatal(code)
	}
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	var kinds []string
	for _, l := range lines {
		var ev api.SyncEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("progress line is not JSON: %q", l)
		}
		kinds = append(kinds, string(ev.Kind))
	}
	if kinds[0] != "run_started" || kinds[len(kinds)-1] != "run_finished" {
		t.Fatalf("%v", kinds)
	}
}

func TestSyncTextOutputOnTerminal(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.tty = true
	code, out, errOut := s.run("sync")
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(errOut, "[2/2]") || !strings.Contains(out, "2 ok") {
		t.Fatalf("progress %q summary %q", errOut, out)
	}
}

func TestSyncFailedUnitExitsNonZeroButPrintsResult(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.fake.Projects["proj-a"].ListErr = bqtest.PermissionError("datasets.list")
	code, out, _ := s.run("sync")
	if code != api.ExitBigQuery {
		t.Fatalf("exit %d", code)
	}
	res := decode(t, out).Data.(map[string]any)
	if res["units_failed"] != float64(1) {
		t.Fatalf("%v", res)
	}
}

func TestSyncWithoutProjects(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("")
	code, _, errOut := s.run("sync")
	if code != api.ExitConfig || errBody(t, errOut).Code != "no_projects" {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestSyncAuthFailureIsExit4(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.fake.Projects["proj-a"].ListErr = &bq.Error{Kind: bq.KindAuth, Err: os_err("invalid_grant")}
	code, out, errOut := s.run("sync")
	// a discovery auth failure marks the unit failed; the run is still reported
	if code != api.ExitAuth || !strings.Contains(errOut, "gcloud auth application-default login") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if decode(t, out).Data.(map[string]any)["units_failed"] != float64(1) {
		t.Fatalf("%s", out)
	}
}

type os_err string

func (e os_err) Error() string { return string(e) }

func TestStatusBeforeSyncNeedsSync(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	_, out, _ := s.run("status")
	st := decode(t, out).Data.(map[string]any)
	if st["sync_needed"] != true {
		t.Fatalf("%v", st)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "data", "catalog.db")); err == nil {
		t.Fatal("status must not create the catalog")
	}
}

func TestDoctor(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.run("sync")
	code, out, _ := s.run("doctor")
	if code != 0 {
		t.Fatal(code)
	}
	rep := decode(t, out).Data.(map[string]any)
	if rep["config_ok"] != true || rep["execution_project"] != "exec-proj" {
		t.Fatalf("%v", rep)
	}
	cat := rep["catalog"].(map[string]any)
	if cat["exists"] != true || cat["tables"] != float64(2) {
		t.Fatalf("%v", cat)
	}
	auth := rep["auth"].(map[string]any)
	if auth["ok"] != true || auth["type"] != "authorized_user" {
		t.Fatalf("%v", auth)
	}
	if !strings.HasSuffix(rep["log_path"].(string), filepath.Join("logs", "bqt.log")) {
		t.Fatalf("%v", rep["log_path"])
	}

	// a broken config does not stop doctor from reporting
	os.WriteFile(s.cfgPath, []byte("nonsense = 1\n"), 0o600)
	code, out, _ = s.run("doctor")
	rep = decode(t, out).Data.(map[string]any)
	if code != 0 || rep["config_ok"] != false || rep["config_error"] == "" {
		t.Fatalf("%d %v", code, rep)
	}
}

func TestFlagOverridesConfig(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	_, out, _ := s.run("doctor", "--execution-project", "flag-proj")
	if decode(t, out).Data.(map[string]any)["execution_project"] != "flag-proj" {
		t.Fatalf("%s", out)
	}
}

func TestHelpMentionsEveryCommand(t *testing.T) {
	for _, c := range []string{"init", "config path", "config edit", "sync", "status", "doctor", "version"} {
		if !strings.Contains(usage, c) {
			t.Errorf("usage misses %q", c)
		}
	}
}

func TestSearchCommand(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	code, _, errOut := s.run("search", "orders")
	if code == 0 || errBody(t, errOut).Code != "no_catalog" {
		t.Fatalf("%d %s", code, errOut)
	}
	s.run("sync")
	code, out, _ := s.run("search", "ord", "p:proj")
	if code != 0 {
		t.Fatal(code)
	}
	env := decode(t, out)
	data := env.Data.(map[string]any)
	results := data["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["name"] != "orders" {
		t.Fatalf("%v", results)
	}
	if q := data["query"].(map[string]any); len(q["filters"].([]any)) != 1 {
		t.Fatalf("the parsed query is part of the contract: %v", q)
	}
	// paging
	code, out, _ = s.run("search", "--limit", "1", "--tables-only", "type:table")
	env = decode(t, out)
	if code != 0 || !env.Truncated || env.NextCursor != "1" {
		t.Fatalf("%d %+v", code, env)
	}
	_, out, _ = s.run("search", "--limit", "1", "--cursor", "1", "--tables-only", "type:table")
	if env = decode(t, out); env.Truncated || env.NextCursor != "" {
		t.Fatalf("%+v", env)
	}
	// a query may begin with a negated filter
	code, out, _ = s.run("search", "--tables-only", "-d:us_data", "type:table")
	if code != 0 || len(decode(t, out).Data.(map[string]any)["results"].([]any)) != 1 {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ := s.run("search", "--cursor", "x", "a"); code != api.ExitUsage {
		t.Fatal(code)
	}
	s.tty = true
	_, out, _ = s.run("search", "col:amount")
	if !strings.Contains(out, "proj-a.sales.orders") || !strings.Contains(out, "· amount") {
		t.Fatalf("%q", out)
	}
}
