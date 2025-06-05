# Repository Guidelines

To maintain code quality, **all unit tests must be executed via `make test`**. Contributors should run this command and ensure it succeeds before committing their changes.

Additional notes:
- `make test` relies on the Go toolchain and requires necessary build tools.
- Some targets in the `Makefile` download dependencies to `bin/` automatically if missing.
- Running `make test` will also format (`fmt`) and vet the project as part of the test workflow.
- `make lint` must pass; do not alter the lint configuration.
