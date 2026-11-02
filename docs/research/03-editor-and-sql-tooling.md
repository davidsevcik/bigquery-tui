# Research 03: in-TUI SQL editor and SQL tooling (Go / Bubble Tea)

Ticket: [03-research-editor-and-sql-tooling](https://github.com/davidsevcik/bigquery-tui/issues/52). Researched 2026-10-12.

Method: library facts were checked against source checked out at the release tag (not READMEs of third parties), versions and dates against the Go module proxy (`proxy.golang.org`), and the two risky claims (memefish behaviour on BigQuery SQL, SQLite completion latency) were run as small experiments. Anything not verified is marked **unverified**.

## Recommendation (summary)

1. **Stack: Bubble Tea v2 line** (`charm.land/bubbletea/v2` v2.1.0, `charm.land/bubbles/v2` v2.2.1, `charm.land/lipgloss/v2` v2.0.6). v2 is stable and current; v1 (`github.com/charmbracelet/bubbletea` v1.3.10, Sept 2025) is the old line. Cost: `go.mod` of bubbletea v2.1.0 says `go 1.26.0`, so the project needs a Go 1.26+ toolchain.
2. **Editor: vendor (fork) the Bubbles v2 `textarea` into the repo and extend it**, rather than use it as-is, rely on a third-party editor, or write one from scratch. Upstream gives multi-line editing, scrolling, line numbers, selection and cursor-position APIs, but has **no undo/redo and no per-token highlighting hook**. Add (a) a per-line highlight hook (an open upstream PR already implements it, so we can carry that patch) and (b) a snapshot-based undo/redo stack. No vim modes in v1; `$EDITOR` hand-off is the answer for people who want them.
3. **Highlighting: write our own small BigQuery tokenizer** (about 200 lines) and use it for both highlighting and completion. Chroma has no BigQuery/GoogleSQL lexer and its generic SQL lexer gets BigQuery's backtick identifiers wrong.
4. **Completion context: token-stream scanner, not a full parser.** Classify the cursor position (table / column / function / keyword / dataset-qualifier / struct-field path) and collect aliases, CTE names and `UNNEST` aliases in scope by walking tokens with paren-depth tracking. This is what the closest prior art (lazysql) does, it never fails on broken input, and it needs no dependency. Keep `memefish` (pure-Go, partial-AST on errors, Spanner dialect) as an optional later upgrade for deriving CTE output columns; see the parser section for measured limits.
5. **Completion data: the local SQLite index is fast enough at keystroke level.** Measured 0.06 to 0.3 ms for scoped lookups on a synthetic 50k-table / 1.25M-column index (details below). Two traps to avoid: sorted unscoped LIKE (18 ms) and global distinct column names (390 ms).
6. **Popup: lipgloss v2 `Layer`/`Compositor`** to float a completion list over the editor; anchor it from the textarea cursor position.
7. **`$EDITOR` hand-off: keep it as a cheap escape hatch** via `tea.ExecProcess` with a temp `.sql` file; about 30 lines. Resolve `$VISUAL` then `$EDITOR` ourselves.

## 1. Bubbles `textarea`: capabilities and limits

Source read: `charm.land/bubbles/v2` v2.2.1 (2026-08-24), `textarea/textarea.go` (2115 lines). MIT licence. Repo: https://github.com/charmbracelet/bubbles (tag v2.2.1). v1 line: `github.com/charmbracelet/bubbles` v1.0.0 (2026-02-09).

| Feature | v2.2.1 textarea | Evidence |
|---|---|---|
| Multi-line editing, soft wrap, internal viewport scrolling that keeps the cursor visible | Yes | `repositionView`, `ScrollYOffset()`, `ScrollPercent()` |
| Line numbers | Yes, `ShowLineNumbers` defaults to true | `textarea.go` |
| Selection (by char/word/line, select all `ctrl+g`, copy `ctrl+shift+c`, delete selection) | Yes (new in v2; stores anchor and head) | `BeginSelection`, `HasSelection`, `DeleteSelection`, `CopySelection`, `Styles.Selection` |
| Undo / redo | **No.** No `undo`/`redo` identifier anywhere in `textarea.go` | grep of the tag |
| Per-token / syntax highlighting | **No hook.** Styling is only `Styles` (text, cursor line, line numbers, prompt, selection, placeholder) | `textarea.go` |
| Vim-style modes | No | no mode concept |
| Size limits | `maxLines = 10000` hard cap (inserts beyond are truncated; source comment: "in v2, make max lines dynamic"), `CharLimit` default 0 (unlimited), `MaxHeight` default 99 rows, `MaxWidth` default 500 cols, optional `DynamicHeight`/`MaxContentHeight` | `textarea.go` lines ~37, 327-333, 582-590 |
| Cursor position API | `Line()`, `Column()`, `LineInfo()` (soft-wrap segment info), `LineCount()`, `CursorUp/Down`, `SetCursorColumn`, `InsertString`, `SetValue`; `SetVirtualCursor(false)` makes `Cursor()` return a `*tea.Cursor` (real terminal cursor), position relative to the textarea so the caller adds its own screen offset | `textarea.go` |

Limits that matter for bqt:

- 10,000 lines is far above any realistic query, so size is a non-issue. The only past performance complaint I found (issue #301, "Performance of textarea") is closed (2025-01-22) and predates v2; no current evidence of a problem. Not benchmarked here (**unverified** for a 500-line query with highlighting).
- Undo/redo is the real functional gap for a query editor. The cheapest correct approach is a snapshot stack (value + cursor) pushed before each mutating key, with `SetValue` plus cursor-restore on undo; trivial inside a fork, awkward from outside because `SetValue` resets selection and cursor.
- Highlighting: the open upstream issue [bubbles#1043](https://github.com/charmbracelet/bubbles/issues/1043) (2026-08-26) and PR [bubbles#1030](https://github.com/charmbracelet/bubbles/pull/1030) (2026-08-04, 507 added lines, mergeable, **not merged**, no maintainer response yet) add `SetHighlighter(LineHighlighter)` where the hook returns style ranges per logical line and the renderer intersects them with soft-wrapped segments. The issue text spells out why post-processing `View()` output is fragile (tokens straddling soft wraps, wide graphemes shifting offsets, collisions with the cursor). That PR is the design to copy into our fork.

### Third-party editor components for Bubble Tea

| Project | What it is | State | Verdict |
|---|---|---|---|
| [`kujtimiihoxha/vimtea`](https://github.com/kujtimiihoxha/vimtea) | Vim-like editor (normal/insert/visual/command modes, undo/redo, line numbers, Chroma highlighting via `lexers`/`quick`) | Last commit 2025-03-29; on Bubble Tea **v1.3.4** / bubbles v0.20; no completion hook | Reject as a dependency: stale, v1 stack, vim-only editing model, no completion. Useful as a reference for an undo buffer. |
| [`knz/bubbline`](https://github.com/knz/bubbline) | Line editor forked from textarea: history, tab-completion callback with a menu built on `bubbles/list`, open-in-external-editor, conditional Enter | Last commit 2025-12-01; Bubble Tea v1.3.10 / bubbles v0.21 / lipgloss v1.1.0; no tags (pseudo-version only) | Reject as a dependency (v1 stack, REPL-oriented, bracketed paste listed as unsupported in its README). Good reference for the completion-menu `Values`/`Entry` interface and a textarea fork that stays maintainable. |
| `bubbles/textinput` suggestions | Built-in `SetSuggestions`, `AcceptSuggestion` (tab), `NextSuggestion` | In v2.2.1 | Single-line only, not usable for the editor. |
| Writing our own editor from scratch | | | Only worth it if vim modes become a goal. Rune buffer, soft-wrap, scrolling, selection and paste handling are exactly what the textarea fork already provides. |

Decision input for the spec: forking means owning ~2,000 lines of upstream code and re-basing on upstream changes. The risk is small because we only add two features (highlight hook, undo) and could drop the highlight patch if #1030 merges.

## 2. Syntax highlighting

- **Chroma** `github.com/alecthomas/chroma/v2` v2.27.0 (2026-06-17), actively maintained. Lexers in `lexers/embedded/` include `sql`, `mysql`, `postgresql_sql_dialect`, `materialize_sql_dialect`, `transact-sql`, `pl_pgsql`; **no BigQuery or GoogleSQL lexer** (grep of the tag). In the generic `sql.xml`: backtick is lexed as an `Operator` (so `` `my-proj.ds.tbl` `` is split into operator, name, punctuation, ...), strings are single-quote only with `''` doubling (BigQuery uses backslash escapes, `r''`/`b''` prefixes and `"""` triple quotes), and `UNNEST` is a keyword but BigQuery types like `INT64`, `STRING`, `STRUCT` are not in the builtin-type rule. A custom Chroma lexer is possible (XML or Go), but Chroma emits styled strings, which is the wrong shape for a range-based highlight hook and cannot be shared with completion.
- **Own tokenizer (recommended).** BigQuery lexical rules are small: `--`, `#` and `/* */` comments; `'`/`"`/triple-quoted strings with `r`/`b` prefixes and backslash escapes; backtick identifiers (which may contain `.` and `-` and span `project.dataset.table` as one token); numbers; `@param` and `@@sysvar`; operators; keywords; identifiers; dotted paths. The same token stream feeds highlighting (token kind to style range) and completion (context scanner). It must tolerate an unterminated backtick or string by extending to end of input, because that is exactly the state while the user is typing a table path.
- Prior art for this choice: lazysql ships its own `components/sql_lexer.go` and `tokenize()` rather than a library (https://github.com/jorgerojas26/lazysql).

## 3. SQL parsing / tokenizing in pure Go for BigQuery

What completion needs: (1) what kind of thing is at the cursor, (2) which tables/aliases/CTEs are visible there. Needs to work on text that is mid-edit and invalid most of the time.

| Option | Pure Go | BigQuery dialect | Tolerates incomplete input | Maintenance (module proxy) | Verdict |
|---|---|---|---|---|---|
| **Own token scanner** (lazysql-style) | Yes | Whatever we write | Yes by construction (never rejects) | n/a | **Recommended for v1** |
| [`cloudspannerecosystem/memefish`](https://github.com/cloudspannerecosystem/memefish) | Yes (go 1.25, stdlib + `x/text`; test deps only otherwise) | Spanner GoogleSQL, **not BigQuery**; its own AGENTS.md says never to treat BigQuery docs as a syntax source | Partial: returns an AST containing `Bad*` nodes plus errors ([error recovery doc](https://github.com/cloudspannerecosystem/memefish/blob/main/docs/content/error-recover/_index.md)) | v0.8.1 (2026-07-22), last commit 2026-09-27, pre-1.0 | Optional later upgrade |
| [`goccy/go-googlesql`](https://github.com/goccy/go-googlesql) | Yes (GoogleSQL compiled to wasm then transpiled to Go via wasm2go, no cgo) | The real GoogleSQL frontend that BigQuery uses | No. Its own test shows a syntax error returns an error with no AST (`_, err = ParseStatement("SELECT", ...)`) | v0.4.0 (2026-07-11), tracks upstream revision of 2026-01-31 | Reject for the editor; too heavy |
| [`goccy/go-zetasql`](https://github.com/goccy/go-zetasql) | **No, cgo + clang++** | BigQuery/Spanner | no | v0.5.5 (2023-12-18); README says archived, moved to go-googlesql | Reject |
| `vitessio/vitess` sqlparser, `xwb1989/sqlparser` (2018), `blastrain/vitess-sqlparser` (2020), `auxten/postgresql-parser` (2022), `pganalyze/pg_query_go` (cgo wrapper of libpg_query) | mixed | MySQL / PostgreSQL dialects | not evaluated | various, several stale | Reject: wrong dialect; backticks mean something different, no `UNNEST`/`STRUCT` semantics (dialect from module descriptions; **not deeply verified**) |

### Measured: memefish on BigQuery SQL

I built memefish (v0.8.1 sources, Go 1.27.2) and ran 18 statements, then 9 completion-shaped statements with a placeholder identifier spliced in at the cursor. Results:

- Parses cleanly: backtick `project.dataset.table` (including hyphenated project), CTEs, `UNNEST(...) AS e` joins, struct field access and `arr[OFFSET(0)]`, `SELECT * EXCEPT (...)`, `STRUCT<...>(...)` and array literals, wildcard-table backticks with `_TABLE_SUFFIX`.
- **Fails on BigQuery-only syntax**: `QUALIFY`, `PIVOT(...)`, `FOR SYSTEM_TIME AS OF`. Each yields a syntax error at that point; the AST for the part before it still exists.
- **Fails on the common mid-edit shapes** unless patched: `SELECT t. FROM \`p.d.t\` t` (the `.` swallows `FROM` as an identifier, so everything after is lost), `SELECT t.` at end of input, `FROM ` at end of input, unclosed backtick. These return `Bad*` nodes, so scope information after the cursor is gone.
- **With a placeholder identifier spliced in at the cursor** (`t.__c__`, `FROM __c__`, `WHERE __c__`, `JOIN u ON __c__`, `e.__c__` after `UNNEST ... AS e`), 7 of 9 shapes parsed with no errors (the cursor position was not inspected in the AST, only that parsing succeeded with no `Bad*` nodes). The two failures were the unclosed backtick (`` FROM `p.d.__c__ ``), which needs the tokenizer to close the quote first, and a query containing `QUALIFY`, which still errored after the placeholder was inserted.

Conclusion: a parser is not required for cursor classification or alias/CTE scope. The one thing a parser buys is accurate **CTE and subquery output columns** (so `c.` after `WITH c AS (SELECT a, b AS x ...)` can offer `a, x`). A scanner can approximate that by reading top-level select-list items (`name`, `expr AS alias`, `t.col`), and gives up on `*`. Suggested path: scanner first behind an interface; if CTE-column accuracy matters in practice, add memefish behind the same interface with the placeholder trick and fall back to the scanner on error. A cheap spike before committing: run memefish over the user's real favorites/history corpus to get a parse success rate (not done here).

### What the scanner must resolve (derived from the ticket and the experiments)

- **Cursor class**: after `FROM`/`JOIN`/`INTO`/`UPDATE`/`MERGE`/`TABLE` with a comma-list continuation (table position); inside a backtick token (split on `.` to know whether we are completing project, dataset or table); after `alias.` or `alias.struct.` (column / field-path position); inside `SELECT`/`WHERE`/`GROUP BY`/`ORDER BY`/`ON`/`HAVING`/`QUALIFY` (column or function position); after `(` of a function (argument).
- **Scope**: walk tokens with a paren-depth stack; per `SELECT` level record `FROM`/`JOIN` table refs with optional alias (`t`, `AS t`), `UNNEST(x) AS e`, subquery aliases `(...) AS s`, and `WITH name AS (...)` CTE names visible to later CTEs and the main query. Only references at the cursor's depth and its ancestors are visible. lazysql's `scanSQLContext` does the same walk (aliases map, CTE set, per-depth table refs, clause-boundary words) in 457 lines: https://github.com/jorgerojas26/lazysql/blob/main/components/sql_context.go
- **Placeholder trick** is useful even without a parser: classify by the tokens before the cursor and treat the partial word as the prefix.

## 4. Prior art for autocomplete

- **sqls** (https://github.com/sqls-server/sqls, Go LSP server, v0.2.48 on 2026-07-15, README says "no stable release"): `internal/completer/completer.go` parses the buffer with its own parser, walks nodes at the cursor position to produce a set of completion types (keyword, function, column, table, referenced table, view, subquery, subquery column, schema, join, join-on), extracts the tables defined at the position (`parseutil.ExtractTable`) and sub-query views, and detects a leading backtick to quote candidates. Supported databases listed are MySQL, PostgreSQL, SQLite3, MSSQL, H2, Vertica; no BigQuery. Useful as a taxonomy of completion contexts and for the "quote the candidate if the user typed a backtick" behaviour.
- **lazysql** (https://github.com/jorgerojas26/lazysql, tview-based so not reusable UI code, last commit 2026-10-12, ~4.4k stars): own lexer, single-pass token context scanner (above), an autocomplete popup with themeable styles, and **lazy column loading**: its README documents `schema_bulk_load_threshold` (default 200) so columns are bulk-loaded eagerly only for small visible schemas, otherwise table names are shown immediately and a table's columns are fetched on demand (cached or in-flight results reused). Relevant to bqt because column completion depends on column metadata being in the local index (or lazily fetched for a table the first time it appears in the `FROM`).
- **bubbline** (above): Tab-triggered completion callback returning categorised entries (`Values`/`Entry` with `Title()`/`Description()`), rendered as a navigable menu from `bubbles/list`.
- Python tools (pgcli, harlequin) were not examined; no primary-source reading done, so no claims here.

Popup rendering in Bubble Tea: lipgloss v2 (v2.0.6, 2026-08-11) has `NewLayer(content).X().Y().Z()` and `NewCompositor(layers...)` with z-ordering and hit testing (`layer.go`), which is the supported way to draw a list over the editor. On the v1 line the equivalent is the third-party `rmhubbert/bubbletea-overlay` (v0.6.10, published 2026-10-12). Anchor position: if the textarea is forked, expose the cursor's screen cell directly (row from `LineInfo().RowOffset` and `ScrollYOffset()`, column from `LineInfo().ColumnOffset`/`CharOffset`); otherwise use the `*tea.Cursor` returned when the virtual cursor is off.

## 5. Feasibility: completing from the local SQLite index

Experiment (script in the research scratchpad, not committed): `modernc.org/sqlite` v1.34.5 (the newest I could build under the local Go 1.22 toolchain; current is v1.60.1, 2026-09-29), `database/sql`, one connection, WAL, in-process file DB, darwin/amd64. Synthetic data sized above the stated real setup: 5 projects x 125 datasets x 80 tables = **50,000 tables** and 25 columns each = **1,250,000 column rows**. Indexes: `col(tbl_id)`, `col(name COLLATE NOCASE)`, `tbl(name COLLATE NOCASE)`, `tbl(project, dataset, name COLLATE NOCASE)`, FTS5 `trigram` table over table names. 200 repetitions per query; numbers are best / average.

| Query (all `LIMIT 50` unless noted) | Rows | Best | Avg |
|---|---|---|---|
| Columns of one table by `tbl_id`, name prefix `LIKE 'col_1%'` | 11 | 71 us | 75 us |
| Columns of one table, no prefix | 25 | 56 us | 57 us |
| Table prefix within one project+dataset | 20 | 101 us | 108 us |
| Table prefix across all 50k tables, `LIKE` with `NOCASE` index, no `ORDER BY` | 50 | 211 us | 224 us |
| Same, with `ORDER BY name` | 50 | **18.2 ms** | 18.7 ms |
| Table-name substring via FTS5 trigram (`'ssion'`) joined to `tbl` | 50 | 321 us | 336 us |
| Datasets of one project by prefix (`SELECT DISTINCT`) | 50 | 1.38 ms | 1.41 ms |
| `SELECT DISTINCT name` over **all** 1.25M columns by prefix | 11 | **383 ms** | 390 ms |

Findings:

- Everything the completer needs in the common case (columns of the tables in scope, tables within a dataset, tables by prefix) is well under 1 ms, comfortably inside a keystroke budget. FTS5 with the `trigram` tokenizer works in the pure-Go modernc build and gives sub-millisecond substring matches (it ran unmodified).
- Avoid: sorting a large unscoped prefix result (18 ms, still tolerable but wasteful), and global distinct column-name scans (390 ms, would visibly lag). Resolve by only completing columns from tables in scope; when none are in scope, offer nothing or a small precomputed "frequent columns" list rather than scanning `columns`.
- Not tested: `ORDER BY` using a lowercase `name_lc` column with a range predicate (`name_lc >= ? AND name_lc < ?`), which should avoid the sort penalty; ranking by recent use; struct/nested field paths (these need the columns table to store nested paths or a parent link); concurrency with a background sync writer (WAL is on, but contention was not measured). Real data distributions may differ, but these are B-tree lookups, so scaling is logarithmic.
- Execution model: scoped lookups can run inline in `Update`; anything unscoped should run in a `tea.Cmd` tagged with a generation counter so stale results are discarded when the user keeps typing.

## 6. `$EDITOR` hand-off in Bubble Tea

Source: `charm.land/bubbletea/v2` v2.1.0 (2026-10-08), `exec.go`; `github.com/charmbracelet/x/editor` v0.2.0 (2026-01-09), `editor/editor.go`.

- `tea.ExecProcess(cmd *exec.Cmd, fn func(error) tea.Msg) tea.Cmd` pauses the program, hands the terminal to the child process, and resumes when it exits; its doc comment names editors and shells as the use case. `tea.Exec` takes any `ExecCommand` (`Run`, `SetStdin/Stdout/Stderr`) when `exec.Cmd` is not enough. This is distinct from `tea.Suspend()`/`SuspendMsg`/`ResumeMsg`, which is Ctrl+Z job-control suspension.
- Recipe: write the current editor text to `os.CreateTemp("", "bqt-*.sql")`; build the command; `return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{path: tmp, err: err} })`; on `editorDoneMsg` read the file, remove it, and `SetValue` the result (skip if unchanged or on error). Child inherits the terminal because `osExecCommand.SetStdin` defaults to the program's input.
- `x/editor` helper: `editor.Command("bqt", path, editor.AtPosition(line, col))` builds the `*exec.Cmd` and adds the right "open at line/column" flags for vi/vim/nvim/nano/emacs/kak/gedit/code/hx. Caveats from reading the code: it reads **only `$EDITOR`** (not `$VISUAL`), splits it with `strings.Fields` (no shell quoting), falls back to `nano`, and returns an error when running under Snap. It is a small, separately versioned module on `go 1.24`; alternatively a 20-line helper of our own (`$VISUAL`, then `$EDITOR`, then a config key) avoids those caveats. Either way, the hand-off costs about 30 lines.
- Role: with a forked textarea that has undo, selection and highlighting, the hand-off becomes an escape hatch for vim/emacs users and for long queries. Cursor sync back into the in-TUI editor is not needed (replace the whole buffer).

## Sources

- Bubbles v2.2.1 source: https://github.com/charmbracelet/bubbles/tree/v2.2.1/textarea (tag read locally)
- Bubbles #1043 (highlight hook issue): https://github.com/charmbracelet/bubbles/issues/1043 ; PR #1030: https://github.com/charmbracelet/bubbles/pull/1030
- Bubble Tea v2.1.0 `exec.go`: https://github.com/charmbracelet/bubbletea/blob/v2.1.0/exec.go
- Lipgloss v2 `layer.go`: https://github.com/charmbracelet/lipgloss (v2.0.6; `NewLayer`, `NewCompositor`)
- `x/editor`: https://github.com/charmbracelet/x/tree/main/editor
- Chroma v2.27.0: https://github.com/alecthomas/chroma/tree/v2.27.0/lexers/embedded (`sql.xml`)
- memefish: https://github.com/cloudspannerecosystem/memefish (README, AGENTS.md, `docs/content/error-recover/_index.md`, `parser.go`, `lexer.go`)
- go-googlesql: https://github.com/goccy/go-googlesql (README "Resource footprint": about 63 MB test binary, ~76 s cold build with 5.6 GB peak RSS, ~150 ms `Init`, ~70 MiB heap; `parser_test.go`); go-zetasql (archived, cgo): https://github.com/goccy/go-zetasql
- sqls: https://github.com/sqls-server/sqls (`internal/completer/completer.go`)
- lazysql: https://github.com/jorgerojas26/lazysql (`components/sql_context.go`, README config table)
- vimtea: https://github.com/kujtimiihoxha/vimtea ; bubbline: https://github.com/knz/bubbline
- Versions and dates: `https://proxy.golang.org/<module>/@latest` for each module named above (queried 2026-10-12)
- Benchmark: `modernc.org/sqlite` v1.34.5, synthetic dataset as described in section 5 (run locally; reproducible from the description)
