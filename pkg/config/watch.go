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

type Event struct {
	Config          *Config
	Err             error
	Changed         []string
	RestartRequired []string
}

type WatchOptions struct {
	Load      LoadOptions
	Interval  time.Duration
	Stability time.Duration
	Kick      <-chan struct{}
}

var restartKeys = map[string]bool{"data_dir": true}

func Watch(ctx context.Context, current *Config, o WatchOptions) <-chan Event {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Stability <= 0 {
		o.Stability = 300 * time.Millisecond
	}
	out := make(chan Event, 1)
	path := ResolvePath(o.Load)
	// ADR-0012
	seen, _, _ := readHashed(path)
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
			h, raw, rerr := readHashed(path)
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
			cfg, err := fromRead(path, raw, rerr, o.Load)
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

func readHashed(path string) (h [sha256.Size]byte, raw []byte, err error) {
	raw, err = readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sha256.Sum256([]byte("\x00missing")), nil, err
		}
		return sha256.Sum256([]byte("\x00unreadable")), nil, err
	}
	return sha256.Sum256(raw), raw, nil
}

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
	add("location", a.Location, b.Location)
	add("ignore", a.Filter().Ignore, b.Filter().Ignore)
	add("shard_patterns", a.Shards().Sources(), b.Shards().Sources())
	add("data_dir", a.DataDir, b.DataDir)
	add("favorites_dir", a.FavoritesDir, b.FavoritesDir)
	add("export_dir", a.ExportDir, b.ExportDir)
	add("sync_ttl", a.SyncTTL, b.SyncTTL)
	add("confirm_bytes", a.ConfirmBytes, b.ConfirmBytes)
	add("max_bytes_billed", a.MaxBytesBilled, b.MaxBytesBilled)
	add("max_concurrent_queries", a.MaxConcurrentQueries, b.MaxConcurrentQueries)
	add("history_retention_days", a.HistoryRetentionDays, b.HistoryRetentionDays)
	add("agent_max_bytes", a.AgentMaxBytes, b.AgentMaxBytes)
	add("agent_timeout", a.AgentTimeout, b.AgentTimeout)
	add("cli_allow_writes", a.CLIAllowWrites, b.CLIAllowWrites)
	add("tui", a.TUI, b.TUI)
	add("gui", a.GUI, b.GUI)
	sort.Strings(out)
	return out
}
