from dataclasses import dataclass
from typing import Any, Optional

@dataclass(frozen=True)
class TargetPosition:
    instrument_id: str
    symbol: str
    target_quantity: str
    reason: Optional[str] = None
    source_time: Optional[str] = None
    data_revision: Optional[str] = None

def empty_targets() -> list[dict[str, Any]]:
    return []
