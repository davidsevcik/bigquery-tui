package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// The agent-facing commands (spec 14.1): schema, query, results, history,
// favorites. Every query runs under the CLI's policy profile (spec 14.3).

// parseAnywhere parses flags that may come before, between or after the
// positional arguments (`bqt query "SELECT 1" --limit 5`). An argument with
// white space in it is never a flag, so SQL starting with "--" or "-" stays
// SQL; a lone "--" ends the flags. A negated search filter (-status:failed)
// is a positional argument too.
func parseAnywhere(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !isFlagArg(a) {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	if err := parse(fs, flags); err != nil {
		return nil, err
	}
	return pos, nil
}

func isFlagArg(a string) bool {
	if len(a) < 2 || a[0] != '-' || strings.IndexFunc(a, unicode.IsSpace) >= 0 {
		return false
	}
	name := strings.TrimLeft(a, "-")
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	// -status:failed is a negated filter, -5 a number
	return name != "" && !strings.Contains(name, ":") && !unicode.IsDigit(rune(name[0]))
}

// limits holds the flags that lower the policy limits.
type limits struct {
	maxBytes string
	timeout  time.Duration
}

func (l *limits) register(fs *flag.FlagSet) {
	fs.StringVar(&l.maxBytes, "max-bytes", "", "lower the byte limit for this query (e.g. 100MiB); raising it takes agent_max_bytes in the config")
	fs.DurationVar(&l.timeout, "timeout", 0, "lower the timeout for this query (e.g. 30s); raising it takes agent_timeout in the config")
}

func (l *limits) bytes() (int64, error) {
	if l.maxBytes == "" {
		return 0, nil
	}
	n, err := config.ParseBytes(l.maxBytes)
	if err != nil || n <= 0 {
		return 0, usageErr("--max-bytes %q: want a size such as 500MiB", l.maxBytes)
	}
	return n, nil
}

// openAgent opens a read-only engine whose queries run as source cli.
func openAgent(ctx context.Context, o *out, g *globals) (*engine.Engine, func(), error) {
	env := o.env
	cfg, err := load(env, g)
	if err != nil {
		return nil, nil, err
	}
	closeLog, log, err := newLogger(env, g, cfg)
	if err != nil {
		return nil, nil, err
	}
	eng, err := engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Now: env.Now, Log: log.Logger, Source: "cli", Jobs: env.Jobs})
	if err != nil {
		closeLog()
		return nil, nil, err
	}
	return eng, func() { eng.Close(); closeLog() }, nil
}

func parseParams(list []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range list {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimPrefix(strings.TrimSpace(k), "@")
		if !ok || k == "" {
			return nil, usageErr("--param %q: want name=value (a TYPE: prefix forces the type, e.g. d=DATE:2024-01-31)", p)
		}
		for prev := range m {
			if strings.EqualFold(prev, k) {
				return nil, usageErr("--param %s given twice (names ignore case)", k)
			}
		}
		m[k] = v
	}
	return m, nil
}

func cursorOf(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, usageErr("invalid --cursor %q", s)
	}
	return n, nil
}

func cmdSchema(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("schema", g)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: bqt schema project.dataset.table")
	}
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	if eng.Catalog() == nil {
		return engine.ErrNoCatalog
	}
	s, err := eng.Schema(ctx, pos[0])
	if err != nil {
		return err
	}
	o.result(s, func(w io.Writer) { printSchema(w, s) })
	return nil
}

func printSchema(w io.Writer, s api.TableSchema) {
	fmt.Fprintf(w, "%s  (%s", s.Table, s.Type)
	if s.Location != "" {
		fmt.Fprintf(w, ", %s", s.Location)
	}
	fmt.Fprintln(w, ")")
	if s.Shard != nil {
		fmt.Fprintf(w, "shard family %s*: %d tables, %s → %s (schema of the newest)\n", s.Shard.Prefix, s.Shard.Count, s.Shard.First, s.Shard.Last)
	}
	if s.Description != "" {
		fmt.Fprintln(w, s.Description)
	}
	if s.NamesOnly {
		fmt.Fprintln(w, "names only: sync could not read the columns")
	}
	if s.Stale {
		fmt.Fprintln(w, "stale: the catalog is older than sync_ttl")
	}
	width := 4
	for _, c := range s.Columns {
		width = max(width, len(c.Name))
	}
	for _, c := range s.Columns {
		flags := ""
		if c.Required {
			flags += " REQUIRED"
		}
		if c.Partition {
			flags += " partitioning"
		}
		if c.Clustering > 0 {
			flags += fmt.Sprintf(" clustering#%d", c.Clustering)
		}
		line := fmt.Sprintf("  %-*s  %s%s", width, c.Name, c.Type, flags)
		if c.Description != "" {
			line += "  — " + c.Description
		}
		fmt.Fprintln(w, line)
	}
	if s.ViewSQL != "" {
		fmt.Fprintf(w, "view SQL:\n%s\n", s.ViewSQL)
	}
}

