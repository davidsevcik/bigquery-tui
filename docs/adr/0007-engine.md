# 7. Engine: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

Package `engine` is the facade every frontend (TUI, CLI, MCP server) uses: it wires the config, the store, the BigQuery ports and the sync engine, and holds the Selected projects, the Execution project, completion, export and the agent surface. These decisions were code comments until ADR 0003 moved them here.

## Decision

### The facade and its lifetime

- **`engine`** (package): the single facade every frontend uses. It wires the config, the store, the BigQuery ports and the sync engine, and exposes long operations as a handle plus a stream of plain-data events, so frontends stay thin.
- **`engine.Options`** (`Meta`): the BigQuery metadata client is built lazily, so commands that never sync (`status`) need no credentials. `nil` uses the real client.
- **`engine.Options`** (`Execution`): when set, it fixes the Execution project for the engine's lifetime. `bqt mcp` keeps the one it started with (`ResolveExecution`), even if the selection changes later.
- **`engine.(*Engine).Close`**: takes `e.mu`, because operations still running (a late `TouchRecent`, a client being built) set its fields under `e.mu`.

### BigQuery clients and the Execution project

- **`engine.Engine`** (`lastExec`): records the Execution project last seen, because pricing is learned per project. `noticeExecution` forgets the learned pricing model when it changes.
- **`engine.(*Engine).lister`**: the metadata client for listing projects needs no Execution project and never uses one. The credentials' own quota project applies, so an unusable Execution project cannot hide the list the user needs to pick another from.
- **`engine.(*Engine).jobs`**: returns the query client of the Run's own Execution project. A Run keeps the project it started in, even if the Execution project changes meanwhile.
- **`engine.(*Engine).clientFor`**: keeps one shared client per Execution project, built on first use. Clients of earlier Execution projects stay open, because Runs that started in them still use them.
- **`engine.(*Engine).SetConfig`**: run-related and sync-related keys apply only to operations started afterwards. A changed Execution project takes effect on the next BigQuery call, since each project has its own client. `data_dir` cannot change at run time; callers report it as restart-required.

### The agent surface

- **`engine`** (agent surface, spec 14): `bqt query`, `bqt favorites run` and the MCP tools run every query under the policy profile of the engine's Source (`cli` or `mcp`). Nothing here asks a question, because no person is there to answer.
- **`engine.AgentQuery`**: `MaxBytes` and `Timeout` can only lower the profile's limits; higher values are ignored, so an agent caller can never raise the configured cost or time ceiling.
- **`engine.(*Engine).RunAgentQuery`**, **`engine.TestRowReadingIsBoundedByTheTimeout`**: one deadline is computed before the Run starts and covers both the Run and reading the rows returned, so `agent_timeout` does not restart for the row read. The first page is read inside the Run, so the test slows only the second `QueryRows` call to hit the row read after the Run.
- **`engine.ResultID`**: names a job's result for later paging as `project:LOCATION.job_id`, the form BigQuery itself uses for job references.
- **`engine.ParseResultID`**: splits on the last colon, because a project ID can carry a domain prefix (`example.com:proj`).
- **`engine.(*Engine).ParsePath`**: accepts `project.dataset.table`, optionally in backticks, or `dataset.table` in the Execution project. A shard family can be named by its family name (`orders_*`, `orders_*_history`, `events_YYYYMMDD`) or its wildcard `prefix*`; its Newest shard answers.
- **`engine.(*Engine).Freshness`**: the compact catalog status of the agent surface. The agent surface never syncs: the advice tells the person how to.

### Sync handles and background syncs

