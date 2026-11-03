# Research 01: Metadata enumeration for sync

Ticket: [01-research-metadata-enumeration](https://github.com/davidsevcik/bigquery-tui/issues/50)
Researched: 2026-10-12. Sources are official Google Cloud docs (fetched from `docs.cloud.google.com`, the new home of `cloud.google.com/bigquery/docs`) and the `cloud.google.com/go/bigquery` source at `main` (latest tag `bigquery/v1.85.0`). Nothing was run against any GCP project. Items marked **UNVERIFIED** are not stated in the docs and need a check against a scratch project before they are locked into the spec.

## Recommendation

**Verdict on the seed: the claim holds, with corrections.** The current docs say `INFORMATION_SCHEMA.TABLES`, `COLUMNS`, `COLUMN_FIELD_PATHS` and `TABLE_OPTIONS` can be queried at project-and-region scope (`` `proj`.`region-X`.INFORMATION_SCHEMA.TABLES ``) as well as at dataset scope. So "run TABLES/COLUMNS once per project and region" is valid. What the seed missed:

1. `TABLES` has no `last_modified_time`, and `COLUMNS` has no `description`. Descriptions and nested fields live in `COLUMN_FIELD_PATHS` and `TABLE_OPTIONS`.
2. Region qualifiers are non-hierarchical (`region-us` does not include `us-central1`), a query can only run in one location, and you cannot know which regions to ask about until you list datasets (`SCHEMATA` without a region silently means US).
3. Region-scoped queries may include rows from recently deleted datasets still inside their time-travel window, and they exclude hidden datasets.

**Proposed sync algorithm (per configured project):**

1. **Discover datasets and their locations** with one paginated REST `datasets.list` call per project (not the Go helper iterator; see "Go client"). The response carries `datasetReference`, `location`, `labels`, `friendlyName` for each dataset, and an `unreachable[]` list. This needs no `bigquery.jobs.create` and is not a query. Apply the ignore file here.
2. **Group datasets by `(project, location)`.** For each group run these region-qualified queries as jobs with `Location` set to that region, billed to the Execution project:
   - `region-X.INFORMATION_SCHEMA.TABLES`: `table_schema, table_name, table_type, creation_time` (add `ddl` only for views, or fetch lazily; see caveats).
   - `region-X.INFORMATION_SCHEMA.TABLE_OPTIONS` filtered to `option_name IN ('description','friendly_name','labels','expiration_timestamp')`: table descriptions and labels.
   - `region-X.INFORMATION_SCHEMA.COLUMNS`: `ordinal_position, data_type, is_nullable, is_partitioning_column, clustering_ordinal_position` (top-level columns only).
   - `region-X.INFORMATION_SCHEMA.COLUMN_FIELD_PATHS`: `field_path, data_type, description` (all nested paths plus column descriptions).
   - Optional `region-X.INFORMATION_SCHEMA.TABLE_STORAGE`: `storage_last_modified_time`, `deleted`, and size/row counts. Region-only and delayed (see delta detection).
   - Optional `region-X.INFORMATION_SCHEMA.SCHEMATA_OPTIONS`/`SCHEMATA`: dataset description, `last_modified_time`.
   That is 4 to 6 queries per `(project, region)` pair. For 5 projects with maybe 1 to 3 regions each, roughly 20 to 90 queries per full sync, versus 4,000+ if done per dataset (250 to 1,000 datasets).
3. **Fall back per dataset** when a region query fails with a permission error: re-run the same views with the dataset qualifier (`proj.dataset.INFORMATION_SCHEMA.X`) for datasets the user can read, or `tables.list` plus `tables.get(view=BASIC)` as the SQL-free path. Mark the project/dataset as degraded in the store rather than failing the sync.
4. **Do a full re-snapshot and diff locally** instead of building delta detection (next point). A full sweep costs a handful of minimum-size queries, which is cheaper than the engineering needed to be incremental.

**Delta detection: there is no reliable cheap "what changed" signal in SQL.** Do not design sync around one. Details below; the practical answer is to re-pull everything per `(project, region)` and let the store diff (hash of each table's schema/description), which also handles deletions by absence.

**Use REST (not SQL) for:** dataset discovery (above), and optionally per-table refresh on demand (user presses refresh on one table: `tables.get` with `view=BASIC`, no job needed). Do not enumerate all tables via `tables.list` + `tables.get`: that is one call per table (thousands to tens of thousands of calls) when four region queries return everything.

## Facts

### 1. Which views are dataset-scoped vs region-scoped

From the INFORMATION_SCHEMA intro page ([Introduction to INFORMATION_SCHEMA](https://docs.cloud.google.com/bigquery/docs/information-schema-intro)):

- "An INFORMATION_SCHEMA view needs to be qualified with a dataset or region." With neither, you get an error.
- Dataset qualifier is supported by: `COLUMNS`, `COLUMN_FIELD_PATHS`, `MATERIALIZED_VIEWS`, `PARAMETERS`, `PARTITIONS`, `ROUTINES`, `ROUTINE_OPTIONS`, `TABLES`, `TABLE_OPTIONS`, `VIEWS`.
- Region qualifiers (`region-REGION`, "any dataset location name") are supported by all views except `PARTITIONS` and the `SEARCH_INDEX*` views. So `TABLES`, `COLUMNS`, `COLUMN_FIELD_PATHS`, `TABLE_OPTIONS` support both scopes.
- Region qualifiers "aren't hierarchical": the `EU` multi-region does not include `europe-*` regions, and `US` does not include `us-*` regions.
- All views accept a project qualifier: `` myProject.`region-us`.INFORMATION_SCHEMA.TABLES `` and `myProject.myDataset.INFORMATION_SCHEMA.TABLES`. Default is the project the query runs in.

Per-view scope tables (each view's own page has a "Scope and syntax" table):

| View | Dataset scope | Region scope | Notes | Source |
|---|---|---|---|---|
| `TABLES` | yes | yes (project level) | No `last_modified_time` column. Has `table_type`, `creation_time`, `ddl`. | [TABLES](https://docs.cloud.google.com/bigquery/docs/information-schema-tables) |
| `COLUMNS` | yes | yes | Top-level columns only; **no description column**. | [COLUMNS](https://docs.cloud.google.com/bigquery/docs/information-schema-columns) |
| `COLUMN_FIELD_PATHS` | yes | yes | One row per top-level column and per nested path; has `description`. | [COLUMN_FIELD_PATHS](https://docs.cloud.google.com/bigquery/docs/information-schema-column-field-paths) |
| `TABLE_OPTIONS` | yes | yes | One row per option; `description`, `friendly_name`, `labels`, `expiration_timestamp`, `tags`... | [TABLE_OPTIONS](https://docs.cloud.google.com/bigquery/docs/information-schema-table-options) |
| `SCHEMATA` | no | region only ("Project level", default US if unqualified) | `creation_time`, `last_modified_time`, `location`, `ddl`. | [SCHEMATA](https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata) |
| `SCHEMATA_OPTIONS` | no | region only (default US) | Dataset `description` etc. as option rows. | [SCHEMATA_OPTIONS](https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata-options) |
| `TABLE_STORAGE` | no | region only: "Queries against this view must include a region qualifier" | Has `storage_last_modified_time`, `deleted`, byte/row counts. | [TABLE_STORAGE](https://docs.cloud.google.com/bigquery/docs/information-schema-table-storage) |

Where each wanted field lives:

| Wanted | Where |
|---|---|
| table name, type (`BASE TABLE`, `VIEW`, `MATERIALIZED VIEW`, `EXTERNAL`, `CLONE`, `SNAPSHOT`) | `TABLES.table_type` |
| table creation time | `TABLES.creation_time` |
| table description / friendly name / labels | `TABLE_OPTIONS` (`option_name='description'` etc.) |
| column name, order, type, nullability, partition/clustering flags | `COLUMNS` |
| column description and nested field paths | `COLUMN_FIELD_PATHS` (`description`, `field_path`) |
| table last data write | `TABLE_STORAGE.storage_last_modified_time` (region only) |
| dataset description | `SCHEMATA_OPTIONS` or `datasets.get` (not in `datasets.list`) |

### 2. Discovering dataset location and query routing

- `datasets.list` returns, per dataset, `datasetReference`, `labels`, `friendlyName`, **`location`**, `type`, plus a top-level `unreachable[]` ("skipped locations that were unreachable"). It needs no specific IAM permission; results are filtered to datasets where the caller has `bigquery.datasets.get`. It excludes hidden datasets unless `all=true`. Pages via `maxResults`/`pageToken`. Source: [datasets.list](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/list).
- `SCHEMATA` also has `location` but is itself region-scoped, and "If you do not specify a regional qualifier, metadata is retrieved from the US region", so it cannot bootstrap region discovery. Source: [SCHEMATA](https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata).
- Job routing ([Locations: Specify locations / Default location](https://docs.cloud.google.com/bigquery/docs/locations)):
  - Location can be set via `@@location`, `bq --location`, or `jobReference.location` in the API. The REST field defaults to `US` ([JobReference](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/JobReference)).
  - If unspecified, the job location is inferred from referenced datasets; failing that, the configured default; failing that, `US` multi-region.
  - "BigQuery returns an error if the specified location does not match the location of the datasets in the request." For INFORMATION_SCHEMA the same rule appears as: `Table myproject: region-us.INFORMATION_SCHEMA.[VIEW] not found in location US` ([intro](https://docs.cloud.google.com/bigquery/docs/information-schema-intro)).
  - Single-region and multi-region locations do not match, even when nested (job in `US` referencing `us-central1` is a different location).
- A query against `region-X` INFORMATION_SCHEMA runs in region X, "which means that you can't write a single query to join data from views in different regions. To combine INFORMATION_SCHEMA views from multiple regions, read and combine the query results locally" ([intro](https://docs.cloud.google.com/bigquery/docs/information-schema-intro)). So the sync must issue one job per region and merge locally. This is the answer to "UNION ALL across regions": impossible for INFORMATION_SCHEMA.
- **Global queries** (cross-region SQL) exist but are Preview, need `bigquery.jobs.createGlobalQuery` (only BigQuery Admin has it), `ALTER PROJECT` opt-in options per region, and they copy data between regions. Not suitable for a read-only browsing tool; do not depend on them. Source: [Global queries](https://docs.cloud.google.com/bigquery/docs/global-queries).
- Execution project vs data project: the job runs in (and bills to) the Execution project, and the data project is the qualifier (`` `data_proj`.`region-X`.INFORMATION_SCHEMA.TABLES ``). The job location must match the region of the data, regardless of the execution project's own reservation locations. (Capacity-based reservations are regional, and a job outside the reservation's location falls back to on-demand pricing: [Locations](https://docs.cloud.google.com/bigquery/docs/locations).)

### 3. Alternatives to SQL (REST / Go client)

REST ([tables.list](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/list), [tables.get](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get)):

- `tables.list` (per dataset; needs `bigquery.tables.list`) returns for each table: `tableReference`, `friendlyName`, `type`, partitioning/clustering config, `labels`, `view`, `creationTime`, `expirationTime`, `requirePartitionFilter`, plus `totalItems`. **No description, no schema, no last-modified time.**
- `tables.get` (per table; needs `bigquery.tables.get`) has a `view` parameter: `BASIC` ("basic table information including schema and partitioning specification ... does not include storage statistics such as numRows or numBytes. This view is significantly more efficient and should be used to support high query rates"), `STORAGE_STATS` (default; adds `numBytes`, `numLongTermBytes`, `numRows`, `lastModifiedTime`), `FULL`. Table `description` and per-field descriptions are in the Table resource. So `lastModifiedTime` is only available with the heavier `STORAGE_STATS`/`FULL` views.
- `datasets.list`: see section 2. No dataset description and no `lastModifiedTime` in the list payload; `datasets.get` has `creationTime`, `lastModifiedTime`, `location` ([Dataset resource](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets)).
- Rate limits ([Quotas](https://docs.cloud.google.com/bigquery/quotas), "API requests" section): 100 API requests per second per user per method; 300 concurrent API requests per user; throttling above that. At 100 req/s, 20,000 `tables.get` calls would take at least ~3.5 minutes best case, plus per-call latency; bounded concurrency (say 16 to 32 workers) would take longer. Contrast with ~20 to 90 queries for the SQL path.
- Cost: the pricing page lists query processing (on-demand per TiB or slots) and storage as charges; list/get metadata calls are not listed as a billed operation ([Pricing](https://docs.cloud.google.com/bigquery/pricing)). That is an inference from absence; the "Free operations" table there lists load/copy/export/delete and not metadata reads. Quotas apply.

Go client (`cloud.google.com/go/bigquery`, source at `main`, [dataset.go](https://github.com/googleapis/google-cloud-go/blob/main/bigquery/dataset.go), [table.go](https://github.com/googleapis/google-cloud-go/blob/main/bigquery/table.go)):

- `Client.Datasets(ctx)` returns a `DatasetIterator` (`ListHidden`, `Filter` by label, `ProjectID`). Its `fetch` builds `&Dataset{ProjectID, DatasetID}` from each list item only. **The location, labels and friendly name returned by `datasets.list` are discarded.** Getting a location through the helper requires a `Dataset.Metadata(ctx)` call per dataset (one `datasets.get` each, 250 to 1,000 calls).
- `Dataset.Tables(ctx)` / `TableIterator.fetch` likewise keeps only `TableReference`, dropping type, creation time and labels. `Table.Metadata(ctx, bigquery.WithMetadataView(bigquery.BasicMetadataView))` maps to `tables.get` with `view=BASIC`; the source comment says BASIC "is a more efficient view to use for large tables or higher metadata query rates".
- Implication for the core library: to use `datasets.list` efficiently, call the generated service (`google.golang.org/api/bigquery/v2`, `Datasets.List(project)`) or the lower-level `bqs` client directly, not `DatasetIterator`. Query jobs: `Client.Location` sets the default job location and `Query.Location` overrides it per query ([bigquery.go](https://github.com/googleapis/google-cloud-go/blob/main/bigquery/bigquery.go), [query.go](https://github.com/googleapis/google-cloud-go/blob/main/bigquery/query.go)); a per-region query therefore needs `q.Location = region` (or one client per region).

### 4. Cost and slot implications of INFORMATION_SCHEMA queries

From [Introduction to INFORMATION_SCHEMA](https://docs.cloud.google.com/bigquery/docs/information-schema-intro), Pricing section:

- On-demand projects: "queries against INFORMATION_SCHEMA views incur a minimum of 10 MB of data processing charges". Capacity-based projects: they consume slots.
- Results are never cached, so you are charged every run. No storage fees for the views.
- Combined with the 1 TiB/month on-demand free tier ([Pricing](https://docs.cloud.google.com/bigquery/pricing)), 1 TiB is roughly 100,000 minimum-size (10 MB) queries. A full sync of ~90 queries is negligible; even syncing every few minutes would be cheap. Bytes actually processed for metadata views are typically small, so assume the 10 MB minimum dominates (**UNVERIFIED** for very large metadata sets; a dry-run in a scratch project reports `totalBytesProcessed` for the exact queries).
- A dry-run (`DryRun: true` on the job config) can be used to get the byte estimate for the exact queries before running them (general BigQuery feature; see the Go `Query.DryRun` field in query.go).
- Do not assume combining several views into one query saves money: the general on-demand rules say "a minimum 10 MB data processed per table referenced by the query, and with a minimum 10 MB data processed per query" ([Pricing](https://docs.cloud.google.com/bigquery/pricing), stated in the Omni section; the INFORMATION_SCHEMA intro states only the per-query minimum). Savings come from fewer queries overall, which the region scope already provides.

Batching many datasets into one query:

- Dataset-qualified views can be combined with `UNION ALL` in one query as long as all datasets are in the same location. Limits: "A query can reference up to 1,000 total of unique tables, unique views, unique UDFs and unique table functions" and unresolved GoogleSQL text is capped at 1 MB ([Quotas](https://docs.cloud.google.com/bigquery/quotas), query limits). 200 datasets x 1 view is well within both.
- But the region-qualified form needs no batching at all: one reference returns every dataset in the region for the project. It is simpler and does not blow the 1,000-resource limit. Use dataset-qualified `UNION ALL` only for the degraded permission path or to scope to a subset (for example, after the ignore file removes datasets and the user wants a partial refresh).
- Query results above 10 GB compressed response size need a destination table ([Quotas](https://docs.cloud.google.com/bigquery/quotas)); metadata results will not approach this, but `COLUMN_FIELD_PATHS` on very wide/nested schemas can be large (see caveats). Results are paged by the client iterator.

### 5. Delta detection

What each source offers:

| Signal | Granularity | Reality | Source |
|---|---|---|---|
| `TABLES.creation_time` | table | Detects new tables only; a table dropped and recreated gets a new value. | [TABLES](https://docs.cloud.google.com/bigquery/docs/information-schema-tables) |
| `TABLES` last-modified | n/a | **Does not exist** in the schema. | [TABLES](https://docs.cloud.google.com/bigquery/docs/information-schema-tables) |
| `TABLE_STORAGE.storage_last_modified_time` | table | "The most recent time that data was written to the table." Data writes only, so schema/description edits are likely **not** reflected (**UNVERIFIED**). View is not real-time (seconds to minutes delay; expiration-driven changes up to a day), region-only, includes temp/script datasets and soft-deleted tables (`deleted` flag). | [TABLE_STORAGE](https://docs.cloud.google.com/bigquery/docs/information-schema-table-storage) |
| `tables.get` `lastModifiedTime` (`STORAGE_STATS`/`FULL` view) | table | "The time when this table was last modified." Needs one call per table. Whether metadata-only edits (description, schema, labels) bump it is not stated (**UNVERIFIED**). | [Table resource](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables) |
| `SCHEMATA.last_modified_time` / dataset `lastModifiedTime` | dataset | "The date when this dataset was last modified" (REST) and "The dataset's last modified time" (SCHEMATA). The Go struct comment claims "When the dataset or any of its tables were modified", but the REST docs do not say tables count, so **do not rely on it as a cheap change flag without testing** (create a table, edit a column description, and add a column in a scratch dataset and watch whether it moves). | [Dataset resource](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets), [SCHEMATA](https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata), [dataset.go](https://github.com/googleapis/google-cloud-go/blob/main/bigquery/dataset.go) |
| `datasets.list` `etag` | page | Hash of the list page; changes when the dataset list changes. Detects dataset adds/removes, not table changes. | [datasets.list](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/list) |

Conclusion: nothing documented gives a reliable cheap "anything changed in this dataset/table?" bit that covers schema and description edits. Because a full per-region sweep is ~4 to 6 minimum-size queries, a full re-snapshot plus local diff is both cheaper to build and more correct. If the spec later wants incremental refresh for latency reasons, gate it on a scratch-project experiment of the dataset `lastModifiedTime` behavior and keep full re-snapshots as the fallback (for example, run a full sweep on explicit user refresh or daily).

### 6. Permission failure modes

Permission matrix (from each view's page):

| Operation | Permission needed | Notes |
|---|---|---|
| Any SQL query | `bigquery.jobs.create` on the **Execution project** | Failure: `Access Denied: Project [project_id]: User does not have bigquery.jobs.create permission in project [project_id].` ([Troubleshoot queries](https://docs.cloud.google.com/bigquery/docs/troubleshoot-queries)). This is independent of permissions on the data project. |
| `TABLES`, `TABLE_OPTIONS` | `bigquery.tables.get`, `bigquery.tables.list`, `bigquery.routines.get`, `bigquery.routines.list` (e.g. `roles/bigquery.metadataViewer`, `dataViewer`) | Dataset scope: "you must have permissions for the dataset". Region scope: "you must have permissions for the project". |
| `COLUMNS`, `COLUMN_FIELD_PATHS` | `bigquery.tables.get`, `bigquery.tables.list` | Same dataset/project wording. |
| `SCHEMATA`, `SCHEMATA_OPTIONS` | `bigquery.datasets.get` at project level | |
| `TABLE_STORAGE` | `bigquery.tables.get`, `bigquery.tables.list`, on the project (`roles/bigquery.metadataViewer`) | |
| `datasets.list` | none specific; results filtered to datasets where caller has `bigquery.datasets.get` | Cannot "fail" on a dataset level, it just omits what you cannot see. No `jobs.create` needed. |
| `tables.list` | `bigquery.tables.list` on the dataset | |
| `tables.get` | `bigquery.tables.get` on the table | |

Consequences for degradation:

- The docs state permission scope differences between dataset-qualified and region-qualified queries ("for the dataset" vs "for the project") but do not document the exact error text or whether a region query silently omits datasets the caller cannot read versus failing outright. Treat both as possible. **UNVERIFIED**: the exact error string and behavior for a region-qualified query when the caller has dataset-level grants only. Test with a user holding only a dataset-level `dataViewer` role in a scratch project.
- A project-level permission failure on a region query should degrade to per-dataset queries (dataset-level grants suffice for dataset-qualified views) and, if that also fails for a dataset, record that dataset as `inaccessible` and move on. Because `datasets.list` already filters to datasets the caller can `get`, the dataset list is the authoritative set to attempt.
- Quota/throttling errors are separate: 403 quota-exceeded and `rateLimitExceeded` styles are documented under [Troubleshoot queries](https://docs.cloud.google.com/bigquery/docs/troubleshoot-queries); the Go client retries transient errors internally (`runWithRetry`), but the sync should add its own bounded retry/backoff.
- `unreachable[]` in the `datasets.list` response signals locations that were skipped; surface it as a per-project warning, not a failure.
- The INFORMATION_SCHEMA docs note that permissions are checked at the parent level, not per row, "any deny policy (preview) that conditionally targets an individual row using tags is ignored" ([intro](https://docs.cloud.google.com/bigquery/docs/information-schema-intro)). Minor, but means listing can show metadata that a row-level tag-based deny would hide in other surfaces.

### 7. Large-table and wide-schema caveats

- A table can have up to 10,000 columns, "including nested and repeated columns" ([Quotas](https://docs.cloud.google.com/bigquery/quotas), table limits). `COLUMNS` returns one row per top-level column; `COLUMN_FIELD_PATHS` returns one row per top-level column plus one per nested path ([COLUMN_FIELD_PATHS](https://docs.cloud.google.com/bigquery/docs/information-schema-column-field-paths)). A table with 10,000 leaf fields therefore yields up to 10,000 rows there. A region-wide `COLUMN_FIELD_PATHS` for a project with many wide nested tables can reach hundreds of thousands to millions of rows (**sizing estimate, not from docs**); stream the iterator into SQLite in a transaction and do not hold it in memory.
- The nested `data_type` string in `COLUMNS` already encodes the whole STRUCT/ARRAY shape, so field paths are only needed for per-field descriptions and for searching nested field names. Option: store `COLUMNS` for everyone and pull `COLUMN_FIELD_PATHS` filtered to rows with a non-null `description` or `field_path != column_name`.
- `TABLES.ddl` is the full `CREATE` statement; for views it contains the view SQL and for wide tables it lists every column. Avoid `SELECT *`; Google also recommends listing columns explicitly because view schemas can change (warning on every view page).
- Region-qualified results can include metadata "from resources in that region from deleted datasets that are within your time travel window" ([intro, Limitations](https://docs.cloud.google.com/bigquery/docs/information-schema-intro)). Filter results to the dataset list from `datasets.list`. Hidden datasets (including `_script...`/`_anonymous...` temp datasets) are not in INFORMATION_SCHEMA views per the same page, but `TABLE_STORAGE` explicitly includes temp tables in generated-name datasets ([TABLE_STORAGE](https://docs.cloud.google.com/bigquery/docs/information-schema-table-storage)), so filter on the dataset list there too.
- View names are case-sensitive in the SQL (`INFORMATION_SCHEMA.TABLES`); dataset and table name case-sensitivity depends on the dataset's `IsCaseInsensitive` setting (Go: `DatasetMetadata.IsCaseInsensitive`). Store IDs exactly as returned.
- Views and materialized views appear in `TABLES` with `table_type` `VIEW` / `MATERIALIZED VIEW`; external tables appear as `EXTERNAL` and may have descriptions/columns from the external schema. Clones and snapshots report storage as if complete tables (`TABLE_STORAGE` note), so size figures are over-estimates there.

## Implications for other tickets (summary)

- **Sync/store design:** key everything by `(project, location, dataset, table)`; sync unit is `(project, region)`; store dataset location from `datasets.list`; full re-snapshot plus local diff is the baseline, no reliance on a change flag.
- **Core library:** needs a thin metadata port with two implementations (SQL region sweep, REST fallback); do not use the Go `DatasetIterator` for location; run jobs with explicit `Location`.
- **Config:** Execution project must have `bigquery.jobs.create`; job region is derived per dataset group during sync (the single "job region" config value applies to user queries, not to sync); the ignore file is applied after `datasets.list`.
- **Testing:** a fake of the metadata port must model region grouping and per-project/dataset permission failures.
- **Open verification items (scratch project, not the user's real projects):** exact error and partial-result behavior of region-qualified views with dataset-only grants; whether dataset `lastModifiedTime` or `tables.get.lastModifiedTime` move on metadata-only edits; real `totalBytesProcessed` of the region sweep via dry-run.

## Sources

- https://docs.cloud.google.com/bigquery/docs/information-schema-intro
- https://docs.cloud.google.com/bigquery/docs/information-schema-tables
- https://docs.cloud.google.com/bigquery/docs/information-schema-columns
- https://docs.cloud.google.com/bigquery/docs/information-schema-column-field-paths
- https://docs.cloud.google.com/bigquery/docs/information-schema-table-options
- https://docs.cloud.google.com/bigquery/docs/information-schema-table-storage
- https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata
- https://docs.cloud.google.com/bigquery/docs/information-schema-datasets-schemata-options
- https://docs.cloud.google.com/bigquery/docs/locations
- https://docs.cloud.google.com/bigquery/docs/global-queries
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/list
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/list
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables
- https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/JobReference
- https://docs.cloud.google.com/bigquery/quotas
- https://docs.cloud.google.com/bigquery/pricing
- https://docs.cloud.google.com/bigquery/docs/troubleshoot-queries
- https://github.com/googleapis/google-cloud-go/blob/main/bigquery/dataset.go
- https://github.com/googleapis/google-cloud-go/blob/main/bigquery/table.go
- https://github.com/googleapis/google-cloud-go/blob/main/bigquery/bigquery.go
- https://github.com/googleapis/google-cloud-go/blob/main/bigquery/query.go
