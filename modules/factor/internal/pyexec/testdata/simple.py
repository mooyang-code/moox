import pandas as pd


def compute(df, params, context):
    result = df[["data_time", "series_tag"]].copy()
    result["value_out"] = df["value"] * params.get("scale", 1)
    return result
