package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/mcp"
	"github.com/davidsevcik/bigquery-tui/internal/version"
)

func cmdMCP(ctx context.Context, o *out, g *globals, args []string) error {
	var printConfig bool
	fs := commandFlags("mcp", g)
	fs.BoolVar(&printConfig, "print-config", false, "print configuration snippets for Claude Code, Codex and Gemini CLI")
	if err := parseNoArgs(fs, args); err != nil {
		return err
	}
	env := o.env
	if printConfig {
		exe, err := os.Executable()
		if err != nil {
			exe = "bqt"
		}
		fmt.Fprint(env.Stdout, mcpSnippets(exe, g.configPath))
		return nil
	}
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	closeLog, log, err := newLogger(env, g, cfg, "mcp")
	if err != nil {
		return err
	}
	defer closeLog()
	// ADR-0009
	exec, err := engine.ResolveExecution(cfg)
	if err != nil {
		return err
	}
	srv, err := mcp.New(ctx, func(ctx context.Context) (*engine.Engine, error) {
		return engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now, Log: log.Logger, Source: "mcp", Jobs: env.Jobs, Execution: &exec})
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	log.Info("mcp server started")
	if err := srv.Run(ctx, version.String()); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func mcpSnippets(exe, configPath string) string {
	args := []string{"mcp"}
	if configPath != "" {
		args = []string{"--config", configPath, "mcp"}
	}
	q := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Claude Code (user scope):\n")
	w("claude mcp add --scope user bqt -- %s %s\n\n", shellQuote(exe), strings.Join(shellQuoteAll(args), " "))
	w("# Claude Code project file .mcp.json, and Gemini CLI ~/.gemini/settings.json:\n")
	w("{\n  \"mcpServers\": {\n    \"bqt\": {\"command\": %s, \"args\": %s}\n  }\n}\n\n", q(exe), q(args))
	w("# Codex ~/.codex/config.toml:\n")
	w("[mcp_servers.bqt]\ncommand = %s\nargs = %s\n", q(exe), q(args))
	return b.String()
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == ':' || r == '~' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellQuoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shellQuote(s)
	}
	return out
}
