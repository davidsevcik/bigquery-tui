package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func mustParse(t *testing.T, src string, opts LoadOptions) *Config {
	t.Helper()
	c, err := Parse([]byte(src), "test.toml", opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func parseErrs(t *testing.T, src string, opts LoadOptions) Errors {
	t.Helper()
	_, err := Parse([]byte(src), "test.toml", opts)
	var es Errors
	if !errors.As(err, &es) {
		t.Fatalf("want Errors, got %v", err)
	}
	return es
}

func TestDefaults(t *testing.T) {
	c := mustParse(t, `execution_project = "exec-proj"`, LoadOptions{Getenv: env(map[string]string{"XDG_DATA_HOME": "/xdg"})})
	if c.ExecutionProject != "exec-proj" {
		t.Fatal(c.ExecutionProject)
	}
	if c.DataDir != "/xdg/bqt" || c.FavoritesDir != "/xdg/bqt/favorites" {
		t.Fatalf("dirs: %s %s", c.DataDir, c.FavoritesDir)
	}
	if c.SyncTTL != 24*time.Hour || c.AgentTimeout != 60*time.Second || c.MaxConcurrentQueries != 4 {
		t.Fatalf("durations/ints: %+v", c)
	}
	if c.ConfirmBytes.Mode != ByteLimitAuto {
		t.Fatalf("confirm_bytes %v", c.ConfirmBytes)
	}
	if c.MaxBytesBilled.Mode != ByteLimitValue || c.MaxBytesBilled.Bytes != 1<<40 {
		t.Fatalf("max_bytes_billed %v", c.MaxBytesBilled)
	}
	if c.AgentMaxBytes != 1<<30 || c.CLIAllowWrites {
		t.Fatalf("agent: %d %v", c.AgentMaxBytes, c.CLIAllowWrites)
	}
}

func TestFullFile(t *testing.T) {
	src := `
execution_project = "exec-proj"
location = "EU"
projects = ["a-proj", "b-proj"]
ignore = ["sandbox", "!a-proj.keep"]
data_dir = "~/bqt-data"
favorites_dir = "$HOME/favs"
sync_ttl = "2d"
confirm_bytes = "5GiB"
max_bytes_billed = "off"
max_concurrent_queries = 8
history_retention_days = 90
agent_max_bytes = "500MB"
agent_timeout = "30s"
cli_allow_writes = true

[tui]
editing = "plain"
`
	home, _ := os.UserHomeDir()
	c := mustParse(t, src, LoadOptions{Getenv: env(map[string]string{"HOME": home})})
	if c.Location != "EU" || len(c.Projects) != 2 || len(c.Ignore) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.DataDir != filepath.Join(home, "bqt-data") || c.FavoritesDir != filepath.Join(home, "favs") {
		t.Fatalf("expansion: %s %s", c.DataDir, c.FavoritesDir)
	}
	if c.SyncTTL != 48*time.Hour {
		t.Fatal(c.SyncTTL)
	}
	if c.ConfirmBytes.Bytes != 5<<30 || c.MaxBytesBilled.Mode != ByteLimitOff {
		t.Fatalf("%v %v", c.ConfirmBytes, c.MaxBytesBilled)
	}
	if c.MaxConcurrentQueries != 8 || c.HistoryRetentionDays != 90 || c.AgentMaxBytes != 500_000_000 || c.AgentTimeout != 30*time.Second || !c.CLIAllowWrites {
		t.Fatalf("%+v", c)
	}
	if c.TUI["editing"] != "plain" {
		t.Fatalf("tui: %v", c.TUI)
	}
}

func TestUnknownKeyHasLine(t *testing.T) {
	es := parseErrs(t, "execution_project = \"p-roject\"\n\ningore = [\"x\"]\n", LoadOptions{})
	if len(es) != 1 || es[0].Key != "ingore" || es[0].Line != 3 {
		t.Fatalf("%v", es)
	}
	if !strings.Contains(es[0].Error(), "line 3") || !strings.Contains(es[0].Error(), "ingore") {
		t.Fatal(es[0].Error())
	}
}

func TestRequiredExecutionProject(t *testing.T) {
	es := parseErrs(t, `location = "EU"`, LoadOptions{})
	if len(es) != 1 || es[0].Key != "execution_project" || !strings.Contains(es[0].Msg, "required") {
		t.Fatalf("%v", es)
	}
}

func TestValidationErrorsCarryLines(t *testing.T) {
	src := "execution_project = \"ok-project\"\nsync_ttl = \"soon\"\nmax_concurrent_queries = 99\nconfirm_bytes = \"lots\"\n"
	es := parseErrs(t, src, LoadOptions{})
	got := map[string]int{}
	for _, e := range es {
		got[e.Key] = e.Line
	}
	for k, line := range map[string]int{"sync_ttl": 2, "max_concurrent_queries": 3, "confirm_bytes": 4} {
		if got[k] != line {
			t.Errorf("%s line = %d, want %d (%v)", k, got[k], line, es)
		}
	}
}

func TestInvalidPatterns(t *testing.T) {
	es := parseErrs(t, "execution_project = \"ok-project\"\nignore = [\"good\", \"bad..path\", \"!\"]\n", LoadOptions{})
	if len(es) != 2 {
		t.Fatalf("%v", es)
	}
	if es[0].Key != "ignore[1]" || es[1].Key != "ignore[2]" {
		t.Fatalf("%v", es)
	}
	if es[0].Line != 2 {
		t.Fatalf("line %d", es[0].Line)
	}
}

func TestPrecedenceFlagEnvFile(t *testing.T) {
	src := "execution_project = \"file-proj\"\nlocation = \"US\"\nsync_ttl = \"1h\"\n"
	e := env(map[string]string{"BQT_EXECUTION_PROJECT": "env-proj", "BQT_LOCATION": "EU", "BQT_SYNC_TTL": "2h"})
	c := mustParse(t, src, LoadOptions{Getenv: e, Flags: map[string]string{"execution_project": "flag-proj"}})
	if c.ExecutionProject != "flag-proj" {
		t.Errorf("flag should win: %s", c.ExecutionProject)
	}
	if c.Location != "EU" || c.SyncTTL != 2*time.Hour {
		t.Errorf("env should beat file: %s %s", c.Location, c.SyncTTL)
	}
	c = mustParse(t, src, LoadOptions{})
	if c.ExecutionProject != "file-proj" || c.Location != "US" {
		t.Errorf("file only: %s %s", c.ExecutionProject, c.Location)
	}
}

func TestEnvErrorNamesSource(t *testing.T) {
	_, err := Parse([]byte(`execution_project = "ok-project"`), "t.toml", LoadOptions{Getenv: env(map[string]string{"BQT_SYNC_TTL": "nope"})})
	if err == nil || !strings.Contains(err.Error(), "BQT_SYNC_TTL") {
		t.Fatalf("%v", err)
	}
}

func TestEnvProjectsList(t *testing.T) {
	c := mustParse(t, `execution_project = "ok-project"`, LoadOptions{Getenv: env(map[string]string{"BQT_PROJECTS": "a-proj, b-proj"})})
	if len(c.Projects) != 2 || c.Projects[1] != "b-proj" {
		t.Fatalf("%v", c.Projects)
	}
}

func TestDuplicateProject(t *testing.T) {
	es := parseErrs(t, "execution_project = \"ok-project\"\nprojects = [\"a-proj\", \"a-proj\"]\n", LoadOptions{})
	if len(es) != 1 || !strings.Contains(es[0].Msg, "duplicate") {
		t.Fatalf("%v", es)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(LoadOptions{Path: filepath.Join(t.TempDir(), "nope.toml")})
	var nc *ErrNoConfig
	if !errors.As(err, &nc) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "bqt init") {
		t.Fatal(err)
	}
}

func TestResolvePath(t *testing.T) {
	if p := ResolvePath(LoadOptions{Path: "/x/c.toml"}); p != "/x/c.toml" {
		t.Fatal(p)
	}
	if p := ResolvePath(LoadOptions{Getenv: env(map[string]string{"BQT_CONFIG": "/y/c.toml"})}); p != "/y/c.toml" {
		t.Fatal(p)
	}
	if p := ResolvePath(LoadOptions{Getenv: env(map[string]string{"XDG_CONFIG_HOME": "/xdg"})}); p != "/xdg/bqt/config.toml" {
		t.Fatal(p)
	}
	if p := ResolvePath(LoadOptions{Getenv: env(nil)}); !strings.HasSuffix(p, ".config/bqt/config.toml") {
		t.Fatal(p)
	}
}

func TestFilterHash(t *testing.T) {
	a := mustParse(t, "execution_project = \"ok-project\"\nprojects = [\"b-proj\", \"a-proj\"]\nignore = [\"x\", \"# c\"]\n", LoadOptions{})
	b := mustParse(t, "execution_project = \"ok-project\"\nprojects = [\"a-proj\", \"b-proj\"]\nignore = [\"x\"]\nsync_ttl = \"1h\"\n", LoadOptions{})
	if a.Filter().Hash() != b.Filter().Hash() {
		t.Fatal("project order, comments and unrelated keys must not change the hash")
	}
	c := mustParse(t, "execution_project = \"ok-project\"\nprojects = [\"a-proj\", \"b-proj\"]\nignore = [\"x\", \"y\"]\n", LoadOptions{})
	if a.Filter().Hash() == c.Filter().Hash() {
		t.Fatal("ignore change must change the hash")
	}
}

func TestTemplateParsesOnceProjectSet(t *testing.T) {
	if _, err := Parse([]byte(Template("")), "t", LoadOptions{}); err == nil {
		t.Fatal("template without execution_project must fail validation")
	}
	c, err := Parse([]byte(Template("my-proj")), "t", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecutionProject != "my-proj" {
		t.Fatal(c.ExecutionProject)
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{"10GiB": 10 << 30, "1TiB": 1 << 40, "500MB": 500_000_000, "1.5KiB": 1536, "42": 42, "2 gib": 2 << 30}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GiB", "10XB", "-5", "1e3"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0B", 1023: "1023B", 1 << 30: "1GiB", 10 << 30: "10GiB", 1536: "1.5KiB", 1 << 40: "1TiB"} {
		if got := FormatBytes(in); got != want {
			t.Errorf("%d: %s want %s", in, got, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "2d": 48 * time.Hour, "90s": 90 * time.Second, "0.5d": 12 * time.Hour} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseDuration("soon"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTUIEditingKey(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.toml")
		os.WriteFile(p, []byte("execution_project = \"x\"\n"+body), 0o600)
		return p
	}
	load := func(body string) (*Config, error) {
		return Load(LoadOptions{Path: write(body), Getenv: func(string) string { return "" }})
	}
	c, err := load("")
	if err != nil || c.Editing() != EditingVim {
		t.Fatalf("vim is the default: %v %v", c, err)
	}
	if c, err = load("[tui]\nediting = \"plain\"\n"); err != nil || c.Editing() != EditingPlain {
		t.Fatalf("%v", err)
	}
	if _, err = load("[tui]\nediting = \"emacs\"\n"); err == nil || !strings.Contains(err.Error(), "tui.editing") {
		t.Fatalf("an unknown style is an error: %v", err)
	}
	if _, err = load("[tui]\neditng = \"vim\"\n"); err == nil || !strings.Contains(err.Error(), "tui.editng") {
		t.Fatalf("an unknown [tui] key is an error: %v", err)
	}
}
