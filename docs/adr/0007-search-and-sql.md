# 7. Search and SQL tooling: the decisions behind the code

Date: 2026-11-25
Status: accepted

## Context

Package `search` is catalog search: the query grammar, tiered ranking, Shard family grouping, the Explorer filter and the browse lists (spec section 8). Package `sqlx` is bqt's SQL language intelligence: a BigQuery tokenizer, a completion engine built on its token stream, and the functions that write table paths and the Explorer's `SELECT`. The decisions below were code comments until ADR 0004 moved them here.

## Decision

### Package boundaries

- **`search`**: pure core logic over an in-memory index of projects, datasets and tables (spec section 8). Columns are consulted only for the `col:` filter.
- **`sqlx`**: the tokenizer and the completion engine are pure functions that never fail on broken input, because a half-typed query is the editor's normal state. Every offset is a UTF-8 byte offset.
- **`search.Index`**: the immutable in-memory catalog, everything but columns. It is never changed once built; the Searcher replaces whole indexes.
- **`search.Searcher`**: safe for concurrent use. `SetIndex` and `Swap` replace the catalog atomically under its lock, and a reader takes the current index once per call.
- **`search.(*Searcher).compile`**: returns the index that the `col:` column sets were built against, alongside the compiled terms and usable filters. After a swap it can differ from the current one, so callers score against the index the sets point into.

### Query grammar and paths

- **`search.Parse`**: never fails. An incomplete or invalid filter is reported through its `Status`, not as an error, so a frontend can show it as a pending or red chip while the user types. A pasted path is taken from the raw text, not the lower-cased term, so it keeps its case: table identity is case-sensitive.
- **`search.SplitPath`**: splits `project.dataset.table`, optionally backticked. The last two segments are the dataset and the table and the rest is the project, because domain-scoped project ids contain dots themselves.
- **`search.BuildIndex`**: table identity is case-sensitive, because BigQuery allows `Orders` next to `orders`. The lower-case `byPathLC` map only serves typed or pasted paths, and marks a path that two tables share as ambiguous.
- **`search.HistoryQuery`**: a comma in `since:` means OR, so the condition keeps the earliest cutoff, which covers every listed value.
- **`search.maxAge`**: caps a `since:` age at 200 years, so multiplying it into a `time.Duration` cannot overflow and wrap into the future. `SinceTime` refuses a larger age.

### Ranking

- **`search.penName`**: a score is the field's base (`penName`, `penLabel`, `penDataset`, `penProject`, then `descScore`) plus tier × 10; lower is better. A hit on a table's own name always beats a hit that only comes from its labels, dataset or project, because otherwise an exact dataset name would lift every table in it above real name matches. Any of those beats a description-only hit.
- **`search.(*Searcher).Search`**: ranks by tier when the query has free terms and by path otherwise. Spans and column hits are computed only for the returned page, not for every hit, to keep a keystroke cheap.
- **`search.Searcher`** (`Recent`): must return recently opened object paths newest first. Search lists an empty query's results in that order without sorting them.
- **`search.hit`**: `i` indexes the table, dataset or project slice according to `kind`. For a Shard family hit it is the table index of the family's representative member.
- **`search.(*Index).passTable`**: in search, `label:` and `desc:` also accept the table's dataset labels and description. With `own` set (the Explorer filter) they match only the table's own.
- **`search.(*Index).containerHits`**: returns the projects and datasets whose names match. Where only tables would be listed (jump-to-table, an empty query) a Pending dataset is still listed, because it has no tables yet to stand for it.
- **`search.(*Searcher).materialize`**: sets a dataset's matched field and tier only when the query has terms, because a Pending dataset is also listed without any term.

### Search performance

