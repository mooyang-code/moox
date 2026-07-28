from decimal import Decimal, InvalidOperation
import re

class ContractError(ValueError):
    pass

_DECIMAL = re.compile(r"^-?(0|[1-9][0-9]*)(\.[0-9]+)?$")

def validate_output(value):
    if not isinstance(value, dict) or value.get("action") not in ("hold", "rebalance"):
        raise ContractError("action must be hold or rebalance")
    if not isinstance(value.get("next_state"), dict):
        raise ContractError("next_state must be an object")
    targets = value.get("targets", [])
    if not isinstance(targets, list):
        raise ContractError("targets must be a list")
    if value["action"] == "rebalance" and not targets:
        raise ContractError("rebalance targets are required")
    seen = set()
    for target in targets:
        if not isinstance(target, dict):
            raise ContractError("target must be an object")
        if "target_weight" in target:
            raise ContractError("target_weight is not supported")
        instrument = target.get("instrument_id")
        symbol = target.get("symbol")
        if (
            not isinstance(instrument, str)
            or not instrument.strip()
            or not isinstance(symbol, str)
            or not symbol.strip()
        ):
            raise ContractError("target instrument_id and symbol are required")
        if symbol in seen:
            raise ContractError("target symbols must be unique")
        try:
            raw_quantity = target.get("target_quantity")
            if (
                not isinstance(raw_quantity, str)
                or len(raw_quantity) > 256
                or not _DECIMAL.fullmatch(raw_quantity)
            ):
                raise ContractError("target_quantity must be decimal")
            quantity = Decimal(raw_quantity)
            if not quantity.is_finite():
                raise ContractError("target_quantity must be finite")
        except (InvalidOperation, TypeError):
            raise ContractError("target_quantity must be decimal")
        seen.add(symbol)
    return value
