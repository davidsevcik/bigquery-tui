# bqt

A keyboard-driven terminal tool for browsing, searching, writing and running BigQuery queries across many GCP projects, built to sit next to AI agents.

Status: **phases 1 and 2**. This build has the config file, the local metadata catalog, sync, catalog search and the interactive explorer. The query editor, query runs, History and Favorites, and the agent layer follow the plan in [`docs/spec/bqt-v1.md`](docs/spec/bqt-v1.md).

## Build

Needs Go 1.26 or newer.

```sh
go build -o bin/bqt ./cmd/bqt
```

## Use

```sh
gcloud auth application-default login   # Application Default Credentials only
bin/bqt init --execution-project my-gcp-project
bin/bqt config edit                      # add the projects to sync
bin/bqt                                  # the interactive explorer (syncs in the background)
bin/bqt sync                             # or fill the local catalog from a script or cron
bin/bqt search 'orders p:prod -d:tmp'    # search the catalog from the command line
bin/bqt status                           # freshness and sync problems
bin/bqt doctor                           # paths, versions, auth, catalog age
```

In the explorer: `hjkl` or the arrows move, `/` searches, `ctrl+t` jumps to a table, `ctrl+p` opens the command palette, `L` cycles the layout (Miller columns, tree, flat list), `p` previews rows, `?` lists every key.

Search syntax: free terms match names (and descriptions last); filters are `p:` project, `d:` dataset, `type:`, `col:`, `label:`, `desc:`; `-d:tmp` excludes; `type:view,mview` is OR; a pasted `project.dataset.table` is an exact lookup. Columns are searched only with `col:`.

Output is JSON when stdout is not a terminal and text otherwise (`--format json|text`). Exit codes: 0 ok, 1 failure, 2 usage, 3 config, 4 auth, 5 over limit, 6 BigQuery error (also: a sync with failed units), 7 cancelled.

The config file lives at `~/.config/bqt/config.toml` (or `$BQT_CONFIG`, or `--config`). Unknown keys are errors. See the comments in the file `bqt init` writes.

## Develop

```sh
go vet ./... && go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

Tests use hand-written fakes behind the BigQuery ports. An optional live suite runs read-only against a real project:

```sh
BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_PROJECT=... go test -tags live ./internal/bq -run Live -v
```

Scale measurements: `BQT_SCALE=500 go test ./internal/store -run Scale -v` (catalog apply) and `BQT_SCALE=1 go test ./internal/search -run LatencyReport -v` (search latency at 50k tables). Terminal screens are golden files in `internal/tui/testdata`; regenerate with `go test ./internal/tui -update` after checking the change.
