package cli

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/logging"
	"github.com/davidsevcik/bigquery-tui/internal/store"
	"github.com/davidsevcik/bigquery-tui/internal/tui"
)

// cmdTUI starts the interactive explorer. Logs go to the log file only: the
// terminal belongs to the UI.
func cmdTUI(ctx context.Context, o *out, g *globals, args []string) error {
	if err := parse(commandFlags("tui", g), args); err != nil {
		return err
	}
	env := o.env
	if !env.IsTTY(env.Stdout) || !env.IsTTY(env.Stdin) {
		return usageErr("the interactive UI needs a terminal (use `bqt search`, `bqt status` and friends in scripts)")
	}
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	level := g.logLevel
	if level == "" {
		level = env.Getenv("BQT_LOG")
	}
	log, closeLog, err := logging.New(logging.Options{Dir: store.PathsFor(cfg.DataDir).LogDir, Level: level})
	if err != nil {
		return usageErr("%v", err)
	}
	defer closeLog()
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, Meta: env.Meta, Now: env.Now, Log: log})
	if err != nil {
		return err
	}
	defer eng.Close()

	m := tui.New(tui.EngineBackend{Engine: eng, Load: g.loadOptions(env)}, tui.Options{Getenv: env.Getenv, Now: env.Now})
	defer m.Close()
	if _, err := tea.NewProgram(m, tea.WithContext(ctx)).Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("ui: %w", err)
	}
	return nil
}