// querySQL reads the SQL of `bqt query`: the arguments, --file, or stdin
// ("-", or no SQL at all with stdin not a terminal).
func querySQL(env Env, pos []string, file string) (string, error) {
	var sql string
	switch {
	case file != "" && len(pos) > 0:
		return "", usageErr("give the SQL as arguments or --file, not both")
	case file == "-" || (len(pos) == 1 && pos[0] == "-") || (file == "" && len(pos) == 0 && !env.IsTTY(env.Stdin)):
		b, err := io.ReadAll(env.Stdin)
		if err != nil {
			return "", err
		}
		sql = string(b)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", usageErr("%v", err)
		}
		sql = string(b)
	default:
		sql = strings.Join(pos, " ")
	}
	if strings.TrimSpace(sql) == "" {
		return "", usageErr(`usage: bqt query "SELECT ..." (or --file f.sql, or SQL on stdin)`)
	}
	return sql, nil
}

func cmdQuery(ctx context.Context, o *out, g *globals, args []string) error {
	var params stringList
	var file, cursor string
	var dry bool
	var limit int
	var lim limits
	fs := commandFlags("query", g)
	fs.BoolVar(&dry, "dry-run", false, "estimate only: bytes, statement type and whether the policy allows it")
	fs.StringVar(&file, "file", "", "read the SQL from a file (- for stdin)")
	fs.Var(&params, "param", "a query parameter name=value (repeatable; a TYPE: prefix forces the type)")
	fs.IntVar(&limit, "limit", 50, "rows to return (at most 1000; page the rest with `bqt results`)")
	fs.StringVar(&cursor, "cursor", "", "not used: page with `bqt results RESULT_ID --cursor N`")
	lim.register(fs)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if cursor != "" {
		return usageErr("page a result with `bqt results RESULT_ID --cursor %s`", cursor)
	}
	sql, err := querySQL(o.env, pos, file)
	if err != nil {
		return err
	}
	values, err := parseParams(params)
	if err != nil {
		return err
	}
	ps, err := engine.BindParams(sql, nil, values)
	if err != nil {
		return usageErr("%v", err)
	}
	return runQuery(ctx, o, g, engine.AgentQuery{SQL: sql, Params: ps, Limit: limit}, dry, lim)
}

func runQuery(ctx context.Context, o *out, g *globals, q engine.AgentQuery, dry bool, lim limits) error {
	if q.Limit <= 0 {
		return usageErr("--limit must be positive")
	}
	maxBytes, err := lim.bytes()
	if err != nil {
		return err
	}
	if lim.timeout < 0 {
		return usageErr("--timeout must be positive")
	}
	q.MaxBytes, q.Timeout = maxBytes, lim.timeout
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	if dry {
		r, err := eng.AgentDryRun(ctx, q)
		if err != nil {
			return err
		}
		o.result(r, func(w io.Writer) { printDryRun(w, r) })
		return nil
	}
	res, err := eng.RunAgentQuery(ctx, q)
	if err != nil {
		return err
	}
	return printResult(o, res)
}

func printDryRun(w io.Writer, r api.DryRunResult) {
	fmt.Fprintf(w, "%s, would process %s", r.StatementType, format.Bytes(r.Bytes))
	if r.Accuracy != "" && r.Accuracy != "PRECISE" {
		fmt.Fprintf(w, " (%s)", strings.ToLower(strings.ReplaceAll(r.Accuracy, "_", " ")))
	}
	switch {
	case r.Capacity:
		fmt.Fprint(w, ", on a reservation (not billed)")
	case r.ApproxUSD != nil:
		fmt.Fprintf(w, ", about $%.4f on demand", *r.ApproxUSD)
	}
	fmt.Fprintln(w)
	if r.Allowed {
		fmt.Fprintf(w, "allowed (limit %s)\n", format.Bytes(r.Limit))
	} else if r.Refusal != nil {
		fmt.Fprintf(w, "would be refused: %s\n", r.Refusal.Message)
	}
}

