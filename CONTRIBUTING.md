# Contributing to Kratix

Thank you for considering contributing to Kratix! Before submitting code, please ensure your development environment is set up correctly.

## Linting

Kratix uses [golangci-lint](https://github.com/golangci/golangci-lint) for linting Go code. Before running `make lint`, install the required version of `golangci-lint`:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6
```

Make sure the installed binary is available on your `PATH` (typically `$(go env GOPATH)/bin`). After installation and confirming the binary is on your `PATH`, you can run:

```bash
make lint
```
