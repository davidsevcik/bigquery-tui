# Research 02: Query execution with the Go BigQuery client

Researched 2026-10-12. Go client read at `cloud.google.com/go/bigquery` v1.85.0 (`bigquery/internal/version.go` on `main`). Nothing was run against any GCP project; all findings come from docs and client source.

Legend: **[doc]** stated in official docs, **[src]** read in client source, **[inferred]** my deduction from source or docs, not directly stated, so confirm in a prototype.

## Recommendation

1. **Use `cloud.google.com/go/bigquery` with the jobs path: `Query.Run` then `Job.Wait` then `Job.Read`.** Do not use `Query.Read` for the interactive path. Do not call `Client.EnableStorageReadClient`. Reasons:
   - `Run` gives a `*Job` with its `Location()` and `ID()`. These are needed for Cancel, History and re-opening results.
   - The Storage Read path in the Go client has no random access paging (see Result retrieval). The REST path does.
   - `Query.Read`'s fast path (`jobs.query`) returns a minimal job with no statistics. A History entry needs statistics.
2. **Execution project:** `bigquery.NewClient(ctx, executionProject, option.WithQuotaProject(executionProject))`.
   - The `projectID` argument to `NewClient` is the project jobs are billed to **[src]**.
   - `WithQuotaProject` sets the `X-Goog-User-Project` header. This avoids the "no quota project" failure that user-credential ADC can hit **[doc]**. It needs the `serviceusage.services.use` permission on that project **[doc]**.
   - Take the execution project from the bqt config file. Fall back to the gcloud config `core/project` only when the config is empty (see Auth for how).
3. **Auth:** rely on ADC as-is. Surface auth failures at two points:
   - `NewClient` fails with "could not find default credentials".
   - The first RPC fails on token refresh. Detect this with `errors.As(err, &*oauth2.RetrieveError)` and `ErrorCode == "invalid_grant"`, plus a string match on `invalid_rapt` or `reauth`.
   - Show a fixed message: run `gcloud auth application-default login`. Do not run it from the TUI.
4. **Dry run on every edit-and-pause (debounced) and before every run, gated by a confirm threshold.**
   - Show `TotalBytesProcessed` and a cost estimate: `bytes / 2^40 * price_per_TiB`. Price is per region, and $6.25 is the US list price **[doc]**.
   - Always show `TotalBytesProcessedAccuracy`. Label `LOWER_BOUND` and `UNKNOWN` honestly.
   - Set `QueryConfig.MaxBytesBilled` as a hard cap on real runs. Over-cap queries fail without charge **[src, field doc]**.
5. **Location:** leave `Query.Location` empty by default and let BigQuery infer it from the referenced datasets **[doc]**.
   - On a location error, show it verbatim plus a hint. Allow a per-run override and a per-execution-project default location in config.
   - Always store `job.Location()` in History and use it for Cancel and `JobFromIDLocation`.
6. **Paging into the table view:** use `RowIterator` over `jobs.getQueryResults` (REST).
   - Wrap it with `iterator.NewPager(it, pageSize, token)` and keep a small page cache in the TUI. Keep the page tokens so the user can go back.
   - Use `RowIterator.StartIndex` for jump-to-row. This works only on the REST path.
   - Reserve the Storage Read API for a future "export all rows" action. Do not use it in the interactive table.
7. **Cancel:** call `Job.Cancel(ctx)` on Esc/Ctrl-C, then poll `Job.Status` until `Done`.
   - Warn the user that cancelling may still bill up to the full query cost.
   - Cancelling a `context` does not cancel the server-side job **[inferred]**: `Job.Cancel` is the only place the client calls `jobs.cancel` **[src]**.
   - So bqt must call `Cancel` explicitly on quit while a job is running.
8. **Parameters:** map Favorite placeholders to BigQuery **named** parameters (`@name`) with explicit types.
   - Use a header declaration so bqt knows the types.
   - Table and column names cannot be parameters **[doc]**. If Favorites need those, they need a separate client-side substitution that is strictly validated. Decide that in the Favorite-format ticket.
9. **History entry fields:** see the table in the Reporting section. Record them from `Job.Status` after the job finishes, not from the `Read` path.

## Auth

