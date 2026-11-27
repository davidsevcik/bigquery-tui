# 10. Frontend contract, CLI and MCP: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

Package `api` holds the plain data types the core shares with every frontend. Package `cli` is the non-interactive command line, and package `mcp` is the MCP server over stdio. Together they make up the agent surface of spec 14. These decisions were code comments until ADR 0004 moved them here.

## Decision

### The shared contract

- **`api`** (package): holds only plain data types shared by the core and every frontend. Everything serializes to JSON, holds no rendered strings and imports nothing UI-related. Offsets, where they appear, are UTF-8 byte offsets.
- **`api`** (agent result types): the types in `agent.go` are the agent surface's results, both what the JSON CLI prints and what the MCP tools return (spec 14). Both frontends use the same shapes, so a script written against `bqt query` also reads `run_query`. They are compact on purpose because an agent pays for every token.
- **`cli`** (package): when stdout is not a terminal, every command writes exactly one JSON document to stdout, the core type serialized as-is. Otherwise it writes human text (spec section 14.1).
- **`api.SchemaVersion`**: the version of the JSON contract. Changes within one version are additive only.
- **`api.ExitOK` … `api.ExitCancelled`**: the exit codes are a documented contract (0 ok, 1 failure, 2 usage, 3 config, 4 auth, 5 over limit, 6 BigQuery, 7 cancelled) and are never renumbered.
- **`api.Span`**: a half-open range [Start, End) of UTF-8 byte offsets into the text of the field it belongs to. A frontend that counts in other units (UTF-16 in a JavaScript shell) converts the offsets itself.
- **`api.Candidate`**: accepting a completion suggestion replaces the bytes [Start, End) of the text with Insert.
- **`api.Cell`**: Text is one formatted value and never contains control characters or ANSI sequences.
- **`api.QueryError`**: a failed dry-run or job. Line and Col are 1-based, and 0 means BigQuery gave no position.
- **`api.Source`**: names the frontend that started a Run. It is stored on the History entry (`source:` in History search) and, for every Source but the TUI, put on the job as the `bqt_source` label; the engine's agent methods also use it to pick a policy profile.

### Queries, safety and cost

- **`cli`** (agent commands): schema, query, results, history and favorites are the agent-facing commands (spec 14.1). Every query they run goes through the CLI's policy profile (spec 14.3), so none of them bypasses the agent limits.
- **`mcp`** (package): eight tools over stdio (spec 14.2), each a thin wrapper over an engine call and its `api` type. Every query runs under the agent policy (spec 14.3) inside the engine. The MCP tool annotations are hints for the client, never the enforcement.
- **`api.Refusal`**: the safety policy saying no (spec 14.3). A refused query never starts a BigQuery job.
- **`api.DryRunResult`**: Capacity is set when the job would run on a reservation, where bytes are not billed. ApproxUSD is the on-demand price and is set only otherwise.
- **`api.QueryResult.ResultID`**: the job's `project:LOCATION.job_id`. It reads further pages (`get_results`, `bqt results`) for about 24 hours, as long as BigQuery keeps the result.
- **`api.QueryResult.Rows`**: every cell is text or null, so INT64 and NUMERIC stay exact. STRUCT and JSON cells are compact JSON. A cell longer than the cell limit is cut and ends with "…", and TruncatedCells counts those cells.
- **`api.QueryStats.BytesBilled`**: left out under capacity pricing, where billed bytes are no charge.
- **`api.RunStats.BytesBilled`**: what BigQuery reports. Under capacity pricing it is not a charge.
- **`api.HistoryGroup`**: BytesBilled sums the on-demand Runs only, and SlotMs sums the Runs on a reservation, because the billed bytes of a Run on a reservation are not a charge.
- **`api.RunInfo.CancelRequested`**: set between the cancel request and BigQuery's confirmation that the job stopped. The Run may still be billed during that gap.

### Runs, History and Favorites

