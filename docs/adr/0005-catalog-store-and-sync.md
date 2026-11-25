# 5. Catalog, History store and sync: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

The `store` package owns the two SQLite files in the data directory: `catalog.db`, the cached BigQuery metadata, and `history.db`, the History entries. The `sync` package keeps the catalog in line with BigQuery, `shard` turns the Shard patterns into Shard families, and `selection` keeps the Selected projects and the Execution project. The TUI, CLI and MCP server are separate processes that share these files. These decisions were code comments until ADR 0003 moved them here.

## Decision

### Selection state

- **`selection`**: the Selected projects and the chosen Execution project are the working set that used to live in the config file. They are one small JSON file in the data directory's state folder, shared by every frontend (TUI, CLI, MCP) and every process.
- **`selection.Project.SelectedAt`**: it decides the default Execution project, which is the earliest-selected project (`State.Earliest`).
- **`selection.Store`**: reads are cached until the file changes, so a change written by another process shows up on the next read.
- **`selection.stamp`**: a version of the file is identified by its file identity (`os.SameFile`) as well as mtime and size. Every save renames a new file into place, so the identity changes even when two saves land within one mtime tick.
- **`selection.(*Store).Update`**: it rereads the file, applies `fn` and writes the result atomically. There is no cross-process lock, so the last write wins between processes. Nothing is written when `fn` fails.

### Shard patterns and families

- **`shard`**: Shard families (spec section 8.3) come only from the configured Shard patterns: tables of one dataset and type whose names differ only in the part a pattern's capturing group marks. There is no built-in rule, so with no patterns nothing collapses.
- **`shard.(*Patterns).Hash`**: a stable hash of the effective patterns. A change of it means the families in the catalog no longer match the config.
- **`shard.(*Patterns).Combined`**: one RE2 expression that matches a name when any pattern does, for BigQuery's `REGEXP_CONTAINS`, which uses the same dialect as Go's `regexp`. It is `""` when there are no patterns.
- **`shard.Assign`**: it sets `Shard` on every table in a family and clears it on the rest. Patterns are tried in order, each on the tables no earlier pattern put in a family. Under one pattern the matches are grouped by dataset, the text before and after the group, and table type; a group of two or more is a family, and the others are tried against the next pattern.
- **`store.(*Catalog).shardSelect`**: it lists the shard family and schema-mark columns in exactly the order `shardRow.dest` scans them. Changing one without the other assigns values to the wrong fields.
- **`store.(shardRow).apply`**: a row an older bqt wrote (a family count but no family name) is a Date family of the old built-in rule, named prefix + `YYYYMMDD` with prefix + last as its Newest shard.
- **`store.(*Catalog).StaleFamilies`**: a family is stale, and needs a sync, when a member names a Newest shard its dataset does not hold, or when an older bqt added rows with its date rule next to rows this version wrote. The missing Newest shard was pruned by a tightened ignore list, or deleted by an older bqt that does not keep the newest-shard column.
- **`store.(*Catalog).StaleFamilies`**, **`store.TestStaleFamiliesIsLinearInOldRows`**: the datasets with this version's families are found once, because a per-row subquery over the dataset is quadratic in a dataset of old rows; the Newest shard check is an index lookup on `(dataset_id, name)`. The test guards that old-rule rows in a dataset without this version's families are not stale and that the check stays linear: a per-row scan made a 20,000-member family take minutes.
- **`store.(*Catalog).ApplyTable`**: it stores one table's row and columns read on their own (`gs`), and only for a table already in the catalog. Its family stays as the last unit sync assigned it, and `own` marks it as having its own schema.

### Catalog schema and opening

- **`store.CatalogSchemaVersion`**: the catalog has no migrations. A version mismatch deletes and recreates `catalog.db` (ticket 06). A column with a default is added in place instead (`addedColumns`), without a version change, so that older bqt binaries sharing the file keep working.
- **`store.addedColumns`**: the columns added to the schema after its version. The writer's open adds any an existing file lacks, and a read-only open reads a missing one as its default. Older binaries name their columns in their statements, so they never see these.
- **`store.OpenCatalog`**, **`store.addMissingColumns`**: the whole check-and-create (or recreate) sequence runs under a blocking init lock, because two processes doing it at once (two first launches) must not interleave. `addMissingColumns` adds the missing columns while keeping the data, and its caller must hold that lock.
- **`store.TestContentHashSeesOnlyASetPartitionFilterFlag`**: the golden value is the hash a table had before the `require_partition_filter` flag existed. An upgrade must not rewrite tables that do not have the flag, so the value must not be updated to follow a change to the hash code.

### Applying units and listings

