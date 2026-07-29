"""Bounded stdout/stderr capture for Python strategy and factor adapters."""

from __future__ import annotations

from contextlib import contextmanager, redirect_stderr, redirect_stdout
from dataclasses import dataclass
from io import TextIOBase
from typing import Iterator


class _BoundedTextStream(TextIOBase):
    def __init__(self, limit_bytes: int):
        super().__init__()
        self._limit_bytes = limit_bytes
        self._buffer = bytearray()
        self.truncated = False

    @property
    def encoding(self) -> str:
        return "utf-8"

    @property
    def buffered_bytes(self) -> int:
        return len(self._buffer)

    def writable(self) -> bool:
        return True

    def write(self, value: str) -> int:
        if not isinstance(value, str):
            raise TypeError("write() argument must be str")
        remaining = self._limit_bytes - len(self._buffer)
        if remaining <= 0:
            if value:
                self.truncated = True
            return len(value)

        # A UTF-8 code point occupies at least one byte, so no more than
        # `remaining` characters can fit. Encoding only that prefix keeps the
        # temporary allocation bounded too.
        prefix = value[:remaining]
        raw = prefix.encode("utf-8")
        self._buffer.extend(raw[:remaining])
        if len(prefix) != len(value) or len(raw) > remaining:
            self.truncated = True
        return len(value)

    def flush(self) -> None:
        return None

    def getvalue(self) -> str:
        return bytes(self._buffer).decode("utf-8", errors="ignore")


@dataclass
class CapturedOutput:
    stdout: str = ""
    stderr: str = ""
    truncated: bool = False
    _stdout_stream: _BoundedTextStream | None = None
    _stderr_stream: _BoundedTextStream | None = None

    @property
    def buffered_bytes(self) -> int:
        streams = (self._stdout_stream, self._stderr_stream)
        return max(
            (stream.buffered_bytes for stream in streams if stream is not None),
            default=0,
        )


@contextmanager
def capture_output(limit_bytes: int = 64 * 1024) -> Iterator[CapturedOutput]:
    """Capture business logs without allowing them onto the protocol pipe.

    Truncation happens at UTF-8 byte boundaries and is reported explicitly so
    callers can include the flag in RESULT logs.
    """

    if limit_bytes <= 0:
        raise ValueError("limit_bytes must be positive")
    stdout = _BoundedTextStream(limit_bytes)
    stderr = _BoundedTextStream(limit_bytes)
    result = CapturedOutput(_stdout_stream=stdout, _stderr_stream=stderr)
    try:
        with redirect_stdout(stdout), redirect_stderr(stderr):
            yield result
    finally:
        result.stdout = stdout.getvalue()
        result.stderr = stderr.getvalue()
        result.truncated = stdout.truncated or stderr.truncated
