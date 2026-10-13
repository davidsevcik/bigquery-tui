# bqt v1 specification

Status: draft for build, assembled 2026-10-13 from the planning effort in `.scratch/bqt/` (map, tickets 01–18, research notes 01–04). Domain terms are defined in [`CONTEXT.md`](../../CONTEXT.md): **Favorite**, **History entry**, **Run**, **Execution project**. Use them exactly.

How to read this: phases 1 and 2 (the MVP) are specified sharply, with acceptance criteria. Phases 3 to 5 are specified more coarsely, but every risky decision in them is locked. Section 17 lists what is still unverified. Section 18 is the GUI-readiness check. Ticket references in brackets point to the decision record in `.scratch/bqt/issues/`.

## 1. What bqt is

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents. The author's setup: 5 GCP projects, 50 to 200 datasets each, datasets in any region, no Dataplex.

The pain points it solves: the web UI is slow; everything should work from the keyboard; search across hundreds of datasets and thousands of tables must be instant; queries need history and favorites; it must live in the terminal next to Claude Code and similar agents.

The core idea: keep a local, searchable index of metadata (projects, datasets, tables, columns) in SQLite with full-text search, so every keystroke hits local data and not the BigQuery API.

### Non-goals (out of scope for v1)

- Phase 6 polish: themes, keybinding remapping, Homebrew release, docs.
- Writing to BigQuery beyond running queries: creating or deleting tables, load or export jobs, scheduled queries, IAM.
- Dataplex or Data Catalog search. The tool never depends on it.
- Importing server-side `INFORMATION_SCHEMA.JOBS_BY_USER` into History (only the job label and key are reserved, section 6.3).
- Building or designing the GUI. Only the constraint that nothing blocks it is in scope (section 18).
- Packaging and support for other people's auth or organisation setups.
- Windows.

## 2. Stack and platforms

- **Language:** Go. The toolchain is pinned to the version the Bubble Tea v2 stack needs (Go 1.26 or newer).
- **TUI:** Bubble Tea v2 (`charm.land/bubbletea/v2`), Bubbles v2, Lip Gloss v2. Third-party editors on the v1 line are not usable.
- **Store:** SQLite with FTS5 through `modernc.org/sqlite` (pure Go, no cgo).
- **BigQuery:** `cloud.google.com/go/bigquery` for jobs, plus the generated REST service for `datasets.list` and `getQueryResults` paging.
- **MCP:** the official `github.com/modelcontextprotocol/go-sdk` over stdio.
- **Auth:** Application Default Credentials only (`gcloud auth application-default login`). `gcloud` is never read for anything but an optional init suggestion (section 4.4).
- **Platforms:** macOS and Linux, amd64 and arm64. [18]
- **Module:** `github.com/davidsevcik/bigquery-tui`, binary `bqt`. [14]

## 3. Architecture

```
TUI (Bubble Tea)  ──┐
JSON CLI          ──┤
MCP server        ──┼──> Engine (core, no UI imports) ──> BigQuery API
future GUI        ──┘        │
                             └──> catalog.db, history.db (SQLite) + Favorite .sql files
```

One Go module. All packages live under `internal/` for v1; a future GUI would be `cmd/bqt-gui` in the same module. [14]

| Group | Packages |
|---|---|
| Core (no UI imports) | `api` (plain data types with JSON tags, the shared contract), `config`, `store` (catalog and history), `sync`, `bq` (BigQuery ports and the real implementation), `search`, `sqlx` (tokenizer, completion, parameters), `runs`, `policy`, `favorites`, `export` |
| Frontends | `tui`, `cli`, `mcp`, `cmd/bqt` |

Rules:

- A test fails if any core package imports `charm.land/*` or a frontend package.
- One `Engine`, built from the config, with method groups `Catalog`, `Search`, `Sync`, `Runs`, `History`, `Favorites`, `Status`.
- Long operations (sync, run, result paging, export) return a handle plus a typed event channel, accept `context.Context` for cancellation, and emit plain-data events that serialize to JSON.
- BigQuery (metadata and query clients), the clock and the filesystem sit behind small interfaces so tests can fake them.
- Confirmations come back as results (for example `NeedsConfirmation`), never as prompts. The core never touches a TTY.
- Core types are plain data that serialize to JSON. The CLI and MCP contracts are these types. Core types contain no rendered strings (no ANSI).
- No daemon. The TUI, CLI and MCP are separate processes sharing the store through WAL. A GUI runs the same engine in-process.

Core versus frontend-only:

- Core: config and its watcher, store, sync and its events, search parsing and ranking, shard families, preview fetching, runs (dry-run, estimate, confirmation, errors with position, paging, cell formatting, record trees), export, History and Favorites operations and the parameter schema, tokenizer, highlight spans, completion, the safety policy, hand-off text formats.
- Frontend-only: layouts, panes, keys, focus and `esc` handling, the forked textarea and its popup, chips and colours, clipboard and file prompts, `$EDITOR` suspension, the remembered layout.

## 4. Configuration [05]

### 4.1 File

One TOML file at `$XDG_CONFIG_HOME/bqt/config.toml`, falling back to `~/.config/bqt/config.toml` on every platform. `--config <path>` and `BQT_CONFIG` select another file. There is no per-repo or walk-up override, so the working directory never changes behaviour.

### 4.2 Keys

| Key | Meaning | Default |
|---|---|---|
| `execution_project` | The Execution project: where jobs run, passed to `NewClient`. Required, with no fallback to `gcloud` or `GOOGLE_CLOUD_PROJECT`. | none |
| `pricing` | `on_demand` or `capacity`, describing the Execution project only. bqt does not detect it. | `on_demand` |
| `location` | Default job location for user queries only. Unset means BigQuery infers it from the referenced datasets, then US. Sync never uses it. | unset |
| `projects` | Explicit allowlist of data projects to sync. No discovery through Resource Manager. Empty means sync does nothing and the UI says how to add one. | `[]` |
| `ignore` | One ordered array of gitignore-style patterns (4.3). | `[]` |
| `data_dir` | Where the SQLite files, draft, state and logs live. | `$XDG_DATA_HOME/bqt` (`~/.local/share/bqt`) |
| `favorites_dir` | Where Favorite files live; can point into a git repo. | `<data_dir>/favorites` |
| `sync_ttl` | Age after which a unit is refreshed in the background. | `24h` |
| `confirm_bytes` | Soft threshold for a confirmation. | `10GiB` on-demand; off on capacity |
| `max_bytes_billed` | Hard cap set as `MaximumBytesBilled` on every job, under both pricing models. `off` disables it. | `1TiB` |
| `max_concurrent_queries` | Parallel Runs per TUI session. | `4` |
| `history_retention_days` | Delete History entries older than this. | off |
| `agent_max_bytes` | Byte limit for CLI and MCP queries (section 14.3). | `1GiB` |
| `agent_timeout` | Timeout per CLI or MCP query. | `60s` |
| `cli_allow_writes` | Allow non-SELECT statements from the CLI. | `false` |
| `[tui]` | TUI-only settings. A GUI would add `[gui]`. | |

