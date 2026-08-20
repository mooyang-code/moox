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
    columns = spec.get("columns")
    rows = spec.get("rows")
    if not isinstance(columns, list) or columns[:2] != ["data_time", "series_tag"]:
        raise ValueError("dataframe columns must start with data_time, series_tag")
    if len(set(columns)) != len(columns):
        raise ValueError("dataframe columns must be unique")
    if not isinstance(rows, list):
        raise ValueError("dataframe rows must be an array")
    if any(not isinstance(row, list) or len(row) != len(columns) for row in rows):
        raise ValueError("dataframe row width must match columns")
    df = pd.DataFrame(rows, columns=columns)
    df["data_time"] = pd.to_datetime(df["data_time"], format="ISO8601", utc=True)
    _validate_series_tags(df["series_tag"])
    if df.duplicated(["data_time", "series_tag"]).any():
        raise ValueError("dataframe contains duplicate data_time, series_tag")
    if not df.sort_values(["data_time", "series_tag"], kind="stable").index.equals(df.index):
        raise ValueError("dataframe must be sorted by data_time, series_tag")
    return df


def encode_json_results(task_id, result, logs=None):
    response = {"id": task_id, "ok": True, "encoding": "json", "results": encode_result_rows(result)}
    if logs:
        response["logs"] = logs
    return response


def encode_result_rows(result):
    return [
        {
            "data_time": row["data_time"].isoformat().replace("+00:00", "Z"),
            "series_tag": row["series_tag"],
            "values": {
                name: _json_value(row[name])
                for name in result.columns
                if name not in {"data_time", "series_tag"}
            },
        }
        for _, row in result.iterrows()
    ]


def encode_json_batch_results(batch_id, items):
    return {
        "id": batch_id,
        "ok": True,
        "encoding": "json",
        "items": items,
    }


def _json_value(value):
    if pd.isna(value):
        return None
    if hasattr(value, "item"):
        value = value.item()
    if isinstance(value, float) and not np.isfinite(value):
        return None
    return value


def _validate_series_tags(tags):
    for tag in tags:
        if not isinstance(tag, str):
            raise TypeError("series_tag must be a string")
        if len(tag.encode("utf-8")) > 128:
            raise ValueError("series_tag must not exceed 128 bytes")
        if tag.strip() != tag:
            raise ValueError("series_tag must not have leading or trailing whitespace")
        if any(ord(char) < 0x20 or ord(char) == 0x7F for char in tag):
            raise ValueError("series_tag must not contain ASCII control characters")
