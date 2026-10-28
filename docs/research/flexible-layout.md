# Flexible pane layout and replacing the preview

Research note, 2026-10-24; updated 2026-10-26 with the user's decisions (sections 6–10 are new). Question: what would it take to (1) let the four panes (explorer, schema, editor, results) be arranged freely, side by side or stacked, and (2) remove the preview pane and add a shortcut that puts `SELECT * FROM <table>\nLIMIT 100` into the editor? No code was changed.

Citation style: repo claims are `path:line` at commit `5823ec3`. Upstream claims name the file and symbol in the module source at the exact versions in `go.mod` (downloaded with `go mod download` because the local module cache was empty), or a URL to the official source or docs.

## Decisions (2026-10-26)

The user answered the open questions of the first version of this note:

1. **Keep the free sample** (`tabledata.list`), but show it **in the Results pane**, not in a preview pane of its own. Section 6 has the design.
2. **The `SELECT * FROM <table>\nLIMIT 100` shortcut always opens a new buffer.** The note was to suggest the best key; section 7 does.
3. **Suggest alternative layouts.** Section 8.
4. **No config-file layout.** Layouts are switched only inside the app. Option 3 (section 4) is dropped, and so is `[tui] layout = …`.
5. **Research hiding panes and mouse resizing.** Section 9.
6. **The pane arrangement is called "layout".** The existing Miller/tree/flat styles (`L`) get a new name. Section 10.

## Decisions (2026-10-28)

Settled with the user in a grilling session. These supersede the recommendations below wherever they differ. The model is recorded in ADR 0002 (`docs/adr/0002-panes-arranged-by-a-split-tree.md`), and the target behaviour is in the spec (`docs/spec/bqt-v1.md` sections 8.1, 8.4, 10.4 and 15).

- **Phases**, one issue and PR each:
  1. **Foundation:** the pane-rectangle function, Schema as its own pane, `classic` and `focus`, geometric focus, persistence, step-down and the Explorer-style rename.
  2. **Preview replaced:** the Sample in Results, `s`, and the partition-filter sync.
  3. **Rearranging:** `query`, `browse` and `tall`, plus the `ctrl+w` window commands.
  4. **Mouse.**
- **Presets:** `classic` (the default), `query`, `browse`, `tall` and `focus` first. `wide` and `results-right` come later. The status line shows `layout: <name>`, or `custom` after an edit.
- **Window commands:** as in section 8. Geometric focus prefers the most recently focused pane, then the best aligned. The layout and its edits are remembered in `state/tui.json`. Step-down is never saved.
- **Bringing panes back:** an action aimed at a hidden or off-screen pane brings it back. Running a query while a pane is maximised restores the layout.
- **Naming:** the explorer styles are renamed `columns`, `tree` and `list`. The old state values are still read.
- **Sample (`p`):** it replaces what Results shows, and the Run's result stays one keypress away. Focus stays where it was. It pages through the whole table, with no export for now. Views and external tables get a toast pointing at `s`. A shard family samples its newest shard.
- **SELECT (`s`):** it always opens a new buffer named after the table, with the cursor on the condition. Each clause is on its own line:
  ```sql
  SELECT *
  FROM `p.d.t`
  WHERE true
  LIMIT 100
  ```
  - Tables that **require** a partition filter get a one-day filter typed to the column (`_PARTITIONTIME` for ingestion-time tables, `BETWEEN 0 AND 0` for integer ranges).
  - Other partitioned tables keep `WHERE true`.
  - A shard family uses `_TABLE_SUFFIX = '<newest>'`.
  - Sync stores `require_partition_filter` (a `TABLE_OPTIONS` BOOL option), and the Schema pane shows it.
