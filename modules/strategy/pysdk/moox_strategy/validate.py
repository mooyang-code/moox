from decimal import Decimal, InvalidOperation
import re

class ContractError(ValueError):
    pass

_DECIMAL = re.compile(r"^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$")

def validate_output(value):
    if not isinstance(value, dict) or value.get("action") not in ("hold", "rebalance"):
        raise ContractError("action must be hold or rebalance")
    if not isinstance(value.get("next_state"), dict):
        raise ContractError("next_state must be an object")
    targets = value.get("targets", [])
    if not isinstance(targets, list):
        raise ContractError("targets must be a list")
    seen=set()
    for target in targets:
        if not isinstance(target, dict):
            raise ContractError("target must be an object")
        instrument=target.get("instrument_id")
        if not instrument or instrument in seen:
            raise ContractError("target instruments must be unique")
        try:
            raw_weight = str(target.get("target_weight"))
            if not _DECIMAL.fullmatch(raw_weight):
                raise ContractError("target_weight must be decimal")
            weight = Decimal(raw_weight)
            if not weight.is_finite():
                raise ContractError("target_weight must be finite")
        except (InvalidOperation, TypeError):
            raise ContractError("target_weight must be decimal")
        seen.add(instrument)
    return value
