# bqt v1 specification

Status: draft for build, assembled 2026-10-13 from the planning effort in `.scratch/bqt/` (map, tickets 01–18, research notes 01–04). Domain terms are defined in [`CONTEXT.md`](../../CONTEXT.md): **Favorite**, **History entry**, **Run**, **Execution project**, **Accessible project**, **Selected project**, **Cached project**. Use them exactly.

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
- **Store:** SQLite through `modernc.org/sqlite` (pure Go, no cgo). FTS5 is used for History; catalog search runs on an in-memory index (section 8.2).
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
| Core (no UI imports) | `api` (plain data types with JSON tags, the shared contract), `config`, `store` (catalog and history), `sync`, `bq` (BigQuery ports and the real implementation; `bq/bqtest` holds the fake, the contract suite and the synthetic catalog generator), `engine` (the facade below), `logging`, `version`, `search` (query parser, ranking, in-memory index, browse lists), `format` (cell formatting), `sqlx` (tokenizer, completion, parameters), `runs`, `policy`, `favorites`, `export`, `handoff` (the hand-off text formats) |
| Frontends | `tui`, `cli`, `mcp`, `cmd/bqt` |
| Tests | `archtest` (the import-boundary test), `smoke` (starts the real binary in a pseudo-terminal) |

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

The working set is not configured here (ADR 0001): the Selected projects and the Execution project are chosen in the Projects dialog (`ctrl+x p`, section 8.1) or with `bqt projects`, and kept in `state/projects.json`. The keys `projects` and `execution_project` are errors that point there. `--execution-project` and `BQT_EXECUTION_PROJECT` override the Execution project for one invocation (flag over environment over the chosen project over the earliest-selected one).

