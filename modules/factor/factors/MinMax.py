"""Normalised typical-price position from the XBX MinMax factor."""


def _windows(params):
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("MinMax requires window or windows")
    return [int(value) for value in values]


def _names(params, windows):
    names = params.get("output_names")
    if names is not None:
        if len(names) != len(windows):
            raise ValueError("MinMax output_names length must match windows")
        return list(names)
    return [f"minmax_{window}" for window in windows]


def compute(df, params):
    windows = _windows(params)
    if any(window <= 0 for window in windows):
        raise ValueError("MinMax windows must be positive")
    typical = (df["high"] + df["low"] + df["close"]) / 3
    grouped = typical.groupby(df["series_tag"], sort=False)
    output = df[["data_time", "series_tag"]].copy()
    for name, window in zip(_names(params, windows), windows):
        minimum = grouped.transform(lambda values: values.rolling(window, min_periods=1).min())
        maximum = grouped.transform(lambda values: values.rolling(window, min_periods=1).max())
        output[name] = (typical - minimum) / (maximum - minimum) - 0.5
    return output