`~` and `$VARS` expand in path values.

### 4.3 Ignore patterns

Globs over the dotted path `project.dataset.table`, matched up to table level.

- `*` stays inside one segment, `**` spans segments, `#` starts a comment, `!` negates, and the last matching line wins.
- A shorter pattern covers everything beneath it (`sandbox-proj` ignores the project, `prod.tmp_*` ignores datasets, `prod.events.events_2*` ignores sharded tables).
- There is no bare-name-matches-anywhere rule.

```toml
ignore = [
  "sandbox-proj",          # whole project
  "prod.tmp_*",            # datasets
  "prod.events.events_2*", # sharded tables
  "!prod.tmp_keep",
]
```

Ignore applies at sync: ignored objects never enter the store. Ignoring does not reduce query cost, since region queries return everything. The store records a hash of the effective `projects` + `ignore`. If it changed, bqt prunes locally with no BigQuery calls. If a pattern was removed or a project added, the UI shows "config changed, sync needed" and does not sync until the user asks.

### 4.4 Precedence, validation, first run

- Precedence: `--flag` > `BQT_*` environment variable > `config.toml`.
- Unknown keys and invalid patterns are hard errors naming the key and line (a typo like `ingore` must not silently sync everything).
- With no config file, commands exit with a one-line hint to run `bqt init`, and the TUI shows the same hint. `bqt init` writes a commented template, never overwrites an existing file, and may offer the `gcloud` project as a suggestion, written only after confirmation.
- Permissions needed on the Execution project: `bigquery.jobs.create` (sync queries run as jobs there) and `serviceusage.services.use`.

### 4.5 Editing and live reload (TUI)

- Commands: palette entry "Open config", `bqt config edit`, `bqt config path`. The editor is resolved as `$VISUAL`, then `$EDITOR`, then the OS opener (`open -t` or `xdg-open`). Terminal editors suspend the TUI through `tea.ExecProcess`; GUI editors do not block. With no config file, the template is written first.
- Mechanism: the TUI polls the file's mtime and content hash once per second and applies a change after 300 ms of stability. It also checks when an external editor exits and on terminal focus. No `fsnotify`. The core exposes `config.Watch(ctx) <-chan ConfigEvent` (new config, changed keys, live or restart-required).
- Live: `projects`, `ignore` (banner and local prune as above), `sync_ttl`, `history_retention_days`, `favorites_dir`, `location`, `pricing`, `confirm_bytes`, `max_bytes_billed`, `max_concurrent_queries`, `[tui]`, and `execution_project`. Run-related keys apply to new Runs only. A change of `execution_project` rebuilds the BigQuery client when no Run or sync is in flight; otherwise it waits with a status notice. The status line always shows the active Execution project.
- Restart required: `data_dir`.
- An invalid config on reload keeps the last good config, shows a persistent status-bar error naming key and line, lists it under "config problems" in the palette, never applies partially, and clears when the file is valid.
- CLI: reads the config on every invocation. `bqt mcp`: reads it at startup only, plus `projects` and `ignore` at each sync. `agent_*`, `pricing`, `execution_project` and `cli_allow_writes` are not hot-reloaded in `bqt mcp`.

## 5. Local files [06, 18]

In `data_dir`:

| File | Purpose | Lifecycle |
|---|---|---|
| `catalog.db` | Metadata index | Disposable; rebuilt by sync |
| `history.db` | History entries and `recent_objects` | Durable; the file that matters |
| `drafts/<frontend>/<buffer-id>.sql` | Autosaved editor buffers, one file per buffer, held under an advisory lock by the owning instance (`drafts/tui/`) | Local state |
| `state/<frontend>.json` | Frontend-only state such as the last layout; shared by instances, last writer wins, nothing critical | Local state |
| `bqt.lock` | Advisory lockfile for the single sync writer | Runtime |
| `logs/bqt.log` | Structured log, rotated at 5 MB, 3 files | Local state |

Favorites are plain `.sql` files in `favorites_dir` with no persistent index.

## 6. Store

### 6.1 Common

Both SQLite files use `modernc.org/sqlite`, WAL, `busy_timeout`, `synchronous=NORMAL`, and carry `PRAGMA user_version`.

### 6.2 Catalog

- Projects, datasets (location, description, friendly name, labels), tables (type, creation time, description, labels, expiration).
- Columns: one row per field path (from `COLUMN_FIELD_PATHS`) with a parent link, top-level flag, type, description, plus partition and clustering flags from `COLUMNS`. View SQL is stored only for `VIEW` and `MATERIALIZED VIEW`.
- Shard family per table: prefix, first, last, count (section 8.3).
- A content hash per table for the local diff. No `TABLE_STORAGE` in v1; size and row-count columns can be added later as nullable.
- Surrogate integer keys with unique constraints on `(project)`, `(project, dataset)` and `(dataset_id, table_name)`. Identifiers are stored exactly as returned plus a lowercase `name_lc` column for lookups and range scans. The hot path is `columns(table_id)`.
- Per-`(project, region)` sync state: last attempt, last success, status (`ok`, `degraded`, `inaccessible`), error text.
- A `meta` table: config hash, schema version, and a `last synced as` value if cheap. The catalog is not identity-aware; switching ADC accounts means deleting `catalog.db`.
- FTS5: one row per table with dataset and project names denormalized in, plus rows for datasets. A `trigram` index covers names (substring match, sub-millisecond in the research benchmark). Descriptions and column names use `unicode61` over text that bqt pre-splits (`order_items` is indexed as `order_items order items`) with prefix indexes. A column hit resolves to its table, then to the matching columns by a scoped lookup.
- Never design a lookup that needs a global distinct-column scan per keystroke (390 ms measured) or a sorted unscoped `LIKE` (18 ms).
- Versioning: no migrations. A version mismatch deletes and recreates the file and shows "sync needed". A corrupt file is recreated silently.

### 6.3 History