- **ADC search order [doc]** (<https://docs.cloud.google.com/docs/authentication/application-default-credentials>):
  1. `GOOGLE_APPLICATION_CREDENTIALS`
  2. the gcloud ADC file, `$HOME/.config/gcloud/application_default_credentials.json` (macOS/Linux) or `%APPDATA%\gcloud\application_default_credentials.json`
  3. the metadata server
- The same order is in `auth/credentials/detect.go` `DetectDefault` **[src]** (<https://github.com/googleapis/google-cloud-go/blob/main/auth/credentials/detect.go>).
  - The error text when nothing is found is `credentials: could not find default credentials. See ... for more information`.
- ADC credentials are separate from `gcloud auth login` credentials **[doc]**. A user can be logged into gcloud and still have no ADC file. Tell the user which one is missing.
- **Gotcha: `CLOUDSDK_CONFIG` is not honoured for the ADC file path [src].**
  - `credsfile.GetWellKnownFileName` uses `$HOME/.config/gcloud` (or `%APPDATA%`). It has no `CLOUDSDK_CONFIG` handling (<https://github.com/googleapis/google-cloud-go/blob/main/auth/internal/credsfile/credsfile.go>).
  - A grep of `golang/oauth2/google/{google,default,sdk}.go` also found no `CLOUDSDK_CONFIG`.
  - If the user relocates the gcloud config dir, they must set `GOOGLE_APPLICATION_CREDENTIALS`.
  - The gcloud docs say `CLOUDSDK_CONFIG` overrides the config dir for gcloud itself (<https://docs.cloud.google.com/sdk/gcloud/reference/topic/configurations>).
- **Project ID:**
  - `bigquery.DetectProjectID` reads `GOOGLE_CLOUD_PROJECT`, then `creds.ProjectID` from ADC, then errors **[src]** (<https://github.com/googleapis/google-cloud-go/blob/main/internal/detect/detect.go>).
  - It does **not** read gcloud's `core/project`.
  - Because CONTEXT.md makes the execution project a config-file value, bqt should not use `DetectProjectID`.
  - If a fallback to gcloud config is wanted, shell out to `gcloud config get-value project` (this honours `--configuration`, `CLOUDSDK_ACTIVE_CONFIG_NAME` and `CLOUDSDK_CONFIG` for free) rather than parsing the INI files. The Go client has no gcloud config reader.
- **Quota project [src]** (`google-api-go-client/internal/creds.go` `GetQuotaProject`, <https://github.com/googleapis/google-api-go-client/blob/main/internal/creds.go>). Precedence:
  1. `option.WithQuotaProject`
  2. env `GOOGLE_CLOUD_QUOTA_PROJECT`
  3. `quota_project_id` in the ADC JSON (set by `gcloud auth application-default set-quota-project`)
  - The result is sent as the `X-Goog-User-Project` header.
  - Troubleshooting doc: user-credential ADC without a quota project can fail with "user credentials not supported / API not enabled / no quota project". Remedies: `gcloud auth application-default set-quota-project P`, or send `x-goog-user-project`. The caller needs `serviceusage.services.use` (<https://docs.cloud.google.com/docs/authentication/troubleshoot-adc>).
  - `gcloud config set billing/quota_project` affects gcloud only **[doc]**, not Go ADC.
  - Passing `WithQuotaProject` to the client is incompatible with `WithHTTPClient` **[src]** (`internal/settings.go`).
- **Expired credentials:**
  - Refresh tokens stop working when revoked, unused for 6 months, or when an admin session-length limit is exceeded. A session-control end shows as `invalid_grant` with `error_subtype` such as `invalid_rapt`. Sessions run from 1 to 24 hours **[doc]** (<https://developers.google.com/identity/protocols/oauth2#expiration>).
  - Workspace-account ADC credentials expire with the Workspace session. The fix is to re-run `gcloud auth application-default login` **[doc]** (<https://docs.cloud.google.com/docs/authentication/reauthentication>).
  - `oauth2.RetrieveError` has `ErrorCode`, `ErrorDescription`, `Body` **[src]**. `error_subtype` is not a field, so look in `Body` (<https://github.com/golang/oauth2/blob/master/token.go>). The newer `auth.Error` type is also possible **[src]**, depending on the code path (`cloud.google.com/go/auth/auth.go`). Handle both with `errors.As` plus a string match.
  - Not found in the docs I fetched: the exact error strings the Go client prints for a revoked or expired user token. Capture them in a prototype by running `gcloud auth application-default revoke` on a throwaway credential, not the user's real one.

## Dry run

- `Query.DryRun = true`, then `Query.Run`, then `job.LastStatus().Statistics`. `Query.Read` fails on dry-run queries, and `Job.Status` on a dry-run job fails **[src]** (`bigquery/query.go`, field comment).
- Result: `status.Statistics.TotalBytesProcessed` (also `Details.(*QueryStatistics).TotalBytesProcessed`). Dry runs use no slots and are free **[doc]** (<https://docs.cloud.google.com/bigquery/docs/dry-run-queries>).
- `QueryStatistics.TotalBytesProcessedAccuracy` is one of `UNKNOWN`, `PRECISE`, `LOWER_BOUND`, `UPPER_BOUND` **[src]**.
- `QueryStatistics.Schema` is filled for a successful dry run of non-legacy SQL **[src]**. bqt can show result columns before running.
- `UndeclaredQueryParameterNames` lists undeclared `@params` found during a dry run **[src]**. Use this to discover placeholders in an arbitrary pasted query.
- **Limits [doc, first 100k of 138k chars of the page read]:**
  - Federated queries on external sources may report a lower bound of 0 bytes. They still cost money.
  - Row-level-security tables always report 0 bytes.
  - The sample sets `q.Location` to match the dataset.
- **Cost arithmetic [doc]** (<https://cloud.google.com/bigquery/pricing>):
  - On-demand is $6.25 per TiB in the table I read. The page lists many regions, so confirm the region before showing a currency figure.
  - The first 1 TiB per month is free, per account. A single query cannot know this, so label the estimate "before free tier".
  - Charges round up to the nearest MB, with a 10 MB minimum per referenced table and 10 MB per query.
  - Cache hits are not billed.
- Cached results [doc] (<https://docs.cloud.google.com/bigquery/docs/cached-results>):
  - Hits have `cacheHit=true` and zero bytes. They last about 24 hours, are per user and project, and need identical query text.
  - The page does not say how dry run interacts with the cache. Treat the dry-run figure as the worst case.
- **Scripts (multi-statement) dry run:** not covered in the pages I read. Check in a prototype.

## Job location

- If not set, BigQuery infers location from referenced datasets, then connections, then the destination table. Otherwise it uses the default location, else the `US` multi-region **[doc]** (<https://docs.cloud.google.com/bigquery/docs/locations>).
- Mismatch returns an error. Every dataset read or written must be in the job's location. Single region and multi-region do not match each other, even when contained, so a `US` job on a `us-central1` dataset is a "global query" **[doc]**.
- Dynamic SQL is not parsed up front, so it cannot drive location inference **[doc]**.
- Setting location is recommended: without it, queries might be temporarily stored in router logs while location is resolved **[doc]**.
- Go: `Client.Location` is the default for all job operations and a per-operation `Query.Location` overrides it **[src]**.
- For lookups and cancel, the job location is needed. `Client.JobFromIDLocation(ctx, id, location)` and `Client.JobFromProject(ctx, project, id, location)` exist **[src, pkg.go.dev]**.
- Reservations apply only if job location matches the reservation location **[doc]**.
- Cross-region queries are therefore not possible. The tool can only warn.

## Result retrieval

- **REST path (`Job.Read`, `jobs.getQueryResults`) [src]:**
  - The iterator calls `getQueryResults` with `pageToken` or `startIndex`, plus `maxResults` from the pager's page size (`bigquery/iterator.go` `fetchJobResultPage`).
  - `tabledata.list` and `getQueryResults` paginate by `pageToken` and `maxResults`. `tabledata.list` pages at more than 10 MB or more than `maxResults` rows and has no max row limit. `getQueryResults` returns up to 20 MB unless raised via support **[doc]** (<https://docs.cloud.google.com/bigquery/docs/paging-results>).
  - `RowIterator.TotalRows` is set after the first page. In the `jobs.query` fast path it may only be known after the first `Next` **[src]**.
- **Fast path [src]:** `Query.Read` uses `jobs.query` when no incompatible options are set (destination, priority other than interactive, legacy SQL, `JobTimeout`, custom `JobID`, etc.), and caches the first page. `Run` is not subject to this.
- **Storage Read path [src]:**
  - Enabled only by `EnableStorageReadClient`. The iterator is then built from the job's destination table. The Go source then ignores `PageInfo().Token` and `RowIterator.StartIndex` ("Currently pagination is not supported when the Storage API is enabled").
  - It reads in Arrow format, spawns up to `GOMAXPROCS` workers with buffered channels, and uses one stream for ordered queries (`ORDER BY`).
  - That makes the table view unable to jump to row N or go back without re-reading, and it holds background streams open.
  - `RowIterator.IsAccelerated()` tells which path is active.
  - In the `Query.Read` fast path, if the first response has more pages and the Storage client is enabled, the client discards the cached page and switches to Storage **[src]**.
- **Storage Read API facts [doc]** (<https://docs.cloud.google.com/bigquery/docs/reference/storage>):
  - Parallel streams, Avro or Arrow, snapshot reads, sessions last at least 6 hours.
  - Needs `bigquery.readsessions.create` on the project (role `bigquery.readSessionUser`).
  - Cannot read views or external tables.
  - The doc does not give a row or size threshold for choosing it over `getQueryResults`. There is no documented switch point. The decision here is based on access pattern, not size.
- **Pricing [doc]** (<https://cloud.google.com/bigquery/pricing>):
  - Storage Read is $1.10 per TiB, with the first 300 TiB per month free.
  - Reading "cached query results from temporary tables (anonymous dataset)" is listed as free.
  - There is no charge for retrieving query results through the console, API or clients.
  - Cross-location reads have network charges.
- **Memory:** `RowIterator` holds one page of rows at a time (`it.rows` is reset per page) **[src]**. The TUI controls memory by choosing page size and how many pages to cache. Nothing forces loading the whole result.
- **Result lifetime:** results live in an anonymous table for about 24 hours **[doc]** (cached results page). Within that time `Client.JobFromIDLocation` followed by `Job.Read` can re-open a History entry's results (same user) without re-running or re-billing **[inferred]**. After that, re-running is required.
- **Scripts:** `Job.Read` on a script job resolves the last child SELECT job (`resolveLastChildSelectJob`) only on the Storage path **[src]**. Behaviour for scripts on the REST path was not checked.

## Cancellation

- `jobs.cancel(jobId, projectId, location)` works on `RUNNING` or `PENDING` jobs, usually completes in under a minute, and is not guaranteed **[doc]** (<https://docs.cloud.google.com/bigquery/docs/managing-jobs>).
- Go `Job.Cancel(ctx)` "returns without waiting for cancellation to take effect... Cancelled jobs may still incur costs." Poll `Job.Status` to see termination **[src]**.
- The managing-jobs page says you can still be charged after cancelling, depending on the stage. The pricing page says: "Canceling a running query job might incur charges up to the full cost for the query if you let the query run to completion" **[doc]** (<https://cloud.google.com/bigquery/pricing>).
- A cancelled `ReadRows` on the Storage API still bills for data read **[doc]**.
- UX consequence: the Cancel action is "request cancel", then show a "cancelling..." state until the job reports `DONE`. The final status may be `DONE` with an error result, or a successful completion if it raced.

## Query parameters

- Syntax: `@name` (named) or `?` (positional). A query cannot mix both. Parameters cannot replace identifiers, column names or table names. Legacy SQL does not support parameters. The doc says a parameter value cannot be NULL, but the Go client has `Null*` types to send NULLs, so there is a documentation/client tension to test **[doc, src]** (<https://docs.cloud.google.com/bigquery/docs/parameterized-queries>).
- Go: set `Query.Parameters = []bigquery.QueryParameter{{Name: "x", Value: v}}` **[src]** (`bigquery/params.go`).
  - Inferred types: int kinds map to INT64, floats to FLOAT64, bool, string, `[]byte`, `time.Time` to TIMESTAMP, `*big.Rat` to NUMERIC, slices to ARRAY, and structs to STRUCT.
  - `civil.Date` and similar are handled in the client. For explicit typing use `*bigquery.QueryParameterValue` with `StandardSQLDataType{TypeKind: "..."}`. The comment recommends sending the value as a string for scalars.
  - `Name` must match the query case-insensitively.
- **Mapping for Favorites:**
  - Declare placeholders in the Favorite header, for example `-- param: start DATE = 2024-01-01`, and use `@start` in the SQL body. The `.sql` file stays valid BigQuery SQL with no custom templating, which suits agents that read the file.
  - bqt parses the declarations, prompts for values, and builds typed `QueryParameterValue`s.
  - For an undeclared `@x` in a pasted query, use the dry-run `UndeclaredQueryParameterNames` to prompt for it as a STRING by default.
  - Parameter values are not written to BigQuery logs **[doc]**. bqt's own History should decide whether to store them (privacy versus reproducibility).

## Reporting: History entry fields

All from `Job.Status(ctx).Statistics` after the job is done unless noted.

| Field | Go source | Notes |
|---|---|---|
| Job ID, project, location | `Job.ID()`, `ProjectID()`, `Location()` | Needed for re-open and cancel. |
| Status | `JobStatus.State`, `Done()`, `Err()` | `Wait` returns nil error even if `status.Err() != nil`, so check both **[src]**. |
| Error | `JobStatus.Err()`, `JobStatus.Errors []*Error` | Not all `Errors` entries are fatal **[src]**. |
| Created / started / ended | `JobStatistics.CreationTime`, `StartTime`, `EndTime` | Duration = `EndTime - StartTime` (queueing = `StartTime - CreationTime`). |
| Execution duration | `FinalExecutionDuration` | Final attempt only. |
| Bytes billed | `QueryStatistics.TotalBytesBilled` | The billed figure. |
| Bytes processed | `JobStatistics.TotalBytesProcessed` or `QueryStatistics.TotalBytesProcessed` | The estimate-comparable figure. |
| Slot time | `JobStatistics.TotalSlotDuration` or `QueryStatistics.SlotMillis` | |
| Cache hit | `QueryStatistics.CacheHit` | Billed bytes are 0 on a hit **[doc]**. |
| Statement type | `QueryStatistics.StatementType` | SELECT, DML, DDL, script, etc. |
| Rows affected | `QueryStatistics.NumDMLAffectedRows` | DML only. |
| Total rows | `RowIterator.TotalRows` | |
| Reservation / edition | `ReservationID`, `Edition` | Whether it was on-demand. |
| Referenced tables | `QueryStatistics.ReferencedTables` | Max 50 listed **[src]**. |
| Script info | `NumChildJobs`, `ParentJobID` | |

Notes:
- Statistics come from `Job.Status`, not `Job.LastStatus()`. `LastStatus()` returns the status as of job creation or the last `Status`/`Wait` call **[src]**.
- Query-specific numbers live in `JobStatistics.Details`, type-asserted to `*bigquery.QueryStatistics` **[src]**.
- Server-side history exists too: `INFORMATION_SCHEMA.JOBS` has `total_bytes_billed`, `total_slot_ms`, `creation_time`, `start_time`, `end_time`, `state`, `error_result`, `cache_hit`, `statement_type`, `query`, `job_id`, `parameters`, `user_email`, `project_id`. It keeps 180 days and needs a region qualifier **[doc, partial read]** (<https://docs.cloud.google.com/bigquery/docs/information-schema-jobs>).
  - The local History should be bqt's own store: it works offline, spans regions in one list, and avoids a billable query against `INFORMATION_SCHEMA`.
  - Whether querying `INFORMATION_SCHEMA.JOBS` is itself billable was not verified here.

## Open items worth a prototype (no billable queries needed)

- Exact error text from an expired or revoked user ADC token as seen through the Go client.
- Dry-run behaviour for multi-statement scripts, and whether dry run reports cache effects.
- Whether `option.WithQuotaProject` is actually required for BigQuery with user ADC (BigQuery may work without one). It is harmless if the user has `serviceusage.services.use` on the execution project, but it would error if they do not. Consider a fallback.
- `Job.Read` on script jobs using the REST path.

## Sources

- ADC: <https://docs.cloud.google.com/docs/authentication/application-default-credentials>, <https://docs.cloud.google.com/docs/authentication/set-up-adc-local-dev-environment>, <https://docs.cloud.google.com/docs/authentication/troubleshoot-adc>, <https://docs.cloud.google.com/docs/authentication/reauthentication>, <https://developers.google.com/identity/protocols/oauth2#expiration>
- gcloud config: <https://docs.cloud.google.com/sdk/gcloud/reference/topic/configurations>
- BigQuery docs: <https://docs.cloud.google.com/bigquery/docs/dry-run-queries>, <https://docs.cloud.google.com/bigquery/docs/locations>, <https://docs.cloud.google.com/bigquery/docs/paging-results>, <https://docs.cloud.google.com/bigquery/docs/reference/storage>, <https://docs.cloud.google.com/bigquery/docs/managing-jobs>, <https://docs.cloud.google.com/bigquery/docs/parameterized-queries>, <https://docs.cloud.google.com/bigquery/docs/cached-results>, <https://docs.cloud.google.com/bigquery/docs/information-schema-jobs>, <https://cloud.google.com/bigquery/pricing>
- Go client: <https://pkg.go.dev/cloud.google.com/go/bigquery>; source at <https://github.com/googleapis/google-cloud-go/tree/main/bigquery> (`bigquery.go`, `query.go`, `job.go`, `iterator.go`, `storage_iterator.go`, `storage_client.go`, `params.go`); auth at <https://github.com/googleapis/google-cloud-go/tree/main/auth>; <https://github.com/googleapis/google-api-go-client> (`internal/creds.go`, `internal/settings.go`, `option/option.go`, `transport/http/dial.go`); <https://github.com/golang/oauth2> (`token.go`).
