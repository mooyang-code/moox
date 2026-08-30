"""Rolling percentile rank of rolling volume mean."""


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("VolumeMeanQ requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("VolumeMeanQ output_names length must match windows")
        return list(names)
    return [f"volume_mean_q_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("VolumeMeanQ windows must be positive")
    grouped = df.groupby("series_tag", sort=False)["volume"]
    output = df[["data_time", "series_tag"]].copy()
    for name, window in zip(_names(params, windows), windows):
        mean = grouped.transform(lambda values: values.rolling(window, min_periods=1).mean())
        output[name] = mean.groupby(df["series_tag"], sort=False).transform(
            lambda values: values.rolling(window, min_periods=1).rank(ascending=True, pct=True)
        )
    return output