"""Small Python counterpart of the moox Python worker protocol."""

from .capture import CapturedOutput, capture_output
from .protocol import read_frame, write_frame

__all__ = [
    "CapturedOutput",
    "capture_output",
    "read_frame",
    "write_frame",
]
