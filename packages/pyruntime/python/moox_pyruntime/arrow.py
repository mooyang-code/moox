"""Cross-language Arrow IPC helpers for moox.py/v1.

The Go runtime writes standard Arrow IPC stream/file bytes. Stream payloads are
decoded from a frame payload; mmap snapshots are read-only Arrow IPC *files*
opened by ``pa.memory_map``. No custom serialization is hidden in this module.
"""

from __future__ import annotations

from contextlib import contextmanager
from io import BytesIO
from pathlib import Path
from typing import Iterator


def _pa():
    try:
        import pyarrow as pa
    except ImportError as exc:  # pragma: no cover - exercised in deployments
        raise RuntimeError("pyarrow is required for Arrow runtime encodings") from exc
    return pa


def decode_stream(payload: bytes):
    """Decode one Arrow IPC stream payload into a ``pyarrow.Table``."""

    pa = _pa()
    return pa.ipc.open_stream(BytesIO(payload)).read_all()


def encode_stream(table) -> bytes:
    """Encode a ``pyarrow.Table`` as one Arrow IPC stream payload."""

    pa = _pa()
    out = BytesIO()
    with pa.ipc.new_stream(out, table.schema) as writer:
        writer.write_table(table)
    return out.getvalue()


@contextmanager
def open_mmap(path: str | Path) -> Iterator[object]:
    """Open a read-only Arrow IPC file through the OS memory-map API.

    The yielded object is a ``pyarrow.ipc.RecordBatchFileReader``. Keep the
    context open while consuming record batches; closing it releases the file
    descriptor and mapping.
    """

    pa = _pa()
    source = pa.memory_map(str(path), "r")
    reader = pa.ipc.open_file(source)
    try:
        yield reader
    finally:
        # RecordBatchFileReader has no stable close method across pyarrow
        # versions; closing the memory map is the portable lifetime boundary.
        close = getattr(source, "close", None)
        if close is not None:
            close()
