// Package logging builds bqt's structured logger: JSON lines in a rotating
// file in data_dir/logs, never on the terminal for interactive commands
// (spec section 13).
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// FileName is the log file name inside the log directory.
const FileName = "bqt.log"

// Options configures New.
type Options struct {
	// Dir is the log directory (data_dir/logs). Empty disables the file.
	Dir string
	// Level is "debug", "info", "warn" or "error" (default "info").
	Level string
	// Stderr, if set, also receives records at warn and above (the CLI and the
	// MCP server; the TUI leaves it nil).
	Stderr io.Writer
}

// ParseLevel parses a level name.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (use debug, info, warn or error)", s)
}

// New returns the logger and a function that closes the log file.
func New(o Options) (*slog.Logger, func() error, error) {
	level, err := ParseLevel(o.Level)
	if err != nil {
		return nil, nil, err
	}
	var handlers []slog.Handler
	closeFn := func() error { return nil }
	if o.Dir != "" {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create log dir: %w", err)
		}
		lj := &lumberjack.Logger{Filename: filepath.Join(o.Dir, FileName), MaxSize: 5, MaxBackups: 3}
		handlers = append(handlers, slog.NewJSONHandler(lj, &slog.HandlerOptions{Level: level}))
		closeFn = lj.Close
	}
	if o.Stderr != nil {
		sl := slog.LevelWarn
		if level > sl {
			sl = level
		}
		handlers = append(handlers, slog.NewTextHandler(o.Stderr, &slog.HandlerOptions{Level: sl}))
	}
	switch len(handlers) {
	case 0:
		return slog.New(slog.DiscardHandler), closeFn, nil
	case 1:
		return slog.New(handlers[0]), closeFn, nil
	}
	return slog.New(fanout(handlers)), closeFn, nil
}

type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
