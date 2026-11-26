# 5. BigQuery client: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

`pkg/bq` defines the BigQuery ports the core depends on (`Metadata`, `Jobs`, `Tables`) and the real client behind them: credentials, job submission, the `INFORMATION_SCHEMA` reads that sync runs, and the choice between the Storage Read API and REST paging. `pkg/bq/bqtest` holds the fake and the contracts that hold the fake and the real client to the same behaviour, and the live tests check that behaviour against real BigQuery. These decisions were code comments until ADR 0004 moved them here.

## Decision

### Ports and package boundary

- **`bq`** (package): it defines the BigQuery ports the core depends on and the real implementation behind them. Everything above this package talks to the interfaces only, so tests substitute fakes (ticket 16).
- **`bq.Metadata`**: implementations must set an explicit job location on every query and label their jobs.
- **`bq.Metadata.ListProjects`**: lists the projects the credentials hold a project-level role in (REST `projects.list`), with their friendly names. It needs no Execution project, so it works before one is chosen.
- **`bq.Metadata.ListDatasets`**: enumerates a project's datasets with their locations (REST `datasets.list`). It is the only way to discover which regions to query.
- **`bq.Metadata.GetDataset`**: reads one dataset's description, friendly name, labels and location through REST `datasets.get`, which is free. The dataset-scope fallback uses it for what `SCHEMATA_OPTIONS` gives at region scope only.
- **`bq.Metadata.RegionColumns`**: phase 2, the column field paths (`COLUMNS` and `COLUMN_FIELD_PATHS`) of the tables the `ColumnFilter` keeps. The filter leaves the other tables out on BigQuery's side, not locally. Tables without columns are absent from the result.
- **`bq.Metadata.DatasetTables`, `bq.Metadata.DatasetColumns`**: the two phases restricted to one dataset, using dataset-qualified views. They are the first fallback when the region views are not accessible.
- **`bq.Metadata.GetTableMeta`**: reads one table's row and columns through REST `tables.get` (free, no job), in the shape the views report them. `gs` syncs one table this way.
- **`bq.Metadata.ListTableNames`**: returns table names and types only (REST `tables.list`). It is the last fallback; its tables have `NamesOnly` set and no columns.
- **`bq.WithWarnings`**: puts a warning callback in the context, which a `Metadata` implementation calls through `bq.Warn` with something the user should know about the work done under that context that is not an error, such as a slower read path. The sync run adds these to its warnings. The callback may be called from several goroutines and more than once with the same message. The `Metadata` port is not widened for this, because a detail of one implementation should not change the port.
- **`bq.Tables`**: reads live table metadata and rows without running a query, so at no cost. Views and external tables cannot be listed this way.
- **`bq.RawPage`**: a page of rows in the REST wire shape: each cell is nil, a string, a `[]any` of `{"v": cell}` maps (REPEATED) or a `{"f": [{"v": cell}]}` map (RECORD). The `format` package, not `bq`, turns it into display cells.

### Credentials and impersonation

- **`bq.AuthInfo`**: describes the Application Default Credentials without exposing any secret.
- **`bq.CheckAuth`**: finds the default credentials and fetches a token to report whether they work. It never returns or logs the token.
- **`bq.NewClient`**: jobs run in the Execution project using Application Default Credentials, with the Execution project as the quota project. With no Execution project only `ListProjects` works, under the credentials' own quota project.
- **`bq.ImpersonationDenied`**: reports whether an error is a refused impersonation. The `ImpersonationError` message already names the fix, so callers do not add the ADC login hint, which would not help.
- **`bq.refusedImpersonation`**: matches the raw failure by its text. `golang.org/x/oauth2` and `cloud.google.com/go/auth` both turn the IAM credentials answer into a plain error, `status code 403: <JSON body>` (or 404), with no type to match.
- **`bq.defaultADC`**: returns the ADC file's JSON, or nil. It runs only after a refused impersonation, so the file is known to exist and no metadata server is probed. Calling it anywhere else could probe the GCE metadata server.
- **`bq.impersonationRefused`** (test): the test credentials use a service account source, because both auth libraries honour its `token_uri`. `cloud.google.com/go/auth` ignores `token_uri` for `authorized_user` and would call Google.