- **`api.RunInfo.Tag`**: opaque to the core. A frontend uses it to tie a Run to a buffer.
- **`api.RunInfo.FromHistory`**: names the History entry whose result a reopened Run shows. Such a Run ran earlier and is not recorded in History again.
- **`api.Favorite`** (ID): a Favorite is a `.sql` file under `favorites_dir`, and its ID is the file's path relative to `favorites_dir`, always with forward slashes.
- **`api.Favorite.Path`**: the file the Favorite was read from. A rename or delete acts only on that file.
- **`cli.cmdHistoryPrune`**: refuses an `--older-than` value that contains `-`. `search.SinceTime` also accepts dates, and `--older-than` must be an age such as 90d.

### Catalog and Shard families

- **`api.ColumnMeta.Required`**: the REQUIRED mode. BigQuery's metadata views report it for top-level columns only, so nested fields are always false.
- **`api.TableMeta.OwnSchema`**: marks a Shard family member whose own schema was synced on request (`gs`, `bqt sync --table`). Later syncs keep reading its columns instead of falling back to the family's schema.
- **`api.TableMeta.ContentHash`**: relies on `encoding/json` sorting map keys, which keeps the marshalled bytes, and so the hash, stable.
- **`api.ShardFamily.Wildcard`**: the BigQuery wildcard table over the family, Prefix + `*`. BigQuery allows the `*` only at the end, so the wildcard also matches any other table whose name starts with Prefix.
- **`api.ShardFamily.AgentName`, `api.CatalogHit`**: the CLI and MCP name a Shard family by its family name (`orders_*`, `orders_*_history`). The exception is a Date family without a suffix, which keeps the wildcard it has always been reported as (`events_*`). A CatalogHit's Kind is project, dataset, table or shard_family, and for shard_family its Path names the family this way.
- **`cli.cmdSearch`**: rewrites a Shard family's `Path.Table` to its agent name for CLI and MCP output (a suffix-less Date family keeps its wildcard `events_*`, spec 8.3). It leaves Name unchanged because the match spans index into Name.
- **`api.FilterMatch.Shard`**: a shard_family match carries the family's prefix and suffix, which say which tables are its members. The family's name alone can be read two ways when its fixed parts hold `YYYYMMDD` themselves.
- **`cli.TestSyncTableSyncsOneTablesSchema`**: expects `orders_2` to be the Newest shard because the fake tables have equal creation times, and ties on creation time go by name.

### Search

- **`api.SearchOptions.TablesOnly`**: drops project and dataset results (jump-to-table), except Pending datasets, because they have no tables yet that could stand for them.
- **`api.SearchOptions.SelectedOnly`**: leaves out the projects that are not Selected (the TUI uses it). A pasted path is still an exact lookup.
- **`api.FilterOptions.ExpandShards`**: reports every passing Shard family member as a table match. By default a member matches on its own only when a term reaches into its varying part. Otherwise only its family matches.
- **`cli.TestSearchMarksAPendingDataset`**: a dataset BigQuery lists but whose sync unit has not synced is in the catalog, marked pending (issue 82), so an agent does not take it for an empty dataset.

### Projects, status and auth

