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
    df = pd.DataFrame(spec.get("columns", {}))
    data_times = spec.get("data_times", [])
    if len(data_times) != len(df.index):
        raise ValueError("data_times length must match dataframe rows")
    df.insert(
        0,
        "data_time",
        pd.to_datetime(data_times, format="ISO8601", utc=True),
    )
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
