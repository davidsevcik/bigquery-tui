# bqt

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents.

Status: **phases 1 to 5** of [`docs/spec/bqt-v1.md`](docs/spec/bqt-v1.md). This build has the config file, the local metadata catalog, sync, catalog search, the interactive explorer, the query editor with completion, query runs with a dry-run first, the results viewer with export, History and Favorites, and the agent layer: a JSON command line, an MCP server and hand-off actions in the UI.

## Build

Needs Go 1.26 or newer.

```sh
go build -o bin/bqt ./cmd/bqt
```

## Use

```sh
gcloud auth application-default login   # Application Default Credentials only
bin/bqt init --execution-project my-gcp-project
bin/bqt config edit                      # add the projects to sync
bin/bqt                                  # the interactive UI (syncs in the background)
bin/bqt path/to/query.sql                # the UI with that file open in the editor
bin/bqt sync                             # or fill the local catalog from a script or cron
bin/bqt search 'orders p:prod -d:tmp'    # search the catalog from the command line
bin/bqt status                           # freshness and sync problems
bin/bqt doctor                           # paths, versions, auth, catalog age
```

In the explorer: `hjkl` or the arrows move, `/` filters the list in place (the focused Miller column, the whole tree, or the flat list; `esc` clears it), `ctrl+t` jumps to a table, `ctrl+p` opens the command palette, `L` cycles the layout (Miller columns, tree, flat list), `p` previews rows, `?` lists every key. `ctrl+x p` turns projects on or off; with one project on, the Miller view starts at its datasets. `ctrl+g` starts an interactive tutorial of the navigation.

**Writing and running queries.** `ctrl+o` cycles the three zones: Explorer, Editor, Results (`ctrl+w` then `h`/`l`/`j` goes straight to one; `ctrl+w w` / `W` cycle as in vim).

- Editor: vim-style editing by default (`i` to insert, `esc` back to Normal mode, the usual motions, operators, text objects, Visual mode, `/`, `.`; `?` lists what is built). In Normal mode `enter` runs and `space d` dry-runs; `ctrl+r` is redo there. Set `[tui] editing = "plain"` in the config for a modeless editor. The keys below are for Insert mode and plain editing.
- Type SQL; the completion popup offers projects, datasets, tables, columns (through aliases, CTEs and `UNNEST`), functions and keywords. `tab` accepts, `ctrl+space` forces it. `ctrl+r` runs the selection or the whole buffer, `ctrl+d` only dry-runs it. `@name` parameters are asked for. `ctrl+x n` / `b` / `w` / `[` / `]` manage buffers; every buffer is autosaved as a draft and offered for restore after a crash or a closed terminal. `i` in the Explorer inserts the selected table's name.
- Every run does a dry-run first. bqt shows the estimate, asks before a non-SELECT statement or a scan over `confirm_bytes`, and sets `max_bytes_billed` on the job. `ctrl+c` cancels the focused run; `ctrl+x l` (or `gr`) lists the runs.
- Results: `hjkl` move the cell cursor, `enter` shows the row as a full record, `/` finds text in the loaded pages, `f` freezes the first column, `H` hides one, `y` / `Y` copy the cell or the row as JSON, `e` exports every row to CSV, JSON or NDJSON.

**History and Favorites.** `ctrl+x r` opens History: every query you ran, grouped by SQL text and searchable (`status:failed`, `project:`, `source:`, `since:7d`). `ctrl+x f` opens Favorites, saved queries kept as plain `.sql` files in `favorites_dir` (put it in a git repo if you like); `tab` switches between the two. `enter` loads a pick into the editor, `ctrl+s` on a History entry saves it as a Favorite, `ctrl+r` shows its result again (BigQuery keeps results about 24 hours). In the editor `ctrl+x s` saves the buffer as a Favorite. A Favorite's header declares typed parameters, and the file still runs as plain SQL:

```sql
-- description: Daily active users by country
-- tags: events, dau
-- param: start DATE = 2024-01-01
-- param: country STRING
SELECT ... WHERE d >= @start AND c = @country
```

