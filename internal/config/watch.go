package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"reflect"
	"sort"
	"time"
)

// Event is one observed change of the config file.
type Event struct {
	// Config is the new valid configuration. It is nil when Err is set, in
	// which case the last good configuration stays in force.
	Config *Config
	Err    error
	// Changed lists the keys whose effective value differs from the last
	// good configuration.
	Changed []string
	// RestartRequired is the subset of Changed that cannot be applied live.
	RestartRequired []string
}

// WatchOptions configures Watch.
type WatchOptions struct {
	Load LoadOptions
	// Interval is the poll period (default 1s).
	Interval time.Duration
	// Stability is how long the content must stay unchanged before it is
	// applied (default 300ms).
	Stability time.Duration
	// Kick triggers an immediate check (editor exited, terminal focused).
	Kick <-chan struct{}
}

// restartKeys cannot be applied to a running process.
var restartKeys = map[string]bool{"data_dir": true}

// Watch polls the config file and emits an Event whenever its content changes
// and has been stable for the configured time. Polling (rather than fsnotify)
// copes with editors that save by rename and with symlinked dotfiles. The
// channel is closed when ctx ends.
func Watch(ctx context.Context, current *Config, o WatchOptions) <-chan Event {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Stability <= 0 {
		o.Stability = 300 * time.Millisecond
	}
	out := make(chan Event, 1)
	path := ResolvePath(o.Load)
	// Hash synchronously so a write racing with the goroutine start is seen.
	seen, _ := readHashed(path)
	go func() {
		defer close(out)
		good := current
		pending, pendingSince := seen, time.Time{}
		tick := time.NewTicker(o.Interval)
		defer tick.Stop()
		var settle <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			case <-o.Kick:
			case <-settle:
			}
			settle = nil
			h, raw := readHashed(path)
			if h == seen {
				pending = seen
				continue
			}
			now := time.Now()
			if h != pending {
				pending, pendingSince = h, now
			}
			if wait := o.Stability - now.Sub(pendingSince); wait > 0 {
				settle = time.After(wait)
				continue
			}
			seen = h
			ev := Event{}
			// Parse the bytes that were hashed: reading the file again could
			// catch a write that started after the stability check.
			var cfg *Config
			var err error
			if raw != nil {
				cfg, err = Parse(raw, path, o.Load)
			} else {
				cfg, err = Load(o.Load)
			}
			if err != nil {
				ev.Err = err
			} else {
				ev.Config = cfg
				ev.Changed = Diff(good, cfg)
				for _, k := range ev.Changed {
					if restartKeys[k] {
						ev.RestartRequired = append(ev.RestartRequired, k)
					}
				}
				good = cfg
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// readHashed returns the file's content and its hash. raw is nil when the
// file is missing or unreadable.
func readHashed(path string) (h [sha256.Size]byte, raw []byte) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sha256.Sum256([]byte("\x00missing")), nil
		}
		return sha256.Sum256([]byte("\x00unreadable")), nil
	}
	return sha256.Sum256(b), b
}

// Diff returns the config keys whose effective values differ, sorted.
func Diff(a, b *Config) []string {
	if a == nil || b == nil {
		return nil
	}
	var out []string
	add := func(key string, x, y any) {
		if !reflect.DeepEqual(x, y) {
			out = append(out, key)
		}
	}
	add("execution_project", a.ExecutionProject, b.ExecutionProject)
	add("location", a.Location, b.Location)
	add("projects", a.Filter().Projects, b.Filter().Projects)
	add("ignore", a.Filter().Ignore, b.Filter().Ignore)
	add("data_dir", a.DataDir, b.DataDir)
	add("favorites_dir", a.FavoritesDir, b.FavoritesDir)
	add("sync_ttl", a.SyncTTL, b.SyncTTL)
	add("confirm_bytes", a.ConfirmBytes, b.ConfirmBytes)
	add("max_bytes_billed", a.MaxBytesBilled, b.MaxBytesBilled)
	add("max_concurrent_queries", a.MaxConcurrentQueries, b.MaxConcurrentQueries)
	add("history_retention_days", a.HistoryRetentionDays, b.HistoryRetentionDays)
	add("agent_max_bytes", a.AgentMaxBytes, b.AgentMaxBytes)
	add("agent_timeout", a.AgentTimeout, b.AgentTimeout)
	add("cli_allow_writes", a.CLIAllowWrites, b.CLIAllowWrites)
	add("tui", a.TUI, b.TUI)
	sort.Strings(out)
	return out
}
