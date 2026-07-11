"""Small Python counterpart of the moox Python runtime data contract.

The package intentionally keeps pyarrow optional at import time. A worker that
negotiates ``arrow_stream`` or ``arrow_mmap`` must install pyarrow; JSON-only
workers can still import the runtime without that dependency.
"""

from .arrow import decode_stream, encode_stream, open_mmap
from .capture import CapturedOutput, capture_output
from .protocol import read_frame, write_frame

__all__ = [
    "CapturedOutput",
    "capture_output",
    "decode_stream",
    "encode_stream",
    "open_mmap",
    "read_frame",
    "write_frame",
]
