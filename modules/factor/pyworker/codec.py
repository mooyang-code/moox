import json

import numpy as np
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


def encode_json_results(task_id, results, logs=None):
    response = {
        "id": task_id,
        "ok": True,
        "encoding": "json",
        "results": {
            name: [_json_value(v) for v in list(values)]
            for name, values in results.items()
        },
    }
    if logs:
        response["logs"] = logs
    return response


def _json_value(value):
    if pd.isna(value):
        return None
    if hasattr(value, "item"):
        value = value.item()
    if isinstance(value, float) and not np.isfinite(value):
        return None
    return value


def _utc_ns(values):
    return pd.to_datetime(values, unit="ms", utc=True).astype("datetime64[ns, UTC]")