- **`store.(*Catalog).ApplyUnit`**: one unit's rows are swapped in a single transaction: new and changed rows are written, and rows missing from the snapshot are deleted. Readers see the old or the new snapshot, never a half-written one; splitting it into several transactions would break that.
- **`store.upsertDataset`**: a same-named dataset row under another location is deleted and replaced. A dataset can only exist under another location if it was recreated there.
- **`store.insertColumns`**: columns are inserted parents first, so a child's `parent_id` can be resolved from the parent rows already inserted.
- **`store.(*Catalog).RecordEmptyProject`**, **`sync.(*Syncer).discover`**: a complete dataset listing that leaves nothing to sync is recorded as a successful sync with 0 datasets on the project's discovery unit (location `""`). The project has no (project, location) unit to record it, so without this it would never count as synced.
- **`store.(*Catalog).RecordListing`**, **`sync.(*Syncer).discover`**: a project's dataset listing is stored before its units sync, so the listed datasets show at once as Pending datasets until their unit commits (a large unit can take many minutes). Every listed dataset the catalog does not hold yet is inserted as pending, with the listing's location, friendly name and labels and no tables; rows already held in the listed location are never touched. A dataset listed in another location than its row was recreated there, so its row is replaced by a pending one in the new location: otherwise the old location's unit or its vanished-location removal would delete it, and it would be missing until the new location's unit commits. Only a complete listing (no unreachable locations) deletes pending rows it no longer returns; an incomplete one removes nothing. It reports how many rows it added and removed.
- **`sync.(*Syncer).discover`**: locations the catalog knows but BigQuery no longer lists for the project are removed only when the listing is complete. When BigQuery reports unreachable locations, a missing location may only have been skipped, so nothing is removed.
- **`store.(*Catalog).Prune`**: only the allowlist removes a whole project, and nil keeps every project (the only caller, `sync.ReconcileConfig`, passes nil). Ignore rules are applied per dataset and per table, exactly as sync applies them, because an ignored project can still hold re-included datasets (`"p"`, `"!p.keep"`).

### SQLite files and the WAL

