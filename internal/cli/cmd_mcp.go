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

// cmdMCP serves the MCP tools over stdio (spec 14.2), or prints the client
// configuration snippets. stdout belongs to the protocol: logs go to the log
// file and stderr only.
func cmdMCP(ctx context.Context, o *out, g *globals, args []string) error {
	var printConfig bool
	fs := commandFlags("mcp", g)
	fs.BoolVar(&printConfig, "print-config", false, "print configuration snippets for Claude Code, Codex and Gemini CLI")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
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
	closeLog, log, err := newLogger(env, g, cfg)
	if err != nil {
		return err
	}
	defer closeLog()
	srv, err := mcp.New(ctx, func(ctx context.Context) (*engine.Engine, error) {
		return engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now, Log: log.Logger, Source: "mcp", Jobs: env.Jobs, PinExecution: true})
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

// mcpSnippets renders ready-to-paste client configuration.
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
