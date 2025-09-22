# Contributing to Kratix

Thank you for considering contributing to Kratix! 

Before submitting code, please ensure your development environment is set up correctly and that you run `make test` and `make lint`.

## Development workflow

Before submitting your changes, please:

1. [Install Go](https://go.dev/doc/install) and ensure the toolchain is available in your `$PATH`.
2. Install [`golangci-lint`](https://golangci-lint.run/usage/install/).
3. Run `make test` to execute unit tests.
4. Run `make lint` to run static analysis.
5. Run `make system-test` to run the system tests.
6. Follow any instructions documented in an `AGENTS.md` file if present.

Running the tests and linter locally helps catch issues early and ensures consistency across contributions.

## Linting

Kratix uses [golangci-lint](https://github.com/golangci/golangci-lint) for linting Go code. Before running `make lint`, install the required version of `golangci-lint`:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6
```

Make sure the installed binary is available on your `PATH` (typically `$(go env GOPATH)/bin`). After installation and confirming the binary is on your `PATH`, you can run:

```bash
make lint
```




