// Command bqt is a keyboard-driven BigQuery explorer: the terminal UI, a
// scriptable CLI and an MCP server for agents, in one binary.
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