- **`search.TestSearchLatencySmoke`**: the CI guard. At the target catalog size (50,000 tables) a keystroke must stay under 150 ms p95, five times the 30 ms budget, which catches order-of-magnitude regressions without flaking on slow runners. The race detector slows the search path several times over, so the test skips under `-race` and CI runs it in its own step without it.
- **`search.colBlob`**: every column as lower-case `path\x00description\n` records in one string, so a `col:` value is found with a handful of fast substring scans instead of a loop over more than a million columns.
- **`search.(*Index).datasetScores`**: computes each dataset's best score per term, from its own name, its project's name or its description, once per (term, dataset), because it is the same for every table of the dataset.
- **`search.(*Index).tableHits`**: scores the tables in scope that pass the filters in parallel chunks and concatenates them in chunk order, so the result is deterministic. It returns early, with a partial result, when the context is cancelled.

### Shard families in search

- **`search.(*Index).setFamily`**: ranks claims on a family path so it resolves to the right Newest shard. A family's own path beats a suffixed family's wildcard, which that family answers only when no other family owns it (`own` is false). When families of two table types share a name (`orders_*` tables and `orders_*` views) the path resolves to the TABLE family, which wildcard queries can read, whatever order the rows arrive in; the other is reached through its members. Equal claims keep the first.
- **`search.(*Searcher).ShardMembers`**: lists one family's members newest first: by date for a Date family, by creation time for any other. It takes the family's table type as well as its name (`orders_*`, `events_YYYYMMDD`), because families of two types can share a name; an empty type matches any.
- **`search.(*Searcher).Lookup`**: a family's path, its name or its wildcard `prefix*`, resolves to its Newest shard and sets `TableRef.Family`. A member's own path resolves to the member.
- **`search.(*Index).memberHit`**, **`search.(*filterRun).addTables`**: a hit on a table stands for the table itself, not its family, when it is in no family, a free term matches inside its varying part, or (under `col:`) it has its own schema. A term that matches only the shared prefix or suffix stands for the family. Search and the Explorer filter apply the same rule.
- **`search.(*Index).collapseShards`**: a family hit's representative is the Newest shard, because it carries the family's columns, but the hit is scored as its best member.

### Match spans and case folding

- **`search.(*Index).memberHit`** (bounds): the prefix and suffix bounds are computed on the lower-cased name, because match spans are offsets into it, and lower-casing can change the byte count (the Kelvin sign `K` lowers to an ASCII `k`).
- **`search.setSpans`**: drops the spans when lower-casing changed the text's byte length. Offsets are computed on the lower-case text and are valid for the original only when the length is the same, which holds for ASCII.

### Explorer filter

- **`api.SearchOptions`** (`Projects`), **`api.FilterOptions`** (`Projects`): limit results to a set of projects, their datasets and their tables; empty means every project. The set only narrows `Scope` and `SelectedOnly`, never widens them: a Scope or a hidden project outside it stays out. Search does not apply it to a pasted path, which stays an exact lookup, as with `SelectedOnly`; the Explorer filter applies it to a pasted path too, as it treats a hidden project.
- **`api.ProjectSet`**: the set a `Projects` option names (`SearchOptions`, `FilterOptions`, `CompleteOptions`). `Has` treats an empty set as every project; search, the Explorer filter and completion all test it through `Has`, so they agree on what empty means.
- **`search.(*Searcher).Filter`**: returns every object that passes the Explorer's in-place filter, best match first, or by path when the query has no free term. Unlike Search, free terms match only an object's own name, so a term that names a dataset does not keep the tables inside it. Table filters (`type:`, `col:`, `label:`, `desc:`) keep a table that passes them and any project or dataset that holds one; `p:` and `d:` only narrow by path.
- **`search.filterRun`** (stages): one Filter call's state, whose stages run in order: `passTables` fills `pass` and `own`, `addContainers` and `addTables` read them and append to `out`, and `matches` sorts `out` into the response.
- **`search.filterRun`** (`tableFilters`, `dsFilters`): `tableFilters` is set when the query has filters beyond `p:` and `d:`; a container is then kept only when it holds a passing table or passes the filters itself. `dsFilters` is set when there are filters beyond `p:`; a project is then kept only when it holds a kept dataset.
- **`search.newFilterRun`**: when the query is a pasted path that resolves to a table, it keeps only that table and the containers that hold it. It clears the compiled terms, so the path's text is not matched as terms, and turns on `tableFilters` and `dsFilters`.

