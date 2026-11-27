package cli

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/tui"
	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/engine"
)

func cmdTUI(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("tui", g)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	file := ""
	switch len(pos) {
	case 0:
	case 1:
		file = pos[0]
	default:
		return usageErr("the UI opens at most one file")
	}
	env := o.env
	if !env.IsTTY(env.Stdout) || !env.IsTTY(env.Stdin) {
		return usageErr("the interactive UI needs a terminal (use `bqt search`, `bqt status` and friends in scripts)")
	}
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	log, closeLog, err := openLog(env, g, cfg, "tui", nil)
	if err != nil {
		return err
	}
	defer closeLog()
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, Meta: env.Meta, Now: env.Now, Log: log, Source: api.SourceTUI})
	if err != nil {
		return err
	}
	defer eng.Close()

	m := tui.New(tui.EngineBackend{Engine: eng, Load: g.loadOptions(env)}, tui.Options{Getenv: env.Getenv, Now: env.Now, File: file})
	defer m.Close()
	if err := env.RunUI(ctx, m); err != nil && ctx.Err() == nil {
		return fmt.Errorf("ui: %w", err)
	}
	return nil
}

func runProgram(ctx context.Context, m tea.Model) error {
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}