- **`api.ProjectStatus`**: groups the units of one project, either a Selected project or a Cached project that is no longer selected. A Cached project that is not Selected has Selected false and never makes a sync needed.
- **`api.ProjectStatus.SyncedAt`**: the oldest successful unit sync, and nil if any unit never succeeded, so the project's freshness is that of its worst unit.
- **`api.ProjectInfo.Listed`**: false when BigQuery's projects list, as last fetched, does not return the project. It is true when no list could be fetched, so a failed fetch does not mark every project unlisted.
- **`api.DoctorAuth`**: reports the state of the credentials without any secret.
- **`cli.cmdProjects`**: lists the projects (BigQuery's list, the Selected ones, the cached ones) and changes the selection and the Execution project, like the TUI's Projects dialog. It never syncs.
- **`cli.warnUnlisted`**: warns about project IDs BigQuery's projects list does not return, and still accepts them. Data shared at dataset level never appears in that list, and the ID may also be a typo or there may be no list at all.
- **`cli.oauth2Refused`, `cli.clientRefused`**: these test helpers build auth errors exactly as the auth libraries do, because `pkg/bq` drives the real libraries and matches their error text. `golang.org/x/oauth2` builds the error for `bqt doctor`, and `cloud.google.com/go/auth`, under the BigQuery client's `http.Client`, builds it for every API call.

### Sync scope

- **`api.UnitRef`**: identifies a sync unit. Location `""` is the project-level discovery unit (`datasets.list`). It is used when a project cannot be enumerated completely, and to record the sync of a project that has no datasets and so no other unit.
- **`api.SyncScope`**: an empty Projects list means every Selected project. Table, when set, syncs only that one table's row and schema (`gs`, `bqt sync --table`), and Projects is ignored.
- **`api.SyncScope.Family`**: set with Table when the path named a Shard family. The sync then reads the family's Newest shard, which refreshes the family's schema and marks nothing. A member named itself gets the own-schema mark.

### Command-line parsing

- **`cli.parseAnywhere`**: flags may come before, between or after the positional arguments (`bqt query "SELECT 1" --limit 5`). An argument with white space in it is never a flag, so SQL starting with `--` or `-` stays SQL. A lone `--` ends the flags. A negated search filter (`-status:failed`) is a positional argument too.
- **`cli.isFlagArg`**: a name containing `:` is not a flag, because `-status:failed` is a negated search filter. A name starting with a digit is not a flag, because `-5` is a number.
- **`cli.formatFlag`**: finds `--format` among a command's arguments the way the command's own parser reads them. The last one wins, and an argument with white space in it (SQL such as `"--format=x\nSELECT 1"`) is not a flag.
- **`cli.parseNoArgs`**: a command that takes no arguments refuses one, rather than ignore it and the flags after it, so a mistyped command line fails loudly.

### Logging and the terminal

- **`cli.cmdMCP`** (stdout): serves the MCP tools over stdio (spec 14.2), or prints the client configuration snippets. stdout belongs to the protocol, so logs go only to the log file and stderr.
- **`cli.cmdTUI`**: logs go to the log file only, never stderr, because the terminal belongs to the UI and stray output would corrupt the screen. The one line it writes to stderr is the warning about a failed close of the log file, after the UI has quit and the terminal is restored. So `closeLog` stays the first defer: it runs last, after `m.Close` and `eng.Close`, which may still log.
- **`cli.openAgent`**: opens its logger without a start record, because agents call these short commands often and a start record per call would flood the log.

### The MCP server

- **`mcp.Instructions`**: sent to the client at initialization. Claude Code cuts server instructions and tool descriptions at 2,048 characters, so each must stay under that length.
- **`mcp.Server`**: serves the tools of one engine. When the server starts before the first sync, it reopens the engine once a catalog exists, instead of needing a restart.
- **`mcp.(*Server).watch`**: keeps an engine's search index current by watching the catalog, because another process's `bqt sync` changes the catalog under this long-lived server.
- **`mcp.(*Server).engine`**: when it swaps in a fresh engine, it keeps the old one open until the server closes, because a query may still be running on it.
- **`cli.cmdMCP`** (Execution project): resolves the Execution project once, before the server starts. The server may open its engine again when the first catalog appears, and it must keep the same Execution project.

## Consequences

- The `api` types are a public contract shared by the CLI and MCP. Within one `SchemaVersion`, fields may be added but never renamed, removed or given a new meaning, and the exit codes keep their numbers.
- Cost reporting depends on the pricing model everywhere: under capacity pricing, billed bytes are never shown or summed as a charge, and slot time stands in for them.
- Safety lives in the core's policy, never in a frontend. A new CLI command or MCP tool that runs SQL must go through the engine's agent policy, and MCP annotations do not count as enforcement.
- The CLI and MCP name a suffix-less Date family `events_*` for compatibility, while the TUI and the search spans use the family's display name. Code that rewrites names must leave the fields the spans index alone.
- stdout is reserved: one JSON document for the CLI, the protocol for MCP, and the screen for the TUI. Logging must respect which frontend owns the terminal.
