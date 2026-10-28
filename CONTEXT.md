# bqt

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents.

## Language

**Favorite**:
A query the user chose to keep, stored as a plain `.sql` file with a small header for tags and description.
_Avoid_: Saved query, bookmark, snippet

**History entry**:
An automatic record of one query run: its SQL, project, bytes billed, duration and status. A History entry can be promoted to a Favorite.
_Avoid_: Past query, log line

**Run**:
One execution of a SQL snapshot, with its own state, dry-run estimate and result pager. Several Runs can be in flight at once. A finished Run produces a History entry.
_Avoid_: Job (that is the BigQuery object behind a Run), query tab

**Execution project**:
The GCP project that query and sync jobs run in, chosen in the Projects dialog (`e`) or with `bqt projects exec`; until the user first chooses one, it is the earliest-selected project that is still selected. Under on-demand pricing it is billed for the bytes processed; under capacity pricing it is assigned to a reservation, and slots are billed to the reservation's administration project. It can differ from the project whose data is being read. `--execution-project` and `BQT_EXECUTION_PROJECT` override it for one invocation.
_Avoid_: Billing project, default project, quota project

**Accessible project**:
A project BigQuery's projects list returns for the user's credentials (projects where they hold a project-level role). The Projects dialog lists them.
_Avoid_: Available project, visible project

**Selected project**:
A project the user chose in the Projects dialog or with `bqt projects select`. Only Selected projects are synced automatically and shown in the TUI. It may be an Accessible project or one added by ID (data shared at dataset level is not listed).
_Avoid_: Storage project, data project, configured project, project that is on

**Cached project**:
A project that has metadata in the catalog. A deselected project stays cached: the CLI and MCP still read it (with a warning once it is older than `sync_ttl`), but nothing refreshes it except an explicit request: `bqt sync --project`, or `s` on its row in the Projects dialog.
_Avoid_: Stale project, archived project

**Pane**:
One of the four areas of the TUI: Explorer, Schema, Editor and Results. Overlays (the palette, pickers, popups) are not Panes.
_Avoid_: Zone (the old word, still used in code), window, view (a BigQuery table type)

**Layout**:
How the four Panes are arranged on screen: a split tree, either one of the named presets (`classic`, `query`, `browse`, `tall`, `focus`) or the user's edit of one (`custom`). Switched inside the TUI and remembered in its state file, never set in the config (ADR 0002).
_Avoid_: Screen, arrangement; "layout" for the Explorer's columns/tree/list drawing (that is the Explorer style)

**Explorer style**:
How the Explorer draws the catalog: `columns` (Miller columns), `tree` or `list`. Cycled with `L`.
_Avoid_: Layout, view, mode

**Sample**:
Rows of a stored table read free through `tabledata.list` and shown in the Results Pane. A Sample is not a Run: it starts no job, costs nothing and never becomes a History entry. Views and external tables have no Sample.
_Avoid_: Preview, query result, peek
