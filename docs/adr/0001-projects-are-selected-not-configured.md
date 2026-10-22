# 1. Projects are selected in bqt, not configured

Date: 2026-10-22
Status: accepted

## Context

v1 kept the user's working set in the config file: `projects`, an explicit allowlist of data projects to sync (with no discovery), and `execution_project`, a required key naming where jobs run. To add a project the user had to know its exact ID and edit the file; a fresh install could run nothing until they did. The Projects dialog (`ctrl+x p`) could only turn configured projects on or off for the TUI, and a project that was off was still synced.

## Decision

The working set leaves the config and becomes state that bqt manages:

- The Projects dialog lists every project BigQuery's own projects list (REST `projects.list`) returns for the credentials. This is not Resource Manager discovery: it needs no extra permission and only lists. Nothing is synced until the user selects a project.
- The Selected projects and the chosen Execution project live in `state/projects.json` in the data directory, shared by the TUI, the CLI (`bqt projects`) and the MCP server. Not in `state/tui.json` (TUI-only) and not in the catalog (it can be recreated and would lose user intent).
- Only Selected projects are synced automatically. Deselecting stops the syncing but keeps the cached metadata: selecting again is instant, the CLI and MCP can still read it (with a staleness warning), and `bqt sync --project` can refresh it. Only `ignore` patterns prune the catalog.
- The Execution project defaults to the earliest-selected project still selected; once chosen it can be replaced but not cleared. `--execution-project` and `BQT_EXECUTION_PROJECT` stay as one-invocation overrides.
- The config keys `projects` and `execution_project` are rejected with an error that points at the dialog. Old values are not imported.

## Consequences

- Reverses spec 4.2 ("explicit allowlist, no discovery", "`execution_project` required") and the on/off model of spec 8.1; the spec is updated.
- The selection is per data directory, not per config file.
- Two processes changing the selection at once: the last write wins (atomic rename).
- Cached metadata of deselected projects is never cleaned up on its own.