### Errors and retries

- **`bq.KindPermission`**: access denied is not retried; the sync tries the next fallback instead.
- **`bq.Classify`**: checks `ImpersonationDenied` (as `KindAuth`) before the `net.Error` check. The client wraps a refused impersonation in a `*url.Error`, which is a `net.Error` and would otherwise be classified as transient.

### Jobs

- **`bq.QueryRequest.JobID`**: the job ID is chosen by the caller, so a submission whose answer never arrived can still be cancelled by ID. Empty lets the client choose one.
- **`bq.JobResult`**: the outcome of a finished job. `Err` is the job's own failure (a query error); the error `WaitJob` returns is a failure to learn the outcome. The two must not be conflated.
- **`bq.Jobs.CancelJob`**: only asks BigQuery to cancel; the job may still finish, so callers wait for the real outcome.
- **`bq.Jobs.QueryRows`**: reads a page of a finished query job's result. For a script, that is the last statement's result.
- **`bq.(*Client).WaitJob`**: the Go client's `Wait` reports a failed or cancelled job as an error of its own. Unless the context is done, `WaitJob` then asks for the job's status, so the caller gets the outcome with its statistics.

### Two clients and the sync read path

- **`bq.SyncMaxBytesBilled`**: the safety cap on every sync query, 10 GiB. An `INFORMATION_SCHEMA` query bills the 10 MiB minimum, so the cap never binds in normal use and only guards against bugs.
- **`bq.Client.bq`**: reads through REST paging. Runs, Samples and agent queries depend on its page-by-page semantics (spec 10.4), and sync uses it once the Storage Read API is out of reach.
- **`bq.Client.syncBQ`**: a second client with the Storage Read client enabled, which runs sync's `INFORMATION_SCHEMA` queries only. It is nil when there is no Execution project or the endpoint is overridden (`BQT_BIGQUERY_ENDPOINT`).
- **`bq`** (Storage Read, #83): sync reads its `INFORMATION_SCHEMA` results through the BigQuery Storage Read API, because on a region with millions of column rows REST `getQueryResults` paging takes minutes where the Storage Read API takes seconds. The Go client creates the read session itself and, when that fails, quietly reads the same job's result through REST paging (the job is not run again). A gRPC interceptor is how bqt learns what happened to the session, so it can tell a missing permission or a disabled API (remembered for the client's lifetime, which is one Execution project) from any other failure (this query only).
- **`bq.readPath`**: one client's choice between the Storage Read API and REST paging for sync results. It starts on the Storage Read API and switches to REST for good on the first read session that fails for want of permission or because the API is disabled. Other failures do not switch it.
- **`bq.readSessionUnavailable`**: reports whether a read-session error means the Storage Read API cannot be used from this Execution project at all: the caller lacks `bigquery.readsessions.create`, or the API is disabled. BigQuery reports both as gRPC `PERMISSION_DENIED` (the disabled API with the `SERVICE_DISABLED` reason); `accessNotConfigured` is the REST form. The reason it returns is a short text for the warning.
- **`bq.(*Client).readOutcome`**: names the read path a query took and acts on a failed read session. The Go client has already fallen back to REST paging for that query, so the same job's result is read and nothing runs again. A result that came whole in the first REST response needed no read session.
- **`bq.(*Client).query`**: warns on every query read through REST while the Storage Read API is off. The sync run dedupes the warning to one per run.
- **`bq.(*Client).ListDatasets`**: uses REST `datasets.list`, because unlike the Go dataset iterator it returns each dataset's location.

### Read session budget

- **`bq.defaultReadSessionBudget`**: bounds the time spent creating one read session at 20 s, retries and the waits between them included. The Go client would retry an unreachable endpoint for up to ten minutes; `sessionInterceptor` retries within the budget instead, and past it the result is read through REST.
- **`bq.sessionInterceptor`, `bq.TestSessionInterceptorKeepsRetriesInTheBudget`**: the interceptor watches `CreateReadSession` calls made under a context that carries a `sessionWatch` and leaves every other call alone. The Go client retries that call outside any interceptor, sleeping between attempts on the query's context, so the budget holds only if the interceptor does the retrying itself and never returns an error the client retries: it returns either the session or a non-retryable error, whether the endpoint refuses at once or hangs.
- **`bq.sessionRetryCodes`, `bq.sessionBackoff`**: the Go client's own retry settings for `CreateReadSession`, so retries inside the interceptor behave as the client's would.
- **`bq.(*sessionWatch).create`**: an attempt cut off by the budget deadline fails with a retryable `DeadlineExceeded`; the check that the remaining time exceeds the next pause is what ends it with `budgetSpent` instead of another attempt.
- **`bq.fakeREST`** (test): answers the REST calls the Go client makes for one query read through `jobs.query`, in order: the first page with a page token, the job and its anonymous destination table (looked up for the read session), and the second page.
- **`bq.budgetSpent`**: the error for a read session given up on. It is not a gRPC status, so the client does not retry it, and the last error is kept as text only, so its status does not leak through `errors.As` either.

### INFORMATION_SCHEMA views and their quirks

- **`bq`** (option literals, `parseStringOption`, `parseLabelsOption`, `parseTimestampOption`): the `*_OPTIONS` views return option values as GoogleSQL literal text, not decoded values, for example `description "Raw \"events\" table"`, `labels [STRUCT("team", "data"), STRUCT("env", "prod")]` and `expiration_timestamp TIMESTAMP "2026-11-01 10:00:00+00"`. These helpers decode them.
- **`bq.scope.datasetsSQL`, `bqtest.(*Fake).DatasetTables`**: `SCHEMATA_OPTIONS` exists only at region scope, so dataset scope runs no datasets query and its snapshot carries no description, friendly name or labels.
- **`bq.(*Client).RegionTables`**: `SCHEMATA_OPTIONS` only lists datasets that carry options, so the dataset list starts from the requested names and overlays what the view returned. Using the view's rows alone would drop datasets.
- **`bq.(*Client).group`**: runs a phase's `INFORMATION_SCHEMA` queries concurrently, each producing its own typed rows. On a 1,200-dataset region this halves the wall time of a sequential run.
- **`bq.(*Client).columns`**: ARRAY types are excluded when deriving Required from `is_nullable = 'NO'`, because the view also reports REPEATED columns as not nullable.
- **`bq.scope.fieldPathsSQL`**: the field paths' row order matters, because a nested field's ordinal is its place among the rows of its parent (the view has no ordinal for nested fields). The SQL ends with `readInOrder`.
- **`bq.readInOrder`, `bq.TestFieldPathsAreReadInOrder`**: a SQL comment that keeps a result's rows in the order REST paging returns them when read through the Storage Read API. The Go client reads a result through one stream when the SQL has `ORDER BY` at its root (outside parentheses) and through parallel streams, whose rows interleave, otherwise. It only looks at the text, so the comment is enough and BigQuery sorts nothing. The comment must stay free of parentheses, because the client skips text inside them. Only the field paths need this; the other views keep parallel streams.
- **`bq.tableMetaOf`**: converts a REST table into the catalog's shape the way the views report it, so a table synced with `gs` matches a region sync: standard SQL type names, one column per field path, partitioning and clustering on the top-level columns, and REQUIRED on top-level columns only.
- **`bq.sqlType`**: writes a REST field's type as `COLUMN_FIELD_PATHS.data_type` spells it.
- **`bq.TestMergeSnapshotMarksRequiredTopLevelColumns`, `bq.TestTableMetaOfMatchesTheViews`**: nested fields such as `addr.city` are expected to be not required, because the metadata views carry no mode for nested fields.

### Column filter and shard patterns

- **`bq.ColumnFilter.Pattern`**: the combined Shard patterns, anchored to the whole name, in the RE2 dialect BigQuery's `REGEXP_CONTAINS` shares with Go, since it is evaluated on BigQuery's side. A table it matches is left out unless `Keep` lists it. `""` reads every table.
- **`bq.ColumnFilter.Keep`**: `"dataset.table"` names read although `Pattern` matches them: each family's Newest shard, matching tables in no Shard family, and members synced on their own.
- **`bq.ColumnFilter.Only`**: when set, reads exactly these `"dataset.table"` names and ignores `Pattern` and `Keep`. It is the safety net's re-read.

### The fake and the contracts

- **`bqtest.RunMetadataContract`**: checks the behaviour every `bq.Metadata` must share. It asserts invariants rather than exact data, so it runs against a live project as well as the fake.
- **`bqtest.ContractOptions`**: bounds the contract run. The real implementation is read-only but billed, so the contract samples.
- **`bqtest.withColumns`**: column rows for a table missing from phase 1 (a leaked or deleted table) are skipped, not failed on, because the caller filters them.
- **`bqtest.checkFallbacks`**: in the dataset-scope fallback, `datasets.get` stands in for `SCHEMATA_OPTIONS`.
- **`bqtest.RunJobsContract`**: the contract queries read no tables, so they cost nothing to run for real.
- **`bqtest.Dataset.NamesOK`**: lets `ListTableNames` and `GetDataset` succeed on a `Denied` dataset, because `tables.list` and `datasets.get` need weaker permissions than `INFORMATION_SCHEMA`.
- **`bqtest.Project.Unlisted`**: leaves the project out of `ListProjects`, as for data shared at dataset level: readable, but the projects list does not return it.
- **`bqtest.Project.Leak`, `bqtest.TestRegionTablesFiltersToRequestedAndLocation`**: leaked datasets appear in region snapshots although `datasets.list` does not return them (deleted-in-time-travel and temporary datasets). The fake leaks them the way region views do, and callers must filter.
- **`bqtest.(*Fake).regionDatasets`**: a region request covers the requested datasets in the location plus any leaked ones. A denied dataset fails the whole request, as a region view does.
- **`bqtest.(*Fake).columns`**: returns no rows for a table without columns, because the views have none.
- **`bqtest.(*Fake).ListDatasets`**: clears descriptions, because `datasets.list` does not return them.
- **`bqtest.Fake.OnCall`**: runs at the start of every call, outside the lock, so the hook can call back into the fake.
- **`bqtest.Fake.PageLimit`**: caps the rows one `QueryRows` call returns, the way BigQuery cuts a response of wide rows short, so callers must page on. 0 means no cap.
- **`bqtest.Fake.MissColumns`**: names `"project.dataset.table"` tables whose columns a filtered column call leaves out although bqt kept them, as when BigQuery's regex matching differs from Go's. A re-read by name (the filter's `Only`) still returns them.
- **`bqtest.(*Fake).Fail`**: an unknown method panics, because a failure that can match no call would let a test pass without testing anything.
- **`bqtest.QueryScript.IgnoreCancel`**: accepts a cancel request but keeps the job running, as BigQuery does until it confirms the cancel.
- **`bqtest.(*Fake).fakeJob`**: finds a job by its project and ID, as BigQuery does, so a ref that names another project is not found and a test catches a Run that waits on, reads or cancels its job through the wrong project.
- **`bqtest.fakeJob.held`**: true while `WaitJob` is waiting on `script.Block`; guarded by `Fake.mu`.
- **`bqtest.Synthetic`**: generates a deterministic snapshot. The default scale used by the spec's budgets is 50k tables with 1.25M field paths: 500 datasets x 100 tables x (20 columns + 5 nested fields).

### Live tests

- **`bq_test.TestLiveMetadataContract`**: run with `BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_PROJECT=... go test -tags live ./pkg/bq -run Live -v`. It only reads metadata (`INFORMATION_SCHEMA` queries, each billing the 10 MiB minimum) and never writes to the project.
- **`bq_test.TestLiveMetadataContract`** (shard family): `BQT_LIVE_SHARD_DATASET` (in `BQT_LIVE_SHARD_PROJECT`, default `BQT_LIVE_PROJECT`, at `BQT_LIVE_SHARD_LOCATION`) holds a Shard family that `BQT_LIVE_SHARD_PATTERN` matches, and `BQT_LIVE_SHARD_KEEP` names members to keep (comma-separated); the phase 2 column filter is then checked against it. The public `google_analytics_sample` works: `BQT_LIVE_SHARD_PROJECT=bigquery-public-data BQT_LIVE_SHARD_DATASET=google_analytics_sample BQT_LIVE_SHARD_LOCATION=US BQT_LIVE_SHARD_PATTERN='ga_sessions_(\d{8})' BQT_LIVE_SHARD_KEEP=ga_sessions_20170801`.
- **`bq_test.TestLivePartitionFilterMetadata`**: checks what sync reads for tables that require a partition filter, and that the `s` shortcut's `SELECT` passes a dry-run on them. Dry-runs are free; nothing is run or written. Run with `BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_FILTER_TABLE=p.d.t BQT_LIVE_INGESTION_TABLE=p.d.t go test -tags live ./pkg/bq -run TestLivePartitionFilterMetadata -v`. `BQT_LIVE_FILTER_TABLE` is column-partitioned and requires a filter; `BQT_LIVE_INGESTION_TABLE` is partitioned by ingestion time and requires one.
- **`bq`** (live Storage Read tests): the Storage Read API checks of spec section 17 read no tables. Run with `BQT_LIVE_EXECUTION_PROJECT=... [BQT_LIVE_PROJECT=...] [BQT_LIVE_LOCATION=US] go test -tags live ./pkg/bq -run TestLiveStorageRead -v`. With `BQT_LIVE_PROJECT` set, the region of that project with the most datasets is also snapshotted both ways and the timings are logged. `TestLiveStorageReadFallback` needs `BQT_LIVE_NO_READ_SESSION_PROJECT`: an Execution project where the credentials lack `bigquery.readsessions.create`, or where the BigQuery Storage API is disabled.
- **`bq.manyRows`**: a query whose result spans several REST pages (500,000 generated rows), so the Go client reads it through a read session. It scans no table.
- **`bq.TestLiveStorageReadMatchesREST`** (scope): reads both phases of a region unit; with no column filter, phase 2 reads every table's columns. Where the region views are refused it reads one dataset's views (`BQT_LIVE_STORAGE_DATASET`, or the location's first), as sync falls back to. `BQT_LIVE_SHARD_PATTERN`, when set, filters phase 2 as a shard pattern would, so its `WHERE` precedes `readInOrder`.
- **`bq.TestLiveStorageReadMatchesREST`** (comparison): tables come in no fixed order, so they are matched up before comparing, but each table's columns must match exactly and in order, because nested ordinals depend on that order.
- **`bq.TestLiveShardRegexParity`**: checks that BigQuery's `REGEXP_CONTAINS` agrees with Go's `regexp` on Shard patterns, anchored as bqt anchors them. Sync leaves collapsed members out of the column queries on BigQuery's side, so a construct the two read differently would cost a re-read (the safety net), and one BigQuery rejects would fail the column queries. Either must fail this test, and config validation must then refuse the construct. Run with `BQT_LIVE_EXECUTION_PROJECT=... go test -tags live ./pkg/bq -run TestLiveShardRegexParity -v`; `BQT_LIVE_LOCATION` sets the job location. It is one query over `UNNEST` parameters, needs no `INFORMATION_SCHEMA` access, and bills the 10 MB minimum.

## Consequences

- Everything above `bq` depends on the ports, and the fake must keep modelling BigQuery's quirks (leaked datasets, region-only `SCHEMATA_OPTIONS`, short pages, missed columns, ignored cancels, weaker permissions for `tables.list` and `datasets.get`). A change to real behaviour needs the matching change in `bqtest` and a contract check, or the fake drifts from BigQuery.
- Every REST or single-table path (`GetTableMeta`, `tableMetaOf`, `sqlType`, `GetDataset`) must report data in the shape the `INFORMATION_SCHEMA` views do, so no catalog row depends on which path read it.
- The `COLUMN_FIELD_PATHS` query must keep `readInOrder`, free of parentheses, at its root; nested field ordinals come from row order. The other views stay unordered to keep parallel streams.
- The Storage Read API is an optimisation that must never fail a sync: session creation is bounded at 20 s, failures fall back to REST on the same job, and only a permission or disabled-API failure switches a client off it. The cost is a second client per Execution project and an interceptor that duplicates the Go client's retry settings, which must be updated when the client's settings change.
- Error classification depends on matching untyped error text from the auth libraries and on check order in `Classify`. A library upgrade can change either, so the impersonation tests guard them.
