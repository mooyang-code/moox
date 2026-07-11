"""Canonical moox.py/v1 stdio frame helpers.

Go's ``protocol.Frame`` uses the following wire layout (big endian):
``MX`` magic, one-byte message type, uint32 JSON metadata length, metadata,
uint64 payload length, payload. Factor and strategy workers should import this
module instead of maintaining subtly different copies of the framing code.
"""

from __future__ import annotations

import json
import struct
from dataclasses import dataclass
from typing import BinaryIO

MAGIC = b"MX"
PROTOCOL_VERSION = "moox.py/v1"
TYPE_HELLO = 0x01
TYPE_LOAD = 0x02
TYPE_RUN = 0x03
TYPE_RESULT = 0x04
TYPE_ERROR = 0x05
TYPE_PING = 0x06
TYPE_DRAIN = 0x07

# Compatibility aliases for workers that used the old names. New code should
# use TYPE_* names so control messages cannot be confused with responses.
FRAME_READY = TYPE_HELLO
FRAME_LOAD = TYPE_LOAD
FRAME_RUN = TYPE_RUN
FRAME_RESULT = TYPE_RESULT
FRAME_ERROR = TYPE_ERROR
FRAME_PING = TYPE_PING
FRAME_DRAIN = TYPE_DRAIN


@dataclass(frozen=True)
class FrameLimits:
    max_meta_bytes: int = 4 * 1024 * 1024
    max_payload_bytes: int = 64 * 1024 * 1024
    max_frame_bytes: int = 68 * 1024 * 1024


DEFAULT_LIMITS = FrameLimits()


def _read_exact(stream: BinaryIO, size: int) -> bytes:
    chunks: list[bytes] = []
    remaining = size
    while remaining:
        chunk = stream.read(remaining)
        if not chunk:
            break
        chunks.append(chunk)
        remaining -= len(chunk)
    data = b"".join(chunks)
    if not data and size:
        raise EOFError("end of stream")
    if len(data) != size:
        raise ValueError(f"truncated frame: expected {size} bytes, got {len(data)}")
    return data


def read_frame(stream: BinaryIO, limits: FrameLimits = DEFAULT_LIMITS):
    """Read and decode one frame as ``(message_type, metadata, payload)``."""

    if _read_exact(stream, 2) != MAGIC:
        raise ValueError("invalid frame magic")
    message_type = _read_exact(stream, 1)[0]
    if message_type not in {TYPE_HELLO, TYPE_LOAD, TYPE_RUN, TYPE_RESULT, TYPE_ERROR, TYPE_PING, TYPE_DRAIN}:
        raise ValueError(f"unknown message type: {message_type}")
    meta_size = struct.unpack(">I", _read_exact(stream, 4))[0]
    if meta_size > limits.max_meta_bytes:
        raise ValueError("frame metadata exceeds configured limit")
    raw_meta = _read_exact(stream, meta_size)
    metadata = json.loads(raw_meta.decode("utf-8")) if raw_meta else {}
    payload_size = struct.unpack(">Q", _read_exact(stream, 8))[0]
    if payload_size > limits.max_payload_bytes:
        raise ValueError("frame payload exceeds configured limit")
    if meta_size + payload_size > limits.max_frame_bytes:
        raise ValueError("frame exceeds configured limit")
    payload = _read_exact(stream, payload_size) if payload_size else b""
    return message_type, metadata, payload


def write_frame(
    stream: BinaryIO,
    message_type: int,
    metadata: dict,
    payload: bytes = b"",
    limits: FrameLimits = DEFAULT_LIMITS,
) -> None:
    """Encode one frame and flush it to the worker pipe."""

    raw_meta = json.dumps(metadata, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if len(raw_meta) > limits.max_meta_bytes:
        raise ValueError("frame metadata exceeds configured limit")
    if len(payload) > limits.max_payload_bytes:
        raise ValueError("frame payload exceeds configured limit")
    if len(raw_meta) + len(payload) > limits.max_frame_bytes:
        raise ValueError("frame exceeds configured limit")
    stream.write(MAGIC)
    stream.write(bytes([message_type]))
    stream.write(struct.pack(">I", len(raw_meta)))
    stream.write(raw_meta)
    stream.write(struct.pack(">Q", len(payload)))
    if payload:
        stream.write(payload)
    stream.flush()
