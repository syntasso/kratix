# Kratix introspection using Kamera

[kamera](https://github.com/tgoodwin/kamera) is a simulation toolkit for
observing, analyzing, and verifying the behavior of Kubernetes control planes.

It can be used to detect non-desired situations, such as order-based
convergence, and ensure correctness of execution.

It can also aid with debugging controller behaviour by showing states and
changes between reconciliation runs.

## Running the examples

The examples are split into separate runs so that Kamera explores each interaction
independently.

```bash
go run works.go
```

For Promise/PromiseRevision creation:

```bash
go run promises.go
```

To disable the interactive inspector UI:

```bash
go run works.go -interactive=false
go run promises.go -interactive=false
```

Makefile shortcuts:

```bash
make works ARGS=-interactive=false
make promises ARGS=-interactive=false
```

## References

- https://thenewstack.io/kamera-uses-simulation-to-verify-kubernetes-controller-logic/
- https://www.youtube.com/watch?v=E1f87Z6mij0
