# 12. Supporting packages and tooling: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

Around the catalog, sync and the frontends sit smaller packages: configuration and its watcher, Favorites, value formatting, export and hand-off, logging, the agent safety policy, the build version, and developer tools (the emulator seeder, the architecture map, the PTY smoke test). Each holds a few rules that are easy to break by "simplifying" the code. These decisions were code comments until ADR 0004 moved them here.

## Decision

### Configuration

- **`config.Config.ExecutionOverride`**: holds an Execution project given for this one invocation (`--execution-project` or `BQT_EXECUTION_PROJECT`), and `ExecutionOverrideFrom` says which (`"flag"` or `"env"`). The saved choice lives in the selection state (`state/projects.json`), never in the config (ADR 0001).
- **`config.Config.ExportDir`**: empty means the working directory.
- **`config.Filter`**: the part of the configuration that decides what the catalog contains, which is the `ignore` patterns only. The Selected projects decide what is synced, never what is pruned. Its hash decides "config changed, sync needed".
- **`config.fileConfig`**: `ExecutionProject` and `Projects` are no longer settings. They are read only so the error can point the user at the Projects dialog.
- **`config.Matcher`**: evaluates the ordered gitignore-style `ignore` patterns over the dotted path `project.dataset.table` (ticket 05). `*` stays inside one segment, `**` spans segments, `?` is one character inside a segment. A pattern covers everything beneath the path it matches, so `proj` ignores the project and `proj.tmp_*` ignores matching datasets. A leading `!` negates and the last matching line wins. There is no bare-name-matches-anywhere rule. Elements starting with `#` are comments and blank elements are skipped.
- **`config.Tightening`**: reports whether the new patterns only add ignore rules to the old ones, with no negations among the additions. A tightening change can be applied by pruning the stored catalog; any other change may need a sync.
- **`config.Config.GUI`**: the `[gui]` table of the config file, read by BigQuery Studio, a separate module that shares the file. The TUI, CLI and MCP ignore it, but bqt still parses and validates it.
- **`config.(*Config).GUIEditing`**: the GUI's `[gui] editing` style. bqt itself never reads it, so it is not dead code. Its default is plain, unlike the TUI's vim.
- **`config.(*builder).lineOfTableKey`**: finds a key inside its own `[table]` section rather than anywhere in the file (as `lineOfKey` does), so an error on a key that both `[tui]` and `[gui]` have points at the right line. A dotted key (`table.key = ...`) or an inline table (`table = { ... }`) gives the line of that assignment instead.

### Watching the config file

- **`config.Watch`**: polls the config file and emits an Event when its content has changed and then stayed stable for the configured time. It polls rather than using fsnotify, because polling copes with editors that save by rename and with symlinked dotfiles. The channel is closed when the context ends.
- **`config.Watch`**: hashes the file once synchronously, before starting the goroutine, so a write racing with the goroutine start is still seen.
- **`config.Watch`**: builds the new config from the very read that was hashed. Reading the file again could catch a write that started after the stability check.
- **`config.Event.Config`**: the new valid configuration. It is nil when `Err` is set, and then the last good configuration stays in force.
- **`config.watchSetup`**: the test waits for the watcher to stop (it closes the channel), because a later test swaps `readFile`, and a watcher still running would race with that.
- **`config.save`**: test helper that replaces the config the way most editors do, writing a temp file and renaming it over the target, so the watcher never sees it half-written. `os.WriteFile` truncates first, and a writer stalled after the truncate (a busy CI runner) leaves an empty, valid config that can look stable. The half-written case has its own test.
- **`config.TestWatchWaitsForStability`**: content that changes on every check is never applied, however slowly the checks come. The test feeds the watcher through `readFile` instead of rewriting the file, because a busy CI runner can stall a real writer longer than the stability window (for example right after `os.WriteFile` truncates the file), and then the half-written file really is stable.

### Favorites on disk

