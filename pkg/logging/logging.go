// Package logging builds bqt's structured logger: JSON lines in a rotating
// file in data_dir/logs, never on the terminal for interactive commands
// (spec section 13).
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/davidsevcik/bigquery-tui/pkg/version"
)

const FileName = "bqt.log"

type Options struct {
	Dir     string
	Level   string
	Stderr  io.Writer
	Command string
}

func fileWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	// ADR-0012
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	_ = f.Chmod(0o600)
	_ = f.Close()
	return true
}

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

func New(o Options) (*slog.Logger, func() error, error) {
	level, err := ParseLevel(o.Level)
	if err != nil {
		return nil, nil, err
	}
	var handlers []slog.Handler
	closeFn := func() error { return nil }
	if o.Dir != "" && fileWritable(o.Dir) {
		path := filepath.Join(o.Dir, FileName)
		lj := &lumberjack.Logger{Filename: path, MaxSize: 5, MaxBackups: 3}
		handlers = append(handlers, slog.NewJSONHandler(lj, &slog.HandlerOptions{Level: level}))
		closeFn = lj.Close
		if o.Command != "" {
			slog.New(slog.NewJSONHandler(lj, nil)).Info("bqt started",
				"command", o.Command, "version", version.String(), "log_level", level.String(), "log_dir", o.Dir)
		}
	}
	if o.Stderr != nil {
		sl := slog.LevelWarn
		if level > sl {
			sl = level
		}
		handlers = append(handlers, slog.NewTextHandler(o.Stderr, &slog.HandlerOptions{Level: sl}))
	}
	var l *slog.Logger
	switch len(handlers) {
	case 0:
		return slog.New(slog.DiscardHandler), closeFn, nil
	case 1:
		l = slog.New(handlers[0])
	default:
		l = slog.New(fanout(handlers))
	}
	return l, closeFn, nil
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
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
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
