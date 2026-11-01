# Contributing

Bug reports, ideas and pull requests are welcome.

## Before you start

- **Bugs and ideas:** open an issue. For a security problem, follow [SECURITY.md](SECURITY.md) instead.
- **Larger changes:** open an issue first so we can agree on the approach. The design lives in [`docs/spec/bqt-v1.md`](docs/spec/bqt-v1.md) and the domain terms in [`CONTEXT.md`](CONTEXT.md).

## Making a change

Needs Go 1.26 or newer.

```sh
go vet ./... && go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

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

The live suite also runs a few queries that read no tables (`BQT_LIVE_LOCATION` sets their job location).

Scale measurements: `BQT_SCALE=500 go test ./internal/store -run Scale -v` (catalog apply) and `BQT_SCALE=1 go test ./internal/search -run LatencyReport -v` (search latency at 50k tables).

## Releasing

Pushing a tag `v*` runs `.github/workflows/release.yml`, which builds the binaries with GoReleaser (`.goreleaser.yaml`) and publishes a GitHub release. Try the config locally with `goreleaser release --snapshot --clean`.

The project page in `site/` is published to GitHub Pages by `.github/workflows/pages.yml` on every push to `master` that changes it.

## License

bqt is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution you agree that it is licensed under the same terms, as described in section 5 of the license.