- **`favorites`**: a Favorite is a plain `.sql` file under `favorites_dir` whose leading `-- key: value` comment lines carry a description, tags and typed parameters (spec section 11). The header is SQL comments, so a Favorite still runs anywhere as it is. There is no index: a directory walk is fast enough below a few thousand files.
- **`favorites.quoteDefault`**: writes a default so `unquote` reads it back: bare when it is plain, otherwise double quoted with escapes. A line break must never end the comment line.
- **`favorites.Dir.contained`**: refuses a path that a symlink (a linked folder, or the file itself) leads out of the favorites directory. The deepest part of the path that exists is resolved and must stay under the resolved root. When nothing exists yet, nothing can link out, so the path is accepted.
- **`favorites.Dir.shown`**: returns the file of a Favorite as it was read (by `List` or `Read`) and refuses one whose ID now leads to another file. A reload of `favorites_dir` in the meantime must not redirect a rename or a delete.
- **`favorites.Dir.Write`**: claims the name first with `O_EXCL`, which refuses an existing file. The rename that follows then replaces only bqt's own empty placeholder, never a file someone else just created.
- **`favorites.Dir.Delete`**: removes a Favorite as it was read. There is no trash; git is the safety net. A Favorite whose name now leads to another file is an `ErrMoved`.
- **`favorites.Dir.Save`**: when the body does not end with a newline, the new header length is one byte shorter, because `Render` ended the body with a newline the text does not have.
- **`favorites.FromHistory`**: the file text of a History entry promoted to a Favorite. It starts from the header the SQL may already carry (a Run from a Favorite's buffer), so the new header replaces it instead of stacking on it. A description or tags given replace the old ones. Every other parameter the SQL uses is declared with the type and value it ran with as its default (NULL as `null`).

### Favorite parameters

- **`favorites.Query`**: the query that runs is the body (a selection, or the whole text) typed by the Favorite header the text carries. The TUI reads the header from the whole buffer, so a selection still gets typed parameters.
- **`favorites.Schema`**: the parameters the body uses, in order of first use, typed by the header declarations. A parameter used but not declared has no type, and its type is guessed from the value. A declared one the body does not use is left out, because BigQuery would refuse it.
- **`favorites.Given`**: values passed by name (`--param name=value`, an MCP tool's params). A default fills only a missing value, and a value given empty stays empty (spec 14).
- **`favorites.Prompted`**: the answers to a prompt that asked for every parameter. An empty answer takes the default (spec 11).
- **`favorites.Remembered`**: turns the parameters a Run ran with back into the values a prompt shows, so the query runs again as it did: NULL as `"null"`, and a typed value with its type as a prefix (`"DATE:2024-01-01"`), which `Bind` reads back under a declared or a guessed type.
- **`favorites.bindValue`**: accepts a remembered value that carries its type as a prefix (History keeps `"DATE:2024-01-01"`). When the parameter is declared, the declared type makes the prefix redundant.

### Formatting, export and hand-off

- **`format`**: turns BigQuery wire values into display cells. It is core logic shared by every frontend, and frontends only lay the cells out. Output never contains control characters or ANSI sequences.
- **`format.Text`**: the full, unsanitized text of a wire value, for export: scalars as they are, RECORD, REPEATED and JSON values as compact JSON. `null` reports a NULL value.
- **`format.arraySummary`**: stops adding elements once the summary passes 4096 bytes, because the cell is clipped anyway.
- **`format.writeJSON`**: renders a wire value as compact JSON, keeping field order and the exact numeric text. INT64 and NUMERIC are never routed through floats.
- **`format.RowJSON`**: renders formatted cells as one JSON object, columns in schema order. Nested and JSON values, numbers and booleans keep their JSON form; everything else is a string. Callers pass untruncated cells.
- **`export`**: writes a query result to a file format by streaming: rows are pulled page by page and written as they arrive, so the size of a result is bounded by the disk, not by memory.
- **`export.Next`**: returns the rows from `startIndex` on, any number of them. No rows means the end.
- **`handoff`**: renders the plain-text formats the TUI hands to an agent (spec 14.4): a table's schema, a failed query with its error, a sample of a result, a Run's summary. The text has no ANSI; a frontend only decides where it goes (the clipboard, a file).
- **`handoff.Sample`**: the first rows of a result as markdown, TSV or NDJSON. Cells should be unclipped so the values are exact.
- **`export.AskAboveRows`**: a frontend asks before it starts an export of a result above 1,000,000 rows, because a result that large takes long to page through and to write (spec 10.4).
- **`export.Exportable`**: refuses a Sample. It is not a Run, so there is no job result to page through (`Engine.StartExport` takes a Run); exporting a table's rows takes a `SELECT` of the table, which is billed.
- **`handoff.Items`**: each hand-off kind has its own default file name (schema `.md` and `.json`, `query-error.md`, `sample-<run>.<ext>`, `run-<run>.md`), so writing one item never overwrites another item's file.

### Drafts

- **`drafts.Buffer.Unsaved`**: a scratch buffer with text counts as unsaved although it autosaves to a draft, because closing or replacing the buffer discards that draft and the text lives nowhere else. A file buffer is unsaved only when it has changes not written to its file. A frontend asks before closing or replacing either.
- **`drafts.(*Draft).Save`**: writes through a temporary file and a rename, so a failed write leaves the draft on disk as it was, and the caller tries again later. Empty text has no draft: the file is removed. Whether the draft is empty changes only once its file operation succeeds: true after a remove that succeeds or finds no file, false after the rename. A failed save keeps the old value, so after a failed remove `Release` keeps the lock file, and the Execution project it records, beside the text that is still there.
- **`drafts.(*Draft).SetExecutionProject`**: an empty value clears the record. The Execution project a draft was written under lives in its lock file, not in the `.sql` file, so the draft file stays the buffer's text alone and the record goes wherever the lock goes: an empty draft keeps none once released, and a discarded, expired, blank or lone lock takes it along. The record is written through the held lock (`store.(*Lock).Rewrite`), never by path or by rename, because a file at that path need not be the one this instance holds the lock on.
- **`drafts.heldExecutionProject`**, **`drafts.executionProjectIn`**: a draft's record is read only through its held lock, never by path; one that can't be read is unknown. The record ends with a line break, and one without it reads as unknown: a write cut short by a crash or a full disk must not leave a prefix that reads as another, valid project.
- **`drafts.(*Store).Restore`**: holds the lock before it reads the text, so the text and the recorded Execution project come from the same moment; a read that fails releases the lock again.

### Logging

- **`logging`**: bqt's structured logger writes JSON lines to a rotating file in `data_dir/logs`, and never to the terminal for interactive commands (spec section 13).
- **`logging.Options.Command`**: when set, a start record (`"bqt started"`) is written with the version and the level, so every run leaves a line in the file.
- **`logging.New`**: the start record goes to the file only and at any level. It is not a warning for stderr, and a run at level error still leaves its line.
- **`logging.fileWritable`**: creates the log directory and the log file up front, so the path `bqt doctor` reports exists even before anything reaches the level (lumberjack opens the file only on its first write). It reports false when either cannot be written, as on a read-only mount; the command then runs without the file sink rather than failing, and stderr still gets its records. It also tightens a directory or file an older bqt left readable to others to 0700 and 0600, because the modes apply only on creation, lumberjack's rotation copies the file's mode, and debug records hold SQL. The tightening is best effort, since not every file system keeps Unix modes.
- **`logging.TestNewTightensExistingModes`**: sets the directory to 0755 with an explicit `os.Chmod`, because the mode passed to `MkdirAll` is reduced by the umask.
- **`logging.fanout.Handle`**: gives the record to every handler that wants it, even after one failed. A log file that cannot be written must not silence stderr.

### Agent safety policy

- **`policy`**: the safety model of the agent entry points (spec 14.3): `bqt query`, `bqt favorites run` and `bqt mcp`. bqt cannot tell an agent from a person at the command line, so every non-TUI query goes through the same profile: SELECT only (unless the config allows CLI writes), a byte limit checked on the dry-run, a timeout, and caps on rows and cell size. The profile is a pure function of the config and the dry-run, so every frontend (and a future GUI's agent mode) gets the same answer. Flags and tool arguments can only lower a limit; raising one takes the config file.
- **`policy.Profile.Timeout`**: bounds the whole query, dry-run included: `agent_timeout`, or shorter.
- **`policy.ConfirmThreshold`**: the `confirm_bytes` threshold for a Run whose dry-run did (capacity) or did not show a reservation; 0 means none. The TUI asks above it; the agent entry points refuse.
- **`policy.Profile.Check`**: returns why the profile refuses a query with this dry-run estimate, or nil when it may run. It fails closed: an empty or unknown statement type, an UNKNOWN estimate and a missing accuracy are refused. A LOWER_BOUND estimate is checked against its lower bound. The job's `MaximumBytesBilled` (`max_bytes_billed`, set on every job) is the server-side backstop. The agent limit is not set as the job's cap, because BigQuery bills at least 10 MiB per table referenced, so a cap that low would refuse queries whose estimate is tiny.

### Build and developer tools

- **`version.Version`**: set at build time with `-ldflags "-X .../pkg/version.Version=v1.2.3"`.
- **`emuseed` (`main`)**: syncs bqt's catalog from a BigQuery emulator whose `INFORMATION_SCHEMA` is too thin for bqt's views, so the dataset snapshot is built from `tables.get`. It is for manual testing only: the run-bqt-emulator skill's `setup.sh` builds and runs it with `BQT_CONFIG` and `BQT_BIGQUERY_ENDPOINT` set.
- **`emuseed` `meta.ListDatasets`**: fills in the location the emulator leaves out (with `US`), because bqt keeps the empty location for a project's discovery state.
- **`smoke.TestTUIStartsRendersAndQuitsInAPTY`**: waits for the first row of the help, because on 24 rows the overlay shows only the top of its list.
- **`archmap.extractAPI`**: returns the exported declarations of one package and the first sentence of its package comment. The files must be parsed with the same `fset` and with comments.
- **`archmap.emptyAPI`**: has non-nil lists, so they serialize as `[]` rather than `null`.
- **`archmap.printNode`**: prints a declaration without comments, indented with spaces. go/printer notes fields that go/doc filtered out.
- **`archmap.layerGraph`**: ranks the nodes (0 = imports nothing in the graph) and orders each row to reduce edge crossings. `layers[0]` is the top row (highest rank). It guards against cycles even though Go forbids import cycles.
- **`archmap.TestLayerGraphReducesCrossings`**: sorted by name the rows are `x1 x2` over `a b`, so the edges cross until a sweep swaps one row.
- **`archmap.dataMarker`**: replaced by the model JSON. encoding/json escapes `<`, `>` and `&`, so the JSON cannot end the `<script>` element it sits in.

## Consequences

- Anything that reads the config file or a Favorite must assume another writer: the watcher applies only a read it hashed and found stable, and Favorites claim names with `O_EXCL`, act only on the file as it was read, and refuse paths a symlink leads out of.
- Values keep their exact text from the wire to every output: no floats for INT64 or NUMERIC, unclipped cells for export, hand-off and `RowJSON`, and no ANSI or control characters in anything displayed.
- The agent policy fails closed and can only be tightened from a flag or tool argument. Changing what counts as runnable means changing `policy.Profile.Check`, not a frontend.
- Logging never blocks a command: a missing or read-only log directory drops the file sink, and one failing handler does not silence the others.
- A Favorite's header is ordinary SQL comments and History values carry their type, so a Favorite or a remembered Run runs again the same way in the TUI, the CLI and MCP.
