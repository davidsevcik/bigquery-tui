package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

func cmdSearch(ctx context.Context, o *out, g *globals, args []string) error {
	var limit int
	var cursor string
	var tablesOnly bool
	fs := commandFlags("search", g)
	fs.IntVar(&limit, "limit", 50, "maximum number of results")
	fs.StringVar(&cursor, "cursor", "", "continue from a previous next_cursor")
	fs.BoolVar(&tablesOnly, "tables-only", false, "leave out project and dataset results")
	if err := parse(fs, args); err != nil {
		return err
	}
	query := strings.Join(fs.Args(), " ")
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return usageErr("invalid --cursor %q", cursor)
		}
		offset = n
	}
	if limit <= 0 {
		return usageErr("--limit must be positive")
	}
	env := o.env
	cfg, err := load(env, g)
	if err != nil {
		return err
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now})
	if err != nil {
		return err
	}
	defer eng.Close()
	if eng.Catalog() == nil {
		return &cmdError{exit: api.ExitFailure, code: "no_catalog", msg: "no catalog yet", hint: "run `bqt sync`"}
	}
	resp, err := eng.Search(ctx, query, api.SearchOptions{Limit: limit, Offset: offset, TablesOnly: tablesOnly, AllWhenEmpty: false})
	if err != nil {
		return err
	}
	next := ""
	if offset+len(resp.Results) < resp.Total {
		next = strconv.Itoa(offset + len(resp.Results))
	}
	if o.json {
		return o.envelope(api.Envelope{SchemaVersion: api.SchemaVersion, Data: resp, Truncated: next != "", NextCursor: next})
	}
	printSearch(env.Stdout, resp, next)
	return nil
}

func printSearch(w io.Writer, r api.SearchResponse, next string) {
	for _, f := range r.Query.Filters {
		if f.Status != api.FilterOK {
			fmt.Fprintf(w, "ignored filter %s: (%s)\n", f.Key, f.Status)
		}
	}
	if r.Recent && len(r.Results) > 0 {
		fmt.Fprintln(w, "recently opened:")
	}
	for _, x := range r.Results {
		kind := string(x.Type)
		switch x.Kind {
		case api.KindProject, api.KindDataset:
			kind = string(x.Kind)
		case api.KindShards:
			kind = fmt.Sprintf("%s ×%d", x.Type, x.Shard.Count)
		}
		line := fmt.Sprintf("%-10s %s", kind, x.Path)
		if x.Kind == api.KindShards {
			line += fmt.Sprintf("  (%s → %s)", x.Shard.First, x.Shard.Last)
		}
		if x.Tier != "" {
			line += "  [" + x.MatchedField + "·" + x.Tier + "]"
		}
		if x.Stale {
			line += "  stale"
		}
		if x.NamesOnly {
			line += "  names only"
		}
		fmt.Fprintln(w, line)
		for _, c := range x.ColumnHits {
			fmt.Fprintf(w, "             · %s %s\n", c.FieldPath, c.DataType)
		}
		if x.MoreColumns > 0 {
			fmt.Fprintf(w, "             · +%d more columns\n", x.MoreColumns)
		}
	}
	fmt.Fprintf(w, "%d of %d result(s) in %.1f ms", len(r.Results), r.Total, r.TookMS)
	if next != "" {
		fmt.Fprintf(w, " (more: --cursor %s)", next)
	}
	fmt.Fprintln(w)
}
