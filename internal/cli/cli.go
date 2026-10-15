// Package cli is bqt's non-interactive command-line frontend. Every command
// writes one JSON document (the core type, serialized) to stdout when stdout
// is not a terminal, and human text otherwise (spec section 14.1).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	catsync "github.com/davidsevcik/bigquery-tui/internal/sync"
	"github.com/davidsevcik/bigquery-tui/internal/version"
)

// Env holds everything a command touches outside its arguments, so tests can
// replace it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// IsTTY reports whether a stream is an interactive terminal.
	IsTTY func(any) bool
	Now   func() time.Time
	// Meta builds the BigQuery metadata client; nil uses the real one.
	Meta func(context.Context) (bq.Metadata, error)
	// CheckAuth inspects the default credentials; nil uses the real check.
	CheckAuth func(context.Context) (bq.AuthInfo, error)
	// Gcloud returns `gcloud config get-value project`, or "".
	Gcloud func() string
	// RunEditor runs an editor command attached to the terminal.
	RunEditor func(ctx context.Context, argv []string, env Env) error
	// Backoff overrides sync retry waits (tests).
	Backoff func(int) time.Duration
}

// DefaultEnv returns the process environment.
func DefaultEnv() Env {
	return Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv,
		IsTTY:  isTTY,
		Now:    time.Now,
		Gcloud: gcloudProject,
		RunEditor: func(ctx context.Context, argv []string, env Env) error {
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
	}
}

func isTTY(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func gcloudProject() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project", "--quiet").Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if p == "(unset)" {
		return ""
	}
	return p
}

type globals struct {
	configPath  string
	logLevel    string
	format      string
	execProject string
	location    string
	dataDir     string
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.configPath, "config", g.configPath, "path to the config file (default: $BQT_CONFIG or ~/.config/bqt/config.toml)")
	fs.StringVar(&g.logLevel, "log-level", g.logLevel, "log level: debug, info, warn, error (default: $BQT_LOG or info)")
	fs.StringVar(&g.format, "format", g.format, "output format: json or text (default: text on a terminal, json otherwise)")
	fs.StringVar(&g.execProject, "execution-project", g.execProject, "override execution_project")
	fs.StringVar(&g.location, "location", g.location, "override location")
	fs.StringVar(&g.dataDir, "data-dir", g.dataDir, "override data_dir")
}

func (g *globals) loadOptions(env Env) config.LoadOptions {
	flags := map[string]string{}
	if g.execProject != "" {
		flags["execution_project"] = g.execProject
	}
	if g.location != "" {
		flags["location"] = g.location
	}
	if g.dataDir != "" {
		flags["data_dir"] = g.dataDir
	}
	return config.LoadOptions{Path: g.configPath, Getenv: env.Getenv, Flags: flags}
}

// cmdError carries an exit code and a stable error code to the printer.
type cmdError struct {
	exit int
	code string
	msg  string
	hint string
}

func (e *cmdError) Error() string { return e.msg }

func usageErr(format string, a ...any) error {
	return &cmdError{exit: api.ExitUsage, code: "usage", msg: fmt.Sprintf(format, a...), hint: "run `bqt help`"}
}

// out renders results.
type out struct {
	env  Env
	json bool
}

func newOut(env Env, format string) (*out, error) {
	switch format {
	case "json":
		return &out{env: env, json: true}, nil
	case "text":
		return &out{env: env}, nil
	case "":
		return &out{env: env, json: !env.IsTTY(env.Stdout)}, nil
	}
	return nil, usageErr("unknown --format %q (use json or text)", format)
}

func (o *out) result(data any, text func(w io.Writer)) {
	if o.json {
		_ = json.NewEncoder(o.env.Stdout).Encode(api.Envelope{SchemaVersion: api.SchemaVersion, Data: data})
		return
	}
	text(o.env.Stdout)
}

func (o *out) envelope(e api.Envelope) error {
	return json.NewEncoder(o.env.Stdout).Encode(e)
}

// fail prints err in the active format and returns its exit code.
func (o *out) fail(err error) int {
	ce := classify(err)
	if o.json {
		_ = json.NewEncoder(o.env.Stderr).Encode(api.ErrorBody{Error: api.ErrorInfo{Code: ce.code, Message: ce.msg, Hint: ce.hint}})
	} else {
		fmt.Fprintf(o.env.Stderr, "bqt: %s\n", ce.msg)
		if ce.hint != "" {
			fmt.Fprintf(o.env.Stderr, "hint: %s\n", ce.hint)
		}
	}
	return ce.exit
}