func printResult(o *out, res api.QueryResult) error {
	if o.json {
		return o.envelope(api.Envelope{SchemaVersion: api.SchemaVersion, Data: res, Truncated: res.Truncated, NextCursor: res.NextCursor})
	}
	w := o.env.Stdout
	if len(res.Columns) > 0 {
		printTable(w, res.Columns, res.Rows)
	}
	switch {
	case len(res.Columns) > 0:
		fmt.Fprintf(w, "rows %d–%d of %d", min(res.StartIndex+1, res.TotalRows), res.StartIndex+int64(len(res.Rows)), res.TotalRows)
	case res.Stats != nil && res.Stats.AffectedRows > 0:
		fmt.Fprintf(w, "%s: %d row(s) affected", res.StatementType, res.Stats.AffectedRows)
	default:
		fmt.Fprintf(w, "%s done", res.StatementType)
	}
	if s := res.Stats; s != nil {
		fmt.Fprintf(w, " · %s processed", format.Bytes(s.BytesProcessed))
		if s.CacheHit {
			fmt.Fprint(w, " (cached)")
		}
	}
	fmt.Fprintln(w)
	if res.TruncatedCells > 0 {
		fmt.Fprintf(w, "%d cell(s) cut at 200 characters\n", res.TruncatedCells)
	}
	if res.NextCursor != "" {
		fmt.Fprintf(w, "more: bqt results %s --cursor %s\n", res.ResultID, res.NextCursor)
	}
	return nil
}

// printTable writes rows as aligned text columns (each at most 40 wide).
func printTable(w io.Writer, cols []api.PreviewColumn, rows [][]*string) {
	const maxW = 40
	cell := func(v *string) string {
		if v == nil {
			return "NULL"
		}
		s := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' || r == '\r' {
				return ' '
			}
			return r
		}, *v)
		if r := []rune(s); len(r) > maxW {
			s = string(r[:maxW-1]) + "…"
		}
		return s
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = min(len([]rune(c.Name)), maxW)
	}
	text := make([][]string, len(rows))
	for r, row := range rows {
		text[r] = make([]string, len(cols))
		for i := range cols {
			var v *string
			if i < len(row) {
				v = row[i]
			}
			text[r][i] = cell(v)
			widths[i] = max(widths[i], len([]rune(text[r][i])))
		}
	}
	line := func(vals []string) {
		var b strings.Builder
		for i, v := range vals {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(v)
			if i < len(vals)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(v))))
			}
		}
		fmt.Fprintln(w, b.String())
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = cell(&c.Name)
	}
	line(names)
	for _, r := range text {
		line(r)
	}
}

func cmdResults(ctx context.Context, o *out, g *globals, args []string) error {
	var cursor string
	var limit int
	fs := commandFlags("results", g)
	fs.StringVar(&cursor, "cursor", "", "the row to start at (a next_cursor)")
	fs.IntVar(&limit, "limit", 50, "rows to return (at most 1000)")
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: bqt results RESULT_ID [--cursor N] [--limit N]")
	}
	if limit <= 0 {
		return usageErr("--limit must be positive")
	}
	start, err := cursorOf(cursor)
	if err != nil {
		return err
	}
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	res, err := eng.AgentResults(ctx, pos[0], start, limit)
	if err != nil {
		return err
	}
	return printResult(o, res)
}

// cmdHistory handles `bqt history [QUERY]` and `bqt history prune`.
func cmdHistory(ctx context.Context, o *out, g *globals, args []string) error {
	if len(args) > 0 && args[0] == "prune" {
		return cmdHistoryPrune(ctx, o, g, args[1:])
	}
	var cursor string
	var limit int
	fs := commandFlags("history", g)
	fs.StringVar(&cursor, "cursor", "", "continue from a previous next_cursor")
	fs.IntVar(&limit, "limit", 50, "maximum number of entries")
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return usageErr("--limit must be positive")
	}
	start, err := cursorOf(cursor)
	if err != nil {
		return err
	}
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	l, err := eng.HistoryList(ctx, strings.Join(pos, " "), limit, int(start))
	if err != nil {
		return err
	}
	if o.json {
		return o.envelope(api.Envelope{SchemaVersion: api.SchemaVersion, Data: l, Truncated: l.Truncated, NextCursor: l.NextCursor})
	}
	w := o.env.Stdout
	for _, f := range l.Ignored {
		fmt.Fprintf(w, "ignored filter %s\n", f)
	}
	for _, it := range l.Items {
		meta := fmt.Sprintf("#%d %s %s %s", it.ID, it.Created.Local().Format("2006-01-02 15:04"), it.State, it.Source)
		if it.Runs > 1 {
			meta += fmt.Sprintf(" ×%d", it.Runs)
		}
		if it.BytesProcessed != nil {
			meta += " " + format.Bytes(*it.BytesProcessed)
		}
		if it.Favorite != "" {
			meta += " ★ " + it.Favorite
		}
		fmt.Fprintln(w, meta)
		sql := strings.Join(strings.Fields(it.SQL), " ")
		if r := []rune(sql); len(r) > 160 {
			sql = string(r[:159]) + "…"
		}
		fmt.Fprintln(w, "  "+sql)
		if it.Error != "" {
			fmt.Fprintln(w, "  error: "+it.Error)
		}
	}
	if l.NextCursor != "" {
		fmt.Fprintf(w, "more: --cursor %s\n", l.NextCursor)
	}
	return nil
}