### Sync health in browse lists

- **`search.(*Searcher).Projects`**: lists only the catalog's projects with their sync health. Selected projects that have not been synced yet are added by the engine, not here.
- **`search.(*Searcher).projectHealth`**: a location that holds Pending datasets but no unit state counts as a unit that never succeeded, and while any dataset of the project is pending the discovery unit's success does not count (spec 7.2), so the next start syncs the project again.

### SQL completion

- **`api.CompleteOptions`**: `Projects` limits the projects completion offers, with their datasets and tables, to some of the Selected projects, but not what the text names in full: the columns of a `project.dataset.table` are offered wherever the table is. `Execution` is where an unqualified `dataset.table` resolves; its datasets and tables are offered whether or not `Projects` names it. Empty means the shared Execution project.
- **`sqlx.(*completer).path`** (one segment): a name before a dot is looked up among `DefaultProject` as well as `Projects`, so `` `proj-b. `` offers the Execution project's datasets even when that project is not listed. It is not added to the project-name suggestions.
- **`sqlx.Complete`**: works on the token stream, not a parser, so it never fails on a half-typed query. The cursor is a byte offset. No candidates means the popup stays closed.
- **`sqlx.Catalog`**: the slice of the metadata index completion needs. Every lookup is scoped to one project, one dataset or one table, so completion stays fast on every keystroke; the interface deliberately offers no catalog-wide scan.
- **`sqlx.Catalog`** (`DefaultProject`): the project an unqualified `dataset.table` resolves to: the Execution project, as BigQuery resolves it for the job.
- **`sqlx.TableName`**, **`sqlx.(*completer).path`**: `Members` is set on a Shard family's wildcard (`Name` is `prefix*`). The members' names are read only once the typed text reaches past `Prefix` into the varying part, so a large family does not flood the popup.
- **`sqlx.fromOfFunction`**: FROM also appears inside calls such as `EXTRACT(x FROM y)`, `TRIM` and `SUBSTRING`; such a FROM is not a query's FROM clause.
- **`sqlx.inTableContext`**, **`sqlx.fromOfFunction`** (`;`): their walks back stop at `;`, because the tokenizer asks them over the whole text, and neither an unclosed call (`select trim(a;`) nor a FROM in an earlier statement may decide a later one. `Complete` and `analyze` pass one statement's tokens, so the stop changes nothing there.
- **`sqlx.(*scope).parseRef`**: the word under the cursor after a table reference is not its alias, because the user is still typing it and it must stay completable.
- **`sqlx.(*completer).finish`**: returns no candidates when the only one left equals the text it would replace up to the cursor, because typing a whole candidate is not a reason to keep the popup open.
- **`sqlx.dashedName`**, **`sqlx.Complete`**: GoogleSQL allows dashes in an unquoted table path (`acme-prod.sales.orders`), so in a table position the word under the cursor runs across dashes, both for the typed prefix and for the replace range. Anywhere else a `-` is a minus and the word stops at it: `a-b` in a select list is two words. `sqlx.dashJoined` is the one rule for what a dash joins; completion, the reference parser (`parseRef`) and the tokenizer all use it.
- **`sqlx.(*completer).add`** (`dashedStart`, `dashedEnd`): outside a table position, the completer keeps the span of the dashed name around the word under the cursor. A candidate that begins with the span's text before the word (an alias named after a dashed path) replaces from the span's start. One that spells the whole span replaces through its end too. Either way, accepting a candidate never writes text that is already there a second time. A candidate that spells neither replaces only the word, as after any minus.

### Tokenizer

- **`sqlx.Tokenize`**: whitespace yields no token, every other byte belongs to exactly one token, and tokens are in order; callers rely on this coverage. A word right after a dot is an identifier (a field or path segment) whatever it is spelled like, so a column named like a keyword is not highlighted or parsed as one.
- **`sqlx.continuesDashedPath`**: digits right after a dashed name in a table position are a name part, not a number, because GCP's generated project IDs end in digits (`my-proj-412345.sales`). They are lexed as an identifier, so they are highlighted as the path and the `.` after them stays a path separator. Digits after a `-` anywhere else are a number (`x-1.0`). It rejects a `-` with no name part right before it before walking back at all: `Tokenize` runs on every edit, and SQL with many negative literals (`VALUES (1,-1),(2,-2)`) would otherwise tokenize in quadratic time. It asks `inTablePath` over the whole text, not one statement (see the `;` entry under SQL completion).
- **`sqlx.scanString`**: an unterminated string literal ends at the line end, a triple-quoted one at the text end, so a half-typed string does not swallow the rest of the query. A backslash still guards the next quote in a raw string: the raw string keeps the backslash, but `r'\''` does not end at the escaped quote.
- **`sqlx.Params`**: lists the named query parameters (`@name`) in order of first use and skips `@@name`, which is a BigQuery system variable, not a parameter.
- **`sqlx.keywords`**: these words are tokenized as keywords, so they are highlighted as keywords and never taken for a table alias; `quoteColumn` also backticks them.

### Writing table paths and SELECT

- **`sqlx.QuoteTable`**: writes a table path as GoogleSQL, the whole path in one pair of backticks, as written, and never a wildcard, because a plain table's name may hold `YYYYMMDD` itself (`report_YYYYMMDD_backup`). Only `QuoteFamily`, given a family, writes a wildcard.
- **`sqlx.QuoteFamily`**: writes a Shard family as its wildcard `` `p.d.prefix*` ``, because BigQuery allows the `*` only at the end of a table name. With a nil family, when only the family's name is known, the prefix is the text before the name's marker.
- **`sqlx.FamilyPrefix`**: returns the text before a family name's `*` or `YYYYMMDD` marker. Use it only on names known to be Shard family names, because a plain table name may hold `YYYYMMDD` and would be misread as a family.
- **`sqlx.FamilyParts`**: splits a family name into the text before and after its marker, so a member is prefix + varying part + suffix. A `*` is the marker when there is one, since a BigQuery table name cannot hold `*`; otherwise it is the first `YYYYMMDD`. A Date family whose fixed parts hold `YYYYMMDD` themselves can be read two ways, so callers that have the family use its `Prefix` and `Suffix` instead.
- **`sqlx.SelectStar`**: writes the Explorer's `s` query, one clause per line, with a WHERE condition the table accepts. A Shard family (the path is the family path and `Table.Shard` is set) reads its Newest shard through its wildcard with `_TABLE_SUFFIX` pinned to that one shard; a table that requires a partition filter gets a one-day filter on its partition column; everything else gets `true`. It returns the condition's byte offset, where the cursor lands.
- **`sqlx.partitionFilter`**: filters one day on the first top-level partitioning column. A table that requires a partition filter but has no such column is assumed to be partitioned by ingestion time and gets a `_PARTITIONTIME` filter.

## Consequences

- Both packages must stay pure and must never fail on partial input: `Parse` reports status, `Complete` and `Tokenize` work on any text. Error returns for a half-typed query would break the chips and the popup.
- Table identity is case-sensitive everywhere: paths keep their case through parsing, indexing and lookup, and only typed or pasted paths go through the lower-case map. Any code that computes offsets on lower-cased text must check that the byte length survived.
- A Shard family is one result unless a term reaches into a member's varying part; search, the Explorer filter, lookup and completion each apply that rule, and a change to it must reach all four. Only a known family becomes a wildcard.
- The 30 ms keystroke budget rests on precomputation and laziness: the immutable index, the column blob, per-dataset scores, parallel chunks and spans for the returned page only. A change that scores per column or per hit outside the page will show in the CI latency guard.
- Completion lookups are scoped by design; a feature that needs a catalog-wide scan belongs in `search`, not in `sqlx.Catalog`.
