"""Rolling mean of quote volume."""


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("QuoteVolumeMean requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("QuoteVolumeMean output_names length must match windows")
        return list(names)
    return [f"quote_volume_mean_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("QuoteVolumeMean windows must be positive")
    grouped = df.groupby("series_tag", sort=False)["quote_volume"]
    output = df[["data_time", "series_tag"]].copy()
    for name, window in zip(_names(params, windows), windows):
        output[name] = grouped.transform(lambda values: values.rolling(window, min_periods=1).mean())
    return output