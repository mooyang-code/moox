from decimal import Decimal, InvalidOperation

class ContractError(ValueError):
    pass

def validate_output(value):
    if not isinstance(value, dict) or value.get("action") not in ("hold", "rebalance"):
        raise ContractError("action must be hold or rebalance")
    if not isinstance(value.get("next_state"), dict):
        raise ContractError("next_state must be an object")
    seen=set()
    for target in value.get("targets", []):
        instrument=target.get("instrument_id")
        if not instrument or instrument in seen:
            raise ContractError("target instruments must be unique")
        try:
            Decimal(str(target.get("target_weight")))
        except (InvalidOperation, TypeError):
            raise ContractError("target_weight must be decimal")
        seen.add(instrument)
    return value