- One row per real Run. Dry-runs are not recorded. Result rows are never stored.
- Identity: unique `(project, location, job_id)` plus the Execution project.
- Content: SQL text, statement type, destination (anonymous) table.
- Outcome: state, error message, `total_bytes_processed`, `total_bytes_billed`, cache hit, creation, start and end time, duration, nullable `total_slot_ms`, and the `pricing` value the Run ran under.
- bqt-only: `source` (`tui`, `cli`, `mcp`), originating Favorite path (nullable, also written back on promotion), parameter names, types and values.
- An FTS5 table over the SQL text.
- `recent_objects`: path, kind, last opened, open count (feeds the empty-query search).
- Every job bqt submits is labelled `bqt=1`; CLI and MCP jobs also carry `bqt_source=cli|mcp`. Together with the `(project, location, job_id)` key, this lets a later `history import` from `JOBS_BY_USER` or `jobs.list` be idempotent and filter out foreign jobs. The import is not in v1.
- Versioning: embedded forward-only numbered migrations, run on open in a transaction after copying the file to `history.db.bak-v<N>` (newest kept). A file newer than the binary is refused. A corrupt file is renamed `history.db.corrupt-<timestamp>`, a fresh file is started, and the message states what was lost. No `quick_check` on startup, no other automatic backups.

### 6.4 Concurrency

- One sync writer, enforced across processes by the advisory lockfile. A second process that tries to sync reports "already running" and reads. Processes without the lock open the catalog read-only.
- Each `(project, region)` unit is applied in one transaction: swap the unit's rows, delete rows missing from the new snapshot. Readers see the old or new snapshot of a unit, never half. A failed unit keeps its old data and is marked stale. The explorer fills in progressively during a first sync.
- History inserts are single-row and allowed from any process.
- A process that is not the writer learns about catalog changes by polling `PRAGMA data_version` (or the sync state rows) once per second and emitting a `CatalogChanged` event, so a TUI beside a GUI or a running `bqt sync` refreshes.

## 7. Sync (phase 1) [01, 07]

### 7.1 Method

