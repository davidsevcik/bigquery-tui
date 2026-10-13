// Command bqt is a keyboard-driven BigQuery explorer. This build contains the
// foundation (config, local catalog, sync, diagnostics); the interactive UI
// arrives in the next phase.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/davidsevcik/bigquery-tui/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], cli.DefaultEnv()))
}
