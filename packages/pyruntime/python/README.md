# moox Python Arrow contract

`moox_pyruntime.arrow` is the Python side of the shared runtime's data
contract. It uses standard Apache Arrow IPC and does not define a private
wire format:

`moox_pyruntime.protocol` provides the shared frame codec and
`moox_pyruntime.capture.capture_output` bounds business stdout/stderr so logs
cannot corrupt the binary protocol.

| Runtime encoding | Go producer | Python consumer |
| --- | --- | --- |
| `arrow_stream` | `transport.EncodeArrowStream` | `decode_stream(payload)` |
| `arrow_mmap` | `snapshot.Store.AcquireArrow` + `Store.Open` | `open_mmap(path)` |

`arrow_stream` bytes include an IPC schema followed by one or more record
batches. `arrow_mmap` is an IPC **file** (not a stream), so workers open it
read-only with `pyarrow.memory_map(path, "r")`. The Go snapshot handle owns the
file lifetime; Python must finish consuming the reader before the handle is
released. The runtime only negotiates these encodings when `pyarrow` is
installed and reported by `HELLO`.

The conversion contract for the current `transport.Table` API is:

- Go `int*`/`uint*` -> Arrow `int64` (unsigned values must fit `int64`).
- Go `float*` -> Arrow `double`.
- Go `time.Time` -> Arrow `timestamp[ms, UTC]`.
- Go `bool` and `string` retain their Arrow types.
- `nil` is a nullable value; an all-null column uses Arrow `null` type.

Install `pyarrow` in the worker environment and set `PYTHONPATH` to this
directory when using the helper package directly.
