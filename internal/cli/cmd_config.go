package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/davidsevcik/bigquery-tui/pkg/config"
)

func cmdInit(o *out, g *globals, args []string) error {
	fs := commandFlags("init", g)
	if err := parseNoArgs(fs, args); err != nil {
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
	pos, err := parseAnywhere(commandFlags("config", g), args)
	if err != nil {
		return err
	}
	switch len(pos) {
	case 0:
		return usageErr("config needs a subcommand: path or edit")
	case 1:
	default:
		return usageErr("unexpected argument %q", pos[1])
	}
	sub := pos[0]
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
		before, beforeErr := os.ReadFile(path)
		if err := run(ctx, argv, env); err != nil {
			return fmt.Errorf("editor %q: %w", argv[0], err)
		}
		after, afterErr := os.ReadFile(path)
		changed := beforeErr != nil || afterErr != nil || !bytes.Equal(before, after)
		if _, err := config.Load(g.loadOptions(env)); err != nil {
			if changed {
				return err
			}
			return unchangedConfigErr(path, err)
		}
		o.result(map[string]any{"path": path, "valid": true, "changed": changed}, func(w io.Writer) {
			if changed {
				fmt.Fprintf(w, "%s is valid\n", path)
			} else {
				fmt.Fprintf(w, "no changes to %s; it is valid\n", path)
			}
		})
		if !changed {
			fmt.Fprintln(env.Stderr, "hint: "+waitingEditorHint)
		}
		return nil
	}
	return usageErr("unknown config subcommand %q (use path or edit)", sub)
}

const waitingEditorHint = "an editor that returns before you save needs a command that waits, for example VISUAL='code --wait'"

func unchangedConfigErr(path string, err error) error {
	ce := *classify(err)
	ce.msg = "no changes to " + path + "; " + ce.msg
	if ce.hint == "" {
		ce.hint = waitingEditorHint
	} else {
		ce.hint += "; " + waitingEditorHint
	}
	return &ce
}