- **`engine.(*SyncHandle).Events`**: the channel closes after the last event. Reading it is optional: events queue internally, and events still unread when `Wait` returns are dropped, so a caller that wants every event reads `Events` to the end before `Wait`.
- **`engine.(*SyncHandle).Cancel`**: committed units stay and in-flight units roll back, since each unit commits on its own.
- **`engine.(*Engine).StartSync`** (one table): a one-table sync (`gs`, `bqt sync --table`) needs the table to be in the catalog already, and a shard family's path syncs its Newest shard. It tracks no projects, because a table sync reads no unit and so no dataset is syncing.
- **`engine.(*Engine).StartSync`** (completion order): after a sync it reloads the search index before signalling completion, so the result is searchable before anyone is told the sync is done. The catalog watcher covers progressive updates and other processes. The sync track ends only after that reload, so a committed unit's datasets never read "not synced" from the index before it shows them synced; the lock is held until then, so no next sync replaces the track first.
- **`engine.(*Engine).Reconcile`**: applies a changed project or `ignore` filter by pruning the catalog locally. It needs the sync lock; if another process is syncing it does nothing and reports skipped, because that sync reconciles the filter itself.
- **`engine.(*Engine).AutoRefresh`**: starts the background sync a frontend wants at startup: a full sync when the catalog is empty, otherwise a refresh of the Selected projects that are stale or were never synced (selecting a project asks for its sync). It returns `nil` when nothing needs doing or a sync runs elsewhere. Deselected projects are never refreshed, and a "config changed" state is never synced automatically: the user decides.
- **`engine.(*Engine).checkScope`**: an explicit sync may cover Selected projects and cached deselected ones. Refreshing a cache does not select the project.

### Pending datasets and freshness

- **`engine`** (pending datasets): a Pending dataset is one BigQuery lists whose unit has never committed it (`api.DatasetPending`). What users see is derived: "syncing" while this process's sync is reading or will read its unit, "not synced" otherwise. Sync events reach only the process that runs the sync, so every other process shows every Pending dataset as not synced.
- **`engine.syncTrack`** (`queued`): holds the discovered units that have not failed, and keeps a committed unit. Its datasets stay pending in the search index until it reloads, and they must read "syncing", not "not synced", in that gap.
- **`engine.withPending`**: folds Pending datasets into units for freshness (spec 7.2). A location that holds Pending datasets but no unit state counts as a unit that never succeeded, and while a project has a Pending dataset its discovery unit's success does not count.
- **`engine.syncedAt`**: a project's oldest successful unit sync; `ok` is false when a unit never succeeded. A project with no located unit counts as synced when its discovery unit succeeded, because a complete listing found no datasets.
- **`engine.(*Engine).Status`**: the discovery unit (empty location) carries a project's sync time only when the project has no located units, that is, an empty project.

### The Explorer's reads

- **`engine.(*Engine).Projects`**: lists the Selected projects, synced or not, with sync health: the Explorer's project list. Deselected projects keep their cache but are not browsed.
- **`engine.(*Engine).TableDetail`** (stale index): checks the fetched row's name and dataset against the index entry. The in-memory index can be a moment older than the catalog, and a deleted table's row id may already belong to another table.
- **`engine.(*Engine).TableDetail`** (shard families): a family's schema is its Newest shard's, so `SchemaFrom` names that shard. For a member whose columns were not read, it shows its family's columns read from the Newest shard the member records, not through the family's name, because a family of another table type can share that name.
- **`engine.(*Engine).TableStats`**: fetches the live row and byte counts and the required fields, cached for five minutes (`statsTTL`). It needs credentials and one API call, so frontends call it off the UI thread and treat a failure as "unknown".
- **`engine.(*Engine).Preview`**, **`engine.TestPreviewOfAMaterializedViewNeedsAQuery`**, **`engine.TestPreviewReadsPastShortPages`**: reads rows straight from a table through `tabledata.list` (no query, no cost): the free Sample. Views, materialized views and external tables return `bq.ErrNeedsQuery`, because BigQuery refuses `tabledata.list` on them ("Cannot list a table of type MATERIALIZED_VIEW", seen live 2026-10-31), so a materialized view's Sample needs a query like a view. A shard family path reads its Newest shard, and `o` formats the cells as for `RunRows`. BigQuery returns fewer rows than asked when rows are wide, so Preview reads on from the next index until it has the rows, a call returns none, or the table ends; the fake's `ListRowsLimit` models that cut.
- **`engine.(*Engine).WatchCatalog`**: reloads the index once right after taking its change baseline, because anything committed between `Open` loading the index and the baseline would otherwise go unnoticed.

