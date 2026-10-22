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

- Tests use the fakes in `internal/bq/bqtest`; no Google Cloud account is needed. The optional live suite is described in the README.
- Core packages must not import UI libraries or frontend packages. `internal/archtest` enforces this.
- Terminal screens are golden files in `internal/tui/testdata`. If a change alters a screen, regenerate with `go test ./internal/tui -update` and check the diff.
- Keep pull requests focused on one change, and say in the description what it fixes or adds.

## License

bqt is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution you agree that it is licensed under the same terms, as described in section 5 of the license.
