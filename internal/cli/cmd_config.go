package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/config"
)

func cmdInit(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("init", g)
	if err := parse(fs, args); err != nil {
		return err
	}
	env := o.env
	path := config.ResolvePath(g.loadOptions(env))
	if _, err := os.Stat(path); err == nil {
		return &cmdError{exit: 1, code: "exists", msg: "config already exists at " + path + " (bqt init never overwrites)", hint: "edit it with `bqt config edit`"}
	}

	project := g.execProject
	suggested := false
	if project == "" && env.IsTTY(env.Stdin) && env.IsTTY(env.Stdout) && env.Gcloud != nil {
		if p := env.Gcloud(); p != "" {
			fmt.Fprintf(env.Stdout, "Use %q (your gcloud project) as execution_project? [y/N] ", p)
			line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a == "y" || a == "yes" {
				project, suggested = p, true
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(config.Template(project))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	data := map[string]any{"path": path, "execution_project_set": project != "", "from_gcloud": suggested}
	o.result(data, func(w io.Writer) {
		fmt.Fprintf(w, "Created %s\n", path)
		if project == "" {
			fmt.Fprintln(w, "Set execution_project (required) and your projects: bqt config edit")
		} else {
			fmt.Fprintln(w, "Add the projects to sync: bqt config edit")
		}
	})
	return nil
}

// ResolveEditor picks the editor command: $VISUAL, then $EDITOR, then the OS
// opener (open -t on macOS, xdg-open elsewhere).
func ResolveEditor(getenv func(string) string, goos string) []string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return strings.Fields(v)
		}
	}
	if goos == "darwin" {
		return []string{"open", "-t"}
	}
	return []string{"xdg-open"}
}

func cmdConfig(ctx context.Context, o *out, g *globals, args []string) error {
	if len(args) == 0 {
		return usageErr("config needs a subcommand: path or edit")
	}
	sub, rest := args[0], args[1:]
	fs := commandFlags("config "+sub, g)
	if err := parse(fs, rest); err != nil {
		return err
	}
	env := o.env
	path := config.ResolvePath(g.loadOptions(env))
	switch sub {
	case "path":
		o.result(map[string]string{"path": path}, func(w io.Writer) { fmt.Fprintln(w, path) })
		return nil
	case "edit":
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(config.Template(g.execProject)), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(env.Stderr, "created %s from the template\n", path)
		}
		argv := append(ResolveEditor(env.Getenv, runtime.GOOS), path)
		run := env.RunEditor
		if run == nil {
			return fmt.Errorf("no editor runner available")
		}
		if err := run(ctx, argv, env); err != nil {
			return fmt.Errorf("editor %q: %w", argv[0], err)
		}
		// Validate what was saved (a GUI editor may return before saving; the
		// TUI's file watcher covers that case).
		c, err := config.Load(g.loadOptions(env))
		if err != nil {
			return err
		}
		o.result(map[string]any{"path": path, "valid": true, "projects": len(c.Projects)}, func(w io.Writer) {
			fmt.Fprintf(w, "%s is valid (%d project(s))\n", path, len(c.Projects))
		})
		return nil
	}
	return usageErr("unknown config subcommand %q (use path or edit)", sub)
}
