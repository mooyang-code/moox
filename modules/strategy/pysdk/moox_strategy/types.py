from dataclasses import dataclass
from typing import Any

@dataclass(frozen=True)
class TargetWeight:
    instrument_id: str
    target_weight: str

def empty_targets() -> list[dict[str, Any]]:
    return []
