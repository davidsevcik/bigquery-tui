package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/search"
)

// PruneResult is the output of `bqt history prune`.
type PruneResult struct {
	Deleted int64     `json:"deleted"`
	Before  time.Time `json:"before"`
}

// cmdHistoryPrune handles `bqt history prune`.
func cmdHistoryPrune(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("history prune", g)
	older := fs.String("older-than", "", "delete entries older than an age such as 90d or 12w")
	before := fs.String("before", "", "delete entries created before a date (2026-01-31)")
	all := fs.Bool("all", false, "delete every entry")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	n := 0
	for _, set := range []bool{*older != "", *before != "", *all} {
		if set {
			n++
		}
	}
	if n != 1 {
		return usageErr("give exactly one of --older-than, --before or --all")
	}
	env := o.env
	now := env.Now()
	var cut time.Time
	switch {
	case *all:
		cut = now.Add(time.Hour) // anything created up to now
	case *older != "":
		t, ok := search.SinceTime(*older, now)
		if !ok || strings.Contains(*older, "-") { // an age, not a date

			return usageErr("--older-than %q: want an age such as 90d, 12w or 36h", *older)
		}
		cut = t
	default:
		t, ok := search.SinceTime(*before, now)
		if !ok {
			return usageErr("--before %q: want a date such as 2026-01-31", *before)
		}
		cut = t
	}
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now})
	if err != nil {
		return err
	}
	defer eng.Close()
	deleted, err := eng.PruneHistory(ctx, cut)
	if err != nil {
		return err
	}
	res := PruneResult{Deleted: deleted, Before: cut}
	o.result(res, func(w io.Writer) {
		if *all {
			fmt.Fprintf(w, "deleted %d History entries (all)\n", deleted)
			return
		}
		fmt.Fprintf(w, "deleted %d History entries created before %s\n", deleted, cut.Format("2006-01-02 15:04"))
	})
	return nil
}
