# 3. The core is public, under `pkg/`

Date: 2026-11-25
Status: accepted

## Context

Spec section 3 put every package under `internal/` for v1 and planned a future GUI as `cmd/bqt-gui` in the same module. The GUI is now planned as a separate project in its own module. Go refuses imports of `internal/` packages from outside the module, so that project could not use the engine, the store, sync or any other part of the core.

## Decision

- The core packages (the ones spec section 3 lists as core: `api`, `config`, `store`, `sync`, `bq` and `bq/bqtest`, `engine`, `logging`, `version`, `search`, `format`, `sqlx`, `runs`, `policy`, `favorites`, `export`, `handoff`, `selection`, `shard`) move from `internal/` to `pkg/`. Their names and contents do not change.
- The frontends (`tui`, `cli`, `mcp`), the tests that are not a package's own (`archtest`, `smoke`) and the developer tools (`tools/*`) stay under `internal/`. `cmd/bqt` stays where it is.
- `internal/archtest` treats every package under `pkg/` as core, so a new core package is checked without a list to update. Besides the UI libraries and the frontends, core packages may not import anything under `internal/`: a public package that exposed an internal type would give other modules a value they cannot name.

## Consequences

- Another module can `go get github.com/davidsevcik/bigquery-tui` and import `pkg/engine` and the rest. `bq/bqtest` (the fake BigQuery, contract suite and synthetic catalog) is public too, so a GUI can test against the same fakes.
- Every exported identifier under `pkg/` is now API for other modules. While the module is at v0 it can still change, but a change there can break the GUI, which the archtest does not catch; the GUI's own build does.
- `pkg/` was chosen over packages at the module root to keep the root to docs, `cmd/` and configuration, and to make the core and frontend split visible in the path.