func classify(err error) *cmdError {
	var ce *cmdError
	if errors.As(err, &ce) {
		return ce
	}
	var nc *config.ErrNoConfig
	if errors.As(err, &nc) {
		return &cmdError{exit: api.ExitConfig, code: "no_config", msg: nc.Error(), hint: "run `bqt init`"}
	}
	var ces config.Errors
	if errors.As(err, &ces) {
		return &cmdError{exit: api.ExitConfig, code: "config", msg: "invalid config: " + ces.Error(), hint: "fix the config file (`bqt config edit`)"}
	}
	switch {
	case errors.Is(err, catsync.ErrNoProjects):
		return &cmdError{exit: api.ExitConfig, code: "no_projects", msg: err.Error(), hint: "add projects with `bqt config edit`"}
	case errors.Is(err, engine.ErrSyncRunning):
		return &cmdError{exit: api.ExitFailure, code: "sync_running", msg: err.Error()}
	case errors.Is(err, context.Canceled):
		return &cmdError{exit: api.ExitCancelled, code: "cancelled", msg: "cancelled"}
	}
	switch bq.KindOf(err) {
	case bq.KindAuth:
		return &cmdError{exit: api.ExitAuth, code: "auth", msg: err.Error(), hint: "run `gcloud auth application-default login`"}
	case bq.KindOverLimit:
		return &cmdError{exit: api.ExitOverLimit, code: "over_limit", msg: err.Error()}
	case bq.KindCancelled:
		return &cmdError{exit: api.ExitCancelled, code: "cancelled", msg: "cancelled"}
	case bq.KindPermission, bq.KindNotFound, bq.KindTransient:
		return &cmdError{exit: api.ExitBigQuery, code: "bigquery", msg: err.Error()}
	}
	var be *bq.Error
	if errors.As(err, &be) {
		return &cmdError{exit: api.ExitBigQuery, code: "bigquery", msg: err.Error()}
	}
	return &cmdError{exit: api.ExitFailure, code: "failure", msg: err.Error()}
}

const usage = `bqt - a keyboard-driven BigQuery explorer

Usage:
  bqt [global flags] <command> [flags]

Commands:
  (none), tui    Open the interactive UI: explorer, editor, results (needs a terminal)
  FILE.sql       Open the UI with that file as an editor buffer
  init           Create a commented config file (never overwrites)
  config path    Print the config file path
  config edit    Open the config file in $VISUAL / $EDITOR and validate it
  sync           Refresh the local metadata catalog from BigQuery
  status         Show catalog freshness and sync problems
  search QUERY   Search the catalog (p: d: type: col: label: desc: filters)
  doctor         Print diagnostics (paths, versions, auth, catalog age)
  version        Print the version
  help           Show this help

Global flags:
  --config PATH            config file (default $BQT_CONFIG or ~/.config/bqt/config.toml)
  --format json|text       output format (json when stdout is not a terminal)
  --log-level LEVEL        debug, info, warn or error (default $BQT_LOG or info)
  --execution-project ID   override execution_project
  --location LOC           override location
  --data-dir DIR           override data_dir

Exit codes: 0 ok, 1 failure, 2 usage, 3 config, 4 auth, 5 over limit,
6 BigQuery error (also: sync finished with failed units), 7 cancelled.
`

// Run executes one bqt invocation and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	g := &globals{}
	fs := flag.NewFlagSet("bqt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		return printUsageError(env, err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		if !env.IsTTY(env.Stdout) || !env.IsTTY(env.Stdin) {
			fmt.Fprint(env.Stderr, usage)
			return api.ExitUsage
		}
		rest = []string{"tui"} // bare `bqt` on a terminal opens the explorer
	}
	cmd, cargs := rest[0], rest[1:]
	if strings.HasSuffix(strings.ToLower(cmd), ".sql") {
		// `bqt path/to/query.sql` opens the file as an editor buffer
		cmd, cargs = "tui", rest
	}
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		fmt.Fprint(env.Stdout, usage)
		return api.ExitOK
	}
	o, err := newOut(env, g.format)
	if err != nil {
		return (&out{env: env}).fail(err)
	}
	var cerr error
	switch cmd {
	case "version":
		cerr = cmdVersion(o, g, cargs)
	case "init":
		cerr = cmdInit(ctx, o, g, cargs)
	case "config":
		cerr = cmdConfig(ctx, o, g, cargs)
	case "sync":
		var code int
		code, cerr = cmdSync(ctx, o, g, cargs)
		if cerr == nil {
			return code
		}
	case "status":
		cerr = cmdStatus(ctx, o, g, cargs)
	case "search":
		cerr = cmdSearch(ctx, o, g, cargs)
	case "tui":
		cerr = cmdTUI(ctx, o, g, cargs)
	case "doctor":
		cerr = cmdDoctor(ctx, o, g, cargs)
	default:
		cerr = usageErr("unknown command %q", cmd)
	}
	if cerr != nil {
		return o.fail(cerr)
	}
	return api.ExitOK
}

func printUsageError(env Env, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(env.Stdout, usage)
		return api.ExitOK
	}
	return (&out{env: env}).fail(usageErr("%v", err))
}

// commandFlags builds a FlagSet for a command that also accepts the globals.
func commandFlags(name string, g *globals) *flag.FlagSet {
	fs := flag.NewFlagSet("bqt "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g.register(fs)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageErr("flags of `%s`: run `bqt help`", fs.Name())
		}
		return usageErr("%v", err)
	}
	return nil
}

func cmdVersion(o *out, g *globals, args []string) error {
	if err := parse(commandFlags("version", g), args); err != nil {
		return err
	}
	data := map[string]string{"version": version.String(), "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
	o.result(data, func(w io.Writer) {
		fmt.Fprintf(w, "bqt %s (%s %s/%s)\n", data["version"], data["go"], data["os"], data["arch"])
	})
	return nil
}