func cmdFavorites(ctx context.Context, o *out, g *globals, args []string) error {
	if len(args) == 0 {
		return usageErr("usage: bqt favorites list [QUERY] | show ID | run ID [--param name=value]")
	}
	switch args[0] {
	case "list", "ls":
		return cmdFavoritesList(ctx, o, g, args[1:])
	case "show":
		return cmdFavoritesShow(ctx, o, g, args[1:])
	case "run":
		return cmdFavoritesRun(ctx, o, g, args[1:])
	}
	return usageErr("unknown favorites command %q (list, show or run)", args[0])
}

func cmdFavoritesList(ctx context.Context, o *out, g *globals, args []string) error {
	var cursor string
	var limit int
	var withSQL bool
	fs := commandFlags("favorites list", g)
	fs.StringVar(&cursor, "cursor", "", "continue from a previous next_cursor")
	fs.IntVar(&limit, "limit", 50, "maximum number of Favorites")
	fs.BoolVar(&withSQL, "sql", false, "include each Favorite's SQL")
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return usageErr("--limit must be positive")
	}
	start, err := cursorOf(cursor)
	if err != nil {
		return err
	}
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	l, err := eng.FavoriteList(strings.Join(pos, " "), limit, int(start), withSQL)
	if err != nil {
		return err
	}
	if o.json {
		return o.envelope(api.Envelope{SchemaVersion: api.SchemaVersion, Data: l, Truncated: l.Truncated, NextCursor: l.NextCursor})
	}
	w := o.env.Stdout
	for _, f := range l.Favorites {
		line := f.ID
		if f.Description != "" {
			line += "  — " + f.Description
		}
		if len(f.Tags) > 0 {
			line += "  [" + strings.Join(f.Tags, ", ") + "]"
		}
		fmt.Fprintln(w, line)
		for _, p := range f.Params {
			fmt.Fprintf(w, "    @%s %s%s\n", p.Name, p.Type, defaultText(p))
		}
		if f.SQL != "" {
			fmt.Fprintln(w, indent(strings.TrimRight(f.SQL, "\n"), "    "))
		}
	}
	fmt.Fprintf(w, "%d of %d Favorite(s)", len(l.Favorites), l.Total)
	if l.NextCursor != "" {
		fmt.Fprintf(w, " (more: --cursor %s)", l.NextCursor)
	}
	fmt.Fprintln(w)
	return nil
}

func defaultText(p api.ParamSpec) string {
	if p.Default == nil {
		return ""
	}
	return " = " + *p.Default
}

func indent(s, pre string) string { return pre + strings.ReplaceAll(s, "\n", "\n"+pre) }

func cmdFavoritesShow(ctx context.Context, o *out, g *globals, args []string) error {
	fs := commandFlags("favorites show", g)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: bqt favorites show ID")
	}
	eng, done, err := openAgent(ctx, o, g)
	if err != nil {
		return err
	}
	defer done()
	f, err := eng.ReadFavorite(pos[0])
	if err != nil {
		return err
	}
	s := api.FavoriteSummary{ID: f.ID, Description: f.Description, Tags: f.Tags, Params: eng.QueryParams(f.Text, f.SQL), SQL: f.SQL, Problems: f.Problems}
	o.result(s, func(w io.Writer) { fmt.Fprint(w, f.Text) })
	return nil
}

func cmdFavoritesRun(ctx context.Context, o *out, g *globals, args []string) error {
	var params stringList
	var dry bool
	var limit int
	var lim limits
	fs := commandFlags("favorites run", g)
	fs.BoolVar(&dry, "dry-run", false, "estimate only")
	fs.Var(&params, "param", "a parameter name=value (repeatable; declared defaults fill the rest)")
	fs.IntVar(&limit, "limit", 50, "rows to return (at most 1000)")
	lim.register(fs)
	pos, err := parseAnywhere(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: bqt favorites run ID [--param name=value]")
	}
	values, err := parseParams(params)
	if err != nil {
		return err
	}
	cfg, err := load(o.env, g)
	if err != nil {
		return err
	}
	f, err := engine.ReadFavoriteIn(cfg, pos[0])
	if err != nil {
		return err
	}
	ps, err := engine.BindParams(f.Text, &f, values)
	if err != nil {
		return usageErr("%v", err)
	}
	return runQuery(ctx, o, g, engine.AgentQuery{SQL: f.Text, Params: ps, Favorite: f.ID, Limit: limit}, dry, lim)
}