- **`store.dsn`**: the DSN is built with `url.URL` so the path is escaped. SQLite parses the DSN as a URI, so a raw `#`, `?` or `%` in `data_dir` would silently change which file is opened.
- **`store.walSizeLimit`**: the size (64 MiB, `journal_size_limit`) the WAL file is cut back to once it has been checkpointed. A sync unit is one transaction, so without it the WAL stays as large as the largest unit ever applied.
- **`store.(*Catalog).trimWAL`**: after a unit it checkpoints the WAL and truncates it, so a large unit does not leave a WAL file of its size behind while the process keeps running (#91). It never waits: a reader still in the WAL (another pooled connection, or the TUI, CLI or MCP process) makes it give up at once instead of holding the write lock for `busy_timeout`, and it then retries in the background until the WAL is truncated or the catalog closes.
- **`store.truncateWAL`**: one `wal_checkpoint(TRUNCATE)` with `busy_timeout` 0, so it never waits on locks. It is not done when a reader or a writer was in the way, which SQLite reports in two ways: `busy=1` in the result, or `SQLITE_BUSY` for the write lock.
- **`store.truncateWAL`**: the connection goes back to the pool, so the DSN's `busy_timeout` is restored in a deferred call, whatever happens, even if setting it to 0 failed half way (a cancel). A connection that cannot be restored is dropped, so no pooled connection keeps a wait of 0.
- **`store.walTrimmer`**: at most one background retry loop runs per catalog. A request that arrives while the loop runs is kept (`pending`), so the loop does not end on a success that came before that request.
- **`store.(*walTrimmer).retry`**: the loop starts with the caller's context values but not its cancellation (`context.WithoutCancel`), because the unit that asked has finished. It ends on success, after `walRetryFor` (10 minutes, trying every 2 s), or when `stop` cancels it, even while it waits for a pooled connection. Giving up without truncating is safe: the journal size limit cuts the WAL at a later write.
- **`store.(*walTrimmer).stop`**: it ends the retry loop before the database closes under it, so `(*Catalog).Close` calls it first.

### Reading the catalog

- **`store.IndexSnapshot`**: the whole catalog minus columns. That is small enough to hold in memory (about 50k short rows at the target size) and to rank on every keystroke.
- **`store.(*Catalog).EachColumn`**: it streams every column's table id, lower-case field path and lower-case description, grouped by table, and the search package builds its in-memory column blob for the `col:` filter from it. A trigram FTS index was measured and rejected: it made the first apply of 1.3M columns ten times slower, while an in-memory scan answers in tens of milliseconds.
- **`store.Watcher`**: it detects catalog changes made by any connection, including other processes, with SQLite's `PRAGMA data_version` read on one pinned connection, since the value only means something when read repeatedly on the same connection.
- **`store.(*Catalog).TableByID`**: the table row and its columns are read in one transaction, so a sync committing in between cannot mix an old row with new columns.
- **`store.eachRow`**: it always closes the rows and returns the first error, from the query, from `scan` or from iterating (`rows.Err`). A cancelled context would otherwise look like a short but successful result.

### History store

- **`store.historyMigrations`**: the embedded, forward-only migrations; index i takes `history.db` from version i to i+1. A released migration is never edited; changes are appended.
- **`store.backupHistory`**: the backup is written with `VACUUM INTO`, which makes a transactionally consistent copy even while other connections hold the WAL open. A plain file copy after a checkpoint can miss committed pages.
- **`store.NormalizeSQL`**: whitespace is collapsed only outside string literals, quoted identifiers and comments, and a trailing semicolon is dropped. The same query typed twice groups as one History entry, while `'a  b'` and `'a b'` stay apart.
- **`store.termSpans`**: it marks every case-insensitive occurrence of the terms as byte offsets, folding only ASCII letters (`asciiLower`). Full Unicode case folding can change byte lengths, and the offsets would no longer be valid in the original text.

### Sync units and the failure ladder

- **`sync`**: sync (spec section 7) works per (project, location) unit. Each unit is fetched with region-qualified `INFORMATION_SCHEMA` views and applied to the catalog in one transaction. When the region views are not accessible the unit degrades through a ladder: per-dataset views, then table names only, then keeping the old data and marking the dataset inaccessible.
- **`sync.Options.Selected`**: the Selected projects are what a full sync covers. A scope may name other projects; the Syncer does not restrict them, and the caller decides which are allowed.
- **`sync.Syncer`**: it does not take the process sync lock; every caller must hold it.
- **`sync.(*Syncer).fromSnapshot`**: a full region snapshot becomes dataset applies, dropping the datasets that were not requested, because region views can leak deleted and temporary datasets, and the tables the ignore rules exclude.
- **`sync.(*Syncer).fallbackDataset`**: a NotFound error drops the dataset instead of keeping its old data, because it was deleted since `datasets.list`.
- **`sync.(*Syncer).fallbackDataset`**: the `gs` own-schema marks are carried onto names-only tables, so they outlast a names-only refresh.
- **`sync.(*Syncer).withDatasetOptions`**: on the fallback paths the description, friendly name and labels come from `datasets.get`, since `SCHEMATA_OPTIONS` exists only at region scope. A failure is logged and leaves the `datasets.list` values, because the tables are still fresh.
- **`sync.DeniedUnit`**: a unit's last failure counts as a permission (or not found) error, meaning the project is not accessible rather than failing for another reason, by the `kind:` prefix `errText` writes into the stored error. `errText` must keep that prefix.

### Columns and single-table sync

- **`sync.(*Syncer).readColumns`**: phase 2 of a unit reads columns only for the tables that should have them: tables in no family, and the keep list (each family's Newest shard, every table that matches a pattern but is in no family, and every member synced on its own). The other members are left out on BigQuery's side and marked as using their family's schema. As a safety net, a table that should have columns but got none is read again by name, so a difference between BigQuery's regex dialect and Go's costs a query but never a schema.
- **`sync.(*Syncer).readColumns`**: column rows for tables it did not ask for are ignored: a collapsed member BigQuery returned anyway, or a leaked table.
- **`sync.(*Syncer).readColumns`**: only a table the by-name re-read found columns for is reported, since the filtered query should have returned it. Tables without any columns stay missing and are not news.
- **`sync.(*Syncer).syncTable`**: it syncs one table's row and schema with REST `tables.get` (`gs`, `bqt sync --table`). A family member read this way is marked as having its own schema, so later unit syncs keep reading its columns, the Newest shard included (it keeps them once a newer shard arrives). When the user named the family, its Newest shard is only refreshed and keeps any mark it had; a plain table is only refreshed.

### Config changes

- **`sync.MetaShardHash`**: the hash of the Shard patterns the catalog's families were computed with. A catalog without it was built by an older bqt, whose built-in rule collapsed date-suffixed tables.
- **`sync.ReconcileConfig`**: it brings the catalog in line with the effective ignore filter and Shard patterns without any BigQuery call. If the filter hash changed, everything the new filter excludes is pruned locally. A sync is flagged as needed when the ignore list was loosened (anything other than appending ignore rules), since the catalog may then be incomplete, and when the Shard patterns changed, since the families (and the `gs` own-schema marks that go with them) were computed with the old ones. Selection never prunes: a deselected project keeps its cache. The caller must hold the sync lock.
- **`sync.ReconcileConfig`**: a pure tightening, or a change of only the Shard patterns, keeps any earlier "sync needed" flag rather than clearing it.
- **`sync.sameIgnore`**: only the stored ignore patterns are compared. Catalogs from before the Projects dialog also stored the old `projects` list in the filter, so their hash differs although nothing that still matters changed.

## Consequences

- Several bqt versions and processes share `catalog.db`. The schema changes only by added columns with defaults or by a version bump that recreates the file, statements name their columns, and old-rule shard rows stay readable.
- A unit is one transaction, so readers never see half a unit, but the WAL can grow to a unit's size. `journal_size_limit` and a checkpoint that never waits keep it small without blocking readers.
- An incomplete BigQuery listing never deletes anything. Datasets show as pending before their unit commits, and old data is kept rather than dropped when access fails, except for a dataset BigQuery reports as not found.
- Column reads depend on the Shard patterns meaning the same thing in Go and in BigQuery's RE2. The by-name re-read makes a mismatch cost a query, not a schema.
- Callers of `sync.Syncer` and `sync.ReconcileConfig` must hold the sync lock. The selection file has no lock: the last write wins.
