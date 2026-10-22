package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

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

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(config.Template())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	data := map[string]any{"path": path}
	o.result(data, func(w io.Writer) {
		fmt.Fprintf(w, "Created %s\n", path)
		fmt.Fprintln(w, "Pick your projects in the TUI (ctrl+x p) or with `bqt projects`")
	})
	return nil
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
			if err := os.WriteFile(path, []byte(config.Template()), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(env.Stderr, "created %s from the template\n", path)
		}
		argv := append(config.ResolveEditor(env.Getenv, runtime.GOOS), path)
		run := env.RunEditor
		if run == nil {
			return fmt.Errorf("no editor runner available")
		}
		if err := run(ctx, argv, env); err != nil {
			return fmt.Errorf("editor %q: %w", argv[0], err)
		}
		// Validate what was saved (a GUI editor may return before saving; the
		// TUI's file watcher covers that case).
		if _, err := config.Load(g.loadOptions(env)); err != nil {
			return err
		}
		o.result(map[string]any{"path": path, "valid": true}, func(w io.Writer) {
			fmt.Fprintf(w, "%s is valid\n", path)
		})
		return nil
	}
	return usageErr("unknown config subcommand %q (use path or edit)", sub)
}
