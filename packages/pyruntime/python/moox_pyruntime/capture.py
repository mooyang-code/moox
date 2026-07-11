"""Bounded stdout/stderr capture for Python strategy and factor adapters."""

from __future__ import annotations

from contextlib import contextmanager, redirect_stderr, redirect_stdout
from dataclasses import dataclass
from io import StringIO
from typing import Iterator


@dataclass
class CapturedOutput:
    stdout: str = ""
    stderr: str = ""
    truncated: bool = False


@contextmanager
def capture_output(limit_bytes: int = 64 * 1024) -> Iterator[CapturedOutput]:
    """Capture business logs without allowing them onto the protocol pipe.

    Truncation happens at UTF-8 byte boundaries and is reported explicitly so
    callers can include the flag in RESULT logs.
    """

    if limit_bytes <= 0:
        raise ValueError("limit_bytes must be positive")
    stdout, stderr = StringIO(), StringIO()
    result = CapturedOutput()
    try:
        with redirect_stdout(stdout), redirect_stderr(stderr):
            yield result
    finally:
        result.stdout, out_truncated = _bounded(stdout.getvalue(), limit_bytes)
        result.stderr, err_truncated = _bounded(stderr.getvalue(), limit_bytes)
        result.truncated = out_truncated or err_truncated


def _bounded(value: str, limit_bytes: int) -> tuple[str, bool]:
    raw = value.encode("utf-8")
    if len(raw) <= limit_bytes:
        return value, False
    # Decode with ignore so a multibyte character cannot be split in the
    # externally visible log string.
    return raw[:limit_bytes].decode("utf-8", errors="ignore"), True
