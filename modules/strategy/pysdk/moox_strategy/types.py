from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class InstrumentTarget:
    instrument_id: str
    quantity: str


def empty_targets() -> list[dict[str, Any]]:
    return []
