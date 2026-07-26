import json

import pandas as pd

from moox_pyruntime.protocol import (
    TYPE_ERROR,
    TYPE_HELLO,
    TYPE_LOAD,
    TYPE_RESULT,
    TYPE_RUN,
    read_frame,
    write_frame,
)


def decode_json_df(meta):
    spec = meta.get("df", {})
    columns = spec.get("columns", {})
    if isinstance(columns, dict):
        df = pd.DataFrame(columns)
    else:
        names = list(columns)
        df = pd.DataFrame(spec.get("rows", []), columns=names)
    index_ms = spec.get("index_ms")
    if index_ms is True and "candle_begin_time" in df.columns:
        df["candle_begin_time"] = _utc_ns(df["candle_begin_time"])
    elif isinstance(index_ms, list):
        df.insert(0, "candle_begin_time", _utc_ns(index_ms))
    return df


def encode_json_results(task_id, results, tail, per_factor_ms, elapsed_ms, result_tails=None, logs=None):
    encoded = {}
    for name, values in results.items():
        item_tail = int((result_tails or {}).get(name, tail))
        encoded[name] = {"tail": item_tail, "values": [_json_value(v) for v in list(values)[-item_tail:]]}
    response = {
        "id": task_id,
        "ok": True,
        "encoding": "json",
        "results": encoded,
        "per_factor_ms": per_factor_ms,
        "elapsed_ms": elapsed_ms,
    }
    if logs:
        response["logs"] = logs
    return response


def _json_value(value):
    if pd.isna(value):
        return None
    if hasattr(value, "item"):
        return value.item()
    return value


def _utc_ns(values):
    return pd.to_datetime(values, unit="ms", utc=True).astype("datetime64[ns, UTC]")
