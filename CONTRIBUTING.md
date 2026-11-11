# Contributing

Bug reports, ideas and pull requests are welcome.

## Before you start

- **Bugs and ideas:** open an issue. For a security problem, follow [SECURITY.md](SECURITY.md) instead.
- **Larger changes:** open an issue first so we can agree on the approach. The design lives in [`docs/spec/bqt-v1.md`](docs/spec/bqt-v1.md) and the domain terms in [`CONTEXT.md`](CONTEXT.md).

## Making a change

Needs Go 1.26 or newer. Before you push, run the checks:

```sh
scripts/quality.sh
```

It needs nothing but Go (each tool runs through `go run` at a pinned version) and prints six sections in a fixed order: lint (golangci-lint with `.golangci.yml`, which includes vet and staticcheck), the race-enabled tests with coverage per package, the ten most complex functions, dead code, known vulnerabilities, and whether `go.mod` is tidy. It exits non-zero if lint, tests, the vulnerability scan or the tidiness check fails; coverage, complexity and dead code are only reported. CI runs the same checks.

The lint rules allow no blanket suppressions. If a finding cannot be fixed, suppress it on that line with the linter's name and a reason: `//nolint:gosec // why`.

- Tests use the fakes in `internal/bq/bqtest`; no Google Cloud account is needed. The optional live suite is described below.
- Core packages must not import UI libraries or frontend packages. `internal/archtest` enforces this.
- Terminal screens are golden files in `internal/tui/testdata`. If a change alters a screen, regenerate with `go test ./internal/tui -update` and check the diff.
- Keep pull requests focused on one change, and say in the description what it fixes or adds.

To build a binary from a checkout:

```sh
go build -o bin/bqt ./cmd/bqt
```

To see the architecture (packages, the imports between them, and each package's exported API), generate the map from the current source:

```sh
go run ./internal/tools/archmap -open   # writes bin/architecture.html and opens it
```

## Live and scale tests

An optional live suite runs read-only against a real project:

```sh
BQT_LIVE_EXECUTION_PROJECT=... BQT_LIVE_PROJECT=... go test -tags live ./internal/bq -run Live -v
```

The live suite also runs a few queries that read no tables (`BQT_LIVE_LOCATION` sets their job location). The Storage Read API checks (`-run TestLiveStorageRead`) compare both read paths of sync and, with `BQT_LIVE_NO_READ_SESSION_PROJECT` naming an Execution project without `bigquery.readsessions.create`, the fallback to REST paging (spec section 17 item 15).

Scale measurements: `BQT_SCALE=500 go test ./internal/store -run Scale -v` (catalog apply) and `BQT_SCALE=1 go test ./internal/search -run LatencyReport -v` (search latency at 50k tables).

## Releasing

Pushing a tag `v*` runs `.github/workflows/release.yml`, which builds the binaries with GoReleaser (`.goreleaser.yaml`) and publishes a GitHub release. Try the config locally with `goreleaser release --snapshot --clean`.

The project page in `site/` is published to GitHub Pages by `.github/workflows/pages.yml` on every push to `master` that changes it.

The screenshots in `site/img` (used by the page and the README) are the real bqt in a real terminal, on fictional data from a local BigQuery emulator. When the UI changes, record them again with `internal/tools/screenshots/record.sh` (needs Docker, Go and python3). Each screen is a [VHS](https://github.com/charmbracelet/vhs) tape in that folder.

## License

bqt is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution you agree that it is licensed under the same terms, as described in section 5 of the license.
