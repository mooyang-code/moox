"""Ranked mean of positive-bar amplitude, following XBX ZfAbsMean."""

import numpy as np
import pandas as pd


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("ZfAbsMean requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("ZfAbsMean output_names length must match windows")
        return list(names)
    return [f"zf_abs_mean_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("ZfAbsMean windows must be positive")
    grouped = df.groupby("series_tag", sort=False)
    # XBX determines whether a bar is positive from the typical price, not
    # the close alone. Keep this calculation grouped by series to prevent
    # venue rows from leaking into one another.
    typical_price = (df["close"] + df["high"] + df["low"]) / 3
    change = typical_price.groupby(df["series_tag"], sort=False).pct_change()
    amplitude = (df["high"] - df["low"]) / df["open"]
    amplitude = pd.Series(np.where(change > 0, amplitude, 0), index=df.index)
    output = df[["data_time", "series_tag"]].copy()
    amp_group = amplitude.groupby(df["series_tag"], sort=False)
    for name, window in zip(_names(params, windows), windows):
        mean = amp_group.transform(lambda values: values.rolling(window, min_periods=1).mean())
        output[name] = mean.groupby(df["series_tag"], sort=False).transform(
            lambda values: values.rolling(window, min_periods=1).rank(ascending=True, pct=True)
        )
    return output