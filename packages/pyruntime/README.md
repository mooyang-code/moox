# MooX Python Runtime

`packages/pyruntime` provides the reusable process, pool, frame protocol and
immutable module publishing primitives used by MooX Python workers. Business
modules keep their own task and result codecs.

The runtime deliberately supports one transport encoding: bounded JSON inside
the `moox.py/v1` binary frame. Factor and Strategy both use that path today.
Arrow and shared mmap snapshots were removed because no production caller used
them; they can be designed again when a measured workload needs them.

The Python counterpart under `python/moox_pyruntime` provides the same frame
contract plus bounded stdout/stderr capture.

Run package tests:

```bash
go test ./...
GOWORK=off go test -race ./...
PYTHONPATH=python python3 -m pytest python/tests -q
```
