"""Rolling percentile rank of XBX Bias."""


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("BiasQ requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("BiasQ output_names length must match windows")
        return list(names)
    return [f"bias_q_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("BiasQ windows must be positive")
    output = df[["data_time", "series_tag"]].copy()
    grouped = df.groupby("series_tag", sort=False)["close"]
    for name, window in zip(_names(params, windows), windows):
        mean = grouped.transform(lambda values: values.rolling(window, min_periods=1).mean())
        bias = df["close"] / mean - 1
        output[name] = bias.groupby(df["series_tag"], sort=False).transform(
            lambda values: values.rolling(window, min_periods=1).rank(ascending=True, pct=True)
        )
    return output