# MooX Python Runtime

`packages/pyruntime` is a reusable Go library that Factor may use for its
Python worker runtime. It owns worker processes, `moox.py/v1` frames, Arrow
transport, immutable source publishing and shared snapshots; business modules
still provide their own task/result codec. This package does not make Python a
runtime dependency of Storage, EventBus, Archive, View or Trade. Strategy keeps
its existing independent implementation; this library's reusable pieces do
not imply a Strategy migration or a shared business runtime entry point.

## Data API

```go
stream, err := transport.EncodeArrowStream(table)
file, err := transport.EncodeArrowFile(table)
store := snapshot.NewStore(root)
handle, err := store.AcquireArrow(ctx, key, table)
mapped, err := store.Open(ctx, handle)
defer mapped.Close()
// mapped.Reader().RecordBatchAt(i) is backed by read-only mmap bytes.
```

Use Arrow stream for one worker's frame payload. Use `AcquireArrow` plus
`Store.Open` when multiple workers consume the same immutable table. Release
records before closing the mapping, then release the snapshot handle.

The Python counterpart is under `python/moox_pyruntime`. It uses standard
`pyarrow.ipc` and `pyarrow.memory_map`, so Go and Python do not need a private
serialization format. Install `python/requirements.txt` only for workers that
negotiate Arrow; JSON remains the explicit fallback when `pyarrow` is absent.

Run package tests:

```bash
go test ./...
GOWORK=off go test -race ./...
PYTHONPATH=python python3 -m pytest python/tests -q
```
