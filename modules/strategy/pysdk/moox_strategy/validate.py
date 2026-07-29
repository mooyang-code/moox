from decimal import Decimal, InvalidOperation
import json
import re


class ContractError(ValueError):
    pass


_DECIMAL = re.compile(r"^-?(0|[1-9][0-9]*)(\.[0-9]+)?$")
_OUTPUT_FIELDS = {"action", "targets", "debug_info"}
_TARGET_FIELDS = {"instrument_id", "quantity"}


def validate_output(value):
    if not isinstance(value, dict):
        raise ContractError("strategy output must be an object")
    unknown_output_fields = set(value) - _OUTPUT_FIELDS
    if unknown_output_fields:
        raise ContractError(f"unknown output fields: {sorted(unknown_output_fields)}")
    if value.get("action") not in ("hold", "rebalance"):
        raise ContractError("action must be hold or rebalance")
    targets = value.get("targets", [])
    if not isinstance(targets, list):
        raise ContractError("targets must be a list")
    debug_info = value.get("debug_info", {})
    if not isinstance(debug_info, dict):
        raise ContractError("debug_info must be an object")
    try:
        debug_info_bytes = json.dumps(
            debug_info,
            separators=(",", ":"),
            ensure_ascii=False,
        ).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise ContractError("debug_info must be JSON serializable") from exc
    if len(debug_info_bytes) > 16 * 1024:
        raise ContractError("debug_info exceeds 16384 bytes")
    seen = set()
    for target in targets:
        if not isinstance(target, dict):
            raise ContractError("target must be an object")
        unknown_target_fields = set(target) - _TARGET_FIELDS
        if unknown_target_fields:
            raise ContractError(f"unknown target fields: {sorted(unknown_target_fields)}")
        instrument_id = target.get("instrument_id")
        if (
            not isinstance(instrument_id, str)
            or not instrument_id.strip()
            or instrument_id != instrument_id.strip()
        ):
            raise ContractError("target instrument_id is required without surrounding whitespace")
        if instrument_id in seen:
            raise ContractError("target instrument_id values must be unique")
        quantity = target.get("quantity")
        if (
            not isinstance(quantity, str)
            or len(quantity) > 256
            or not _DECIMAL.fullmatch(quantity)
        ):
            raise ContractError("quantity must be a canonical decimal string")
        try:
            if not Decimal(quantity).is_finite():
                raise ContractError("quantity must be finite")
        except (InvalidOperation, TypeError):
            raise ContractError("quantity must be a canonical decimal string")
        seen.add(instrument_id)
    return value