- **Schema pane:** it shows the Explorer's selection and does not follow the SQL. For views, the field list comes before the view SQL. Explorer `Enter` on a table moves focus to it.
- **Unverified:** whether `COLUMNS.is_partitioning_column` is `YES` for the `_PARTITIONTIME`/`_PARTITIONDATE` pseudo-columns. The docs only say they are hidden, with a NULL ordinal ([COLUMNS view](https://docs.cloud.google.com/bigquery/docs/information-schema-columns)). Phase 2 checks it live and falls back to `_PARTITIONTIME` when a table requires a filter but has no ordinary partitioning column.

## Summary and recommendation (revised 2026-10-26)

- **Today the screen is two screens, not a layout.** `render` draws either the explorer with its detail panes, or the editor above the results, chosen by `m.zone` (`internal/tui/views.go:94-103`, and the spec at `docs/spec/bqt-v1.md:421`). The explorer and the editor are never on screen together.
- **Pane sizes are worked out in two places.** Render functions are given `w, h`, but key handlers compute sizes again from the whole terminal (`internal/tui/explorer.go:345`, `internal/tui/queryview.go:16-41`). One pure function that gives every pane its rectangle is the prerequisite refactor.
- **The libraries provide building blocks only.**
  - Lip Gloss v2.0.6: joins, `Place`, and a layer compositor with `Compositor.Hit`.
  - Bubble Tea v2.1.0: `View.MouseMode` (SGR 1006 with 1002 or 1003), mouse click, release, motion and wheel messages, and `View.OnMouse`.
  - Ultraviolet: a Cassowary splitter, but its "API may change".
  - None of them provides split trees, focus movement or drag-to-resize.
- **Recommendation (my judgement):**
  - **Layout model:** one n-ary split tree of the four panes, kept in `state/tui.json`.
    - Named presets, cycled with `ctrl+w space` and listed in the palette. The default is **"classic"**: explorer over schema on the left, editor over results on the right.
    - Vim-style `ctrl+w` commands: `H J K L` move a pane, `x` swaps, `r` rotates, `< > + - =` resize, `z` maximises, `c` hides.
    - `ctrl+w h/j/k/l` moves focus by direction, using the computed rectangles.
    - No config file.
  - **Hiding and maximising:**
    - A hidden pane is a flag on its leaf, so it keeps its slot. Focusing it by name shows it again.
    - Maximising is a separate flag (tmux-style zoom), not a change to the tree.
    - Small terminals fall back to showing only the focused pane.
  - **Mouse: a later, opt-in phase.** Click to focus, wheel to scroll, drag a divider to resize. It is off by default and toggled from the palette, because it takes over the terminal's own text selection.
  - **Free sample:** a second *source* for the Results pane, not a pseudo-Run.
    - The existing grid draws it, through a row-source function.
    - It is labelled **"Sample · free"**.
    - It never enters History or the run list.
  - **Keys:** `p` loads the sample (it keeps its old key) and **`s` opens `SELECT * … LIMIT 100` in a new buffer**. Both work from the explorer and the schema pane.
  - **Naming:** the Miller/tree/flat axis becomes the **"explorer style"**, still on `L`, with the palette entry "Cycle explorer style".
- **Cost:**
  - **The sample is free.** It reads through `tabledata.list`.
  - **The SELECT is billed.** `LIMIT` does not reduce the bytes read on non-clustered tables ([cost best practices](https://docs.cloud.google.com/bigquery/docs/best-practices-costs)). bqt inserts it without running it, so the dry-run and `confirm_bytes` still apply.
  - harlequin's DuckDB adapter does the same thing: `select * … limit 100` goes into a new buffer and is not run (section 3).

## 1. Current state

### 1.1 What is drawn, and how

- **The screen is chosen by zone.** `render` (`internal/tui/views.go:87-150`) picks the body:
  - `queryBody` when `m.zone != ZoneExplorer` (`views.go:95-96`).
  - Otherwise the flat search body or `withDetail(list)` for Miller/tree (`views.go:97-103`).
  - Then overlays are centred on top (`views.go:114-145`), the tutorial panel goes below (`views.go:146-148`), then the status and footer lines (`views.go:149`).
  - The spec's "as built" note: "the Explorer is one screen and the Editor above the Results is another (the focused pane gets the larger share)" (`docs/spec/bqt-v1.md:421`).
- **Composition is plain string slices**, not Lip Gloss joins. Every pane returns exactly `h` lines of exactly `w` cells. The helpers are `fit`/`pad`/`fillTo`/`vsep`/`title`/`overlay` (`internal/tui/ui.go:84-259`). `withDetail` concatenates rows by hand (`views.go:289-293`). The golden harness fails if any line is not exactly the terminal width, or if the line count differs from the terminal rows (`internal/tui/harness_test.go:250-262`). That invariant is a good base for a layout engine: each pane renders into its own rectangle.
- **Explorer layouts (`L`):**
  - `layoutMiller`/`layoutTree`/`layoutFlat` (`internal/tui/app.go:24-32`), cycled by the `layout` action (`app.go:885-888`), with `setLayout` at `internal/tui/explorer.go:500`.
  - Miller shows `clamp(w/24, 1, 3-first)` columns weighted 3:5:5 (`views.go:307-341`).
  - These are *explorer* layouts (how the catalog list is drawn), a different axis from pane arrangement. The name "layout" is already taken in code, state (`"layout"` key) and spec (§8.1, `docs/spec/bqt-v1.md:210-212`).
- **Detail pane placement (`P`):**
  - `placement` is `auto|right|bottom|hidden` (`app.go:34-43`), cycled at `app.go:889-892`.
  - `effPlacement` (`views.go:61-84`):
    - auto: right at ≥100 columns; bottom at ≥70 columns and ≥30 rows; otherwise hidden.
    - forced right needs ≥70 columns; forced bottom needs ≥14 body rows.
  - Sizes in `withDetail` (`views.go:280-303`): the right pane is `clamp(42% of w, 36, 72)` wide; the bottom pane is `clamp(45% of h, 7, 18)` tall.
  - `detailBlock` (`views.go:584-618`) shows schema *and* preview together when right with h≥26, bottom with w≥90, or zoomed with w≥100 (stacked: h≥20). Otherwise it shows one of them, switched by `tab` (`m.tab`).
  - Below the breakpoint, `Enter` on a table zooms the detail (`explorer.go:492-494`).
  - The spec text matches (`docs/spec/bqt-v1.md:218`).
- **Editor and results:** `queryLayout` (`queryview.go:16-29`):
  - results get 68% of the body when focused, 42% otherwise, and a 4-row stub when there is no Run;
  - the editor gets the rest.
  - `queryBody` (`queryview.go:118-180`) stacks them and draws the completion popup. The popup uses the editor cursor's coordinates *relative to the body origin* (`m.edX, m.edY`, `queryview.go:166, 170-178`). With a movable editor, these need the editor rectangle's offset added.
- **Sizes in key handlers:** paging and scrolling read sizes derived from the whole terminal, not from what was drawn:
  - `listHeight()` = `m.h-4` (`explorer.go:345`), used at `app.go:851`, `filter.go:251`, `search.go:127`;
  - `editorRows()`/`gridRows()` (`queryview.go:32-41`), used at `query.go:188, 940, 1024`, `vim.go:1266, 1338, 1368`, `grid.go:429`;
  - the zoom half-page `(m.h-4)/2` (`app.go:957`);
  - the overlay half-page (`app.go:810`).
- **The tutorial panel** shortens `m.h` (`internal/tui/tutorial.go:149-154`), so every size above already depends on the panel being open.
- **No mouse.** `View()` sets only `AltScreen` and `ReportFocus` (`app.go:1227-1232`); `MouseMode` is never set.

### 1.2 Persisted layout state

- `uiState{Layout, Panes, ZoomStacked}` (`app.go:225-230`) is written as JSON by `saveState` (`app.go:255-262`) and read by `loadState` (`app.go:232-253`). Unknown names are ignored (the loops at `app.go:242-251`), so adding fields is backward compatible.
- The path is `<data_dir>/state/tui.json` (`internal/tui/backend.go:120`). The spec calls it frontend-only and "last writer wins, nothing critical" (`docs/spec/bqt-v1.md:143`). Layouts and the remembered layout are frontend-only by rule (`docs/spec/bqt-v1.md:67`, `:522`).
- **The config file** has a `[tui]` table with exactly one key, `editing`. Unknown `[tui]` keys are hard errors (`internal/config/config.go:329-337`). `[tui]` is live-reloaded (`docs/spec/bqt-v1.md:129`). Adding `[tui] layout = …` would mean extending that switch and the spec's key table (`docs/spec/bqt-v1.md:94`).

### 1.3 Zones and focus

- **`Zone` mixes two things** (`internal/tui/keymap.go:9-18`): screen panes (`Explorer`, `Editor`, `Results`) and keymap scopes (`Global`, `Filter and jump`, `Favorites and History`, `Popups`). Schema has no zone. It is scrolled from the explorer with `ctrl+e`/`ctrl+y` (`keymap.go:78-79`), and `tab` picks schema or preview (`keymap.go:76`). A first-class schema pane needs a new zone (e.g. `ZoneSchema`) with its own movement keys.
- **Bindings** (`keymap.go:39-43`):
  - `ctrl+o` and `ctrl+w w` = next zone; `ctrl+w W` = previous zone;
  - `ctrl+w h` = Explorer; `ctrl+w l` and `ctrl+w k` = Editor; `ctrl+w j` = Results.
  - These are **named targets dressed as directions**. They only make sense for today's fixed arrangement (spec: `docs/spec/bqt-v1.md:421`).
- **Cycling:** `zoneStep` cycles a hard-coded `Explorer, Editor, Results` order (`internal/tui/query.go:104-113`). `setZone` also clears zoom, the completion popup and a pending chord (`query.go:97-102`).
- **Dispatch** (`app.go:660-717`): prompt/confirm/picker overlays first, then typing states, then the editor zone (`app.go:691-693`), then chords, then overlays/results/explorer. The editor handles `ctrl+w` chords in every vim mode (`docs/spec/bqt-v1.md:277`).
- **If panes can be anywhere,** `ctrl+w h/j/k/l` must become *geometric*: move to the pane whose rectangle is adjacent in that direction. `ctrl+o`/`ctrl+w w` should cycle in a stable order, either tree reading order or the fixed order Explorer, Schema, Editor, Results. Tests that pin the named-target behaviour must change: `TestZonesCycleAndChords` (`internal/tui/query_test.go:44-80`) and the tutorial steps "Zones", "Go to the Results" and "Back to the Explorer" (`tutorial.go:94, 116, 120`), which say "`ctrl+w j` the Results, `ctrl+w k` the Editor, `ctrl+w h` the Explorer".
- **Free keys for window commands:** `ctrl+w` is already a chord prefix in every zone. `ctrl+w` followed by `H J K L x r z = < > + -` is unbound (`keymap.go:37-180`).

### 1.4 Everything the preview touches

Each item below is removed or changed. **Update 2026-10-26:** the free sample stays (decision 1), so the engine, bq and fake rows below are *kept*; section 6.4 has the revised list. The CLI and MCP do **not** expose preview: there are no `Preview(` calls under `internal/cli` or `internal/mcp`, and the MCP tool list is eight tools without one (`docs/spec/bqt-v1.md:387`).

| Area | Location | What happens |
|---|---|---|
| Keys | `internal/tui/keymap.go:80` (`p` preview), `:81` (`J` record) | Remove; `p` (or another key) becomes the SELECT insert |
| Keys (text) | `keymap.go:70` (`enter` "zoomed preview: show the row as a record"), `:75` (`P` "zoomed: … side by side or stacked"), `:76` (`tab` schema/preview), `:78-79` (`ctrl+e`/`ctrl+y` "next preview row") | Reword or remove; `tab` and `P` lose their purpose in the zoom |
| Constants | `internal/tui/app.go:65-67` (`previewPage = 20`, `previewMax = 1000`, `previewChars = 200`) | Remove |
| Model fields | `app.go:123-124` (`prevOff`, `prevRow`), `:126` (`tab`), `:128` (`zoomStacked`) | Remove |
| State | `app.go:228-229, 252, 260` (`zoom_stacked`) | Drop the field; old files still load because unknown JSON is ignored |
| Messages | `app.go:281-285` (`previewMsg`), `:513-514` | Remove |
| Actions | `app.go:893-894` (`tab`), `:901-904` (`preview`, `record`), `:976-988` (zoom `panes`, zoom `open` → record), `:996-1007` (`scrollDetail` preview branch), `:1128` (reset of preview cursor) | Remove or simplify; `scrollDetail` only scrolls the schema |
| Detail cache | `app.go:1103-1106` (`prev`, `prevErr`, `prevBusy`, `prevEnd`), `:1164-1216` (`previewMore`, `onPreview`) | Remove |
| Rendering | `internal/tui/views.go:265` (zoom footer hint), `:584-618` (`detailBlock` both/tab logic), `:620-640` (`paneTitle` tabs), `:860-943` (`previewPane`) | Remove; `cellText` (`views.go:945-953`) stays because the grid uses it (`internal/tui/grid.go:110, 388`) |
| Record popup | `internal/tui/record.go:15-20` (preview `key`), `:43-84` (`previewRows`, `openRecord`, `loadRecord`), `:128-142` (`recordStep` preview branch) | Remove the preview half; `openRunRecord` (`record.go:87-97`) stays for Results |
| Backend port | `internal/tui/backend.go:62` (`Preview`) | Remove from the interface |
| Engine | `internal/engine/explore.go:221-251` (`Engine.Preview`) | Remove (or keep unused; see open questions) |
| BigQuery port | `internal/bq/tables.go:49` (`Tables.ListRows`), `:76-93` (REST `tabledata.list`), `:95-96` (`ErrNeedsQuery`) | `ListRows` and `ErrNeedsQuery` become unused; `GetTable` stays for live stats |
| Fake | `internal/bq/bqtest/fake.go:399-412` (`ListRows`), `TableData.Rows` (`:96`) | Remove the rows half; `TableData` also feeds `GetTable` |
| Shared type | `internal/api/search.go:226-239` (`PreviewColumn`, `PreviewPage`) | **Keep**: used by `internal/runs/pages.go:93`, `internal/engine/query.go:33`, `internal/tui/grid.go:53`, `internal/handoff/handoff.go:110`, `internal/cli/cmd_query.go:378`. A rename to `RowPage` is optional and touches the JSON contract only by name, since the JSON tags do not change |
| Engine test | `internal/engine/explore_test.go:89-127` (`TestTableDetailStatsAndPreview`) | Split: keep the stats half |
| TUI tests | `internal/tui/tui_test.go:65` (golden `miller_preview`), `:263-280` (`TestPreviewPagesAndStops`), `:417` (`TestPreviewContinuesAfterAShortPage`), `:433` (`TestPreviewScrollsIndependentlyOfTheSchema`), `:471` (`TestRecordPopupShowsTheFullRow`, preview part), `:500` (`TestZoomMovementKeysStayInTheDetailPane`, preview part), `:574-603` (`TestZoomSplitSwitchesBetweenSideBySideAndStacked`); `internal/tui/projects_test.go:199` (help wording); `internal/tui/dump_test.go:20`; `internal/tui/harness_test.go:66-81` (fake rows) | Remove or rewrite |
| Goldens | `internal/tui/testdata/miller_preview_{60x20,80x24,120x36}.txt`; nine 120x36 goldens draw a "Preview" pane (`flat`, `flat_filter`, `miller_filter_tables`, `miller_filter_typing`, `miller_preview`, `miller_table`, `palette`, `tree`, `tree_filter`) | Regenerate (`go test ./internal/tui -update`) |
| Tutorial | `internal/tui/tutorial.go:88` ("`tab` switches to the preview (`p` loads rows), `P` moves the pane") | Rewrite |
| Spec | §3 core list "preview fetching" (`docs/spec/bqt-v1.md:66`); §8.1 detail panes (`:218`); §8.2 core actions "fetch a preview" (`:232`); §8.4 title and the preview bullet (`:240, 244`); §15 rows `L`,`P` / `p`,`J` / `ctrl+e`,`ctrl+y` / `z` (`:437-439, 442`); §18 "preview paging are core" (`:521`); phase list (`:541`) | Update; likely worth an ADR, as it reverses §8.4 |
| README | `README.md:37` ("`p` previews rows") | Update |

### 1.5 Putting text into the editor, and quoting

- **The editor API** (`internal/tui/editor.go`):
  - `insert(s)` types at the cursor and replaces the selection, as one undo step (`editor.go:264-277`);
  - `setText(s)` replaces everything, as one undoable change, with the cursor at the end (`editor.go:83-90`);
  - `replaceBytes` (`editor.go:304-312`).
- **The buffer API** (`internal/tui/buffers.go`): `addBuffer(text, path, id)` appends and focuses a new buffer (`buffers.go:69-90`).
- **`loadPick`** (`internal/tui/picker.go:263-320`) is the existing "put a whole query into the editor" flow. It asks before replacing a buffer with unsaved text ("n opens … in a new buffer instead"), and `finishLoad` sets `draftVersion = -1` so the draft is written on the next tick.
- **The explorer's `i`** calls `insertPath` (`internal/tui/query.go:1146-1160`, called from `app.go:935-941` and from jump-to-table in insert mode, `internal/tui/search.go:168-171`). It:
  - wraps the whole path in **one pair of backticks**: `` `proj.ds.tbl` ``;
  - turns a shard family (`Table` ending `YYYYMMDD`) into `` `proj.ds.prefix*` ``;
  - inserts at the cursor;
  - switches to the Editor;
  - saves the draft in tests.
- **`Y` (copy in backticks)** does the same wrapping inline (`app.go:910-920`). A shared `quotedName(api.ObjectPath)` helper would serve `i`, `Y` and the new action. `ObjectPath.String()` joins with dots (`internal/api/search.go:21-30`).
- **The quoting is valid GoogleSQL.** A table path can have "up to three quoted or unquoted identifiers", the example is `` `myproject.mydataset.mytable` ``, and only the first identifier may contain dashes ([lexical structure, "Table names"](https://docs.cloud.google.com/bigquery/docs/reference/standard-sql/lexical#table_names)).
- **Key conflicts in the Explorer zone** (`keymap.go:62-93`):
  - `p` is free once preview goes.
  - `s` is unbound (`S` is the flat-list scope).
  - `o`, `a`, `e`, `x`, `v`, `w`, `c`, `m` are unbound.
  - In the Overlay scope `p`/`N` mean "previous record" (`keymap.go:169`); that does not conflict, because overlays have their own scope.
  - Vim users may read `p` as "paste", but it is not in the explorer.
  - In the spec, `space` in vim Normal is the editor leader (`docs/spec/bqt-v1.md:276`), so `space s` is free in the editor if a from-the-editor variant is wanted.

## 2. Library capabilities (exact versions)

The versions come from `go.mod`: `charm.land/bubbletea/v2 v2.1.0`, `charm.land/lipgloss/v2 v2.0.6`, and the indirect `github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4`. The paths below are relative to each module's root.

**Lip Gloss v2.0.6**

- `join.go`: `JoinHorizontal(pos Position, strs ...string)` and `JoinVertical(...)` join multi-line blocks along an edge. `position.go`: `Place`, `PlaceHorizontal` and `PlaceVertical` place a block in a box. These are string helpers with no sizing logic; bqt's `fit`/`fillTo` already do the same at a lower level.
- `canvas.go`: `Canvas`, a cell buffer that implements `uv.Screen`.
  - `NewCanvas(w, h)` creates it.
  - `Compose(drawer uv.Drawable)` draws onto it, later drawables on top.
  - `Render() string` returns the result.
- `layer.go`:
  - `Layer` has content plus `X/Y/Z` and `ID`.
  - `NewCompositor(layers...)` "flattens a layer hierarchy once".
  - `Compositor.Hit(x, y) LayerHit` returns the top-most layer with an ID at a point.
  - This is the upstream answer to positioned panes, overlays and mouse hit-testing. The README's "Compositing" section shows layers composed and rendered, and points to the docs for detecting mouse clicks on layers (`README.md:402-421`). Its snippet calls `compositor.Compose(a, b, c)`, which does not match the `Canvas.Compose(drawer)` / `NewCompositor(...)` signatures in this version, so trust the source over the snippet.
- No split, flex or "pane" type exists in Lip Gloss.

**Bubble Tea v2.1.0**

- `tea.go` `type View struct`: `Content string` (so a composited `Canvas.Render()` goes in as a string), `MouseMode` (`MouseModeNone|CellMotion|AllMotion`, `tea.go:297-319`), and `OnMouse func(MouseMsg) Cmd`. `OnMouse` is documented as a way "to intercept mouse messages that depends on view content from last render", which is the hook for clicking a pane to focus it or dragging a divider.
- `mouse.go`: `MouseClickMsg`, `MouseReleaseMsg`, `MouseWheelMsg`, `MouseMotionMsg`, plus the `MouseMsg` interface (`mouse.go:46-128`). Dragging a divider needs `MouseModeCellMotion` (click, release and motion while a button is held). Enabling the mouse would take the terminal's native text selection away from bqt's screen in most terminals; that last point is general terminal behaviour, unverified for each terminal.

**Ultraviolet (pseudo-version 20261008)**

- `buffer.go`: `type Rectangle = image.Rectangle`, `Rect(x, y, w, h)`, `type Position = image.Point`. These are good, standard types for pane rectangles; the standard library offers the same.
- `uv.go`: the `Drawable` interface (`Draw(scr Screen, area Rectangle)`) and `Screen`.
- `layout/layout.go`, package `layout`: "partitions terminal screen space into rectangular regions using a constraint-based solver" (Cassowary, "roughly 1:1 translation" of Ratatui).
  - `layout.Vertical(...)`/`Horizontal(...)` take constraints and `.Split(area) Splitted`.
  - The constraints are `Len`, `Percent`, `Ratio`, `Min`, `Max` and `Fill`, with priority Min > Max > Len > Percent > Ratio > Fill (`layout/constraint.go:8-24`).
  - It also has `Flex` modes (`layout/flex.go`), `Padding` and `Spacing`.
  - It uses `internal/casso` and has a split cache (`layout/cache.go`).
- **Caveats for ultraviolet:**
  - The README says "Ultraviolet is in active development. The API may change." (`README.md:140`).
  - bqt pulls it in only indirectly through Bubble Tea and Lip Gloss, at a pseudo-version. Importing `layout` directly would make it a direct dependency pinned to a moving commit.
  - For four panes with weights and minimum sizes, the arithmetic is about 30 lines (bqt already does it in `withDetail`/`queryLayout`/`millerList`).

**What none of them provide:** a split tree, swap/rotate/move operations, directional focus, resizable dividers, or persistence. lazygit's equivalent lives in its own `lazycore/boxlayout` package (see §3).

## 3. Prior art

These sources were read on 2026-10-24 at each project's default-branch head. "Unverified" marks claims I could not confirm in the source.

**The pattern:** every SQL TUI I checked uses a *fixed* layout with toggle, maximise and resize controls. None lets the user rearrange panes. Rearrangeable panes appear in tmux, zellij, Vim and Helix, and all four model them as a **split tree**. lazygit sits between the two: a box tree it rebuilds from config and state every frame.

### SQL tools

- **harlequin** (Textual SQL IDE; data catalog, editor, results). This is the closest product.
  - Layout is fixed. `compose` builds `Horizontal(DataCatalog, Vertical#main_panel(EditorCollection, RunQueryBar, ResultsViewer))` ([app.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin/app.py)), sized 1fr : 3fr in [app.tcss](https://github.com/tconbeer/harlequin/blob/main/src/harlequin/app.tcss).
  - Panes are hidden by disabling them: `:disabled { width: 0; height: 0 }`.
  - Default keys ([harlequin_vscode/\_\_init\_\_.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin_vscode/__init__.py)):
    - `ctrl+b`/`f9` toggles the sidebar; `f10` toggles full screen;
    - `f2`/`f5`/`f6` focus the editor, results and catalog;
    - `tab`/`shift+tab` cycle focus;
    - there is no directional focus.
  - Data catalog:
    - `ctrl+enter`/`ctrl+j` inserts the item's `query_name` at the cursor and focuses the editor;
    - `.` opens a context menu of `INTERACTIONS` ([data_catalog/\_\_init\_\_.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin/components/data_catalog/__init__.py)).
  - **DuckDB's "Preview Data" (`show_select_star`) writes `select *\nfrom {qualified_identifier}\nlimit 100` into a *new editor buffer* and does not run it** ([harlequin_duckdb/interactions.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin_duckdb/interactions.py)). That is almost exactly the requested feature.
  - The community BigQuery adapter backticks each part (`` `project`.`dataset`.`table` ``) as `qualified_identifier`, but inserts only `` `table_id` `` as `query_name`, and defines no interactions ([harlequin-bigquery adapter.py](https://github.com/joshtemple/harlequin-bigquery/blob/main/src/harlequin_bigquery/adapter.py)).
- **lazysql** (Go, tview).
  - Layout: a Flex of tree plus tabs; the editor is a fixed 12 rows above the results inside a tab ([components/home.go](https://github.com/jorgerojas26/lazysql/blob/main/components/home.go), [results_table.go](https://github.com/jorgerojas26/lazysql/blob/main/components/results_table.go)).
  - Keys: `T` toggles the tree, `+`/`-` resize it, clamped to between 24 columns and half the screen; `H`/`L` focus left/right ([app/keymap.go](https://github.com/jorgerojas26/lazysql/blob/main/app/keymap.go)).
  - Config: `TreeWidth` in a TOML `[application]` section ([app/config.go](https://github.com/jorgerojas26/lazysql/blob/main/app/config.go)).
  - Selecting a table **runs** `SELECT * FROM "schema"."table" … LIMIT $1 OFFSET $2` at once, page size 300 ([drivers/postgres.go](https://github.com/jorgerojas26/lazysql/blob/main/drivers/postgres.go)).
- **gobang** (Rust, ratatui; dormant since 2023).
  - Fixed left/right split; `<`/`>` change it by 5% within 15–70% ([src/app.rs](https://github.com/TaKO8Ki/gobang/blob/main/src/app.rs)).
  - Editor and records are tabs, never visible together.
  - Enter on a table runs `SELECT * … LIMIT 200` ([database/postgres.rs](https://github.com/TaKO8Ki/gobang/blob/main/src/database/postgres.rs), [database/mod.rs](https://github.com/TaKO8Ki/gobang/blob/main/src/database/mod.rs)).
- **DBeaver:**
  - "Read data in SQL console" opens a console and runs the SELECT; "Generate SQL" shows SQL to review without running it ([SQL Generation](https://dbeaver.com/docs/dbeaver/26.1/SQL-Generation/)).
  - The default row limit is `resultset.maxrows = 200` ([ModelPreferences.java](https://github.com/dbeaver/dbeaver/blob/devel/plugins/org.jkiss.dbeaver.model/src/org/jkiss/dbeaver/ModelPreferences.java)).
- **DataGrip:**
  - Results are limited to 500 rows by default ([rows](https://www.jetbrains.com/help/datagrip/rows.html)).
  - F4 "Edit Data" opens the table in the data editor, which runs it ([database explorer](https://www.jetbrains.com/help/datagrip/database-explorer.html)).
  - An "insert SELECT … LIMIT into the console" action: unverified.

**Takeaway for bqt:** "select star into a new buffer, not run" has precedent in harlequin. The others execute at once with a 200–500 row page. On BigQuery, executing at once costs money (§5), so harlequin's choice fits better.

### Layout engines and window managers

- **lazygit / lazycore `boxlayout`** (Go). This is the closest ready-made Go model.
  - `Box{Direction ROW|COLUMN, ConditionalDirection func(w,h), Children, ConditionalChildren func(w,h), Window, Size, Weight}`. `ArrangeWindows(root, x0, y0, w, h) map[string]Dimensions` gives the fixed `Size` first and splits the rest by `Weight` ([boxlayout.go](https://github.com/jesseduffield/lazycore/blob/master/pkg/boxlayout/boxlayout.go)).
  - lazygit rebuilds the tree each frame from config, screen mode, focus and size ([window_arrangement_helper.go](https://github.com/jesseduffield/lazygit/blob/master/pkg/gui/controllers/helpers/window_arrangement_helper.go)).
  - Config keys ([docs/Config.md](https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md)):
    - `gui.sidePanelWidth` (0.3333), `expandFocusedSidePanel`, `mainPanelSplitMode` (`horizontal|vertical|flexible`), `enlargedSideViewLocation` (`left|top`);
    - `screenMode` (`normal|half|full`, cycled at runtime with `+`/`_`, not persisted);
    - `portraitMode` (`auto|always|never`; auto stacks when width ≤ 84 and height ≥ 46).
  - Focus: `h`/`l`/tab cycle the blocks; `1`–`5` jump to one.
  - Small screens: unfocused side panels squash to 1–3 rows on short terminals.
  - **Lessons:** keep a pure layout function; make "maximise the focused pane" a mode, not a separate layout; express responsive collapse as conditional children.
- **tmux** ([tmux.1](https://github.com/tmux/tmux/blob/master/tmux.1), [tmux.h](https://github.com/tmux/tmux/blob/master/tmux.h)).
  - Model: a split tree of `struct layout_cell` with `LAYOUT_LEFTRIGHT | LAYOUT_TOPBOTTOM | LAYOUT_WINDOWPANE`.
  - Commands:
    - `select-pane -L/-R/-U/-D` (directional), `-l` (last pane);
    - `swap-pane -U/-D`, `rotate-window`;
    - `resize-pane -L/-R/-U/-D n` and `-Z` (zoom, `prefix z`).
  - Presets via `select-layout`/`next-layout`: `even-horizontal`, `even-vertical`, `main-horizontal`, `main-vertical`, `tiled`.
  - `list-windows` prints a layout string that `select-layout` accepts back, rescaled to the window. That is precedent for persisting the tree as data.
- **zellij:**
  - KDL [layout files](https://zellij.dev/documentation/creating-a-layout.html) (`pane split_direction="vertical" size="50%"`, nested panes).
  - [Swap layouts](https://zellij.dev/documentation/swap-layouts.html) are presets chosen by pane count, cycled with `Alt+[`/`Alt+]`.
  - [Actions](https://zellij.dev/documentation/keybindings-possible-actions.html): `MoveFocus`, `MovePane`, `Resize`, `ToggleFocusFullscreen`, `NextSwapLayout`.
  - Zellij uses both a config file for the tree and runtime keys for moves and presets: Option 4's split.
- **Vim** ([windows.txt](https://github.com/vim/vim/blob/master/runtime/doc/windows.txt)):
  - `CTRL-W j` "Move cursor to Nth window below current one. Uses the cursor position to select between alternatives."
  - `CTRL-W H` "Move the current window to be at the far left, using the full height of the screen".
  - `CTRL-W x` "Exchange current window with next one".
  - `CTRL-W r` "Rotate windows downwards/rightwards".
  - Plus `CTRL-W =` (equalise), `_`/`|` (maximise height/width) and `o` (only).
  - bqt already uses `ctrl+w h/j/k/l/w/W` (`keymap.go:39-43`), so the rest of this vocabulary is the natural extension.
- **Helix** ([helix-view/src/tree.rs](https://github.com/helix-editor/helix/blob/master/helix-view/src/tree.rs), [keymap.md](https://github.com/helix-editor/helix/blob/master/book/src/keymap.md)):
  - an n-ary `Container{layout: Horizontal|Vertical, children}` **without weights** (even split);
  - `Ctrl-w h/j/k/l` jumps; `H/J/K/L` *swap* in a direction (unlike Vim's move-to-edge); `t` transposes the container direction; `w` rotates; `o` keeps only the current view.
- **k9s / gitui:**
  - fixed layouts;
  - k9s: header toggle `ctrl-e` and full-screen `f` ([README](https://github.com/derailed/k9s/blob/master/README.md));
  - gitui: the focused diff takes the whole width ([src/tabs/status.rs](https://github.com/gitui-org/gitui/blob/master/src/tabs/status.rs)).

### Go layout libraries for Bubble Tea

| Library | Model | State (2026-10-24) |
|---|---|---|
| [treilik/bubbleboxer](https://github.com/treilik/bubbleboxer/blob/main/boxer.go) | Tree: `Node{Children, VerticalStacked, SizeFunc}`, leaves keyed into `map[string]tea.Model` | 87★, last push 2023-09 (stale; predates Bubble Tea v2) |
| [76creates/stickers flexbox](https://github.com/76creates/stickers/blob/master/flexbox/flexbox.go) | Rows of ratio cells with minimums | 402★, push 2026-04 |
| [winder/bubblelayout](https://github.com/winder/bubblelayout/blob/main/layout.go) | MiG-style grid with docks and min/pref/max | 25★, push 2026-07 |

None of them offers move, swap or directional focus. They would add a dependency for about 30 lines of arithmetic. bqt's spec already found third-party editors built on Bubble Tea v1 unusable (`docs/spec/bqt-v1.md:28`), and I did not check whether these three support Bubble Tea v2.

## 4. Design options

All the options share a foundation that any of them needs:

- **F1. One pane geometry function.** A pure function `arrange(spec, w, h) map[Pane]uv.Rectangle` (or `image.Rectangle`), run on `WindowSizeMsg`, on arrangement changes and on tutorial toggling. Rendering *and* key handlers use its result instead of `m.h-4`, `queryLayout()` and so on (§1.1). The completion popup offsets by the editor's origin. This is testable without a terminal: table tests of rectangles for each spec and size.
- **F2. Panes render into a rectangle.** `explorerPane(w, h)`, `schemaPane(w, h)`, `editorPane(w, h)` and `resultsPane(w, h)` already almost exist (`millerList`/`treeList`/`searchBody`, `schemaPane` at `views.go:642`, `queryBody` split in two, `resultsPane` at `queryview.go:182`). Composition joins exact-size line slices row by row: an n-way generalisation of `withDetail`. Alternatively, draw each pane as a Lip Gloss `Layer` at `X/Y` on a `Canvas`. That buys `Compositor.Hit` for mouse focus later, at the cost of a second rendering path beside `overlay()`.
- **F3. Separate "pane" from "keymap scope".** Add `ZoneSchema` with `j/k`, `ctrl+d/u` and `gg/G` scrolling. Keep `ctrl+e`/`ctrl+y` in the explorer as a "scroll the schema without leaving the list" convenience.
- **F4. Directional focus.** `ctrl+w h/j/k/l` picks the nearest pane whose rectangle lies in that direction and overlaps on the other axis; ties go to the pane nearest the focused pane's cursor or centre line (Vim "uses the cursor position to select between alternatives", §3), falling back to the most recently focused pane. `ctrl+o`/`ctrl+w w` cycle in a fixed order.
- **F5. Small terminals.** Each pane has a minimum size (the explorer about 24 columns per Miller column, today's `w/24` at `views.go:310`; the editor about 3 rows; the results about 4 rows; the schema about 30×5). When the arrangement cannot meet the minimums, show **only the focused pane** full-screen with a one-line pane bar, and let `ctrl+o` and `ctrl+w h/j/k/l` switch. This generalises today's "collapsed below 70 columns, `Enter` zooms" (`views.go:77-83`, `explorer.go:492-494`) and the current two-screen behaviour.
- **F6. Maximise.** `ctrl+w z` (or keep `z` in the explorer and the schema) toggles "focused pane full-screen". It replaces today's detail zoom (`app.go:895-896`, `:956-991`) for every pane.

### Option 1: named presets cycled with a key

- **Model:** an enum such as `classic` (explorer and schema stacked on the left; editor over results on the right), `wide` (explorer | editor over results | schema), `stacked` (every pane in one column), `results-right` (explorer and schema | editor | results) and `focus` (one pane).
- **Configuration:** a palette entry and a key such as `ctrl+w L` cycle the presets. The choice is saved as `"arrangement": "classic"` in `tui.json`, a new field next to `layout`/`panes`.
- **Complexity:** low. Each preset is a hard-coded tree or function. Directional focus still comes from F4, over rectangles.
- **Fit:** purely frontend, as the spec requires (`docs/spec/bqt-v1.md:67`). It maps onto the existing `L`/`P` cycling style (`app.go:885-892`) and persistence.
- **Small terminals:** each preset can declare its own fallback; for example `classic` drops to two columns, then to focus mode.
- **Testability:** one golden per preset at the three standard sizes (60×20, 80×24, 120×36, as in `tui_test.go:56-85`).
- **Limit:** the user cannot make an arrangement the author did not anticipate. That does not meet "freely positionable" unless the set of presets is generous.

### Option 2: split tree, edited with keys (tmux/vim/Helix style)

- **Model:** an n-ary tree.

  ```go
  type node struct {
      Split  string  `json:"split,omitempty"`  // "row" (side by side) | "col" (stacked); empty for a leaf
      Sizes  []int   `json:"sizes,omitempty"`  // weights, same length as Kids
      Kids   []*node `json:"kids,omitempty"`
      Pane   string  `json:"pane,omitempty"`   // explorer | schema | editor | results (leaf)
  }
  ```

  Invariant: each pane appears at most once. A hidden pane keeps its leaf, with a `Hidden` flag (section 9.2). n-ary rather than binary keeps presets flat and avoids degenerate nesting (Helix, lazycore and zellij are n-ary; tmux is a tree of splits as well, see §3).
- **Operations** (Vim's `CTRL-W` vocabulary, so the `ctrl+w` prefix bqt already uses reads naturally):
  - `H/J/K/L`: move the focused pane to the far left, bottom, top or right edge as a full-height or full-width split;
  - `x`: exchange with the next sibling;
  - `r`: rotate siblings;
  - `<`/`>`/`+`/`-`: change the weight;
  - `=`: equalise;
  - `z`: maximise;
  - `c` or `q`: hide the pane (a palette entry shows it again).
- **Persistence:** the whole tree goes in `tui.json` (`"arrangement": {…}`). On load, validate it: unknown panes are dropped, missing panes are appended, and if the tree is invalid it falls back to the default preset. That keeps the state file at "nothing critical" (`docs/spec/bqt-v1.md:143`).
- **Complexity:** medium. My estimate is about 300–500 lines with tests: tree operations, validation, `arrange`, directional focus and rendering glue. The operations are pure functions over the tree, which is ideal for table tests (input tree, operation, expected tree).
- **Small terminals:** as F5. Minimum sizes propagate up the tree; a split whose children cannot fit collapses to the focused child.
- **Presets** fall out for free as named trees, so Option 1 is a subset.

### Option 3: layout in the config file (TOML), dropped by decision 4

- **Model:** the same tree, written by the user in TOML. For example:

  ```toml
  [tui]
  layout = "explorer,schema | editor / results"   # mini-DSL: "|" side by side, "/" stacked, "," stacked within a column
  # or structured:
  [[tui.layout.split]] …
  ```

- **Configuration:** the user edits `config.toml`. It is live-reloaded with the rest of `[tui]` (`docs/spec/bqt-v1.md:129`), and errors carry key and line through the existing `config.Error` (`internal/config/config.go:329-337`).
- **Complexity:** low to medium on top of Option 2, from parsing, validation and the spec table. A mini-DSL needs its own parser and error positions; nested TOML tables are verbose for a tree.
- **Fit:** the config is shared by every frontend. The spec routes TUI-only keys through `[tui]` (`docs/spec/bqt-v1.md:94`), so this fits. But the spec also says the *remembered* layout lives in state, not config (`:143`, `:522`). Two sources of truth need a rule, for example: "the config sets the default, `ctrl+w` changes are remembered in state, and a palette entry resets to the config".
- **Focus and small terminals:** as Option 2.
- **Testability:** config parse tests plus Option 2's.
- **Downside:** discoverability. Users must read docs to rearrange panes, while key-driven rearrangement is visible.

### Option 4: combination (recommended, my judgement)

**Option 2's tree as the single model.** Expose it as:

1. presets, cycled with one key and listed in the palette;
2. vim-style `ctrl+w` window commands to move, swap, resize, maximise and hide panes;
3. persistence of the current tree in `state/tui.json`;
4. ~~optionally `[tui] layout = "<preset>"`~~: dropped by decision 4; layouts are switched in the app only.

Reasons:

- It meets "freely positionable" without config editing.
- It reuses the `ctrl+w` prefix already reserved in every zone and vim mode (`docs/spec/bqt-v1.md:277`).
- It keeps layout frontend-only, as the GUI-readiness rule wants (`docs/spec/bqt-v1.md:521-522`).
- The tree operations and `arrange` are pure and table-testable. The golden harness checks the composed screen.

Mouse: see section 9 (an opt-in later phase).

The default and the other presets are in section 8.

**Migration:**

- `P` (detail placement) and the zoom's `P`/`tab` go away. Schema placement is now part of the arrangement.
- Naming and the state migration (the new concept is called "layout"; `L` becomes "explorer style") are in section 10.

## 5. Preview removal and the SELECT-insert shortcut (first pass, 2026-10-24; sections 6–7 supersede the key and sample parts)

- **What goes:** everything in the §1.4 table.
  - **Decided 2026-10-26:** the engine/bq pieces stay, because the free sample moves into the Results pane (section 6).
- **Cost and behaviour of the replacement:**
  - Google: "For non-clustered tables, applying a `LIMIT` clause to a query doesn't affect the amount of data that is read" and "You are billed for reading all bytes in the entire table as indicated by the query". It recommends table preview / `tabledata.list` "to view data at no charge" ([Control query costs: "Avoid using LIMIT in non-clustered tables", "Avoid running queries to explore table data"](https://docs.cloud.google.com/bigquery/docs/best-practices-costs)). So the new shortcut is strictly more expensive than `p` was.
  - On a table with "Require partition filter", a query without a qualifying `WHERE` fails with "Cannot query over table … without a filter that can be used for partition elimination" ([Querying partitioned tables](https://docs.cloud.google.com/bigquery/docs/querying-partitioned-tables)). The catalog knows the partitioning column (`api.ColumnMeta.Partitioning`, `internal/api/catalog.go:77`; shown in the schema at `views.go:725-748`), but no code reads the `requirePartitionFilter` flag (`grep` finds none). Unverified whether `tables.get` in `internal/bq/tables.go` could cheaply return it.
  - Mitigation already in bqt: running goes through the dry-run estimate, `confirm_bytes` and `max_bytes_billed` (`docs/spec/bqt-v1.md:87-88`). Insert-only (never auto-run) keeps that guard.
- **Proposed behaviour** (an open question for the user where marked):
  - **Key:** superseded. `s` for the SELECT, `p` for the free sample (section 7).
  - **Text:** ``SELECT * FROM `proj.ds.tbl`\nLIMIT 100``, using the same quoting as `i` (`query.go:1151`).
  - For a shard family: ``SELECT * FROM `proj.ds.prefix*`\nWHERE _TABLE_SUFFIX = '<newest>'\nLIMIT 100``. The newest shard is `ShardFamily.Last`, `internal/api/catalog.go:133-138`; its exact format is not checked. The spec already promises a `_TABLE_SUFFIX` hint for family inserts (`docs/spec/bqt-v1.md:238`).
  - For a partitioned table, optionally a commented `-- WHERE <partition column> …` line.
  - **Where it goes:** decided: always a **new buffer** (decision 2; section 7.3), like harlequin's `show_select_star` (section 3).
  - Focus moves to the Editor (as `insertPath` does, `query.go:1156`), with the cursor at the end or on `100`. In vim mode, Normal mode, so `enter` runs it (`docs/spec/bqt-v1.md:276`).
  - **Views and external tables:** a query works on them (the old preview could not, `engine/explore.go:228-238`), so the shortcut is an improvement there.
  - **Tests:** a `query_test.go` case beside `TestExplorerInsertsTableNamesIntoTheEditor` (`internal/tui/query_test.go:521`) for the table and shard family cases, plus a check that a new buffer is created each time.

## 6. The free sample in the Results pane (decision 1)

### 6.1 What the Results pane is today

- Each buffer has `viewID`, "Run shown in the results pane" (`internal/tui/buffers.go:33`).
- `shownRun` reads that Run from the follower (`internal/tui/grid.go:404-411`).
- `resultsPane` (`internal/tui/queryview.go:182-256`) draws two things from it:
  - the title, `" Results · "+m.runSummary(r)`;
  - the state lines (failed, cancelled, running…).
- When there are rows, `resultsPane` delegates to the grid (`queryview.go:249-250`).
- **The grid** is "a window over server-side pages with a cell cursor" (`grid.go:25-46`).
  - It is keyed by Run ID in `m.grids` (`app.go:148`).
  - It fetches pages of `gridPage = runs.PageRows = 500` rows (`grid.go:17`, `internal/runs/runs.go:32`), through `m.be.RunRows(ctx, id, start, n, gridFormat)` (`grid.go:413-420`).
  - Cells are cut at 200 characters (`grid.go:19`). That is the same limit as `previewChars` (`app.go:67`).
  - `pageMsg` carries the Run ID (`grid.go:48-53`), and `onPage` stores the page (`grid.go:435-448`).
- **The record popup for Results** (`openRunRecord`, `internal/tui/record.go:87-97`) reads the one row again in full, through `RunRows(id, row, 1, recordFormat)`. `recordStep` moves the grid cursor (`record.go:115-127`).
- The bqt-side half of `tabledata.list` is already the same shape:
  - `Engine.Preview(ctx, path, startIndex, rows, maxChars) (api.PreviewPage, error)` (`internal/engine/explore.go:221-251`).
  - It returns `Columns`, `StartIndex`, `TotalRows` and `Rows` (`internal/api/search.go:233-239`). That is the same type `RunRows` returns (`internal/engine/query.go:33`).
  - So the grid needs only a different page function.

### 6.2 Options

| Option | What it means | Verdict |
|---|---|---|
| **Pseudo-Run** in the core `runs` manager | A sample becomes an `api.RunInfo` with no job | **Reject.** The glossary defines a Run as "one execution of a SQL snapshot … A finished Run produces a History entry" (`CONTEXT.md`, **Run**). `result_id` is the job's `project:LOCATION.job_id` (`docs/spec/bqt-v1.md:410`). The run list, cancel, confirmation and History all assume a job. A sample would need special cases everywhere, and it would leak into the CLI and MCP contract types. |
| **Results tab** | The Results pane gets tabs: `Run #3 │ Sample: orders` | Workable, but it adds a tab bar and its keys. The run list (`gr`, `ctrl+x l`) already covers switching between Runs. |
| **A second source of the Results pane** (recommended) | The pane shows either the buffer's Run or **the current sample** | **Recommended.** It is the smallest change. Runs stay Runs. Every grid key works on the sample for free. |

### 6.3 Recommended design

- **State.** One sample slot on the model: `m.sample *sample{path, kind, grid, err, busy}` and `m.showSample bool`. The sample is not tied to a buffer, because it is about a table, not a query.
  - `p` replaces the slot.
  - Up to N loaded samples could be kept in the existing `details` cache, keyed by table (`app.go:1098-1119`). That makes switching back instant. It is optional.
- **When the sample shows:**
  - `p` sets `showSample = true`.
  - These clear it:
    - starting a Run in any buffer (`startRun` already sets `viewID`, `internal/tui/query.go:367-390`);
    - choosing a Run in the run list (`query.go:1086-1100`);
    - a History "show result" (`picker.go:322-335`);
    - `esc` in the Results.
  - `esc` order: clear the find text, then leave the sample and show the buffer's Run, then go to the editor. This extends today's `back` (`query.go:925-931`) and keeps "esc backs out exactly one level" (`docs/spec/bqt-v1.md:218`).
- **The grid gets a row source.**
  - `grid.run int64` becomes `grid.key` (a Run ID, or a table key for a sample).
  - Add `grid.src func(ctx, start int64, n int, o format.Options) (api.PreviewPage, error)`.
  - `fetchPage` (`grid.go:413-420`) calls `g.src`. `pageMsg` carries the key.
  - `newGrid(info)` (`grid.go:55-58`) gains a twin `newSampleGrid(page)`, built from the first page's `Columns` and `TotalRows`.
  - Paging is the grid's own: `need`/`store` (`grid.go:202-249`) and `gridEnsure` (`grid.go:423-433`).
  - **"Fetch more" goes away.** Scrolling, `G` and `ctrl+d` page through the whole table on demand, as they do for a Run. The 20-row step and the 1,000-row cap (`app.go:65-66`, `:1179-1181`) were UI limits, not API limits. `tabledata.list` is free at any page ("view data at no charge and without affecting quotas", [cost best practices](https://docs.cloud.google.com/bigquery/docs/best-practices-costs)).
  - **Short pages.** `tabledata.list` can return fewer rows than asked when rows are wide (the comment at `app.go:1211-1212`). The sample's source should loop until it has `n` rows or reaches the end, as `Run.Rows` does across server pages (`internal/runs/pages.go:90-115`). Without that, rows past a short page would stay blank: `grid.cells` returns nil for them (`grid.go:65-75`).
- **The record view.** `Enter`/`v` in the Results already open the record popup (`keymap.go:123`), and `n`/`N` step through rows (`keymap.go:168-169`).
  - Generalise `openRunRecord` so that it reads the full row through the grid's source with `recordFormat` (`record.go:40`). For a sample that is `Preview(path, row, 1, recordCellMax)`, exactly what the preview's `loadRecord` does today (`record.go:80-82`).
  - **`J` is then unnecessary** and is removed.
- **Labelling: free versus billed must be visible.**
  - The title is `" Sample · proj.ds.tbl · free read, no query job · 45,120 rows"`, in its own colour (for example `sOk`), where a Run shows `" Results · #3 done · … billed"` (`queryview.go:195`).
  - The status footer says `"s writes a SELECT for it (billed)"`.
  - The status line's `▶ N run(s)` (`views.go:172-174`) never counts samples.
- **What does not apply to a sample:**
  - It has no History entry, because there is no job. History records query Runs (`CONTEXT.md`, **History entry**).
  - It does not appear in the run list.
  - `ctrl+c` does not cancel it, because it is not a Run. A slow fetch is a plain command, and a new `p` replaces it.
  - **Export (`e`)** is disabled at first ("export needs a query: s writes one"). Export reads raw Run rows (`internal/runs/pages.go:125-139`, `RawRows`). A free export over `ListRows` is possible later.
- **Hand-off.** `focusedRun` (`internal/tui/handoff.go:39-54`) stays Run-only.
  - Add sample items when the sample is shown: `"First 20 rows of proj.ds.tbl (free sample, markdown|TSV|NDJSON)"`.
  - They are built with the core's `handoff.Sample(cols, rows, f)` (`internal/handoff/handoff.go:110`, `SampleRows = 20` at `:18`) over the loaded page.
  - Mind the NDJSON caveat: the Run version is written from raw values so that strings keep their line breaks (`docs/spec/bqt-v1.md:415`). For the sample, read the 20 rows again with `KeepNewlines`, as `recordFormat` does.
- **Views and external tables.** The Results pane shows `"views and external tables cannot be read for free: s writes a SELECT (billed)"`. This is the existing `bq.ErrNeedsQuery` path (`app.go:1196-1197`, `internal/engine/explore.go:228-238`).
- **Shard families.** `search.Lookup` resolves a family path to a member, `ShardMembers(…)[0]` (`internal/search/browse.go:185-192`). I did not check whether that member is the newest shard. The schema pane promises "the latest shard" (`docs/spec/bqt-v1.md:238`).

### 6.4 Removed versus kept (this replaces the §1.4 verdicts)

| Kept | Where | Why |
|---|---|---|
| `Engine.Preview` (rename to `Sample`, optional) | `internal/engine/explore.go:221-251` | It is the free read. The spec lists "preview fetching" as core (`docs/spec/bqt-v1.md:66`), which suits a future GUI |
| `Backend.Preview` | `internal/tui/backend.go:62` | The TUI's port to it |
| `bq.Tables.ListRows`, `ErrNeedsQuery` | `internal/bq/tables.go:49, 76-96` | `tabledata.list` |
| `bqtest.Fake.ListRows`, `TableData.Rows` | `internal/bq/bqtest/fake.go:96, 399-412` | Tests |
| `TestTableDetailStatsAndPreview` | `internal/engine/explore_test.go:89-127` | Unchanged |
| `api.PreviewPage`/`PreviewColumn` | `internal/api/search.go:226-239` | Shared by Runs and the sample |
| `onPreview`'s rule for the end of a page, and the `ErrNeedsQuery` message | `app.go:1192-1216` | Moves into the sample's source and message |

| Removed | Where |
|---|---|
| `previewPage`, `previewMax` (`previewChars` merges with `gridCellChar`) | `app.go:65-67` |
| `prevOff`, `prevRow`, `tab`, `zoomStacked` and the `zoom_stacked` state field | `app.go:123-128, 228-229, 252, 260` |
| `detailData.prev*`, `previewMore` (rewritten as `sampleOpen`) | `app.go:1103-1106, 1164-1190` |
| The `tab`, `record` and zoom `panes`/`open` actions, and the preview branch of `scrollDetail` | `app.go:893-894, 903-904, 976-988, 996-1004` |
| `previewPane`, `detailBlock`'s both/tab logic, `paneTitle`'s tabs, the zoom footer | `views.go:265, 584-640, 860-943` |
| The preview half of the record popup (`key` field, `previewRows`, `openRecord`, `loadRecord`, the `recordStep` branch) | `record.go:19, 43-84, 128-142` |
| Keys `J` and `tab`; the zoomed `P` | `keymap.go:76, 81` (and the text at `:70, :75, :78-79`) |
| The preview tests | `tui_test.go:263-280, 417-470, 471-499` (preview part), `500-573` (preview part), `574-603` |

`TestPreviewPagesAndStops` becomes a sample test with the same fake rows (`harness_test.go:66-81`): `p` → Results shows "Sample · free"; `G` pages; there is no `ListRows` call after the last page; a view → "cannot be read for free".

## 7. Shortcuts (decision 2)

### 7.1 Key inventory

The Explorer zone binds (`internal/tui/keymap.go:62-93`):

`j k h l` (and the arrows), `g g`, `home`, `G`, `end`, `ctrl+d`, `ctrl+u`, `pgdown`, `pgup`, `enter`, `esc`, `/`, `:`, `L`, `P`, `tab`, `z`, `ctrl+e`, `ctrl+y`, `p`, `J`, `S`, `i`, `g r`, `y`, `Y`, `r`, `R`, `g p`, `g s`, `g c`, `?`, `q`.

The Global zone (`keymap.go:38-60`) binds `ctrl+q o p t g c`, `f1`, the `ctrl+w` chords `w W h j k l`, and the `ctrl+x` chords `p f r y`.

Other places where letters already mean something:

- **Results zone** (`keymap.go:111-139`): `0 $ f H U / n N y Y e v`, among others.
- **Overlay zone** (`keymap.go:163-179`): `n N p y space x o e / r s S w q`. In the Projects dialog, `s` syncs now (`docs/spec/bqt-v1.md:216`).
- **The vim `space` leader in the editor** (`internal/tui/vim.go:893-894`): `r d e l n b w [ ] s f h F p y`.
- **The tutorial** mentions `L`, `tab`, `p`, `P` and `z` (`internal/tui/tutorial.go:86-88`).

After the preview goes, these Explorer letters are free:

- `p` (freed);
- `J`, `tab` and `P` (freed);
- `a b c d e f m n o s t u v w x`;
- the capitals other than `G L S Y R`.

None of these keys is a global key, so all are safe in the Explorer. The new **Schema** zone (`ZoneSchema`, section 4 F3) needs its movement keys plus the same table actions. A test checks for duplicate bindings within each zone (`keymap.go:219-233`, `internal/tui/tui_test.go:21-22`).

### 7.2 Recommendations

| Action | Key | Zones | Palette name | Runner-up | Why |
|---|---|---|---|---|---|
| Free sample into Results | **`p`** | Explorer, Schema | "Sample the table's rows (free)" | `d` ("data") | The key, the mnemonic and the footer hint carry over, and so does the tutorial text ("`p` loads rows"). Users who press `p` get the same rows, now in the Results. `d` is free; a vim "delete" reflex would be harmless here, but it is a weaker mnemonic. |
| `SELECT * … LIMIT 100` into a new buffer | **`s`** | Explorer, Schema | "Query the table in a new buffer (SELECT * … LIMIT 100)" | `I` | **s** for SELECT. It is free in both zones. The only nearby meaning is the Projects dialog's `s` (sync now), which is a different zone and an overlay. `I` pairs nicely with `i` (`i` inserts the name, `I` inserts a whole query), but it is a capital and a vim-ism with the opposite meaning (insert at the start of the line). |

Rejected candidates:

- `Q`: next to `q` (quit), so a mis-press is costly.
- `o`: vague.
- `e`: in the Results, `e` is export.
- `n`: `n`/`N` mean "next" in the Results and in overlays.
- `enter`: it already expands and opens.
- `ctrl+` keys: none is needed, because the Explorer and Schema zones read plain letters.

From the editor: nothing new is needed. `ctrl+t` opened from the editor still inserts a name (`app.go:785-790`, `internal/tui/search.go:168-171`). A later `ctrl+s` inside the jump popup ("query this table") would be free there: the popup passes only *global* ctrl keys through (`search.go:174-177`), and `ctrl+s` is not global.

### 7.3 How the new buffer is made, and where focus goes

- **Today:**
  - `addBuffer(text, path, id)` (`internal/tui/buffers.go:69-90`) creates a scratch buffer. It gets a random ID, the name `"query N"`, an advisory draft lock in `drafts/tui/`, and the cursor at 0:0. It appends the buffer and makes it current.
  - `ctrl+x n` calls it with empty text (`internal/tui/query.go:293-295`).
  - The picker's `finishLoad` also sets `draftVersion = -1`, so the draft is written on the next tick (`picker.go:300-303`).
  - `insertPath` moves focus with `setZone(ZoneEditor)` (`query.go:1156`). That clears zoom, the completion popup and any pending chord (`query.go:97-102`).
- **Proposed `s`:**
  1. Run `nb := m.addBuffer(sql, "", "")` and set `nb.draftVersion = -1`.
  2. Optionally name the buffer after the table: `nb.name = p.Table`, instead of `query N`.
  3. Put the cursor at the end, or on `100`.
  4. Call `setZone(ZoneEditor)`.
  - In vim mode the new buffer starts in Normal mode (`vimNormal` is the zero value, `internal/tui/vim.go:18`), so `enter` runs it (`docs/spec/bqt-v1.md:276`).
  - Toast: `"new buffer: SELECT * from orders (billed when run; ctrl+d / space d estimates)"`.
  - It is never auto-run.
- **The SQL text:**
  - It uses `insertPath`'s quoting, factored into a `quotedName` helper (`query.go:1151-1154`).
  - For a shard family it adds a `WHERE _TABLE_SUFFIX = '<newest>'` line (`docs/spec/bqt-v1.md:238`).
- **One question to confirm:** should `s` reuse the current buffer when it is an *empty, unsaved scratch* buffer (the fresh `query 1` at startup, `app.go:205-207`)? Otherwise a startup `s` leaves an empty `query 1` behind. Decision 2 says "a new buffer", so the default here is always new.

## 8. Layout presets (decision 3)

The notation: **E** = Explorer, **S** = Schema, **Q** = SQL editor, **R** = Results. Percentages are starting weights, which `ctrl+w < > + -` can change. Each preset is a named split tree (Option 2). After any `ctrl+w` move or resize, the layout becomes `custom (from <preset>)` and is remembered.

**1. `classic` (proposed default)**: explorer and schema on the left, query and results on the right.

```
┌ E ──────────┬ Q ───────────────────┐
│             │                      │
├ S ──────────┼ R ───────────────────┤
│             │                      │
└─────────────┴──────────────────────┘
  ~35%           ~65%
```

- **Suits:** ≥100×28.
- **Smaller terminals:** below 100 columns, S hides (E is full height); below 70 columns, focus mode.
- It is what harlequin uses (catalog left, editor over results right, `app.py`/`app.tcss`, section 3), lazygit puts its side column at one third (`gui.sidePanelWidth` 0.3333), and the 120×36 goldens fit all four panes.

**2. `wide`**: three columns, with the schema on the right.

```
┌ E ──────┬ Q ───────────────┬ S ──────┐
│         ├ R ───────────────┤         │
│         │                  │         │
└─────────┴──────────────────┴─────────┘
  ~22%        ~53%              ~25%
```

- **Suits:** ≥160 columns (an ultra-wide monitor or a full screen).
- **Smaller terminals:** below 160 columns it becomes `classic`.

**3. `query`**: SQL first, with the catalog narrow.

```
┌ E ─────┬ Q ─────────────────────────┐
│        │                            │
│        ├ R ─────────────────────────┤
│        │                            │
└────────┴────────────────────────────┘
  ~25%  (S hidden; toggle with ctrl+w c / palette)
```

- **Suits:** 80–120 columns on a laptop.
- **Smaller terminals:** below 70 columns, focus mode.

**4. `browse`**: catalog first. Close to today's explorer screen, with the editor shown.

```
┌ E ─────────────────┬ S ─────────────┐
│                    │                │
├ Q ─────────────────┴────────────────┤
├ R ──────────────────────────────────┤
└─────────────────────────────────────┘
  top ~60%, Q ~15%, R ~25%
```

- **Suits:** ≥100×36, for exploring the catalog.
- **Smaller terminals:** below 30 rows, Q and R collapse into one focus slot, cycled with `ctrl+o`.

**5. `tall`**: one column (portrait).

```
┌ E ─────────┬ S ─────────┐
├ Q ─────────┴────────────┤
├ R ──────────────────────┤
└─────────────────────────┘
```

- **Suits:** narrow and tall terminals, for example a tmux pane at ≤90×40 or more.
- It mirrors lazygit's portrait mode (`portraitMode` auto stacks when width ≤ 84 and height ≥ 46, section 3).
- **Smaller terminals:** below 70 columns, E and S stack (no side-by-side row).

**6. `results-right`**: SQL and results side by side.

```
┌ E ─────┬ Q ──────────┬ R ─────────────┐
├ S ─────┤             │                │
└────────┴─────────────┴────────────────┘
  ~22%     ~33%          ~45%
```

- **Suits:** ≥180 columns when queries are long and results are narrow.
- **Smaller terminals:** below 180 columns it becomes `classic`.

**7. `focus`**: one pane at a time.

```
┌ Q · [E S Q R] ───────────────────────┐
│                                      │
└──────────────────────────────────────┘
```

- **Suits:** any size; it is also the automatic fallback below the minimum sizes (section 4 F5).
- `ctrl+o` and `ctrl+w h/j/k/l` switch panes. The one-line pane bar shows where you are.
- It generalises today's "collapsed below 70 columns, `Enter` zooms" (`views.go:77-83`).

**Why `classic` is the default:**

- It shows all four panes at the sizes the goldens already use.
- It keeps the explorer and schema together (the schema follows the explorer's selection).
- Its first fallback (hiding S) matches today's "detail collapses on small terminals".
- It matches the arrangement of harlequin, the closest product.

**Switching inside the app** (no config file, decision 4):

| Key | Action | Palette name |
|---|---|---|
| `ctrl+w space` | Next preset (tmux binds `next-layout` to `Space` after its prefix, section 3) | "Next layout" |
| (none) | One entry per preset | "Layout: classic", "Layout: wide", … |
| (none) | Back to the last preset | "Reset layout" |
| `ctrl+w H/J/K/L` | Move the focused pane to the far left, bottom, top or right (Vim's `CTRL-W H` "Move the current window to be at the far left, using the full height of the screen") | "Move pane left/down/up/right" |
| `ctrl+w x` | Exchange with the next pane in the same split (Vim `CTRL-W x`) | "Swap pane" |
| `ctrl+w r` | Rotate the panes in the same split (Vim `CTRL-W r`) | "Rotate panes" |
| `ctrl+w < > + -`, `ctrl+w =` | Narrower, wider, taller, shorter; equalise | "Equalise panes" |
| `ctrl+w z` | Maximise or restore the focused pane (tmux `resize-pane -Z`) | "Maximise pane" |
| `ctrl+w c` | Hide the focused pane; the palette's "Show <pane>" brings it back | "Hide pane", "Show schema", … |
| `ctrl+w h/j/k/l` | Move focus in that direction (geometric) | "Go to the pane on the left", … |
| `ctrl+o`, `ctrl+w w`/`W` | Cycle focus in a fixed order: E, S, Q, R | "Next pane" |

- **The `ctrl+w` chords work in every zone and vim mode.** The spec keeps `ctrl+w h/j/k/l` "in every mode (so `ctrl+w` does not delete a word in Insert mode)" (`docs/spec/bqt-v1.md:277`). The editor checks `keys.chordPrefix(ZoneEditor, …)`, which also consults the Global zone (`internal/tui/query.go:129-132`, `keymap.go:207-217`).
- **Key names:** `space` and the capitals are spelled `"space"` and `"H"` (`keyName`, `app.go:642-648`), so chords such as `"ctrl+w space"` and `"ctrl+w H"` fit the registry as they are.
- **Tests to update:** the named-target chords (`ctrl+w l` and `ctrl+w k` → Editor, and so on) become geometric. `TestZonesCycleAndChords` (`internal/tui/query_test.go:44-80`) and the tutorial steps at `tutorial.go:94, 116, 120` change.

## 9. Hiding, maximising and mouse resizing (decision 5)

### 9.1 How other tools hide and zoom panes

| Tool | Zoom / maximise | Hide | Focus while zoomed or hidden | Persisted |
|---|---|---|---|---|
| tmux | `resize-pane -Z` (`prefix z`). `window_zoom()` sets `PANE_ZOOMED`/`WINDOW_ZOOMED`, saves `layout_root` as `saved_layout_root`, and builds a one-cell layout; `window_unzoom()` restores it ([window.c](https://github.com/tmux/tmux/blob/master/window.c)). **The real tree is set aside, not edited.** | No in-place hidden state. `break-pane` moves a pane to its own window and `join-pane` brings it back ([tmux.1](https://github.com/tmux/tmux/blob/master/tmux.1)). | `select-pane -L/-R/-U/-D` unzooms temporarily (`window_push_zoom`/`window_pop_zoom`) to find the neighbour in the *real* layout. Plain `select-pane` stays unzoomed; with `-Z` the zoom follows focus ([cmd-select-pane.c](https://github.com/tmux/tmux/blob/master/cmd-select-pane.c)). | For the server's lifetime (no persistence code; unverified) |
| zellij | `ToggleFocusFullscreen`. `fullscreen_is_active: Option<PaneId>` plus size overrides; `unset_fullscreen()` re-runs the layout solver ([tiled_panes/mod.rs](https://github.com/zellij-org/zellij/blob/main/zellij-server/src/panes/tiled_panes/mod.rs)) | A tiled pane cannot be hidden by the user. `PanesToHide` tracks `beneath_fullscreen` and `covered` separately, "so that one cannot undo the other" ([panes_to_hide.rs](https://github.com/zellij-org/zellij/blob/main/zellij-server/src/panes/tiled_panes/panes_to_hide.rs)). Floating panes can be hidden (`ToggleFloatingPanes`, [actions](https://zellij.dev/documentation/keybindings-possible-actions.html)). | `focus_pane_left_fullscreen`: unset fullscreen, move, re-enter fullscreen. The zoom follows focus. | Unverified |
| Vim | No flag. `CTRL-W _`/`CTRL-W \|` maximise height/width, and the others shrink to `'winminheight'`/`'winminwidth'` (0 shrinks them to a status line or separator) ([windows.txt](https://github.com/vim/vim/blob/master/runtime/doc/windows.txt), [options.txt](https://github.com/vim/vim/blob/master/runtime/doc/options.txt)) | `:hide`/`CTRL-W c` closes the window and keeps the buffer. `:only`/`CTRL-W o` closes the others. The layout must be rebuilt by hand. | A squashed window grows back to at least one line when it is entered | No (sessions aside) |
| Helix | None. `Tree` has only a commented-out `// fullscreen: bool` ([tree.rs](https://github.com/helix-editor/helix/blob/master/helix-view/src/tree.rs)) | `wonly` closes the other views; the documents stay open ([commands.rs](https://github.com/helix-editor/helix/blob/master/helix-term/src/commands.rs), [editor.rs](https://github.com/helix-editor/helix/blob/master/helix-view/src/editor.rs)) | n/a | No |
| lazygit | `screenMode` normal/half/full via `+`/`_`. `getMidSectionWeights` sets the side or main weight to 0, depending on focus ([window_arrangement_helper.go](https://github.com/jesseduffield/lazygit/blob/master/pkg/gui/controllers/helpers/window_arrangement_helper.go)) | Unfocused side windows get `Size: 0` | Focusing a size-0 window (`nextBlock`, `jumpToBlock`) makes it current, and the next layout pass enlarges it. **The zoom follows focus.** | The runtime change "won't change the default" `gui.screenMode` ([Config.md](https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md)) |
| harlequin | `full_screen` reactive (F10) keeps only the focused editor or results ([app.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin/app.py)) | `sidebar_hidden` (`ctrl+b`/F9). `widget.disabled` plus CSS `:disabled {width:0;height:0}` | Textual's focus chain skips disabled widgets ([widget.py](https://github.com/Textualize/textual/blob/main/src/textual/widget.py)). `action_focus_data_catalog` **re-shows the hidden catalog**. Running a query leaves full screen. | No ([editor_cache.py](https://github.com/tconbeer/harlequin/blob/main/src/harlequin/editor_cache.py) keeps only buffers and focus) |

### 9.2 Recommended model for bqt (my judgement)

- **Hide is a flag on the leaf:** `node.Hidden bool`. The pane keeps its slot, so showing it again puts it back where it was.
  - That is harlequin's `disabled` and lazygit's `Size: 0`. It is not Vim's or Helix's close-and-rebuild.
  - `arrange` gives hidden leaves zero size and lets their siblings take the space.
  - A split whose children are all hidden takes no space.
  - Invariant: at least one pane stays visible, so `ctrl+w c` on the last one is refused with a toast.
- **Maximise is a separate field:** `layout.Zoomed Pane`, never a change to the tree. That is the tmux and zellij model.
  - While it is set, `arrange` returns one full rectangle.
  - The F5 small-terminal fallback is the same mechanism, computed rather than chosen.
- **Focus rules:**
  - `ctrl+o`, `ctrl+w w` and `ctrl+w h/j/k/l` **skip hidden panes**, as Textual's focus chain does.
  - **Jumping to a pane by name shows it** if it is hidden, as harlequin's `action_focus_data_catalog` does. "Jumping by name" means the palette's "Go to schema", and actions that need a pane: `s` needs the Editor, `p` needs the Results, and `Enter` on a table can focus the Schema.
  - **While maximised, the zoom follows focus**, as in lazygit, zellij and tmux `-Z`: `ctrl+o` and the directional keys show the next pane maximised. The direction is computed on the *real* layout, as tmux's `window_push_zoom`/`window_pop_zoom` do. `ctrl+w z` again restores the layout.
  - **Running a query while the editor is maximised** should un-maximise, or at least switch the zoom to the Results. harlequin leaves full screen on a run. Open question 6.
- **What is persisted:**
  - The tree and the hidden flags go in `state/tui.json` (`"pane_layout"`, section 10.4).
  - The zoom is not persisted, as in all the tools above: a restart shows the full layout.
- **Keys:**
  - `ctrl+w z` maximises or restores.
  - `ctrl+w c` hides.
  - `ctrl+w o` ("only", Vim and Helix) hides all others. It is optional.
  - The palette lists "Show <pane>" for each hidden pane and "Show all panes".
  - The explorer's `z` (`keymap.go:77`) can stay as an alias of `ctrl+w z` for the Schema.

### 9.3 Mouse: what the stack supports

- **Bubble Tea v2.1.0:**
  - `View.MouseMode`:
    - `MouseModeCellMotion` is click, release, wheel, and motion while a button is held. It "will try to enable the mouse in extended mode (SGR)".
    - `MouseModeAllMotion` is all motion (`tea.go:297-319`).
  - The renderer writes `ansi.SetModeMouseButtonEvent + ansi.SetModeMouseExtSgr` for CellMotion and `SetModeMouseAnyEvent + …ExtSgr` for AllMotion (`cursed_renderer.go:146-152`). Those are DEC modes 1002, 1006 and 1003 (`github.com/charmbracelet/x/ansi@v0.11.9/mode.go:472-523`).
  - The mode is compared against the previous frame and switched (`cursed_renderer.go:385-406`). So **a runtime "mouse on/off" toggle is just a model field** read by `View()`.
  - The messages are `MouseClickMsg`, `MouseReleaseMsg`, `MouseWheelMsg` and `MouseMotionMsg`, with zero-based `X, Y`, `Button` and `Mod` (`mouse.go:46-128`).
  - **Every mouse message also reaches `Update`** (`tea.go:824-831` calls `renderer.onMouse`, then `model.Update(msg)` at `tea.go:899`). `View.OnMouse` is an optional extra hook; its command runs in a goroutine (`go p.Send(cmd())`, `tea.go:829-830`). bqt can handle the mouse in `update` against the rectangles from F1, with no `OnMouse` and no compositor.
- **Lip Gloss v2.0.6:** `Compositor.Hit(x, y) LayerHit` returns the top-most layer with an ID (`layer.go:286-300`). It is only useful if panes become `Layer`s (section 4 F2). A rectangle lookup over `map[Pane]Rectangle` is simpler.
- **Terminals** ([xterm ctlseqs, "Mouse Tracking"](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html)):
  - 1002 reports motion only while a button is held, and only when the pointer reaches a new cell.
  - 1003 reports all motion.
  - X10 coordinates stop at 223.
  - SGR (1006) has no such limit and tells press (`M`) from release (`m`).
  - Bubble Tea always asks for SGR.
- **tmux:**
  - Its `mouse` option is off by default, yet "applications inside panes can use the mouse even when 'off'" ([options-table.c](https://github.com/tmux/tmux/blob/master/options-table.c)).
  - With `mouse on`, tmux handles its own border drags (`MouseDrag1Border { resize-pane -M }`) and forwards pane events to apps that asked for the mouse (`send -M` when `#{mouse_any_flag}`, [key-bindings.c](https://github.com/tmux/tmux/blob/master/key-bindings.c)).
  - It re-encodes those events as SGR only if the app asked for 1006 *and* the outer terminal sent SGR ([input-keys.c](https://github.com/tmux/tmux/blob/master/input-keys.c) `input_key_get_mouse`).
  - Dragging a *bqt* divider inside tmux works. Dragging the *tmux* border resizes tmux panes, as expected.
- **Text selection:** once an app enables mouse reporting, a plain drag no longer selects text. The usual bypass:
  - Shift in xterm (`XTSHIFTESCAPE`, ctlseqs), [kitty](https://github.com/kovidgoyal/kitty/blob/master/kitty/options/definition.py) (`shift+left … grabbed`), [WezTerm](https://wezterm.org/config/lua/config/bypass_mouse_reporting_modifiers.html) (`bypass_mouse_reporting_modifiers`, default `SHIFT`) and [Alacritty](https://alacritty.org/config-alacritty.html).
  - Option in [iTerm2](https://iterm2.com/documentation-preferences-profiles-terminal.html).
  - lazygit's docs warn about this for `gui.mouseEvents` ([Config.md](https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md)).
  - bqt users copy SQL and cells with the keyboard (OSC 52 yanks and `y`/`Y`/`gt`, `docs/spec/bqt-v1.md:272`, `:466`). Losing plain-drag selection is still a real cost.

### 9.4 How the others drag a divider

- **tmux:**
  - `MouseDrag1Border` calls `resize-pane -M`. That installs `cmd_resize_pane_mouse_resize_tiled` as `mouse_drag_update` ([cmd-resize-pane.c](https://github.com/tmux/tmux/blob/master/cmd-resize-pane.c)).
  - On each motion it finds the border cells under the previous position (`layout_search_by_border`) and applies the *delta* to the parent split with `layout_resize_layout`.
- **zellij:**
  - A press on a pane frame edge gives `StartResize{pane_id, edge, …}`; motion gives `ContinueResize`; release gives `StopResize`.
  - `PaneResizeState{edge, start_position, start_geom}` holds the drag.
  - `edge_and_delta_to_strategies` turns deltas into the same `ResizeStrategy` that keyboard resize uses ([tab/mouse_handler.rs](https://github.com/zellij-org/zellij/blob/main/zellij-server/src/tab/mouse_handler.rs)).
  - Ctrl+wheel also resizes (`mouse_scroll_resize`, [default.kdl](https://github.com/zellij-org/zellij/blob/main/zellij-utils/assets/config/default.kdl)).
- **Vim** resizes "by dragging a status line … or a vertical separator" when `'mouse'` is set (windows.txt).
- **lazygit** (click to focus, wheel to scroll) and **Helix** (click to focus, drag to select) have no border drag (lazygit: not found, unverified; Helix: [ui/editor.rs](https://github.com/helix-editor/helix/blob/master/helix-term/src/ui/editor.rs)).
- I found no ratatui or Bubble Tea divider-drag widget (unverified). Cursive has `cursive-split-panel` ([docs.rs](https://docs.rs/cursive-split-panel)).

### 9.5 Effort and risk for bqt, and recommendation

| Step | What | Effort (my estimate) | Risk |
|---|---|---|---|
| M0 | `View().MouseMode` from a model field, off by default; palette "Toggle mouse", remembered in state | ~20 lines | None while it is off |
| M1 | **Click to focus** (rectangle lookup), **wheel scrolls** the pane under the pointer (explorer list, schema, editor, results grid) | ~100 lines | Low. Clicking inside the editor to move the cursor is extra work, because it needs the gutter and horizontal scroll (`editor.go:574-604`) |
| M2 | **Drag a divider.** Hit-test the separator cells: `vsep` columns (`ui.go:228-234`) and the title row of the lower pane (bqt draws no horizontal rule between editor and results). On press, record `{split node, child index, start}`; on motion, apply the delta to the two neighbouring weights (the tmux/zellij delta model, clamped to the F5 minimums); on release, save the state | ~150–250 lines | Medium. Off-by-one at borders; a drag that starts on a title row must not also act as a click; minimum sizes |
| Tests | Send `tea.MouseClickMsg`, `MouseMotionMsg` and `MouseReleaseMsg` through the rig's `send` (`internal/tui/harness_test.go:167`) and assert the zone and weights. **Goldens do not change**: the mouse draws nothing (no hover). Add one golden after a drag. | small | Low |

**Recommendation (my judgement):**

- Ship the layout first with keyboard control only, including `ctrl+w < > + - =`.
- Then add **M0 and M1** together, off by default, and **M2** after that.
- Keep the mouse opt-in: bqt is keyboard-first (`docs/spec/bqt-v1.md:9`), and turning it on costs plain-drag selection in every terminal listed above.
- Note the Shift/Option bypass in the help text, as lazygit does.

## 10. Naming: "layout" and the explorer styles (decision 6)

### 10.1 Terms already in use

- **`CONTEXT.md`** defines Favorite, History entry, Run, Execution project, Accessible, Selected and Cached project. It has **no** entry for layout, Explorer, pane or zone.
- **The spec** uses "Layouts (TUI-only)" for Miller/tree/flat (`docs/spec/bqt-v1.md:210-214`, also `:238` "in all three layouts" and `:541`), "pane placement" for `P` (`:437`), "Zones" (`:363-365`, `:421`) and "the last layout" in state (`:143`).
- **"View" is already taken.**
  - It is a BigQuery table type: `type:view`, `api.TypeView`, and the `V` letter in rows (`internal/tui/ui.go:44-45`, `docs/spec/bqt-v1.md:223`).
  - It is also Bubble Tea's `View()`.
  - So "explorer view" would be ambiguous ("views" are rows in the explorer).
- **"Mode" is already taken** by vim's Normal and Insert modes (`docs/spec/bqt-v1.md:267`).

### 10.2 Proposal

| Concept | Name | Key | Palette | Status line |
|---|---|---|---|---|
| Arrangement of E/S/Q/R | **Layout** (presets: classic, wide, query, browse, tall, results-right, focus; edited = custom) | `ctrl+w space` (next) | "Next layout", "Layout: <name>", "Reset layout" | `layout classic` (optional) |
| Miller/tree/flat | **Explorer style**: "columns" (Miller columns), "tree", "list" (the flat, ranked list) | `L` (kept; read it as "list style") | "Cycle explorer style" | `columns` / `tree` / `list` |

- **Runner-up for the second name:** "catalog style". But the pane is called the Explorer everywhere: the zone, the tutorial, the README.
- **Keeping `miller` and `flat` as style names** would avoid rewriting the 50 goldens that show the name in the status line (count below). Only the *axis* would be renamed. That is a choice for the user: open question 3.

Proposed glossary entries for `CONTEXT.md`:

> **Layout**: How the four panes (Explorer, Schema, Editor, Results) are arranged on screen: a named preset, or a custom arrangement made with the `ctrl+w` keys. TUI-only, remembered in `state/tui.json`. _Avoid_: arrangement, screen mode, pane placement.
>
> **Explorer style**: How the Explorer lists the catalog: Miller columns, tree or flat list. _Avoid_: layout, view (a view is a BigQuery table type).

### 10.3 What has to be renamed

- **Code** (`internal/tui`):
  - The type and values: `layoutKind`, `layoutMiller`, `layoutTree`, `layoutFlat` and `layoutNames` (`app.go:24-32`).
  - The model field `m.layout` (`app.go:99`) and about 75 uses of it and of the `layout*` constants in `explorer.go` (35), `app.go` (19), `filter.go` (11), `views.go` (7) and `tutorial.go` (3) (`grep -n 'm.layout\|layout[A-Z]'`).
  - The functions: `setLayout` (`explorer.go:500`) and `layoutFilter` (`filter.go:238-247`).
  - The action `"layout"` and its binding text ("cycle layout…", "Cycle layout", "L layout") (`keymap.go:74`; `app.go:885-888`).
  - The tutorial's `tutSnap.layout` and `t.layouts` (`tutorial.go:39, 46, 52, 143, 222-223`).
  - The status line (`views.go:159-161`).
  - Comments that say "flat layout" or "tree layout" (`search.go:11, 25, 29`; `filter.go:13`; `explorer.go:11, 509`).
  - **Not affected:** `grid.layout` (`grid.go:146-148`), which lays out the grid's columns, and `queryLayout` (`queryview.go:14-41`), which disappears with the layout work anyway.
- **Tests:**
  - `TestLayoutCycleKeepsSelection`, `TestLayoutIsRemembered`, `TestSlashFiltersInPlaceInEveryLayout`, `TestFlatLayoutFilterScopeAndEsc`, `TestFlatLayoutExpandsShardFamilies` and `TestFlatLayoutRestoredAtStartupIsNotEmpty` (`internal/tui/tui_test.go:136, 159, 184, 247, 391, 406`).
  - `TestEachLayoutKeepsItsOwnFilter` (`internal/tui/filter_test.go:128`) and `TestFlatExplorerLayoutDoesNotStealEditorKeys` (`internal/tui/query_test.go:565`).
  - Some tests assert the status-line word: `r.contains(" " + layout)` (`tui_test.go:145, 191`).
- **Goldens:**
  - 25 of the 71 files under `internal/tui/testdata/` contain the footer hint `L layout`.
  - 50 contain a style name in the status line (`grep -l`).
  - Both sets change if the hint or the names change. The layout work rewrites most of them anyway.
- **Tutorial:** the step "Layouts" ("`L` cycles the Explorer layout…", `tutorial.go:86`). The mentions of "the tree layout" and "the Miller layout" (`tutorial.go:78, 82`).
- **Spec:**
  - §8.1 title and text (`docs/spec/bqt-v1.md:210-214`), `:238`, `:541`.
  - §3 "layouts, panes" (`:67`), §5 "the last layout" (`:143`), §15 rows (`:437`), §18 (`:521-522`).
  - The decision reverses §8.1, §8.4 and §15, so it is worth an ADR in `docs/adr/` (there is one ADR, `0001`).
- **README:** `README.md:37` ("`L` cycles the layout (Miller columns, tree, flat list), `p` previews rows").

### 10.4 State-file migration

- Today the file is `{"layout":"miller","panes":"right","zoom_stacked":true}` (`app.go:225-230`).
- **The file is shared by instances, last writer wins** (`docs/spec/bqt-v1.md:143`). So an *older* bqt in another tmux pane can read a file a newer one wrote.
- If the new layout were stored under the same key `"layout"` as an object, the old binary's `json.Unmarshal` into `Layout string` would fail as a whole, and it would drop all its state (`app.go:238-241`). That is "nothing critical", but avoidable.
- **Recommendation:**
  - Write **`"explorer_style"`** (a string) and **`"pane_layout"`** (a preset name, or the custom tree as an object). The UI and code still call it "layout".
  - On load, if `"explorer_style"` is missing, read the legacy string `"layout"` (accepting `miller`, `tree` and `flat`, and the new style names).
  - Map the legacy `"panes"` value to a starting preset: `right` → `classic`; `bottom` → `browse`; `hidden` → `query`; `auto` → `classic`.
  - Ignore `"zoom_stacked"`.
  - Unknown values fall back to defaults, as they do today (`app.go:242-251`).
  - Old binaries reading a new file find no `"layout"` key and use Miller; nothing breaks.

## 11. Open questions for the user (revised 2026-10-26)

The 2026-10-24 questions are answered (see Decisions). Still open:

1. **Keys:** confirm `p` (free sample into Results) and `s` (SELECT into a new buffer), or prefer the runner-ups `d` and `I` (section 7.2).
2. **A fresh empty buffer:** should `s` reuse an empty, never-edited scratch buffer such as the startup `query 1`, or always open another (section 7.3)? Should the new buffer be named after the table?
3. **Explorer style names:** rename the styles too (`columns` / `tree` / `list`), or rename only the axis and keep `miller` / `tree` / `flat`? Keeping them leaves about 50 golden status lines unchanged (section 10.2).
4. **Presets:** ship all seven (section 8), or start with `classic`, `query`, `browse`, `tall` and `focus`? Is `classic` the right default? Should the status line show the layout's name?
5. **Sample paging:** with the grid, the sample pages through the whole table on demand (still free), and the old 1,000-row cap goes away. Is that wanted? Should the sample be exportable later (free, through `tabledata.list`)?
6. **Maximise and runs:** when a Run starts while the editor is maximised, keep the zoom, move it to the Results, or leave the zoom? harlequin leaves full screen (section 9.2).
7. **Mouse:** is the phased, off-by-default plan in section 9.5 acceptable? Should M2 (drag dividers) be built at all?
8. **Partition filter (carried over):** should `s` add a `WHERE` on the partition column for partitioned tables? The column is known; whether a filter is *required* is not (section 5).
9. **Shard families:** sample the newest shard, and query it with `_TABLE_SUFFIX = '<newest>'`? Which member `Lookup` picks today is unverified (section 6.3).
