---
name: run-bqt-emulator
description: Launch the real bqt TUI against a local BigQuery emulator and drive it in tmux. Use to run, smoke-test or screenshot bqt, or to check a TUI change end to end without a GCP project.
---

# Run bqt against the BigQuery emulator

The real binary, a real terminal (tmux), a local [goccy/bigquery-emulator](https://github.com/goccy/bigquery-emulator) in Docker (colima). No credentials and no GCP project are needed; `BQT_BIGQUERY_ENDPOINT` points every BigQuery client at the emulator (spec §16).

## 1. Set up

```bash
.claude/skills/run-bqt-emulator/setup.sh
```

It starts colima if needed, runs the emulator container `bqemu` on port 9050 with the seed data, builds `bqt` from the working tree, and syncs a fresh catalog into `.scratch/emu/`. Re-run it after changing code: it rebuilds and resets all state.

Done when it prints `ready:`.

Seed data in project `test-proj`:
- `sales`: `orders` (700 rows), `customers`, the view `v_big_orders`, and `orders_part` (DATE-partitioned, requires a partition filter).
- `events`: the shard family `events_20240101` to `events_20240103`.
- `empty_ds`: an empty dataset.

## 2. Drive it

```bash
. .claude/skills/run-bqt-emulator/drv.sh
start 120 36            # waits for the status line ("layout:")
k j; k l; k p           # keys by tmux name: C-w, Enter, Escape, Space, F1
lit "select 1"          # literal text
cap                     # read the screen
size 80 24              # resize the terminal
quit
```

`drv.sh` also has `click X Y`, `wheel 65 X Y` (64 up, 65 down) and `mouseflags` for the mouse. Turn the mouse on first with the palette's "Mouse: on/off". A key does something only in the focused Pane; read the footer line (`cap | tail -1`) to see which Pane has focus before sending keys. The key reference is the help overlay (`F1`) or spec §15.

The editor starts in vim Normal mode: `k i` before typing, `k Escape` after, `k Enter` runs.

## Emulator gaps

These are emulator limits, not bqt bugs:

- **Sync:** it never runs bqt's own sync against the emulator. bqt's sync needs dataset locations and `INFORMATION_SCHEMA` views the emulator lacks, so `setup.sh` fills the catalog with `emuseed/` (from `tables.get`). `r` and `R` in the TUI, and `bqt sync`, fail and mark the datasets inaccessible; re-run `setup.sh` to recover.
- **Sample paging:** `tabledata.list` ignores `startIndex`, so a Sample's pages past the first repeat the first rows. Query results page correctly.
- **Pseudo-columns:** the emulator has no ingestion-time partitioning, so it doesn't create the `_PARTITIONTIME` / `_PARTITIONDATE` pseudo-columns.
- **Wildcard tables:** results include an extra `_TABLE_SUFFIX` column.

## Clean up

```bash
docker rm -f bqemu; colima stop
```