- Discover regions first: REST `datasets.list` per project, through the generated service (Go's `DatasetIterator` discards `location`), which also returns `unreachable[]` (surfaced as warnings).
- Apply `ignore` to datasets right after `datasets.list`.
- Group datasets by `(project, location)`. For each group, run region-qualified `INFORMATION_SCHEMA` queries (`` `proj`.`region-X`.INFORMATION_SCHEMA.* ``) as jobs on the Execution project with `Location` set explicitly: `TABLES`, `COLUMNS`, `COLUMN_FIELD_PATHS`, `TABLE_OPTIONS`, and the view definitions. About 4 to 6 queries per `(project, region)` unit, roughly 20 to 90 per full sync, against 4,000 or more per-dataset queries.
- Filter region results to the `datasets.list` set (deleted-in-time-travel and temp datasets can leak into region views). Apply `ignore` to tables locally.
- Corrections to the seed: `TABLES` has no last-modified time, `COLUMNS` has no descriptions (use `COLUMN_FIELD_PATHS` and `TABLE_OPTIONS`), regions must be found through REST, and there is no cheap delta signal. The baseline is a full re-snapshot per unit plus a local diff, deleting by absence.
- Compute shard families after each unit's table snapshot.
- Cost: `INFORMATION_SCHEMA` queries are uncached with a 10 MB on-demand minimum each, so a full sync is negligible against the free tier. The core sets `MaximumBytesBilled` (10 GiB) on every sync query as a safety net (enforced on reservation-backed projects too, section 17 check 1).

### 7.2 Behaviour

- **Triggers:** an empty catalog starts a full sync in the background at once; the explorer fills in as units commit. Later launches open instantly from the catalog, and a unit older than `sync_ttl` refreshes in the background. `r` refreshes the selected project, `R` everything, `bqt sync` from the CLI. The JSON CLI and MCP never auto-sync.
- **Failure ladder:** region-scope `INFORMATION_SCHEMA`, then per-dataset `INFORMATION_SCHEMA` for datasets that failed, then REST `tables.list` (names only, flagged "names only"). No per-table `tables.get` in v1. Transient errors (quota, 5xx, network) get 2 retries with backoff; permission errors are not retried. A failed unit keeps old data and is marked `degraded` or `inaccessible` with the error text.
- **Reporting:** failures never open a modal. They show as a warning glyph per project and in a "sync problems" list; `bqt sync` returns the same data as structured events.
- **Events:** unit started, datasets and tables counted, unit done or failed, run finished. The TUI shows `sync 7/23 units` in the status bar with a details overlay; `ctrl+c` or `esc` there cancels through `context`, committed units stay and in-flight units roll back. Four units run in parallel (a fixed internal cap).
- **Freshness:** "synced 3h ago" per project; a dataset is dimmed if its unit is stale or degraded.

## 8. Explorer and search (phase 2) [08, 09]

### 8.1 Layouts (TUI-only)

Three layouts: Miller columns (project | dataset | table; the default), tree, and flat list with a scoped filter. `L` cycles them and selection carries across. The last layout is remembered in `state/tui.json`. All lists are virtualized.

Detail panes (schema and preview): to the right at 100 columns or more, at the bottom from 70 to 99 columns, collapsed below 70 (where `Enter` zooms the detail and `esc` returns). The bottom pane defaults to hidden below 30 rows. The list keeps focus; `ctrl+e` and `ctrl+y` scroll the detail pane. `esc` always backs out exactly one level (zoom, then results, then filter). Search results are one line each, and the selected result expands to show its description and column hits. Footer hints must be readable (`↑↓←→/hjkl move`).

### 8.2 Search syntax and ranking (core)

- Free terms AND together and match project, dataset and table names, and (lowest tier) table and dataset descriptions. They never match column names.
- Filters are `key:value`: `p:` project, `d:` dataset, `type:` (`table`, `view`, `mview`, `external`, `snapshot`, `clone`), `col:`, `label:`, `desc:`.
- `col:` matches the field path and the column description, and only `col:` results carry `column_hits` (up to 3 per table plus a count). `desc:` matches table and dataset descriptions only.
- `-d:tmp` negates. Commas inside a filter mean OR (`type:view,mview`). A quoted value may contain spaces. A pasted `proj.dataset.table` or `` `proj.dataset.table` `` is an exact path lookup. Values are prefix or substring matches, not regex. An unknown key stays a free term.
- The parser returns a structured query with per-filter status (complete, pending, invalid), so a frontend can show chips.
- Ranking tiers, best first: exact name, name prefix, word-boundary (`oi` matches `order_items`), substring, fuzzy subsequence, description-only. A name hit always outranks a description hit. Ties: shorter name, then alphabetical. With no free term, results order by path (project, dataset, table). No usage boost in v1.
- An empty query shows recently opened tables from `recent_objects`; this is also what `ctrl+t` shows before typing.
- Candidates come from trigram FTS on names and prose FTS on descriptions and columns, then are scored in Go. Spans and column hits are computed only for visible rows.
- Result shape: `{kind, path, matched_field, tier, spans[], column_hits[≤3], more, freshness, stale, names_only, shard_family?}`. Spans are offsets for several fields at once (table, dataset, project, each column hit, description), never highlighted strings.
- Actions in the core: open schema, copy the qualified name, copy as backticked, fetch a preview, insert into the editor (the frontend decides how). Project and dataset results offer select only.
- Latency: p95 ≤ 30 ms per keystroke at 50k tables and 1.25M columns. Search runs off the UI thread and a new keystroke cancels the stale query. (The prototype's plain scan took 1 to 20 ms at 13k tables and extrapolates to 50 to 100 ms at target size, so the FTS prefilter is required.)
- Sync ignore patterns play no part in search.

### 8.3 Shard families (core)

Two or more tables in one dataset sharing a prefix and an 8-digit date suffix (`events_YYYYMMDD`) form a family, computed and stored at sync. It shows as one row, `events_YYYYMMDD ×2,303 (2022-01-01 → 2024-06-30)`, in all three layouts and in search, and `Enter` expands it. The schema pane shows the latest shard. Inserting a family into the editor inserts `` `proj.ds.events_*` `` with a `_TABLE_SUFFIX` hint.

### 8.4 Schema pane and preview

- Schema pane fields. Table: qualified name, kind, rows, bytes, partition and clustering, view SQL or external URI, labels, description, freshness and status with error text, field list (path, depth, type, description), "names only" notice. Dataset: location, table count by type, labels, description, status. Project: dataset and table counts, status and error, names-only count, Execution project note.
- Preview is an explicit fetch (`p`). The first press loads 20 rows through `tabledata.list` (free); each further press appends the next 20, up to 1,000, after which the pane says to run a query. Views and external tables show "needs a query". Cells use the results formatting (section 10.4); `J` opens the record view.

### 8.5 Navigation

Vim keys (`hjkl`, `gg`/`G`, `ctrl+d`/`ctrl+u`, `/`), the command palette (`ctrl+p`, or `:` in the explorer), and jump-to-table (`ctrl+t`). Full table in section 15.

### 8.6 MVP acceptance criteria (phases 1 and 2)

1. On a catalog of 5 projects with 50 to 200 datasets each, and on a single project with 1,300 datasets and 9,400 tables, a first sync completes with per-unit progress, can be cancelled, and leaves a usable partial catalog.
2. A permission failure on one dataset or project degrades that unit only, with its error visible, and never aborts the run.
3. After the first sync, `bqt` opens to a usable explorer without waiting for the network; a stale unit refreshes in the background.
4. Search at 50k tables and 1.25M columns meets the 30 ms p95 budget on the author's machine; the smoke guard in CI passes.
5. `p:`, `d:`, `type:`, `col:`, `label:`, `desc:`, negation, OR and quoted values behave as in 8.2, with table tests.
6. `config.toml` errors are reported with key and line; `bqt init`, `bqt config edit` and live reload work as in 4.
7. Deleting `catalog.db` and restarting recovers everything except History.
8. The import test passes (no UI packages in the core).

## 9. Editor and autocomplete (phase 3) [10, research 03]

### 9.1 Editing (TUI-only)

- Fork the Bubbles v2 `textarea` into the repo; add a per-line highlight hook and a snapshot undo stack. Modeless, no vim modes.
- **Several buffers per TUI session.** `ctrl+x n` opens a new buffer, `ctrl+x b` opens a searchable buffer list, `ctrl+x w` closes one, `ctrl+x [` and `ctrl+x ]` move between them. This is TUI-only; the core is unchanged because a Run snapshots its SQL.
- A Run belongs to the buffer that started it, and the buffer shows a status badge for its latest Run (running, done, failed). Switching buffers while Runs are in flight is fine; the run list stays global. Closing a buffer asks only if it has changes not saved to a file or a Favorite (autosaved drafts do not count).
- **Drafts:** each buffer autosaves to `<data_dir>/drafts/tui/<buffer-id>.sql` (random ID), held under an advisory lock by the owning instance, so several `bqt` instances (for example in tmux panes) never overwrite each other. On startup an instance restores only unlocked drafts (orphans from a crash or a closed pane), listed as "restore drafts" in the buffer list rather than all opened. Orphaned drafts older than 30 days are deleted at startup.
- `bqt path/to/query.sql` opens that file as a buffer and saves back to it.
- The `$EDITOR` hand-off stays as an escape hatch on `ctrl+x e` (`tea.ExecProcess` and a temp `.sql` file).

### 9.2 Language intelligence (core, pure functions)

- Own BigQuery tokenizer (about 200 lines; Chroma has no BigQuery lexer and mishandles backticks). `Tokenize(text) -> []Span` with kinds: keyword, string, number, comment, backtick identifier, function, `@param`. It is shared by highlighting and completion.
- Completion context comes from a token-stream scanner, not a parser, behind an interface. It never fails on broken input. `Complete(text, cursorOffset, catalog) -> []Candidate` over a small catalog interface backed by SQLite.
- Scope in v1: projects, datasets and tables (including inside backtick paths, segment by segment); columns for tables in `FROM` or `JOIN` with alias, `UNNEST` alias and struct-path awareness; CTEs complete as table names with columns from explicit select-list aliases or from the source table when the CTE selects `*`; functions and keywords from a static list with signatures.
- Out of v1: subquery column inference beyond the CTE rule, type-aware suggestions, `JOIN ... ON` suggestions, a SQL formatter (auto-indent on newline only), live dry-run while typing.
- `go-googlesql` (no AST on syntax errors, about 63 MB, about 76 s cold build) and `memefish` (Spanner dialect, fails on `QUALIFY`, `PIVOT`, `FOR SYSTEM_TIME`) are not v1 dependencies. Revisit only if CTE column accuracy hurts, after a parse-success spike over the real Favorites and History.
- Lookups stay scoped and prefix-friendly: 0.06 to 0.3 ms on the synthetic 50k-table index.

### 9.3 Popup (TUI-only)

Triggered after 2 typed characters in an identifier position, and immediately after `.` or a backtick; `ctrl+space` forces it. Tab accepts, Enter inserts a newline and never accepts, `ctrl+n`/`ctrl+p` and the arrows navigate, Esc dismisses. Floated with the Lip Gloss v2 `Layer`/`Compositor`.

## 10. Query runs and results (phase 3) [02, 11, 15]

### 10.1 Runs

- A **Run** snapshots SQL and parameters into an independent object with its own ID, state, dry-run estimate and result pager. Editing the buffer afterwards does not affect it.
- Runs queue beyond `max_concurrent_queries` (default 4) and show as queued. A run list (`ctrl+x l`, or `gr` outside the editor) shows running, done, failed and cancelled. A finished background Run raises a status-bar notice. Cancel targets the focused Run; "cancel all" is a palette command. Quitting with Runs in flight asks, then cancels.
- Core API: `StartRun(ctx, sql, params) -> RunHandle` with an event channel. Every submitted job gets the `bqt=1` label.
- Execution: `cloud.google.com/go/bigquery` jobs path (`Query.Run`, `Job.Wait`, `Job.Read`), the Execution project in `NewClient` (plus `option.WithQuotaProject`), `Location` from the `location` key or inferred. History fields come from `Job.Status` statistics, not `Query.Read`'s fast path. Do not use `DetectProjectID`.
- Parameters: named `@param` placeholders. Table and column names cannot be parameters.

### 10.2 Dry-run, confirmation, caps

- Every Run does a dry-run first; the estimate (bytes, approximate dollars, flagged if `LOWER_BOUND` or `UNKNOWN`) shows in the status line. `ctrl+d` runs the dry-run alone. Federated tables and row-level security can show 0, so accuracy matters.
- **Both models:** `max_bytes_billed` (default 1 TiB) is set as `MaximumBytesBilled` on every job for every frontend. BigQuery enforces it up front against the dry-run upper bound, including on reservation-backed projects (section 17), so a query can be refused even if its real scan would have been smaller. The core treats `bytesBilledLimitExceeded` as a normal refusal, shows the required bytes, and offers no override from the CLI or MCP.
- **On-demand:** above `confirm_bytes` (default 10 GiB) the core returns `NeedsConfirmation{estimate}` and the frontend asks. The status line shows an approximate dollar estimate. Cancel shows "cancel requested, may still be billed".
- **Capacity:** the dry-run still runs for validation, bytes and `statementType`, but the status shows "scanned (not billed)" with no dollar estimate. `confirm_bytes` is off by default, because bytes are not charged there (set it explicitly to confirm large scans). After the Run, `total_slot_ms` is recorded, and the `totalBytesBilled` BigQuery still reports is not shown as a charge. The "may still be billed" warning is not shown.
- The cap is set by one code path for both models; the only pricing-specific behaviour is presentation (dollar estimate, billed-bytes display, warnings) and the `confirm_bytes` default.
- Statements other than `SELECT` (by the dry-run's `statementType`) ask for confirmation in the TUI, naming the type. Multi-statement scripts run and the viewer shows the last statement's result.
- A per-job reservation override is out of v1.

### 10.3 Errors

The core parses BigQuery's `[line:col]` into `{message, line?, col?}` (position is optional: some errors, such as a missing table, carry none); when present the editor jumps to and marks it. `invalid_grant` and `invalid_rapt` show the `gcloud auth application-default login` hint; a missing ADC file shows the same hint. A cancelled Run is "cancel requested" until the job state confirms.

### 10.4 Results

- Paging through REST `getQueryResults` in pages of 500 rows, an LRU of about 10 pages, jump-to-row through `StartIndex`, page tokens for back. No row cap in the viewer (paging is server-side). Do not enable the Storage Read client for the interactive table. Results stay re-openable from a History entry for about 24 hours via `JobFromIDLocation`.
- Cell values are formatted by the core: NULL, grid truncation at about 200 characters (a parameter the frontend passes), JSON and STRUCT as compact JSON.
- Grid (TUI): column widths from the first page (min 4, max 40 characters), a cell cursor moved with `h`/`l` with whole-column scrolling, `f` freezes the first column, `H` hides the cursor's column for the session, headers with a type badge, NULL dim. Nested cells: STRUCT or JSON as compact one-line JSON with an ellipsis, ARRAY as `[3] first, second…`.
- Single-record view: `Enter` or `v` opens a popup; `n`/`p` move between records across page boundaries. The core returns a structured, indented JSON tree for the record with no truncation, columns in schema order, types preserved (INT64 and NUMERIC as exact strings, BYTES as base64, JSON columns as parsed indented JSON). Long and multi-line strings wrap. A cell over 1 MiB shows a notice and expands only on demand. Scrolling, JSON colour and collapse (`space`) are TUI-only.
- Cell actions: copy cell value, copy row as JSON, copy column values (current page), copy field path.
- No client-side sort or filter in v1. `/` finds text within the loaded pages and states how many pages are loaded.
- Export: CSV, JSON array and NDJSON. Clipboard copy is TSV for the visible page or selection. Export defaults to all rows, streams through paging to a file with progress, can be cancelled, and asks above 1M rows. Destination defaults to the working directory as `bqt-<timestamp>.<ext>` with an editable path prompt.

## 11. History and Favorites (phase 4) [12]

- **History:** every real Run is kept indefinitely (optional `history_retention_days`, and `bqt history prune`). Failed runs and cache hits are recorded. Search is FTS over the SQL, filterable by `status:`, `project:`, `source:` and `since:` using the same filter parser as section 8.2. Identical SQL (whitespace-normalised) is grouped, showing the latest Run, run count and bytes billed (slot time for capacity entries).
- **Favorite file:** `<favorites_dir>/**/name.sql`, subfolders allowed, the relative path is the ID. The header is leading `-- key: value` comment lines, so the file still runs as plain SQL:

  ```sql
  -- description: Daily active users by country
  -- tags: events, dau
  -- param: start DATE = 2024-01-01
  -- param: country STRING
  SELECT ... WHERE d >= @start AND c = @country
  ```

- **Parameters:** the body keeps `@name`; the header declares the type and optional default. v1 types: `STRING`, `INT64`, `FLOAT64`, `BOOL`, `DATE`, `DATETIME`, `TIMESTAMP`. Out of v1: array parameters and identifier substitution. The core exposes a parameter schema (name, type, default); the TUI prompts, the CLI takes `--param name=value`.
- **Operations:** promoting a History entry asks for a name, tags and description, writes the file, and writes the Favorite path back onto the entry. "Save buffer as Favorite" overwrites the file; rename is a file rename; delete asks for confirmation and has no trash (git is the safety net).
- **Picker (TUI-only):** one overlay with two scopes (Favorites, History) switched with Tab, a shared search box and a preview pane. Favorites search is in-memory fuzzy over name, tags, description and body. Opening a pick loads it into the buffer, after a confirmation if the buffer is dirty.
- Favorites stay unindexed; revisit past about 2,000 files.

## 12. TUI zones and keys [17]

See section 15 for the table. Principles: three zones (Explorer, Editor, Results) plus overlays; the editor is modeless so printable keys type there; Explorer and Results use vim-style plain keys; globals use `ctrl+` keys or the palette. One keymap registry generates the help overlay, the palette and the footer hints.

## 13. Platform, logging, diagnostics [18]

- macOS and Linux (amd64, arm64).
- Structured logs in `<data_dir>/logs/bqt.log` (5 MB, 3 files). Default level `info`; `BQT_LOG=debug` or `--log-level`. The TUI and interactive commands never log to the terminal; `bqt mcp` and the CLI may also log to stderr at `warn` and above. SQL text only at `debug`; credentials and tokens never.
- `bqt doctor` prints the config path, data dir, Go and bqt versions, auth status, catalog age and the log path.

## 14. Agent layer (phase 5) [04, 13]

### 14.1 JSON CLI

- Subcommands: `search <query>`, `schema <table>`, `query [--dry-run] [--file f.sql] [--param k=v]`, `history`, `favorites list|show|run`, `status`, `sync`, `init`, `config edit|path`, `doctor`, `mcp`.
- Every non-TUI command writes one JSON document to stdout (the core type serialized as-is). JSON is the default when stdout is not a TTY; `--format text` gives human tables, `--format json` forces JSON.
- Envelope: `{"schema_version":1,"data":…,"truncated":bool,"next_cursor":…}`. Changes within a version are additive only.
- Errors: JSON on stderr, `{"error":{"code","message","hint"}}`, with documented exit codes (usage, config, auth, over-limit, BigQuery error, cancelled).
- `--limit` defaults to 50, with a cursor. The CLI and MCP return the same result types.

### 14.2 MCP server

- `bqt mcp`, flagless, over stdio. Logs to stderr only. `bqt mcp --print-config` prints ready-to-paste snippets for Claude Code, Codex and Gemini CLI.
- Eight tools, thin wrappers over core types: `search_catalog`, `get_schema`, `catalog_status`, `dry_run`, `run_query`, `get_results`, `list_favorites`, `search_history`.
- Every list tool has `limit` (default 20), a continuation `cursor`, `truncated` and `total`, and a compact default view. `run_query` returns up to `limit` rows (default 100, hard max 1000), cells cut at 200 characters, plus a `result_id`; `get_results(result_id, cursor)` pages the rest from the stored job (about 24 hours). Tool descriptions and server instructions stay under 2,048 characters (Claude Code truncates there). No resources, prompts or elicitation in v1.
- No sync tool in v1, and CLI and MCP reads never take the sync lock. `catalog_status` reports staleness and tells the user to run `bqt sync` or open the TUI; `bqt sync` can run from cron. CLI and MCP Runs write History entries with `source = cli|mcp`, the only write the agent surface does. Agents cannot create or edit Favorites.

### 14.3 Safety model

The CLI is as reachable by an agent as MCP (Claude Code calls it through Bash), and bqt cannot tell an agent from the user at the command line. One policy therefore covers every non-TUI entry point: `bqt query`, `bqt favorites run` and `bqt mcp`.

- **Read-only classification:** the dry-run is the classifier. BigQuery returns its own `statementType` and the agent profile allows only `SELECT`. It fails closed: an unknown type, `SCRIPT`, `EXPORT DATA`, `CALL` or a failed dry-run is refused. Not blocked: a `SELECT` calling a remote function or UDF with side effects. Read-only IAM on the Execution project is the real backstop; the docs recommend it but bqt cannot enforce it.
- **Limits:** `agent_max_bytes` (default 1 GiB) is checked on the dry-run bytes, for both pricing models; over the limit the query is refused with the estimate and the limit. `UNKNOWN` accuracy is refused. `LOWER_BOUND` is checked against the lower bound, with `max_bytes_billed` as the server-side backstop, under both pricing models. `agent_timeout` (default 60 s) per query, plus the row and cell caps above.
- **Who can change limits:** flags and tool arguments can only lower a limit; raising one needs the config file. An over-limit query is a refusal, not a prompt: run it in the TUI or raise the config.
- **Writes:** DML, DDL and scripts run only in the TUI (with confirmation), or from the CLI if `cli_allow_writes = true`. There is no flag override, and no `--yes`.
- **Audit:** `source` on the History entry plus the `bqt=1` and `bqt_source` job labels.
- **Not defended:** an agent that can edit your config file can raise the limits. This is documented.
- The policy and limits live in the core `policy` package, so every frontend gets them.

### 14.4 TUI hand-off actions

Palette commands (plus keys for the common ones): copy schema (a compact markdown table of path, type and description with nested fields as dotted paths, or JSON in the same shape as `bqt schema`); copy query plus error (SQL, message with `line:col`, Execution project, location); copy a result sample (first 20 rows as markdown, TSV or NDJSON); copy the active Run (SQL, estimate, state, outcome). Targets: the clipboard (OSC 52, so it works over SSH and tmux) or a file with an editable path prompt (default `./.bqt-handoff/<name>.md`). The core renders the plain-text formats; the frontend only handles the clipboard and the prompt.

## 15. Keybindings [08, 10, 11, 15, 17]

Zones: Explorer, Editor, Results, overlays. Zone switching: `F6` / `shift+F6` cycle zones; `ctrl+w` then `h`/`j`/`k`/`l` moves directionally.

| Zone | Key | Action |
|---|---|---|
| Global | `ctrl+q` | Quit (asks if Runs are in flight) |
| Global | `ctrl+p` | Command palette (unless the completion popup is open) |
| Global | `ctrl+t` | Jump to table |
| Global | `ctrl+c` | Cancel focused Run; with nothing running, shows a "ctrl+q to quit" hint |
| Global | `F1`, `?` (outside editor) | Help overlay |
| Explorer | `h` `j` `k` `l`, arrows | Move, collapse/expand, enter column |
| Explorer | `gg`, `G`, `ctrl+d`, `ctrl+u` | Top, bottom, half page |
| Explorer | `/` | Search; `:` opens the palette |
| Explorer | `L`, `P` | Cycle layout; cycle pane placement |
| Explorer | `p`, `J` | Fetch more preview rows; record JSON |
| Explorer | `ctrl+e`, `ctrl+y` | Scroll detail pane |
| Explorer | `r`, `R`, `gp` | Refresh project, refresh all, sync problems |
| Explorer | `Enter` | Expand shard family; zoom detail if collapsed |
| Explorer | `esc` | Back out one level |
| Editor | printable keys | Type |
| Editor | `ctrl+r` | Run selection, or the whole buffer |
| Editor | `ctrl+d` | Dry-run |
| Editor | `ctrl+space` | Force completion |
| Editor | Tab, `ctrl+n`/`ctrl+p`, arrows, Esc | Popup accept, navigate, dismiss |
| Editor | `ctrl+x e` | `$EDITOR` hand-off |
| Editor | `ctrl+x l` | Run list |
| Editor | `ctrl+x n`, `ctrl+x b`, `ctrl+x w`, `ctrl+x [`, `ctrl+x ]` | New buffer, buffer list, close buffer, previous and next buffer |
| Results | `h` `j` `k` `l` | Move the cell cursor |
| Results | `Enter`, `v` | Single-record view; `n`/`p` next and previous record |
| Results | `f`, `H`, `/` | Freeze first column, hide column, find in loaded pages |
| Results | `gr` | Run list |

`ctrl+a` and `ctrl+b` stay with the textarea; move them if the user's tmux prefix is one of them. The registry is checked by a test that finds duplicate bindings within a zone.

## 16. Testing and CI [16]

- **Fakes first:** hand-written fakes behind the `bq` ports model region grouping, per-project and per-dataset permission failures, transient errors, dry-run results (`statementType`, accuracy flags), paging, cancellation, job errors with `line:col`, and both pricing models. The BigQuery emulator is not used in CI (beta; region-qualified `INFORMATION_SCHEMA`, jobs paging and dry-run unconfirmed).
- **Live suite:** `//go:build live`, run manually against a scratch project, selected by an env var. It covers the checks in section 17 and re-runs the verified ones.
- **Contract suites:** each port's behaviour tests are written once and run against the fake in every build and against the real implementation under `live`, so a fake that drifts fails the live run.
- **TUI:** golden-file snapshots of `View()` at 120x36, 80x24 and 60x20, ANSI-stripped, with a deterministic fake engine and clock and an `-update` flag; key-sequence tests without a PTY; one PTY smoke test. Colours and OSC 52 are checked by hand.
- **Store and performance:** real SQLite in temp directories; a generator for 50k tables and 1.25M columns; manual benchmarks of search (budget p95 ≤ 30 ms), completion lookups and sync commit time; one CI smoke guard at about 5x the budget.
- **Pure logic:** table tests for the filter parser and ranker, completion with cursor markers, the Favorite header and parameter schema, the safety policy over every statement type and accuracy flag, cell formatting and record trees; fuzz tests for the tokenizer (never panics, spans cover the text) and filter parser; MCP over the SDK's in-memory transport; CLI golden JSON; the import test; the keymap duplicate test.
- **CI:** GitHub Actions on push: `go vet`, `staticcheck`, `go test -race ./...` on Go 1.26. No coverage gate, no live tests.

## 17. Live checks

Run on 2026-10-13 against a real EU dev project (about 1,280 datasets and 9,400 tables, jobs running on a Standard edition reservation), using dry-runs and one tiny test table that was dropped afterwards. They are the seed for the `live` suite (section 16).

Verified:

1. **`MaximumBytesBilled` is enforced on a reservation-backed project.** A job on a Standard edition reservation with a 1 MB cap was rejected up front (`reason: bytesBilledLimitExceeded`, "Query exceeded limit for bytes billed: 1000000. 336592896 or higher required.") with no slots used. The check compares the cap against the dry-run upper bound, not the final processed bytes. Seen on one edition only (Standard); re-check on Enterprise if it matters. This is what lets the capacity and on-demand models share one cap code path (10.2, 14.3). **On-demand enforcement was not tested directly**: the test project's default configuration does not allow overriding the reservation (`reservation_override_mode` unset), so no job could be forced on-demand. On-demand behaviour rests on BigQuery's documented guidance for the setting; the `live` suite must run the same cap test once against a project without a reservation. Finished jobs on the reservation still report non-zero `totalBytesBilled` (111 MB for 110 MB processed) alongside `totalSlotMs` and `statistics.reservation_id`, so the UI must not present those bytes as a charge under `pricing = capacity`.
2. **Dry-run exposes `statementType`** for every kind tried: `SELECT` (also for `WITH … SELECT` and `INFORMATION_SCHEMA` queries), `CREATE_TABLE_AS_SELECT`, `DROP_TABLE`, `DROP_SCHEMA`, `EXPORT_DATA` and `SCRIPT`. Accuracy was `PRECISE` for all. The read-only classifier in 14.3 stands. `CALL` was not tried (no procedure available); the fail-closed rule covers it.
3. **Dry-run bytes for `INFORMATION_SCHEMA`:** every region-scope view tried (`TABLES`, `COLUMNS`, `COLUMN_FIELD_PATHS`) and the dataset-scope `TABLES` reports 10,485,760 bytes, the 10 MiB minimum, as the research expected.
4. **`lastModifiedTime` moves on a metadata-only edit.** Changing a table's description changed the table's `lastModifiedTime`; the dataset's did not move. A per-table delta signal therefore exists through `tables.get` or the legacy `__TABLES__` view, but `INFORMATION_SCHEMA.TABLES` does not expose it, so the full re-snapshot baseline in section 7 stands. Whether `__TABLES__` is a cheaper signal at scale was not measured.
5. **Error positions are not always present.** A syntax error carries `at [1:1]`; a "table not found" error did not carry a position. The `{message, line, col}` parse in 10.3 must treat position as optional.
6. **Scale.** One project here has about 1,280 datasets, well above the 50 to 200 assumed, and `datasets.list` pages correctly at `maxResults=1000`. All tables sit in one region (EU). Table types seen: `BASE TABLE`, `VIEW`, `SNAPSHOT` and `CLONE`; `CLONE` is added to the `type:` filter (8.2).

Not verified. Items 7 and 8 are not needed for the author's own use and are **blockers before any public release**; resolve them if the tool is ever released:

7. `tabledata.list` on a materialized view: the test project is on a Standard edition reservation, which does not allow materialized views ("Materialized views cannot be created in a project with Standard edition reservation"). `tabledata.list` on an ordinary table works. Needs a project with an MV.
8. Region-view behaviour and error text with dataset-only grants (needs a restricted identity).
9. OSC 52 clipboard and colours in the author's real terminal and tmux.
10. Whether the `query` column in `JOBS_BY_USER` truncates long statements (only for the deferred History import).
11. Optional spike: run `memefish` or `go-googlesql` over the author's Favorites and History to measure parse success, if CTE column accuracy matters.

## 18. GUI-readiness check

The requirement: a GUI may be added beside the TUI later. It is not built or designed here, but no decision may block it. The check below walks the map's rules against the decisions.

| Rule | Verdict | Notes |
|---|---|---|
| Core imports nothing from Bubble Tea, Bubbles or Lip Gloss and returns no rendered strings | Holds | Enforced by the import test (section 3). Hand-off formats are plain text, not ANSI. |
| Core types are plain JSON-serializable data; the JSON CLI types double as GUI wire types | Holds | Section 14.1 makes the CLI contract the serialization of `api` types. |
| Long operations expose progress, state and cancellation through `context` and structured events | Holds | Sync, Runs, paging and export all use a handle plus an event channel. |
| Editor intelligence is in the core (`Tokenize`, `Complete`) | Holds | Only the textarea fork and popup are TUI-specific. |
| Search, explorer and results behaviour (filter parsing, ranking, shapes, cell formatting, paging) are core | Holds | Layouts, keys and focus are frontend-only; shard families and preview paging are core. |
| Config, store and Favorites contain no TUI-only assumptions; TUI settings in `[tui]` | Holds | Plus `state/<frontend>.json` for remembered layout. |
| Store allows several processes at once | Holds, with one gap | WAL and the sync lockfile work; section 6.4 adds a `CatalogChanged` poll so a second process notices another's sync. |
| No interactive prompts in the core; no terminal-only hand-offs as the only path | Holds | Confirmations are `NeedsConfirmation` results. `$EDITOR` is an escape hatch and the in-TUI editor is the default. |

Adjustments the check forced (already reflected above, listed so they are not lost):

1. **Drafts per frontend and per buffer.** An early draft used a single `draft.sql`; with a TUI and a GUI side by side, or two TUI instances in tmux panes, they would overwrite each other. Drafts are now `drafts/<frontend>/<buffer-id>.sql`, locked by the owning instance, and orphans are offered for restore (section 9.1).
2. **Catalog change signal.** The decisions said readers see "old or new snapshot of a unit" but not how a second process learns of a change. Section 6.4 adds the `PRAGMA data_version` poll and a `CatalogChanged` event.
3. **Offset units.** Spans and cursor offsets (`Complete`, highlight spans, search spans) are UTF-8 byte offsets in the `api` types. A JavaScript-based GUI shell counts UTF-16 units, so conversion happens in the frontend; the contract must say "bytes".
4. **Truncation as a parameter.** The 200-character grid truncation is a request parameter to the core's formatting, not a constant baked into the stored result, so a GUI can choose another width and the full-value record view needs no second fetch path.
5. **Runs are per process.** A Run list is not shared across processes in v1: a Run started in the TUI is invisible to a GUI beside it until it completes and appears in History. Acceptable for v1; the spec says so rather than leaving it implied.
6. **Config editing is a path, not a command.** `config.Path()` is core; "open in the default editor" is a frontend action (the TUI suspends, a GUI uses the OS opener).
7. **Clipboard.** OSC 52 is TUI-only; hand-off text formats are core so a GUI uses its native clipboard with the same content.

No decision needs to be reversed. Remaining GUI work is design, not rework: toolkit choice, screens and packaging.

## 19. Delivery order

1. **Foundation (phase 1):** module skeleton and import test, `config` (keys, validation, `bqt init`), `store` (both files, versioning, lockfile), `bq` ports with fakes, `sync` with events and the failure ladder, `bqt sync` and `bqt status`, logging, `doctor`.
2. **Explorer and search (phase 2):** `search` (parser, ranking, FTS, shard families), the TUI shell with the registry, three layouts, panes, preview, palette, jump, live config reload. Meets the MVP acceptance criteria in 8.6.
3. **Query run (phase 3):** `sqlx` tokenizer and completion, the forked textarea, `runs` with dry-run, caps and parallel Runs, the results grid and record view, export.
4. **History and Favorites (phase 4):** History store and search, Favorite files and parameters, promotion, the picker.
5. **Agent layer (phase 5):** `policy`, the JSON CLI contract, the MCP server, hand-off actions.

Each phase keeps the build green with the fakes; the live suite runs before a phase is declared done.

## 20. Decision index

| Topic | Ticket |
|---|---|
| Metadata enumeration research | `issues/01-research-metadata-enumeration.md` |
| Query execution research | `issues/02-research-query-execution.md` |
| Editor and SQL tooling research | `issues/03-research-editor-and-sql-tooling.md` |
| MCP Go SDK research | `issues/04-research-mcp-go-sdk.md` |
| Config and ignore patterns (and reload) | `issues/05-config-and-ignore-file.md` |
| Local store layout | `issues/06-local-store-layout.md` |
| Sync strategy | `issues/07-sync-strategy.md` |
| Explorer UX prototype | `issues/08-explorer-ux-prototype.md` |
| Search syntax and ranking | `issues/09-search-syntax-and-ranking.md` |
| Editor and autocomplete | `issues/10-editor-and-autocomplete.md` |
| Query run and results | `issues/11-query-run-and-results.md` |
| History and Favorites | `issues/12-history-and-favorites.md` |
| Agent surface and safety | `issues/13-agent-surface-and-safety.md` |
| Core library boundary | `issues/14-core-library-boundary.md` |
| Results viewer details | `issues/15-results-viewer-details.md` |
| Testing strategy | `issues/16-testing-strategy.md` |
| Keybindings and help | `issues/17-keybindings-and-help.md` |
| Platform and diagnostics | `issues/18-platform-and-diagnostics.md` |

Research notes with measurements and citations are in `.scratch/bqt/research/`. The throwaway explorer prototype is in `.scratch/bqt/prototype/explorer/`. Both are gitignored; this spec is self-contained for the decisions, and the tickets are the place for the reasoning.
