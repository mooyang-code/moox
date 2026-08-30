def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    close = df.groupby("series_tag", sort=False)["close"]
    values = params.get("windows", params.get("window"))
    if isinstance(values, (int, float, str)):
        values = [values]
    if not values:
        raise ValueError("Bias requires window or windows")
    output_names = params.get("output_names")
    if output_names is not None and len(output_names) != len(values):
        raise ValueError("Bias output_names length must match windows")
    for index, raw_window in enumerate(values):
        window = int(raw_window)
        if window <= 0:
            raise ValueError("Bias windows must be positive")
        average = close.transform(
            lambda values: values.rolling(window, min_periods=1).mean()
        )
        name = output_names[index] if output_names is not None else f"bias_{window}"
        output[name] = df["close"] / average
    return output