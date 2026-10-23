package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/logging"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// load reads the config and builds the logger for a command.
func load(env Env, g *globals) (*config.Config, error) {
	return config.Load(g.loadOptions(env))
}

func newLogger(env Env, g *globals, cfg *config.Config) (func() error, *engineLog, error) {
	level := g.logLevel
	if level == "" {
		level = env.Getenv("BQT_LOG")
	}
	opts := logging.Options{Dir: store.PathsFor(cfg.DataDir).LogDir, Level: level}
	if !env.IsTTY(env.Stderr) {
		opts.Stderr = env.Stderr
	}
	l, closeFn, err := logging.New(opts)
	if err != nil {
		return nil, nil, usageErr("%v", err)
	}
	return closeFn, &engineLog{l}, nil
}

func cmdSync(ctx context.Context, o *out, g *globals, args []string) (int, error) {
	var projects stringList
	var progress bool
	fs := commandFlags("sync", g)
	fs.Var(&projects, "project", "sync only this project: a selected one, or a deselected one to refresh its cache (repeatable)")
	fs.BoolVar(&progress, "progress", false, "stream progress to stderr (default on a terminal)")
	if err := parse(fs, args); err != nil {
		return 0, err
	}
	env := o.env
	cfg, err := load(env, g)
	if err != nil {
		return 0, err
	}
	closeLog, log, err := newLogger(env, g, cfg)
	if err != nil {
		return 0, err
	}
	defer closeLog()

	eng, err := engine.Open(ctx, engine.Options{
		Config: cfg, Meta: env.Meta, Now: env.Now, Log: log.Logger, Backoff: env.Backoff,
	})
	if err != nil {
		return 0, err
	}
	defer eng.Close()
	if n := eng.Notice(); n != "" {
		fmt.Fprintln(env.Stderr, "note: "+n)
	}

	h, err := eng.StartSync(ctx, api.SyncScope{Projects: projects})
	if err != nil {
		return 0, err
	}
	showProgress := progress || (!o.json && env.IsTTY(env.Stderr))
	for ev := range h.Events() {
		if showProgress {
			printEvent(env.Stderr, ev, o.json)
		}
	}
	res, err := h.Wait()
	if err != nil {
		return 0, err
	}
	o.result(res, func(w io.Writer) { printSyncSummary(w, res) })
	switch {
	case res.Cancelled:
		return api.ExitCancelled, nil
	case res.UnitsFailed > 0:
		if allAuthFailures(res) {
			fmt.Fprintln(env.Stderr, "hint: run `gcloud auth application-default login`")
			return api.ExitAuth, nil
		}
		return api.ExitBigQuery, nil
	}
	return api.ExitOK, nil
}

func printEvent(w io.Writer, ev api.SyncEvent, asJSON bool) {
	if asJSON {
		b, _ := jsonMarshal(ev)
		fmt.Fprintln(w, string(b))
		return
	}
	where := ev.Project
	if ev.Location != "" {
		where += "/" + ev.Location
	}
	switch ev.Kind {
	case api.EventRunStarted:
		fmt.Fprintf(w, "syncing %s\n", ev.Message)
	case api.EventDiscovered:
		fmt.Fprintf(w, "found %s\n", ev.Message)
	case api.EventUnitStarted:
		fmt.Fprintf(w, "  %s: %d datasets...\n", where, ev.Datasets)
	case api.EventUnitDone:
		line := fmt.Sprintf("[%d/%d] %s: %d datasets, %d tables", ev.UnitsDone, ev.UnitsTotal, where, ev.Datasets, ev.Tables)
		if ev.Status != api.UnitOK {
			line += " (" + string(ev.Status) + ")"
		}
		fmt.Fprintln(w, line)
		if ev.Error != "" {
			fmt.Fprintf(w, "      %s\n", ev.Error)
		}
	case api.EventUnitFailed:
		fmt.Fprintf(w, "[%d/%d] %s: FAILED: %s\n", ev.UnitsDone, ev.UnitsTotal, where, ev.Error)
	case api.EventWarning:
		fmt.Fprintf(w, "warning: %s %s\n", ev.Project, ev.Message)
	}
}

func printSyncSummary(w io.Writer, r api.SyncResult) {
	state := "finished"
	if r.Cancelled {
		state = "cancelled (committed units were kept)"
	}
	fmt.Fprintf(w, "sync %s in %s: %d unit(s): %d ok, %d degraded, %d failed\n",
		state, r.Finished.Sub(r.Started).Round(time.Millisecond), r.UnitsTotal, r.UnitsOK, r.UnitsDegraded, r.UnitsFailed)
	for _, u := range r.Units {
		if u.Status != api.UnitOK && u.Error != "" {
			loc := u.Location
			if loc == "" {
				loc = "-"
			}
			fmt.Fprintf(w, "  %s/%s [%s]: %s\n", u.Project, loc, u.Status, u.Error)
		}
	}
	for _, m := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", m)
	}
}

// allAuthFailures reports whether every failed unit failed on credentials.
func allAuthFailures(r api.SyncResult) bool {
	n := 0
	for _, u := range r.Units {
		if u.Status == api.UnitInaccessible || u.Status == api.UnitDegraded {
			if strings.HasPrefix(u.Error, "auth: ") {
				n++
			}
		}
	}
	return n > 0 && n >= r.UnitsFailed
}