| Key | Meaning | Default |
|---|---|---|
| `location` | Default job location for user queries only. Unset means BigQuery infers it from the referenced datasets, then US. Sync never uses it. | unset |
| `ignore` | One ordered array of gitignore-style patterns (4.3). | `[]` |
| `data_dir` | Where the SQLite files, draft, state and logs live. | `$XDG_DATA_HOME/bqt` (`~/.local/share/bqt`) |
| `favorites_dir` | Where Favorite files live; can point into a git repo. | `<data_dir>/favorites` |
| `sync_ttl` | Age after which a unit is refreshed in the background. | `24h` |
| `confirm_bytes` | Soft threshold for a confirmation. `auto` means 10 GiB when the Run's dry-run shows no reservation and off when it shows one (bytes are not charged on a reservation). | `auto` |
| `max_bytes_billed` | Hard cap set as `MaximumBytesBilled` on every job, under both pricing models. `off` disables it. | `1TiB` |
| `max_concurrent_queries` | Parallel Runs per TUI session. | `4` |
| `history_retention_days` | Delete History entries older than this. | off |
| `agent_max_bytes` | Byte limit for CLI and MCP queries (section 14.3). | `1GiB` |
| `agent_timeout` | Timeout per CLI or MCP query. | `60s` |
| `cli_allow_writes` | Allow non-SELECT statements from the CLI. | `false` |
| `[tui]` | TUI-only settings. A GUI would add `[gui]`. Keys: `editing` = `"vim"` or `"plain"` (the query editor's editing style). Unknown keys are errors. | `editing = "vim"` |

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

Ignore applies at sync: ignored objects never enter the store. Ignoring does not reduce query cost, since region queries return everything. The store records a hash of the effective `ignore`. If it changed, bqt prunes locally with no BigQuery calls. If a pattern was removed, the UI shows "config changed, sync needed" and does not sync until the user asks. Selection never prunes: a deselected project keeps its cache.

### 4.4 Precedence, validation, first run

- Precedence: `--flag` > `BQT_*` environment variable > `config.toml`.
- Unknown keys and invalid patterns are hard errors naming the key and line (a typo like `ingore` must not silently sync everything).
- With no config file, commands exit with a one-line hint to run `bqt init`, and the TUI shows the same hint. `bqt init` writes a commented template, never overwrites an existing file, and points at the Projects dialog and `bqt projects`.
- With nothing selected, the TUI opens the Projects dialog by itself; the first-run tutorial hint follows when it closes.
- Permissions needed on the Execution project: `bigquery.jobs.create` (sync queries run as jobs there) and `serviceusage.services.use`. Listing projects needs no Execution project.

### 4.5 Editing and live reload (TUI)

- Commands: palette entry "Open config", `bqt config edit`, `bqt config path`. The editor is resolved as `$VISUAL`, then `$EDITOR`, then the OS opener (`open -t` or `xdg-open`). Terminal editors suspend the TUI through `tea.ExecProcess`; GUI editors do not block. With no config file, the template is written first.
- Mechanism: the TUI polls the file's mtime and content hash once per second and applies a change after 300 ms of stability. It also checks when an external editor exits and on terminal focus. No `fsnotify`. The core exposes `config.Watch(ctx) <-chan ConfigEvent` (new config, changed keys, live or restart-required).
- Live: `ignore` (banner and local prune as above), `sync_ttl`, `history_retention_days`, `favorites_dir`, `location`, `confirm_bytes`, `max_bytes_billed`, `max_concurrent_queries` and `[tui]`. Run-related keys apply to new Runs only. A change of the Execution project (in the dialog, or by `bqt projects` in another process, noticed within a second) applies to new Runs and syncs. Each Execution project has its own BigQuery client, so a Run in flight finishes in the project it started in (no waiting is needed); the pricing model is relearned, and a dry-run of the previous project no longer updates it. Listing projects uses a client with no Execution project (the credentials' own quota project). The status line always shows the active Execution project.
- Restart required: `data_dir`.
- An invalid config on reload keeps the last good config, shows a persistent status-bar error naming key and line, lists it under "config problems" in the palette, never applies partially, and clears when the file is valid.
- CLI: reads the config and the selection on every invocation. `bqt mcp`: reads the config and the Execution project at startup only, plus the selection and `ignore` at each sync. `agent_*`, the Execution project and `cli_allow_writes` are not hot-reloaded in `bqt mcp`.

## 5. Local files [06, 18]

In `data_dir`:

| File | Purpose | Lifecycle |
|---|---|---|
| `catalog.db` | Metadata index | Disposable; rebuilt by sync |
| `history.db` | History entries and `recent_objects` | Durable; the file that matters |
| `drafts/<frontend>/<buffer-id>.sql` | Autosaved editor buffers, one file per buffer, held under an advisory lock by the owning instance (`drafts/tui/`) | Local state |
| `state/<frontend>.json` | Frontend-only state such as the last layout; shared by instances, last writer wins, nothing critical | Local state |
| `state/projects.json` | The Selected projects (with when each was selected) and the chosen Execution project; shared by every frontend and process, written atomically, last writer wins | Durable user choice |
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
- No full-text index in the catalog. Search loads projects, datasets and tables (not columns) into memory and ranks them there; the `col:` filter scans an in-memory blob of column paths and descriptions built on first use (section 8.2). A trigram FTS5 index over columns was built and measured: it made the first apply of 1.3M columns ten times slower (14 s to 146 s), so it was dropped. `columns(parent_id)` must stay indexed (section 17 item 13).
- Never design a lookup that needs a global distinct-column scan per keystroke (390 ms measured) or a sorted unscoped `LIKE` (18 ms).
- Versioning: no migrations. A version mismatch deletes and recreates the file and shows "sync needed". A corrupt file is recreated silently.

### 6.3 History

- One row per real Run. Dry-runs are not recorded. Result rows are never stored.
- Identity: unique `(project, location, job_id)` plus the Execution project.
- Content: SQL text, statement type, destination (anonymous) table.
- Outcome: state, error message, `total_bytes_processed`, `total_bytes_billed`, cache hit, creation, start and end time, duration, nullable `total_slot_ms`, and the observed `reservation_id` and edition (both null for an on-demand Run) from the job statistics.
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
- Group datasets by `(project, location)`. For each group, run region-qualified `INFORMATION_SCHEMA` queries (`` `proj`.`region-X`.INFORMATION_SCHEMA.* ``) as jobs on the Execution project with `Location` set explicitly: `SCHEMATA_OPTIONS` (dataset descriptions, friendly names and labels; region scope only, so the dataset-scope fallback has no dataset descriptions), `TABLES`, `TABLE_OPTIONS`, `VIEWS` (view SQL; materialized-view definitions are not captured yet), `COLUMNS` and `COLUMN_FIELD_PATHS`. That is 6 queries per `(project, region)` unit, run concurrently within the unit, roughly 20 to 90 per full sync, against 4,000 or more per-dataset queries. Large regions are dominated by BigQuery's own execution time (see section 17 item 13).
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

`/` filters the list in place in every layout; it never opens a separate view. In the Miller columns it filters the focused column: each column has its own filter, shown in its title with the matched count (`Tables 2/3 of 4 ▸ ord`), and moving left clears the filter of the column left behind. In the tree it filters the whole tree: matches are shown with their ancestors, which open by themselves; a project or dataset found by its own name opens to all its children; clearing the filter gives back the rows the user had opened. The flat list is the ranked, catalog-wide filter. Typing narrows the list on every keystroke and moves the cursor to the best match; `enter` or `esc` goes back to the list with the filter kept, and `esc` there clears it. Each layout keeps its own filter for the session; jumping to an object (or carrying the selection across `L`) clears a filter that would hide it. Jump-to-table (`ctrl+t`) is the quick search.

The Projects dialog (`ctrl+x p`; vim Normal: `space p`, or the palette's "Projects") chooses the Selected projects and the Execution project (ADR 0001). It lists the union of the projects BigQuery's projects list returns for the credentials (fetched on first open, kept for the session, `r` fetches again), the Selected ones and the cached ones: Selected first, then the rest, each A–Z; rows keep their place while the dialog is open. The header names the Execution project and why (`chosen`, `first selected`, `--execution-project`, `BQT_EXECUTION_PROJECT`); rows show the ID, the friendly name and, for cached projects, the dataset count and the time since the last sync, or `syncing…`, `not synced yet`, `sync failed`, `not listed` (added by ID), `not accessible` (a permission error). `space` selects or deselects, `o` selects only this one, `e` makes it the Execution project (any listed project; a chosen one can be replaced, not cleared), `s` syncs the project under the cursor now, even when its cache is fresh (a deselected one only when cached), `S` syncs every Selected project now, `/` filters by ID and name and offers "add <id>" for a valid ID that matches nothing, `esc`/`enter` close. A list longer than the dialog scrolls under a fixed header; on a short terminal the header leaves out the key hint, keeping the selection count and the filter. Every key applies at once; the last Selected project cannot be deselected. Closing starts one sync of the projects selected in that visit that have no cache or one older than `sync_ttl`. Only Selected projects are synced and shown in the explorer, search, jump-to-table and SQL completion (completion still offers names inside the Execution project, where unqualified `dataset.table` resolves, and the columns of a table the query names); a deselected project keeps its cache (the CLI and MCP still read it, marked `selected: false`, ranked last, with a warning when stale). A pasted `project.dataset.table` in a deselected project selects it (syncing it when stale) and opens it; in a project the catalog does not hold at all, it selects the project and opens the path once its sync finishes. Projects selected while another sync runs sync right after it. With a single project selected, the Miller layout leaves out the project column and starts at the datasets; the status line names the project (or "N projects"). The TUI notices a selection changed by another process within a second.

Detail panes (schema and preview): to the right at 100 columns or more, at the bottom from 70 to 99 columns, collapsed below 70 (where `Enter` zooms the detail and `esc` returns). The bottom pane defaults to hidden below 30 rows. The list keeps focus; `ctrl+e` and `ctrl+y` scroll the detail pane. `esc` always backs out exactly one level (zoom, then results, then filter). `z` zooms the detail to the full screen. While zoomed, the movement keys act on the detail (scroll the schema, move the preview row cursor), left or `esc` leaves the zoom, and `P` switches schema and preview between side by side and stacked (remembered in the state file). Search results are one line each, and the selected result expands to show its description and column hits. Footer hints must be readable (`↑↓←→/hjkl move`).

### 8.2 Search syntax and ranking (core)

- Free terms AND together and match project, dataset and table names, and (lowest tier) table and dataset descriptions. They never match column names.
- Filters are `key:value`: `p:` project, `d:` dataset, `type:` (`table`, `view`, `mview`, `external`, `snapshot`, `clone`), `col:`, `label:`, `desc:`.
- `col:` matches the field path and the column description, and only `col:` results carry `column_hits` (up to 3 per table plus a count). `desc:` matches table and dataset descriptions only.
- `-d:tmp` negates. Commas inside a filter mean OR (`type:view,mview`). A quoted value may contain spaces. A pasted `proj.dataset.table` or `` `proj.dataset.table` `` is an exact path lookup. Values are prefix or substring matches, not regex. An unknown key stays a free term.
- The parser returns a structured query with per-filter status (complete, pending, invalid), so a frontend can show chips.
- Ranking tiers, best first: exact name, name prefix, word-boundary (`oi` matches `order_items`), substring, fuzzy subsequence, description-only. A hit on the table's own name always outranks a hit that comes only from its labels, dataset name or project name (otherwise an exact dataset name would lift every table in that dataset above real name matches), and any of those outranks a description-only hit. Ties: shorter name, then path. With no free term, results order by path (project, dataset, table). No usage boost in v1.
- An empty query shows recently opened tables from `recent_objects`; this is also what `ctrl+t` shows before typing.
- Implementation: an in-memory index of every project, dataset and table (names, descriptions, labels, shard family) is scored in Go in parallel chunks; dataset- and project-level scores are computed once per term and dataset. The index is rebuilt when the catalog changes (a `PRAGMA data_version` poll on a pinned connection, which also sees other processes) and at the end of the process's own sync. The `col:` filter scans one in-memory string of all lower-cased `path\0description` records. Spans and column hits are computed only for the returned page.
- Result shape: `{kind, path, matched_field, tier, spans[], column_hits[≤3], more, freshness, stale, names_only, shard_family?}`. Spans are offsets for several fields at once (table, dataset, project, each column hit, description), never highlighted strings.
- The Explorer filter (Miller columns and tree) uses the same syntax with one difference: free terms match only an object's own name, never descriptions and never through a parent, because a filter that kept every table of a dataset whose name matched would not narrow the list. Table filters (`type:`, `col:`, `label:`, `desc:`) keep a table that passes them and a project or dataset that holds one; `p:` and `d:` narrow by path; a shard family passes when one of its members does. The core's filter query returns every matching path in rank order, without spans or paging, within the same latency budget.
- Actions in the core: open schema, copy the qualified name, copy as backticked, fetch a preview, insert into the editor (the frontend decides how). Project and dataset results offer select only.
- Latency: p95 ≤ 30 ms per keystroke at 50k tables and 1.25M columns. Search runs off the UI thread and a new keystroke cancels the stale query. Measured on the synthetic 50k-table catalog: name and filter queries p50 6 ms, p95 23 ms; `col:` queries p50 20 ms, p95 66 ms once the column data is in memory (about 1 s and 23 MB to build for 1.3M field paths). On the real 9,500-table catalog queries take 1 to 4 ms. A CI smoke guard fails above 150 ms.
- Sync ignore patterns play no part in search.

### 8.3 Shard families (core)

Two or more tables in one dataset sharing a prefix and an 8-digit date suffix (`events_YYYYMMDD`) form a family, computed and stored at sync. It shows as one row, `events_YYYYMMDD ×2,303 (2022-01-01 → 2024-06-30)`, in all three layouts and in search, and `Enter` expands it. The schema pane shows the latest shard. Inserting a family into the editor inserts `` `proj.ds.events_*` `` with a `_TABLE_SUFFIX` hint.

### 8.4 Schema pane and preview

- Schema pane fields. Table: qualified name, kind, location, rows and bytes (fetched live with `tables.get` when a table is selected, cached for five minutes, omitted silently on failure, since the catalog does not store them), partition and clustering, view SQL, labels, expiry, description, freshness and status with error text, field list (path, depth, type, description), "names only" notice. Dataset: location, table count, status, description. Project: dataset count, status and error, Execution project note.
- The schema pane flags `REQUIRED` fields. The catalog stores the mode of top-level columns (`COLUMNS.is_nullable`, excluding `ARRAY` columns, which the view also reports as not nullable), so they are flagged at once and offline. The metadata views carry no mode for nested fields (checked live); those are flagged from the same lazy `tables.get` as the row and byte counts.
- Preview is an explicit fetch (`p`). The first press loads 20 rows through `tabledata.list` (free); each further press appends the next 20, up to 1,000, after which the pane says to run a query. Views and external tables show "needs a query". Cells use the results formatting (section 10.4); The preview has a row cursor (`ctrl+e` / `ctrl+y`, or the movement keys while zoomed; moving past the last loaded row loads the next page). `J` (or `Enter` on a zoomed preview) opens the record popup for that row: one field per line, nested and JSON values indented, long text wrapped, `n` / `N` for the next and previous loaded row. A row with clipped cells is read again without truncation.

### 8.5 Navigation

Vim keys (`hjkl`, `gg`/`G`, `ctrl+d`/`ctrl+u`), `/` to filter (8.1), the command palette (`ctrl+p`, or `:` in the explorer), and jump-to-table (`ctrl+t`). Full table in section 15.

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

- A small editor widget written for bqt (`internal/tui/editor.go`): cursor, shift-selection, word motions, snapshot undo and redo, auto-indent, horizontal scrolling, and a per-token highlight hook fed by the core tokenizer. The plan was to fork the Bubbles v2 `textarea`; the fork was dropped because the textarea has no selection (needed for "run selection") and its rendering model fights per-token colours, and the replacement is about 600 lines. No soft wrap: long lines scroll.
- **Vim-style editing is the default** (`[tui] editing = "vim"`; `"plain"` gives the modeless editor, and the key is live-reloaded). This reverses the earlier "modeless, no vim modes" decision. It is a layer over the same widget (`internal/tui/vim.go`), a deliberate subset of vim:
  - Modes: Normal (where a buffer starts), Insert, Visual (`v`) and Visual-line (`V`). The pane title shows the mode and the pending count or operator; the Insert cursor is drawn differently.
  - Counts, and the motions `h j k l`, `w b e W B E`, `0 ^ $`, `gg G` (with a count: go to that line), `{ }`, `%`, `f F t T ; ,`, `ctrl+d ctrl+u ctrl+f ctrl+b`.
  - Operators `d c y > <` with any motion or text object, doubled for whole lines (`dd cc yy >> <<`), plus `D C Y x X s S r J ~ p P`. `cw` behaves like `ce`, and `dw` on the last word of a line keeps the line break, as in vim.
  - Text objects: `iw aw iW aW`, quotes `i" a" i' a'` and backticks, brackets `i( a( ib`, `i[ a[`, `i{ a{ iB`, `i< a<` (brackets across lines).
  - Insert entry `i a I A o O`; `esc` returns to Normal and closes the completion popup in the same press.
  - One unnamed register. A yank or delete also goes to the system clipboard (OSC 52); there are no named registers, and pasting from the system clipboard is the terminal's paste.
  - `u` undoes and `ctrl+r` redoes. One command is one undo step, including everything typed in the Insert session it started. `.` repeats the last change by replaying its keys (a completion accepted with Tab is not replayable, so such a change is not recorded).
  - `/` finds text in the buffer (lower case ignores case), `n` / `N` repeat, `*` finds the word under the cursor. There is no `?` backward search (`?` is help) and no regular expressions.
  - Visual mode: motions and text objects extend the selection; `d x c s y > < ~ u U J p`, `o` swaps the ends.
  - **Key conflicts, resolved in vim's favour in Normal and Visual mode:** `ctrl+r` is redo and `ctrl+d` scrolls, so a query can never start from a redo reflex. Running is `enter` (the buffer, or the Visual selection) or `space r`; the dry-run is `space d`. The `space` leader also has `l` run list, `b` buffers, `n` new, `w` close, `[` `]` switch, `s` save, `e` `$EDITOR`. In Insert mode `ctrl+r` runs and `ctrl+d` dry-runs, as in plain editing; `ctrl+n` forces completion. `:` opens the command palette; there is no ex command line.
  - Kept from bqt in every mode: the `ctrl+x` chords, `ctrl+w h/j/k/l` zone moves (so `ctrl+w` does not delete a word in Insert mode), `ctrl+s`, `ctrl+c`, `ctrl+p`, `ctrl+t`, `ctrl+q`, `ctrl+o`, `ctrl+g`.
  - Not built: macros, marks, named registers, `:` commands, block-visual mode, `ip`/`ap`, `gU`/`gu`/`g~` operators, regex search, counts on insert commands (`3ix`).
- **Several buffers per TUI session.** `ctrl+x n` opens a new buffer, `ctrl+x b` opens the buffer list (open buffers, then the drafts that can be restored), `ctrl+x w` closes one, `ctrl+x [` and `ctrl+x ]` move between them. This is TUI-only; the core is unchanged because a Run snapshots its SQL.
- A Run belongs to the buffer that started it, and the buffer shows a status badge for its latest Run (running, done, failed). Switching buffers while Runs are in flight is fine; the run list stays global. Closing a buffer asks only if it has changes not saved to a file or a Favorite (autosaved drafts do not count).
- **Drafts:** each buffer autosaves to `<data_dir>/drafts/tui/<buffer-id>.sql` (random ID), held under an advisory lock by the owning instance, so several `bqt` instances (for example in tmux panes) never overwrite each other. On startup an instance restores only unlocked drafts (orphans from a crash or a closed pane), listed as "restore drafts" in the buffer list rather than all opened. Orphaned drafts older than 30 days are deleted at startup.
- `bqt path/to/query.sql` opens that file as a buffer and saves back to it with `ctrl+s`. A file buffer has no draft. `ctrl+s` in a scratch buffer asks for a file name and turns it into a file buffer.
- The Explorer's `i` inserts the selected table's backticked name at the editor cursor (a shard family as a wildcard table), and jump-to-table (`ctrl+t`) opened from the editor inserts instead of navigating.
- The `$EDITOR` hand-off stays as an escape hatch on `ctrl+x e` (`tea.ExecProcess` and a temp `.sql` file).

### 9.2 Language intelligence (core, pure functions)

- Own BigQuery tokenizer (about 200 lines; Chroma has no BigQuery lexer and mishandles backticks). `Tokenize(text) -> []Span` with kinds: keyword, string, number, comment, backtick identifier, function, `@param`. It is shared by highlighting and completion.
- Completion context comes from a token-stream scanner, not a parser, behind an interface. It never fails on broken input. `Complete(text, cursorOffset, catalog) -> []Candidate` over a small catalog interface backed by SQLite.
- Scope in v1: projects, datasets and tables (including inside backtick paths, segment by segment); columns for tables in `FROM` or `JOIN` with alias, `UNNEST` alias and struct-path awareness; CTEs complete as table names with columns from explicit select-list aliases or from the source table when the CTE selects `*`; functions and keywords from a static list with signatures.
- Out of v1: subquery column inference beyond the CTE rule, type-aware suggestions, `JOIN ... ON` suggestions, a SQL formatter (auto-indent on newline only), live dry-run while typing.
- `go-googlesql` (no AST on syntax errors, about 63 MB, about 76 s cold build) and `memefish` (Spanner dialect, fails on `QUALIFY`, `PIVOT`, `FOR SYSTEM_TIME`) are not v1 dependencies. Revisit only if CTE column accuracy hurts, after a parse-success spike over the real Favorites and History.
- Lookups stay scoped and prefix-friendly: 0.06 to 0.3 ms on the synthetic 50k-table index.

### 9.3 Popup (TUI-only)

Triggered after 2 typed characters in an identifier position, and immediately after `.` or a backtick; `ctrl+space` forces it. Tab accepts, Enter inserts a newline and never accepts, `ctrl+n`/`ctrl+p` and the arrows navigate, Esc dismisses. Accepting a project or dataset adds the dot and continues with the next segment; accepting a table closes an open backtick. A table name that needs quoting (a wildcard) replaces the whole unquoted path with a backticked one. Drawn with the TUI's own overlay helper, as the other popups are.

## 10. Query runs and results (phase 3) [02, 11, 15]

### 10.1 Runs

- A **Run** snapshots SQL and parameters into an independent object with its own ID, state, dry-run estimate and result pager. Editing the buffer afterwards does not affect it.
- Runs queue beyond `max_concurrent_queries` (default 4) and show as queued. A run list (`ctrl+x l`, or `gr` outside the editor) shows running, done, failed and cancelled. A finished background Run raises a status-bar notice. Cancel targets the focused Run; "cancel all" is a palette command. Quitting with Runs in flight asks, then cancels; quitting also asks when a file buffer has unsaved changes. Shutdown waits (up to 15 s) for job submissions in flight and for the cancel requests to reach BigQuery, so a job is not left running after bqt exits. A cancel that arrives while a job is being submitted is applied as soon as the job ID is known. If a job's status cannot be read after retries, the job is cancelled and the Run fails with "lost track of the job". A cancel request that cannot be delivered is retried, then shown as failed so it can be asked for again. Confirmations of several Runs queue; `enter` on a waiting Run in the run list reopens its question. One export runs at a time.
- Core API: `StartRun(ctx, sql, params) -> RunHandle` with an event channel. Every submitted job gets the `bqt=1` label.
- Execution: `cloud.google.com/go/bigquery` jobs path (`Query.Run`, `Job.Wait`, `Job.Read`), the Execution project in `NewClient` (plus `option.WithQuotaProject`), `Location` from the `location` key or inferred. History fields come from `Job.Status` statistics, not `Query.Read`'s fast path. Do not use `DetectProjectID`.
- Parameters: named `@param` placeholders. Table and column names cannot be parameters. Until Favorites declare parameter types (phase 4), the TUI asks for each value and the core guesses the type from the text (`42` INT64, `1.5` FLOAT64, `true` BOOL, `2024-01-31` DATE, a date and time DATETIME or TIMESTAMP, `null`, anything else STRING); a `TYPE:` prefix forces it. Values are remembered per buffer.
- A follower API (`Run.Await(ctx, afterVersion)`) lets a frontend wait for each change of a Run without missing its end state. The History entry is written before the end state is published.

### 10.2 Dry-run, confirmation, caps

- Every Run does a dry-run first; the estimate (bytes, approximate dollars, flagged if `LOWER_BOUND` or `UNKNOWN`) shows in the status line. `ctrl+d` runs the dry-run alone. Federated tables and row-level security can show 0, so accuracy matters.
- **Pricing model is detected, not configured.** The dry-run job statistics carry `reservation_id` and `edition` (Go client: `JobStatistics.ReservationID` and `Edition`). A set `reservation_id` means the Run executes on a reservation (capacity); an absent one means on-demand. It is read per Run, so it is correct per location and per job even when an Execution project is on a reservation in one region and on-demand in another. If the model cannot be determined (for example the dry-run failed), bqt treats it as on-demand, the safer mistake (it shows dollars and keeps the confirmation threshold). When `location` is set, a free dry-run of a slot-using probe query in that location at startup, and again when the Execution project changes, pre-learns the model so the first Run is presented correctly; until it is known the model is treated as on-demand. The status line shows what was detected, for example `capacity: <reservation> (Standard)` or `on-demand`. A dry-run that scans zero bytes and names no reservation does not change the shown model (seen live: with no `location` set, `SELECT 1` resolves to the US and reports no reservation on a project whose reservation is in the EU).
- **Both models:** `max_bytes_billed` (default 1 TiB) is set as `MaximumBytesBilled` on every job for every frontend. BigQuery enforces it up front against the dry-run upper bound, including on reservation-backed projects (section 17), so a query can be refused even if its real scan would have been smaller. The core treats `bytesBilledLimitExceeded` as a normal refusal, shows the required bytes, and offers no override from the CLI or MCP.
- **On-demand (no reservation in the statistics):** above `confirm_bytes` (default 10 GiB) the core returns `NeedsConfirmation{estimate}` and the frontend asks. The status line shows an approximate dollar estimate. Cancel shows "cancel requested, may still be billed".
- **Capacity (reservation in the statistics):** the dry-run still runs for validation, bytes and `statementType`, but the status shows "scanned (not billed)" with no dollar estimate. `confirm_bytes` is off by default, because bytes are not charged there (set it explicitly to confirm large scans). After the Run, `total_slot_ms` is recorded, and the `totalBytesBilled` BigQuery still reports is not shown as a charge. The "may still be billed" warning is not shown.
- The cap is set by one code path for both models; the only difference between the models is presentation (dollar estimate, billed-bytes display, warnings) and the `confirm_bytes` default.
- Statements other than `SELECT` (by the dry-run's `statementType`) ask for confirmation in the TUI, naming the type. Multi-statement scripts run and the viewer shows the last statement's result.
- A per-job reservation override is out of v1.

### 10.3 Errors

The core parses BigQuery's `[line:col]` into `{message, line?, col?}` (position is optional: some errors, such as a missing table, carry none); when present the editor jumps to and marks it. `invalid_grant` and `invalid_rapt` show the `gcloud auth application-default login` hint; a missing ADC file shows the same hint. A cancelled Run is "cancel requested" until the job state confirms.

### 10.4 Results

- Paging through REST `getQueryResults` in pages of 500 rows, an LRU of 10 pages, every page addressed by `StartIndex` (no page tokens are kept). No row cap in the viewer (paging is server-side). Do not enable the Storage Read client for the interactive table. Re-opening results from a History entry (about 24 hours, via the stored job) is built in phase 4 (section 11).
- Cell values are formatted by the core: NULL, grid truncation at about 200 characters (a parameter the frontend passes), JSON and STRUCT as compact JSON.
- Grid (TUI): column widths from the first page (min 4, max 40 characters), a cell cursor moved with `h`/`l` with whole-column scrolling, `f` freezes the first column, `H` hides the cursor's column for the session, headers with a type badge, NULL dim. Nested cells: STRUCT or JSON as compact one-line JSON with an ellipsis, ARRAY as `[3] first, second…`.
- Single-record view: `Enter` or `v` opens a popup; `n`/`p` move between records across page boundaries. The core returns a structured, indented JSON tree for the record with no truncation, columns in schema order, types preserved (INT64 and NUMERIC as exact strings, BYTES as base64, JSON columns as parsed indented JSON). Long and multi-line strings wrap. A cell over 1 MiB shows a notice and expands only on demand. Scrolling and JSON colour are TUI-only. As built: the record popup reads the row again with cells clipped at 1 MiB and says when a value was clipped; expanding such a value on demand and collapsing nested values (`space`) are not built.
- Cell actions: copy cell value, copy row as JSON, copy column values (current page), copy field path.
- No client-side sort or filter in v1. `/` finds text within the loaded pages and states how many pages are loaded.
- Export: CSV, JSON array and NDJSON. Clipboard copy is TSV for the visible page or selection. Export defaults to all rows, streams through paging to a file with progress, can be cancelled, and asks above 1M rows. Destination defaults to `bqt-<timestamp>.<ext>` in `export_dir` (config key, default the working directory) with an editable path prompt.

## 11. History and Favorites (phase 4) [12]

- **History:** every real Run is kept indefinitely (optional `history_retention_days`, and `bqt history prune`). Failed runs and cache hits are recorded. Search is FTS over the SQL, filterable by `status:`, `project:`, `source:` and `since:` using the same filter parser as section 8.2. Identical SQL (whitespace-normalised) is grouped, showing the latest Run, run count and bytes billed (slot time for entries that ran on a reservation).
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

As built (phase 4, 2026-10-16):

- **History search** (`store.SearchHistory`, `search.ParseHistory`): the filter parser is shared with catalog search (`search.ParseWith` takes the key set); History keys are `status:` (done, failed, cancelled; prefixes), `project:` or `p:` (substring of the job or Execution project), `source:` (tui, cli, mcp) and `since:` (an age `30m` `12h` `7d` `2w`, or a date `2026-10-01` / date and time). Negation and comma OR work as in 8.2; an invalid or incomplete filter shows as a chip and is ignored. Free terms go to FTS5 as prefix phrases (`"customer_id"*` matches the two tokens in order); punctuation-only terms are dropped. Grouping uses a `sql_key` column (migrations 2 and 3: SHA-256 of the SQL with whitespace collapsed outside string literals, quoted identifiers and comments, and a trailing `;` dropped, case kept); rows without a key get it on open. A group shows its newest Run by creation time (not the last one recorded), counts only the Runs that match the filters, sums the bytes billed of its on-demand Runs and the slot time of its reservation Runs. Comma OR in `since:` takes the earliest cutoff; ages are capped at 200 years and `history_retention_days` at 36500. Newest group first, pages of 100.
- **Retention:** `history_retention_days` is applied when History is opened and then at most hourly (and at once when the value changes). `bqt history prune --older-than 90d | --before 2026-01-31 | --all` deletes by hand. `bqt history` listing and search arrive with the agent layer (phase 5).
- **Favorites** (`internal/favorites`): the header is the run of leading `-- description:`, `-- tags:` and `-- param:` lines; the first other line (including other comments) starts the body. Repeated description lines join; tags are comma-separated and de-duplicated. A bad `param:` line (unknown type, bad name, a default that does not fit the type, a duplicate) is listed as a problem with its line number and left out; the picker marks such a file with ⚠. Names are relative paths, `.sql` is added, and absolute paths, `..` and hidden segments are refused; listing skips hidden folders (`.git`). Writes are atomic (temp file, then rename; a new file is linked so an existing one is never replaced by accident); rename refuses an existing target; empty folders are removed after a rename or delete.
- **Parameters:** the schema is the parameters the body uses, in order, typed by the header (undeclared ones are guessed from the value as in 10.1). Values are checked per type before the Run starts; an empty value takes the default; `null` is NULL for every type. The TUI reads the header from the whole buffer, so any buffer with `-- param:` lines gets typed parameters, not only one opened from a Favorite.
- **Promotion** starts from the header the SQL may already carry (a Run from a Favorite's buffer), replaces its description and tags when given, declares the other parameters the SQL uses with the type and value they ran with as the default (NULL as `null`), and writes the Favorite's ID onto the History entry. A default that is not plain text (spaces at the ends, quotes, backslashes, line breaks) is written double-quoted with Go escapes, so it cannot end the comment line. A remembered value with a `TYPE:` prefix equal to the declared type is read without it. Paths are checked after resolving links: a linked folder or file that leads out of `favorites_dir` is refused. Delete and rename act only on the file that was shown, so a `favorites_dir` reload meanwhile cannot redirect them. A Run started from a buffer whose file is inside `favorites_dir` records that Favorite on its History entry.
- **Reopening results** (10.4): `ctrl+r` on a History entry reads page 0 of the stored job again (`runs.Manager.Reopen`) and shows it as a finished Run in the Results; it is not recorded again. A job BigQuery no longer has reads as "no longer available (about 24 hours)".
- **Picker (TUI):** `ctrl+x f` (Favorites) and `ctrl+x r` (History) from any zone, `space f` / `space h` in vim's Normal mode, or the palette. Typing searches (History chips show the filters); `tab` switches the list; `enter` loads the pick; `ctrl+e`/`ctrl+y` scroll the preview. Favorites: `ctrl+r` renames, `ctrl+d` deletes (asks). History: `ctrl+s` saves as a Favorite, `ctrl+r` shows the result again. The search box is shared: `tab` keeps its text; closing the picker keeps it for the next opening of the same list, and opening the other list (`ctrl+x f` after History) starts empty. A page loaded later skips groups already shown, since History can change between pages. Loading: a Favorite already open is switched to; an empty buffer is replaced without asking; a buffer with unsaved text asks (`y` replaces it, `n` opens the pick in a new buffer). A Favorite opens as a file buffer (`ctrl+s` saves back to it); a History entry opens as a scratch buffer with its parameter values remembered (with their types). `ctrl+x s` (`space F`) saves the buffer as a Favorite: name, description and tags, prefilled from the buffer's header; the header in the buffer is rewritten as one undo step and the buffer becomes the Favorite's file. Saving under another existing name asks for a second `enter`.

## 12. TUI zones and keys [17]

See section 15 for the table. Principles: three zones (Explorer, Editor, Results) plus overlays; the editor is modeless so printable keys type there; Explorer and Results use vim-style plain keys; globals use `ctrl+` keys or the palette. One keymap registry generates the help overlay, the palette and the footer hints.

## 13. Platform, logging, diagnostics [18]

- macOS and Linux (amd64, arm64).
- Structured logs in `<data_dir>/logs/bqt.log` (5 MB, 3 files). Default level `info`; `BQT_LOG=debug` or `--log-level`. The TUI and interactive commands never log to the terminal; `bqt mcp` and the CLI may also log to stderr at `warn` and above. SQL text only at `debug`; credentials and tokens never.
- `bqt doctor` prints the config path, data dir, Go and bqt versions, auth status, catalog age and the log path.

## 14. Agent layer (phase 5) [04, 13]

### 14.1 JSON CLI

- Subcommands: `search <query>`, `schema <table>`, `query [--dry-run] [--file f.sql] [--param k=v]`, `history`, `favorites list|show|run`, `status`, `sync [--project ID]`, `projects [select ID…|deselect ID…|exec ID]`, `init`, `config edit|path`, `doctor`, `mcp`.
- `bqt projects` lists what the dialog shows (JSON: per project `id`, `friendly_name`, `selected`, `listed`, `accessible`, `sync`, `datasets`, `synced_at`, plus the Execution project and its source); `select` saves only (run `bqt sync`), `deselect` keeps the cache and refuses the last Selected project, IDs BigQuery does not list are accepted with a warning. Plain `bqt sync` covers the Selected projects; `--project` also refreshes a cached deselected one, and refuses a project that is neither.
- Every non-TUI command writes one JSON document to stdout (the core type serialized as-is). JSON is the default when stdout is not a TTY; `--format text` gives human tables, `--format json` forces JSON.
- Envelope: `{"schema_version":1,"data":…,"truncated":bool,"next_cursor":…}`. Changes within a version are additive only.
- Errors: JSON on stderr, `{"error":{"code","message","hint"}}`, with documented exit codes: 0 ok, 1 failure, 2 usage, 3 config, 4 auth, 5 over limit or refused by the policy, 6 BigQuery error, 7 cancelled. `bqt sync` prints its result and then exits 6 if any unit failed, 4 if every failure was an authentication failure, and 7 if cancelled.
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

### 14.5 As built (phase 5, 2026-10-20)

- **Result types** (`api/agent.go`) are shared by the CLI and MCP: `QueryResult` (statistics, columns, rows as text or null, `result_id`, `truncated`, `next_cursor`, `truncated_cells`), `DryRunResult` (estimate plus `allowed` and the `refusal`), `TableSchema`, `CatalogHits`, `FavoriteList`, `HistoryList`, `CatalogFreshness`. Cells are cut at 200 characters and end with `…`. Shard families appear as `project.dataset.prefix*`; `get_schema` and `bqt schema` accept that form, `prefixYYYYMMDD`, or `dataset.table` in the Execution project.
- **`result_id`** is the job's `project:LOCATION.job_id` (BigQuery's own form), so `get_results` and `bqt results` page a result from any process while BigQuery keeps it; the cursor is a row number. Nothing is cached between calls. History items carry the `result_id` of done Runs. Any job the credentials can read can be paged this way (reading only); restricting it to jobs in History was considered and left out.
- **CLI:** `schema`, `query`, `results` (new: the CLI side of `get_results`), `history [QUERY]` (plus `history prune`), `favorites list|show|run`, `mcp`. Flags may come before or after the arguments (`bqt query "SELECT 1" --limit 5`); an argument with white space is never a flag, so SQL starting with `--` stays SQL. The SQL comes from the arguments, `--file`, or stdin. `--param name=value` binds `@name` (a `TYPE:` prefix forces the type; a Favorite's header types its parameters and its defaults fill the rest); a missing value or an unused parameter is a usage error. `--max-bytes` and `--timeout` lower the limits. `--format` may also follow the command name.
- **Policy** (`policy.Profile`, `policy.For(cfg, source)`): checked after the dry-run inside `runs` (`Request.Policy`), so a refused Run (`state = refused`, `refusal` set) never starts a job and is not recorded in History. Refusal codes: `not_read_only` (also an empty statement type), `unknown_estimate` (accuracy other than PRECISE, LOWER_BOUND, UPPER_BOUND), `over_limit`, `needs_confirmation` (over `confirm_bytes`, `auto` meaning 10 GiB on demand and off on capacity, as in the TUI). A policy Run never waits for confirmation. The timeout covers the dry-run and the job; when it runs out the job is cancelled and the Run fails with kind `timeout` (exit 5). Dry-run-only Runs report the verdict without refusing. **The job's cap stays `max_bytes_billed`**: an earlier build also capped agent jobs at `agent_max_bytes`, and the live check showed why not: BigQuery bills at least 10 MiB per table referenced and checks the cap against that, so a low cap refuses queries whose estimate is a few bytes (and a query over many small tables would be refused under the default 1 GiB).
- **MCP** (`internal/mcp`): the official Go SDK v1.8.0; tools use typed inputs and outputs (schemas inferred from the `api` types). Instructions are about 1,000 characters. All tools are marked read-only (`run_query`, `dry_run` and `get_results` as open-world). A refusal or a failure is a tool error with the reason. The server watches the catalog (another process's sync reloads the index) and, when it started before the first sync, opens the catalog once one exists. List tools default to 20 and cap at 200. Logs go to the log file and stderr.
- **Hand-off** (TUI): `ctrl+x y` (vim `space y`) opens a popup listing what applies: schema of the explorer's selected table (markdown, JSON), query and error, a 20-row sample (markdown, TSV, NDJSON), the Run summary. The focused Run is the one shown in the Results when they have the focus, else the buffer's latest. `enter` or `y` copies, `w` writes a file (default `.bqt-handoff/<name>`, asks before overwriting). Each item is also a palette command.
- **Codex review fixes (2026-10-20):** every job gets a bqt-chosen ID (`bqt_` plus 32 hex digits) and its submission is bounded by the agent deadline, or 2 minutes outside the policy (the client retries transient errors until its context ends); when the bound runs out, bqt asks BigQuery to cancel that ID and the Run fails ("took too long", or `timeout` under the policy). The agent timeout also covers reading the first result page inside the Run (a Run whose rows could not be read in time fails with `timeout`, its statistics kept) and the rows `run_query` returns. A missing estimate accuracy is refused like `UNKNOWN`. On the agent surface a parameter's default fills only a missing value: a value given empty stays empty, and two names that differ only in case are an error. History lists carry `total` (the matching groups). The NDJSON hand-off sample is written from the raw values like an export, so strings keep line breaks and tabs. A hand-off file is created exclusively unless overwriting was confirmed.
- **Shutdown:** `runs.Manager.Close` now also waits (within the same 15 s) for the cancelled Runs to end, so their History entries are written before the store closes (this also removed a flaky TUI test).
- Verified live (dev project, read-only): sync, `bqt query` (JSON and text), `--dry-run` on a reservation, `bqt results` paging, a `SCRIPT` refused (exit 5), a syntax error with its position (exit 6), `bqt schema`, `bqt history`, and the MCP server over real stdio with all eight tools. Not checked by hand yet: OSC 52 copies of the hand-off in the author's terminal.

## 15. Keybindings [08, 10, 11, 15, 17]

Zones: Explorer, Editor, Results, overlays. Zone switching: `ctrl+o` (or vim's `ctrl+w w`) cycles to the next zone and `ctrl+w W` to the previous one; `ctrl+w` then `h`/`j`/`k`/`l` moves directionally. (F6 was dropped on the user's request, 2026-10-15: function keys are awkward on a laptop keyboard. `ctrl+o` is free in every zone and in vim's Insert mode, since vim's `ctrl+o` is not built.) As built, the Explorer is one screen and the Editor above the Results is another (the focused pane gets the larger share); `ctrl+w h` goes to the Explorer, `ctrl+w l` or `k` to the Editor, `ctrl+w j` to the Results.

Tutorial (user request 2026-10-15): `ctrl+g` (or the palette's "Tutorial") docks a panel under the screen that teaches the navigation in 17 steps: Explorer movement, in and out, search, jump, layouts, the detail pane and zoom, the palette, help, zones, writing (vim or plain), running, Results, record view, Favorites and History. The UI works as usual meanwhile; each step watches the model (a selection change, an overlay opened and closed, a zone reached, a Run started) and opens the next step when its task is done. While it runs, `ctrl+g` is a prefix for the tutorial's own keys in every zone and popup: `n` next (skip), `p` back, `r` restart, `q` quit. The panel's rows are taken from the app's height. The first start without a UI state file shows a one-time hint about `ctrl+g`.

| Zone | Key | Action |
|---|---|---|
| Global | `ctrl+q` | Quit (asks if Runs are in flight) |
| Global | `ctrl+p` | Command palette (unless the completion popup is open) |
| Global | `ctrl+t` | Jump to table |
| Global | `ctrl+c` | Cancel focused Run; with nothing running, shows a "ctrl+q to quit" hint |
| Global | `F1`, `?` (outside editor) | Help overlay |
| Global | `ctrl+o`, `ctrl+w w`, `ctrl+w W` | Next zone, next zone, previous zone |
| Global | `ctrl+g` | Tutorial of the navigation (see below) |
| Explorer | `h` `j` `k` `l`, arrows | Move, collapse/expand, enter column |
| Explorer | `gg`, `G`, `ctrl+d`, `ctrl+u` | Top, bottom, half page |
| Explorer | `/` | Filter the focused column, the tree or the flat list in place; `:` opens the palette |
| Explorer | `L`, `P` | Cycle layout; cycle pane placement |
| Explorer | `p`, `J` | Fetch more preview rows; record JSON |
| Explorer | `ctrl+e`, `ctrl+y` | Scroll detail pane |
| Explorer | `r`, `R`, `gp` | Refresh project, refresh all, sync problems |
| Explorer | `Enter` | Expand shard family; zoom detail if collapsed |
| Explorer | `z` | Zoom the detail; zoomed: movement keys scroll it, left/`esc` leave, `P` side by side or stacked, `Enter` record |
| Explorer | `i`, `gr` | Insert the table's name into the editor; run list |
| Explorer | `esc` | Back out one level |
| Editor | printable keys | Type (plain editing, and vim's Insert mode) |
| Editor (vim Normal) | `enter`, `space r`, `space d` | Run, run, dry-run; `ctrl+r` is redo and `ctrl+d` scrolls here |
| Editor (vim) | see 9.1 | Motions, operators, text objects, Visual mode, `/`, `.` |
| Editor | `ctrl+r` | Run selection, or the whole buffer |
| Editor | `ctrl+d` | Dry-run |
| Editor | `ctrl+space` | Force completion |
| Editor | Tab, `ctrl+n`/`ctrl+p`, arrows, Esc | Popup accept, navigate, dismiss |
| Editor | `ctrl+x e` | `$EDITOR` hand-off |
| Editor | `ctrl+x l` | Run list (`enter` shows a Run's result, `c` cancels it) |
| Editor | `ctrl+s`, `ctrl+x h`, `alt+w` | Save to file; select all; copy the selection |
| Editor | `ctrl+z`, `ctrl+y` | Undo, redo |
| Editor | `ctrl+x n`, `ctrl+x b`, `ctrl+x w`, `ctrl+x [`, `ctrl+x ]` | New buffer, buffer list, close buffer, previous and next buffer |
| Editor | `ctrl+x s` (vim: `space F`) | Save the buffer as a Favorite |
| Global | `ctrl+x f`, `ctrl+x r` (vim: `space f`, `space h`) | Favorites and History picker |
| Global | `ctrl+x p` (vim: `space p`) | Projects: turn them on or off (`space`, `o` only this one), `e` Execution project, `s`/`S` sync now |
| Global | `ctrl+x y` (vim: `space y`) | Hand-off to an agent: `enter`/`y` copies, `w` writes a file |
| Picker | `tab`, `enter`, `ctrl+e`/`ctrl+y`, `esc` | Switch list, load into the editor, scroll the preview, close |
| Picker | `ctrl+r`, `ctrl+d`, `ctrl+s` | Favorites: rename, delete; History: show the result again, save as a Favorite |
| Results | `h` `j` `k` `l` | Move the cell cursor |
| Results | `Enter`, `v` | Single-record view; `n`/`p` next and previous record |
| Results | `f`, `H`, `U`, `/`, `n`/`N` | Freeze first column, hide column, show hidden columns, find in loaded pages, next and previous match |
| Results | `y`, `Y`, `gy`, `gf`, `gt` | Copy cell, row as JSON, column values (loaded page), column name, visible rows as TSV |
| Results | `e` | Export (Tab cycles CSV, JSON, NDJSON) |
| Results | `0`, `$`, `gg`, `G`, `ctrl+d`, `ctrl+u` | First and last column, first and last row, half page |
| Results | `esc` | Clear the find text, then back to the editor |
| Results | `gr` | Run list |

`ctrl+a` and `ctrl+b` stay with the textarea; move them if the user's tmux prefix is one of them. The registry is checked by a test that finds duplicate bindings within a zone.

## 16. Testing and CI [16]

- **Fakes first:** hand-written fakes behind the `bq` ports model region grouping, per-project and per-dataset permission failures, transient errors, dry-run results (`statementType`, accuracy flags), paging, cancellation, job errors with `line:col`, and dry-run statistics with and without a reservation. The BigQuery emulator is not used in CI (beta; region-qualified `INFORMATION_SCHEMA`, jobs paging and dry-run unconfirmed).
- **Live suite:** `//go:build live`, run manually against a scratch project, selected by an env var. It covers the checks in section 17 and re-runs the verified ones.
- **Contract suites:** each port's behaviour tests are written once and run against the fake in every build and against the real implementation under `live`, so a fake that drifts fails the live run.
- **TUI:** golden-file snapshots of `View()` at 120x36, 80x24 and 60x20, ANSI-stripped, with a deterministic fake engine and clock and an `-update` flag; key-sequence tests without a PTY; one PTY smoke test. Colours and OSC 52 are checked by hand.
- **Store and performance:** real SQLite in temp directories; a generator for 50k tables and 1.25M columns; manual benchmarks of search (budget p95 ≤ 30 ms), completion lookups and sync commit time; one CI smoke guard at about 5x the budget.
- **Pure logic:** table tests for the filter parser and ranker, completion with cursor markers, the Favorite header and parameter schema, the safety policy over every statement type and accuracy flag, cell formatting and record trees; fuzz tests for the tokenizer (never panics, spans cover the text) and filter parser; MCP over the SDK's in-memory transport; CLI golden JSON; the import test; the keymap duplicate test.
- **CI:** GitHub Actions on push: `go vet`, `staticcheck`, `go test -race ./...` on Go 1.26. No coverage gate, no live tests.

## 17. Live checks

Run on 2026-10-13 against a real EU dev project (about 1,280 datasets and 9,400 tables, jobs running on a Standard edition reservation), using dry-runs and one tiny test table that was dropped afterwards. They are the seed for the `live` suite (section 16).

Verified:

1. **`MaximumBytesBilled` is enforced on a reservation-backed project.** A job on a Standard edition reservation with a 1 MB cap was rejected up front (`reason: bytesBilledLimitExceeded`, "Query exceeded limit for bytes billed: 1000000. 336592896 or higher required.") with no slots used. The check compares the cap against the dry-run upper bound, not the final processed bytes. Seen on one edition only (Standard); re-check on Enterprise if it matters. This is what lets the capacity and on-demand models share one cap code path (10.2, 14.3). **On-demand enforcement was not tested directly**: the test project's default configuration does not allow overriding the reservation (`reservation_override_mode` unset), so no job could be forced on-demand. On-demand behaviour rests on BigQuery's documented guidance for the setting. Running the same cap test against a project without a reservation is a **blocker before any public release** (item 7 below); the author's own use does not need it. Finished jobs on the reservation still report non-zero `totalBytesBilled` (111 MB for 110 MB processed) alongside `totalSlotMs` and `statistics.reservation_id`, so the UI must not present those bytes as a charge under `pricing = capacity`.
2. **Dry-run exposes `statementType`** and the job's `reservation_id` and `edition` (the basis for pricing-model detection in 10.2). `statementType` came back for every kind tried: `SELECT` (also for `WITH … SELECT` and `INFORMATION_SCHEMA` queries), `CREATE_TABLE_AS_SELECT`, `DROP_TABLE`, `DROP_SCHEMA`, `EXPORT_DATA` and `SCRIPT`. Accuracy was `PRECISE` for all. The read-only classifier in 14.3 stands. `CALL` was not tried (no procedure available); the fail-closed rule covers it.
3. **Dry-run bytes for `INFORMATION_SCHEMA`:** every region-scope view tried (`TABLES`, `COLUMNS`, `COLUMN_FIELD_PATHS`) and the dataset-scope `TABLES` reports 10,485,760 bytes, the 10 MiB minimum, as the research expected.
4. **`lastModifiedTime` moves on a metadata-only edit.** Changing a table's description changed the table's `lastModifiedTime`; the dataset's did not move. A per-table delta signal therefore exists through `tables.get` or the legacy `__TABLES__` view, but `INFORMATION_SCHEMA.TABLES` does not expose it, so the full re-snapshot baseline in section 7 stands. Whether `__TABLES__` is a cheaper signal at scale was not measured.
5. **Error positions are not always present.** A syntax error carries `at [1:1]`; a "table not found" error did not carry a position. The `{message, line, col}` parse in 10.3 must treat position as optional.
6. **Scale.** One project here has about 1,280 datasets, well above the 50 to 200 assumed, and `datasets.list` pages correctly at `maxResults=1000`. All tables sit in one region (EU). Table types seen: `BASE TABLE`, `VIEW`, `SNAPSHOT` and `CLONE`; `CLONE` is added to the `type:` filter (8.2).

13. **Sync at scale (measured while building phase 1).** A real first sync of one EU region with about 1,240 datasets, 9,400 tables and 533,000 field paths took about 45 s end to end (84 s before the six queries ran concurrently), almost all of it BigQuery executing `COLUMN_FIELD_PATHS` (about 35 s) and `COLUMNS` (about 20 s); larger result pages did not help. The process peaked around 580 MB because a unit's snapshot is held in memory. Applying 50,000 tables with 1.3 million field paths to the catalog takes about 14 s the first time, 1.1 s when nothing changed and 1.5 s when 1% changed, but only because `columns(parent_id)` is indexed: without it, deleting a changed table's columns scans the whole table once per deleted row and a 1% change took over a minute at one tenth of that size. Possible later optimizations: split very large regions across datasets in parallel, and stream rows into SQLite.

Not verified. Items 7, 8 and 9 are not needed for the author's own use and are **blockers before any public release**; resolve them if the tool is ever released:

7. **`MaximumBytesBilled` on an on-demand project.** Run the cap test from item 1 (a capped query that must be rejected with `bytesBilledLimitExceeded`, and a query under the cap that must run) against a project with no reservation assignment, and confirm the dollar estimate and confirmation threshold behave as specified. Also confirm that the dry-run statistics carry no `reservation_id` for an on-demand job, since pricing-model detection (10.2) relies on that absence; only the reservation-backed side was observed.
8. `tabledata.list` on a materialized view: the test project is on a Standard edition reservation, which does not allow materialized views ("Materialized views cannot be created in a project with Standard edition reservation"). `tabledata.list` on an ordinary table works. Needs a project with an MV.
9. Region-view behaviour and error text with dataset-only grants (needs a restricted identity).

Not verified, not release blockers:

10. OSC 52 clipboard and colours in the author's real terminal and tmux.
11. Whether the `query` column in `JOBS_BY_USER` truncates long statements (only for the deferred History import).
12. Optional spike: run `memefish` or `go-googlesql` over the author's Favorites and History to measure parse success, if CTE column accuracy matters.

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
