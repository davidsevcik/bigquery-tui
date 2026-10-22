package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

// cmdProjects lists the projects (BigQuery's list, the Selected ones, the
// cached ones) and changes the selection and the Execution project, like the
// TUI's Projects dialog. It never syncs.
func cmdProjects(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("projects", g)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	env := o.env
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now, Meta: env.Meta})
	if err != nil {
		return err
	}
	defer eng.Close()

	if len(pos) == 0 {
		rep, err := eng.ProjectsReport(ctx)
		if err != nil {
			return err
		}
		o.result(rep, func(w io.Writer) { printProjects(w, rep, env) })
		return nil
	}
	sub, ids := pos[0], pos[1:]
	switch sub {
	case "select":
		if len(ids) == 0 {
			return usageErr("usage: bqt projects select ID...")
		}
		warnUnlisted(ctx, eng, env, ids)
		if err := eng.SelectProjects(ids...); err != nil {
			return err
		}
		o.result(map[string]any{"selected": ids}, func(w io.Writer) {
			fmt.Fprintf(w, "selected %s; run `bqt sync` to fetch their metadata\n", strings.Join(ids, ", "))
		})
	case "deselect":
		if len(ids) == 0 {
			return usageErr("usage: bqt projects deselect ID...")
		}
		if err := eng.DeselectProjects(ids...); err != nil {
			return err
		}
		o.result(map[string]any{"deselected": ids}, func(w io.Writer) {
			fmt.Fprintf(w, "deselected %s (their cached metadata stays)\n", strings.Join(ids, ", "))
		})
	case "exec":
		if len(ids) != 1 {
			return usageErr("usage: bqt projects exec ID")
		}
		warnUnlisted(ctx, eng, env, ids)
		if err := eng.ChooseExecution(ids[0]); err != nil {
			return err
		}
		o.result(map[string]any{"execution_project": ids[0]}, func(w io.Writer) {
			fmt.Fprintf(w, "query and sync jobs now run in %s\n", ids[0])
		})
	default:
		return usageErr("unknown projects subcommand %q (use select, deselect or exec)", sub)
	}
	return nil
}

// warnUnlisted warns about IDs BigQuery's projects list does not return
// (data shared at dataset level, a typo, or no list at all). They are still
// accepted.
func warnUnlisted(ctx context.Context, eng *engine.Engine, env Env, ids []string) {
	listed, err := eng.AccessibleProjects(ctx, false)
	if err != nil {
		fmt.Fprintf(env.Stderr, "warning: could not list your projects (%v); not checking %s\n", err, strings.Join(ids, ", "))
		return
	}
	in := map[string]bool{}
	for _, p := range listed {
		in[p.ID] = true
	}
	for _, id := range ids {
		if !in[id] {
			fmt.Fprintf(env.Stderr, "warning: %s is not in BigQuery's list of your projects (fine for data shared to you at dataset level)\n", id)
		}
	}
}

func printProjects(w io.Writer, r api.ProjectsReport, env Env) {
	if r.ExecutionProject == "" {
		fmt.Fprintln(w, "execution project: none (select a project, or `bqt projects exec ID`)")
	} else {
		fmt.Fprintf(w, "execution project: %s (%s)\n", r.ExecutionProject, r.ExecutionSource)
	}
	if r.ListError != "" {
		fmt.Fprintf(w, "could not list your projects: %s\n", r.ListError)
	}
	// Selected projects first, then the rest; each A–Z (as in the dialog).
	for _, sel := range []bool{true, false} {
		for _, p := range r.Projects {
			if p.Selected != sel {
				continue
			}
			box := "[ ]"
			if p.Selected {
				box = "[x]"
			}
			fmt.Fprintf(w, "%s %-32s %-24s %s\n", box, p.ID, p.FriendlyName, projectState(p, env))
		}
	}
}

// projectState is the right-hand text of a project row.
func projectState(p api.ProjectInfo, env Env) string {
	var parts []string
	if !p.Listed {
		parts = append(parts, "not listed")
	}
	if !p.Accessible {
		parts = append(parts, "not accessible")
	}
	switch {
	case p.Sync == api.ProjectFailed:
		parts = append(parts, "sync failed")
	case p.Sync == api.ProjectNotSynced && p.Selected:
		parts = append(parts, "not synced yet")
	}
	if p.SyncedAt != nil {
		parts = append(parts, fmt.Sprintf("%d datasets", p.Datasets), ago(env.Now(), p.SyncedAt))
	}
	return strings.Join(parts, " · ")
}