`bqt history prune --older-than 90d` deletes old History entries (`history_retention_days` in the config does it automatically).

**Hand-off to an agent.** `ctrl+x y` (vim: `space y`) offers what fits the moment: the selected table's schema as markdown or JSON, the focused run's SQL with its error, the first 20 result rows (markdown, TSV or NDJSON), or a summary of the run. `enter` copies it to the clipboard (OSC 52, so it works over SSH and tmux); `w` writes it to a file, `./.bqt-handoff/` by default. Each one is also a palette command ("Hand-off: …").

## Agents: command line and MCP

Agents can use bqt two ways: run the commands (Claude Code calls them through Bash), or start the MCP server.

```sh
bqt search 'col:customer_id d:sales'         # find tables
bqt schema proj.sales.orders                  # columns, types, descriptions
bqt query --dry-run 'SELECT ...'              # bytes, statement type, allowed or not
bqt query 'SELECT ... WHERE d > @d' --param d=DATE:2024-01-31 --limit 20
bqt results 'proj:EU.job_id' --cursor 20      # more rows of that query (about 24 hours)
bqt history 'status:failed since:7d'          # past runs
bqt favorites list | show ID | run ID --param country=CZ
bqt mcp --print-config                        # snippets for Claude Code, Codex, Gemini CLI
```

Every command prints one JSON document when stdout is not a terminal (`{"schema_version":1,"data":…,"truncated":…,"next_cursor":…}`) and errors as JSON on stderr. The SQL can also come from `--file` or stdin.

`bqt mcp` serves eight tools over stdio: `search_catalog`, `get_schema`, `catalog_status`, `dry_run`, `run_query`, `get_results`, `list_favorites`, `search_history`. It never syncs; `catalog_status` tells the agent to ask you to run `bqt sync` when the catalog is stale.

**Safety.** bqt cannot tell an agent from a person at the command line, so `bqt query`, `bqt favorites run` and the MCP server share one policy, checked on the dry-run before any job starts:

- Only `SELECT` runs. Scripts, DML, DDL, `EXPORT DATA` and `CALL` are refused (`cli_allow_writes = true` opens the command line, never MCP).
- At most `agent_max_bytes` (default 1 GiB) per query; an `UNKNOWN` estimate is refused; a scan over `confirm_bytes` is refused instead of asked about.
- `agent_timeout` (default 60s) per query, up to 1,000 rows per call, cells cut at 200 characters.
- Flags and tool arguments can only lower these limits. Raising them takes the config file, which an agent that can edit your files can also do: give the Execution project read-only IAM for the real guarantee.
- Every job carries the labels `bqt=1` and `bqt_source=cli|mcp`, and lands in History with its source.

Search syntax: free terms match names (and descriptions last); filters are `p:` project, `d:` dataset, `type:`, `col:`, `label:`, `desc:`; `-d:tmp` excludes; `type:view,mview` is OR; a pasted `project.dataset.table` is an exact lookup. Columns are searched only with `col:`.

Output is JSON when stdout is not a terminal and text otherwise (`--format json|text`). Exit codes: 0 ok, 1 failure, 2 usage, 3 config, 4 auth, 5 over limit or refused by the policy, 6 BigQuery error (also: a sync with failed units), 7 cancelled.

The config file lives at `~/.config/bqt/config.toml` (or `$BQT_CONFIG`, or `--config`). Unknown keys are errors. See the comments in the file `bqt init` writes.

## Develop

```sh
go vet ./... && go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

Tests use hand-written fakes behind the BigQuery ports. An optional live suite runs read-only against a real project:

```sh
BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_PROJECT=... go test -tags live ./internal/bq -run Live -v
```

The live suite also runs a few queries that read no tables (`BQT_LIVE_LOCATION` sets their job location).

Scale measurements: `BQT_SCALE=500 go test ./internal/store -run Scale -v` (catalog apply) and `BQT_SCALE=1 go test ./internal/search -run LatencyReport -v` (search latency at 50k tables). Terminal screens are golden files in `internal/tui/testdata`; regenerate with `go test ./internal/tui -update` after checking the change.