### Selection

- **`engine`** (selection): the Selected projects and the Execution project (the Projects dialog and `bqt projects`) live in the selection state file, shared by every frontend and process, not in the config (ADR 0001).
- **`engine.(*Engine).SelectProjects`**: a project already selected keeps its selection time, because that time decides the default Execution project. Selecting syncs nothing.
- **`engine.(*Engine).DeselectProjects`**: a deselected project keeps its cache. The last Selected project cannot be deselected (`ErrLastSelected`).
- **`engine.(*Engine).ChooseExecution`**: a chosen Execution project can be replaced but not cleared.
- **`engine.(*Engine).ProjectInfos`**, **`engine.TestProjectInfosMergeListedSelectedAndCached`**: merges the listed, Selected and cached projects, sorted by ID. A `nil` listed slice means no list could be fetched, and every project then counts as listed; an empty non-nil slice means the list was fetched and empty. The test marks a project `Unlisted` to model BigQuery's projects list leaving out a project shared only at dataset level.

### Completion

- **`engine.completionCatalog.Projects`**: offers the Selected projects that hold data. Completion follows the Explorer: deselected projects are not offered.
- **`engine.completionCatalog.offered`**: offers the names inside a Selected project or the Execution project, the latter even when deselected, because unqualified `dataset.table` names resolve there. The columns of a table the query names are offered from any cached project, since the user wrote that table.
- **`engine.completionCatalog.Columns`**: completion runs on the frontend's input path, so a table's columns are read from the catalog once, with the read bounded at 250 ms, and then served from memory. The cache is dropped whenever the index reloads.

### Export and hand-off

- **`engine.(*ExportHandle).Events`**: when the reader falls behind, older progress events are dropped, so a slow reader never blocks the export. The channel closes after the final event (`Done` set).
- **`engine.(*Engine).StartExport`**: streams every row of a Run's result through the result pages to a file written under a temporary name and renamed when complete, so a cancelled or failed export leaves nothing behind. It uses the schema the Run kept instead of fetching it, so no schema fetch can fail or be skipped during the export.
- **`engine.(*Engine).RunSample`**: renders the first `handoff.SampleRows` rows of a Run's result for a hand-off. NDJSON is written from the raw values, as an export would, so strings keep their line breaks and tabs; markdown and TSV come from the formatted cells, which they have to flatten anyway.

### History retention

- **`engine.(*Engine).applyRetention`**: deletes entries older than `history_retention_days`, at most once an hour (`retentionEvery`) and again whenever the setting changes. The caller holds `e.mu`, because it updates `retainedDays` and `retainedAt`. It is housekeeping with its own timeout, not part of the caller's request. The cutoff uses `time.AddDate` instead of subtracting a `Duration`, because `AddDate` cannot overflow into the future for large retention values.

## Consequences

- A Run, an export and a client stay bound to the Execution project they started in. Changing the Execution project never closes a client or moves work in flight, so clients accumulate for the engine's lifetime.
- The agent surface never prompts and never syncs. Its limits can only go down from the policy profile, and one deadline bounds the whole query including the row read.
- The order at the end of a sync is load-bearing: commit, reload the index, then end the sync track and release the lock. Reordering it makes datasets flash "not synced" or leaves results unsearchable when completion is announced.
- "Syncing" is local knowledge. Only the process running a sync can show it; every other process sees Pending datasets as not synced.
- Only Selected projects are browsed, completed and refreshed automatically. Deselected projects keep their cache and stay reachable only through an explicit sync, a qualified table name, or the Execution project.
