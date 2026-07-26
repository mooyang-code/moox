# MooX Python Worker Helpers

`moox_pyruntime.protocol` implements the shared `moox.py/v1` frame codec.
`moox_pyruntime.capture.capture_output` bounds business stdout and stderr so
worker logs cannot corrupt that binary protocol.

Business payloads use JSON. The helper package has no optional binary data
dependencies.
