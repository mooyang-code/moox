"""Signed-amplitude rolling standard deviation, following XBX ZfStd."""

import numpy as np
import pandas as pd


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("ZfStd requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("ZfStd output_names length must match windows")
        return list(names)
    return [f"zf_std_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("ZfStd windows must be positive")
    grouped = df.groupby("series_tag", sort=False)
    momentum = grouped["close"].pct_change()
    amplitude = (df["high"] - df["low"]) / df["open"]
    signed = pd.Series(np.where(momentum > 0, amplitude, -amplitude), index=df.index)
    signed_group = signed.groupby(df["series_tag"], sort=False)
    output = df[["data_time", "series_tag"]].copy()
    for name, window in zip(_names(params, windows), windows):
        output[name] = signed_group.transform(lambda values: values.rolling(window, min_periods=1).std())
    return output